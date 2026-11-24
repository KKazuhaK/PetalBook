package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/KKazuhaK/PetalBook/internal/dailyfetch"
)

// client talks to one host's REST API and OAuth endpoints. Every connection it
// makes goes through the public-address check (unless the server was started
// with the unsafe switch, for tests and a lab), nothing is followed across a
// redirect, and a token appears in no error, log line or URL.
type client struct {
	base string // scheme://host[:port]
	hc   *http.Client
	now  func() time.Time
	log  *slog.Logger
}

// newHTTPClient is the client every provider talks through. unsafe lets it
// reach loopback and private addresses over plain http.
func newHTTPClient(unsafe bool) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if !unsafe {
		d.Control = publicOnly
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy:                  nil, // never through a proxy of the environment: it would answer for the check
			DialContext:            d.DialContext,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  25 * time.Second,
			MaxResponseHeaderBytes: dailyfetch.MaxResponseHeaderBytes,
			MaxIdleConnsPerHost:    4,
			IdleConnTimeout:        60 * time.Second,
		},
		// A redirect is an answer, not something to follow: a token must not
		// travel to a place the administrator did not set up.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// publicOnly is the Control of the dialer: it refuses every address that is not
// one of the open internet, after the name was resolved.
var publicOnly = dailyfetch.RefuseAddrs(func(ip netip.Addr) bool { return !dailyfetch.Public(ip) })

// StatusError is a non-2xx answer from a host.
type StatusError struct {
	Method, Kind string
	Status       int
	Message      string
	RateLimited  bool
	Reset        time.Time
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: %s: %d %s", e.Method, e.Kind, e.Status, quote(e.Message))
}

// RetryAfter is how long until a rate limit ends, at least one second.
func (e *StatusError) RetryAfter(now time.Time) time.Duration {
	if d := e.Reset.Sub(now); d > time.Second {
		return d
	}
	return time.Second
}

func isStatus(err error, codes ...int) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	for _, c := range codes {
		if se.Status == c {
			return true
		}
	}
	return false
}

// OAuthError is a refusal in the body of an OAuth answer.
type OAuthError struct{ Code, Description string }

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return "oauth: " + quote(e.Code) + ": " + quote(e.Description)
	}
	return "oauth: " + quote(e.Code)
}

// isBadGrant reports whether err says a refresh token or a code is no good.
func isBadGrant(err error) bool {
	var oe *OAuthError
	return errors.As(err, &oe) && (oe.Code == "invalid_grant" || oe.Code == "bad_refresh_token")
}

// isBadClient reports whether err says the host does not accept the client.
func isBadClient(err error) bool {
	var oe *OAuthError
	return errors.As(err, &oe) && (oe.Code == "invalid_client" || oe.Code == "unauthorized_client")
}

type request struct {
	method, path string
	query        url.Values
	bearer       string
	basicUser    string
	basicPw      string
	form         url.Values
	body         any
	header       map[string]string
	// kind names what the call is for in errors and logs, without any of the
	// names or numbers in its path.
	kind string
}

// do makes one request; a 2xx JSON answer is decoded into out (nil: ignored).
func (c *client) do(ctx context.Context, r request, out any) (http.Header, error) {
	target := c.base + r.path
	if len(r.query) > 0 {
		target += "?" + r.query.Encode()
	}
	var rd io.Reader
	ctype := ""
	switch {
	case r.form != nil:
		rd, ctype = strings.NewReader(r.form.Encode()), "application/x-www-form-urlencoded"
	case r.body != nil:
		b, _ := json.Marshal(r.body)
		rd, ctype = bytes.NewReader(b), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, r.method, target, rd)
	if err != nil {
		return nil, err
	}
	switch {
	case r.bearer != "":
		req.Header.Set("Authorization", "Bearer "+r.bearer)
	case r.basicUser != "":
		req.SetBasicAuth(r.basicUser, r.basicPw)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "petalbook")
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		var ue *url.Error // keep only the cause: the URL may hold a name that is nobody's business
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("%s: %s: %w", r.method, r.kind, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return resp.Header, c.statusError(r, resp, data)
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.Header, fmt.Errorf("%s: %s: the answer is not what was expected", r.method, r.kind)
		}
	}
	return resp.Header, nil
}

