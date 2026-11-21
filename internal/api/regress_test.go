package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KazuhaHub/PetalBook/internal/api"
)

// The server must accept its own GET output: the web app sends meta and body
// straight back, and the file must not change when nothing was edited.
func TestPutAcceptsWhatGetServedAndLeavesTheFileUntouched(t *testing.T) {
	f := newFixture(t)
	headers := map[string]string{
		"string tags":      "tags: 栀子\ntitle: T",
		"null in list":     "tags: [a, ~, b]\ntitle: T",
		"null tags":        "tags:\ntitle: T",
		"numeric tags":     "tags: [2024, 1.5, true]\ntitle: T",
		"datetime created": "created: 2025-12-03T10:00:00\ntitle: T",
		"quoted created":   "created: \"2025-12-03\"\ntitle: T",
		"bad created":      "created: someday\ntitle: T",
		"numeric title":    "title: 1984",
		"boolean title":    "title: true",
		"empty title":      "title: \"\"\nsubtitle:",
		"padded values":    "title: '  padded  '\nsubtitle: \"  x \"\ntags: [' a ', b]",
		"duplicate keys":   "title: first\ntitle: second\nseries: a\nseries: b",
		"comments":         "# top comment\ntitle: T   # trailing\n\n# before series\nseries: s",
		"anchors":          "base: &b {k: v, n: [1, 2]}\ncopy: *b\nlist: &l [x, y]\nagain: *l\ntitle: T",
		"merge key":        "base: &b {k: v}\nother:\n  <<: *b\n  z: 1\ntitle: T",
		"nested maps":      "title: T\nextra:\n  2024: a\n  b:\n    c: [1, {d: e}]\n    10: x\n    2: y\n  a: 1",
		"integer key":      "2024: 年度\n10: ten\n2: two\ntitle: T",
		"numbers":          "title: T\nn: 5\nbig: 12345678901234567890\nf: 1.50\ne: 1e3\nneg: -0.0",
		"multiline":        "title: T\nnote: |\n  line one\n  line two\nfolded: >\n  a\n  b",
		"type and misc":    "title: T\ntype: 文案\nstatus: 定稿\nflag: yes\nnothing: null",
		"untitled":         "tags: [x]",
		"unicode keys":     "标题: x\ntitle: T",
		"quoted keys":      "\"a b\": 1\n'c:d': 2\ntitle: T",
		"flow header":      "title: T\ntags: [a,    b]\nmap: {a: 1,   b: 2}",
	}
	i := 0
	for _, hdr := range headers {
		i++
		id := fmt.Sprintf("T%09d", i)
		raw := "---\nid: " + id + "\n" + hdr + "\n---\n第一行\n\n```\ncode\n```\n"
		if err := os.WriteFile(filepath.Join(f.dir, fmt.Sprintf("p%02d.md", i)), []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code, _, raw := f.do("POST", base+"/reindex", nil); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	for n := 1; n <= len(headers); n++ {
		id := fmt.Sprintf("T%09d", n)
		file := filepath.Join(f.dir, fmt.Sprintf("p%02d.md", n))
		before, _ := os.ReadFile(file)
		req := httptest.NewRequest("GET", base+"/pages/"+id, nil)
		req.Host = "localhost:8080"
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("GET %s: %d %s\n%s", id, rec.Code, rec.Body, before)
		}
		var got struct {
			Rev  string          `json:"rev"`
			Meta json.RawMessage `json:"meta"`
			Body json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		put := `{"baseRev":"` + got.Rev + `","meta":` + string(got.Meta) + `,"body":` + string(got.Body) + `}`
		code, r, raw := f.do("PUT", base+"/pages/"+id, put)
		if code != 200 {
			t.Errorf("%s\nPUT of the served page was refused: %d %s", before, code, raw)
			continue
		}
		after, _ := os.ReadFile(file)
		if string(after) != string(before) {
			t.Errorf("file changed by a save with no edits:\n--- before\n%s\n--- after\n%s", before, after)
		}
		if r["rev"] != got.Rev {
			t.Errorf("%s: rev changed", id)
		}
	}
	// An edit keeps untouched values byte for byte while changing one.
	id := "T000000001"
	_, page, _ := f.do("GET", base+"/pages/"+id, nil)
	meta := page["meta"].(map[string]any)
	meta["extra"] = "added"
	code, _, raw := f.do("PUT", base+"/pages/"+id, map[string]any{"baseRev": page["rev"], "meta": meta, "body": page["body"]})
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestSaveKeepsFileHeaderOrderOverHTTP(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.dir, "o.md"), []byte("---\nid: ABCDE00001\n2024: 年度\ntitle: T\nzeta: z\n---\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.do("POST", base+"/reindex", nil)
	_, p, _ := f.do("GET", base+"/pages/ABCDE00001", nil)
	// What a browser sends after parsing and stringifying the object: integer-like keys first.
	body := `{"baseRev":"` + p["rev"].(string) + `","meta":{"2024":"年度","zeta":"z","title":"T","id":"ABCDE00001","fresh":1},"body":"b\n"}`
	code, _, raw := f.do("PUT", base+"/pages/ABCDE00001", body)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	got, _ := os.ReadFile(filepath.Join(f.dir, "o.md"))
	if string(got) != "---\nid: ABCDE00001\n2024: 年度\ntitle: T\nzeta: z\nfresh: 1\n---\nb\n" {
		t.Fatalf("%s", got)
	}
}

// --- cross-site requests ---------------------------------------------------

func send(f *fixture, method, path, body string, hdr map[string]string) (int, string) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "localhost:8080"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestStateChangesNeedAJSONContentType(t *testing.T) {
	f := newFixture(t)
	p := f.create("", map[string]any{"title": "T"}, "")
	id := p["id"].(string)
	for _, c := range []struct{ method, path, body string }{
		{"POST", base + "/pages", ""}, {"POST", base + "/pages", "{}"},
		{"POST", base + "/reindex", ""}, {"DELETE", base + "/pages/" + id, ""},
		{"POST", base + "/pages/" + id + "/move", `{"folder":"x"}`}, {"PUT", base + "/pages/" + id, "{}"},
		{"POST", base + "/folders", `{"path":"x"}`}, {"PATCH", base + "/pages", ""},
	} {
		for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data"} {
			hdr := map[string]string{}
			if ct != "" {
				hdr["Content-Type"] = ct
			}
			code, raw := send(f, c.method, c.path, c.body, hdr)
			if code != 415 || !strings.Contains(raw, `"invalid"`) {
				t.Errorf("%s %s (%q): %d %s", c.method, c.path, ct, code, raw)
			}
		}
	}
	// Nothing was changed by any of them.
	if code, r, _ := f.do("GET", base+"/pages/"+id, nil); code != 200 || r["id"] != id {
		t.Fatal("the page was deleted or moved by a refused request")
	}
	if _, r, _ := f.do("GET", base+"/pages", nil); len(r["items"].([]any)) != 1 {
		t.Fatal("a refused request created a page")
	}
	// With the type, with charset, the same requests work.
	if code, raw := send(f, "POST", base+"/pages", "", map[string]string{"Content-Type": "application/json; charset=utf-8"}); code != 201 {
		t.Fatalf("%d %s", code, raw)
	}
	if code, _ := send(f, "POST", base+"/reindex", "", map[string]string{"Content-Type": "application/json"}); code != 200 {
		t.Fatal(code)
	}
	if code, _ := send(f, "DELETE", base+"/pages/"+id, "", map[string]string{"Content-Type": "application/json"}); code != 204 {
		t.Fatal(code)
	}
	// Reads need none.
	if code, _ := send(f, "GET", "/api/meta", "", nil); code != 200 {
		t.Fatal(code)
	}
}

func TestCrossOriginRequestsAreRefused(t *testing.T) {
	f := newFixture(t, func(o *api.Options) {
		o.RestrictHost = true
		o.AllowOrigins = []string{"http://localhost:5173", "HTTPS://Dev.Example.com/"}
	})
	json := map[string]string{"Content-Type": "application/json"}
	with := func(k, v string) map[string]string {
		return map[string]string{"Content-Type": "application/json", k: v}
	}
	cases := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"no origin (curl)", json, 201},
		{"own origin", with("Origin", "http://localhost:8080"), 201},
		{"own origin, other case", with("Origin", "http://LOCALHOST:8080"), 201},
		{"another site", with("Origin", "https://evil.example.com"), 403},
		{"another port on localhost", with("Origin", "http://localhost:9999"), 403},
		{"same host, other scheme", with("Origin", "https://localhost:8080"), 403},
		{"null origin (sandboxed frame)", with("Origin", "null"), 403},
		{"garbage origin", with("Origin", "not a url"), 403},
		{"origin without host", with("Origin", "http://"), 403},
		{"allowed origin", with("Origin", "http://localhost:5173"), 201},
		{"allowed origin with a slash and other case", with("Origin", "http://LOCALHOST:5173/"), 201},
		{"allowed https origin", with("Origin", "https://dev.example.com"), 201},
		{"allowed host, wrong scheme", with("Origin", "http://dev.example.com"), 403},
		{"fetch metadata same-origin", with("Sec-Fetch-Site", "same-origin"), 201},
		{"fetch metadata none (typed URL)", with("Sec-Fetch-Site", "none"), 201},
		{"fetch metadata cross-site", with("Sec-Fetch-Site", "cross-site"), 403},
		{"fetch metadata same-site", with("Sec-Fetch-Site", "same-site"), 403},
		{"fetch metadata cross-site with allowed origin", map[string]string{"Content-Type": "application/json", "Origin": "http://localhost:5173", "Sec-Fetch-Site": "cross-site"}, 403},
	}
	created := 0
	for _, c := range cases {
		code, raw := send(f, "POST", base+"/pages", "{}", c.hdr)
		if code != c.want {
			t.Errorf("%s: %d %s", c.name, code, raw)
		}
		if code == 403 && !strings.Contains(raw, `"forbidden"`) {
			t.Errorf("%s: wrong error shape %s", c.name, raw)
		}
		if code == 201 {
			created++
		}
		// Reads are checked the same way.
		hdr := map[string]string{}
		for k, v := range c.hdr {
			if k != "Content-Type" {
				hdr[k] = v
			}
		}
		wantGet := 200
		if c.want == 403 {
			wantGet = 403
		}
		if code, _ := send(f, "GET", base+"/pages", "", hdr); code != wantGet {
			t.Errorf("%s: GET gave %d", c.name, code)
		}
	}
	if _, r, _ := f.do("GET", base+"/pages?limit=200", nil); len(r["items"].([]any)) != created {
		t.Fatalf("refused requests created pages: %d, want %d", len(r["items"].([]any)), created)
	}
	// An https origin is never implicitly the server's own, even for the
	// same host: only the allow list says so.
	req := httptest.NewRequest("POST", "https://localhost:8080"+base+"/pages", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://localhost:8080")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("https own origin: %d %s", rec.Code, rec.Body)
	}
}

