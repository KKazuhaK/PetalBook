package dailyfetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublic(t *testing.T) {
	for ip, want := range map[string]bool{
		"8.8.8.8": true, "1.1.1.1": true, "93.184.216.34": true, "2606:4700:4700::1111": true,
		"127.0.0.1": false, "127.1.2.3": false, "::1": false, "10.0.0.1": false, "172.16.5.5": false, "172.31.255.255": false,
		"192.168.1.1": false, "169.254.169.254": false, "169.254.0.1": false, "fe80::1": false, "fd00::1": false, "fc00::1": false,
		"0.0.0.0": false, "::": false, "224.0.0.1": false, "ff02::1": false, "255.255.255.255": false,
		"100.64.0.1": false, "100.127.255.255": false, "100.128.0.1": true, "192.0.2.1": false, "198.51.100.1": false, "203.0.113.9": false,
		"198.18.0.1": false, "240.0.0.1": false, "2001:db8::1": false,
		"::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false, "::ffff:169.254.169.254": false, "::ffff:8.8.8.8": true,
		"64:ff9b::7f00:1": false, "2002:7f00:1::": false, "172.32.0.1": true,
	} {
		if got := Public(netip.MustParseAddr(ip)); got != want {
			t.Errorf("%s: %v, want %v", ip, got, want)
		}
	}
}

func TestCheckAPI(t *testing.T) {
	hosts := []string{"v1.hitokoto.cn", "api.example.org:8443"}
	ok := API{ID: "hito", Name: "一言", Lang: "zh", URL: "https://v1.hitokoto.cn/?c=d", Text: "hitokoto", Source: "from", Author: "from_who"}
	if _, err := CheckAPI(ok, hosts, false); err != nil {
		t.Fatal(err)
	}
	good := ok
	good.URL, good.Lang = "https://api.example.org:8443/q/{lang}?l={lang}", ""
	if _, err := CheckAPI(good, hosts, false); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*API){
		"http":                          func(a *API) { a.URL = "http://v1.hitokoto.cn/" },
		"another host":                  func(a *API) { a.URL = "https://evil.example/" },
		"the host with a user":          func(a *API) { a.URL = "https://v1.hitokoto.cn@evil.example/" },
		"a user":                        func(a *API) { a.URL = "https://u:p@v1.hitokoto.cn/" },
		"a port not listed":             func(a *API) { a.URL = "https://v1.hitokoto.cn:444/" },
		"lang in the host":              func(a *API) { a.URL = "https://{lang}.hitokoto.cn/" },
		"a fragment":                    func(a *API) { a.URL = "https://v1.hitokoto.cn/#x" },
		"spaces":                        func(a *API) { a.URL = "https://v1.hitokoto.cn/ x" },
		"no text path":                  func(a *API) { a.Text = "" },
		"a bad id":                      func(a *API) { a.ID = "A B" },
		"no name":                       func(a *API) { a.Name = " " },
		"a bad lang":                    func(a *API) { a.Lang = "中文" },
		"an address that is no address": func(a *API) { a.URL = "not a url" },
		"loopback not listed":           func(a *API) { a.URL = "https://127.0.0.1/" },
	} {
		a := ok
		mod(&a)
		if _, err := CheckAPI(a, hosts, false); err == nil {
			t.Errorf("%s is accepted", name)
		}
	}
	// http only where it is allowed (tests).
	a := ok
	a.URL = "http://v1.hitokoto.cn/"
	if _, err := CheckAPI(a, hosts, true); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"", "a/b", "https://x", "x y", "a@b", "x?y"} {
		if _, err := CheckHost(h); err == nil {
			t.Errorf("host %q is accepted", h)
		}
	}
	if h, err := CheckHost(" Example.COM:8080 "); err != nil || h != "example.com:8080" {
		t.Fatalf("%q %v", h, err)
	}
}

