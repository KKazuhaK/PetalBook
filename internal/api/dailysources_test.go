package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
)

func (x *extFixture) putLines(lang, text, format string) {
	x.t.Helper()
	if code, r := x.adm("PUT", "/api/admin/daily/lines", map[string]any{"lang": lang, "text": text, "format": format}); code != 200 {
		x.t.Fatalf("%d %v", code, r)
	}
}

func (x *extFixture) setSources(sources map[string]any) (int, map[string]any) {
	return x.adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "sources", "sources": sources}})
}

func TestLinesKeepTheirAuthorAndSource(t *testing.T) {
	t.Parallel()
	withExternal(t, func(x *extFixture) {
		t := x.t
		x.putLines("", "人到情多情转薄，而今真个不多情。 —— 纳兰性德《山花子》\nThe best way out is always through. — Robert Frost\nI think — therefore I am\nPlain line\nWith tab\tA Book", "lines")
		_, g := x.adm("GET", "/api/admin/daily/lines?lang=", nil)
		got := fmt.Sprint(g["lines"])
		want := "[map[author:纳兰性德 source:山花子 text:人到情多情转薄，而今真个不多情。] map[author:Robert Frost text:The best way out is always through.] map[text:I think — therefore I am] map[text:Plain line] map[source:A Book text:With tab]]"
		if got != want {
			t.Fatalf("%s", got)
		}
		// The preview splits the same way and keeps nothing.
		code, p := x.adm("POST", "/api/admin/daily/lines/preview", map[string]any{"text": "a. — B C\nno split here", "format": "lines"})
		if code != 200 || fmt.Sprint(p["lines"]) != "[map[author:B C text:a.] map[text:no split here]]" {
			t.Fatalf("%d %v", code, p)
		}
		_, g = x.adm("GET", "/api/admin/daily/lines?lang=", nil)
		if len(asSlice(g["lines"])) != 5 {
			t.Fatalf("a preview kept something: %v", g)
		}
		// CSV: text, author, source; the header is optional.
		for name, csv := range map[string]string{"with a header": "text,author,source\n\"One, two\",Seneca,Letters\nThree,,\n", "without": "\"One, two\",Seneca,Letters\nThree,,\n"} {
			code, p := x.adm("POST", "/api/admin/daily/lines/preview", map[string]any{"text": csv, "format": "csv"})
			if code != 200 || fmt.Sprint(p["lines"]) != "[map[author:Seneca source:Letters text:One, two] map[text:Three]]" {
				t.Errorf("%s: %d %v", name, code, p)
			}
		}
		// The administrators correct a split: the lines go in as they are.
		if code, r := x.adm("PUT", "/api/admin/daily/lines", map[string]any{"lang": "", "lines": []any{map[string]any{"text": "He said — no", "author": "Someone", "source": "Somewhere"}}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		x.adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "list"}})
		q := asMap(quoteOf(t, x.own, x.base, "date=2026-11-23&lang=en")["quote"])
		if q["text"] != "He said — no" || q["author"] != "Someone" || q["source"] != "Somewhere" {
			t.Fatalf("%v", q)
		}
		if code, _ := x.adm("PUT", "/api/admin/daily/lines", map[string]any{"lang": "", "text": "a", "format": "xml"}); code != 400 {
			t.Errorf("a format that is none: %d", code)
		}
	})
}

