package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func app() fs.FS {
	return fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><title>app</title>")},
		"favicon.svg":             {Data: []byte("<svg/>")},
		"manifest.webmanifest":    {Data: []byte(`{"name":"PetalBook"}`)},
		"theme-init.js":           {Data: []byte("var a=1")},
		"icons/icon-192.png":      {Data: []byte("png")},
		"assets/index-abc123.js":  {Data: []byte("console.log(1)")},
		"assets/index-abc123.css": {Data: []byte("a{}")},
		"assets/font-xyz.woff2":   {Data: []byte("font")},
		"assets/dir/.keep":        {Data: []byte("")},
		"secret/../outside":       {Data: []byte("x")}, // not a valid name; must stay unreachable
		"unknown.zzz-not-a-type":  {Data: []byte("bin")},
	}
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestFallbackAndHeaders(t *testing.T) {
	h := New(app())
	for _, p := range []string{"/", "/index.html", "/login", "/s/x/recent", "/s/x/p/abc", "/s/x/f/v1.2/notes.2024", "/settings/", "/t/a.b"} {
		rec := do(h, "GET", p)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>app</title>") {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body.String())
			continue
		}
		hd := rec.Header()
		if hd.Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: Cache-Control %q", p, hd.Get("Cache-Control"))
		}
		if hd.Get("Content-Type") != "text/html; charset=utf-8" {
			t.Errorf("%s: Content-Type %q", p, hd.Get("Content-Type"))
		}
		csp := hd.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'self'") || strings.Contains(csp, "script-src") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s: CSP %q", p, csp)
		}
		if hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Referrer-Policy") != "same-origin" {
			t.Errorf("%s: security headers %v", p, hd)
		}
	}
}

func TestAssetsAreImmutable(t *testing.T) {
	h := New(app())
	cases := map[string]string{
		"/assets/index-abc123.js":  "text/javascript; charset=utf-8",
		"/assets/index-abc123.css": "text/css; charset=utf-8",
		"/assets/font-xyz.woff2":   "font/woff2",
	}
	for p, ct := range cases {
		rec := do(h, "GET", p)
		if rec.Code != 200 {
			t.Fatalf("%s: %d", p, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("%s: Cache-Control %q", p, got)
		}
		if got := rec.Header().Get("Content-Type"); got != ct {
			t.Errorf("%s: Content-Type %q, want %q", p, got, ct)
		}
		if rec.Header().Get("Content-Security-Policy") != "" {
			t.Errorf("%s: CSP is only for the page", p)
		}
	}
}

func TestTopLevelFiles(t *testing.T) {
	h := New(app())
	cases := map[string]string{
		"/favicon.svg":          "image/svg+xml",
		"/manifest.webmanifest": "application/manifest+json",
		"/theme-init.js":        "text/javascript; charset=utf-8",
		"/icons/icon-192.png":   "image/png",
	}
	for p, ct := range cases {
		rec := do(h, "GET", p)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != ct {
			t.Errorf("%s: %d %q, want %q", p, rec.Code, rec.Header().Get("Content-Type"), ct)
		}
		if rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: Cache-Control %q", p, rec.Header().Get("Cache-Control"))
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: nosniff missing", p)
		}
	}
	if rec := do(h, "GET", "/unknown.zzz-not-a-type"); rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Errorf("unknown type: %q", rec.Header().Get("Content-Type"))
	}
}

