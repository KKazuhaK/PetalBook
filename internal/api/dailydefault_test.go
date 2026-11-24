package api_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

// The default of the administrators may be an interface for each language.
func TestSiteDefaultIsAnInterfaceForEachLanguage(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		other := newQuoteServer(t)
		zhAPI, enAPI := x.qs.api("zh-quotes", "zh-Hans"), other.api("en-quotes", "en")
		zhAPI["langMap"] = map[string]any{"zh-Hans": "zh"}
		for id, b := range map[string]map[string]any{"zh-quotes": zhAPI, "en-quotes": enAPI} {
			if code, r := x.putAPI(id, b); code != 200 {
				t.Fatalf("%d %v", code, r)
			}
		}
		setDefault := func(apis map[string]any) (int, map[string]any) {
			return x.adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "api", "apis": apis}})
		}
		// Each must be an interface that is set up; the languages are tags.
		for name, apis := range map[string]map[string]any{
			"an unknown id":            {"": "nothing"},
			"an unknown id for a lang": {"": "en-quotes", "zh": "nothing"},
			"nothing":                  {},
		} {
			if code, r := setDefault(apis); code != 400 || (name != "nothing" && r["error"] != "api_not_found") {
				t.Errorf("%s: %d %v", name, code, r)
			}
		}
		for _, l := range []string{"cn", "english", "zh_CN"} {
			if code, r := setDefault(map[string]any{l: "en-quotes"}); code != 400 || r["error"] != "bad_language" {
				t.Errorf("%s: %d %v", l, code, r)
			}
		}
		if code, r := setDefault(map[string]any{"zh": "zh-quotes", "zh-Hans": "en-quotes"}); code != 400 {
			t.Errorf("a language twice: %d %v", code, r)
		}
		if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "api", "apis": map[string]any{"": "en-quotes"}, "space": x.tm.sid}}); code != 400 {
			t.Errorf("an interface with a space: %d %v", code, r)
		}
		if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "list", "apis": map[string]any{"": "en-quotes"}}}); code != 400 {
			t.Errorf("a list with interfaces: %d %v", code, r)
		}
		if _, g := x.adm("GET", "/api/admin/daily", nil); g["default"] != nil {
			t.Fatalf("a refused default was saved: %v", g["default"])
		}
		code, r := setDefault(map[string]any{"": "en-quotes", "zh": "zh-quotes"})
		if d := asMap(r["default"]); code != 200 || d["kind"] != "api" || fmt.Sprint(d["apis"]) != "map[:[en-quotes] zh-Hans:[zh-quotes]]" {
			t.Fatalf("%d %v", code, r)
		}
		// Nobody has chosen an interface, and the spaces may not: the default is the administrators' own.
		if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"allow": map[string]any{"api": false}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		read := func(lang string) map[string]any {
			x.t.Helper()
			return quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang="+lang)
		}
		for lang, want := range map[string]string{"zh-CN": "in zh", "zh-SG": "in zh", "zh": "in zh", "en": "in en", "en-GB": "in en", "fr": "in en"} {
			r := read(lang)
			if q := asMap(r["quote"]); q == nil || !strings.HasSuffix(q["text"].(string), want) || r["origin"] != "external" || r["page"] != nil {
				t.Errorf("%s: %v", lang, r)
			}
		}
		if x.qs.hits.Load() != 1 || other.hits.Load() != 1 {
			t.Errorf("the same cache as a space's choice: %d and %d requests", x.qs.hits.Load(), other.hits.Load())
		}
		// The same refresh and rate limit: a second one asks again, over the limit nothing waits.
		for _, id := range []string{"zh-quotes"} {
			b := zhAPI
			b["rps"], b["skipNew"] = 0.01, true
			if code, r := x.putAPI(id, b); code != 200 {
				t.Fatalf("%d %v", code, r)
			}
		}
		x.qs.hits.Store(0)
		read("zh-CN")
		if code, _ := x.viewer("GET", x.base+"/daily?date=2026-11-23&lang=zh-CN&skip=1", nil); code != 200 && code != 429 {
			t.Errorf("%d", code)
		}
		// A language with no interface of its own and no "all languages" has no source.
		if code, r := setDefault(map[string]any{"en": "en-quotes"}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		if r := read("fr"); r["quote"] != nil || r["reason"] != "no_source" {
			t.Errorf("%v", r)
		}
		if r := read("en-GB"); r["quote"] == nil {
			t.Errorf("%v", r)
		}
		// A disabled one is unavailable, and so is one that is gone.
		enAPI["enabled"] = false
		if code, r := x.putAPI("en-quotes", enAPI); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		if r := read("en"); r["quote"] != nil || r["reason"] != "unavailable" {
			t.Errorf("disabled: %v", r)
		}
		if code, _ := x.adm("DELETE", "/api/admin/daily/apis/en-quotes", nil); code != 200 {
			t.Fatal(code)
		}
		if r := read("en"); r["quote"] != nil || r["reason"] != "unavailable" {
			t.Errorf("gone: %v", r)
		}
		// A space that does not take the default does not show it.
		x.own("PUT", x.base+"/daily/prefs", map[string]any{"inherit": false, "apis": []any{}})
		if r := read("zh"); r["quote"] != nil {
			t.Errorf("%v", r)
		}
	})
}

