package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/dailyfetch"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// A quote service of the test's own: the text says which language and which time it was asked.
type quoteServer struct {
	*httptest.Server
	hits atomic.Int64
	fail atomic.Bool
	host string
	key  atomic.Value
}

func newQuoteServer(t *testing.T) *quoteServer {
	t.Helper()
	q := &quoteServer{}
	q.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := q.hits.Add(1)
		if q.fail.Load() {
			http.Error(w, "down", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		q.key.Store(r.Header.Get("X-Api-Key"))
		fmt.Fprintf(w, `{"data":[{"q":"quote %d in %s","a":"someone","s":"a book","id":"id%d"}]}`, n, r.URL.Query().Get("lang"), n)
	}))
	t.Cleanup(q.Close)
	q.host = strings.TrimPrefix(q.URL, "http://")
	return q
}

func (q *quoteServer) api(id, lang string) map[string]any {
	return map[string]any{"id": id, "name": "Quotes " + id, "lang": lang, "url": q.URL + "/q?lang={lang}&d={date}", "text": "data.0.q", "source": "data.0.s", "author": "data.0.a",
		"link": "https://example.org/q/{data.0.id}", "skipNew": true, "rps": 100}
}

func quoteOf(t *testing.T, c caller, base, query string) map[string]any {
	t.Helper()
	code, r := c("GET", base+"/daily?"+query, nil)
	if code != 200 {
		t.Fatalf("%s: %d %v", query, code, r)
	}
	return r
}

func quoteText(r map[string]any) string {
	q, _ := r["quote"].(map[string]any)
	if q == nil {
		return ""
	}
	return q["text"].(string)
}

func TestDailyFromTheAdministrators(t *testing.T) {
	qs := newQuoteServer(t)
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		// Each database gets a fetcher, a clock and a count of its own.
		qs.hits.Store(0)
		qs.fail.Store(false)
		fetcher := dailyfetch.NewForTests(api.NewDailyBacking(st))
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		fetcher.SetClock(func() time.Time { return now })
		needGit(t)
		a := newAcc(t, st, accOpts{dailyFetch: fetcher})
		boss := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid
		adm, own, viewer := asClient(boss), asClient(tm.owner), asClient(tm.viewer)
		const day = "date=2026-11-23"

		// Nothing is set anywhere.
		if r := quoteOf(t, viewer, base, day+"&lang=en"); r["page"] != nil || r["quote"] != nil || r["reason"] != "no_source" {
			t.Fatalf("%v", r)
		}
		// Only administrators reach the settings.
		for _, rt := range [][3]string{{"GET", "/api/admin/daily", ""}, {"PUT", "/api/admin/daily", "{}"}, {"PUT", "/api/admin/daily/lines", "{}"}, {"DELETE", "/api/admin/daily/lines", ""}, {"PUT", "/api/admin/daily/apis/x", "{}"}, {"DELETE", "/api/admin/daily/apis/x", ""}, {"POST", "/api/admin/daily/try", "{}"}} {
			for name, c := range map[string]*client{"owner": tm.owner, "viewer": tm.viewer} {
				if r := c.do(rt[0], rt[1], rt[2]); r.code != 403 {
					t.Errorf("%s %v: %d %s", name, rt, r.code, r.raw)
				}
			}
		}
		code, r := adm("GET", "/api/admin/daily", nil)
		if code != 200 || r["default"] != nil || r["hosts"] != nil || len(r["apis"].([]any)) != 0 || r["lines"].(map[string]any)["total"] != float64(0) {
			t.Fatalf("%d %v", code, r)
		}

		// --- the settings are checked
		for name, body := range map[string]map[string]any{
			"an unknown key":                 {"extra": 1},
			"a language that is none":        {"languages": []any{"cn"}},
			"a default of nowhere":           {"default": map[string]any{"kind": "space", "space": "0000000000", "source": map[string]any{"tag": "x"}}},
			"a default with no kind":         {"default": map[string]any{}},
			"a space default with no source": {"default": map[string]any{"kind": "space", "space": tm.sid}},
			"a list with a space":            {"default": map[string]any{"kind": "list", "space": tm.sid}},
			"a default of a bad source":      {"default": map[string]any{"kind": "space", "space": tm.sid, "source": map[string]any{"folder": "Nowhere"}}},
		} {
			if code, r := adm("PUT", "/api/admin/daily", body); code != 400 && code != 404 {
				t.Errorf("%s: %d %v", name, code, r)
			}
		}
		if _, r := adm("GET", "/api/admin/daily", nil); len(r["apis"].([]any)) != 0 || len(r["providers"].([]any)) != 8 {
			t.Fatalf("a refused change changed something: %v", r)
		}

		// --- the default: lines, by language
		put := func(lang, text, format string) (int, map[string]any) {
			return adm("PUT", "/api/admin/daily/lines", map[string]any{"lang": lang, "text": text, "format": format})
		}
		for _, bad := range []map[string]any{{"lang": "chinese!", "text": "x"}, {"lang": "en", "text": "  \n\n"}, {"lang": "en", "text": "x", "format": "html"}, {"lang": "en", "text": "x\x00y"}} {
			if code, _ := adm("PUT", "/api/admin/daily/lines", bad); code != 400 {
				t.Errorf("%v: %d", bad, code)
			}
		}
		if code, r := adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "list"}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		put("zh", "山色空蒙雨亦奇\t苏轼\n\n行到水穷处\n", "lines")
		put("en", "Stay hungry\tJobs\nStay foolish\nBe kind", "lines")
		code, r = put("", "For any language", "lines")
		if code != 200 || r["lines"].(map[string]any)["total"] != float64(6) || r["lines"].(map[string]any)["byLang"].(map[string]any)["en"] != float64(3) {
			t.Fatalf("%d %v", code, r)
		}
		zh := map[string]bool{"山色空蒙雨亦奇": true, "行到水穷处": true}
		seen := map[string]bool{}
		for _, d := range days(20) {
			r := quoteOf(t, viewer, base, "date="+d+"&lang=zh")
			txt := quoteText(r)
			if !zh[txt] || r["origin"] != "site" || r["page"] != nil {
				t.Fatalf("%s: %v", d, r)
			}
			seen[txt] = true
			// The same for everyone, and for the day.
			if quoteText(quoteOf(t, own, base, "date="+d+"&lang=zh")) != txt {
				t.Fatal("not the same")
			}
		}
		if len(seen) != 2 {
			t.Errorf("%v", seen)
		}
		if r := quoteOf(t, viewer, base, day+"&lang=zh"); r["quote"].(map[string]any)["source"] != "苏轼" && r["quote"].(map[string]any)["source"] != nil {
			t.Errorf("%v", r)
		}
		// A region follows its language; a language with none gets those for any; skip moves along the lines.
		if txt := quoteText(quoteOf(t, viewer, base, day+"&lang=zh-CN")); !zh[txt] {
			t.Errorf("zh-CN: %s", txt)
		}
		if txt := quoteText(quoteOf(t, viewer, base, day+"&lang=fr")); txt != "For any language" {
			t.Errorf("fr: %s", txt)
		}
		enSeen := map[string]bool{}
		for k := 0; k < 3; k++ {
			enSeen[quoteText(quoteOf(t, viewer, base, day+fmt.Sprintf("&lang=en&skip=%d", k)))] = true
		}
		if len(enSeen) != 3 {
			t.Errorf("skip over three lines: %v", enSeen)
		}
		// Taking the lines of a language away: it falls back.
		if code, r := adm("DELETE", "/api/admin/daily/lines?lang=en", nil); code != 200 || r["lines"].(map[string]any)["byLang"].(map[string]any)["en"] != nil {
			t.Fatalf("%d %v", code, r)
		}
		if txt := quoteText(quoteOf(t, viewer, base, day+"&lang=en")); txt != "For any language" {
			t.Errorf("en: %s", txt)
		}
		// Markdown: cut at the rules, or at the blank lines.
		put("en", "First one\nsecond line of it\n---\nSecond\n---\n\n", "markdown")
		put("de", "Eins\n\nZwei\n\n\nDrei", "markdown")
		if _, r := adm("GET", "/api/admin/daily", nil); r["lines"].(map[string]any)["byLang"].(map[string]any)["en"] != float64(2) || r["lines"].(map[string]any)["byLang"].(map[string]any)["de"] != float64(3) {
			t.Fatalf("%v", r)
		}
		// The space leaves the default.
		if code, r := own("GET", base+"/daily/prefs", nil); code != 200 || r["inherit"] != true || r["default"].(map[string]any)["kind"] != "list" {
			t.Fatalf("%d %v", code, r)
		}
		if code, r := viewer("PUT", base+"/daily/prefs", map[string]any{"inherit": false, "apis": []any{}}); code != 403 {
			t.Fatalf("a viewer chooses: %d %v", code, r)
		}
		if code, r := own("PUT", base+"/daily/prefs", map[string]any{"inherit": false}); code != 400 {
			t.Fatalf("a half choice: %d %v", code, r)
		}
		if code, r := own("PUT", base+"/daily/prefs", map[string]any{"inherit": false, "apis": []any{}}); code != 200 || r["inherit"] != false {
			t.Fatalf("%d %v", code, r)
		}
		if r := quoteOf(t, viewer, base, day+"&lang=zh"); r["quote"] != nil || r["reason"] != "no_source" {
			t.Fatalf("%v", r)
		}
		own("PUT", base+"/daily/prefs", map[string]any{"inherit": true, "apis": []any{}})

		// --- the default from a space: shown to people who are not in it
		sitePage := func(title, body string) {
			if code, r := adm("POST", "/api/spaces/"+tm.sid+"/pages", map[string]any{"folder": "Site", "meta": map[string]any{"title": title, "tags": []any{"lang-en"}}, "body": body}); code != 201 {
				t.Fatalf("%d %v", code, r)
			}
		}
		sitePage("One", "From the site space\n")
		if code, r := adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "space", "space": tm.sid,
			"source": map[string]any{"en": map[string]any{"folder": "Site"}, "zh": map[string]any{"tag": "nothing"}}}}); code != 200 || r["default"].(map[string]any)["kind"] != "space" {
			t.Fatalf("%d %v", code, r)
		}
		other := a.addUser("otto", pw, "member")
		otherSpace := other.personalSpace()
		r = quoteOf(t, asClient(other), "/api/spaces/"+otherSpace, day+"&lang=en")
		if q, _ := r["quote"].(map[string]any); q == nil || q["text"] != "From the site space\n" || q["source"] != "One" || r["origin"] != "site" || r["page"] != nil {
			t.Fatalf("%v", r)
		}
		// A language whose source meets no page: the block is hidden, with a reason.
		if r := quoteOf(t, asClient(other), "/api/spaces/"+otherSpace, day+"&lang=zh"); r["quote"] != nil || r["reason"] != "no_match" {
			t.Fatalf("%v", r)
		}

		// --- a space that is deleted takes its choice with it
		var n int
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM space_daily WHERE space_id = ?`, tm.sid).Scan(&n)
		if n != 1 {
			t.Fatalf("%d", n)
		}
		if code, _ := adm("DELETE", "/api/admin/spaces/"+tm.sid, nil); code != 200 {
			t.Fatal(code)
		}
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM space_daily WHERE space_id = ?`, tm.sid).Scan(&n)
		if n != 0 {
			t.Fatalf("%d", n)
		}
		// The default that was a space that is gone hides the block instead of failing.
		if r := quoteOf(t, asClient(other), "/api/spaces/"+otherSpace, day+"&lang=en"); r["quote"] != nil {
			t.Logf("%v", r)
		}
	})
}

