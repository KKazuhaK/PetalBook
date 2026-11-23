// Package dailyfetch gets the daily text from external JSON interfaces an
// administrator configured (a quote service, say), on the server's behalf.
//
// The server must not become a way to reach what only it can reach. So the
// interfaces are named by an administrator and never by a space owner or a
// reader; the address may only be a host the administrator listed; and the
// connection itself is checked: the address a name resolves to, at the moment
// of connecting (so that a name which answers differently the second time is
// no way round it), must be a public one, never a loopback, private,
// link-local, unspecified, multicast or carrier-grade-NAT address. Redirects
// are not followed. The answer is JSON only, at most 64 KB, within 5 seconds.
//
// Each interface is asked once a day for each language, and what it said is
// kept, so everybody sees the same text; a further one ("another") is asked
// for again at a limited rate; a failure is remembered for a few minutes so
// that a service that is down is not asked for every page view.
package dailyfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
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
	MaxAPIs = 20
	// MaxHosts is how many hosts may be allowed.
	MaxHosts = 50
	// perMinute is how many requests one interface gets from this server in a
	// minute, however many people ask.
	perMinute = 12
	// failureMemory is how long a failure is remembered.
	failureMemory = 5 * time.Minute
	cacheMax      = 512
)

// API is an external interface of the daily text.
type API struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Lang is the language the interface answers in; "" for one that takes
	// {lang} in its address (or has no language).
	Lang string `json:"lang"`
	// URL is the address; {lang} stands for the language asked for.
	URL string `json:"url"`
	// Text, Source and Author are paths in the JSON answer: names and numbers
	// with dots (`data.0.q`, `hitokoto`). Text is needed.
	Text   string `json:"text"`
	Source string `json:"source"`
	Author string `json:"author"`
}

// Quote is what an interface said.
type Quote struct {
	Text   string `json:"text"`
	Source string `json:"source,omitempty"`
	Author string `json:"author,omitempty"`
}

// Errors.
var (
	// ErrRateLimited: too many requests to the interface just now.
	ErrRateLimited = errors.New("the interface was asked too often")
	// ErrUnavailable: the interface did not give a usable answer.
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

// CheckAPI validates an interface against the hosts allowed. The address must
// be https, with no user name, and its host (with the port when there is
// one) one of hosts. {lang} may be in the path or the query, not in the host.
func CheckAPI(a API, hosts []string, allowHTTP bool) (API, error) {
	a.Name = strings.TrimSpace(a.Name)
	a.Lang = strings.ToLower(strings.TrimSpace(a.Lang))
	if !ValidID(a.ID) {
		return a, errors.New("an interface id is 1 to 32 letters, digits, - or _")
	}
	if a.Name == "" || utf8.RuneCountInString(a.Name) > 100 {
		return a, errors.New("an interface needs a name of at most 100 characters")
	}
	if a.Lang != "" && !validLang(a.Lang) {
		return a, errors.New("lang is a language code like zh or en, or empty")
	}
	if len(a.URL) > 2000 || strings.ContainsAny(a.URL, " \t\r\n") {
		return a, errors.New("that is not an address")
	}
	probe := strings.ReplaceAll(a.URL, "{lang}", "en")
	u, err := url.Parse(probe)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return a, errors.New("that is not an address")
	}
	if u.Scheme != "https" && !(allowHTTP && u.Scheme == "http") {
		return a, errors.New("the address must be https")
	}
	if strings.Contains(strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(a.URL, "https://"), "http://"), "/", 2)[0], "{") {
		return a, errors.New("{lang} may be in the path or the query, not in the host")
	}
	if !hostAllowed(u.Host, hosts) {
		return a, fmt.Errorf("%s is not in the list of hosts that may be fetched from; add it there first", u.Host)
	}
	for _, p := range []*string{&a.Text, &a.Source, &a.Author} {
		*p = strings.TrimSpace(*p)
		if len(*p) > 200 || strings.ContainsAny(*p, "\r\n") {
			return a, errors.New("a JSON path is dots and names, like data.0.q")
		}
	}
	if a.Text == "" {
		return a, errors.New("text is the JSON path of the text; it is needed")
	}
	return a, nil
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

// Fetcher asks the interfaces. It is safe for concurrent use.
type Fetcher struct {
	client    *http.Client
	timeout   time.Duration
	allowHTTP bool
	now       func() time.Time

	mu    sync.Mutex
	cache map[cacheKey]cached
	sent  map[string][]time.Time // by interface: when the last requests were made
	busy  map[cacheKey]*flight
}

type cacheKey struct {
	api, lang, date string
	skip            int
}

type cached struct {
	q   Quote
	err error
	at  time.Time
}

type flight struct {
	done chan struct{}
	q    Quote
	err  error
}