func TestExtract(t *testing.T) {
	a := API{Text: "data.0.q", Source: "data.0.meta.from", Author: "data.0.a"}
	q, err := Extract(a, []byte(`{"data":[{"q":" 山高月小 ","a":"苏轼","meta":{"from":"后赤壁赋"}}]}`))
	if err != nil || q.Text != "山高月小" || q.Author != "苏轼" || q.Source != "后赤壁赋" {
		t.Fatalf("%+v %v", q, err)
	}
	// [0].q, numbers, and the missing optional ones.
	q, err = Extract(API{Text: "[1].n"}, []byte(`[{"n":1},{"n":2.5}]`))
	if err != nil || q.Text != "2.5" || q.Source != "" {
		t.Fatalf("%+v %v", q, err)
	}
	for name, body := range map[string]string{"not JSON": `nope`, "no such path": `{"x":1}`, "a list at the end": `{"data":[1]}`, "blank": `{"data":[{"q":"  "}]}`, "an object": `{"data":[{"q":{"a":1}}]}`, "out of range": `{"data":[]}`} {
		if _, err := Extract(a, []byte(body)); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	long := strings.Repeat("字", 3000)
	q, err = Extract(API{Text: "t"}, []byte(`{"t":"`+long+`"}`))
	if err != nil || len([]rune(q.Text)) != MaxTextChars {
		t.Fatalf("%d %v", len([]rune(q.Text)), err)
	}
}

func serve(t *testing.T, h http.HandlerFunc) (*httptest.Server, API, []string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	return srv, API{ID: "t", Name: "t", URL: srv.URL + "/q?lang={lang}", Text: "text", Source: "from", Author: "who"}, []string{host}
}

func jsonOK(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	fmt.Fprint(w, body)
}

func TestGetAsksOncePerDayAndLanguage(t *testing.T) {
	var n atomic.Int64
	var langs sync.Map
	_, api, hosts := serve(t, func(w http.ResponseWriter, r *http.Request) {
		k := n.Add(1)
		langs.Store(r.URL.Query().Get("lang"), true)
		jsonOK(w, fmt.Sprintf(`{"text":"line %d","from":"src","who":"me"}`, k))
	})
	f := NewForTests()
	ctx := context.Background()
	q1, err := f.Get(ctx, api, hosts, "zh", "2026-11-23", 0)
	if err != nil || q1.Text != "line 1" || q1.Source != "src" || q1.Author != "me" {
		t.Fatalf("%+v %v", q1, err)
	}
	// 20 people asking: the same text, no more requests.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if q, err := f.Get(ctx, api, hosts, "zh", "2026-11-23", 0); err != nil || q != q1 {
				t.Errorf("%+v %v", q, err)
			}
		}()
	}
	wg.Wait()
	if n.Load() != 1 {
		t.Fatalf("%d requests", n.Load())
	}
	// Another language or day is asked for on its own, with the language in the address.
	if q, _ := f.Get(ctx, api, hosts, "en", "2026-11-23", 0); q.Text != "line 2" {
		t.Fatalf("%+v", q)
	}
	if q, _ := f.Get(ctx, api, hosts, "zh", "2026-11-24", 0); q.Text != "line 3" {
		t.Fatalf("%+v", q)
	}
	if _, ok := langs.Load("zh"); !ok {
		t.Error("{lang} was not filled in")
	}
	// "Another": a new request, and the same one again for the same skip.
	q4, _ := f.Get(ctx, api, hosts, "zh", "2026-11-23", 1)
	if q4.Text != "line 4" {
		t.Fatalf("%+v", q4)
	}
	if again, _ := f.Get(ctx, api, hosts, "zh", "2026-11-23", 1); again != q4 {
		t.Fatalf("%+v", again)
	}
	// A different text path is a different interface for the cache.
	api2 := api
	api2.Text = "from"
	if q, _ := f.Get(ctx, api2, hosts, "zh", "2026-11-23", 0); q.Text != "src" {
		t.Fatalf("%+v", q)
	}
}

func TestGetRateLimitsRequests(t *testing.T) {
	var n atomic.Int64
	_, api, hosts := serve(t, func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, fmt.Sprintf(`{"text":"%d"}`, n.Add(1)))
	})
	f := NewForTests()
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	f.SetClock(func() time.Time { return now })
	ctx := context.Background()
	for skip := 0; skip < perMinute; skip++ {
		if _, err := f.Get(ctx, api, hosts, "en", "2026-11-23", skip); err != nil {
			t.Fatalf("%d: %v", skip, err)
		}
	}
	if _, err := f.Get(ctx, api, hosts, "en", "2026-11-23", perMinute); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("%v", err)
	}
	// Those already answered are not asked for again, so they cost no rate.
	if _, err := f.Get(ctx, api, hosts, "en", "2026-11-23", 3); err != nil {
		t.Fatal(err)
	}
	// Dates cannot be used to get round it.
	if _, err := f.Get(ctx, api, hosts, "en", "2026-12-25", 0); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("%v", err)
	}
	now = now.Add(61 * time.Second)
	if _, err := f.Get(ctx, api, hosts, "en", "2026-11-23", perMinute); err != nil {
		t.Fatal(err)
	}
	if n.Load() != perMinute+1 {
		t.Fatalf("%d requests went out", n.Load())
	}
}

