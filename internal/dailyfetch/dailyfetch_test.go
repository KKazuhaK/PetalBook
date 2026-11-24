package dailyfetch

import (
	"context"
	"encoding/json"
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

func okAPI() API {
	return API{ID: "hito", Name: "一言", Lang: "zh", URL: "https://v1.hitokoto.cn/?c=d&l={lang}&d={date}", Text: "hitokoto", Source: "from", Author: "from_who",
		Link: "https://hitokoto.cn?uuid={uuid}", Headers: []Header{{"X-Api-Key", "k"}}}
}

func TestCheckAPI(t *testing.T) {
	a, err := CheckAPI(okAPI(), false)
	if err != nil || a.Refresh != RefreshDaily || a.RPS != DefaultRPS || a.Lang != "zh-Hans" || a.LangMap == nil {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := CheckAPI(API{ID: "q", Name: "q", URL: "https://api.example.org:8443/q/{lang}?l={lang}", Text: "[0].q", Refresh: RefreshMinutes, RefreshMinutes: 15, RPS: 3}, false); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*API){
		"http":                                func(a *API) { a.URL = "http://v1.hitokoto.cn/" },
		"a user":                              func(a *API) { a.URL = "https://u:p@v1.hitokoto.cn/" },
		"a user in the host":                  func(a *API) { a.URL = "https://v1.hitokoto.cn@evil.example/" },
		"lang in the host":                    func(a *API) { a.URL = "https://{lang}.hitokoto.cn/" },
		"lang in an upper-case scheme's host": func(a *API) { a.URL = "HTTPS://{lang}.hitokoto.cn/" },
		"date in the port":                    func(a *API) { a.URL = "https://v1.hitokoto.cn:{date}/" },
		"lang in the user":                    func(a *API) { a.URL = "https://{lang}@v1.hitokoto.cn/" },
		"lang at the end of the host":         func(a *API) { a.URL = "https://v1.hitokoto.cn{lang}?x=1" },
		"lang in the scheme":                  func(a *API) { a.URL = "http{lang}://v1.hitokoto.cn/" },
		"a fragment":                          func(a *API) { a.URL = "https://v1.hitokoto.cn/#x" },
		"spaces":                              func(a *API) { a.URL = "https://v1.hitokoto.cn/ x" },
		"no address":                          func(a *API) { a.URL = "not a url" },
		"loopback":                            func(a *API) { a.URL = "https://127.0.0.1/" },
		"loopback in IPv6":                    func(a *API) { a.URL = "https://[::1]:8443/" },
		"a private address":                   func(a *API) { a.URL = "https://10.1.2.3/" },
		"the metadata address":                func(a *API) { a.URL = "https://169.254.169.254/latest" },
		"localhost":                           func(a *API) { a.URL = "https://localhost/" },
		"a subdomain of it":                   func(a *API) { a.URL = "https://a.localhost/" },
		"no text path":                        func(a *API) { a.Text = "" },
		"a bad id":                            func(a *API) { a.ID = "A B" },
		"no name":                             func(a *API) { a.Name = " " },
		"a bad lang":                          func(a *API) { a.Lang = "中文" },
		"a lang that is no language":          func(a *API) { a.Lang = "cn" },
		"a lang with an underscore":           func(a *API) { a.Lang = "zh_CN" },
		"a map from no language":              func(a *API) { a.LangMap = map[string]string{"chinese": "zh"} },
		"a map to no code":                    func(a *API) { a.LangMap = map[string]string{"zh-Hans": ""} },
		"a map to a long code":                func(a *API) { a.LangMap = map[string]string{"zh-Hans": strings.Repeat("a", 36)} },
		"a map to a code with a slash":        func(a *API) { a.LangMap = map[string]string{"zh-Hans": "zh/../x"} },
		"a map to the same language twice":    func(a *API) { a.LangMap = map[string]string{"zh": "a", "zh-Hans": "b"} },
		"a link that is a path":               func(a *API) { a.Link = "/x/{uuid}" },
		"a link to a script":                  func(a *API) { a.Link = "javascript:alert({uuid})" },
		"refresh that is none":                func(a *API) { a.Refresh = "weekly" },
		"minutes with none":                   func(a *API) { a.Refresh = RefreshMinutes },
		"minutes beyond a day":                func(a *API) { a.Refresh = RefreshMinutes; a.RefreshMinutes = 1441 },
		"a rate below the least":              func(a *API) { a.RPS = 0.001 },
		"a rate above the most":               func(a *API) { a.RPS = 101 },
		"a forbidden header":                  func(a *API) { a.Headers = []Header{{"Host", "evil"}} },
		"a header with a newline":             func(a *API) { a.Headers = []Header{{"X-A", "a\r\nX-B: b"}} },
		"a bad header name":                   func(a *API) { a.Headers = []Header{{"X A", "a"}} },
		"a header twice":                      func(a *API) { a.Headers = []Header{{"X-A", "a"}, {"x-a", "b"}} },
		"too many headers": func(a *API) {
			a.Headers = nil
			for i := 0; i < MaxHeaders+1; i++ {
				a.Headers = append(a.Headers, Header{fmt.Sprint("X-", i), "v"})
			}
		},
	} {
		a := okAPI()
		mod(&a)
		if _, err := CheckAPI(a, false); err == nil {
			t.Errorf("%s is accepted", name)
		}
	}
	// Tests may use a server of their own.
	a = okAPI()
	a.URL = "http://127.0.0.1:1234/"
	if _, err := CheckAPI(a, true); err != nil {
		t.Fatal(err)
	}
	// A bad language is refused with its code.
	for _, l := range []string{"cn", "english", "zh_CN"} {
		a := okAPI()
		a.Lang = l
		var ce *CodedError
		if _, err := CheckAPI(a, false); !errors.As(err, &ce) || ce.Code != CodeBadLanguage {
			t.Errorf("%s: %v", l, err)
		}
	}
	// Tags are written in canonical form, and so are the keys of a language map.
	a = okAPI()
	a.Lang, a.LangMap = "ZH-cn", map[string]string{"zh": "zh_CN", "EN-gb": "en"}
	if got, err := CheckAPI(a, false); err != nil || got.Lang != "zh-CN" || len(got.LangMap) != 2 || got.LangMap["zh-Hans"] != "zh_CN" || got.LangMap["en-GB"] != "en" {
		t.Fatalf("%+v %v", got, err)
	}
}

// The built-in interfaces are valid as they are, and are what the contract says.
func TestProviders(t *testing.T) {
	ps := Providers()
	for _, p := range ps {
		if _, err := CheckAPI(p.API, false); err != nil {
			t.Errorf("%s: %v", p.ID, err)
		}
		if !p.Builtin {
			t.Errorf("%s is not built in", p.ID)
		}
	}
	if len(ps) != 3 || ps[0].ID != "hitokoto" || ps[0].RPS != 20 || ps[0].Link != "https://hitokoto.cn?uuid={uuid}" || ps[0].URL != "https://international.v1.hitokoto.cn/?max_length=80" ||
		ps[0].Text != "hitokoto" || ps[0].Source != "from" || ps[0].Author != "from_who" || fmt.Sprint(ps[0].Languages) != "[zh-Hans zh-Hant]" || ps[0].Site != "https://hitokoto.cn" ||
		ps[1].ID != "zenquotes" || ps[1].Text != "[0].q" || ps[1].Author != "[0].a" || ps[1].RPS != 0.1 || ps[1].Link != "https://zenquotes.io/" || ps[1].URL != "https://zenquotes.io/api/today" || ps[1].Lang != "en" || ps[1].SkipNew ||
		ps[2].ID != "favqs" || ps[2].Text != "quote.body" || ps[2].Author != "quote.author" || ps[2].RPS != 1 || ps[2].Link != "https://favqs.com/quotes/{quote.author_permalink}/{quote.id}" || ps[2].URL != "https://favqs.com/api/qotd" || ps[2].Lang != "en" || ps[2].SkipNew {
		t.Fatalf("%+v", ps)
	}
	if p, ok := ProviderByID("zenquotes"); !ok || p.ID != "zenquotes" {
		t.Fatal("ProviderByID")
	}
	if _, ok := ProviderByID("nope"); ok {
		t.Fatal("ProviderByID")
	}
}

func TestExtractAndLinks(t *testing.T) {
	a := API{Text: "data.0.q", Source: "data.0.meta.from", Author: "data.0.a", Link: "https://example.org/q?id={data.0.id}&n={n}"}
	q, err := extract(a, []byte(`{"n":7,"data":[{"q":" 山高月小 ","a":"苏轼","id":"a b","meta":{"from":"后赤壁赋"}}]}`))
	if err != nil || q.Text != "山高月小" || q.Author != "苏轼" || q.Source != "后赤壁赋" || q.Link != "https://example.org/q?id=a+b&n=7" {
		t.Fatalf("%+v %v", q, err)
	}
	// A link whose value is not in the answer is left out, not half made.
	q, _ = extract(a, []byte(`{"data":[{"q":"x"}]}`))
	if q.Text != "x" || q.Link != "" {
		t.Fatalf("%+v", q)
	}
	// A value cannot turn the link into another address.
	q, _ = extract(API{Text: "t", Link: "{u}"}, []byte(`{"t":"x","u":"javascript:alert(1)"}`))
	if q.Link != "" {
		t.Fatalf("%q", q.Link)
	}
	q, err = extract(API{Text: "[1].n"}, []byte(`[{"n":1},{"n":2.5}]`))
	if err != nil || q.Text != "2.5" || q.Source != "" {
		t.Fatalf("%+v %v", q, err)
	}
	for name, body := range map[string]string{"not JSON": `nope`, "no such path": `{"x":1}`, "a list at the end": `{"data":[1]}`, "blank": `{"data":[{"q":"  "}]}`, "an object": `{"data":[{"q":{"a":1}}]}`, "out of range": `{"data":[]}`} {
		if _, err := extract(API{Text: "data.0.q"}, []byte(body)); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	long := strings.Repeat("字", 3000)
	q, err = extract(API{Text: "t"}, []byte(`{"t":"`+long+`"}`))
	if err != nil || len([]rune(q.Text)) != MaxTextChars {
		t.Fatalf("%d %v", len([]rune(q.Text)), err)
	}
}

// A hitokoto-like answer has "from_who": null: only the text path is required.
func TestOptionalFieldsWithoutAValueAreNoError(t *testing.T) {
	a := API{Text: "hitokoto", Source: "from", Author: "from_who", Link: "https://hitokoto.cn?uuid={uuid}"}
	for name, body := range map[string]string{
		"null":    `{"hitokoto":"x","from":"s","from_who":null}`,
		"missing": `{"hitokoto":"x","from":"s"}`,
		"empty":   `{"hitokoto":"x","from":"s","from_who":"  "}`,
	} {
		q, err := extract(a, []byte(body))
		if err != nil || q.Text != "x" || q.Source != "s" || q.Author != "" || q.Link != "" {
			t.Errorf("%s: %+v %v", name, q, err)
		}
		_, errs, codes, _ := takeOut(a, mustJSON(t, body), "zh", "2026-11-23", secretsOf(a))
		if errs != nil || codes != nil {
			t.Errorf("%s: %v %v", name, errs, codes)
		}
	}
	// The text is still required.
	for _, body := range []string{`{"hitokoto":null,"from":"s"}`, `{"from":"s"}`, `{"hitokoto":"","from":"s"}`} {
		if _, err := extract(a, []byte(body)); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v", body, err)
		}
		_, errs, codes, _ := takeOut(a, mustJSON(t, body), "zh", "2026-11-23", secretsOf(a))
		if errs["text"] == "" || codes["text"] != CodePathError || len(errs) != 1 {
			t.Errorf("%s: %v %v", body, errs, codes)
		}
	}
}

func mustJSON(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPeriods(t *testing.T) {
	at := time.Date(2026, 11, 23, 10, 17, 30, 0, time.UTC)
	k, ends := API{Refresh: RefreshDaily}.Period(at, "2026-11-23")
	if k != "d:2026-11-23" || !ends.IsZero() {
		t.Fatalf("%s %v", k, ends)
	}
	k, ends = API{Refresh: RefreshHourly}.Period(at, "2026-11-23")
	k2, _ := API{Refresh: RefreshHourly}.Period(at.Add(20*time.Minute), "")
	k3, _ := API{Refresh: RefreshHourly}.Period(at.Add(43*time.Minute), "")
	if k != k2 || k == k3 || !ends.Equal(time.Date(2026, 11, 23, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("%s %s %s %v", k, k2, k3, ends)
	}
	a := API{Refresh: RefreshMinutes, RefreshMinutes: 15}
	k, ends = a.Period(at, "")
	if !ends.Equal(time.Date(2026, 11, 23, 10, 30, 0, 0, time.UTC)) {
		t.Fatalf("%v", ends)
	}
	if k2, _ := a.Period(at.Add(14*time.Minute), ""); k2 == k {
		t.Fatal("a quarter of an hour is not over")
	}
	if k2, _ := a.Period(at.Add(2*time.Minute), ""); k2 != k {
		t.Fatal("two minutes later is the same quarter")
	}
}

// --- asking ----------------------------------------------------------------------

// mem is a Backing in memory, which a "restart" keeps.
type mem struct {
	mu   sync.Mutex
	rows map[string]Result
	puts int
	last map[string]Result
	prun []time.Time
}

func newMem() *mem { return &mem{rows: map[string]Result{}, last: map[string]Result{}} }

func (m *mem) k(a, l, p string, s int) string { return fmt.Sprint(a, "|", l, "|", p, "|", s) }
func (m *mem) Get(_ context.Context, a, l, p string, s int) (Result, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[m.k(a, l, p, s)]
	return r, ok, nil
}
func (m *mem) Put(_ context.Context, a, l, p string, s int, r Result) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	m.rows[m.k(a, l, p, s)] = r
	if old, ok := m.last[a+"|"+l]; !ok || !r.Fetched.Before(old.Fetched) {
		m.last[a+"|"+l] = r
	}
	return nil
}
func (m *mem) Latest(_ context.Context, a, l string) (Result, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.last[a+"|"+l]
	return r, ok, nil
}
func (m *mem) Prune(_ context.Context, before time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prun = append(m.prun, before)
	return nil
}

// A service of the test's own whose answer counts the requests.
type svc struct {
	*httptest.Server
	hits    atomic.Int64
	mode    atomic.Value
	headers sync.Map
	paths   sync.Map
}

func newSvc(t *testing.T) *svc {
	t.Helper()
	s := &svc{}
	s.mode.Store("ok")
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := s.hits.Add(1)
		s.headers.Store("X-Api-Key", r.Header.Get("X-Api-Key"))
		s.paths.Store(r.URL.RequestURI(), true)
		switch s.mode.Load() {
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
		case "echo":
			b, _ := json.Marshal(map[string]any{"text": "key " + r.Header.Get("X-Api-Key"), "headers": r.Header})
			jsonOK(w, string(b))
		case "delay":
			time.Sleep(200 * time.Millisecond)
			jsonOK(w, fmt.Sprintf(`{"text":"item %d"}`, n))
		case "slow":
			select {
			case <-time.After(3 * time.Second):
			case <-r.Context().Done():
			}
			jsonOK(w, `{"text":"late"}`)
		default:
			jsonOK(w, fmt.Sprintf(`{"text":"item %d","from":"src","who":"me","uuid":"u%d"}`, n, n))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *svc) api() API {
	return API{ID: "t", Name: "t", URL: s.URL + "/q?lang={lang}&d={date}", Text: "text", Source: "from", Author: "who", Link: "https://example.org/{uuid}",
		Refresh: RefreshDaily, SkipNew: true, RPS: 100, Enabled: true, Headers: []Header{{"X-Api-Key", "secret-1"}}}
}

func jsonOK(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	fmt.Fprint(w, body)
}

type clk struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clk) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clk) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func fetcher(b Backing) (*Fetcher, *clk) {
	f := NewForTests(b)
	c := &clk{t: time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)}
	f.SetClock(c.now)
	return f, c
}