// Without accounts there are no administrators, so no settings of theirs.
func TestSingleUserHasNoSiteDailySettings(t *testing.T) {
	f := newFixture(t)
	for _, c := range [][2]string{{"GET", "/api/admin/daily"}, {"PUT", "/api/admin/daily"}, {"PUT", "/api/admin/daily/lines"}, {"PUT", "/api/admin/daily/apis/x"}, {"POST", "/api/admin/daily/try"}, {"GET", base + "/daily/prefs"}, {"PUT", base + "/daily/prefs"}} {
		if code, r, raw := f.do(c[0], c[1], "{}"); code != 404 || r["error"] != "not_found" {
			t.Errorf("%v: %d %s", c, code, raw)
		}
	}
	if code, r, _ := f.do("GET", base+"/daily?date=2026-11-23&lang=en", nil); code != 200 || r["reason"] != "no_source" {
		t.Fatalf("%d %v", code, r)
	}
	// With no administrators there is nothing to lock: everything is in effect.
	if code, r, _ := f.do("GET", base+"/daily/settings", nil); code != 200 || r["inEffect"] != true {
		t.Fatalf("%d %v", code, r)
	}
}

func TestDailyLangIsChecked(t *testing.T) {
	f := newFixture(t)
	for _, l := range []string{"中文", "x", "en_US", "toolongcode", "en-", "cn", "english", "zh_CN"} {
		if code, _, raw := f.do("GET", base+"/daily?date=2026-11-23&lang="+url.QueryEscape(l), nil); code != 400 || !strings.Contains(raw, `"bad_language"`) {
			t.Errorf("%s: %d %s", l, code, raw)
		}
	}
	for _, l := range []string{"en", "zh-CN", "EN", "", "zh-Hans", "pt-br", "en-GB"} {
		if code, _, raw := f.do("GET", base+"/daily?date=2026-11-23&lang="+l, nil); code != 200 {
			t.Errorf("%s: %d %s", l, code, raw)
		}
	}
}

