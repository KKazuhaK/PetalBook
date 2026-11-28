// Package webui serves the built web app from files embedded in the binary.
//
// The files are copied into dist/ by "make release"; in the repository the
// directory holds only a placeholder, so the package (and the server) builds
// on a fresh clone and then serves the API alone.
package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
)

// all: keeps names that start with a dot or an underscore.
//
//go:embed all:dist
var embedded embed.FS

const indexName = "index.html"

// Handler serves the embedded web app. built is false when only the
// placeholder is embedded; the handler then explains how to get the app.
func Handler(opts ...Option) (h http.Handler, built bool) {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil { // cannot happen: dist is part of the embed pattern
		return New(nil, opts...), false
	}
	return New(sub, opts...), Built(sub)
}

// Option changes how New serves the app.
type Option func(*handler)

// WithCaptcha names the third-party captcha providers in force for a request
// (the site's, and every organisation's own), so the page's policy lets their
// widgets load. It is called once per page served and must be cheap (memory
// only): the policies themselves are built once per set of providers.
func WithCaptcha(provider func(*http.Request) []string) Option {
	return func(h *handler) { h.captcha = provider }
}

// Built reports whether fsys holds a built web app (an index.html).
func Built(fsys fs.FS) bool {
	if fsys == nil {
		return false
	}
	fi, err := fs.Stat(fsys, indexName)
	return err == nil && fi.Mode().IsRegular()
}

// indexCSP is the policy of the app's page. script-src falls back to
// default-src 'self': the page has no inline script (the theme is applied by
// /theme-init.js), so 'unsafe-inline' is not needed for scripts.
const indexCSP = "default-src 'self'; img-src 'self' data: blob: https: http:; style-src 'self' 'unsafe-inline'; " +
	"font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// captchaParts are the sources each third-party captcha provider needs, from
// each provider's own CSP documentation: script, frame and (where it calls out
// from the page) connect and style. They are a fixed table: nothing a tenant
// supplies reaches the policy, only which of these providers is in force.
// web/src/components/auth/Captcha.tsx loads the scripts named here.
var captchaParts = map[string]struct{ script, frame, connect, style string }{
	// Cloudflare: script and frame from challenges.cloudflare.com.
	"turnstile": {"https://challenges.cloudflare.com", "https://challenges.cloudflare.com", "", ""},
	// Google's reCAPTCHA FAQ: scripts under www.google.com/recaptcha/ and
	// www.gstatic.com/recaptcha/, frames from www.google.com/recaptcha/ and
	// recaptcha.google.com/recaptcha/.
	"recaptcha": {"https://www.google.com/recaptcha/ https://www.gstatic.com/recaptcha/",
		"https://www.google.com/recaptcha/ https://recaptcha.google.com/recaptcha/", "", ""},
	// hCaptcha: script, frame, style and connect from hcaptcha.com and its subdomains.
	"hcaptcha": {"https://hcaptcha.com https://*.hcaptcha.com", "https://hcaptcha.com https://*.hcaptcha.com",
		"https://hcaptcha.com https://*.hcaptcha.com", "https://hcaptcha.com https://*.hcaptcha.com"},
}

// captchaOrder fixes the order of the sources in a policy.
var captchaOrder = []string{"turnstile", "recaptcha", "hcaptcha"}

// captchaPolicies keeps the policies built for each set of providers.
var captchaPolicies sync.Map // "turnstile,hcaptcha" -> policy

// captchaCSP is the page's policy while third-party captcha providers are in
// force: indexCSP plus those providers' sources (unknown names are ignored).
func captchaCSP(providers []string) string {
	var in []string
	for _, p := range captchaOrder {
		if slices.Contains(providers, p) {
			in = append(in, p)
		}
	}
	if len(in) == 0 {
		return indexCSP
	}
	key := strings.Join(in, ",")
	if v, ok := captchaPolicies.Load(key); ok {
		return v.(string)
	}
	var script, frame, connect, style string
	for _, p := range in {
		c := captchaParts[p]
		script += " " + c.script
		frame += " " + c.frame
		if c.connect != "" {
			connect += " " + c.connect
		}
		if c.style != "" {
			style += " " + c.style
		}
	}
	pol := "default-src 'self'; script-src 'self'" + script + "; frame-src" + frame +
		"; img-src 'self' data: blob: https: http:; style-src 'self' 'unsafe-inline'" + style +
		"; font-src 'self' data:; connect-src 'self'" + connect + "; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
	captchaPolicies.Store(key, pol)
	return pol
}

