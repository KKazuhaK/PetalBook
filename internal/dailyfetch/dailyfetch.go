// Package dailyfetch gets the daily text from external JSON interfaces an
// administrator configured (a quote service, say), on the server's behalf.
// Browsers never call these interfaces: the server asks, keeps the answer, and
// gives everybody the same one.
//
// The server must not become a way to reach what only it can reach. So the
// interfaces are named by an administrator and never by a space owner or a
// reader; the address may only be a host on the administrators' list (saving an
// interface adds its host); and the connection itself is checked: the address
// a name resolves to, at the moment of connecting (so a name that answers
// differently the second time is no way round it), must be a public one,
// never a loopback, private, link-local, unspecified, multicast or
// carrier-grade-NAT address. Redirects are not followed and no proxy is used.
// The answer is JSON only, at most 64 KB, within 5 seconds.
//
// What an interface said is kept in three places: in memory, by (interface,
// language, refresh period, skip); in the state database (Backing), so that a
// restart asks for nothing again; and, by the caller, in the browser. When an
// interface does not answer, the last answer it gave is used, with its time,
// and when it has none the block is hidden; it is asked again after a wait that
// doubles with each failure, never at every request. Each interface has a rate
// limit on what this server sends it; with the bucket empty, a kept answer is
// used instead of waiting.
package dailyfetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// Limits.
const (
	Timeout      = 5 * time.Second
	MaxResponse  = 64 << 10
	MaxTextChars = 2000
	// MaxAPIs is how many interfaces an administrator may configure.
	MaxAPIs = 50
	// MaxHosts is how many hosts may be allowed.
	MaxHosts = 100
	// MaxHeaders is how many request headers an interface may have.
	MaxHeaders = 10
	// MaxSkip is the largest skip that asks an interface for a new item.
	MaxSkip = 100
	// Retention is how long what an interface said is kept.
	Retention = 7 * 24 * time.Hour
	// RawLimit is how much of an answer a trial shows.
	RawLimit = 4000

	backoffBase = 30 * time.Second
	backoffMax  = time.Hour
	cacheMax    = 2048
	// laneMax bounds the lanes (an interface in a language) kept in memory: the
	// last answers, the failures and the rate limits.
	laneMax = 1024
	// MaxCachedRows is how many answers of one interface the Backing keeps
	// (the oldest go first); it is trimmed every trimEvery answers.
	MaxCachedRows = 2000
	trimEvery     = 32
	// DefaultRPS is the rate limit of an interface that has none set.
	DefaultRPS = 1.0
	// MaxRPS is the largest rate limit.
	MaxRPS = 100.0
)

// Refresh periods.
const (
	RefreshDaily   = "daily"
	RefreshHourly  = "hourly"
	RefreshMinutes = "minutes"
)

// Header is a request header of an interface. The value is in the clear only
// in memory: the caller keeps it encrypted.
type Header struct {
	Name  string
	Value string
}

// API is an external interface of the daily text.
type API struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Lang is the language the interface answers in; "" for every language
	// (an address that takes {lang}, or an interface that has no language).
	Lang string `json:"lang"`
	// URL is the address, fetched with GET; {lang} stands for the language asked
	// for and {date} for the day.
	URL     string   `json:"url"`
	Headers []Header `json:"-"`
	// Text, Source and Author are paths in the JSON answer: names and positions
	// with dots and brackets (`data.0.q`, `[0].content`, `hitokoto`). Text is
	// needed.
	Text   string `json:"text"`
	Source string `json:"source"`
	Author string `json:"author"`
	// Link is a template of a link to show with the text (an attribution): {x}
	// is the value at the path x of the answer, `https://hitokoto.cn?uuid={uuid}`.
	Link string `json:"link"`
	// Refresh is how long the item everybody sees stays the same: "daily" (the
	// day asked for), "hourly", or "minutes" (every RefreshMinutes).
	Refresh        string `json:"refresh"`
	RefreshMinutes int    `json:"refreshMinutes"`
	// SkipNew says "another" asks the interface for a new item each time.
	SkipNew bool `json:"skipNew"`
	// RPS is how many requests a second this server sends the interface at most.
	RPS float64 `json:"rps"`
	// Enabled interfaces are used.
	Enabled bool `json:"enabled"`
}

// Quote is what an interface said.
type Quote struct {
	Text   string `json:"text"`
	Source string `json:"source,omitempty"`
	Author string `json:"author,omitempty"`
	// Link is the attribution link, when the interface has a template.
	Link string `json:"link,omitempty"`
}

// Result is a Quote with when it was got. Stale says the interface could not be
// asked and this is the last thing it said.
type Result struct {
	Quote
	Fetched time.Time `json:"fetched"`
	Stale   bool      `json:"stale,omitempty"`
}

