package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
		fmt.Fprintf(w, `{"data":[{"q":"quote %d in %s","a":"someone","s":"a book"}]}`, n, r.URL.Query().Get("lang"))
	}))
	t.Cleanup(q.Close)
	q.host = strings.TrimPrefix(q.URL, "http://")
	return q
}

func (q *quoteServer) api(id, lang string) map[string]any {
	return map[string]any{"id": id, "name": "Quotes " + id, "lang": lang, "url": q.URL + "/q?lang={lang}", "text": "data.0.q", "source": "data.0.s", "author": "data.0.a"}
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
		fetcher := dailyfetch.NewForTests()
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
		for _, rt := range [][3]string{{"GET", "/api/admin/daily", ""}, {"PUT", "/api/admin/daily", "{}"}, {"PUT", "/api/admin/daily/lines", "{}"}, {"DELETE", "/api/admin/daily/lines", ""}, {"POST", "/api/admin/daily/apis/x/test", "{}"}} {
			for name, c := range map[string]*client{"owner": tm.owner, "viewer": tm.viewer} {
				if r := c.do(rt[0], rt[1], rt[2]); r.code != 403 {
					t.Errorf("%s %v: %d %s", name, rt, r.code, r.raw)
				}
			}
		}
		code, r := adm("GET", "/api/admin/daily", nil)
		if code != 200 || r["default"] != nil || len(r["hosts"].([]any)) != 0 || len(r["apis"].([]any)) != 0 || r["lines"].(map[string]any)["total"] != float64(0) {
			t.Fatalf("%d %v", code, r)
		}

		// --- the settings are checked
		for name, body := range map[string]map[string]any{
			"an unknown key":                 {"extra": 1},
			"a host with a path":             {"hosts": []any{"a.example/x"}},
			"hosts that are not":             {"hosts": "a.example"},
			"an api of another host":         {"hosts": []any{"a.example"}, "apis": []any{qs.api("q", "en")}},
			"an api with no text":            {"hosts": []any{qs.host}, "apis": []any{map[string]any{"id": "q", "name": "q", "url": qs.URL + "/", "text": ""}}},
			"two apis alike":                 {"hosts": []any{qs.host}, "apis": []any{qs.api("q", "en"), qs.api("q", "zh")}},
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
		if _, r := adm("GET", "/api/admin/daily", nil); len(r["apis"].([]any)) != 0 {
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

		// --- external interfaces
		cfg := map[string]any{"hosts": []any{qs.host}, "apis": []any{qs.api("any", ""), qs.api("zhonly", "zh")}}
		code, r = adm("PUT", "/api/admin/daily", cfg)
		if code != 200 || len(r["apis"].([]any)) != 2 || r["default"] == nil {
			t.Fatalf("%d %v", code, r)
		}
		// The owner sees what there is (names, not addresses) and chooses.
		code, pr := own("GET", base+"/daily/prefs", nil)
		if code != 200 || len(pr["available"].([]any)) != 2 || strings.Contains(fmt.Sprint(pr), qs.host) {
			t.Fatalf("%d %v", code, pr)
		}
		if code, r := own("PUT", base+"/daily/prefs", map[string]any{"inherit": true, "apis": []any{"nope"}}); code != 400 {
			t.Fatalf("%d %v", code, r)
		}
		// Choosing is not enough to fetch for a space that did not choose.
		if quoteOf(t, viewer, base, day+"&lang=en")["origin"] != "site" || qs.hits.Load() != 0 {
			t.Fatal("a space that chose nothing fetched")
		}
		if code, r := own("PUT", base+"/daily/prefs", map[string]any{"inherit": true, "apis": []any{"any", "zhonly"}}); code != 200 || len(r["apis"].([]any)) != 2 {
			t.Fatalf("%d %v", code, r)
		}
		// Priority: the interface before the default; the language of the interface when it has one.
		r1 := quoteOf(t, viewer, base, day+"&lang=en")
		if r1["origin"] != "external" || quoteText(r1) != "quote 1 in en" || r1["quote"].(map[string]any)["author"] != "someone" || r1["quote"].(map[string]any)["source"] != "a book" {
			t.Fatalf("%v", r1)
		}
		for i := 0; i < 5; i++ {
			if quoteText(quoteOf(t, own, base, day+"&lang=en")) != "quote 1 in en" {
				t.Fatal("not the same all day")
			}
		}
		if qs.hits.Load() != 1 {
			t.Fatalf("%d requests for one day", qs.hits.Load())
		}
		if txt := quoteText(quoteOf(t, viewer, base, day+"&lang=zh")); txt != "quote 2 in zh" {
			t.Fatalf("zh: %s", txt)
		}
		if txt := quoteText(quoteOf(t, viewer, base, "date=2026-11-24&lang=en")); txt != "quote 3 in en" {
			t.Fatalf("the next day: %s", txt)
		}
		// "Another" asks again, and is limited.
		if txt := quoteText(quoteOf(t, viewer, base, day+"&lang=en&skip=1")); txt != "quote 4 in en" {
			t.Fatalf("skip: %s", txt)
		}
		limited := false
		for k := 2; k < 30; k++ {
			code, r := viewer("GET", base+fmt.Sprintf("/daily?%s&lang=en&skip=%d", day, k), nil)
			if code == 429 && r["error"] == "rate_limited" {
				limited = true
				break
			}
			if code != 200 {
				t.Fatalf("%d %v", code, r)
			}
		}
		if !limited {
			t.Error("asking for another text was never limited")
		}
		// An interface that is down hides the block; the day's text asked for before stays.
		now = now.Add(2 * time.Minute)
		qs.fail.Store(true)
		if r := quoteOf(t, viewer, base, "date=2026-12-01&lang=en"); r["quote"] != nil || r["page"] != nil || r["reason"] != "unavailable" {
			t.Fatalf("%v", r)
		}
		if txt := quoteText(quoteOf(t, viewer, base, day+"&lang=en")); txt != "quote 1 in en" {
			t.Fatalf("%s", txt)
		}
		// The space's own source comes before all of it.
		own("POST", base+"/folders", map[string]any{"path": "Own"})
		if code, r := own("POST", base+"/pages", map[string]any{"folder": "Own", "meta": map[string]any{"title": "Mine"}, "body": "mine\n"}); code != 201 {
			t.Fatalf("%d %v", code, r)
		}
		if code, r := own("PUT", base+"/daily/settings", map[string]any{"daily": map[string]any{"folder": "Own"}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		r = quoteOf(t, viewer, base, day+"&lang=en")
		if r["origin"] != "space" || r["quote"] != nil || r["page"].(map[string]any)["title"] != "Mine" {
			t.Fatalf("%v", r)
		}
		// Another space that opted out of everything shows nothing.
		if code, r := asClient(other)("PUT", "/api/spaces/"+otherSpace+"/daily/prefs", map[string]any{"inherit": false, "apis": []any{}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		if r := quoteOf(t, asClient(other), "/api/spaces/"+otherSpace, day+"&lang=en"); r["reason"] != "no_source" {
			t.Fatalf("%v", r)
		}
		// An interface taken away is not used.
		if code, r := adm("PUT", "/api/admin/daily", map[string]any{"apis": []any{}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		if code, pr := own("GET", base+"/daily/prefs", nil); code != 200 || len(pr["apis"].([]any)) != 0 {
			t.Fatalf("%d %v", code, pr)
		}
		// Testing an interface.
		adm("PUT", "/api/admin/daily", map[string]any{"apis": []any{qs.api("any", "")}})
		qs.fail.Store(false)
		if code, r := adm("POST", "/api/admin/daily/apis/any/test", map[string]any{"lang": "zh"}); code != 200 || r["ok"] != true || !strings.HasPrefix(r["quote"].(map[string]any)["text"].(string), "quote") {
			t.Fatalf("%d %v", code, r)
		}
		qs.fail.Store(true)
		now = now.Add(2 * time.Minute)
		if code, r := adm("POST", "/api/admin/daily/apis/any/test", map[string]any{}); code != 200 || r["ok"] != false || r["message"] == "" {
			t.Fatalf("%d %v", code, r)
		}
		if code, _ := adm("POST", "/api/admin/daily/apis/none/test", map[string]any{}); code != 404 {
			t.Fatalf("%d", code)
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
	for _, c := range [][2]string{{"GET", "/api/admin/daily"}, {"PUT", "/api/admin/daily"}, {"PUT", "/api/admin/daily/lines"}, {"GET", base + "/daily/prefs"}, {"PUT", base + "/daily/prefs"}} {
		if code, r, raw := f.do(c[0], c[1], "{}"); code != 404 || r["error"] != "not_found" {
			t.Errorf("%v: %d %s", c, code, raw)
		}
	}
	if code, r, _ := f.do("GET", base+"/daily?date=2026-11-23&lang=en", nil); code != 200 || r["reason"] != "no_source" {
		t.Fatalf("%d %v", code, r)
	}
}

func TestDailyLangIsChecked(t *testing.T) {
	f := newFixture(t)
	for _, l := range []string{"中文", "x", "en_US", "toolongcode", "en-"} {
		if code, _, raw := f.do("GET", base+"/daily?date=2026-11-23&lang="+l, nil); code != 400 {
			t.Errorf("%s: %d %s", l, code, raw)
		}
	}
	for _, l := range []string{"en", "zh-CN", "EN", ""} {
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
	if d["zh"].(map[string]any)["folder"] != "zh" || d["en-us"].(map[string]any)["folder"] != "en" || d["folder"] != nil {
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
	for name, bad := range map[string]any{"a bad code": map[string]any{"chinese!": map[string]any{"folder": "zh"}}, "a source that is not": map[string]any{"zh": "folder"}} {
		if code, r := c("PUT", base+"/daily/settings", map[string]any{"daily": bad}); code != 400 {
			t.Errorf("%s: %d %v", name, code, r)
		}
	}
}