func (c *client) statusError(r request, resp *http.Response, data []byte) error {
	var e struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
		Desc    string `json:"error_description"`
	}
	_ = json.Unmarshal(data, &e)
	msg := messageOf(e.Message)
	if msg == "" {
		msg = e.Error
	}
	// An OAuth refusal is an answer of its own, whatever its status.
	if r.kind == "oauth_token" && e.Error != "" {
		c.logRefusal(r, resp.StatusCode, e.Error)
		return &OAuthError{Code: e.Error, Description: e.Desc}
	}
	se := &StatusError{Method: r.method, Kind: r.kind, Status: resp.StatusCode, Message: msg}
	h := resp.Header
	if resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode == http.StatusForbidden && h.Get("Retry-After") != "") {
		se.RateLimited = true
		if s, err := strconv.ParseInt(h.Get("Retry-After"), 10, 64); err == nil && s >= 0 {
			se.Reset = c.now().Add(time.Duration(s) * time.Second)
		} else if s, err := strconv.ParseInt(h.Get("RateLimit-Reset"), 10, 64); err == nil && s > 0 {
			se.Reset = time.Unix(s, 0)
		}
	}
	c.logRefusal(r, resp.StatusCode, msg)
	return se
}

// messageOf reads the "message" of an error answer: a string (Gitea, GitLab's
// plain errors) or GitLab's field-by-field object.
func messageOf(v any) string {
	switch m := v.(type) {
	case string:
		return m
	case map[string]any:
		var parts []string
		for k, vv := range m {
			if l, ok := vv.([]any); ok {
				for _, x := range l {
					if s, ok := x.(string); ok {
						parts = append(parts, k+" "+s)
					}
				}
			} else if s, ok := vv.(string); ok {
				parts = append(parts, k+" "+s)
			}
		}
		sortStrings(parts)
		return strings.Join(parts, "; ")
	}
	return ""
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// tokenLike is what looks like a secret in text a host sent: the prefixes of
// the tokens the hosts make, a private key, and anything shaped like a JWT (three
// base64url parts with dots between them: Gitea's refresh tokens are one).
var tokenLike = regexp.MustCompile(`(?i)\b(?:glpat-|gloas-|gho_|ghp_)[A-Za-z0-9_-]+|-----BEGIN[^-]*-----|[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)

// quote makes something a host said fit on one log line or in an error: no
// line breaks or other control characters (a host cannot forge a line), at
// most 300 bytes, and anything that looks like a token blanked out.
func quote(s string) string {
	s = tokenLike.ReplaceAllString(s, "[redacted]")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
	if len(s) > 300 {
		s = strings.ToValidUTF8(s[:300], "") + "..."
	}
	return s
}

func (c *client) logRefusal(r request, status int, msg string) {
	if c.log == nil {
		return
	}
	level := slog.LevelWarn
	if status == http.StatusNotFound || status == http.StatusTooManyRequests {
		level = slog.LevelInfo
	}
	c.log.Log(context.Background(), level, "a provider's host refused a request", "kind", r.kind, "method", r.method, "status", status, "message", quote(msg))
}

// nextPage is the page after this one according to the headers, 0 at the end:
// X-Next-Page (GitLab) or the Link header's rel="next" (Gitea, GitLab).
func nextPage(h http.Header) int {
	if v := strings.TrimSpace(h.Get("X-Next-Page")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		return 0
	}
	for _, part := range strings.Split(h.Get("Link"), ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		s := strings.TrimPrefix(part, "<")
		end := strings.Index(s, ">")
		if end < 0 {
			continue
		}
		u, err := url.Parse(s[:end])
		if err != nil {
			continue
		}
		if p, err := strconv.Atoi(u.Query().Get("page")); err == nil && p > 0 {
			return p
		}
	}
	return 0
}

// perPage is the page size of every list: one request per page, never more.
const perPage = 30

func pageQuery(page int) url.Values {
	if page < 1 {
		page = 1
	}
	return url.Values{"per_page": {strconv.Itoa(perPage)}, "limit": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
}