// Languages are canonical BCP 47 tags everywhere; what was saved before the
// rule is read as such and written back canonical on the next save.
func TestDailyLanguagesAreCanonicalAndOldValuesAreRead(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		ctx := context.Background()
		st := x.a.st
		old := `{"default":{"kind":"list"},"hosts":["gone.example"],"languages":["zh","pt-br","en"],"apis":[{"id":"old","name":"Old","lang":"zh","url":"` + x.qs.URL + `/q?lang={lang}","text":"data.0.q","refresh":"daily","rps":100,"skipNew":true,"enabled":true,"headers":[]}],"allow":{"own":true,"api":true}}`
		if err := store.SetSiteSetting(ctx, st.Q(), "daily", old, x.now); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReplaceDailyLines(ctx, st.Q(), "zh", []store.DailyLine{{Lang: "zh", Body: "旧的一行"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReplaceDailyLines(ctx, st.Q(), "en", []store.DailyLine{{Lang: "en", Body: "an english line"}}); err != nil {
			t.Fatal(err)
		}
		code, r := x.adm("GET", "/api/admin/daily", nil)
		if code != 200 || fmt.Sprint(r["languages"]) != "[zh-Hans pt-BR en]" || asMap(r["apis"].([]any)[0])["lang"] != "zh-Hans" || r["hosts"] != nil {
			t.Fatalf("%d %v", code, r)
		}
		if l := asMap(r["lines"]); l["total"] != float64(2) || asMap(l["byLang"])["zh-Hans"] != float64(1) || asMap(l["byLang"])["en"] != float64(1) || asMap(l["byLang"])["zh"] != nil {
			t.Fatalf("%v", r["lines"])
		}
		for lang, want := range map[string]string{"zh-CN": "旧的一行", "zh-Hans": "旧的一行", "en-GB": "an english line"} {
			if got := quoteText(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang="+lang)); got != want {
				t.Errorf("%s: %q", lang, got)
			}
		}
		// Saving the lines of the language writes them under its tag, once.
		if code, r := x.adm("PUT", "/api/admin/daily/lines", map[string]any{"lang": "ZH-hans", "text": "新的一行\n另一行"}); code != 200 || asMap(asMap(r["lines"])["byLang"])["zh-Hans"] != float64(2) || r["lines"].(map[string]any)["total"] != float64(3) {
			t.Fatalf("%d %v", code, r)
		}
		counts, _ := store.DailyLineCounts(ctx, st.Q())
		if counts["zh"] != 0 || counts["zh-Hans"] != 2 || counts["en"] != 1 {
			t.Fatalf("%v", counts)
		}
		for _, l := range []string{"cn", "english", "zh_CN"} {
			if code, r := x.adm("PUT", "/api/admin/daily/lines", map[string]any{"lang": l, "text": "x"}); code != 400 || r["error"] != "bad_language" {
				t.Errorf("%s: %d %v", l, code, r)
			}
			if code, r := x.adm("DELETE", "/api/admin/daily/lines?lang="+l, nil); code != 400 || r["error"] != "bad_language" {
				t.Errorf("delete %s: %d %v", l, code, r)
			}
		}
		// The next save of the settings writes everything back canonical.
		if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"languages": []any{"zh", "pt-br"}}); code != 200 || fmt.Sprint(r["languages"]) != "[zh-Hans pt-BR]" {
			t.Fatalf("%d %v", code, r)
		}
		raw, _, err := store.GetSiteSetting(ctx, st.Q(), "daily")
		if err != nil || !strings.Contains(raw, `"languages":["zh-Hans","pt-BR"]`) || !strings.Contains(raw, `"lang":"zh-Hans"`) || strings.Contains(raw, "gone.example") {
			t.Fatalf("%v %s", err, raw)
		}
		// An old interface in zh is chosen for readers of zh-CN, and the language of an interface is a tag.
		x.choose("old")
		if got := quoteText(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=zh-CN")); !strings.HasSuffix(got, "in zh-Hans") {
			t.Errorf("%q", got)
		}
		b := x.qs.api("bad", "cn")
		if code, r := x.putAPI("bad", b); code != 400 || r["error"] != "bad_language" {
			t.Errorf("%d %v", code, r)
		}
		if code, r := x.adm("POST", "/api/admin/daily/try", map[string]any{"api": x.qs.api("bad", ""), "lang": "zh_CN"}); code != 400 || r["error"] != "bad_language" {
			t.Errorf("%d %v", code, r)
		}
	})
}