// Errors.
var (
	// ErrRateLimited: the interface's rate limit is used up and nothing is kept to give.
	ErrRateLimited = errors.New("the interface was asked too often")
	// ErrUnavailable: the interface did not give a usable answer and nothing is kept to give.
	ErrUnavailable = errors.New("the interface gave no usable answer")
)

// ValidID reports whether s can be the id of an interface.
func ValidID(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// CheckHost validates an entry of the list of hosts: a name or an address
// with an optional port, no scheme, no path.
func CheckHost(h string) (string, error) {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" || len(h) > 253 || strings.ContainsAny(h, "/@?# \t\n") {
		return "", fmt.Errorf("%q is not a host name", h)
	}
	u, err := url.Parse("https://" + h)
	if err != nil || u.Host != h || u.Hostname() == "" {
		return "", fmt.Errorf("%q is not a host name", h)
	}
	return h, nil
}

var forbiddenHeaders = map[string]bool{"host": true, "content-length": true, "transfer-encoding": true, "connection": true, "upgrade": true,
	"te": true, "trailer": true, "proxy-authorization": true, "proxy-connection": true, "keep-alive": true, "content-type": true}

// CheckHeader validates a request header.
func CheckHeader(h Header) error {
	if h.Name == "" || len(h.Name) > 100 {
		return errors.New("a header has a name of at most 100 characters")
	}
	for _, r := range h.Name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return fmt.Errorf("%q is not a header name", h.Name)
		}
	}
	if forbiddenHeaders[strings.ToLower(h.Name)] {
		return fmt.Errorf("the header %s cannot be set", h.Name)
	}
	if len(h.Value) > 2000 || strings.ContainsAny(h.Value, "\r\n\x00") {
		return fmt.Errorf("the value of %s is one line of at most 2000 bytes", h.Name)
	}
	return nil
}

// CheckAPI validates an interface and says which host it is to be fetched
// from (what saving adds to the list). allowHTTP is for tests. The headers'
// names and values are checked too.
func CheckAPI(a API, allowHTTP bool) (API, string, error) {
	a.Name = strings.TrimSpace(a.Name)
	a.Lang = strings.ToLower(strings.TrimSpace(a.Lang))
	if !ValidID(a.ID) {
		return a, "", errors.New("an interface id is 1 to 32 letters, digits, - or _")
	}
	if a.Name == "" || utf8.RuneCountInString(a.Name) > 100 {
		return a, "", errors.New("an interface needs a name of at most 100 characters")
	}
	if a.Lang != "" && !validLang(a.Lang) {
		return a, "", errors.New("lang is a language code like zh or en, or empty for every language")
	}
	if len(a.URL) > 2000 || strings.ContainsAny(a.URL, " \t\r\n") {
		return a, "", errors.New("that is not an address")
	}
	// The scheme and the host are read from the address as it is written, before
	// anything is filled in: a placeholder there would let the language or the
	// day a reader asks for choose where the server connects to.
	scheme, auth, ok := splitRaw(a.URL)
	if !ok {
		return a, "", errors.New("that is not an address")
	}
	if strings.ContainsAny(scheme+auth, "{}") {
		return a, "", errors.New("{lang} and {date} may be in the path or the query, not in the scheme or the host")
	}
	probe := strings.NewReplacer("{lang}", "en", "{date}", "2026-01-01").Replace(a.URL)
	u, err := url.Parse(probe)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || strings.ToLower(u.Host) != auth {
		return a, "", errors.New("that is not an address")
	}
	if u.Scheme != "https" && !(allowHTTP && u.Scheme == "http") {
		return a, "", errors.New("the address must be https")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !allowHTTP && !Public(ip) {
		return a, "", fmt.Errorf("%s is not a public address; the server never connects to one", ip)
	}
	if h := strings.ToLower(u.Hostname()); !allowHTTP && (h == "localhost" || strings.HasSuffix(h, ".localhost")) {
		return a, "", errors.New("the server never connects to this machine")
	}
	for _, p := range []*string{&a.Text, &a.Source, &a.Author} {
		*p = strings.TrimSpace(*p)
		if len(*p) > 200 || strings.ContainsAny(*p, "\r\n") {
			return a, "", errors.New("a JSON path is names and positions with dots, like data.0.q or [0].q")
		}
	}
	if a.Text == "" {
		return a, "", errors.New("text is the JSON path of the text; it is needed")
	}
	a.Link = strings.TrimSpace(a.Link)
	if a.Link != "" {
		lu, err := url.Parse(linkTemplate(a.Link, func(string) string { return "x" }))
		if err != nil || (lu.Scheme != "https" && lu.Scheme != "http") || lu.Host == "" || len(a.Link) > 500 {
			return a, "", errors.New("the link is an http or https address, with {path} where a value of the answer goes")
		}
	}
	switch a.Refresh {
	case "":
		a.Refresh = RefreshDaily
	case RefreshDaily, RefreshHourly:
	case RefreshMinutes:
		if a.RefreshMinutes < 1 || a.RefreshMinutes > 1440 {
			return a, "", errors.New("refreshMinutes is 1 to 1440")
		}
	default:
		return a, "", errors.New("refresh is daily, hourly or minutes")
	}
	if a.Refresh != RefreshMinutes {
		a.RefreshMinutes = 0
	}
	if a.RPS == 0 {
		a.RPS = DefaultRPS
	}
	if a.RPS < 0.01 || a.RPS > MaxRPS {
		return a, "", errors.New("rps is the most requests a second, 0.01 to 100")
	}
	if len(a.Headers) > MaxHeaders {
		return a, "", fmt.Errorf("at most %d headers", MaxHeaders)
	}
	seen := map[string]bool{}
	for _, h := range a.Headers {
		if err := CheckHeader(h); err != nil {
			return a, "", err
		}
		if seen[strings.ToLower(h.Name)] {
			return a, "", fmt.Errorf("the header %s is given twice", h.Name)
		}
		seen[strings.ToLower(h.Name)] = true
	}
	return a, auth, nil
}

// splitRaw cuts an address as it is written, placeholders and all, into its
// scheme and its authority (user, host and port), both in lower case.
func splitRaw(raw string) (scheme, authority string, ok bool) {
	i := strings.Index(raw, "://")
	if i <= 0 {
		return "", "", false
	}
	rest := raw[i+3:]
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		rest = rest[:j]
	}
	return strings.ToLower(raw[:i]), strings.ToLower(rest), rest != ""
}

