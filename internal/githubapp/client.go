// Package githubapp is the client of the one GitHub App a server may have: the
// App's JWT, installation access tokens, the user-to-server OAuth flow and the
// REST calls PetalBook makes with them. It is written after Rackbook's package
// of the same name.
//
// Every request goes to the base URLs the Client was made with (api.github.com
// and github.com by default), so tests point it at a fake. A token is held in
// memory only and never appears in an error message, a URL or a log line.
package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Default base URLs of github.com.
const (
	DefaultAPI = "https://api.github.com"
	DefaultWeb = "https://github.com"
)

// PerPage is the page size of every list this package asks GitHub for: one
// request per page, never more.
const PerPage = 30

// Config configures a Client.
type Config struct {
	API  string // REST base URL; DefaultAPI when empty
	Web  string // web base URL (OAuth, and the host of Git); DefaultWeb when empty
	HTTP *http.Client
	Now  func() time.Time
}

// Client talks to one GitHub. It is safe for concurrent use.
type Client struct {
	api, web string
	http     *http.Client
	now      func() time.Time
}

// IsLoopback reports whether host is localhost or a loopback address; plain
// http is accepted only there (tests).
func IsLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func cleanBase(raw, what string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("the GitHub %s URL is unusable", what)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && IsLoopback(u.Hostname())) {
		return "", fmt.Errorf("refusing a GitHub %s URL that is not https (tokens would cross the network in clear)", what)
	}
	return raw, nil
}

// New checks the configuration and returns a Client. It makes no request.
func New(cfg Config) (*Client, error) {
	if cfg.API == "" {
		cfg.API = DefaultAPI
	}
	if cfg.Web == "" {
		cfg.Web = DefaultWeb
	}
	api, err := cleanBase(cfg.API, "API")
	if err != nil {
		return nil, err
	}
	web, err := cleanBase(cfg.Web, "web")
	if err != nil {
		return nil, err
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Client{api: api, web: web, http: cfg.HTTP, now: cfg.Now}, nil
}

// API is the REST base URL.
func (c *Client) API() string { return c.api }

// Web is the web base URL: where people authorise, and where Git repositories are.
func (c *Client) Web() string { return c.web }

// Host is the host (with port) of the web base URL: the only host a token is
// given to Git for.
func (c *Client) Host() string {
	u, _ := url.Parse(c.web)
	return u.Host
}

// RepoURL is the https address of a repository as Git is given it.
func (c *Client) RepoURL(owner, name string) string {
	return c.web + "/" + owner + "/" + name + ".git"
}

// StatusError is a non-2xx answer from GitHub.
type StatusError struct {
	Method, Path string
	Status       int
	Message      string
	Errors       []string // the messages of the "errors" array of a 422
	// RateLimited is set when GitHub is limiting this server; Reset is when it
	// stops (zero when GitHub did not say).
	RateLimited bool
	Reset       time.Time
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("github: %s %s: %d %s", e.Method, e.Path, e.Status, e.Message)
}

// RetryAfter is how long until a rate limit ends, at least one second.
func (e *StatusError) RetryAfter(now time.Time) time.Duration {
	if d := e.Reset.Sub(now); d > time.Second {
		return d
	}
	return time.Second
}

// IsStatus reports whether err is a StatusError with one of the codes.
func IsStatus(err error, codes ...int) bool {
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

// OAuthError is a refusal in the body of an OAuth answer (GitHub answers 200).
type OAuthError struct{ Code, Description string }

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return "github oauth: " + e.Code + ": " + e.Description
	}
	return "github oauth: " + e.Code
}

type request struct {
	method, base, path string
	query              url.Values
	bearer             string // Authorization: Bearer
	basicUser, basicPw string
	body               any        // JSON
	form               url.Values // form-encoded
}

// do makes one request and decodes a 2xx JSON answer into out (nil: ignored).
func (c *Client) do(ctx context.Context, r request, out any) (http.Header, error) {
	target := r.base + r.path
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
	req.Header.Set("Accept", "application/vnd.github+json")
	if r.base == c.web {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "petalbook")
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error // the URL holds no secret, but keep only the cause
		if errors.As(err, &ue) {
			return nil, fmt.Errorf("github: %s %s: %w", r.method, r.path, ue.Err)
		}
		return nil, fmt.Errorf("github: %s %s: %w", r.method, r.path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return resp.Header, c.statusError(r, resp, data)
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.Header, fmt.Errorf("github: %s %s: the answer is not what was expected", r.method, r.path)
		}
	}
	return resp.Header, nil
}

func (c *Client) statusError(r request, resp *http.Response, data []byte) error {
	var e struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(data, &e)
	se := &StatusError{Method: r.method, Path: r.path, Status: resp.StatusCode, Message: e.Message}
	for _, x := range e.Errors {
		if x.Message != "" {
			se.Errors = append(se.Errors, x.Message)
		} else if x.Code != "" {
			se.Errors = append(se.Errors, x.Code)
		}
	}
	h := resp.Header
	limited := resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden && (h.Get("X-RateLimit-Remaining") == "0" || h.Get("Retry-After") != "" ||
			strings.Contains(strings.ToLower(e.Message), "rate limit")))
	if limited {
		se.RateLimited = true
		if s, err := strconv.ParseInt(h.Get("Retry-After"), 10, 64); err == nil && s >= 0 {
			se.Reset = c.now().Add(time.Duration(s) * time.Second)
		} else if s, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil && s > 0 {
			se.Reset = time.Unix(s, 0)
		}
	}
	return se
}

// nextPage is the page after this one according to the Link header, 0 at the end.
func nextPage(h http.Header) int {
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

func pageQuery(page int) url.Values {
	if page < 1 {
		page = 1
	}
	return url.Values{"per_page": {strconv.Itoa(PerPage)}, "page": {strconv.Itoa(page)}}
}