// A space's source per language, through the API.
func TestDailySettingsPerLanguageThroughTheAPI(t *testing.T) {
	f := newFixture(t)
	c := asFixture(f)
	for _, p := range []struct{ folder, title string }{{"zh", "中文"}, {"en", "English"}} {
		if code, r := c("POST", base+"/pages", map[string]any{"folder": p.folder, "meta": map[string]any{"title": p.title}, "body": "x\n"}); code != 201 {
			t.Fatalf("%d %v", code, r)
		}
	}
	code, r := c("PUT", base+"/daily/settings", map[string]any{"daily": map[string]any{"zh": map[string]any{"folder": "zh"}, "en-US": map[string]any{"folder": "en"}}})
	if code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	d := r["daily"].(map[string]any)
	if d["zh-Hans"].(map[string]any)["folder"] != "zh" || d["en-US"].(map[string]any)["folder"] != "en" || d["folder"] != nil {
		t.Fatalf("%v", r)
	}
	if code, g := c("GET", base+"/daily/settings", nil); code != 200 || fmt.Sprint(g) != fmt.Sprint(r) {
		t.Fatalf("%d %v", code, g)
	}
	for lang, title := range map[string]string{"zh": "中文", "zh-CN": "中文", "en-US": "English", "en": "English"} {
		_, r := c("GET", base+"/daily?date=2026-11-23&lang="+lang, nil)
		if r["page"] == nil || r["page"].(map[string]any)["title"] != title {
			t.Errorf("%s: %v", lang, r)
		}
	}
	for name, bad := range map[string]any{"a bad code": map[string]any{"chinese!": map[string]any{"folder": "zh"}}, "no language": map[string]any{"cn": map[string]any{"folder": "zh"}},
		"an underscore": map[string]any{"zh_CN": map[string]any{"folder": "zh"}}, "a source that is not": map[string]any{"zh": "folder"}} {
		code, r := c("PUT", base+"/daily/settings", map[string]any{"daily": bad})
		if code != 400 || (name != "a source that is not" && r["error"] != "bad_language") {
			t.Errorf("%s: %d %v", name, code, r)
		}
	}
}

// --- the external interfaces ---------------------------------------------------------------

type extFixture struct {
	t       *testing.T
	a       *acc
	tm      team
	qs      *quoteServer
	fetcher *dailyfetch.Fetcher
	now     time.Time
	adm     caller
	own     caller
	viewer  caller
	base    string
}

func (x *extFixture) clock() time.Time { return x.now }

func withExternal(t *testing.T, fn func(x *extFixture)) {
	qs := newQuoteServer(t)
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		needGit(t)
		qs.hits.Store(0)
		qs.fail.Store(false)
		x := &extFixture{t: t, qs: qs, now: time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)}
		x.fetcher = dailyfetch.NewForTests(api.NewDailyBacking(st))
		x.fetcher.SetClock(x.clock)
		x.a = newAcc(t, st, accOpts{dailyFetch: x.fetcher})
		boss := x.a.setup("kazuha", pw)
		x.tm = newTeam(t, x.a)
		x.base = "/api/spaces/" + x.tm.sid
		x.adm, x.own, x.viewer = asClient(boss), asClient(x.tm.owner), asClient(x.tm.viewer)
		fn(x)
	})
}

func (x *extFixture) putAPI(id string, body map[string]any) (int, map[string]any) {
	return x.adm("PUT", "/api/admin/daily/apis/"+id, body)
}

// choose makes the space use the interfaces.
func (x *extFixture) choose(ids ...string) {
	x.t.Helper()
	l := []any{}
	for _, id := range ids {
		l = append(l, id)
	}
	if code, r := x.own("PUT", x.base+"/daily/prefs", map[string]any{"inherit": true, "apis": l}); code != 200 {
		x.t.Fatalf("%d %v", code, r)
	}
}