// Origin is the scheme and the host (with the port) of an address as it is
// written, "" when it has none: where a header value is sent.
func Origin(raw string) string {
	scheme, auth, ok := splitRaw(raw)
	if !ok {
		return ""
	}
	return scheme + "://" + auth
}

// LaneLang is the language an interface is asked in for a reader who asks in
// lang, so that languages the answer cannot differ in are not asked for apart:
// "" when the address does not take {lang}; the interface's own language when
// it has one; else lang, or its base language, when it is one of langs (the
// languages the server has), else the first of langs.
func (a API) LaneLang(lang string, langs []string) string {
	if !strings.Contains(a.URL, "{lang}") {
		return ""
	}
	if a.Lang != "" {
		return a.Lang
	}
	base := lang
	if i := strings.IndexByte(lang, '-'); i > 0 {
		base = lang[:i]
	}
	for _, want := range []string{lang, base} {
		for _, l := range langs {
			if want != "" && l == want {
				return l
			}
		}
	}
	if len(langs) > 0 {
		return langs[0]
	}
	return ""
}

func validLang(s string) bool {
	if len(s) < 2 || len(s) > 16 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func hostAllowed(host string, hosts []string) bool {
	host = strings.ToLower(host)
	for _, h := range hosts {
		if h == host {
			return true
		}
	}
	return false
}

// key identifies the interface as it is asked: what makes another answer
// another thing. The value of a header is not part of it.
func (a API) key() string {
	h := sha256.New()
	fmt.Fprint(h, a.ID, "\x00", a.URL, "\x00", a.Text, "\x00", a.Source, "\x00", a.Author, "\x00", a.Link)
	for _, hd := range a.Headers {
		fmt.Fprint(h, "\x00", hd.Name)
	}
	return a.ID + ":" + hex.EncodeToString(h.Sum(nil))[:12]
}

// Period is the refresh period of the item everybody sees at now: for "daily"
// the day asked for, else the hour or the run of minutes that now is in. ends
// is when the period is over, zero for a day (the caller knows the time zone).
func (a API) Period(now time.Time, date string) (key string, ends time.Time) {
	var d time.Duration
	switch a.Refresh {
	case RefreshHourly:
		d = time.Hour
	case RefreshMinutes:
		d = time.Duration(a.RefreshMinutes) * time.Minute
	default:
		return "d:" + date, time.Time{}
	}
	idx := now.Unix() / int64(d/time.Second)
	return "p:" + strconv.Itoa(int(d/time.Minute)) + ":" + strconv.FormatInt(idx, 10), time.Unix((idx+1)*int64(d/time.Second), 0)
}

// Backing keeps what interfaces said beyond a restart.
type Backing interface {
	Get(ctx context.Context, apiKey, lang, period string, skip int) (Result, bool, error)
	Put(ctx context.Context, apiKey, lang, period string, skip int, r Result) error
	// Latest is the newest thing the interface said in the language, whatever the period.
	Latest(ctx context.Context, apiKey, lang string) (Result, bool, error)
	// Prune forgets what was got before the time.
	Prune(ctx context.Context, before time.Time) error
}

// Trimmer is a Backing that can keep only the newest answers of an interface.
type Trimmer interface {
	Trim(ctx context.Context, apiKey string, keep int) error
}

// Fetcher asks the interfaces. It is safe for concurrent use.
type Fetcher struct {
	client    *http.Client
	timeout   time.Duration
	allowHTTP bool
	now       func() time.Time
	backing   Backing

	mu      sync.Mutex
	cache   map[cacheKey]Result
	last    map[laneKey]Result
	buckets map[string]*bucket
	fails   map[laneKey]failure
	busy    map[cacheKey]*flight
	pruned  time.Time
	probe   bucket
	// Sent counts the requests that went out (tests).
	sent int
}

type cacheKey struct {
	api, lang, period string
	skip              int
}

// laneKey is one interface in one language.
type laneKey struct{ api, lang string }

type flight struct {
	done chan struct{}
	r    Result
	err  error
}

type failure struct {
	n    int
	next time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
	// puts counts the answers of the interface kept, for trimming.
	puts int
}

// take uses one token of a bucket that holds up to max(1, rps) and fills at
// rps a second.
func (b *bucket) take(now time.Time, rps float64) bool {
	capacity := max(1, rps)
	if b.at.IsZero() {
		b.tokens = capacity
	} else if dt := now.Sub(b.at).Seconds(); dt > 0 {
		b.tokens += dt * rps
	}
	b.tokens = min(capacity, b.tokens)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// New makes a Fetcher that refuses any address that is not a public one.
func New(backing Backing) *Fetcher {
	d := &net.Dialer{Timeout: Timeout, Control: refusePrivate}
	return newFetcher(&http.Client{
		Timeout:       Timeout,
		Transport:     &http.Transport{DialContext: d.DialContext, Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: Timeout, MaxResponseHeaderBytes: 16 << 10},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, false, backing)
}

// NewForTests makes a Fetcher that may connect to any address and use plain
// http, so a test can talk to a server of its own. Never use it in the server.
func NewForTests(backing Backing) *Fetcher {
	return newFetcher(&http.Client{
		Timeout:       Timeout,
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, true, backing)
}

func newFetcher(c *http.Client, allowHTTP bool, backing Backing) *Fetcher {
	return &Fetcher{client: c, timeout: Timeout, allowHTTP: allowHTTP, backing: backing, now: time.Now,
		cache: map[cacheKey]Result{}, last: map[laneKey]Result{}, buckets: map[string]*bucket{}, fails: map[laneKey]failure{}, busy: map[cacheKey]*flight{}}
}

// AllowHTTP reports whether plain http addresses are accepted (tests only).
func (f *Fetcher) AllowHTTP() bool { return f.allowHTTP }

// SetTimeout shortens the time an answer may take (tests).
func (f *Fetcher) SetTimeout(d time.Duration) { f.timeout = d; f.client.Timeout = d }

// SetClock replaces the clock (tests).
func (f *Fetcher) SetClock(now func() time.Time) { f.mu.Lock(); f.now = now; f.mu.Unlock() }

// Now is the time the fetcher works by.
func (f *Fetcher) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now() }

// Sent is how many requests went out to interfaces.
func (f *Fetcher) Sent() int { f.mu.Lock(); defer f.mu.Unlock(); return f.sent }

// ForgetMemory drops what is kept in memory, as a restart would (tests).
func (f *Fetcher) ForgetMemory() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cache, f.last, f.fails = map[cacheKey]Result{}, map[laneKey]Result{}, map[laneKey]failure{}
}

// refusePrivate is the Control of the dialer: it runs for every address a name
// resolved to, just before the connection is made to it.
func refusePrivate(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("not an address: %q", host)
	}
	if !Public(ip) {
		return fmt.Errorf("%s is not a public address", ip)
	}
	return nil
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// Public reports whether an address is one the open internet has: not
// loopback, private, link-local (the cloud metadata address included),
// unspecified, multicast, carrier-grade NAT, or a documentation or benchmark
// range; an IPv4 address written as IPv6 is judged as the IPv4 address.
func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is6() && ip.Zone() != "" {
		return false
	}
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip.Is4() {
		if cgnat.Contains(ip) {
			return false
		}
		b := ip.As4()
		switch {
		case b[0] == 0, b[0] >= 240: // "this network", reserved, broadcast
			return false
		case b[0] == 192 && b[1] == 0 && b[2] == 0, // IETF protocol assignments
			b[0] == 192 && b[1] == 0 && b[2] == 2,     // documentation
			b[0] == 198 && (b[1] == 18 || b[1] == 19), // benchmarking
			b[0] == 198 && b[1] == 51 && b[2] == 100,
			b[0] == 203 && b[1] == 0 && b[2] == 113:
			return false
		}
		return true
	}
	b := ip.As16()
	if b[0] == 0x20 && b[1] == 0x01 && b[2] == 0x0d && b[3] == 0xb8 {
		return false
	}
	if b[0] == 0x00 && b[1] == 0x64 && b[2] == 0xff && b[3] == 0x9b || b[0] == 0x20 && b[1] == 0x02 {
		return false
	}
	return true
}

// Get returns what the interface says for a language in the refresh period
// that date (for a daily interface) or now (for the others) is in. The item is
// asked for once a period and kept, in memory and in the Backing; "another"
// (skip above 0) asks for a new item each time when the interface has SkipNew,
// else it is the same item. When the interface cannot be asked (it failed
// lately and the wait has not passed, its rate limit is used up, it fails now)
// the last thing it said is returned, marked Stale; with none, ErrUnavailable
// or ErrRateLimited. hosts are the hosts allowed now.
func (f *Fetcher) Get(ctx context.Context, a API, hosts []string, lang, date string, skip int) (Result, error) {
	if !a.SkipNew {
		skip = 0
	}
	skip = max(0, min(skip, MaxSkip))
	// A language the answer cannot differ in is not another lane.
	if !strings.Contains(a.URL, "{lang}") {
		lang = ""
	} else if a.Lang != "" {
		lang = a.Lang
	}
	f.mu.Lock()
	now := f.now()
	period, _ := a.Period(now, date)
	ak := a.key()
	key := cacheKey{ak, lang, period, skip}
	if r, ok := f.cache[key]; ok {
		f.mu.Unlock()
		return r, nil
	}
	if fl, ok := f.busy[key]; ok {
		f.mu.Unlock()
		select {
		case <-fl.done:
			return fl.r, fl.err
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	fl := &flight{done: make(chan struct{})}
	f.busy[key] = fl
	f.mu.Unlock()

	// The one request is everybody's: it is not cut short when the reader who
	// happened to start it goes away (which would count as a failure of the
	// interface and make everybody wait), only by its own time.
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*f.timeout)
	r, err := f.lead(lctx, a, hosts, lang, date, skip, key)
	cancel()

	f.mu.Lock()
	delete(f.busy, key)
	f.mu.Unlock()
	fl.r, fl.err = r, err
	close(fl.done)
	return r, err
}

func (f *Fetcher) remember(key cacheKey, r Result) {
	if len(f.cache) >= cacheMax {
		f.cache = map[cacheKey]Result{}
	}
	f.cache[key] = r
	lane := laneKey{key.api, key.lang}
	if _, ok := f.last[lane]; !ok && len(f.last) >= laneMax {
		f.pruneLast()
	}
	f.last[lane] = r
}

// pruneLast makes room among the last answers (under mu): those past the
// retention go, then, if that is not enough, the older half.
func (f *Fetcher) pruneLast() {
	now := f.now()
	times := make([]int64, 0, len(f.last))
	for k, v := range f.last {
		if now.Sub(v.Fetched) >= Retention {
			delete(f.last, k)
			continue
		}
		times = append(times, v.Fetched.UnixNano())
	}
	if len(f.last) < laneMax {
		return
	}
	slices.Sort(times)
	cut := times[len(times)/2]
	for k, v := range f.last {
		if v.Fetched.UnixNano() <= cut {
			delete(f.last, k)
		}
	}
}

// fail records a failure of a lane (under mu), making room when the lanes are
// many: the failures whose wait is long over go, then those that end soonest.
func (f *Fetcher) fail(lane laneKey, now time.Time) {
	if _, ok := f.fails[lane]; !ok && len(f.fails) >= laneMax {
		ends := make([]int64, 0, len(f.fails))
		for k, v := range f.fails {
			if now.Sub(v.next) > backoffMax {
				delete(f.fails, k)
				continue
			}
			ends = append(ends, v.next.UnixNano())
		}
		if len(f.fails) >= laneMax {
			slices.Sort(ends)
			cut := ends[len(ends)/2]
			for k, v := range f.fails {
				if v.next.UnixNano() <= cut {
					delete(f.fails, k)
				}
			}
		}
	}
	n := f.fails[lane].n + 1
	f.fails[lane] = failure{n, now.Add(min(backoffMax, backoffBase<<min(n-1, 7)))}
}

// bucketOf is the rate limit of an interface (under mu). A bucket idle long
// enough to be full again is the same as a new one, so those go when there
// are many.
func (f *Fetcher) bucketOf(api string, now time.Time) *bucket {
	if b := f.buckets[api]; b != nil {
		return b
	}
	if len(f.buckets) >= laneMax {
		for k, v := range f.buckets {
			if now.Sub(v.at) > 2*time.Minute {
				delete(f.buckets, k)
			}
		}
	}
	b := &bucket{}
	f.buckets[api] = b
	return b
}

// Lanes is how many lanes the fetcher keeps in memory: last answers,
// failures and rate limits (tests).
func (f *Fetcher) Lanes() (last, fails, buckets int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.last), len(f.fails), len(f.buckets)
}

// lead does what the one request in flight for a key does.
func (f *Fetcher) lead(ctx context.Context, a API, hosts []string, lang, date string, skip int, key cacheKey) (Result, error) {
	// The state database: what was got before a restart, or by another process.
	if f.backing != nil {
		if r, ok, err := f.backing.Get(ctx, key.api, lang, key.period, skip); err == nil && ok {
			f.mu.Lock()
			f.remember(key, r)
			f.mu.Unlock()
			return r, nil
		}
	}
	lane := laneKey{key.api, lang}
	f.mu.Lock()
	now := f.now()
	if fl := f.fails[lane]; now.Before(fl.next) {
		f.mu.Unlock()
		return f.fallback(ctx, lane, ErrUnavailable)
	}
	b := f.bucketOf(key.api, now)
	if !b.take(now, a.RPS) {
		f.mu.Unlock()
		return f.fallback(ctx, lane, ErrRateLimited)
	}
	f.sent++
	f.mu.Unlock()

	if !hostAllowed(hostOf(a.URL), hosts) {
		return f.fallback(ctx, lane, fmt.Errorf("%w: the host is not allowed", ErrUnavailable))
	}
	reqDate := date
	if a.Refresh != RefreshDaily {
		reqDate = now.UTC().Format("2006-01-02")
	}
	q, err := f.fetch(ctx, a, hosts, lang, reqDate)
	f.mu.Lock()
	now = f.now()
	if err != nil {
		f.fail(lane, now)
		f.mu.Unlock()
		return f.fallback(ctx, lane, err)
	}
	delete(f.fails, lane)
	r := Result{Quote: q, Fetched: now}
	f.remember(key, r)
	prune := f.backing != nil && now.Sub(f.pruned) > time.Hour
	if prune {
		f.pruned = now
	}
	b.puts++
	trim := b.puts%trimEvery == 0
	f.mu.Unlock()
	if f.backing != nil {
		if err := f.backing.Put(ctx, key.api, lang, key.period, skip, r); err == nil {
			if prune {
				f.backing.Prune(ctx, now.Add(-Retention))
			}
			if t, ok := f.backing.(Trimmer); ok && trim {
				t.Trim(ctx, key.api, MaxCachedRows)
			}
		}
	}
	return r, nil
}

// fallback is the last thing the interface said in the language, if it is not
// too old; else the error.
func (f *Fetcher) fallback(ctx context.Context, lane laneKey, why error) (Result, error) {
	f.mu.Lock()
	r, ok := f.last[lane]
	now := f.now()
	f.mu.Unlock()
	if !ok && f.backing != nil {
		if got, found, err := f.backing.Latest(ctx, lane.api, lane.lang); err == nil && found {
			r, ok = got, true
		}
	}
	if ok && now.Sub(r.Fetched) < Retention {
		r.Stale = true
		return r, nil
	}
	return Result{}, why
}

// hostOf is the host (with the port) of an address as it is written.
func hostOf(raw string) string {
	_, auth, _ := splitRaw(raw)
	return auth
}

// fill puts the language and the day in an address. Both are checked to be
// what they say (a language code, a day) and escaped, so they stay a piece of
// the path or the query.
func fill(raw, lang, date string) string {
	if lang == "" || !validLang(lang) {
		lang = "en"
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		date = ""
	}
	return strings.NewReplacer("{lang}", url.PathEscape(lang), "{date}", url.PathEscape(date)).Replace(raw)
}

func (f *Fetcher) request(ctx context.Context, a API, hosts []string, lang, date string) (*http.Request, error) {
	u, err := url.Parse(fill(a.URL, lang, date))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !(f.allowHTTP && u.Scheme == "http")) {
		return nil, fmt.Errorf("%w: bad address", ErrUnavailable)
	}
	// Where the request goes is where the address as written goes, and it is
	// allowed: checked again on the address really used.
	if h := strings.ToLower(u.Host); h != hostOf(a.URL) || !hostAllowed(h, hosts) {
		return nil, fmt.Errorf("%w: the host %s is not on the list of hosts", ErrUnavailable, u.Host)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: bad address", ErrUnavailable)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PetalBook")
	for _, h := range a.Headers {
		req.Header.Set(h.Name, h.Value)
	}
	return req, nil
}