func TestMemoryAskedOncePerPeriodAndLanguage(t *testing.T) {
	s := newSvc(t)
	a := s.api()
	f, _ := fetcher(nil)
	ctx := context.Background()
	r1, err := f.Get(ctx, a, "zh", "2026-11-23", 0)
	if err != nil || r1.Text != "item 1" || r1.Source != "src" || r1.Author != "me" || r1.Link != "https://example.org/u1" || r1.Stale {
		t.Fatalf("%+v %v", r1, err)
	}
	if v, _ := s.headers.Load("X-Api-Key"); v != "secret-1" {
		t.Fatalf("the header was not sent: %v", v)
	}
	if _, ok := s.paths.Load("/q?lang=zh&d=2026-11-23"); !ok {
		t.Fatal("{lang} and {date} were not filled in")
	}
	// Twenty people asking at once: the same text, no more requests.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, err := f.Get(ctx, a, "zh", "2026-11-23", 0); err != nil || r != r1 {
				t.Errorf("%+v %v", r, err)
			}
		}()
	}
	wg.Wait()
	if s.hits.Load() != 1 {
		t.Fatalf("%d requests", s.hits.Load())
	}
	// Another language or day is asked for on its own.
	if r, _ := f.Get(ctx, a, "en", "2026-11-23", 0); r.Text != "item 2" {
		t.Fatalf("%+v", r)
	}
	if r, _ := f.Get(ctx, a, "zh", "2026-11-24", 0); r.Text != "item 3" {
		t.Fatalf("%+v", r)
	}
	// Another interface (a different path) does not share what was kept.
	a2 := a
	a2.Text = "from"
	if r, _ := f.Get(ctx, a2, "zh", "2026-11-23", 0); r.Text != "src" {
		t.Fatalf("%+v", r)
	}
}

