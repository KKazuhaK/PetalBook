package api_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// categoryServices answers like 一言: the sentence says which categories were
// asked for, and each request is kept.
type categoryServices struct {
	mu   sync.Mutex
	uris []string
}

func (s *categoryServices) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.uris = append(s.uris, r.URL.Host+r.URL.RequestURI())
	s.mu.Unlock()
	body := `[{"q":"zen","a":"Plato"}]`
	if r.URL.Host == "international.v1.hitokoto.cn" {
		cats := r.URL.Query()["c"]
		body = fmt.Sprintf(`{"hitokoto":"c=%s","from":"x","from_who":null,"uuid":"u"}`, strings.Join(cats, ","))
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func (s *categoryServices) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.uris...)
}

func (x *extFixture) otherSpace() string {
	x.t.Helper()
	code, r := x.own("POST", "/api/spaces", map[string]any{"name": "Other"})
	if code != 201 {
		x.t.Fatalf("%d %v", code, r)
	}
	return "/api/spaces/" + r["id"].(string)
}

func (x *extFixture) choose2(base string, apis []any, options map[string]any) (int, map[string]any) {
	body := map[string]any{"inherit": true, "apis": apis}
	if options != nil {
		body["options"] = options
	}
	return x.own("PUT", base+"/daily/prefs", body)
}

func TestBuiltInOptionsAreListedAndChecked(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		x.fetcher.SetTransport(&categoryServices{})
		_, r := x.adm("GET", "/api/admin/daily", nil)
		ps := asSlice(r["providers"])
		opts := asSlice(asMap(ps[0])["options"])
		if len(opts) != 1 || asMap(opts[0])["key"] != "categories" || asMap(opts[0])["param"] != "c" || asMap(opts[0])["kind"] != "multi" || len(asSlice(asMap(opts[0])["values"])) != 12 {
			t.Fatalf("%v", asMap(ps[0])["options"])
		}
		v := asMap(asSlice(asMap(opts[0])["values"])[8])
		if v["value"] != "i" || fmt.Sprint(v["label"]) != "map[en:Poetry zh:诗词]" {
			t.Fatalf("%v", v)
		}
		if len(asSlice(asMap(ps[1])["options"])) != 0 {
			t.Fatalf("zenquotes has no options: %v", ps[1])
		}
		x.adm("PUT", "/api/admin/daily/providers/hitokoto", map[string]any{"enabled": true})
		x.adm("PUT", "/api/admin/daily/providers/zenquotes", map[string]any{"enabled": true})
		_, p := x.own("GET", x.base+"/daily/prefs", nil)
		for _, a := range asSlice(p["available"]) {
			if m := asMap(a); (m["id"] == "hitokoto") != (len(asSlice(m["options"])) == 1) {
				t.Fatalf("options of %v: %v", m["id"], m["options"])
			}
		}
		// Only declared values of declared options of chosen built-in interfaces.
		for name, c := range map[string]struct {
			apis []any
			opts map[string]any
		}{
			"a value that is none":   {[]any{"hitokoto"}, map[string]any{"hitokoto": map[string]any{"categories": []any{"z"}}}},
			"an option that is none": {[]any{"hitokoto"}, map[string]any{"hitokoto": map[string]any{"genre": []any{"a"}}}},
			"an interface without":   {[]any{"zenquotes"}, map[string]any{"zenquotes": map[string]any{"categories": []any{"a"}}}},
			"one that is not chosen": {[]any{"zenquotes"}, map[string]any{"hitokoto": map[string]any{"categories": []any{"a"}}}},
			"a value that is a list": {[]any{"hitokoto"}, map[string]any{"hitokoto": map[string]any{"categories": "a"}}},
		} {
			if code, r := x.choose2(x.base, c.apis, c.opts); code != 400 {
				t.Errorf("%s: %d %v", name, code, r)
			}
		}
		// Saved in the order they are declared, each once; none, or all, is all.
		code, r := x.choose2(x.base, []any{"hitokoto"}, map[string]any{"hitokoto": map[string]any{"categories": []any{"i", "d", "i"}}})
		if code != 200 || fmt.Sprint(r["options"]) != "map[hitokoto:map[categories:[d i]]]" {
			t.Fatalf("%d %v", code, r)
		}
		code, r = x.choose2(x.base, []any{"hitokoto"}, map[string]any{"hitokoto": map[string]any{"categories": []any{}}})
		if code != 200 || len(asMap(r["options"])) != 0 {
			t.Fatalf("%d %v", code, r)
		}
		all := []any{}
		for _, c := range "abcdefghijkl" {
			all = append(all, string(c))
		}
		if code, r = x.choose2(x.base, []any{"hitokoto"}, map[string]any{"hitokoto": map[string]any{"categories": all}}); code != 200 || len(asMap(r["options"])) != 0 {
			t.Fatalf("%d %v", code, r)
		}
		// The same in the site default.
		def := func(opts any) (int, map[string]any) {
			return x.adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "api", "apis": map[string]any{"zh-Hans": []any{"hitokoto"}}, "options": opts}})
		}
		if code, r := def(map[string]any{"hitokoto": map[string]any{"categories": []any{"zz"}}}); code != 400 {
			t.Errorf("%d %v", code, r)
		}
		if code, r := def(map[string]any{"zenquotes": map[string]any{"categories": []any{"a"}}}); code != 400 {
			t.Errorf("%d %v", code, r)
		}
		if code, r := def(map[string]any{"hitokoto": map[string]any{"categories": []any{"k", "a"}}}); code != 200 || fmt.Sprint(asMap(r["default"])["options"]) != "map[hitokoto:map[categories:[a k]]]" {
			t.Fatalf("%d %v", code, r)
		}
	})
}