// do sends the request and reads the answer: status, the body (at most
// MaxResponse) and whether it is JSON.
func (f *Fetcher) do(ctx context.Context, a API, hosts []string, lang, date string) (status int, body []byte, isJSON bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	req, err := f.request(ctx, a, hosts, lang, date)
	if err != nil {
		return 0, nil, false, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, nil, false, fmt.Errorf("%w: %v", ErrUnavailable, trimErr(err))
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if err != nil {
		return resp.StatusCode, nil, false, fmt.Errorf("%w: %v", ErrUnavailable, trimErr(err))
	}
	isJSON = strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "json")
	return resp.StatusCode, body, isJSON, nil
}

func (f *Fetcher) fetch(ctx context.Context, a API, hosts []string, lang, date string) (Quote, error) {
	status, body, isJSON, err := f.do(ctx, a, hosts, lang, date)
	switch {
	case err != nil:
		return Quote{}, err
	case status != http.StatusOK:
		return Quote{}, fmt.Errorf("%w: status %d", ErrUnavailable, status)
	case !isJSON:
		return Quote{}, fmt.Errorf("%w: not JSON", ErrUnavailable)
	case len(body) > MaxResponse:
		return Quote{}, fmt.Errorf("%w: the answer is larger than %d bytes", ErrUnavailable, MaxResponse)
	}
	return Extract(a, body)
}

