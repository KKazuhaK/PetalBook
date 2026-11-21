// Package api is the HTTP API of docs/api.md. The web app talks to the server
// only through it.
package api

import (
	"bytes"
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

	"github.com/KazuhaHub/PetalBook/internal/index"
	"github.com/KazuhaHub/PetalBook/internal/page"
	"github.com/KazuhaHub/PetalBook/internal/space"
)

// SpaceID is the id of the one space milestone 1.1 serves.
const SpaceID = "default"

// DefaultMaxBody caps a request body. A page is text; 8 MiB is hundreds of
// thousands of words.
const DefaultMaxBody = 8 << 20

// Options configures the handler.
type Options struct {
	Space   *space.Space
	Version string
	MaxBody int64 // bytes; DefaultMaxBody when 0
	// RestrictHost is loopback-only mode: it refuses requests whose Host header
	// is not a loopback name (or the host of an allowed origin), which stops a
	// web page on another site from reaching the server through DNS rebinding,
	// and it makes loopback origins on the request's port the server's own.
	// With it off there is no implicit own origin: browsers need AllowOrigins.
	RestrictHost bool
	// AllowOrigins are origins (scheme://host[:port]) besides the server's own
	// that may make requests, for a development server on another port that
	// proxies to this one. Cross-site checks stay on whatever else is set.
	AllowOrigins []string
	Logger       *slog.Logger
}

type server struct {
	opt     Options
	sp      *space.Space
	log     *slog.Logger
	origins map[string]bool
	// originHosts are the hosts of the allowed origins: a request addressed to
	// one of them (through a reverse proxy, say) passes the Host restriction.
	originHosts map[string]bool
}

// New returns the handler for every endpoint.
func New(opt Options) http.Handler {
	if opt.MaxBody <= 0 {
		opt.MaxBody = DefaultMaxBody
	}
	if opt.Version == "" {
		opt.Version = "dev"
	}
	s := &server{opt: opt, sp: opt.Space, log: opt.Logger, origins: map[string]bool{}, originHosts: map[string]bool{}}
	for _, o := range opt.AllowOrigins {
		s.origins[NormalizeOrigin(o)] = true
		if u, err := url.Parse(NormalizeOrigin(o)); err == nil && u.Host != "" {
			s.originHosts[u.Host] = true
		}
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	mux := http.NewServeMux()
	routes := []struct {
		method, pattern string
		h               http.HandlerFunc
	}{
		{"GET", "/api/meta", s.getMeta},
		{"GET", "/api/spaces/{space}/pages", s.listPages},
		{"POST", "/api/spaces/{space}/pages", s.createPage},
		{"GET", "/api/spaces/{space}/pages/{id}", s.getPage},
		{"PUT", "/api/spaces/{space}/pages/{id}", s.savePage},
		{"DELETE", "/api/spaces/{space}/pages/{id}", s.deletePage},
		{"POST", "/api/spaces/{space}/pages/{id}/move", s.movePage},
		{"GET", "/api/spaces/{space}/tree", s.getTree},
		{"POST", "/api/spaces/{space}/folders", s.createFolder},
		{"GET", "/api/spaces/{space}/tags", s.getTags},
		{"POST", "/api/spaces/{space}/reindex", s.reindex},
	}
	allowed := map[string][]string{}
	for _, r := range routes {
		mux.HandleFunc(r.method+" "+r.pattern, r.h)
		allowed[r.pattern] = append(allowed[r.pattern], r.method)
	}
	// The mux answers a wrong method with plain text; a method-less pattern
	// per path lets us answer in the API's error shape.
	for pattern, methods := range allowed {
		allow := strings.Join(methods, ", ")
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", allow)
			writeError(w, http.StatusMethodNotAllowed, "invalid", "method "+r.Method+" is not allowed here; use "+allow)
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	return s.wrap(mux)
}

// wrap adds the Host check, panic recovery and access logging.
func (s *server) wrap(next http.Handler) http.Handler {
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
		if s.opt.RestrictHost && !loopbackHost(r.Host) && !s.originHosts[strings.ToLower(r.Host)] {
			writeError(sw, http.StatusForbidden, "forbidden", "this server only answers requests addressed to localhost")
			return
		}
		if why := s.crossSite(r); why != "" {
			writeError(sw, http.StatusForbidden, "forbidden", why)
			return
		}
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
			// Even without a body: a plain HTML form cannot send this type, so
			// requiring it keeps pages on other sites from making changes.
			if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
				writeError(sw, http.StatusUnsupportedMediaType, "invalid", "send Content-Type: application/json with every request that changes something")
				return
			}
		}
		next.ServeHTTP(sw, r)
	})
}

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

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: code, Message: msg})
}

