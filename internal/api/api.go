// Package api is the HTTP API of docs/api.md. The web app talks to the server
// only through it.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/drafts"
	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// SpaceID is the id of the one space of single-user mode.
const SpaceID = spaces.SingleID

// DefaultMaxBody caps a request body. A page is text; 8 MiB is hundreds of
// thousands of words.
const DefaultMaxBody = 8 << 20

// DefaultMaxUpload caps an uploaded file.
const DefaultMaxUpload = 10 << 20

// DefaultMaxImport caps the body of an import.
const DefaultMaxImport = 100 << 20

// authBodyLimit caps the bodies of the sign-in and account endpoints, which
// anyone can reach.
const authBodyLimit = 64 << 10

// Options configures the handler.
type Options struct {
	Version string
	MaxBody int64 // bytes; DefaultMaxBody when 0
	// MaxUpload is the largest file accepted as an upload, in bytes;
	// DefaultMaxUpload when 0.
	MaxUpload int64
	// MaxImport is the largest body of an import, in bytes; DefaultMaxImport when 0.
	MaxImport int64
	// Store, Spaces and Drafts are required.
	Store  *store.Store
	Spaces *spaces.Manager
	Drafts *drafts.Service
	// Auth is set in accounts mode and nil in single-user mode, where every
	// request acts as the built-in user.
	Auth *auth.Service
	// PublicURL is the address people use in accounts mode; its origin is
	// accepted as the server's own on any listen address.
	PublicURL string
	// RestrictHost is loopback-only mode: it refuses requests whose Host header
	// is not a loopback name (or the host of an allowed origin), which stops a
	// web page on another site from reaching the server through DNS rebinding,
	// and it makes loopback origins on the request's port the server's own.
	// With it off there is no implicit own origin: browsers need AllowOrigins
	// (or, in accounts mode, the public URL).
	RestrictHost bool
	// AllowOrigins are origins (scheme://host[:port]) besides the server's own
	// that may make requests, for a development server on another port that
	// proxies to this one. Cross-site checks stay on whatever else is set.
	AllowOrigins []string
	Logger       *slog.Logger
}

type server struct {
	opt     Options
	log     *slog.Logger
	auth    *auth.Service
	spaces  *spaces.Manager
	drafts  *drafts.Service
	single  bool
	started time.Time
	local   store.User // the built-in user of single-user mode
	origins map[string]bool
	// importing lets one import run at a time (see importMarkdown).
	importing chan struct{}
	// originHosts are the hosts of the allowed origins: a request addressed to
	// one of them (through a reverse proxy, say) passes the Host restriction.
	originHosts map[string]bool
}

// New returns the handler for every endpoint.
func New(opt Options) http.Handler {
	if opt.MaxBody <= 0 {
		opt.MaxBody = DefaultMaxBody
	}
	if opt.MaxUpload <= 0 {
		opt.MaxUpload = DefaultMaxUpload
	}
	if opt.MaxImport <= 0 {
		opt.MaxImport = DefaultMaxImport
	}
	if opt.Version == "" {
		opt.Version = "dev"
	}
	s := &server{opt: opt, log: opt.Logger, auth: opt.Auth, spaces: opt.Spaces, drafts: opt.Drafts, single: opt.Auth == nil, started: time.Now(),
		origins: map[string]bool{}, originHosts: map[string]bool{}, importing: make(chan struct{}, 1)}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	allow := append([]string(nil), opt.AllowOrigins...)
	if !s.single && opt.PublicURL != "" {
		allow = append(allow, OriginOf(opt.PublicURL))
	}
	for _, o := range allow {
		s.origins[NormalizeOrigin(o)] = true
		if u, err := url.Parse(NormalizeOrigin(o)); err == nil && u.Host != "" {
			s.originHosts[u.Host] = true
		}
	}
	if s.single {
		s.local = store.User{ID: store.LocalUserID, Username: "local", Name: "Local user", Role: "admin", Created: time.Now().UTC()}
		if u, err := store.GetUser(context.Background(), opt.Store.Q(), store.LocalUserID); err == nil {
			s.local = *u
		}
	}
	mux := http.NewServeMux()
	uploads := map[string]bool{}
	for _, rt := range routeTable {
		mux.HandleFunc(rt.method+" "+rt.pattern, s.handle(rt))
		if rt.upload {
			uploads[rt.method+" "+rt.pattern] = true
		}
	}
	// A path that exists with other methods is 405 in the API's error shape
	// (the mux would answer in plain text), anything else 404. This cannot be
	// method-less patterns in the mux: they conflict with wildcard routes such
	// as /api/invites/{id} next to /api/invites/accept.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if methods := s.methodsFor(r.URL.Path); len(methods) > 0 {
			allow := strings.Join(methods, ", ")
			w.Header().Set("Allow", allow)
			writeError(w, http.StatusMethodNotAllowed, "invalid", "method "+r.Method+" is not allowed here; use "+allow)
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	// The body of a request that changes something must be JSON, or, on an
	// upload route, a file: asked of the mux, so the route table decides.
	bodyType := func(r *http.Request) string {
		if _, pattern := mux.Handler(r); uploads[pattern] {
			return "application/octet-stream"
		}
		return "application/json"
	}
	return s.wrap(mux, bodyType)
}

