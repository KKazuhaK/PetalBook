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

// twoServices answers as ZenQuotes and FavQs do, each with a text of its own,
// and can be made to fail by host.
type twoServices struct {
	mu   sync.Mutex
	down map[string]bool
}

func (s *twoServices) fail(host string, v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down == nil {
		s.down = map[string]bool{}
	}
	s.down[host] = v
}

func (s *twoServices) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	down := s.down[r.URL.Host]
	s.mu.Unlock()
	status, body := 200, `[{"q":"zen","a":"Plato"}]`
	switch r.URL.Host {
	case "favqs.com":
		body = `{"quote":{"body":"fav","author":"Buddha","author_permalink":"buddha","id":1}}`
	case "international.v1.hitokoto.cn":
		body = `{"hitokoto":"hito","from":"x","from_who":null,"uuid":"u"}`
	}
	if down {
		status, body = 500, `{}`
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func (x *extFixture) setDefault(apis map[string]any, options map[string]any) {
	x.t.Helper()
	d := map[string]any{"kind": "api", "apis": apis}
	if options != nil {
		d["options"] = options
	}
	if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"default": d}); code != 200 {
		x.t.Fatalf("%d %v", code, r)
	}
}

func (x *extFixture) enableAll() {
	for _, id := range []string{"zenquotes", "favqs", "hitokoto"} {
		x.adm("PUT", "/api/admin/daily/providers/"+id, map[string]any{"enabled": true, "rps": 0.1})
	}
	// ZenQuotes at 0.1 is its most; the others are tuned to it so that several days can be asked.
	x.adm("PUT", "/api/admin/daily/providers/favqs", map[string]any{"enabled": true, "rps": 1})
	x.adm("PUT", "/api/admin/daily/providers/hitokoto", map[string]any{"enabled": true, "rps": 5})
}

func TestSeveralInterfacesTakeTurnsByDay(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		x.fetcher.SetTransport(&twoServices{})
		x.enableAll()
		x.setDefault(map[string]any{"": []any{"hitokoto", "favqs"}}, nil)
		// Each ask is a moment later, so that the rate limits do not come into it.
		ask := func(c caller, base, query string) string {
			x.now = x.now.Add(2 * time.Second)
			return quoteText(quoteOf(t, c, base, query))
		}
		on := func(c caller, base, day string) string { return ask(c, base, "date="+day+"&lang=en") }
		d22, d23, d24 := on(x.own, x.base, "2026-11-22"), on(x.own, x.base, "2026-11-23"), on(x.own, x.base, "2026-11-24")
		// They take turns: two days in a row differ, and the third is the first again.
		if d22 == d23 || d22 != d24 || d22 == "" || d23 == "" {
			t.Fatalf("%q %q %q", d22, d23, d24)
		}
		// Everybody in the scope sees the same, in another space too (the site's default).
		other := x.otherSpace()
		if on(x.viewer, x.base, "2026-11-23") != d23 || on(x.own, other, "2026-11-23") != d23 || on(x.own, other, "2026-11-22") != d22 {
			t.Fatal("the scope does not share the turn")
		}
		// "Another" moves to the next; a lap round the list is the first again.
		a0 := ask(x.own, x.base, "date=2026-11-23&lang=en")
		a1 := ask(x.own, x.base, "date=2026-11-23&lang=en&skip=1")
		a2 := ask(x.own, x.base, "date=2026-11-23&lang=en&skip=2")
		if a0 != d23 || a1 == a0 || a1 == "" || a2 != a0 {
			t.Fatalf("another: %q %q %q", a0, a1, a2)
		}
	})
}