func TestSkipAsksAgainOnlyWhenTheInterfaceSaysSo(t *testing.T) {
	s := newSvc(t)
	a := s.api()
	f, _ := fetcher(nil)
	ctx := context.Background()
	r0, _ := f.Get(ctx, a, "en", "2026-11-23", 0)
	r1, _ := f.Get(ctx, a, "en", "2026-11-23", 1)
	r1b, _ := f.Get(ctx, a, "en", "2026-11-23", 1)
	if r0.Text != "item 1" || r1.Text != "item 2" || r1b != r1 || s.hits.Load() != 2 {
		t.Fatalf("%v %v %v %d", r0.Text, r1.Text, r1b.Text, s.hits.Load())
	}
	// Without SkipNew "another" is the same item, and costs no request.
	a.SkipNew = false
	a.URL += "&x=1" // another interface to the cache
	f2, _ := fetcher(nil)
	q0, _ := f2.Get(ctx, a, "en", "2026-11-23", 0)
	hits := s.hits.Load()
	q5, _ := f2.Get(ctx, a, "en", "2026-11-23", 5)
	if q5.Text != q0.Text || s.hits.Load() != hits {
		t.Fatalf("%v %v %d", q0.Text, q5.Text, s.hits.Load()-hits)
	}
	// A skip beyond what is kept as a request is clamped, not a way to ask for ever more.
	a.SkipNew = true
	for k := 0; k < 3; k++ {
		f2.Get(ctx, a, "en", "2026-11-23", 100000+k)
	}
	if got := s.hits.Load() - hits; got != 1 {
		t.Fatalf("%d requests for skips beyond the largest", got)
	}
}