func TestSourcesOfSeveralKindsTakeTurnsAndFallBack(t *testing.T) {
	t.Parallel()
	withExternal(t, func(x *extFixture) {
		t := x.t
		svc := &twoServices{}
		x.fetcher.SetTransport(svc)
		x.enableAll()
		x.putLines("", "Ours —— Someone《A Book》", "lines")
		if code, r := x.setSources(map[string]any{"": []any{map[string]any{"kind": "api", "id": "favqs"}, map[string]any{"kind": "list"}}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		ask := func(c caller, base, query string) map[string]any {
			x.now = x.now.Add(2 * time.Second)
			return quoteOf(t, c, base, query)
		}
		d22, d23, d24 := ask(x.own, x.base, "date=2026-11-22&lang=en"), ask(x.own, x.base, "date=2026-11-23&lang=en"), ask(x.own, x.base, "date=2026-11-24&lang=en")
		if quoteText(d22) == quoteText(d23) || quoteText(d22) != quoteText(d24) {
			t.Fatalf("%q %q %q", quoteText(d22), quoteText(d23), quoteText(d24))
		}
		// Whichever it is, it is one item, in the same fields: text, author, source, link.
		for _, r := range []map[string]any{d22, d23} {
			q := asMap(r["quote"])
			switch q["text"] {
			case "Ours":
				if q["author"] != "Someone" || q["source"] != "A Book" || r["origin"] != "site" {
					t.Errorf("%v", r)
				}
			case "fav":
				if q["author"] != "Buddha" || q["link"] != "https://favqs.com/quotes/buddha/1" || r["origin"] != "external" {
					t.Errorf("%v", r)
				}
			default:
				t.Errorf("%v", r)
			}
			if r["page"] != nil {
				t.Errorf("one item only: %v", r)
			}
		}
		// Everybody in the scope sees the same; "another" moves to the next kind.
		a0 := quoteText(ask(x.viewer, x.base, "date=2026-11-23&lang=en"))
		a1 := quoteText(ask(x.own, x.base, "date=2026-11-23&lang=en&skip=1"))
		if a0 != quoteText(d23) || a1 == a0 || a1 == "" {
			t.Fatalf("%q %q", a0, a1)
		}
		// The interface down: our lines, on the days it would have had.
		svc.fail("favqs.com", true)
		for _, day := range []string{"2026-11-22", "2026-11-23", "2026-11-24"} {
			x.adm("PUT", "/api/admin/daily/providers/favqs", map[string]any{"enabled": true, "rps": 1})
			x.now = x.now.Add(time.Hour)
			if got := quoteText(quoteOf(t, x.own, x.base, "date="+day+"&lang=en")); got != "Ours" && got != "fav" {
				t.Errorf("%s: %q", day, got)
			}
		}
		x.adm("DELETE", "/api/admin/daily/lines?lang=", nil)
		// With no lines, and the interface down, there is nothing; with it up, it is the interface, every day.
		x.now = x.now.Add(48 * time.Hour)
		svc.fail("favqs.com", false)
		x.now = x.now.Add(time.Hour)
		for _, day := range []string{"2026-11-25", "2026-11-26"} {
			x.now = x.now.Add(2 * time.Second)
			if r := quoteOf(t, x.own, x.base, "date="+day+"&lang=en"); r["quote"] != nil && quoteText(r) != "fav" {
				t.Errorf("%s: %v", day, r)
			}
		}
	})
}

func TestFallbackFromAnInterfaceToOurLines(t *testing.T) {
	t.Parallel()
	withExternal(t, func(x *extFixture) {
		t := x.t
		svc := &twoServices{}
		x.fetcher.SetTransport(svc)
		x.enableAll()
		x.putLines("", "Ours", "lines")
		x.setSources(map[string]any{"": []any{map[string]any{"kind": "api", "id": "favqs"}, map[string]any{"kind": "list"}}})
		svc.fail("favqs.com", true)
		for _, day := range []string{"2026-11-22", "2026-11-23", "2026-11-24"} {
			x.now = x.now.Add(2 * time.Second)
			if got := quoteText(quoteOf(t, x.own, x.base, "date="+day+"&lang=en")); got != "Ours" {
				t.Errorf("%s: %q", day, got)
			}
		}
		// The other way round: the lines are empty, the interface answers.
		svc.fail("favqs.com", false)
		x.adm("DELETE", "/api/admin/daily/lines?lang=", nil)
		x.now = x.now.Add(time.Hour)
		for _, day := range []string{"2026-11-22", "2026-11-23", "2026-11-24"} {
			x.now = x.now.Add(2 * time.Second)
			if got := quoteText(quoteOf(t, x.own, x.base, "date="+day+"&lang=en")); got != "fav" {
				t.Errorf("%s: %q", day, got)
			}
		}
	})
}

func TestSourcesAreChecked(t *testing.T) {
	t.Parallel()
	withExternal(t, func(x *extFixture) {
		t := x.t
		x.fetcher.SetTransport(&twoServices{})
		x.enableAll()
		x.putLines("", "Ours", "lines")
		// Each language is a list of sources: a single source, an id or a list of ids is none.
		for name, v := range map[string]any{"one source": map[string]any{"kind": "list"}, "an id": "favqs", "ids": []any{"favqs"}} {
			if code, r := x.setSources(map[string]any{"": v}); code != 400 {
				t.Errorf("%s: %d %v", name, code, r)
			}
		}
		if code, r := x.setSources(map[string]any{"": []any{map[string]any{"kind": "list"}}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		_, r := x.adm("GET", "/api/admin/daily", nil)
		if got := fmt.Sprint(asMap(r["default"])["sources"]); got != "map[:[map[kind:list]]]" {
			t.Fatalf("%s", got)
		}
		// One alone is used as it is.
		if got := quoteText(quoteOf(t, x.own, x.base, "date=2026-11-23&lang=en")); got != "Ours" {
			t.Fatalf("%q", got)
		}
		for name, v := range map[string]any{"a kind that is none": []any{map[string]any{"kind": "rss"}}, "an interface that is none": []any{map[string]any{"kind": "api", "id": "nothing"}},
			"a space that is none": []any{map[string]any{"kind": "space", "space": "0000000000", "source": map[string]any{"tag": "x"}}}, "a space without a source": []any{map[string]any{"kind": "space", "space": x.tm.sid}},
			"the lines with an id": []any{map[string]any{"kind": "list", "id": "favqs"}}, "none": []any{}, "options of a list": []any{map[string]any{"kind": "list", "options": map[string]any{"a": []any{"b"}}}},
			"options not declared": []any{map[string]any{"kind": "api", "id": "hitokoto", "options": map[string]any{"categories": []any{"z"}}}}} {
			if code, r := x.setSources(map[string]any{"": v}); code != 400 && code != 404 {
				t.Errorf("%s: %d %v", name, code, r)
			}
		}
		// Options belong to a source, not to the default: a top-level one is refused, not stored.
		if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "sources", "sources": map[string]any{"": []any{map[string]any{"kind": "list"}}},
			"options": map[string]any{"hitokoto": map[string]any{"categories": []any{"z"}}}}}); code != 400 || r["error"] != "invalid" {
			t.Errorf("%d %v", code, r)
		}
		// A space of the administrators' choice is a source too, with its pages' author and source.
		if code, r := x.setSources(map[string]any{"": []any{map[string]any{"kind": "space", "space": x.tm.sid, "source": map[string]any{"folder": ""}}, map[string]any{"kind": "list"}}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		// Options go with their interface in the list.
		code, r := x.setSources(map[string]any{"zh-Hans": []any{map[string]any{"kind": "api", "id": "hitokoto", "options": map[string]any{"categories": []any{"k", "d"}}}}})
		if code != 200 || fmt.Sprint(asMap(asMap(r["default"])["sources"])) != "map[zh-Hans:[map[id:hitokoto kind:api options:map[categories:[d k]]]]]" {
			t.Fatalf("%d %v", code, r["default"])
		}
	})
}

// A space lists sources of several kinds as the other layers do: its own pages, the
// administrators' lines, an interface with options. Old choices (a list of ids) read as before.
func TestASpaceListsSourcesOfSeveralKinds(t *testing.T) {
	t.Parallel()
	withExternal(t, func(x *extFixture) {
		t := x.t
		svc := &twoServices{}
		x.fetcher.SetTransport(svc)
		x.enableAll()
		x.putLines("", "Ours", "lines")
		if code, r := x.own("POST", x.base+"/pages", map[string]any{"folder": "poems", "meta": map[string]any{"title": "山花子", "author": "纳兰性德"}, "body": "Mine\n"}); code != 201 {
			t.Fatalf("%d %v", code, r)
		}
		put := func(body map[string]any) (int, map[string]any) {
			body["inherit"] = true
			return x.own("PUT", x.base+"/daily/prefs", body)
		}
		own := map[string]any{"kind": "own", "source": map[string]any{"folder": "poems"}}
		for name, sources := range map[string]any{"a kind that is none": []any{map[string]any{"kind": "rss"}}, "an interface that is none": []any{map[string]any{"kind": "api", "id": "nothing"}},
			"a space of another": []any{map[string]any{"kind": "space", "space": x.tm.sid, "source": map[string]any{"folder": "poems"}}}, "pages without a source": []any{map[string]any{"kind": "own"}},
			"options not declared": []any{map[string]any{"kind": "api", "id": "hitokoto", "options": map[string]any{"categories": []any{"z"}}}}} {
			if code, r := put(map[string]any{"apis": []any{}, "sources": map[string]any{"": sources}}); code != 400 {
				t.Errorf("%s: %d %v", name, code, r)
			}
		}
		code, r := put(map[string]any{"apis": []any{}, "sources": map[string]any{"": []any{own, map[string]any{"kind": "list"}, map[string]any{"kind": "api", "id": "favqs"}}}})
		if code != 200 || len(asSlice(asMap(r["sources"])[""])) != 3 {
			t.Fatalf("%d %v", code, r)
		}
		seen := map[string]bool{}
		for _, day := range []string{"2026-11-22", "2026-11-23", "2026-11-24"} {
			x.now = x.now.Add(2 * time.Second)
			r := quoteOf(t, x.viewer, x.base, "date="+day+"&lang=en")
			q := asMap(r["quote"])
			seen[quoteText(r)] = true
			if r["page"] != nil || q == nil {
				t.Fatalf("one item only: %v", r)
			}
			if q["text"] == "Mine" && (q["author"] != "纳兰性德" || q["source"] != "山花子") {
				t.Errorf("%v", q)
			}
		}
		// Three kinds, three days: each had its turn.
		if len(seen) != 3 {
			t.Errorf("%v", seen)
		}
		// Fallback: the pages are gone (the folder has none), the interface down: the lines.
		if code, r := put(map[string]any{"apis": []any{}, "sources": map[string]any{"": []any{map[string]any{"kind": "own", "source": map[string]any{"tag": "nothing"}}, map[string]any{"kind": "api", "id": "favqs"}, map[string]any{"kind": "list"}}}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		svc.fail("favqs.com", true)
		for _, day := range []string{"2026-11-22", "2026-11-23", "2026-11-24"} {
			x.now = x.now.Add(2 * time.Second)
			if got := quoteText(quoteOf(t, x.viewer, x.base, "date="+day+"&lang=en")); got != "Ours" {
				t.Errorf("%s: %q", day, got)
			}
		}
		svc.fail("favqs.com", false)
		// The locks: with interfaces locked, the list that remains is used; both locked, the default.
		x.adm("PUT", "/api/admin/daily", map[string]any{"allow": map[string]any{"api": false}})
		x.now = x.now.Add(time.Hour)
		if got := quoteText(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en")); got != "Ours" {
			t.Errorf("locked interfaces: %q", got)
		}
		x.adm("PUT", "/api/admin/daily", map[string]any{"allow": map[string]any{"api": false, "own": false}})
		if r := quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en"); quoteText(r) != "Ours" {
			t.Errorf("%v", r)
		}
		// Old prefs, a list of ids, read as before and are returned without sources.
		x.adm("PUT", "/api/admin/daily", map[string]any{"allow": map[string]any{"api": true, "own": true}})
		x.choose("favqs")
		_, p := x.own("GET", x.base+"/daily/prefs", nil)
		if fmt.Sprint(p["apis"]) != "[favqs]" || p["sources"] != nil {
			t.Fatalf("%v", p)
		}
		if got := quoteText(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en")); got != "fav" {
			t.Errorf("%q", got)
		}
	})
}

// "Another" starts after the source that answered, not after the one the day started with: when
// the first of the day failed, the first "another" must not show what is already there.
func TestAnotherFollowsTheSourceThatAnswered(t *testing.T) {
	t.Parallel()
	withExternal(t, func(x *extFixture) {
		t := x.t
		svc := &twoServices{}
		x.fetcher.SetTransport(svc)
		x.enableAll()
		svc.fail("dummyjson.com", true)
		x.setSources(map[string]any{"": []any{map[string]any{"kind": "api", "id": "dummyjson"}, map[string]any{"kind": "api", "id": "favqs"}, map[string]any{"kind": "api", "id": "zenquotes"}}})
		ask := func(query string) string {
			x.now = x.now.Add(15 * time.Second)
			return quoteText(quoteOf(t, x.own, x.base, query))
		}
		// Three days start with each of the three in turn, one of them with the one that is down.
		for _, day := range []string{"2026-11-22", "2026-11-23", "2026-11-24"} {
			a0 := ask("date=" + day + "&lang=en")
			a1 := ask("date=" + day + "&lang=en&skip=1")
			a2 := ask("date=" + day + "&lang=en&skip=2")
			if a0 == "" || a1 == "" || a0 == a1 || a2 != a0 {
				t.Errorf("%s: %q %q %q", day, a0, a1, a2)
			}
		}
	})
}

// An interface the administrators switched off stays in what the space chose, marked, and a save
// from the settings page keeps it; one that no longer exists is left out, and the answer says so.
func TestASpacesSwitchedOffInterfaceIsKeptAndMarked(t *testing.T) {
	t.Parallel()
	withExternal(t, func(x *extFixture) {
		t := x.t
		x.fetcher.SetTransport(&twoServices{})
		x.enableAll()
		x.putLines("", "Ours", "lines")
		list := []any{map[string]any{"kind": "api", "id": "favqs"}, map[string]any{"kind": "list"}}
		if code, r := x.own("PUT", x.base+"/daily/prefs", map[string]any{"inherit": true, "apis": []any{}, "sources": map[string]any{"": list}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		x.adm("PUT", "/api/admin/daily/providers/favqs", map[string]any{"enabled": false})
		_, p := x.own("GET", x.base+"/daily/prefs", nil)
		got := asSlice(asMap(p["sources"])[""])
		if len(got) != 2 || asMap(got[0])["id"] != "favqs" || asMap(got[0])["available"] != false || asMap(got[0])["reason"] != "disabled" || asMap(p["unavailable"])["favqs"] != "disabled" {
			t.Fatalf("%v", p)
		}
		// Read: skipped, the lines answer.
		for _, day := range []string{"2026-11-22", "2026-11-23"} {
			if got := quoteText(quoteOf(t, x.viewer, x.base, "date="+day+"&lang=en")); got != "Ours" {
				t.Errorf("%s: %q", day, got)
			}
		}
		// Saved again as the page sends it back, with the marks: kept.
		if code, r := x.own("PUT", x.base+"/daily/prefs", map[string]any{"inherit": true, "apis": []any{}, "sources": p["sources"]}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		_, p = x.own("GET", x.base+"/daily/prefs", nil)
		if len(asSlice(asMap(p["sources"])[""])) != 2 {
			t.Fatalf("a save lost it: %v", p["sources"])
		}
		// But a space cannot newly choose one that is off.
		if code, _ := x.own("PUT", x.base+"/daily/prefs", map[string]any{"inherit": true, "apis": []any{}, "sources": map[string]any{"": []any{map[string]any{"kind": "api", "id": "zenquotes"}, map[string]any{"kind": "api", "id": "animechan"}}}}); code != 400 {
			t.Errorf("a switched-off interface was chosen: %d", code)
		}
		// Switched on again, it is used again.
		x.adm("PUT", "/api/admin/daily/providers/favqs", map[string]any{"enabled": true})
		_, p = x.own("GET", x.base+"/daily/prefs", nil)
		if asMap(asSlice(asMap(p["sources"])[""])[0])["available"] != nil || p["unavailable"] != nil {
			t.Fatalf("%v", p)
		}
	})
}

func TestAChosenInterfaceThatIsGoneIsToldAndNothingElseIsPicked(t *testing.T) {
	t.Parallel()
	withExternal(t, func(x *extFixture) {
		t := x.t
		x.fetcher.SetTransport(&twoServices{})
		x.enableAll()
		x.putLines("", "Ours", "lines")
		x.adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "list"}})
		ctx := context.Background()
		// What the space chose: an interface that is not set up any more.
		for name, raw := range map[string]string{"ids": `{"apis":["retired"]}`, "sources": `{"apis":[],"sources":{"":[{"kind":"api","id":"retired"}]}}`} {
			if err := store.SetSpaceDaily(ctx, x.a.st.Q(), x.tm.sid, store.SpaceDaily{Inherit: true, APIs: raw}); err != nil {
				t.Fatal(err)
			}
			_, p := x.own("GET", x.base+"/daily/prefs", nil)
			if fmt.Sprint(p["apis"]) != "[]" || asMap(p["unavailable"])["retired"] != "gone" {
				t.Fatalf("%s: %v", name, p)
			}
			// The space does not fall through to the default: no text, and why.
			r := quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en")
			if r["quote"] != nil || r["reason"] != "unavailable" {
				t.Errorf("%s: %v", name, r)
			}
		}
		// A list that still has something usable is used.
		if err := store.SetSpaceDaily(ctx, x.a.st.Q(), x.tm.sid, store.SpaceDaily{Inherit: true, APIs: `{"apis":[],"sources":{"":[{"kind":"api","id":"retired"},{"kind":"list"}]}}`}); err != nil {
			t.Fatal(err)
		}
		if got := quoteText(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en")); got != "Ours" {
			t.Errorf("%q", got)
		}
		// Choosing again clears it.
		x.choose("favqs")
		if _, p := x.own("GET", x.base+"/daily/prefs", nil); p["unavailable"] != nil {
			t.Errorf("%v", p)
		}
	})
}

// A bilingual interface serves the two languages it has, one text for each; a reader of another
// language is not given the Chinese side, and the next source answers.
func TestABilingualInterfaceSkipsLanguagesItDoesNotServe(t *testing.T) {
	t.Parallel()
	withExternal(t, func(x *extFixture) {
		t := x.t
		x.fetcher.SetTransport(&twoServices{})
		x.adm("PUT", "/api/admin/daily/providers/shanbay", map[string]any{"enabled": true})
		x.adm("PUT", "/api/admin/daily/providers/favqs", map[string]any{"enabled": true})
		// A default listing the two for every language, shanbay first on some days and favqs on others.
		x.setSources(map[string]any{"": []any{map[string]any{"kind": "api", "id": "shanbay"}, map[string]any{"kind": "api", "id": "favqs"}}})
		ask := func(lang, day string) string {
			x.now = x.now.Add(2 * time.Second)
			return quoteText(quoteOf(t, x.own, x.base, "date="+day+"&lang="+lang))
		}
		days := []string{"2026-11-22", "2026-11-23", "2026-11-24"}
		// French: shanbay is skipped, and favqs is English only: nothing, not the Chinese side.
		for _, day := range days {
			if got := ask("fr", day); got != "" {
				t.Errorf("fr on %s: %q", day, got)
			}
		}
		seen := map[string]bool{}
		for _, day := range days {
			seen[ask("en-GB", day)] = true
		}
		if !seen["Be kind."] {
			t.Errorf("en: %v", seen)
		}
		for _, lang := range []string{"zh-CN", "zh-Hant"} { // zh-Hant is matched to zh-Hans, the one Chinese there is
			seen := map[string]bool{}
			for _, day := range days {
				seen[ask(lang, day)] = true
			}
			if !seen["与人为善。"] || seen["Be kind."] {
				t.Errorf("%s: %v", lang, seen)
			}
		}
	})
}

// The daily text is in the reader's language only: no source for it, no text, and the lines or
// interfaces of another language are not given in its place.
func TestNoTextInAnotherLanguage(t *testing.T) {
	t.Parallel()
	withExternal(t, func(x *extFixture) {
		t := x.t
		x.fetcher.SetTransport(&twoServices{})
		x.enableAll()
		x.putLines("zh-Hans", "中文的一行", "lines")
		x.adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "list"}})
		none := func(what, query string) {
			t.Helper()
			if r := quoteOf(t, x.viewer, x.base, query); r["quote"] != nil {
				t.Errorf("%s: %v", what, r)
			}
		}
		// Lines in Chinese only: a Chinese reader has them (also zh-Hant, matched to zh-Hans), an English one none.
		for _, lang := range []string{"zh-CN", "zh-Hant"} {
			if got := quoteText(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang="+lang)); got != "中文的一行" {
				t.Errorf("%s: %q", lang, got)
			}
		}
		none("lines in zh given to en", "date=2026-11-23&lang=en")
		none("lines in zh given to fr", "date=2026-11-23&lang=fr")
		// Lines for any language ("All other languages") are given to a language with none of its own.
		x.putLines("", "For any language", "lines")
		if got := quoteText(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en")); got != "For any language" {
			t.Errorf("%q", got)
		}
		x.adm("DELETE", "/api/admin/daily/lines?lang=", nil)
		// Sources by language: a list for zh is not given to en, but one under "" is.
		x.adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "sources", "sources": map[string]any{"zh-Hans": []any{map[string]any{"kind": "list"}}}}})
		none("a zh list given to en", "date=2026-11-23&lang=en")
		// A space that chose an interface made for Chinese gives an English reader nothing of it.
		x.adm("PUT", "/api/admin/daily", map[string]any{"default": nil})
		x.choose("hitokoto")
		none("an interface for zh chosen by the space, given to en", "date=2026-11-23&lang=en")
		if got := quoteText(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=zh-CN")); got != "hito" {
			t.Errorf("the Chinese reader has it: %q", got)
		}
	})
}