const notBuiltText = `PetalBook is running, but the web app is not built into this binary.

The API is available under /api/. To get the web app either:
  - build the single binary with "make release" (needs Node.js), or
  - run the Vite dev server: npm --prefix web run dev  (then open http://localhost:5173)
`

// clientRoutes are the first path segments the app's router owns. Names below
// them (folders, tags, pages, spaces) may contain dots, so a missing "file"
// there is a route, not a missing asset.
var clientRoutes = map[string]bool{"s": true, "f": true, "t": true, "p": true}

type handler struct {
	fsys fs.FS // nil or without index.html: not built

	captcha func(*http.Request) []string

	mu    sync.Mutex
	etags map[string]string
}

// New returns a handler that serves fsys as a single-page app: files as
// themselves, other extension-less paths as index.html. A nil fsys, or one
// without index.html, gives the "not built" handler.
func New(fsys fs.FS, opts ...Option) http.Handler {
	if !Built(fsys) {
		fsys = nil
	}
	h := &handler{fsys: fsys, etags: map[string]string{}}
	for _, o := range opts {
		o(h)
	}
	return h
}

// csp is the page's policy for this request.
func (h *handler) csp(r *http.Request) string {
	if h.captcha != nil {
		return captchaCSP(h.captcha(r))
	}
	return indexCSP
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hd := w.Header()
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Referrer-Policy", "same-origin")

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		hd.Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := r.URL.Path
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		notFound(w)
		return
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			notFound(w)
			return
		}
	}
	// The API owns /api. It is routed before this handler, but if one is
	// ever mounted without that, the app's page must not answer for it.
	if p == "/api" || strings.HasPrefix(p, "/api/") {
		hd.Set("Content-Type", "application/json")
		hd.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotFound)
		if r.Method != http.MethodHead {
			io.WriteString(w, `{"error":"not_found","message":"No such endpoint."}`+"\n")
		}
		return
	}

	if h.fsys == nil {
		hd.Set("Cache-Control", "no-store")
		hd.Set("Content-Type", "text/plain; charset=utf-8")
		if p != "/" {
			w.WriteHeader(http.StatusNotFound)
		}
		if r.Method != http.MethodHead {
			io.WriteString(w, notBuiltText)
		}
		return
	}

	name := strings.TrimPrefix(path.Clean(p), "/")
	if name == "" {
		name = indexName
	}
	first, _, _ := strings.Cut(name, "/")
	if fi, err := fs.Stat(h.fsys, name); err == nil && fi.Mode().IsRegular() {
		h.serveFile(w, r, name, first == "assets")
		return
	}
	switch {
	case first == "assets":
		// Hashed build output only; never answer an old, missing chunk with
		// HTML, which the browser would try to run as a script.
		notFound(w)
	case path.Ext(name) != "" && !clientRoutes[first]:
		notFound(w)
	default:
		h.serveFile(w, r, indexName, false)
	}
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "not found", http.StatusNotFound)
}

// serveFile writes name from the file system. Files under /assets carry a
// content hash in their name, so they never change; everything else is
// revalidated with an ETag on each use.
func (h *handler) serveFile(w http.ResponseWriter, r *http.Request, name string, hashed bool) {
	f, err := h.fsys.Open(name)
	if err != nil {
		notFound(w)
		return
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok { // not every fs.File can seek, and ServeContent needs it
		b, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		rs = bytes.NewReader(b)
	}
	hd := w.Header()
	hd.Set("Content-Type", contentType(name))
	switch {
	case hashed:
		hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		hd.Set("Cache-Control", "no-cache")
	}
	if name == indexName {
		hd.Set("Content-Security-Policy", h.csp(r))
	}
	if tag, err := h.etag(name, rs); err == nil {
		hd.Set("ETag", tag)
	}
	http.ServeContent(w, r, name, time.Time{}, rs)
}

// etag is a strong validator from the content, computed once per file.
func (h *handler) etag(name string, rs io.ReadSeeker) (string, error) {
	h.mu.Lock()
	tag, ok := h.etags[name]
	h.mu.Unlock()
	if ok {
		return tag, nil
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, rs); err != nil {
		return "", err
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	tag = `"` + hex.EncodeToString(sum.Sum(nil)[:12]) + `"`
	h.mu.Lock()
	h.etags[name] = tag
	h.mu.Unlock()
	return tag, nil
}

// types are fixed rather than taken from the system's MIME database, which
// differs between machines and may not know .webmanifest.
var types = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".webmanifest": "application/manifest+json",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".gif":         "image/gif",
	".webp":        "image/webp",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".ttf":         "font/ttf",
	".txt":         "text/plain; charset=utf-8",
	".wasm":        "application/wasm",
}

func contentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if t, ok := types[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}