func TestRefreshPeriods(t *testing.T) {
	s := newSvc(t)
	a := s.api()
	ctx := context.Background()
	text := func(f *Fetcher, date string) string {
		r, err := f.Get(ctx, a, "en", date, 0)
		if err != nil {
			t.Fatal(err)
		}
		return r.Text
	}
	// Hourly: the item changes with the hour, whatever the date says.
	a.Refresh = RefreshHourly
	f, c := fetcher(nil)
	one := text(f, "2026-11-23")
	c.add(40 * time.Minute) // 08:40
	if text(f, "2026-11-30") != one {
		t.Fatal("the same hour gave another item")
	}
	c.add(25 * time.Minute) // 09:05
	two := text(f, "2026-11-23")
	if two == one {
		t.Fatal("a new hour gave the same item")
	}
	// Every 15 minutes.
	a.Refresh, a.RefreshMinutes = RefreshMinutes, 15
	a.URL += "&m=15"
	f, c = fetcher(nil)
	one = text(f, "")
	c.add(14 * time.Minute)
	if text(f, "") != one {
		t.Fatal("inside the quarter")
	}
	c.add(2 * time.Minute)
	if text(f, "") == one {
		t.Fatal("after the quarter")
	}
	// Daily: only the date matters.
	a.Refresh, a.RefreshMinutes = RefreshDaily, 0
	a.URL += "&daily=1"
	f, c = fetcher(nil)
	one = text(f, "2026-11-23")
	c.add(10 * time.Hour)
	if text(f, "2026-11-23") != one || text(f, "2026-11-24") == one {
		t.Fatal("a day is the date asked for")
	}
}

func TestStateDatabaseKeepsWhatWasSaidAcrossARestart(t *testing.T) {
	s := newSvc(t)
	a := s.api()
	b := newMem()
	ctx := context.Background()
	f, c := fetcher(b)
	r1, err := f.Get(ctx, a, "zh", "2026-11-23", 0)
	if err != nil || b.puts != 1 {
		t.Fatalf("%v %d", err, b.puts)
	}
	// A restart: nothing in memory. The same text, no request.
	f2, _ := fetcher(b)
	r2, err := f2.Get(ctx, a, "zh", "2026-11-23", 0)
	if err != nil || r2.Text != r1.Text || !r2.Fetched.Equal(r1.Fetched) || s.hits.Load() != 1 {
		t.Fatalf("%+v %v %d", r2, err, s.hits.Load())
	}
	// The same through the first one with its memory gone.
	f.ForgetMemory()
	if r, _ := f.Get(ctx, a, "zh", "2026-11-23", 0); r.Text != r1.Text || s.hits.Load() != 1 {
		t.Fatalf("%+v %d", r, s.hits.Load())
	}
	// A change of the interface's address is another interface: it is asked.
	a.URL += "&v=2"
	if r, _ := f2.Get(ctx, a, "zh", "2026-11-23", 0); r.Text == r1.Text || s.hits.Load() != 2 {
		t.Fatalf("%+v %d", r, s.hits.Load())
	}
	// Old rows are pruned, at most once an hour.
	c.add(2 * time.Hour)
	runs := len(b.prun)
	f.Get(ctx, a, "zh", "2026-11-24", 0)
	f.Get(ctx, a, "zh", "2026-11-25", 0)
	if len(b.prun) != runs+1 {
		t.Fatalf("%d prunes for two requests within an hour", len(b.prun)-runs)
	}
	if want := c.now().Add(-Retention); b.prun[len(b.prun)-1].Sub(want) > time.Second || want.Sub(b.prun[len(b.prun)-1]) > time.Second {
		t.Fatalf("pruned before %v, want %v", b.prun[len(b.prun)-1], want)
	}
}

func TestFailureServesTheLastThingSaidThenBacksOff(t *testing.T) {
	s := newSvc(t)
	a := s.api()
	b := newMem()
	ctx := context.Background()
	f, c := fetcher(b)
	// Nothing was ever said and it fails: nothing to give.
	s.mode.Store("500")
	if _, err := f.Get(ctx, a, "zh", "2026-11-23", 0); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	// While it waits, nothing is asked, however often it is looked at.
	for i := 0; i < 10; i++ {
		if _, err := f.Get(ctx, a, "zh", "2026-11-23", 0); !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
	}
	if s.hits.Load() != 1 {
		t.Fatalf("%d requests while backing off", s.hits.Load())
	}
	// The wait doubles: 30 s, 60 s, 120 s...
	waits := []time.Duration{backoffBase, 2 * backoffBase, 4 * backoffBase}
	for i, w := range waits {
		c.add(w - time.Second)
		before := s.hits.Load()
		f.Get(ctx, a, "zh", "2026-11-23", 0)
		if s.hits.Load() != before {
			t.Fatalf("asked %d s early (round %d)", 1, i)
		}
		c.add(time.Second)
		f.Get(ctx, a, "zh", "2026-11-23", 0)
		if s.hits.Load() != before+1 {
			t.Fatalf("not asked when the wait was over (round %d)", i)
		}
	}
	// The interface comes back: a success clears the wait.
	s.mode.Store("ok")
	c.add(time.Hour)
	good, err := f.Get(ctx, a, "zh", "2026-11-23", 0)
	if err != nil || good.Stale || !strings.HasPrefix(good.Text, "item") {
		t.Fatalf("%+v %v", good, err)
	}
	// Then it fails on the next day: the last thing it said, marked as such, with when.
	s.mode.Store("500")
	got, err := f.Get(ctx, a, "zh", "2026-11-24", 0)
	if err != nil || !got.Stale || got.Text != good.Text || !got.Fetched.Equal(good.Fetched) {
		t.Fatalf("%+v %v", got, err)
	}
	// Also after a restart (from the state database), and for a skip.
	f.ForgetMemory()
	got, err = f.Get(ctx, a, "zh", "2026-11-25", 3)
	if err != nil || !got.Stale || got.Text != good.Text {
		t.Fatalf("%+v %v", got, err)
	}
	// Not for ever: after the retention it is hidden.
	c.add(Retention + time.Hour)
	f.ForgetMemory()
	b2 := newMem() // what the old rows were is gone with the pruning
	f2, _ := fetcher(b2)
	if _, err := f2.Get(ctx, a, "zh", "2026-12-25", 0); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	old := newMem()
	old.last[a.key()+"|zh"] = Result{Quote: Quote{Text: "old"}, Fetched: c.now().Add(-Retention - time.Minute)}
	f3, _ := fetcher(old)
	f3.SetClock(c.now)
	if _, err := f3.Get(ctx, a, "zh", "2026-12-25", 0); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an answer older than the retention was used: %v", err)
	}
}

func TestEveryKindOfBadAnswerIsAFailure(t *testing.T) {
	s := newSvc(t)
	a := s.api()
	ctx := context.Background()
	for _, m := range []string{"500", "html", "big", "redirect", "garbage"} {
		s.mode.Store(m)
		f, _ := fetcher(nil)
		if _, err := f.Get(ctx, a, "en", "2026-11-23", 0); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v", m, err)
		}
	}
}