// ProbeResult is what one trial request showed.
type ProbeResult struct {
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"`
	URL    string `json:"url"`
	// Raw is the start of the answer when it is JSON.
	Raw          string `json:"raw,omitempty"`
	RawTruncated bool   `json:"rawTruncated,omitempty"`
	Quote        *Quote `json:"quote,omitempty"`
	// Message says what went wrong with the request or the answer.
	Message string `json:"message,omitempty"`
	// PathErrors say, by field (text, source, author, link), what a path found
	// no value at.
	PathErrors map[string]string `json:"pathErrors,omitempty"`
}

// Probe makes one request now, at the same rules as any other (the host must be
// one of hosts, and the address is checked when the connection is made) and
// shows the answer as it came and what the paths take out of it, with the
// values of the request's headers blotted out wherever the answer repeats
// them. It is not kept, and is limited to a few a second whatever the
// interface says.
func (f *Fetcher) Probe(ctx context.Context, a API, hosts []string, lang, date string) (ProbeResult, error) {
	f.mu.Lock()
	ok := f.probe.take(f.now(), 2)
	f.mu.Unlock()
	if !ok {
		return ProbeResult{}, ErrRateLimited
	}
	res := ProbeResult{URL: fill(a.URL, lang, date)}
	if !hostAllowed(hostOf(a.URL), hosts) {
		res.Message = "the host " + hostOf(a.URL) + " is not on the list of hosts"
		return res, nil
	}
	status, body, isJSON, err := f.do(ctx, a, hosts, lang, date)
	res.Status = status
	if err != nil {
		res.Message = strings.TrimPrefix(err.Error(), ErrUnavailable.Error()+": ")
		return res, nil
	}
	if !isJSON {
		res.Message = "the answer is not JSON (its Content-Type does not say so)"
		return res, nil
	}
	if len(body) > MaxResponse {
		res.Message = fmt.Sprintf("the answer is larger than %d bytes", MaxResponse)
		return res, nil
	}
	raw := scrub(a, string(body))
	if utf8.RuneCountInString(raw) > RawLimit {
		raw, res.RawTruncated = string([]rune(raw)[:RawLimit]), true
	}
	res.Raw = strings.ToValidUTF8(raw, "�")
	if status != http.StatusOK {
		res.Message = fmt.Sprintf("status %d", status)
		return res, nil
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		res.Message = "the answer is not valid JSON"
		return res, nil
	}
	res.PathErrors = map[string]string{}
	q := Quote{}
	for _, f := range []struct {
		name, path string
		set        func(string)
		need       bool
	}{
		{"text", a.Text, func(s string) { q.Text = cut(strings.TrimSpace(s), MaxTextChars) }, true},
		{"source", a.Source, func(s string) { q.Source = cut(strings.TrimSpace(s), 200) }, false},
		{"author", a.Author, func(s string) { q.Author = cut(strings.TrimSpace(s), 200) }, false},
	} {
		if f.path == "" {
			continue
		}
		s, found := pathText(doc, f.path)
		if !found || (f.need && strings.TrimSpace(s) == "") {
			res.PathErrors[f.name] = "no text or number at " + f.path
			continue
		}
		f.set(s)
	}
	if a.Link != "" {
		q.Link = expandLink(a.Link, doc)
		if q.Link == "" {
			res.PathErrors["link"] = "a value the link needs is not in the answer"
		}
	}
	if len(res.PathErrors) == 0 {
		res.PathErrors = nil
	}
	if q.Text != "" {
		q = scrubQuote(a, q)
		res.Quote = &q
		res.OK = res.PathErrors == nil
	} else if res.Message == "" {
		res.Message = "no text could be taken out of the answer"
	}
	return res, nil
}