// methodsFor lists the methods of the routes a path matches, preferring the
// most literal match (so /api/invites/accept is not also /api/invites/{id}).
func (s *server) methodsFor(path string) []string {
	all := strings.Split(path, "/")
	best := -1
	var out []string
	for _, rt := range routeTable {
		segs := all
		if rt.accountsOnly && s.single {
			continue
		}
		ps := strings.Split(rt.pattern, "/")
		if last := ps[len(ps)-1]; strings.HasSuffix(last, "...}") && len(segs) >= len(ps) {
			segs = append(append([]string{}, segs[:len(ps)-1]...), strings.Join(segs[len(ps)-1:], "/"))
		}
		if len(ps) != len(segs) {
			continue
		}
		wild, ok := 0, true
		for i := range ps {
			if strings.HasPrefix(ps[i], "{") {
				if segs[i] == "" {
					ok = false
				}
				wild++
			} else if ps[i] != segs[i] {
				ok = false
			}
		}
		if !ok {
			continue
		}
		if best < 0 || wild < best {
			best, out = wild, nil
		}
		if wild == best {
			out = append(out, rt.method)
		}
	}
	return out
}

// OriginOf returns scheme://host[:port], lower case, of a URL.
func OriginOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// wrap adds the Host check, panic recovery and access logging.
func (s *server) wrap(next http.Handler, bodyType func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic in handler", "path", r.URL.Path, "panic", v)
				if sw.status == 0 {
					writeError(sw, http.StatusInternalServerError, "internal", "internal error")
				}
			}
			s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
		}()
		sw.Header().Set("X-Content-Type-Options", "nosniff")
		if s.opt.RestrictHost && !loopbackHost(r.Host) && !s.originHosts[strings.ToLower(r.Host)] {
			writeError(sw, http.StatusForbidden, "forbidden", "this server only answers requests addressed to localhost")
			return
		}
		// The identity provider's redirect is a cross-site navigation by
		// nature. It changes nothing unless its state matches a sign-in this
		// browser started, so the cross-site rules do not apply to it.
		if !(r.Method == http.MethodGet && r.URL.Path == oidcCallbackPath) {
			if why := s.crossSite(r); why != "" {
				writeError(sw, http.StatusForbidden, "forbidden", why)
				return
			}
		}
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
			// Even without a body: a plain HTML form cannot send this type, so
			// requiring it keeps pages on other sites from making changes.
			// A file upload is the one exception, and its type cannot be sent by a form either.
			want := bodyType(r)
			if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != want {
				msg := "send Content-Type: application/json with every request that changes something"
				if want != "application/json" {
					msg = "send the file with Content-Type: " + want
				}
				writeError(sw, http.StatusUnsupportedMediaType, "invalid", msg)
				return
			}
		}
		next.ServeHTTP(sw, r)
	})
}

const oidcCallbackPath = "/api/auth/oidc/callback"

// NormalizeOrigin lower-cases an origin and drops a trailing slash.
func NormalizeOrigin(o string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(o), "/"))
}

// crossSite returns why a request must be refused as coming from another
// site, or "". Browsers state where a request comes from in Sec-Fetch-Site and
// Origin; a request without either (curl, a script) is not a browser acting for
// a web page and passes.
//
// An Origin is accepted only if it is in the allow list, or if the server is in
// loopback-only mode and the Origin is a plain-http loopback address on the
// port the request was sent to (the Host header, which has already passed the
// loopback check). In particular there is no "same as Host" rule otherwise:
// with the Host restriction off, a page on a rebound DNS name would have
// Origin equal to Host, and a TLS-terminating proxy hides the scheme.
func (s *server) crossSite(r *http.Request) string {
	const msg = "cross-site requests are not allowed"
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return msg
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return ""
	}
	u, err := url.Parse(o)
	if o == "null" || err != nil || u.Scheme == "" || u.Host == "" {
		return msg
	}
	if s.origins[NormalizeOrigin(o)] {
		return ""
	}
	if s.opt.RestrictHost && u.Scheme == "http" && loopbackHost(u.Host) && hostPort(u.Host) == hostPort(r.Host) {
		return ""
	}
	return msg
}