func TestBuiltInOptionsMakeTheAddressAndSeparateTheCache(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		stub := &categoryServices{}
		x.fetcher.SetTransport(stub)
		x.adm("PUT", "/api/admin/daily/providers/hitokoto", map[string]any{"enabled": true, "rps": 20})
		other := x.otherSpace()
		third := x.otherSpace()
		fourth := x.otherSpace()
		text := func(base string) string {
			return quoteText(quoteOf(t, x.viewer, base, "date=2026-11-23&lang=zh-CN"))
		}
		if code, r := x.choose2(x.base, []any{"hitokoto"}, map[string]any{"hitokoto": map[string]any{"categories": []any{"i"}}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		x.choose2(other, []any{"hitokoto"}, map[string]any{"hitokoto": map[string]any{"categories": []any{"k", "d"}}})
		x.choose2(third, []any{"hitokoto"}, map[string]any{"hitokoto": map[string]any{"categories": []any{"d", "k"}}})
		x.choose2(fourth, []any{"hitokoto"}, nil)
		// The viewer reads a space they may not be a member of in these new spaces, so use the owner.
		own := func(base string) string { return quoteText(quoteOf(t, x.own, base, "date=2026-11-23&lang=zh-CN")) }
		if got := text(x.base); got != "c=i" {
			t.Fatalf("one category: %q", got)
		}
		if got := own(other); got != "c=d,k" {
			t.Fatalf("two categories: %q", got)
		}
		// The same categories, written in another order, are the same item and one request.
		before := len(stub.requests())
		if got := own(third); got != "c=d,k" || len(stub.requests()) != before {
			t.Fatalf("shared: %q (%d requests more)", got, len(stub.requests())-before)
		}
		if got := own(fourth); got != "c=" {
			t.Fatalf("all: %q", got)
		}
		uris := strings.Join(stub.requests(), " ")
		for _, want := range []string{"/?max_length=80&c=i", "/?max_length=80&c=d&c=k", "/?max_length=80 "} {
			if !strings.Contains(uris+" ", want) {
				t.Errorf("no request %q in %s", want, uris)
			}
		}
		if n := len(stub.requests()); n != 3 {
			t.Errorf("%d requests: %s", n, uris)
		}
	})
}

// One lane for a provider: the rate limit holds across its sets of options.
func TestBuiltInRateLimitIsSharedAcrossOptionSets(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		stub := &categoryServices{}
		x.fetcher.SetTransport(stub)
		x.adm("PUT", "/api/admin/daily/providers/hitokoto", map[string]any{"enabled": true, "rps": 0.5})
		other := x.otherSpace()
		x.choose2(x.base, []any{"hitokoto"}, map[string]any{"hitokoto": map[string]any{"categories": []any{"a"}}})
		x.choose2(other, []any{"hitokoto"}, map[string]any{"hitokoto": map[string]any{"categories": []any{"b"}}})
		if got := quoteText(quoteOf(t, x.own, x.base, "date=2026-11-23&lang=zh-CN")); got != "c=a" {
			t.Fatalf("%q", got)
		}
		code, r := x.own("GET", other+"/daily?date=2026-11-23&lang=zh-CN", nil)
		if code == 200 && quoteText(r) != "" {
			t.Errorf("the second set of categories was asked at once: %v", r)
		}
		if n := len(stub.requests()); n != 1 {
			t.Errorf("%d requests: %v", n, stub.requests())
		}
		// A little later the token is back.
		x.now = x.now.Add(3 * time.Second)
		if got := quoteText(quoteOf(t, x.own, other, "date=2026-11-23&lang=zh-CN")); got != "c=b" {
			t.Errorf("%q", got)
		}
	})
}

// The default's options apply to a space that chose nothing; a space's own choice replaces them.
func TestBuiltInOptionsFollowTheLayers(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		x.fetcher.SetTransport(&categoryServices{})
		x.adm("PUT", "/api/admin/daily/providers/hitokoto", map[string]any{"enabled": true, "rps": 20})
		if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "api", "apis": map[string]any{"": []any{"hitokoto"}},
			"options": map[string]any{"hitokoto": map[string]any{"categories": []any{"k", "a"}}}}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		q := func() string { return quoteText(quoteOf(t, x.own, x.base, "date=2026-11-23&lang=zh-CN")) }
		if got := q(); got != "c=a,k" {
			t.Fatalf("default: %q", got)
		}
		x.choose2(x.base, []any{"hitokoto"}, map[string]any{"hitokoto": map[string]any{"categories": []any{"i"}}})
		if got := q(); got != "c=i" {
			t.Fatalf("space: %q", got)
		}
		// The space stops using it: the default again.
		x.choose2(x.base, []any{}, nil)
		if got := q(); got != "c=a,k" {
			t.Fatalf("default again: %q", got)
		}
		// Locked: what the space chose is kept and not used.
		x.choose2(x.base, []any{"hitokoto"}, map[string]any{"hitokoto": map[string]any{"categories": []any{"i"}}})
		if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"allow": map[string]any{"api": false}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		if got := q(); got != "c=a,k" {
			t.Fatalf("locked: %q", got)
		}
	})
}