func TestNotFound(t *testing.T) {
	h := New(app())
	for _, p := range []string{"/nope.png", "/favicon.ico", "/assets/index-old.js", "/assets/missing", "/assets/", "/assets/dir", "/robots.txt", "/a/b/c.map"} {
		rec := do(h, "GET", p)
		if rec.Code != 404 {
			t.Errorf("%s: %d, want 404", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "<title>") {
			t.Errorf("%s: answered with the page", p)
		}
	}
}

func TestAPIIsNeverTheApp(t *testing.T) {
	h := New(app())
	for _, p := range []string{"/api", "/api/", "/api/nope", "/api/spaces/x/pages"} {
		rec := do(h, "GET", p)
		if rec.Code != 404 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || strings.Contains(rec.Body.String(), "<title>") {
			t.Errorf("%s: %d %q %q", p, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
	}
	// A different first segment that merely starts with "api" is a route.
	if rec := do(h, "GET", "/apiary"); rec.Code != 200 {
		t.Errorf("/apiary: %d", rec.Code)
	}
}

func TestTraversal(t *testing.T) {
	h := New(app())
	// URL.Path is the decoded path, so these cover %2e%2e, %2f and %5c too.
	// Dot segments, backslashes and NUL are refused rather than cleaned into
	// another file.
	for _, p := range []string{"/../index.html", "/assets/../index.html", "/x/../favicon.svg", "/a/../../etc/passwd",
		"/..", "/assets/..", "/..\\index.html", "/assets/..\\index.html", "/a\x00b", "/\\favicon.svg"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.URL.Path = p
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Errorf("%q: %d, want 404", p, rec.Code)
		}
	}
	// A path that does not start with a slash (as in a proxy-style request) is refused too.
	req := httptest.NewRequest("GET", "/", nil)
	req.URL.Path = "favicon.svg"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("relative path: %d", rec.Code)
	}
	// Repeated slashes and a trailing slash are cleaned, and stay inside the app.
	if rec := do(h, "GET", "//favicon.svg"); rec.Code != 200 {
		t.Errorf("//favicon.svg: %d", rec.Code)
	}
}

func TestMethodsAndValidators(t *testing.T) {
	h := New(app())
	if rec := do(h, "POST", "/"); rec.Code != 405 || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %d %q", rec.Code, rec.Header().Get("Allow"))
	}
	head := do(h, "HEAD", "/s/x/recent")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("HEAD: %d %d %q", head.Code, head.Body.Len(), head.Header().Get("Cache-Control"))
	}
	first := do(h, "GET", "/manifest.webmanifest")
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag")
	}
	req := httptest.NewRequest("GET", "/manifest.webmanifest", nil)
	req.Header.Set("If-None-Match", tag)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 304 {
		t.Errorf("revalidation: %d, want 304", rec.Code)
	}
}

func TestNotBuilt(t *testing.T) {
	// Only a placeholder: no index.html.
	for name, fsys := range map[string]fs.FS{
		"placeholder": fstest.MapFS{"placeholder.txt": {Data: []byte("x")}},
		"nil":         nil,
	} {
		if Built(fsys) {
			t.Errorf("%s: Built", name)
		}
		h := New(fsys)
		rec := do(h, "GET", "/")
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") ||
			!strings.Contains(rec.Body.String(), "make release") || !strings.Contains(rec.Body.String(), "npm --prefix web run dev") {
			t.Errorf("%s: / gave %d %q", name, rec.Code, rec.Body.String())
		}
		if rec := do(h, "GET", "/s/x/recent"); rec.Code != 404 {
			t.Errorf("%s: deep link %d", name, rec.Code)
		}
		if rec := do(h, "GET", "/placeholder.txt"); rec.Code != 404 {
			t.Errorf("%s: placeholder is served: %d", name, rec.Code)
		}
		if rec := do(h, "GET", "/api/x"); rec.Code != 404 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s: /api/x %d", name, rec.Code)
		}
	}
}

func TestEmbeddedPlaceholder(t *testing.T) {
	// In the repository dist/ holds the placeholder only; after "make release"
	// it holds the app. Either way the handler must be usable.
	h, built := Handler()
	rec := do(h, "GET", "/")
	if built && !strings.Contains(rec.Body.String(), "<div id=\"root\">") {
		t.Errorf("built, but / is %q", rec.Body.String())
	}
	if !built && !strings.Contains(rec.Body.String(), "not built") {
		t.Errorf("not built, but / is %q", rec.Body.String())
	}
}