func TestOutgoingRateLimitServesWhatIsKeptInsteadOfWaiting(t *testing.T) {
	s := newSvc(t)
	a := s.api()
	a.RPS = 2 // a bucket of two, filling at two a second
	ctx := context.Background()
	f, c := fetcher(nil)
	for skip := 0; skip < 2; skip++ {
		if r, err := f.Get(ctx, a, "en", "2026-11-23", skip); err != nil || r.Stale {
			t.Fatalf("%d: %+v %v", skip, r, err)
		}
	}
	// The bucket is empty: the last thing it said is given, at once, no request.
	start := time.Now()
	r, err := f.Get(ctx, a, "en", "2026-11-23", 2)
	if err != nil || !r.Stale || r.Text != "item 2" || s.hits.Load() != 2 || time.Since(start) > time.Second {
		t.Fatalf("%+v %v %d", r, err, s.hits.Load())
	}
	// Half a second fills one token (2 a second): one more request.
	c.add(500 * time.Millisecond)
	if r, err := f.Get(ctx, a, "en", "2026-11-23", 3); err != nil || r.Stale || s.hits.Load() != 3 {
		t.Fatalf("%+v %v %d", r, err, s.hits.Load())
	}
	// With nothing kept at all there is nothing to give.
	b := newMem()
	a2 := a
	a2.URL += "&other=1"
	f2, _ := fetcher(b)
	f2.Get(ctx, a2, "en", "2026-11-23", 0)
	f2.Get(ctx, a2, "en", "2026-11-23", 1)
	if _, err := f2.Get(ctx, a2, "fr", "2026-11-23", 2); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("%v", err)
	}
	// The limit is for the interface, whoever asks, in every language: the French ask above used the same bucket.
	// A high limit is not limited.
	a3 := a
	a3.URL += "&third=1"
	a3.RPS = 100
	f3, _ := fetcher(nil)
	for skip := 0; skip < 50; skip++ {
		if r, err := f3.Get(ctx, a3, "en", "2026-11-23", skip); err != nil || r.Stale {
			t.Fatalf("%d: %+v %v", skip, r, err)
		}
	}
}

func TestTimeout(t *testing.T) {
	s := newSvc(t)
	s.mode.Store("slow")
	a := s.api()
	f, _ := fetcher(nil)
	f.SetTimeout(150 * time.Millisecond)
	start := time.Now()
	if _, err := f.Get(context.Background(), a, "en", "2026-11-23", 0); !errors.Is(err, ErrUnavailable) || time.Since(start) > 2*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}