// Lifting the loopback rule (what --insecure-no-auth does) does not lift the
// cross-site checks, and there is then no implicit own origin: a rebinding page
// has Origin equal to Host and must not pass.
func TestWithoutLoopbackModeOnlyTheAllowListGivesAnOrigin(t *testing.T) {
	f := newFixture(t, func(o *api.Options) {
		o.RestrictHost = false
		o.AllowOrigins = []string{"http://192.168.1.5:8080"}
	})
	post := func(host string, hdr map[string]string) int {
		req := httptest.NewRequest("POST", base+"/pages", strings.NewReader("{}"))
		req.Host = host
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		return rec.Code
	}
	// DNS rebinding: the page's origin is the attacker's name, which is also the Host.
	if c := post("evil.example.com:8080", map[string]string{"Origin": "http://evil.example.com:8080", "Sec-Fetch-Site": "same-origin"}); c != 403 {
		t.Errorf("rebinding page: %d", c)
	}
	// Even the server's own address is not implicit.
	if c := post("10.0.0.7:8080", map[string]string{"Origin": "http://10.0.0.7:8080"}); c != 403 {
		t.Errorf("own address without allow-origin: %d", c)
	}
	if c := post("localhost:8080", map[string]string{"Origin": "http://localhost:8080"}); c != 403 {
		t.Errorf("loopback origin outside loopback mode: %d", c)
	}
	if c := post("192.168.1.5:8080", map[string]string{"Origin": "http://192.168.1.5:8080"}); c != 201 {
		t.Errorf("allow-listed origin: %d", c)
	}
	// Clients that are not browsers keep working.
	if c := post("10.0.0.7:8080", nil); c != 201 {
		t.Errorf("curl: %d", c)
	}
	if c := post("10.0.0.7:8080", map[string]string{"Sec-Fetch-Site": "cross-site"}); c != 403 {
		t.Errorf("cross-site fetch metadata: %d", c)
	}
}