// The page's policy lets the configured captcha provider's widget load, and only that one.
func TestCSPAllowsTheConfiguredCaptchaOnly(t *testing.T) {
	// The sources each provider needs: [directive, source].
	origins := map[string][][2]string{
		"turnstile": {{"script-src", "https://challenges.cloudflare.com"}, {"frame-src", "https://challenges.cloudflare.com"}},
		"recaptcha": {{"script-src", "https://www.google.com/recaptcha/"}, {"script-src", "https://www.gstatic.com/recaptcha/"},
			{"frame-src", "https://www.google.com/recaptcha/"}, {"frame-src", "https://recaptcha.google.com/recaptcha/"}},
		"hcaptcha": {{"script-src", "https://hcaptcha.com"}, {"script-src", "https://*.hcaptcha.com"}, {"frame-src", "https://*.hcaptcha.com"}, {"connect-src", "https://*.hcaptcha.com"}},
	}
	directive := func(csp, name string) string {
		for _, d := range strings.Split(csp, ";") {
			if f := strings.Fields(d); len(f) > 0 && f[0] == name {
				return strings.Join(f[1:], " ")
			}
		}
		return ""
	}
	for _, provider := range []string{"", "image", "turnstile", "recaptcha", "hcaptcha", "unknown"} {
		calls := 0
		h := New(app(), WithCaptcha(func(*http.Request) []string { calls++; return []string{provider} }))
		csp := do(h, "GET", "/").Header().Get("Content-Security-Policy")
		if calls != 1 {
			t.Errorf("%q: provider asked %d times", provider, calls)
		}
		for p, want := range origins {
			for _, o := range want {
				if p == provider {
					if !slices.Contains(strings.Fields(directive(csp, o[0])), o[1]) {
						t.Errorf("%s: %s missing from %s: %q", p, o[1], o[0], csp)
					}
				} else if strings.Contains(csp, strings.TrimPrefix(o[1], "https://*.")) {
					t.Errorf("provider %q: policy has %s of %s: %q", provider, o[1], p, csp)
				}
			}
		}
		if _, ok := origins[provider]; ok {
			if !strings.HasPrefix(directive(csp, "script-src"), "'self' ") {
				t.Errorf("%s: script-src %q", provider, directive(csp, "script-src"))
			}
		} else if strings.Contains(csp, "script-src") || strings.Contains(csp, "frame-src") || directive(csp, "connect-src") != "'self'" {
			t.Errorf("provider %q: policy %q", provider, csp)
		}
		// Only the page carries the policy, so other files do not ask.
		do(h, "GET", "/favicon.svg")
		if calls != 1 {
			t.Errorf("%q: provider asked for a file", provider)
		}
	}
}

// With several providers in force (the site's and organisations' own) the page lets every one load, and no other.
func TestCSPAllowsEveryProviderInForce(t *testing.T) {
	h := New(app(), WithCaptcha(func(*http.Request) []string { return []string{"hcaptcha", "turnstile", "image", "evil"} }))
	csp := do(h, "GET", "/").Header().Get("Content-Security-Policy")
	for _, want := range []string{"https://challenges.cloudflare.com", "https://*.hcaptcha.com"} {
		if !strings.Contains(csp, want) {
			t.Errorf("%s missing: %q", want, csp)
		}
	}
	if strings.Contains(csp, "recaptcha") || strings.Contains(csp, "evil") {
		t.Errorf("a provider that is not in force: %q", csp)
	}
}

// After a deploy an open page asks for a chunk the new build no longer has. It
// must get a 404 that is never cached (so the page can reload), and the page
// and its deep links must always be revalidated so a reload gets the new build.
func TestRedeployContract(t *testing.T) {
	h := New(app())
	for _, p := range []string{"/", "/index.html", "/settings/profile", "/s/x/p/abc"} {
		rec := do(h, "GET", p)
		if got := rec.Header().Get("Cache-Control"); rec.Code != 200 || got != "no-cache" {
			t.Errorf("%s: %d Cache-Control %q, want 200 no-cache", p, rec.Code, got)
		}
	}
	rec := do(h, "GET", "/assets/SettingsRoute-BBVbfHj5.js")
	if rec.Code != 404 || rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), "<title>") {
		t.Errorf("gone chunk: %d %q %q", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	if got := do(h, "GET", "/assets/index-abc123.js").Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("hashed asset: %q", got)
	}
}