func TestProbeShowsTheRawAnswerAndWhatThePathsTakeOut(t *testing.T) {
	s := newSvc(t)
	a := s.api()
	f, c := fetcher(nil)
	ctx := context.Background()
	res, err := f.Probe(ctx, a, "zh", "2026-11-23")
	if err != nil || !res.OK || res.Status != 200 || res.Quote == nil || res.Quote.Text != "item 1" || res.Quote.Link != "https://example.org/u1" || res.PathErrors != nil ||
		!strings.Contains(res.Raw, `"item 1"`) || res.RawTruncated || !strings.Contains(res.URL, "lang=zh&d=2026-11-23") || strings.Contains(res.URL, "secret") {
		t.Fatalf("%+v %v", res, err)
	}
	// Wrong paths are said, by field, and the rest still comes out.
	a.Text, a.Source, a.Author, a.Link = "text", "nope.x", "who", "https://example.org/{missing}"
	c.add(time.Second)
	res, _ = f.Probe(ctx, a, "", "2026-11-23")
	// Source, author and link are optional: a path with no value there is no
	// value, not a path error.
	if !res.OK || res.Quote == nil || res.Quote.Text == "" || res.Quote.Source != "" || res.Quote.Link != "" || res.PathErrors != nil || res.Code != "" {
		t.Fatalf("%+v", res)
	}
	a.Text = "wrong.path"
	c.add(time.Second)
	res, _ = f.Probe(ctx, a, "", "2026-11-23")
	if res.OK || res.Quote != nil || res.PathError("text") == nil || res.Message == "" || res.Raw == "" {
		t.Fatalf("%+v", res)
	}
	// A long answer is cut, and one that is not JSON, or an error, shows why.
	long := API{ID: "l", Name: "l", URL: s.URL + "/", Text: "text"}
	s.mode.Store("big")
	c.add(time.Second)
	res, _ = f.Probe(ctx, long, "", "")
	if len(res.Message) == 0 || res.OK {
		t.Fatalf("%+v", res)
	}
	for _, m := range []string{"html", "500", "garbage", "redirect"} {
		s.mode.Store(m)
		c.add(time.Second)
		res, _ := f.Probe(ctx, long, "", "")
		if res.OK || res.Message == "" || res.Quote != nil {
			t.Errorf("%s: %+v", m, res)
		}
	}
	// It is limited: a few at a time.
	limited := false
	for i := 0; i < 10; i++ {
		if _, err := f.Probe(ctx, long, "", ""); errors.Is(err, ErrRateLimited) {
			limited = true
		}
	}
	if !limited {
		t.Error("a trial is never limited")
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
	f := New(nil)
	for _, h := range []string{hostport, "localhost" + port, "[::1]" + port, "0.0.0.0" + port, "127.1" + port} {
		a := API{ID: "t", Name: "t", URL: "https://" + h + "/", Text: "text", Refresh: RefreshDaily, RPS: 100}
		if _, err := f.Get(context.Background(), a, "en", "2026-11-23", 0); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v", h, err)
		}
		res, err := f.Probe(context.Background(), a, "en", "2026-11-23")
		if err != nil && !errors.Is(err, ErrRateLimited) {
			t.Fatal(err)
		}
		if res.OK || (err == nil && !strings.Contains(res.Message, "public address")) {
			t.Errorf("a trial of %s: %+v", h, res)
		}
		time.Sleep(600 * time.Millisecond) // the trial limiter
	}
	if hit.Load() != 0 {
		t.Fatalf("the server inside was reached %d times", hit.Load())
	}
	a := API{ID: "t", Name: "t", URL: strings.Replace(srv.URL, "https://", "http://", 1) + "/", Text: "text", Refresh: RefreshDaily, RPS: 100}
	if _, err := f.Get(context.Background(), a, "en", "2026-11-23", 0); !errors.Is(err, ErrUnavailable) {
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

// A placeholder that reaches the host (the address was not checked by
// CheckAPI, or the check is fooled) never chooses where the server connects:
// the host is the one written, and it is checked on the address really used.
func TestTheHostIsCheckedOnTheAddressUsed(t *testing.T) {
	f, _ := fetcher(nil)
	a := API{ID: "t", Name: "t", URL: "HTTP://h{lang}.test/q", Text: "text"}
	if _, err := f.request(context.Background(), a, "fr", "2026-11-23"); err == nil {
		t.Fatal("a host made with the reader's language was asked")
	}
	if _, err := f.request(context.Background(), a, "fr", "2026-11-23"); err == nil {
		t.Fatal("a host made with the reader's language was asked")
	}
	ok := API{ID: "t", Name: "t", URL: "http://h.test/q/{lang}?d={date}", Text: "text"}
	req, err := f.request(context.Background(), ok, "zh-hant", "2026-11-23")
	if err != nil || req.URL.String() != "http://h.test/q/zh-hant?d=2026-11-23" {
		t.Fatalf("%v %v", req, err)
	}
	// A day that is not one is not put in the address.
	if req, err := f.request(context.Background(), ok, "en", "../x?"); err != nil || strings.Contains(req.URL.String(), "x") {
		t.Fatalf("%v %v", req, err)
	}
}

// An answer that repeats a header's value is not shown, in a trial or to readers.
func TestProbeHidesHeaderValues(t *testing.T) {
	s := newSvc(t)
	a := s.api()
	f, c := fetcher(nil)
	ctx := context.Background()
	s.mode.Store("echo")
	c.add(time.Second)
	res, err := f.Probe(ctx, a, "en", "2026-11-23")
	if err != nil || s.hits.Load() != 1 || res.OK || res.Raw != "" || res.Quote != nil || res.Message != MsgRepeatsHeader || res.PathError("text") == nil {
		t.Fatalf("%+v %v", res, err)
	}
	if strings.Contains(strings.ToLower(fmt.Sprint(res)), "secret-1") {
		t.Fatalf("a header value is shown: %+v", res)
	}
	// Nor does what is fetched for the readers: such an answer is refused.
	if r, err := f.Get(ctx, a, "en", "2026-11-23", 0); !errors.Is(err, ErrUnavailable) || strings.Contains(r.Text, "secret-1") {
		t.Fatalf("%+v %v", r, err)
	}
}

// Languages an interface cannot answer differently in are one lane: asking
// in them costs no more requests and keeps nothing more.
func TestLanguagesTheAnswerIgnoresAreOneLane(t *testing.T) {
	s := newSvc(t)
	a := s.api()
	a.URL = s.URL + "/q?d={date}"
	f, _ := fetcher(nil)
	ctx := context.Background()
	for _, l := range []string{"en", "fr", "de", "zz-top", ""} {
		if r, err := f.Get(ctx, a, l, "2026-11-23", 0); err != nil || r.Text != "item 1" {
			t.Fatalf("%s: %+v %v", l, r, err)
		}
	}
	// An interface in a language of its own is asked in it, whoever asks.
	b := a
	b.URL, b.Lang = s.URL+"/q?l={lang}&d={date}", "zh-Hans"
	for _, l := range []string{"en", "fr"} {
		f.Get(ctx, b, l, "2026-11-23", 0)
	}
	if s.hits.Load() != 2 {
		t.Fatalf("%d requests", s.hits.Load())
	}
	if _, ok := s.paths.Load("/q?l=zh-Hans&d=2026-11-23"); !ok {
		t.Fatal("not asked in its own language")
	}
	if l, _, _ := f.Lanes(); l != 2 {
		t.Fatalf("%d lanes", l)
	}
	// For one that takes every language, the language is one the server has.
	c := API{URL: "https://x/{lang}"}
	for in, want := range map[string]string{"fr": "fr", "fr-CA": "fr", "zh-CN": "zh-Hans", "zh-hant": "zh-Hant", "xx": "zh-Hans", "": "zh-Hans"} {
		if got := c.LaneLang(in, []string{"zh-Hans", "zh-Hant", "fr"}); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if got := (API{URL: "https://x/"}).LaneLang("fr", []string{"fr"}); got != "" {
		t.Errorf("%q", got)
	}
}

// What is kept in memory by lane is bounded, whatever is asked.
func TestLanesAreBounded(t *testing.T) {
	f, c := fetcher(nil)
	for i := 0; i < 3*laneMax; i++ {
		c.add(3 * time.Minute)
		f.mu.Lock()
		now := f.now()
		lane := laneKey{"a", fmt.Sprint("l", i)}
		f.fail(lane, now)
		f.remember(cacheKey{lane.api, lane.lang, "d", 0}, Result{Quote: Quote{Text: "x"}, Fetched: now})
		f.bucketOf(fmt.Sprint("api", i), now).take(now, 1)
		f.mu.Unlock()
	}
	last, fails, buckets := f.Lanes()
	if last > laneMax || fails > laneMax || buckets > laneMax {
		t.Fatalf("%d %d %d", last, fails, buckets)
	}
	// The newest are kept.
	f.mu.Lock()
	_, ok := f.last[laneKey{"a", fmt.Sprint("l", 3*laneMax-1)}]
	f.mu.Unlock()
	if !ok {
		t.Fatal("the newest answer was dropped")
	}
}

// A reader who goes away does not make the interface fail for everybody.
func TestAReaderWhoLeavesIsNoFailure(t *testing.T) {
	s := newSvc(t)
	s.mode.Store("delay")
	a := s.api()
	f, _ := fetcher(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	f.Get(ctx, a, "en", "2026-11-23", 0)
	r, err := f.Get(context.Background(), a, "en", "2026-11-23", 0)
	if err != nil || r.Stale || r.Text != "item 1" || s.hits.Load() != 1 {
		t.Fatalf("%+v %v %d", r, err, s.hits.Load())
	}
	if _, fails, _ := f.Lanes(); fails != 0 {
		t.Fatalf("%d failures", fails)
	}
}

type trimMem struct {
	*mem
	trims atomic.Int64
}

func (m *trimMem) Trim(_ context.Context, _ string, keep int) error {
	if keep != MaxCachedRows {
		panic(keep)
	}
	m.trims.Add(1)
	return nil
}

// The answers kept of an interface are trimmed now and then.
func TestTheKeptAnswersAreTrimmed(t *testing.T) {
	s := newSvc(t)
	a := s.api()
	b := &trimMem{mem: newMem()}
	f, _ := fetcher(b)
	for skip := 0; skip <= trimEvery; skip++ {
		f.Get(context.Background(), a, "en", "2026-11-23", skip)
	}
	if b.trims.Load() != 1 {
		t.Fatalf("%d trims", b.trims.Load())
	}
}

// extract is Extract for a request with no language or day.
func extract(a API, body []byte) (Quote, error) { return Extract(a, body, "", "") }

// An echo service: it answers what body makes of the request's key.
func newEcho(t *testing.T, body func(key string) string) API {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, body(r.Header.Get("Authorization")))
	}))
	t.Cleanup(srv.Close)
	return API{ID: "e", Name: "e", URL: srv.URL + "/echo", Text: "text", Refresh: RefreshDaily, RPS: 100}
}