func TestLoopbackAliasesAreOwnOriginInLoopbackMode(t *testing.T) {
	f := newFixture(t, func(o *api.Options) { o.RestrictHost = true })
	post := func(host, origin string) int {
		req := httptest.NewRequest("POST", base+"/pages", strings.NewReader("{}"))
		req.Host = host
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, c := range []struct {
		host, origin string
		want         int
	}{
		{"127.0.0.1:8080", "http://127.0.0.1:8080", 201},
		{"localhost:8080", "http://localhost:8080", 201},
		{"[::1]:8080", "http://[::1]:8080", 201},
		{"127.0.0.1:8080", "http://localhost:8080", 201},
		{"localhost:8080", "http://[::1]:8080", 201},
		{"localhost:8080", "http://127.0.0.1:8080", 201},
		{"app.localhost:8080", "http://app.localhost:8080", 201},
		{"localhost:8080", "http://localhost:9090", 403},  // another local app
		{"localhost:8080", "http://127.0.0.1:5173", 403},  // the dev server, unless allowed
		{"localhost:8080", "https://localhost:8080", 403}, // not plain http
		{"localhost:8080", "http://evil.example.com:8080", 403},
		{"localhost:8080", "http://localhost.evil.com:8080", 403},
		{"evil.example.com:8080", "http://evil.example.com:8080", 403}, // refused by the Host check
		{"localhost", "http://localhost", 201},                         // default port
		{"localhost", "http://localhost:80", 201},
	} {
		if got := post(c.host, c.origin); got != c.want {
			t.Errorf("Host %s Origin %s: %d, want %d", c.host, c.origin, got, c.want)
		}
	}
}

// Behind a TLS-terminating reverse proxy the request arrives as plain http for
// the public host name; the browser's origin is https. Allow-listing the origin
// makes both the Host and the Origin acceptable.
func TestBehindATLSProxyTheAllowListIsWhatCounts(t *testing.T) {
	f := newFixture(t, func(o *api.Options) {
		o.RestrictHost = true
		o.AllowOrigins = []string{"https://notes.example.com"}
	})
	post := func(host, origin string) int {
		req := httptest.NewRequest("POST", base+"/pages", strings.NewReader("{}"))
		req.Host = host
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := post("notes.example.com", "https://notes.example.com"); c != 201 {
		t.Errorf("proxied browser request: %d", c)
	}
	if c := post("other.example.com", "https://other.example.com"); c != 403 {
		t.Errorf("another host: %d", c)
	}
	if c := post("notes.example.com", "https://evil.example.com"); c != 403 {
		t.Errorf("foreign origin to the proxied host: %d", c)
	}
}