// A language mapping turns the language into the code the service wants.
func TestInterfaceLanguageMapping(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		b := x.qs.api("mapped", "")
		b["langMap"] = map[string]any{"zh-Hans": "zh", "zh-Hant": "zh-tw", "en": "en_US"}
		code, r := x.putAPI("mapped", b)
		if got := asMap(r["apis"].([]any)[0]); code != 200 || fmt.Sprint(got["langMap"]) != "map[en:en_US zh-Hans:zh zh-Hant:zh-tw]" {
			t.Fatalf("%d %v", code, r)
		}
		x.choose("mapped")
		if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"languages": []any{"zh-Hans", "zh-Hant", "en"}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		for lang, want := range map[string]string{"zh-CN": "in zh", "zh-TW": "in zh-tw", "en-GB": "in en_US"} {
			if got := quoteText(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang="+lang)); !strings.HasSuffix(got, want) {
				t.Errorf("%s: %q", lang, got)
			}
		}
		// A trial is asked in the mapped code too.
		if code, r := x.adm("POST", "/api/admin/daily/try", map[string]any{"api": b, "lang": "zh-Hant", "date": "2026-11-23"}); code != 200 || !strings.Contains(fmt.Sprint(r["url"]), "lang=zh-tw") {
			t.Errorf("%d %v", code, r)
		}
		for name, m := range map[string]any{"a key that is no language": map[string]any{"chinese": "zh"}, "an empty code": map[string]any{"zh-Hans": ""},
			"a long code": map[string]any{"zh-Hans": strings.Repeat("a", 36)}, "a bad code": map[string]any{"zh-Hans": "z h"}, "not a map": []any{"zh"}} {
			c := x.qs.api("mapped", "")
			c["langMap"] = m
			if code, r := x.putAPI("mapped", c); code != 400 {
				t.Errorf("%s: %d %v", name, code, r)
			}
		}
		if code, r := x.putAPI("mapped", map[string]any{"name": "x", "url": x.qs.URL + "/q", "text": "t", "langMap": map[string]any{"chinese": "zh"}}); code != 400 || r["error"] != "bad_language" {
			t.Errorf("%d %v", code, r)
		}
	})
}

// A stub of the services the built-in interfaces ask, so that the tests reach no network.
type stubServices struct {
	mu   sync.Mutex
	hits map[string][]string
}

func (s *stubServices) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	if s.hits == nil {
		s.hits = map[string][]string{}
	}
	s.hits[r.URL.Host] = append(s.hits[r.URL.Host], r.URL.RequestURI())
	s.mu.Unlock()
	body := `{"hitokoto":"山有木兮木有枝","from":"越人歌","from_who":null,"uuid":"abc-123"}`
	if r.URL.Host == "zenquotes.io" {
		body = `[{"q":"Be kind","a":"Plato"}]`
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func (s *stubServices) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, h := range s.hits {
		n += len(h)
	}
	return n
}

