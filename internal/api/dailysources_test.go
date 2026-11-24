package api_test

import (
	"fmt"
	"testing"
	"time"
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
		code, p := x.adm("POST", "/api/admin/daily/lines/preview", map[string]any{"text": "a — B C\nno split here", "format": "lines"})
		if code != 200 || fmt.Sprint(p["lines"]) != "[map[author:B C text:a] map[text:no split here]]" {
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

func TestSourcesAreReadFromOlderConfigsAndChecked(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		x.fetcher.SetTransport(&twoServices{})
		x.enableAll()
		x.putLines("", "Ours", "lines")
		// A single source, an id, and a list are all lists.
		for name, v := range map[string]any{"one source": map[string]any{"kind": "list"}, "an id": "favqs", "ids": []any{"favqs"}, "sources": []any{map[string]any{"kind": "list"}}} {
			if code, r := x.setSources(map[string]any{"": v}); code != 200 {
				t.Errorf("%s: %d %v", name, code, r)
			}
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