func TestAdministratorsConfigureExternalInterfaces(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		qs := x.qs
		// An interface is made with a PUT of its own: anything may be set.
		body := qs.api("any", "")
		body["headers"] = []any{map[string]any{"name": "X-Api-Key", "value": "key-one"}}
		body["refresh"], body["refreshMinutes"] = "minutes", 15
		code, r := x.putAPI("any", body)
		if code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		apis := r["apis"].([]any)
		got := apis[0].(map[string]any)
		if len(apis) != 1 || got["id"] != "any" || got["refresh"] != "minutes" || got["refreshMinutes"] != float64(15) || got["skipNew"] != true || got["rps"] != float64(100) ||
			got["enabled"] != true || got["link"] != "https://example.org/q/{data.0.id}" {
			t.Fatalf("%v", got)
		}
		// The value of the header is never given back, only that it is set.
		if r["hosts"] != nil {
			t.Fatalf("there is no list of hosts: %v", r["hosts"])
		}
		if hv := got["headers"].([]any); len(hv) != 1 || fmt.Sprint(hv[0]) != "map[name:X-Api-Key set:true]" {
			t.Fatalf("%v", got["headers"])
		}
		if code, g := x.adm("GET", "/api/admin/daily", nil); code != 200 || strings.Contains(fmt.Sprint(g), "key-one") {
			t.Fatalf("the value of a header was given back: %d %v", code, g)
		}
		// ... and it is not kept in the clear in the database either.
		var raw string
		x.a.st.Q().QueryRow(context.Background(), `SELECT value FROM site_settings WHERE key = 'daily'`).Scan(&raw)
		if strings.Contains(raw, "key-one") || !strings.Contains(raw, "X-Api-Key") {
			t.Fatalf("%s", raw)
		}
		x.choose("any")
		first := quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en")
		if quoteText(first) != "quote 1 in en" || first["quote"].(map[string]any)["link"] != "https://example.org/q/id1" || qs.key.Load() != "key-one" {
			t.Fatalf("%v (header %v)", first, qs.key.Load())
		}
		// A change that leaves the header out of its value keeps it; one that leaves the header out removes it.
		body["headers"] = []any{map[string]any{"name": "x-api-key"}}
		body["name"] = "Renamed"
		if code, r := x.putAPI("any", body); code != 200 || r["apis"].([]any)[0].(map[string]any)["name"] != "Renamed" {
			t.Fatalf("%d %v", code, r)
		}
		qs.hits.Store(0)
		x.now = x.now.Add(16 * time.Minute) // a new period
		if quoteText(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en")) == "" || qs.key.Load() != "key-one" {
			t.Fatalf("the kept value was not used: %v", qs.key.Load())
		}
		body["headers"] = []any{map[string]any{"name": "X-Api-Key", "value": "key-two"}}
		x.putAPI("any", body)
		x.now = x.now.Add(16 * time.Minute)
		quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en")
		if qs.key.Load() != "key-two" {
			t.Fatalf("a replaced value was not used: %v", qs.key.Load())
		}
		body["headers"] = []any{}
		x.putAPI("any", body)
		x.now = x.now.Add(16 * time.Minute)
		quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en")
		if qs.key.Load() != "" {
			t.Fatalf("a header that was left out is still sent: %v", qs.key.Load())
		}
		// A header with no value to keep is refused; so is everything the rules refuse.
		bad := func(what string, mod func(map[string]any), want int) {
			b := qs.api("other", "")
			mod(b)
			if code, r := x.putAPI("other", b); code != want {
				t.Errorf("%s: %d %v", what, code, r)
			}
		}
		bad("a header with no value", func(b map[string]any) { b["headers"] = []any{map[string]any{"name": "X-New"}} }, 400)
		if code, r := x.putAPI("other", func() map[string]any {
			b := qs.api("other", "")
			b["headers"] = []any{map[string]any{"name": "X-New"}}
			return b
		}()); code != 400 || r["error"] != "header_missing" || r["header"] != "X-New" {
			t.Errorf("%d %v", code, r)
		}
		bad("a forbidden header", func(b map[string]any) { b["headers"] = []any{map[string]any{"name": "Host", "value": "x"}} }, 400)
		bad("no text path", func(b map[string]any) { b["text"] = "" }, 400)
		bad("a refresh that is none", func(b map[string]any) { b["refresh"] = "weekly" }, 400)
		bad("a rate that is too high", func(b map[string]any) { b["rps"] = 1000 }, 400)
		bad("a link that is not a link", func(b map[string]any) { b["link"] = "javascript:x" }, 400)
		if code, r := x.putAPI("other", map[string]any{"id": "another", "name": "n", "url": qs.URL, "text": "t"}); code != 400 {
			t.Errorf("an id that is not the one in the address: %d %v", code, r)
		}
		if code, r := x.adm("GET", "/api/admin/daily", nil); code != 200 || len(r["apis"].([]any)) != 1 {
			t.Fatalf("a refused interface was saved: %v", r)
		}
		// Disabled: not offered to spaces, not used.
		body["enabled"] = false
		if code, r := x.putAPI("any", body); code != 200 || r["apis"].([]any)[0].(map[string]any)["enabled"] != false {
			t.Fatalf("%d %v", code, r)
		}
		if _, pr := x.own("GET", x.base+"/daily/prefs", nil); len(pr["available"].([]any)) != 0 || len(pr["apis"].([]any)) != 0 {
			t.Fatalf("%v", pr)
		}
		if r := quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en"); r["quote"] != nil {
			t.Fatalf("a disabled interface is used: %v", r)
		}
		if code, r := x.own("PUT", x.base+"/daily/prefs", map[string]any{"inherit": true, "apis": []any{"any"}}); code != 400 {
			t.Fatalf("a disabled interface is chosen: %d %v", code, r)
		}
		// Deleting.
		if code, r := x.adm("DELETE", "/api/admin/daily/apis/any", nil); code != 200 || len(r["apis"].([]any)) != 0 {
			t.Fatalf("%d %v", code, r)
		}
		if code, _ := x.adm("DELETE", "/api/admin/daily/apis/any", nil); code != 404 {
			t.Fatalf("%d", code)
		}
		// The list of languages is the administrators' own: BCP 47 tags in canonical form.
		code, r = x.adm("GET", "/api/admin/daily", nil)
		if fmt.Sprint(r["languages"]) != "[zh-Hans en]" {
			t.Fatalf("%v", r["languages"])
		}
		if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"languages": []any{"zh", "en", "ja", "EN", "pt-br", "ZH-hant"}}); code != 200 || fmt.Sprint(r["languages"]) != "[zh-Hans en ja pt-BR zh-Hant]" {
			t.Fatalf("%d %v", code, r["languages"])
		}
		if _, pr := x.own("GET", x.base+"/daily/prefs", nil); fmt.Sprint(pr["languages"]) != "[zh-Hans en ja pt-BR zh-Hant]" {
			t.Fatalf("%v", pr["languages"])
		}
		for _, bad := range []any{[]any{"chinese!"}, []any{"cn"}, []any{"english"}, []any{"zh_CN"}, []any{"en", ""}} {
			if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"languages": bad}); code != 400 || r["error"] != "bad_language" {
				t.Errorf("%v: %d %v", bad, code, r)
			}
		}
		for _, bad := range []any{"zh", nil} {
			if code, _ := x.adm("PUT", "/api/admin/daily", map[string]any{"languages": bad}); code != 400 {
				t.Errorf("%v: %d", bad, code)
			}
		}
		if fmt.Sprint(r["knownLanguages"].([]any)[0]) != "map[english:Chinese (Simplified) name:中文（简体） tag:zh-Hans]" {
			t.Errorf("%v", r["knownLanguages"].([]any)[0])
		}
		if len(r["apis"].([]any)) != 0 {
			t.Fatal("a built-in interface is on without being asked for")
		}
	})
}