// Each way an answer can repeat a request header's value, through any path,
// the link, or the raw answer of a trial, is refused: the value is compared
// decoded, in any case, in pieces, before anything is cut or escaped.
func TestNoPathOrLinkOrTrialRepeatsAHeaderValue(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9." + strings.Repeat("eyJzdWIiOiIxMjM0NTY3ODkwIn0", 9) + ".c2lnbmF0dXJl"
	if len(jwt) <= 200 {
		t.Fatal("the token is to be longer than a cut source")
	}
	esc := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	for _, c := range []struct {
		name, secret string
		mod          func(*API)
		body         func(key string) string
	}{
		{"the text", "Bearer sk-live-0123456789", nil,
			func(k string) string { return `{"text":` + esc("key "+k) + `}` }},
		{"upper case", "Bearer sk-live-abcdefghij", nil,
			func(k string) string { return `{"text":` + esc(strings.ToUpper(k)) + `}` }},
		{"a piece of it", "Bearer sk-live-abcdefghij", nil,
			func(k string) string { return `{"text":` + esc("part "+k[10:20]) + `}` }},
		{"PHP's \\/", "key/with/many/slashes/0123", nil,
			func(k string) string { return `{"text":"` + strings.ReplaceAll(k, "/", `\/`) + `"}` }},
		{"\\u escapes", "Bearer sk-live-0123456789", nil,
			func(k string) string { return `{"text":"B` + k[1:] + `"}` }},
		{"+ read as a space", "Bearer a+b+c+d+e+f+g+h", nil,
			func(k string) string { return `{"text":` + esc(strings.ReplaceAll(k, "+", " ")) + `}` }},
		{"+ query escaped", "Bearer a+b+c+d+e+f+g+h", nil,
			func(k string) string { return `{"text":` + esc(strings.ReplaceAll(k, "+", "%2B")) + `}` }},
		{"a long token in the source, before it is cut", "Bearer " + jwt, func(a *API) { a.Source = "src" },
			func(k string) string { return `{"text":"fine","src":` + esc(k) + `}` }},
		{"a key past the end of the text's cut", "Bearer sk-live-0123456789", nil,
			func(k string) string { return `{"text":` + esc(strings.Repeat("a", MaxTextChars-3)+k) + `}` }},
		{"the author", "Bearer sk-live-0123456789", func(a *API) { a.Author = "who" },
			func(k string) string { return `{"text":"fine","who":` + esc(k) + `}` }},
		{"the link, query escaped", "Bearer sk/live+0123456789", func(a *API) { a.Link = "https://example.org/?k={h}" },
			func(k string) string { return `{"text":"fine","h":` + esc(k) + `}` }},
		{"the link, a number", "1234567890123456", func(a *API) { a.Link = "https://example.org/?k={h}" },
			func(k string) string { return `{"text":"fine","h":` + k + `}` }},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newEcho(t, c.body)
			a.Headers = []Header{{"Authorization", c.secret}}
			if c.mod != nil {
				c.mod(&a)
			}
			if _, err := CheckAPI(a, true); err != nil {
				t.Fatal(err)
			}
			f, _ := fetcher(nil)
			ctx := context.Background()
			res, err := f.Probe(ctx, a, "en", "2026-11-23")
			if err != nil || res.OK || res.Raw != "" || res.Message != MsgRepeatsHeader || len(res.PathErrors) == 0 {
				t.Fatalf("%+v %v", res, err)
			}
			if shown := strings.ToLower(fmt.Sprint(res)); strings.Contains(shown, strings.ToLower(c.secret[7:15])) {
				t.Fatalf("a piece of the value is shown: %s", shown)
			}
			if r, err := f.Get(ctx, a, "en", "2026-11-23", 0); !errors.Is(err, ErrUnavailable) || r.Text != "" {
				t.Fatalf("%+v %v", r, err)
			}
		})
	}
	// An answer that is clean is shown as it came; one that repeats a value in
	// a name given twice (only the last of which is decoded) is not.
	a := newEcho(t, func(string) string { return `{"text":"fine","x":"\/a"}` })
	a.Headers = []Header{{"Authorization", "Bearer sk-live-0123456789"}}
	f, c := fetcher(nil)
	if res, err := f.Probe(context.Background(), a, "en", "2026-11-23"); err != nil || !res.OK || res.Raw != `{"text":"fine","x":"\/a"}` {
		t.Fatalf("%+v %v", res, err)
	}
	a = newEcho(t, func(k string) string { return `{"text":"fine","x":"B` + k[1:] + `","x":"y"}` })
	a.Headers = []Header{{"Authorization", "Bearer sk-live-0123456789"}}
	c.add(time.Second)
	if res, err := f.Probe(context.Background(), a, "en", "2026-11-23"); err != nil || res.OK || res.Raw != "" || res.Message != MsgRepeatsHeader || res.Quote == nil {
		t.Fatalf("%+v %v", res, err)
	}
}

// A link takes values of the answer and the request's language and day,
// never a request header, whatever the path is spelled like.
func TestALinkNeverTakesAHeader(t *testing.T) {
	for _, link := range []string{"https://e.org/?k={headers.Authorization}", "https://e.org/?k={headers.authorization}", "https://e.org/{args.X-Api-Key}",
		"https://e.org/{header}", "https://e.org/{[0].Headers[1]}", "https://e.org/{x-api-key}"} {
		a := okAPI()
		a.Link = link
		if _, err := CheckAPI(a, false); err == nil {
			t.Errorf("%s is accepted", link)
		}
	}
	a := API{Text: "t", Link: "https://e.org/{lang}/{date}?id={id}&n={lang}x"}
	q, err := Extract(a, []byte(`{"t":"x","id":"a b","lang":"no"}`), "zh", "2026-11-23")
	if err != nil || q.Link != "https://e.org/zh/2026-11-23?id=a+b&n=zhx" {
		t.Fatalf("%+v %v", q, err)
	}
}

// Hosts written only with numbers, in shorthand forms, are refused whatever
// the resolver would make of them.
func TestNumericHostShorthandsAreRefused(t *testing.T) {
	for h, want := range map[string]bool{
		"127.1": true, "2130706433": true, "0x7f.0.0.1": true, "0177.0.0.1": true, "10.1": true, "1.": true,
		"127.0.0.1": false, "::1": false, "example.com": false, "cafe.de": false, "0x.example": false, "v1.hitokoto.cn": false, "": false,
	} {
		if got := numericHost(h); got != want {
			t.Errorf("numericHost(%q) = %v, want %v", h, got, want)
		}
	}
}