func TestBuiltInInterfaces(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		stub := &stubServices{}
		x.fetcher.SetTransport(stub)
		code, r := x.adm("GET", "/api/admin/daily", nil)
		ps, _ := r["providers"].([]any)
		if code != 200 || len(ps) != 8 || len(r["apis"].([]any)) != 0 || r["presets"] != nil {
			t.Fatalf("%d %v", code, r)
		}
		h := asMap(ps[0])
		if h["id"] != "hitokoto" || h["name"] != "一言" || fmt.Sprint(h["languages"]) != "[zh-Hans zh-Hant]" || h["site"] != "https://hitokoto.cn" || h["note"] == "" || h["enabled"] != false ||
			h["refresh"] != "daily" || h["rps"] != float64(20) || h["skipNew"] != true {
			t.Fatalf("%v", h)
		}
		if q := asMap(ps[1]); q["id"] != "zenquotes" || fmt.Sprint(q["languages"]) != "[en]" || q["rps"] != 0.1 || q["enabled"] != false || q["skipNew"] != false {
			t.Fatalf("%v", q)
		}
		if f := asMap(ps[2]); f["id"] != "favqs" || fmt.Sprint(f["languages"]) != "[en]" || f["rps"] != float64(1) || f["enabled"] != false {
			t.Fatalf("%v", f)
		}
		// Only administrators; an unknown one is not found.
		for name, c := range map[string]caller{"owner": x.own, "viewer": x.viewer} {
			if code, _ := c("PUT", "/api/admin/daily/providers/hitokoto", map[string]any{"enabled": true}); code != 403 {
				t.Errorf("%s: %d", name, code)
			}
		}
		if code, _ := x.adm("PUT", "/api/admin/daily/providers/nothing", map[string]any{"enabled": true}); code != 404 {
			t.Errorf("%d", code)
		}
		if code, _ := x.adm("PUT", "/api/admin/daily/providers/hitokoto", map[string]any{}); code != 400 {
			t.Errorf("no enabled: %d", code)
		}
		// Nothing is contacted, and nothing can be chosen, before an administrator enables it.
		if code, r := x.own("PUT", x.base+"/daily/prefs", map[string]any{"inherit": true, "apis": []any{"hitokoto"}}); code != 400 {
			t.Errorf("%d %v", code, r)
		}
		if r := quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=zh-CN"); r["quote"] != nil || stub.count() != 0 {
			t.Fatalf("%v (%d requests)", r, stub.count())
		}
		// Enabled, it is an interface among the others, marked built in.
		code, r = x.adm("PUT", "/api/admin/daily/providers/hitokoto", map[string]any{"enabled": true})
		apis := r["apis"].([]any)
		if code != 200 || len(apis) != 1 || stub.count() != 0 {
			t.Fatalf("%d %v", code, r)
		}
		a := asMap(apis[0])
		if a["id"] != "hitokoto" || a["builtin"] != true || a["url"] != "https://international.v1.hitokoto.cn/?max_length=80" || a["text"] != "hitokoto" || a["source"] != "from" || a["author"] != "from_who" ||
			a["link"] != "https://hitokoto.cn?uuid={uuid}" || a["rps"] != float64(20) || a["enabled"] != true || len(a["headers"].([]any)) != 0 {
			t.Fatalf("%v", a)
		}
		if asMap(asMap(r["providers"].([]any)[0]))["enabled"] != true {
			t.Fatalf("%v", r["providers"])
		}
		x.choose("hitokoto")
		// 一言 has Chinese only: a reader of French is given nothing of it.
		if r := quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=fr"); r["quote"] != nil {
			t.Errorf("fr: %v", r)
		}
		for _, lang := range []string{"zh-CN", "zh-TW"} {
			r := quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang="+lang)
			if q := asMap(r["quote"]); q == nil || q["text"] != "山有木兮木有枝" || q["source"] != "越人歌" || q["author"] != nil || q["link"] != "https://hitokoto.cn?uuid=abc-123" {
				t.Errorf("%s: %v", lang, r)
			}
		}
		if got := stub.hits["international.v1.hitokoto.cn"]; len(got) != 1 || got[0] != "/?max_length=80" || len(stub.hits) != 1 {
			t.Fatalf("%v", stub.hits)
		}
		// A reader of English with both chosen is given the one made for English; one of zh-Hant, the one for it.
		if code, r := x.adm("PUT", "/api/admin/daily/providers/zenquotes", map[string]any{"enabled": true, "rps": 0.05}); code != 200 || asMap(asMap(r["providers"].([]any)[1]))["rps"] != 0.05 {
			t.Fatalf("%d %v", code, r)
		}
		x.choose("zenquotes", "hitokoto")
		if q := asMap(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en-GB")["quote"]); q == nil || q["text"] != "Be kind" || q["author"] != "Plato" {
			t.Errorf("%v", q)
		}
		if q := asMap(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=zh-HK")["quote"]); q == nil || q["text"] != "山有木兮木有枝" {
			t.Errorf("%v", q)
		}
		// Tuning is checked: not above what the service allows.
		for name, b := range map[string]map[string]any{"too fast": {"enabled": true, "rps": 21}, "a refresh that is none": {"enabled": true, "refresh": "weekly"}, "minutes with none": {"enabled": true, "refresh": "minutes"}} {
			if code, r := x.adm("PUT", "/api/admin/daily/providers/hitokoto", b); code != 400 {
				t.Errorf("%s: %d %v", name, code, r)
			}
		}
		if code, r := x.adm("PUT", "/api/admin/daily/providers/hitokoto", map[string]any{"enabled": true, "refresh": "minutes", "minutes": 30, "rps": 5, "skipNew": false}); code != 200 {
			t.Fatalf("%d %v", code, r)
		} else if a := asMap(r["apis"].([]any)[0]); a["refresh"] != "minutes" || a["refreshMinutes"] != float64(30) || a["rps"] != float64(5) || a["skipNew"] != false {
			t.Fatalf("%v", a)
		}
		// What is fixed cannot be changed, through the interfaces' own address either.
		for name, mod := range map[string]func(map[string]any){
			"the address": func(b map[string]any) { b["url"] = "https://evil.example/q" },
			"the text":    func(b map[string]any) { b["text"] = "other" },
			"the source":  func(b map[string]any) { b["source"] = "other" },
			"the author":  func(b map[string]any) { b["author"] = "other" },
			"the link":    func(b map[string]any) { b["link"] = "https://evil.example/{uuid}" },
			"the mapping": func(b map[string]any) { b["langMap"] = map[string]any{"zh-Hans": "zh"} },
			"a header":    func(b map[string]any) { b["headers"] = []any{map[string]any{"name": "X-A", "value": "v"}} },
		} {
			b := map[string]any{"id": "hitokoto", "name": "一言", "lang": "zh-Hans", "url": "https://international.v1.hitokoto.cn/?max_length=80", "text": "hitokoto", "source": "from", "author": "from_who",
				"link": "https://hitokoto.cn?uuid={uuid}", "enabled": true}
			mod(b)
			if code, r := x.putAPI("hitokoto", b); code != 400 || r["error"] != "builtin" {
				t.Errorf("%s: %d %v", name, code, r)
			}
		}
		if code, r := x.adm("DELETE", "/api/admin/daily/apis/hitokoto", nil); code != 400 || r["error"] != "builtin" {
			t.Errorf("%d %v", code, r)
		}
		// Switched off, it is gone from the interfaces and from what spaces can use; its settings stay.
		code, r = x.adm("PUT", "/api/admin/daily/providers/hitokoto", map[string]any{"enabled": false})
		if code != 200 || len(r["apis"].([]any)) != 1 || asMap(r["apis"].([]any)[0])["id"] != "zenquotes" || asMap(r["providers"].([]any)[0])["rps"] != float64(5) {
			t.Fatalf("%d %v", code, r)
		}
		if q := quoteOf(t, x.viewer, x.base, "date=2026-11-24&lang=zh-CN")["quote"]; asMap(q)["text"] == "山有木兮木有枝" {
			t.Errorf("a disabled interface is used: %v", q)
		}
	})
}