// minSecret is the shortest header value blotted out of answers: shorter ones
// are not secrets, and blotting them would garble the answer.
const minSecret = 4

// scrub blots out of an answer the values of the request's headers, as they
// are and as JSON writes them, so that a service that repeats what it was sent
// does not show a key.
func scrub(a API, s string) string {
	for _, h := range a.Headers {
		if len(h.Value) < minSecret {
			continue
		}
		s = strings.ReplaceAll(s, h.Value, "•••")
		if b, err := json.Marshal(h.Value); err == nil {
			s = strings.ReplaceAll(s, string(b[1:len(b)-1]), "•••")
		}
	}
	return s
}

func scrubQuote(a API, q Quote) Quote {
	q.Text, q.Source, q.Author, q.Link = scrub(a, q.Text), scrub(a, q.Source), scrub(a, q.Author), scrub(a, q.Link)
	return q
}

func trimErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return err.Error()
}

// Extract reads the text, source, author and link out of an answer.
func Extract(a API, body []byte) (Quote, error) {
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return Quote{}, fmt.Errorf("%w: not JSON", ErrUnavailable)
	}
	text, ok := pathText(doc, a.Text)
	text = strings.TrimSpace(text)
	if !ok || text == "" {
		return Quote{}, fmt.Errorf("%w: no text at %s", ErrUnavailable, a.Text)
	}
	q := Quote{Text: cut(text, MaxTextChars)}
	if a.Source != "" {
		s, _ := pathText(doc, a.Source)
		q.Source = cut(strings.TrimSpace(s), 200)
	}
	if a.Author != "" {
		s, _ := pathText(doc, a.Author)
		q.Author = cut(strings.TrimSpace(s), 200)
	}
	if a.Link != "" {
		q.Link = expandLink(a.Link, doc)
	}
	return scrubQuote(a, q), nil
}