// The language mapping is applied before {lang} is filled in, in the address
// and in the link; a language it does not name is matched to one it does, and
// with no match the tag itself is used.
func TestLangMapIsAppliedBeforeFilling(t *testing.T) {
	s := newSvc(t)
	a := s.api()
	a.URL = s.URL + "/q?l={lang}&d={date}"
	a.Link = "https://example.org/{lang}/{uuid}"
	a.LangMap = map[string]string{"zh-Hans": "zh", "zh-Hant": "zh-tw"}
	f, _ := fetcher(nil)
	ctx := context.Background()
	for lang, want := range map[string]string{"zh-Hans": "zh", "zh-Hant": "zh-tw", "zh-CN": "zh", "zh-HK": "zh-tw", "en": "en", "": "en", "pt-BR": "pt-BR"} {
		req, err := f.request(ctx, a, lang, "2026-11-23")
		if err != nil || req.URL.Query().Get("l") != want {
			t.Errorf("%q: %v %v, want %q", lang, req, err, want)
		}
	}
	r, err := f.Get(ctx, a, "zh-Hant", "2026-11-23", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.paths.Load("/q?l=zh-tw&d=2026-11-23"); !ok {
		t.Fatal("the service was not asked in its own code")
	}
	if !strings.HasPrefix(r.Link, "https://example.org/zh-tw/") {
		t.Fatalf("%q", r.Link)
	}
	res, err := f.Probe(ctx, a, "zh-Hans", "2026-11-23")
	if err != nil || !strings.Contains(res.URL, "l=zh&") {
		t.Fatalf("%+v %v", res, err)
	}
	// What the service is asked is part of what is kept: another mapping is another answer.
	b := a
	b.LangMap = map[string]string{"zh-Hant": "zh-hk"}
	if a.key() == b.key() {
		t.Fatal("two mappings share what is kept")
	}
}

// An interface may contact only the host and port of its own address, whatever
// is written in the rest of it, and nothing else needs to be allowed.
func TestAnInterfaceContactsOnlyItsOwnHostAndPort(t *testing.T) {
	s := newSvc(t)
	a := s.api()
	f, _ := fetcher(nil)
	ctx := context.Background()
	if _, err := f.Get(ctx, a, "en", "2026-11-23", 0); err != nil {
		t.Fatalf("an address needs no list of hosts: %v", err)
	}
	for _, u := range []string{"http://h{lang}.test/q", "http://h.test:{date}/q", "http://{lang}@h.test/q"} {
		b := API{ID: "t", Name: "t", URL: u, Text: "text"}
		if _, err := f.request(ctx, b, "fr", "2026-11-23"); err == nil {
			t.Errorf("%s was asked", u)
		}
	}
	// Another port is another host: the one written is the only one.
	b := a
	b.URL = strings.Replace(a.URL, "127.0.0.1:", "127.0.0.1:{lang}", 1)
	if _, err := f.request(ctx, b, "1", "2026-11-23"); err == nil {
		t.Error("a port made with the language was asked")
	}
}

// A service that is no longer built in is simply not found.
func TestProviderByIDRemoved(t *testing.T) {
	if _, ok := ProviderByID("quotable"); ok {
		t.Fatal("quotable is gone")
	}
}

func TestProviderOptions(t *testing.T) {
	p, _ := ProviderByID("hitokoto")
	if len(p.Options) != 1 || p.Options[0].Key != "categories" || p.Options[0].Param != "c" || len(p.Options[0].Values) != 12 {
		t.Fatalf("%+v", p.Options)
	}
	if z, _ := ProviderByID("zenquotes"); len(z.Options) != 0 {
		t.Fatal("zenquotes has none")
	}
	// Canonical: declared order, each once; none or all is nothing.
	got, err := p.CheckOptions(map[string][]string{"categories": {"k", "d", "k"}})
	if err != nil || fmt.Sprint(got) != "map[categories:[d k]]" {
		t.Fatalf("%v %v", got, err)
	}
	for _, in := range []map[string][]string{nil, {"categories": nil}, {"categories": {"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"}}} {
		if got, err := p.CheckOptions(in); err != nil || len(got) != 0 {
			t.Errorf("%v: %v %v", in, got, err)
		}
	}
	for _, in := range []map[string][]string{{"categories": {"z"}}, {"categories": {"a&c=b"}}, {"genre": {"a"}}, {"categories": {""}}} {
		if _, err := p.CheckOptions(in); err == nil {
			t.Errorf("%v accepted", in)
		}
	}
	// The address.
	a, err := p.WithOptions(map[string][]string{"categories": {"i", "d"}})
	if err != nil || a.URL != "https://international.v1.hitokoto.cn/?max_length=80&c=d&c=i" || a.ID != "hitokoto" {
		t.Fatalf("%q %v", a.URL, err)
	}
	if a, _ := p.WithOptions(nil); a.URL != p.URL {
		t.Fatalf("%q", a.URL)
	}
	if _, err := p.WithOptions(map[string][]string{"categories": {"nope"}}); err == nil {
		t.Fatal("accepted")
	}
	// A parameter is joined with ? when the address has none, and values are escaped.
	q := Provider{API: API{ID: "x", URL: "https://example.org/q"}, Options: []Option{{Key: "k", Param: "a b", Kind: "multi", Values: []OptionValue{{Value: "é/1"}, {Value: "2"}}}}}
	if a, err := q.WithOptions(map[string][]string{"k": {"é/1"}}); err != nil || a.URL != "https://example.org/q?a+b=%C3%A9%2F1" {
		t.Fatalf("%q %v", a.URL, err)
	}
}

// Another address is another cache entry, and the rate limit is the id's.
func TestOptionSetsShareTheRateLimitAndNotTheCache(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"text":"%s"}`, r.URL.RawQuery)
	}))
	defer srv.Close()
	f := NewForTests(nil)
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	f.SetClock(func() time.Time { return now })
	base := API{ID: "x", Name: "x", URL: srv.URL + "/q", Text: "text", Refresh: RefreshDaily, RPS: 0.5, Enabled: true}
	p := Provider{API: base, Options: []Option{{Key: "k", Param: "c", Kind: "multi", Values: []OptionValue{{Value: "a"}, {Value: "b"}}}}}
	a1, _ := p.WithOptions(map[string][]string{"k": {"a"}})
	a2, _ := p.WithOptions(map[string][]string{"k": {"b"}})
	r1, err := f.Get(context.Background(), a1, "", "2026-11-23", 0)
	if err != nil || r1.Text != "c=a" {
		t.Fatalf("%v %v", r1, err)
	}
	if _, err := f.Get(context.Background(), a2, "", "2026-11-23", 0); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("the second set was not limited: %v", err)
	}
	now = now.Add(3 * time.Second)
	r2, err := f.Get(context.Background(), a2, "", "2026-11-23", 0)
	if err != nil || r2.Text != "c=b" {
		t.Fatalf("%v %v", r2, err)
	}
	// Each is kept for itself.
	if again, _ := f.Get(context.Background(), a1, "", "2026-11-23", 0); again.Text != "c=a" || hits.Load() != 2 {
		t.Fatalf("%v %d", again, hits.Load())
	}
}