// There is no list of hosts: one that is sent is ignored, and an interface may
// only contact the host and port of its own address.
func TestNoListOfHosts(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"hosts": []any{"a.example"}, "languages": []any{"en"}}); code != 200 || r["hosts"] != nil || fmt.Sprint(r["languages"]) != "[en]" {
			t.Fatalf("%d %v", code, r)
		}
		if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"hosts": "not a list"}); code != 200 || r["hosts"] != nil {
			t.Fatalf("%d %v", code, r)
		}
		// An interface works with no host saved anywhere, and a trial too.
		x.putAPI("own", x.qs.api("own", ""))
		x.choose("own")
		if got := quoteText(quoteOf(t, x.viewer, x.base, "date=2026-11-23&lang=en")); got == "" {
			t.Fatal("no text")
		}
		if code, r := x.adm("POST", "/api/admin/daily/try", map[string]any{"api": x.qs.api("t", ""), "lang": "en"}); code != 200 || r["ok"] != true {
			t.Fatalf("%d %v", code, r)
		}
		// The host written is the only one: a placeholder cannot make another.
		b := x.qs.api("own", "")
		b["url"] = "http://{lang}.example.test/q"
		if code, r := x.putAPI("own", b); code != 400 {
			t.Errorf("%d %v", code, r)
		}
	})
}