// linkTemplate fills {path} in a template with value(path).
func linkTemplate(tpl string, value func(path string) string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(tpl, '{')
		if i < 0 {
			b.WriteString(tpl)
			return b.String()
		}
		j := strings.IndexByte(tpl[i:], '}')
		if j < 0 {
			b.WriteString(tpl)
			return b.String()
		}
		b.WriteString(tpl[:i])
		b.WriteString(value(tpl[i+1 : i+j]))
		tpl = tpl[i+j+1:]
	}
}

// expandLink makes the link of a template from the answer; "" when a value it
// needs is not there or the result is not an http(s) address.
func expandLink(tpl string, doc any) string {
	missing := false
	out := linkTemplate(tpl, func(path string) string {
		s, ok := pathText(doc, path)
		if !ok || strings.TrimSpace(s) == "" {
			missing = true
		}
		return url.QueryEscape(strings.TrimSpace(s))
	})
	if missing {
		return ""
	}
	u, err := url.Parse(out)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return out
}

func cut(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// pathText follows a path of names and positions (`data.0.q`, `[0].q`) in a
// decoded JSON value and returns what is there if it is a text or a number.
func pathText(doc any, path string) (string, bool) {
	cur := doc
	path = strings.ReplaceAll(strings.ReplaceAll(path, "[", "."), "]", "")
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			continue
		}
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[seg]
			if !ok {
				return "", false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(v) {
				return "", false
			}
			cur = v[i]
		default:
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	}
	return "", false
}

// Preset is an interface a form can be filled in with: a starting point, not
// something that is on until an administrator saves it.
type Preset struct {
	API
	// Note says what to know of the service.
	Note string `json:"note"`
}

// Presets are the interfaces the program knows how to fill in. They are only
// offered; none is created or used until an administrator saves one.
func Presets() []Preset {
	return []Preset{
		{API: API{ID: "hitokoto", Name: "一言", Lang: "zh", URL: "https://international.v1.hitokoto.cn/?max_length=80",
			Text: "hitokoto", Source: "from", Author: "from_who", Link: "https://hitokoto.cn?uuid={uuid}",
			Refresh: RefreshDaily, SkipNew: true, RPS: 20, Enabled: true},
			Note: "Free, no key. The international line allows 20 requests a second; the service asks for a link back, which is filled in."},
		{API: API{ID: "quotable", Name: "Quotable", Lang: "en", URL: "https://api.quotable.io/quotes/random",
			Text: "[0].content", Author: "[0].author",
			Refresh: RefreshDaily, SkipNew: true, RPS: 3, Enabled: true},
			Note: "Open source, no key; 180 requests a minute for each address. It can also be hosted by yourself."},
	}
}