func TestGetFailuresHideTheBlockAndAreRemembered(t *testing.T) {
	var n atomic.Int64
	mode := atomic.Value{}
	mode.Store("500")
	_, api, hosts := serve(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		switch mode.Load() {
		case "500":
			http.Error(w, "boom", 500)
		case "html":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `{"text":"x"}`)
		case "big":
			jsonOK(w, `{"text":"`+strings.Repeat("a", MaxResponse)+`"}`)
		case "redirect":
			http.Redirect(w, r, "http://127.0.0.1:1/secret", http.StatusFound)
		case "garbage":
			jsonOK(w, `{"text":`)
		case "ok":
			jsonOK(w, `{"text":"fine"}`)
		}
	})
	f := NewForTests()
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	f.SetClock(func() time.Time { return now })
	ctx := context.Background()
	for _, m := range []string{"500", "html", "big", "redirect", "garbage"} {
		mode.Store(m)
		_, err := f.Get(ctx, api, hosts, m, "2026-11-23", 0) // a language each, so each is asked
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v", m, err)
		}
	}
	// A failure is not asked for again at once, and is after a few minutes.
	mode.Store("ok")
	before := n.Load()
	if _, err := f.Get(ctx, api, hosts, "500", "2026-11-23", 0); !errors.Is(err, ErrUnavailable) || n.Load() != before {
		t.Fatalf("%v %d", err, n.Load()-before)
	}
	now = now.Add(failureMemory + time.Second)
	if q, err := f.Get(ctx, api, hosts, "500", "2026-11-23", 0); err != nil || q.Text != "fine" {
		t.Fatalf("%+v %v", q, err)
	}
	// A host that was taken off the list is not asked.
	if _, err := f.Get(ctx, api, []string{"other.example"}, "zz", "2026-11-23", 0); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v", err)
	}
}

func TestGetTimesOut(t *testing.T) {
	_, api, hosts := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
		jsonOK(w, `{"text":"late"}`)
	})
	f := NewForTests()
	f.SetTimeout(150 * time.Millisecond)
	start := time.Now()
	if _, err := f.Get(context.Background(), api, hosts, "en", "2026-11-23", 0); !errors.Is(err, ErrUnavailable) || time.Since(start) > 2*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}

// The real fetcher does not connect to this machine or its network, whatever
// the address says and the list allows.
func TestTheServersFetcherRefusesPrivateAddresses(t *testing.T) {
	var hit atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		jsonOK(w, `{"text":"inside"}`)
	}))
	defer srv.Close()
	hostport := strings.TrimPrefix(srv.URL, "https://")
	port := hostport[strings.LastIndex(hostport, ":"):]
	f := New()
	for _, h := range []string{hostport, "localhost" + port, "[::1]" + port, "0.0.0.0" + port, "127.1" + port} {
		a := API{ID: "t", Name: "t", URL: "https://" + h + "/", Text: "text"}
		if _, err := f.Get(context.Background(), a, []string{h}, "en", "2026-11-23", 0); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v", h, err)
		}
	}
	if hit.Load() != 0 {
		t.Fatalf("the server inside was reached %d times", hit.Load())
	}
	// http is refused outright.
	a := API{ID: "t", Name: "t", URL: srv.URL + "/", Text: "text"}
	a.URL = strings.Replace(a.URL, "https://", "http://", 1)
	if _, err := f.Get(context.Background(), a, []string{hostport}, "en", "2026-11-23", 0); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v", err)
	}
}

func TestValidID(t *testing.T) {
	for id, ok := range map[string]bool{"hitokoto": true, "a-b_c1": true, "": false, "A": false, "a b": false, strings.Repeat("a", 33): false} {
		if ValidID(id) != ok {
			t.Errorf("%q", id)
		}
	}
}