// Hitokoto has Chinese only: it is never put under another language, and under "all other languages"
// it is not used for an English reader.
func TestHitokotoIsNeverUsedForEnglish(t *testing.T) {
	t.Parallel()
	withExternal(t, func(x *extFixture) {
		t := x.t
		x.fetcher.SetTransport(&twoServices{})
		x.enableAll()
		for name, sources := range map[string]map[string]any{"en": {"en": []any{map[string]any{"kind": "api", "id": "hitokoto"}}}, "fr, with others": {"fr": []any{map[string]any{"kind": "api", "id": "favqs"}, map[string]any{"kind": "api", "id": "hitokoto"}}}} {
			if code, r := x.setSources(sources); code != 400 {
				t.Errorf("%s: %d %v", name, code, r)
			}
		}
		if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "api", "apis": map[string]any{"en": []any{"hitokoto"}}}}); code != 400 {
			t.Errorf("the earlier kind: %d %v", code, r)
		}
		for _, lang := range []string{"zh-Hans", "zh-Hant"} {
			if code, r := x.setSources(map[string]any{lang: []any{map[string]any{"kind": "api", "id": "hitokoto"}}}); code != 200 {
				t.Errorf("%s: %d %v", lang, code, r)
			}
		}
		// Explicitly under "all other languages": the Chinese readers get it, the English one does not.
		if code, r := x.setSources(map[string]any{"": []any{map[string]any{"kind": "api", "id": "hitokoto"}}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		if got := quoteText(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=zh-CN")); got != "hito" {
			t.Errorf("%q", got)
		}
		if r := quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en"); r["quote"] != nil {
			t.Errorf("hitokoto for an English reader: %v", r)
		}
	})
}