// hostPort returns the port of a host[:port] (80 when absent, the http default).
func hostPort(h string) string {
	if _, port, err := net.SplitHostPort(h); err == nil {
		return port
	}
	return "80"
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// --- JSON -------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		http.Error(w, `{"error":"internal","message":"could not encode the response"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Current any    `json:"current,omitempty"`
}

// draftConflictBody is the 409 of the draft endpoints: the draft there is (or
// null) and the page as the file has it. Unlike errorBody, draft is always present.
type draftConflictBody struct {
	Error   string     `json:"error"`
	Message string     `json:"message"`
	Draft   *draftResp `json:"draft"`
	Current pageResp   `json:"current"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: code, Message: msg})
}

// readJSON decodes the request body into v. An empty body is accepted when
// optional is true. Invalid UTF-8 is refused outright: encoding/json would
// silently replace it, and the API promises 400 for it.
func (s *server) readJSON(w http.ResponseWriter, r *http.Request, v any, optional bool) bool {
	return s.readJSONMax(w, r, v, optional, s.opt.MaxBody)
}

func (s *server) readJSONMax(w http.ResponseWriter, r *http.Request, v any, optional bool, max int64) bool {
	ct := r.Header.Get("Content-Type")
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid", fmt.Sprintf("the request body is larger than %d bytes", max))
		} else {
			writeError(w, http.StatusBadRequest, "invalid", "could not read the request body")
		}
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 && optional {
		return true
	}
	if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "invalid", "send the body as application/json")
		return false
	}
	if !utf8.Valid(raw) {
		writeError(w, http.StatusBadRequest, "invalid", "the request body is not valid UTF-8")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "the request body is not valid JSON for this endpoint: "+err.Error())
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "invalid", "the request body has data after the JSON value")
		return false
	}
	return true
}

// --- errors -----------------------------------------------------------------

func (s *server) fail(w http.ResponseWriter, err error) {
	var (
		inv   *space.InvalidError
		ce    *space.ConflictError
		ae    *auth.Error
		dc    *drafts.Conflict
		ue    *space.UnavailableError
		urle  *gitstore.URLError
		hoste *gitstore.HostError
	)
	switch {
	case errors.As(err, &ae):
		s.failAuth(w, ae)
	case errors.As(err, &dc):
		var draft *draftResp
		if dc.Draft != nil {
			draft = toDraftResp(dc.Draft)
		}
		writeJSON(w, http.StatusConflict, draftConflictBody{Error: "conflict",
			Message: "the draft on the server is not the one you meant; here is what there is", Draft: draft, Current: toPage(dc.Current)})
	case errors.As(err, &ue):
		writeError(w, http.StatusServiceUnavailable, "unavailable", ue.Msg)
	case errors.As(err, &urle):
		writeError(w, http.StatusBadRequest, "invalid", urle.Msg)
	case errors.As(err, &hoste):
		writeError(w, http.StatusForbidden, "forbidden", hoste.Error())
	case errors.Is(err, space.ErrNoRemote):
		writeError(w, http.StatusNotFound, "not_found", "this space has no remote repository")
	case errors.Is(err, space.ErrDiverged):
		writeError(w, http.StatusConflict, "conflict", "both the space and the repository have content of their own, and the server will not merge two histories; make a new space for that repository")
	case errors.Is(err, space.ErrNotTip), errors.Is(err, space.ErrNothingToAccept):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, spaces.ErrConnected):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, spaces.ErrLimit):
		writeError(w, http.StatusForbidden, "forbidden", strings.TrimPrefix(err.Error(), spaces.ErrLimit.Error()+": "))
	case errors.Is(err, spaces.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such space")
	case errors.Is(err, spaces.ErrDisabled):
		writeError(w, http.StatusForbidden, "space_disabled", "this space has been disabled by an administrator")
	case errors.Is(err, spaces.ErrBusy):
		writeError(w, http.StatusConflict, "busy", "the space is in use right now; try again in a moment")
	case errors.Is(err, spaces.ErrPersonalSpace):
		writeError(w, http.StatusConflict, "personal_space", "a personal space belongs to one person and cannot be transferred or deleted")
	case errors.Is(err, spaces.ErrUserDisabled):
		writeError(w, http.StatusConflict, "user_disabled", "that user is disabled and cannot own a space")
	case errors.Is(err, spaces.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "your role in this space does not allow that")
	case errors.Is(err, spaces.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "user_not_found", "there is no such user")
	case errors.Is(err, spaces.ErrNotMember):
		writeError(w, http.StatusNotFound, "not_found", "that user is not a member of this space")
	case errors.Is(err, spaces.ErrAlreadyMember):
		writeError(w, http.StatusConflict, "exists", "that user is a member of this space already")
	case errors.Is(err, spaces.ErrPersonal):
		writeError(w, http.StatusConflict, "personal_space", "a personal space belongs to one person and cannot be shared")
	case errors.Is(err, spaces.ErrLastOwner):
		writeError(w, http.StatusConflict, "last_owner", "a space needs at least one owner; make someone else an owner first")
	case errors.Is(err, spaces.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid", strings.TrimPrefix(err.Error(), spaces.ErrInvalid.Error()+": "))
	case errors.Is(err, space.ErrClosed):
		writeError(w, http.StatusInternalServerError, "internal", "the server is shutting down; try again when it is back")
	case errors.Is(err, space.ErrNoVersion):
		writeError(w, http.StatusNotFound, "not_found", "that commit is not a version of this page")
	case errors.Is(err, space.ErrNoFolder):
		writeError(w, http.StatusNotFound, "not_found", "no such folder")
	case errors.Is(err, space.ErrNotEmpty):
		writeError(w, http.StatusConflict, "conflict", "the folder holds pages; remove them first, or ask for them to go with it")
	case errors.Is(err, space.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such page")
	case errors.Is(err, space.ErrExists), errors.Is(err, fs.ErrExist):
		writeError(w, http.StatusConflict, "exists", "it already exists")
	case errors.As(err, &ce):
		writeJSON(w, http.StatusConflict, errorBody{Error: "conflict",
			Message: "the page changed since you opened it; here is the current version", Current: toPage(ce.Current)})
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, "invalid", inv.Msg)
	case errors.Is(err, index.ErrInvalidQuery):
		writeError(w, http.StatusBadRequest, "invalid", strings.TrimPrefix(err.Error(), index.ErrInvalidQuery.Error()+": "))
	default:
		s.log.Error("request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "something went wrong on the server")
	}
}

func (s *server) failAuth(w http.ResponseWriter, e *auth.Error) {
	switch e.Kind {
	case auth.Invalid:
		writeError(w, http.StatusBadRequest, "invalid", e.Msg)
	case auth.Unauthenticated:
		writeError(w, http.StatusUnauthorized, "unauthenticated", e.Msg)
	case auth.Forbidden:
		writeError(w, http.StatusForbidden, "forbidden", e.Msg)
	case auth.NotFound:
		writeError(w, http.StatusNotFound, "not_found", e.Msg)
	case auth.Conflict:
		writeError(w, http.StatusConflict, "conflict", e.Msg)
	case auth.Exists:
		writeError(w, http.StatusConflict, "exists", e.Msg)
	case auth.RateLimited:
		secs := int((e.RetryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
		writeError(w, http.StatusTooManyRequests, "rate_limited", e.Msg)
	default:
		writeError(w, http.StatusInternalServerError, "internal", e.Msg)
	}
}

// --- resources --------------------------------------------------------------

func rfc3339(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

type pageResp struct {
	ID      string    `json:"id"`
	Path    string    `json:"path"`
	Folder  string    `json:"folder"`
	Rev     string    `json:"rev"`
	Updated string    `json:"updated"`
	Meta    page.Meta `json:"meta"`
	Header  string    `json:"header"`
	Body    string    `json:"body"`
}

func toPage(p *space.Page) pageResp {
	m := p.Meta
	if m == nil {
		m = page.Meta{}
	}
	return pageResp{ID: p.ID, Path: p.Path, Folder: p.Folder, Rev: p.Rev, Updated: rfc3339(p.Updated), Meta: m, Header: p.Header, Body: p.Body}
}

type summaryResp struct {
	ID       string   `json:"id"`
	Path     string   `json:"path"`
	Folder   string   `json:"folder"`
	Title    string   `json:"title,omitempty"`
	Subtitle string   `json:"subtitle,omitempty"`
	Source   string   `json:"source,omitempty"`
	Label    string   `json:"label"`
	Untitled bool     `json:"untitled"`
	Tags     []string `json:"tags"`
	Type     string   `json:"type"`
	Created  string   `json:"created,omitempty"`
	Updated  string   `json:"updated"`
	Excerpt  string   `json:"excerpt"`
}

func toSummary(s index.Summary) summaryResp {
	return summaryResp{ID: s.ID, Path: s.Path, Folder: s.Folder, Title: s.Title, Subtitle: s.Subtitle, Source: s.Source, Label: s.Label,
		Untitled: s.Untitled, Tags: s.Tags, Type: s.Type, Created: s.Created, Updated: rfc3339(s.Updated), Excerpt: s.Excerpt}
}