// readJSON decodes the request body into v. An empty body is accepted when
// optional is true. Invalid UTF-8 is refused outright: encoding/json would
// silently replace it, and the API promises 400 for it.
func (s *server) readJSON(w http.ResponseWriter, r *http.Request, v any, optional bool) bool {
	ct := r.Header.Get("Content-Type")
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.opt.MaxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid", fmt.Sprintf("the request body is larger than %d bytes", s.opt.MaxBody))
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
		inv *space.InvalidError
		ce  *space.ConflictError
	)
	switch {
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

// checkSpace answers 404 for any space but the one this server has.
func (s *server) checkSpace(w http.ResponseWriter, r *http.Request) bool {
	if r.PathValue("space") != SpaceID {
		writeError(w, http.StatusNotFound, "not_found", "no such space")
		return false
	}
	return true
}

// pageID returns the {id} of the request, answering 404 for anything that
// cannot be a page id.
func (s *server) pageID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.checkSpace(w, r) {
		return "", false
	}
	id := r.PathValue("id")
	if !page.ValidID(id) {
		writeError(w, http.StatusNotFound, "not_found", "no such page")
		return "", false
	}
	return id, true
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
	Body    string    `json:"body"`
}

func toPage(p *space.Page) pageResp {
	m := p.Meta
	if m == nil {
		m = page.Meta{}
	}
	return pageResp{ID: p.ID, Path: p.Path, Folder: p.Folder, Rev: p.Rev, Updated: rfc3339(p.Updated), Meta: m, Body: p.Body}
}

type summaryResp struct {
	ID       string   `json:"id"`
	Path     string   `json:"path"`
	Folder   string   `json:"folder"`
	Title    string   `json:"title,omitempty"`
	Subtitle string   `json:"subtitle,omitempty"`
	Label    string   `json:"label"`
	Untitled bool     `json:"untitled"`
	Tags     []string `json:"tags"`
	Type     string   `json:"type"`
	Created  string   `json:"created,omitempty"`
	Updated  string   `json:"updated"`
	Excerpt  string   `json:"excerpt"`
}

func toSummary(s index.Summary) summaryResp {
	return summaryResp{ID: s.ID, Path: s.Path, Folder: s.Folder, Title: s.Title, Subtitle: s.Subtitle, Label: s.Label,
		Untitled: s.Untitled, Tags: s.Tags, Type: s.Type, Created: s.Created, Updated: rfc3339(s.Updated), Excerpt: s.Excerpt}
}

// --- handlers ---------------------------------------------------------------

func (s *server) getMeta(w http.ResponseWriter, r *http.Request) {
	type sp struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	writeJSON(w, http.StatusOK, struct {
		Version string `json:"version"`
		Spaces  []sp   `json:"spaces"`
	}{s.opt.Version, []sp{{SpaceID, s.sp.Name()}}})
}