func TestAdministratorsTryAnInterface(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		qs := x.qs
		body := qs.api("trial", "en")
		body["headers"] = []any{map[string]any{"name": "X-Api-Key", "value": "key-try"}}
		try := func(api map[string]any) (int, map[string]any) {
			x.now = x.now.Add(time.Second)
			return x.adm("POST", "/api/admin/daily/try", map[string]any{"api": api, "lang": "en", "date": "2026-11-23"})
		}
		// A trial needs no list of hosts: it goes to the host of the address in the form.
		var code int
		var r map[string]any
		// An interface that is not saved can be tried, the secret never comes back.
		code, r = try(body)
		if code != 200 || r["ok"] != true || r["status"] != float64(200) || !strings.Contains(r["raw"].(string), "quote 1 in en") || r["quote"].(map[string]any)["text"] != "quote 1 in en" ||
			r["quote"].(map[string]any)["link"] != "https://example.org/q/id1" || strings.Contains(fmt.Sprint(r), "key-try") || qs.key.Load() != "key-try" {
			t.Fatalf("%d %v", code, r)
		}
		if _, g := x.adm("GET", "/api/admin/daily", nil); len(g["apis"].([]any)) != 0 {
			t.Fatalf("a trial saved something: %v", g)
		}
		// Source, author and link are optional: a path that finds nothing (or a
		// link value that is missing) is no value, not an error.
		body["source"], body["link"] = "nope.x", "https://example.org/{missing}"
		code, r = try(body)
		if code != 200 || r["ok"] != true || r["pathErrors"] != nil || r["quote"].(map[string]any)["text"] == "" || r["quote"].(map[string]any)["link"] != nil {
			t.Fatalf("%d %v", code, r)
		}
		// The text path is required: refused, with the raw answer beside it.
		body["text"] = "wrong"
		code, r = try(body)
		res, _ := r["result"].(map[string]any)
		if code != 400 || r["error"] != "path_error" || r["field"] != "text" || res["quote"] != nil || pathErr(res, "text") == nil || res["raw"] == "" {
			t.Fatalf("%d %v", code, r)
		}
		// A trial uses the value of a header already saved, for an interface that is saved.
		good := qs.api("saved", "en")
		good["headers"] = []any{map[string]any{"name": "X-Api-Key", "value": "key-saved"}}
		x.putAPI("saved", good)
		good["headers"] = []any{map[string]any{"name": "X-Api-Key"}}
		x.now = x.now.Add(time.Second)
		if code, r := try(good); code != 200 || r["ok"] != true || qs.key.Load() != "key-saved" {
			t.Fatalf("%d %v (%v)", code, r, qs.key.Load())
		}
		// A kept value goes only where it went: to another host it is given
		// again, for a trial and for a save.
		evil := newQuoteServer(t)
		moved := qs.api("saved", "en")
		moved["url"] = evil.URL + "/q?lang={lang}"
		moved["headers"] = []any{map[string]any{"name": "X-Api-Key"}}
		if code, r := try(moved); code != 400 || r["error"] != "header_reenter" || r["header"] != "X-Api-Key" || evil.hits.Load() != 0 {
			t.Fatalf("a kept value was sent to another host: %d %v (%v)", code, r, evil.key.Load())
		}
		if code, r := x.putAPI("saved", moved); code != 400 {
			t.Fatalf("%d %v", code, r)
		}
		moved["headers"] = []any{map[string]any{"name": "X-Api-Key", "value": "key-evil"}}
		if code, r := try(moved); code != 200 || evil.key.Load() != "key-evil" {
			t.Fatalf("%d %v", code, r)
		}
		// The same host with another path, another query, or only written
		// another way, is another request: the value is given again.
		qs.hits.Store(0)
		for _, u := range []string{"HTTP://" + qs.host + "/q?lang={lang}&d={date}", qs.URL + "/other?lang={lang}&d={date}", qs.URL + "/q?lang={lang}&d={date}&x=1", qs.URL + "/q?lang={lang}"} {
			same := qs.api("saved", "en")
			same["url"] = u
			same["headers"] = []any{map[string]any{"name": "X-Api-Key"}}
			if code, r := try(same); code != 400 {
				t.Fatalf("%s: %d %v", u, code, r)
			}
			if code, r := x.putAPI("saved", same); code != 400 {
				t.Fatalf("%s: %d %v", u, code, r)
			}
		}
		// Nor under another id, even with the same address.
		other := qs.api("other-id", "en")
		other["headers"] = []any{map[string]any{"name": "X-Api-Key"}}
		if code, r := try(other); code != 400 || qs.hits.Load() != 0 {
			t.Fatalf("%d %v", code, r)
		}
		// The exact address keeps it, whatever the paths and the link are now.
		same := qs.api("saved", "en")
		same["headers"] = []any{map[string]any{"name": "X-Api-Key"}}
		same["source"], same["link"] = "data.0.a", "https://example.org/{lang}/{data.0.id}"
		if code, r := x.putAPI("saved", same); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		if code, r := try(same); code != 200 || r["ok"] != true || qs.key.Load() != "key-saved" || r["quote"].(map[string]any)["link"] != "https://example.org/en/id1" {
			t.Fatalf("%d %v", code, r)
		}
		// A service that is down, an address that is not allowed, an interface that is not valid.
		qs.fail.Store(true)
		if code, r := try(good); code != 200 || r["ok"] != false || r["status"] != float64(503) || r["message"] == "" {
			t.Fatalf("%d %v", code, r)
		}
		qs.fail.Store(false)
		for name, mod := range map[string]func(map[string]any){
			"no text":   func(b map[string]any) { b["text"] = "" },
			"a bad rps": func(b map[string]any) { b["rps"] = 0.0001 },
		} {
			b := qs.api("x", "en")
			mod(b)
			if code, r := try(b); code != 400 {
				t.Errorf("%s: %d %v", name, code, r)
			}
		}
		if code, _ := x.adm("POST", "/api/admin/daily/try", map[string]any{"api": good, "lang": "chinese!"}); code != 400 {
			t.Errorf("a bad lang: %d", code)
		}
		if code, _ := x.adm("POST", "/api/admin/daily/try", map[string]any{"api": good, "date": "tomorrow"}); code != 400 {
			t.Errorf("a bad date: %d", code)
		}
		// Trying is limited.
		limited := false
		for i := 0; i < 10; i++ {
			if code, _ := x.adm("POST", "/api/admin/daily/try", map[string]any{"api": good}); code == 429 {
				limited = true
			}
		}
		if !limited {
			t.Error("trying is never limited")
		}
	})
}

// Administrators who save at the same moment each keep their change.
func TestAdministratorsSaveAtOnce(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		const n = 8
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id := fmt.Sprint("api", i)
				if code, r := x.putAPI(id, x.qs.api(id, "")); code != 200 {
					t.Errorf("%d %v", code, r)
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"languages": []any{"zh", "en", "ja"}}); code != 200 {
				t.Errorf("%d %v", code, r)
			}
		}()
		wg.Wait()
		_, r := x.adm("GET", "/api/admin/daily", nil)
		if len(r["apis"].([]any)) != n || fmt.Sprint(r["languages"]) != "[zh-Hans en ja]" {
			t.Fatalf("changes were lost: %d interfaces, languages %v", len(r["apis"].([]any)), r["languages"])
		}
	})
}