func TestAnInterfaceThatFailsIsPassedOverForTheNext(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		svc := &twoServices{}
		x.fetcher.SetTransport(svc)
		x.enableAll()
		x.setDefault(map[string]any{"": []any{"hitokoto", "favqs"}}, nil)
		text := func(day string) string {
			x.now = x.now.Add(2 * time.Second)
			return quoteText(quoteOf(t, x.own, x.base, "date="+day+"&lang=en"))
		}
		svc.fail("international.v1.hitokoto.cn", true)
		for _, day := range []string{"2026-11-22", "2026-11-23", "2026-11-24"} {
			if got := text(day); got != "fav" {
				t.Fatalf("hitokoto is down on %s: %q", day, got)
			}
		}
		// Rate limited counts the same: favqs at once after a request of its own is limited, and
		// hitokoto, which answers, is used.
		svc.fail("international.v1.hitokoto.cn", false)
		x.adm("PUT", "/api/admin/daily/providers/favqs", map[string]any{"enabled": true, "rps": 0.01})
		x.now = x.now.Add(time.Hour)
		if got := text("2026-11-22"); got != "hito" && got != "fav" {
			t.Fatalf("%q", got)
		}
		// None answers: the last thing said is shown as stale, and with nothing said, the text is unavailable.
		svc.fail("international.v1.hitokoto.cn", true)
		svc.fail("favqs.com", true)
		x.now = x.now.Add(48 * time.Hour)
		r := quoteOf(t, x.own, x.base, "date=2026-11-25&lang=en")
		if q := asMap(r["quote"]); q != nil && q["stale"] != true {
			t.Fatalf("%v", r)
		}
		x.adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "api", "apis": map[string]any{"": []any{"zenquotes"}}}})
		svc.fail("zenquotes.io", true)
		if r := quoteOf(t, x.own, x.base, "date=2026-11-25&lang=en"); r["quote"] != nil || r["reason"] != "unavailable" {
			t.Fatalf("%v", r)
		}
	})
}

func TestADefaultWrittenWithOneIdIsAListOfOne(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		x.fetcher.SetTransport(&twoServices{})
		x.enableAll()
		x.setDefault(map[string]any{"": "zenquotes", "zh-Hans": []any{"hitokoto"}}, nil)
		_, r := x.adm("GET", "/api/admin/daily", nil)
		if got := fmt.Sprint(asMap(r["default"])["apis"]); got != "map[:[zenquotes] zh-Hans:[hitokoto]]" {
			t.Fatalf("%s", got)
		}
		if got := quoteText(quoteOf(t, x.own, x.base, "date=2026-11-23&lang=en")); got != "zen" {
			t.Fatalf("one interface: %q", got)
		}
		for name, apis := range map[string]any{"an empty list": map[string]any{"": []any{}}, "too many": map[string]any{"": []any{"a", "b", "c", "d", "e", "f", "g", "h", "i"}},
			"an unknown id": map[string]any{"": []any{"zenquotes", "nothing"}}, "a number": map[string]any{"": 3}} {
			if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"default": map[string]any{"kind": "api", "apis": apis}}); code != 400 {
				t.Errorf("%s: %d %v", name, code, r)
			}
		}
	})
}

// A space's own list takes the turns too, and each interface keeps its own options; the lock keeps it unused.
func TestSeveralInterfacesInASpaceAndTheLock(t *testing.T) {
	withExternal(t, func(x *extFixture) {
		t := x.t
		x.fetcher.SetTransport(&twoServices{})
		x.enableAll()
		x.setDefault(map[string]any{"": []any{"hitokoto"}}, nil)
		x.choose("zenquotes", "favqs")
		d22 := quoteText(quoteOf(t, x.own, x.base, "date=2026-11-22&lang=en"))
		x.now = x.now.Add(15 * time.Second)
		d23 := quoteText(quoteOf(t, x.own, x.base, "date=2026-11-23&lang=en"))
		// Only those made for the reader's language take part (hitokoto is not).
		if d22 == d23 || (d22 != "zen" && d22 != "fav") || (d23 != "zen" && d23 != "fav") {
			t.Fatalf("%q %q", d22, d23)
		}
		if code, r := x.adm("PUT", "/api/admin/daily", map[string]any{"allow": map[string]any{"api": false}}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		if got := quoteText(quoteOf(t, x.own, x.base, "date=2026-11-23&lang=en")); got != "hito" {
			t.Fatalf("locked: %q", got)
		}
	})
}