func (s *server) listPages(w http.ResponseWriter, r *http.Request) {
	if !s.checkSpace(w, r) {
		return
	}
	v := r.URL.Query()
	q := index.Query{Tags: v["tag"], Text: v.Get("q"), Sort: v.Get("sort"), Cursor: v.Get("cursor")}
	if v.Has("folder") {
		f := v.Get("folder")
		if err := space.ValidFolder(f); err != nil {
			s.fail(w, err)
			return
		}
		q.Folder = &f
	}
	switch rec := v.Get("recursive"); rec {
	case "", "false":
	case "true":
		q.Recursive = true
	default:
		writeError(w, http.StatusBadRequest, "invalid", "recursive must be true or false")
		return
	}
	if l := v.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid", "limit must be a number from 1 to 200")
			return
		}
		q.Limit = n
		if n == 0 { // 0 means "default" inside the index; over HTTP it is out of range
			q.Limit = -1
		}
	}
	res, err := s.sp.List(q)
	if err != nil {
		s.fail(w, err)
		return
	}
	items := make([]summaryResp, len(res.Items))
	for i, it := range res.Items {
		items[i] = toSummary(it)
	}
	writeJSON(w, http.StatusOK, struct {
		Items []summaryResp `json:"items"`
		Next  string        `json:"next"`
	}{items, res.Next})
}

func (s *server) getPage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pageID(w, r)
	if !ok {
		return
	}
	p, err := s.sp.Get(id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPage(p))
}

func (s *server) createPage(w http.ResponseWriter, r *http.Request) {
	if !s.checkSpace(w, r) {
		return
	}
	var req struct {
		Folder string    `json:"folder"`
		Meta   page.Meta `json:"meta"`
		Body   string    `json:"body"`
	}
	if !s.readJSON(w, r, &req, true) {
		return
	}
	p, err := s.sp.Create(space.CreateInput{Folder: req.Folder, Meta: req.Meta, Body: req.Body})
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Location", "/api/spaces/"+SpaceID+"/pages/"+url.PathEscape(p.ID))
	writeJSON(w, http.StatusCreated, toPage(p))
}

func (s *server) savePage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pageID(w, r)
	if !ok {
		return
	}
	var req struct {
		BaseRev string     `json:"baseRev"`
		Meta    *page.Meta `json:"meta"`
		Body    *string    `json:"body"`
	}
	if !s.readJSON(w, r, &req, false) {
		return
	}
	// A save replaces the whole page, so a request that leaves out the header
	// or the body would silently empty it. Refuse instead of guessing.
	if req.Meta == nil || req.Body == nil {
		writeError(w, http.StatusBadRequest, "invalid", "a save needs both meta and body; send the page's current values for the part that did not change")
		return
	}
	// A save without the rev it started from is a malformed request, not a
	// conflict: there is no other version to show the person.
	if req.BaseRev == "" {
		writeError(w, http.StatusBadRequest, "invalid", "a save needs baseRev, the rev of the page it started from")
		return
	}
	p, err := s.sp.Save(id, req.BaseRev, *req.Meta, *req.Body)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPage(p))
}

func (s *server) movePage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pageID(w, r)
	if !ok {
		return
	}
	var req struct {
		Folder string `json:"folder"`
	}
	if !s.readJSON(w, r, &req, false) {
		return
	}
	p, err := s.sp.Move(id, req.Folder)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPage(p))
}

func (s *server) deletePage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pageID(w, r)
	if !ok {
		return
	}
	if err := s.sp.Delete(id); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) getTree(w http.ResponseWriter, r *http.Request) {
	if !s.checkSpace(w, r) {
		return
	}
	folders, err := s.sp.Tree()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Folders []index.FolderCount `json:"folders"`
	}{folders})
}

func (s *server) createFolder(w http.ResponseWriter, r *http.Request) {
	if !s.checkSpace(w, r) {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if !s.readJSON(w, r, &req, false) {
		return
	}
	path, err := s.sp.CreateFolder(req.Path)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Path string `json:"path"`
	}{path})
}

func (s *server) getTags(w http.ResponseWriter, r *http.Request) {
	if !s.checkSpace(w, r) {
		return
	}
	tags, err := s.sp.Tags()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Tags []index.TagCount `json:"tags"`
	}{tags})
}

func (s *server) reindex(w http.ResponseWriter, r *http.Request) {
	if !s.checkSpace(w, r) {
		return
	}
	st, err := s.sp.Reindex()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Pages int   `json:"pages"`
		Ms    int64 `json:"ms"`
	}{st.Pages, st.Elapsed.Milliseconds()})
}