// The server is the only one to ask, once for each period, keeps it in memory and in
// the database, and uses the last thing said when the interface fails.
func TestExternalInterfacesAreCachedAndFallBack(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		qs := x.qs
		b := qs.api("any", "")
		if code, r := x.putAPI("any", b); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		x.choose("any")
		const day = "date=2026-11-23&lang=en"

		// --- the browser's cache: until the day is over where the browser is
		get := func(query string, hdr ...string) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", x.base+"/daily?"+query, nil)
			for _, c := range x.tm.viewer.cookies {
				req.AddCookie(c)
			}
			for i := 0; i+1 < len(hdr); i += 2 {
				req.Header.Set(hdr[i], hdr[i+1])
			}
			x.a.h.ServeHTTP(rec, req)
			return rec
		}
		rec := get(day + "&tz=-480") // UTC-8: the day ends at 08:00 UTC the next day
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		cc := rec.Header().Get("Cache-Control")
		etag := rec.Header().Get("ETag")
		want := int(time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC).Sub(x.now) / time.Second)
		if cc != fmt.Sprintf("private, max-age=%d", want) || !strings.HasPrefix(etag, `"`) {
			t.Fatalf("%q %q (want max-age %d)", cc, etag, want)
		}
		// The same request with the ETag is not answered again.
		if rec := get(day+"&tz=-480", "If-None-Match", etag); rec.Code != 304 || rec.Body.Len() != 0 || rec.Header().Get("ETag") != etag {
			t.Fatalf("%d %q", rec.Code, rec.Body)
		}
		if rec := get(day+"&tz=-480", "If-None-Match", `"other"`); rec.Code != 200 {
			t.Fatalf("%d", rec.Code)
		}
		// Without the offset only a short time is promised; the offset is checked; an empty answer is short too.
		if cc := get(day).Header().Get("Cache-Control"); cc != "private, max-age=300" {
			t.Fatalf("%q", cc)
		}
		if rec := get(day + "&tz=9999"); rec.Code != 400 {
			t.Fatalf("%d", rec.Code)
		}
		if rec := get(day + "&tz=x"); rec.Code != 400 {
			t.Fatalf("%d", rec.Code)
		}
		// A day that is almost over still gets a minute, and a day in the past too.
		if cc := get("date=2026-11-22&lang=en&tz=0").Header().Get("Cache-Control"); cc != "private, max-age=60" {
			t.Fatalf("a day that is over: %q", cc)
		}
		if cc := get("date=2026-11-24&lang=zh&tz=0").Header().Get("Cache-Control"); cc != "private, max-age=86400" {
			t.Fatalf("a day ahead: %q", cc)
		}
		// A day far from today (where the reader is, give or take a day) is not
		// asked for: each would be a request and a row kept.
		before0 := qs.hits.Load()
		for _, q := range []string{"date=2026-12-30&lang=en&tz=0", "date=1970-01-01&lang=en", "date=2026-11-21&lang=en&tz=840", "date=2026-11-25&lang=en&tz=-840"} {
			if r := quoteOf(t, x.viewer, x.base, q); r["quote"] != nil || r["reason"] != "out_of_range" {
				t.Fatalf("%s: %v", q, r)
			}
		}
		if qs.hits.Load() != before0 {
			t.Fatalf("%d requests for days far off", qs.hits.Load()-before0)
		}
		if r := quoteOf(t, x.viewer, x.base, "date=2026-11-22&lang=en&tz=-840"); r["quote"] == nil {
			t.Fatalf("today where the reader is: %v", r)
		}
		// --- memory: once for the period, whoever asks and however often
		asked := qs.hits.Load()
		first := quoteText(quoteOf(t, x.viewer, x.base, day))
		for i := 0; i < 5; i++ {
			if quoteText(quoteOf(t, x.own, x.base, day)) != first {
				t.Fatal("not the same")
			}
		}
		// The day was asked for by the first request above (with two other days);
		// asking six times more asked nothing.
		if got := qs.hits.Load() - asked; got != 0 {
			t.Fatalf("%d requests for a day that was kept", got)
		}
		if asked != 3 {
			t.Fatalf("%d requests before", asked)
		}
		// A new day is a new request; "another" asks again (skipNew); both kept.
		if quoteText(quoteOf(t, x.viewer, x.base, "date=2026-11-24&lang=en")) == first {
			t.Fatal("a new day gave the same text")
		}
		other := quoteText(quoteOf(t, x.viewer, x.base, day+"&skip=1"))
		if other == first || quoteText(quoteOf(t, x.viewer, x.base, day+"&skip=1")) != other {
			t.Fatalf("skip: %q", other)
		}
		if got := qs.hits.Load() - asked; got != 2 {
			t.Fatalf("%d requests", got)
		}

		// --- the database: a restart asks for nothing
		fetcher2 := dailyfetch.NewForTests(api.NewDailyBacking(x.a.st))
		fetcher2.SetClock(x.clock)
		h2 := api.New(api.Options{Store: x.a.st, Spaces: x.a.mgr, Drafts: x.a.drafts, Auth: x.a.auth, PublicURL: x.a.public, Version: "test-1", DailyFetch: fetcher2})
		a2 := *x.a
		a2.h = h2
		v2 := *x.tm.viewer
		v2.a = &a2
		restarted := asClient(&v2)
		before := qs.hits.Load()
		if quoteText(quoteOf(t, restarted, x.base, day)) != first || quoteText(quoteOf(t, restarted, x.base, day+"&skip=1")) != other || qs.hits.Load() != before {
			t.Fatalf("a restart asked again (%d requests)", qs.hits.Load()-before)
		}
		var rows int
		x.a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM daily_cache`).Scan(&rows)
		if rows < 3 {
			t.Fatalf("%d rows kept", rows)
		}

		// --- failure: the last thing it said, with its time, then hidden; waits that grow
		qs.fail.Store(true)
		x.now = x.now.Add(48 * time.Hour) // two days on: nothing is kept for them
		stale := quoteOf(t, restarted, x.base, "date=2026-11-25&lang=en")
		sq, _ := stale["quote"].(map[string]any)
		if sq == nil || sq["stale"] != true || sq["fetched"] == nil || sq["text"] == "" {
			t.Fatalf("%v", stale)
		}
		if _, ok := sq["fetched"].(string); !ok || !strings.HasPrefix(sq["fetched"].(string), "2026-11-23T08:00") {
			t.Fatalf("it does not say when it was got: %v", sq["fetched"])
		}
		// A stale answer is promised for a short time only.
		rec = httptest.NewRecorder()
		req := httptest.NewRequest("GET", x.base+"/daily?date=2026-11-25&lang=en&tz=0", nil)
		for _, c := range v2.cookies {
			req.AddCookie(c)
		}
		h2.ServeHTTP(rec, req)
		if cc := rec.Header().Get("Cache-Control"); cc != "private, max-age=60" {
			t.Fatalf("%q", cc)
		}
		hits := qs.hits.Load()
		for i := 0; i < 10; i++ {
			quoteOf(t, restarted, x.base, "date=2026-11-25&lang=en")
		}
		if qs.hits.Load()-hits > 1 {
			t.Fatalf("%d requests while backing off", qs.hits.Load()-hits)
		}
		// Seven days after the last good answer there is nothing to give: the block is hidden.
		x.now = x.now.Add(8 * 24 * time.Hour)
		gone := quoteOf(t, restarted, x.base, "date=2026-12-03&lang=en")
		if gone["quote"] != nil || gone["reason"] != "unavailable" {
			t.Fatalf("%v", gone)
		}
		// It comes back.
		qs.fail.Store(false)
		x.now = x.now.Add(2 * time.Hour)
		back := quoteOf(t, restarted, x.base, "date=2026-12-03&lang=en")
		if bq, _ := back["quote"].(map[string]any); bq == nil || bq["stale"] == true {
			t.Fatalf("%v", back)
		}
	})
}

func TestExternalRefreshIntervalAndRateLimit(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		qs := x.qs
		b := qs.api("any", "")
		b["refresh"] = "hourly"
		x.putAPI("any", b)
		x.choose("any")
		text := func(date string) string { return quoteText(quoteOf(t, x.viewer, x.base, "date="+date+"&lang=en")) }
		one := text("2026-11-23")
		x.now = x.now.Add(30 * time.Minute)
		if text("2026-11-24") != one {
			t.Fatal("the same hour, another item")
		}
		// The browser may keep it until the hour is over.
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", x.base+"/daily?date=2026-11-23&lang=en&tz=0", nil)
		for _, c := range x.tm.viewer.cookies {
			req.AddCookie(c)
		}
		x.a.h.ServeHTTP(rec, req)
		if cc := rec.Header().Get("Cache-Control"); cc != "private, max-age=1800" {
			t.Fatalf("%q", cc)
		}
		x.now = x.now.Add(31 * time.Minute)
		if text("2026-11-23") == one {
			t.Fatal("a new hour, the same item")
		}
		// Not asking for a new item each time: "another" is the same item.
		b["refresh"], b["skipNew"] = "daily", false
		x.putAPI("any", b)
		a := text("2026-11-23")
		hits := qs.hits.Load()
		if quoteText(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en&skip=4")) != a || qs.hits.Load() != hits {
			t.Fatal("skip asked the interface for another item")
		}
		// The outgoing rate: 1 a second, a bucket of one. Nothing waits: what was said last is given.
		b["skipNew"], b["rps"] = true, 1
		x.putAPI("any", b)
		fresh := quoteText(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en&skip=1"))
		hits = qs.hits.Load()
		for k := 2; k < 6; k++ {
			r := quoteOf(t, x.viewer, x.base, fmt.Sprintf("date=2026-11-23&lang=en&skip=%d", k))
			if q := r["quote"].(map[string]any); q["stale"] != true || q["text"] != fresh {
				t.Fatalf("%v", r)
			}
		}
		if qs.hits.Load() != hits {
			t.Fatalf("%d requests above the rate", qs.hits.Load()-hits)
		}
		x.now = x.now.Add(1100 * time.Millisecond)
		if r := quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en&skip=7"); r["quote"].(map[string]any)["stale"] == true {
			t.Fatalf("%v", r)
		}
	})
}

// The real fetcher, with the rules it keeps whatever an administrator types.
func TestPrivateAddressesAreNeverConfigured(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		boss := asClient(a.setup("kazuha", pw))
		put := func(url string) (int, map[string]any) {
			return boss("PUT", "/api/admin/daily/apis/x", map[string]any{"name": "x", "url": url, "text": "t"})
		}
		for _, u := range []string{"http://example.org/", "https://127.0.0.1/", "https://127.0.0.1:8080/x", "https://[::1]/", "https://localhost/", "https://a.localhost/", "https://169.254.169.254/latest/meta-data",
			"https://192.168.1.1/", "https://10.0.0.5/", "https://100.64.0.1/", "https://[::ffff:127.0.0.1]/", "https://0.0.0.0/", "ftp://example.org/", "https://user:pw@example.org/", "https://{lang}.example.org/"} {
			if code, r := put(u); code != 400 {
				t.Errorf("%s: %d %v", u, code, r)
			}
			if code, r := boss("POST", "/api/admin/daily/try", map[string]any{"api": map[string]any{"id": "x", "name": "x", "url": u, "text": "t"}}); code != 400 {
				t.Errorf("a trial of %s: %d %v", u, code, r)
			}
		}
		if _, g := boss("GET", "/api/admin/daily", nil); len(g["apis"].([]any)) != 0 {
			t.Fatalf("%v", g)
		}
		if code, r := put("https://example.org/q"); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		// A name that resolves to this machine is refused when the connection is made (see dailyfetch).
	})
}

// --- what the administrators let the spaces do ----------------------------------------------

func TestDailyAdminLocks(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		day := "date=2026-11-23&lang=en"
		origin := func() string { return quoteOf(t, x.viewer, x.base, day)["origin"].(string) }
		allow := func(body map[string]any) map[string]any {
			t.Helper()
			code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"allow": body})
			if code != 200 {
				t.Fatalf("%v: %d %v", body, code, r)
			}
			return r["allow"].(map[string]any)
		}
		inEffect := func() (bool, map[string]any) {
			code, r := x.own("GET", x.base+"/daily/settings", nil)
			if code != 200 {
				t.Fatalf("%d %v", code, r)
			}
			return r["inEffect"].(bool), r
		}
		prefs := func() map[string]any {
			code, r := x.own("GET", x.base+"/daily/prefs", nil)
			if code != 200 {
				t.Fatalf("%d %v", code, r)
			}
			return r
		}

		// Everything is allowed until it is said otherwise.
		if _, r := x.adm("GET", "/api/admin/daily", nil); fmt.Sprint(r["allow"]) != "map[api:true own:true]" {
			t.Fatalf("%v", r["allow"])
		}
		if a := prefs()["allowed"]; fmt.Sprint(a) != "map[api:true own:true]" {
			t.Fatalf("%v", a)
		}
		// Only the administrators change it, and it is checked.
		for name, c := range map[string]caller{"owner": x.own, "viewer": x.viewer} {
			if code, _ := c("PUT", "/api/admin/daily", map[string]any{"allow": map[string]any{"own": false}}); code != 403 {
				t.Errorf("%s: %d", name, code)
			}
		}
		for name, bad := range map[string]any{"empty": map[string]any{}, "a string": "no", "not a boolean": map[string]any{"own": "no"}, "null": nil} {
			if code, _ := x.adm("PUT", "/api/admin/daily", map[string]any{"allow": bad}); code != 400 {
				t.Errorf("%s: %d", name, code)
			}
		}

		// Three sources: the space's own, an interface, the default.
		x.adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "list"}})
		x.adm("PUT", "/api/admin/daily/lines", map[string]any{"lang": "en", "text": "From the list"})
		if code, r := x.putAPI("q1", x.qs.api("q1", "en")); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		if code, r := x.own("POST", x.base+"/pages", map[string]any{"folder": "poems", "meta": map[string]any{"title": "Own poem"}, "body": "x\n"}); code != 201 {
			t.Fatalf("%d %v", code, r)
		}
		own := map[string]any{"folder": "poems", "filter": []any{}}
		if code, r := x.own("PUT", x.base+"/daily/settings", map[string]any{"daily": own}); code != 200 || r["inEffect"] != true {
			t.Fatalf("%d %v", code, r)
		}
		x.choose("q1")
		if o := origin(); o != "space" {
			t.Fatalf("own first: %s", o)
		}

		// own is locked: the source stays in space.yml, is not used, and the interface is next.
		if a := allow(map[string]any{"own": false}); fmt.Sprint(a) != "map[api:true own:false]" {
			t.Fatalf("%v", a)
		}
		if o := origin(); o != "external" {
			t.Fatalf("own locked: %s", o)
		}
		if ok, r := inEffect(); ok || fmt.Sprint(r["daily"]) != fmt.Sprint(own) {
			t.Fatalf("a locked source is kept: %v", r)
		}
		if a := prefs()["allowed"]; fmt.Sprint(a) != "map[api:true own:false]" {
			t.Fatalf("%v", a)
		}
		// It still saves, says it is not in effect, and does not show.
		other := map[string]any{"folder": "poems", "filter": []any{map[string]any{"key": "status", "op": "in", "values": []any{"x"}}}}
		code, r := x.own("PUT", x.base+"/daily/settings", map[string]any{"daily": other})
		if code != 200 || r["inEffect"] != false || fmt.Sprint(r["daily"]) != fmt.Sprint(other) {
			t.Fatalf("%d %v", code, r)
		}
		if o := origin(); o != "external" {
			t.Fatalf("%s", o)
		}
		x.own("PUT", x.base+"/daily/settings", map[string]any{"daily": own})
		// Unlocked, what was saved works again.
		allow(map[string]any{"own": true})
		if o := origin(); o != "space" {
			t.Fatalf("unlocked: %s", o)
		}
		if ok, _ := inEffect(); !ok {
			t.Fatal("in effect again")
		}

		// api is locked: the choice is kept and not used; the default is next.
		if a := allow(map[string]any{"api": false}); fmt.Sprint(a) != "map[api:false own:true]" {
			t.Fatalf("%v", a)
		}
		x.own("PUT", x.base+"/daily/settings", map[string]any{"daily": nil})
		if o := origin(); o != "site" {
			t.Fatalf("api locked: %s", o)
		}
		p := prefs()
		if fmt.Sprint(p["apis"]) != "[q1]" || fmt.Sprint(p["allowed"]) != "map[api:false own:true]" {
			t.Fatalf("a locked choice is kept: %v", p)
		}
		if code, r := x.own("PUT", x.base+"/daily/prefs", map[string]any{"inherit": true, "apis": []any{}}); code != 200 || fmt.Sprint(r["allowed"]) != "map[api:false own:true]" {
			t.Fatalf("%d %v", code, r)
		}
		x.choose("q1")
		if o := origin(); o != "site" {
			t.Fatalf("%s", o)
		}
		hits := x.qs.hits.Load()
		origin()
		if x.qs.hits.Load() != hits {
			t.Error("a locked interface was asked")
		}
		allow(map[string]any{"api": true})
		if o := origin(); o != "external" {
			t.Fatalf("unlocked: %s", o)
		}

		// Both locked: only the default is left; a partial change keeps the other flag.
		x.own("PUT", x.base+"/daily/settings", map[string]any{"daily": own})
		allow(map[string]any{"own": false, "api": false})
		if o := origin(); o != "site" {
			t.Fatalf("both locked: %s", o)
		}
		if a := allow(map[string]any{"own": true}); fmt.Sprint(a) != "map[api:false own:true]" {
			t.Fatalf("%v", a)
		}
		if o := origin(); o != "space" {
			t.Fatalf("%s", o)
		}
		// A source that matches nothing is "no_match" only when it is in effect.
		x.own("PUT", x.base+"/daily/settings", map[string]any{"daily": other})
		if r := quoteOf(t, x.viewer, x.base, day); r["reason"] != "no_match" {
			t.Fatalf("%v", r)
		}
		allow(map[string]any{"own": false})
		if o := origin(); o != "site" {
			t.Fatalf("%s", o)
		}
	})
}

// An administrator who does not know a kept key cannot read it back: not by
// pointing the interface at a service that says it back (the key is bound to
// the address it was given for), not through a link (a link never takes a
// header), and not through a path, a trial or the readers' daily text when
// the service the key is for says it back itself.
func TestAKeptKeyCannotBeReadBack(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		const key = "Bearer sk-live-0123456789abcdef"
		echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			k := r.Header.Get("Authorization")
			b, _ := json.Marshal(map[string]any{"text": "fine", "plain": k, "upper": strings.ToUpper(k), "headers": map[string]any{"Authorization": k}})
			// PHP writes / as \/.
			w.Write([]byte(strings.ReplaceAll(string(b), "/", `\/`)))
		}))
		defer echo.Close()
		body := map[string]any{"name": "Echo", "url": echo.URL + "/q?lang={lang}", "text": "text", "rps": 100,
			"headers": []any{map[string]any{"name": "Authorization", "value": key}}}
		if code, r := x.putAPI("echo", body); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		x.choose("echo")
		try := func(api map[string]any) (int, map[string]any) {
			x.now = x.now.Add(time.Second)
			return x.adm("POST", "/api/admin/daily/try", map[string]any{"api": api, "lang": "en", "date": "2026-11-23"})
		}
		leaks := func(r map[string]any) bool {
			s := strings.ToLower(fmt.Sprint(r))
			return strings.Contains(s, "sk-live") || strings.Contains(s, "0123456789abcdef")
		}
		kept := func(mod func(map[string]any)) map[string]any {
			b := map[string]any{"id": "echo", "name": "Echo", "url": echo.URL + "/q?lang={lang}", "text": "text", "rps": 100,
				"headers": []any{map[string]any{"name": "Authorization"}}}
			mod(b)
			return b
		}
		// A link with a header in it is refused, saved or tried.
		for _, link := range []string{"https://e.org/?k={headers.Authorization}", "https://e.org/{Authorization}"} {
			b := kept(func(b map[string]any) { b["link"] = link })
			if code, r := x.putAPI("echo", b); code != 400 || r["error"] != "link_uses_header" || r["field"] != "link" || leaks(r) {
				t.Fatalf("%s: %d %v", link, code, r)
			}
			if code, r := try(b); code != 400 || r["error"] != "link_uses_header" || leaks(r) {
				t.Fatalf("%s: %d %v", link, code, r)
			}
		}
		// The answer is not shown, and no path takes the key out, however it is
		// written; the readers never see it either.
		for _, path := range []string{"text", "plain", "upper", "headers.Authorization"} {
			b := kept(func(b map[string]any) { b["source"] = path; b["link"] = "https://e.org/?k={" + path + "}" })
			if path == "headers.Authorization" {
				b["link"] = "https://e.org/?k={plain}" // a link with it is refused before it is asked (above)
			}
			code, r := try(b)
			res, _ := r["result"].(map[string]any)
			if path == "text" {
				// Nothing a path takes out repeats it, so the trial stands; only the raw answer is held back.
				res = r
				if code != 200 || r["ok"] != false || r["message"] != dailyfetch.MsgRepeatsHeader {
					t.Fatalf("%s: %d %v", path, code, r)
				}
			} else if code != 400 || r["error"] != "path_reveals_secret" || r["field"] != "source" || pathErr(res, "link") == nil {
				t.Fatalf("%s: %d %v", path, code, r)
			}
			if res["raw"] != nil || leaks(r) {
				t.Fatalf("%s: %d %v", path, code, r)
			}
			if code, r := x.putAPI("echo", b); code != 200 {
				t.Fatalf("%d %v", code, r)
			}
			x.now = x.now.Add(25 * time.Hour)
			r = quoteOf(t, x.viewer, x.base, "date="+x.now.UTC().Format("2006-01-02")+"&lang=en")
			if (path != "text" && r["quote"] != nil) || leaks(r) {
				t.Fatalf("%s: the readers see %v", path, r)
			}
		}
		// Given again by someone who knows it, the key goes to the new address;
		// the value is never given back.
		moved := kept(func(b map[string]any) {
			b["url"] = echo.URL + "/other"
			b["headers"] = []any{map[string]any{"name": "Authorization", "value": "Bearer sk-live-fedcba9876543210"}}
		})
		if code, r := x.putAPI("echo", moved); code != 200 || leaks(r) {
			t.Fatalf("%d %v", code, r)
		}
	})
}

// pathErr is the entry of a trial's pathErrors for a field, nil when there is none.
func pathErr(res map[string]any, field string) any {
	l, _ := res["pathErrors"].([]any)
	for _, e := range l {
		if m := e.(map[string]any); m["field"] == field {
			return m
		}
	}
	return nil
}