// New makes a Fetcher that refuses any address that is not a public one.
func New() *Fetcher {
	d := &net.Dialer{Timeout: Timeout, Control: refusePrivate}
	return newFetcher(&http.Client{
		Timeout:       Timeout,
		Transport:     &http.Transport{DialContext: d.DialContext, Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: Timeout, MaxResponseHeaderBytes: 16 << 10},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, false)
}

// NewForTests makes a Fetcher that may connect to any address and use plain
// http, so a test can talk to a server of its own. Never use it in the server.
func NewForTests() *Fetcher {
	return newFetcher(&http.Client{
		Timeout:       Timeout,
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, true)
}

func newFetcher(c *http.Client, allowHTTP bool) *Fetcher {
	return &Fetcher{client: c, timeout: Timeout, allowHTTP: allowHTTP, now: time.Now, cache: map[cacheKey]cached{}, sent: map[string][]time.Time{}, busy: map[cacheKey]*flight{}}
}

// AllowHTTP reports whether plain http addresses are accepted (tests only).
func (f *Fetcher) AllowHTTP() bool { return f.allowHTTP }

// SetTimeout shortens the time an answer may take (tests).
func (f *Fetcher) SetTimeout(d time.Duration) { f.timeout = d; f.client.Timeout = d }

// SetClock replaces the clock (tests).
func (f *Fetcher) SetClock(now func() time.Time) { f.now = now }

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
	// Unique local fc00::/7 is IsPrivate; documentation 2001:db8::/32; the
	// NAT64 prefix and 6to4 carry an IPv4 address that is not checked here.
	if b[0] == 0x20 && b[1] == 0x01 && b[2] == 0x0d && b[3] == 0xb8 {
		return false
	}
	if b[0] == 0x00 && b[1] == 0x64 && b[2] == 0xff && b[3] == 0x9b || b[0] == 0x20 && b[1] == 0x02 {
		return false
	}
	return true
}

// Get returns what the interface says for a language on a day. skip 0 is the
// day's text, asked for once; a larger skip is another text, asked for again
// (and kept for that skip), at a limited rate. hosts are the hosts allowed
// now: an interface whose host was taken off the list is not asked.
func (f *Fetcher) Get(ctx context.Context, a API, hosts []string, lang, date string, skip int) (Quote, error) {
	key := cacheKey{a.ID + "\x00" + a.URL + "\x00" + a.Text + a.Source + a.Author, lang, date, skip}
	for {
		f.mu.Lock()
		if c, ok := f.cache[key]; ok {
			if c.err == nil || f.now().Sub(c.at) < failureMemory {
				f.mu.Unlock()
				return c.q, c.err
			}
			delete(f.cache, key)
		}
		if fl, ok := f.busy[key]; ok {
			f.mu.Unlock()
			select {
			case <-fl.done:
				return fl.q, fl.err
			case <-ctx.Done():
				return Quote{}, ctx.Err()
			}
		}
		// The rate is counted for what goes out, not for what is answered from here.
		if !f.allowLocked(a.ID) {
			f.mu.Unlock()
			return Quote{}, ErrRateLimited
		}
		fl := &flight{done: make(chan struct{})}
		f.busy[key] = fl
		f.mu.Unlock()

		q, err := f.fetch(ctx, a, hosts, lang)

		f.mu.Lock()
		delete(f.busy, key)
		if ctx.Err() == nil {
			if len(f.cache) >= cacheMax {
				f.cache = map[cacheKey]cached{}
			}
			f.cache[key] = cached{q, err, f.now()}
		}
		f.mu.Unlock()
		fl.q, fl.err = q, err
		close(fl.done)
		return q, err
	}
}

// Try asks the interface now, bypassing what is kept (an administrator
// testing what they typed), at the same rate limit as any request.
func (f *Fetcher) Try(ctx context.Context, a API, hosts []string, lang string) (Quote, error) {
	f.mu.Lock()
	ok := f.allowLocked(a.ID)
	f.mu.Unlock()
	if !ok {
		return Quote{}, ErrRateLimited
	}
	return f.fetch(ctx, a, hosts, lang)
}

// allowLocked counts a request to the interface against its rate.
func (f *Fetcher) allowLocked(id string) bool {
	now := f.now()
	l := f.sent[id][:0]
	for _, t := range f.sent[id] {
		if now.Sub(t) < time.Minute {
			l = append(l, t)
		}
	}
	if len(l) >= perMinute {
		f.sent[id] = l
		return false
	}
	f.sent[id] = append(l, now)
	return true
}

func (f *Fetcher) fetch(ctx context.Context, a API, hosts []string, lang string) (Quote, error) {
	if lang == "" || !validLang(lang) {
		lang = "en"
	}
	raw := strings.ReplaceAll(a.URL, "{lang}", url.QueryEscape(lang))
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !(f.allowHTTP && u.Scheme == "http")) {
		return Quote{}, fmt.Errorf("%w: bad address", ErrUnavailable)
	}
	if !hostAllowed(u.Host, hosts) {
		return Quote{}, fmt.Errorf("%w: the host is not allowed", ErrUnavailable)
	}
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Quote{}, fmt.Errorf("%w: bad address", ErrUnavailable)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PetalBook")
	resp, err := f.client.Do(req)
	if err != nil {
		return Quote{}, fmt.Errorf("%w: %v", ErrUnavailable, trimErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Quote{}, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); !strings.Contains(ct, "json") {
		return Quote{}, fmt.Errorf("%w: not JSON", ErrUnavailable)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if err != nil {
		return Quote{}, fmt.Errorf("%w: %v", ErrUnavailable, trimErr(err))
	}
	if len(body) > MaxResponse {
		return Quote{}, fmt.Errorf("%w: the answer is larger than %d bytes", ErrUnavailable, MaxResponse)
	}
	return Extract(a, body)
}

func trimErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return err.Error()
}

// Extract reads the text, source and author out of an answer.
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
	return q, nil
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
