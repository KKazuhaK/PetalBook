package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
)

// send makes a request whose body and answer need not be JSON.
func (f *fixture) send(method, path string, body []byte, headers ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Host = "localhost:8080"
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) upload(id, name string, data []byte) (int, map[string]any) {
	f.t.Helper()
	rec := f.send("POST", base+"/pages/"+id+"/assets?name="+url.QueryEscape(name), data, "Content-Type", "application/octet-stream")
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		f.t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Code, out
}

func hash8(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:4])
}

var pngBytes = []byte("\x89PNG\r\n\x1a\n not really a picture")

func TestUploadStoresAFileOfThePage(t *testing.T) {
	f := newFixture(t)
	id := f.create("Tang", map[string]any{"title": "暮雪"}, "x\n")["id"].(string)

	code, r := f.upload(id, "街 道.PNG", pngBytes)
	name := "街 道-" + hash8(pngBytes) + ".png"
	if code != 201 || r["path"] != "/assets/"+id+"/"+name || r["name"] != name || r["size"] != float64(len(pngBytes)) {
		t.Fatalf("%d %v", code, r)
	}
	got, err := os.ReadFile(filepath.Join(f.dir, "assets", id, name))
	if err != nil || !bytes.Equal(got, pngBytes) {
		t.Fatalf("the file is not there: %v", err)
	}
	// The same bytes under the same name: nothing new.
	code, r2 := f.upload(id, "街 道.PNG", pngBytes)
	if code != 200 || r2["path"] != r["path"] || r2["size"] != r["size"] {
		t.Fatalf("%d %v", code, r2)
	}
	if l := listDir(t, filepath.Join(f.dir, "assets", id)); len(l) != 1 {
		t.Fatalf("%v", l)
	}
	// Other bytes under the same name are another file.
	code, r3 := f.upload(id, "街 道.PNG", []byte("other"))
	if code != 201 || r3["name"] == r["name"] {
		t.Fatalf("%d %v", code, r3)
	}

	// The attachments are not pages and not a folder.
	_, tree, raw := f.do("GET", base+"/tree", nil)
	if strings.Contains(raw, "assets") {
		t.Fatalf("%v", tree)
	}
	// An upload named like a page is still not a page.
	code, md := f.upload(id, "notes.md", []byte("---\nid: 0000000000\n---\nhello\n"))
	if code != 201 {
		t.Fatal(md)
	}
	// It can be fetched, as a download.
	if rec := f.send("GET", base+"/files"+md["path"].(string), nil); rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if _, err := f.sp.Reindex(); err != nil {
		t.Fatal(err)
	}
	_, l, _ := f.do("GET", base+"/pages", nil)
	if len(l["items"].([]any)) != 1 {
		t.Fatalf("%v", l)
	}

	// It is committed with the next background commit.
	if err := f.sp.Flush(); err != nil {
		t.Fatal(err)
	}
	if out := rg(t, f.dir, "ls-files", "assets"); !strings.Contains(out, name) {
		t.Fatalf("not committed: %q", out)
	}
	if out := rg(t, f.dir, "status", "--porcelain", "--", "assets"); out != "" {
		t.Fatalf("left uncommitted: %q", out)
	}
}

func TestUploadNames(t *testing.T) {
	f := newFixture(t)
	id := f.create("", nil, "x\n")["id"].(string)
	data := []byte("data")
	h := hash8(data)
	for given, want := range map[string]string{
		"photo.JPG":              "photo-" + h + ".jpg",
		"../../etc/passwd":       "passwd-" + h,
		`C:\Users\me\a b.txt`:    "a b-" + h + ".txt",
		".bashrc":                "file-" + h + ".bashrc",
		"a<b>:c?.png":            "a-b--c--" + h + ".png",
		"trailing. .pdf":         "trailing-" + h + ".pdf",
		"no extension":           "no extension-" + h,
		"weird.ext with spaces":  "weird.ext with spaces-" + h,
		"x.toolongextension":     "x.toolongextension-" + h,
		strings.Repeat("长", 200): strings.Repeat("长", 80) + "-" + h,
		"tab\tname.gif":          "tab-name-" + h + ".gif",
		"...":                    "file-" + h,
		"archive.tar.gz":         "archive.tar-" + h + ".gz",
		"UPPER.Md":               "UPPER-" + h + ".md",
		"/abs/path/to/图.webp":    "图-" + h + ".webp",
		"name.":                  "name-" + h,
	} {
		code, r := f.upload(id, given, data)
		if (code != 201 && code != 200) || r["name"] != want {
			t.Errorf("%q: %d %v, want %q", given, code, r["name"], want)
		}
	}
}

func TestUploadRefusals(t *testing.T) {
	f := newFixture(t, func(o *api.Options) { o.MaxUpload = 1000 })
	id := f.create("", nil, "x\n")["id"].(string)
	u := base + "/pages/" + id + "/assets"
	check := func(what string, rec *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		var r map[string]any
		json.Unmarshal(rec.Body.Bytes(), &r)
		if rec.Code != status || r["error"] != code || r["message"] == "" {
			t.Errorf("%s: %d %s", what, rec.Code, rec.Body.String())
		}
	}
	oct := []string{"Content-Type", "application/octet-stream"}
	check("too large", f.send("POST", u+"?name=a.png", bytes.Repeat([]byte("x"), 1001), oct...), 413, "invalid")
	if rec := f.send("POST", u+"?name=a.png", bytes.Repeat([]byte("x"), 1000), oct...); rec.Code != 201 {
		t.Errorf("at the limit: %d %s", rec.Code, rec.Body.String())
	}
	check("empty", f.send("POST", u+"?name=a.png", nil, oct...), 400, "invalid")
	check("no name", f.send("POST", u, []byte("x"), oct...), 400, "invalid")
	check("blank name", f.send("POST", u+"?name=%20", []byte("x"), oct...), 400, "invalid")
	check("json type", f.send("POST", u+"?name=a.png", []byte("x"), "Content-Type", "application/json"), 415, "invalid")
	check("form type", f.send("POST", u+"?name=a.png", []byte("x"), "Content-Type", "multipart/form-data; boundary=x"), 415, "invalid")
	check("no type", f.send("POST", u+"?name=a.png", []byte("x")), 415, "invalid")
	check("no page", f.send("POST", base+"/pages/0000000000/assets?name=a.png", []byte("x"), oct...), 404, "not_found")
	check("bad id", f.send("POST", base+"/pages/nope/assets?name=a.png", []byte("x"), oct...), 404, "not_found")
	check("cross site", f.send("POST", u+"?name=a.png", []byte("x"), "Content-Type", "application/octet-stream", "Sec-Fetch-Site", "cross-site"), 403, "forbidden")
	// The exception is this route only: octet-stream is not accepted elsewhere.
	check("other route", f.send("POST", base+"/pages", []byte("{}"), oct...), 415, "invalid")
	check("method", f.send("GET", u, nil), 405, "invalid")

	// A link in the way is never followed.
	outside := t.TempDir()
	id2 := f.create("", nil, "y\n")["id"].(string)
	if err := os.Symlink(outside, filepath.Join(f.dir, "assets", id2)); err != nil {
		t.Skip("no symbolic links here")
	}
	check("link", f.send("POST", base+"/pages/"+id2+"/assets?name=a.png", []byte("x"), oct...), 400, "invalid")
	if l := listDir(t, outside); len(l) != 0 {
		t.Fatalf("written through the link: %v", l)
	}

	code, m, _ := f.do("GET", "/api/meta", nil)
	if code != 200 || m["maxUpload"] != float64(1000) {
		t.Fatalf("%v", m)
	}
}

func TestGetFile(t *testing.T) {
	f := newFixture(t)
	id := f.create("Tang", map[string]any{"title": "暮雪"}, "x\n")["id"].(string)
	_, r := f.upload(id, "street.png", pngBytes)
	p := base + "/files" + r["path"].(string)

	rec := f.send("GET", p, nil)
	h := rec.Header()
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), pngBytes) {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if h.Get("Content-Type") != "image/png" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Cache-Control") != "private, no-cache" || h.Get("Last-Modified") == "" ||
		!strings.HasPrefix(h.Get("Content-Disposition"), "inline; filename*=UTF-8''street-") ||
		h.Get("Content-Security-Policy") != "default-src 'none'; style-src 'unsafe-inline'; sandbox" {
		t.Fatalf("%v", h)
	}
	// Revalidation and ranges.
	if rec := f.send("GET", p, nil, "If-Modified-Since", h.Get("Last-Modified")); rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatalf("%d", rec.Code)
	}
	if rec := f.send("GET", p, nil, "Range", "bytes=1-3"); rec.Code != 206 || rec.Body.String() != "PNG" {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if rec := f.send("HEAD", p, nil); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("%d", rec.Code)
	}

	// An SVG is shown, sandboxed; a script in it cannot run.
	_, s := f.upload(id, "x.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	rec = f.send("GET", base+"/files"+s["path"].(string), nil)
	if rec.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("%v", rec.Header())
	}
	// What is not on the list is downloaded, whatever it looks like.
	_, hfile := f.upload(id, "页 面.html", []byte("<script>alert(1)</script>"))
	rec = f.send("GET", base+"/files"+(&url.URL{Path: hfile["path"].(string)}).EscapedPath(), nil)
	cd := rec.Header().Get("Content-Disposition")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(cd, "attachment; filename*=UTF-8''%E9%A1%B5%20%E9%9D%A2-") || strings.ContainsAny(cd, "\"\r\n") {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}

	// Files put there by hand, anywhere in the space, are served too.
	os.MkdirAll(filepath.Join(f.dir, "Tang", "img"), 0o755)
	os.WriteFile(filepath.Join(f.dir, "Tang", "img", "a.txt"), []byte("你好"), 0o644)
	rec = f.send("GET", base+"/files/Tang/img/a.txt", nil)
	if rec.Code != 200 || rec.Body.String() != "你好" || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}

	// What may not be read is "no such file", all alike.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("secret"), 0o644)
	os.WriteFile(filepath.Join(f.dir, "Tang", ".hidden.txt"), []byte("h"), 0o644)
	links := os.Symlink(outside, filepath.Join(f.dir, "Tang", "link.txt")) == nil &&
		os.Symlink(filepath.Dir(outside), filepath.Join(f.dir, "linkdir")) == nil
	for _, bad := range []string{
		"/Tang/暮雪.md", "/Tang/%E6%9A%AE%E9%9B%AA.MD", "/Tang/.hidden.txt", "/.git/config", "/.git/HEAD", "/.petalbook/space.yml",
		"/Tang", "/Tang/img", "/nope.png", "/Tang/img/a.txt/more", "/assets/" + id + "/missing.png",
		"/..%2F..%2Fetc%2Fpasswd", "/Tang/%2E%2E/%2E%2E/x", "/a%00b.txt",
	} {
		rec := f.send("GET", base+"/files"+bad, nil)
		if rec.Code == 301 || rec.Code == 307 || rec.Code == 308 { // the router tidied the path; follow it
			rec = f.send("GET", rec.Header().Get("Location"), nil)
		}
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"not_found"`) {
			t.Errorf("%s: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	if links {
		for _, bad := range []string{"/Tang/link.txt", "/linkdir/secret.txt"} {
			if rec := f.send("GET", base+"/files"+bad, nil); rec.Code != 404 || strings.Contains(rec.Body.String(), "secret") {
				t.Errorf("%s: %d %s", bad, rec.Code, rec.Body.String())
			}
		}
	}
	if rec := f.send("GET", base+"/files/", nil); rec.Code != 404 {
		t.Errorf("empty path: %d", rec.Code)
	}
	if rec := f.send("POST", base+"/files/Tang/img/a.txt", nil, "Content-Type", "application/json"); rec.Code != 405 {
		t.Errorf("POST: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPageCarriesItsHeaderText(t *testing.T) {
	f := newFixture(t)
	id := f.create("", map[string]any{"title": "T"}, "x\n")["id"].(string)
	p := filepath.Join(f.dir, "T.md")
	text := "id: " + id + "\ntitle:   T   # as written\ntags: [a,   b]\n"
	if err := os.WriteFile(p, []byte("---\n"+text+"---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, r, raw := f.do("GET", base+"/pages/"+id, nil)
	if code != 200 || r["header"] != text || r["body"] != "body\n" {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestExcerptIs400Characters(t *testing.T) {
	f := newFixture(t)
	f.create("", nil, strings.Repeat("字", 500)+"\n")
	_, r, _ := f.do("GET", base+"/pages", nil)
	ex := r["items"].([]any)[0].(map[string]any)["excerpt"].(string)
	if n := len([]rune(ex)); n != 400 {
		t.Fatalf("%d", n)
	}
}

func TestSingleUserInbox(t *testing.T) {
	f := newFixture(t)
	_, r, raw := f.do("GET", "/api/meta", nil)
	if sp := r["spaces"].([]any)[0].(map[string]any); sp["inbox"] != "Inbox" {
		t.Fatal(raw)
	}
	if err := f.opts.Spaces.SetSingleInbox("收件箱/今天"); err != nil {
		t.Fatal(err)
	}
	_, r, raw = f.do("GET", "/api/spaces", nil)
	if sp := r["spaces"].([]any)[0].(map[string]any); sp["inbox"] != "收件箱/今天" {
		t.Fatal(raw)
	}
	for _, bad := range []string{"", "assets", ".x", "a/../b", "/a"} {
		if err := f.opts.Spaces.SetSingleInbox(bad); !errors.Is(err, spaces.ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestSpaceInbox(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		sid := c.personalSpace()
		inboxOf := func(id string) any {
			for _, s := range c.do("GET", "/api/spaces", nil).want(t, 200, "").body["spaces"].([]any) {
				if m := s.(map[string]any); m["id"] == id {
					return m["inbox"]
				}
			}
			return nil
		}
		if got := inboxOf(sid); got != "Inbox" {
			t.Fatalf("personal: %v", got)
		}
		r := c.do("POST", "/api/spaces", map[string]any{"name": "Tang", "inbox": "收件箱"}).want(t, 201, "")
		tang := r.body["id"].(string)
		if r.body["inbox"] != "收件箱" || inboxOf(tang) != "收件箱" {
			t.Fatal(r.raw)
		}
		if r := c.do("POST", "/api/spaces", map[string]any{"name": "Plain"}).want(t, 201, ""); r.body["inbox"] != "Inbox" {
			t.Fatal(r.raw)
		}
		for _, bad := range []string{"assets", ".hidden", "a//b", "a/", "con?"} {
			c.do("POST", "/api/spaces", map[string]any{"name": "Bad", "inbox": bad}).want(t, 400, "invalid")
		}

		u := "/api/spaces/" + tang
		if r := c.do("PATCH", u, map[string]any{"inbox": "随手记/收件箱"}).want(t, 200, ""); r.body["inbox"] != "随手记/收件箱" || r.body["name"] != "Tang" {
			t.Fatal(r.raw)
		}
		if r := c.do("PATCH", u, map[string]any{"name": "唐"}).want(t, 200, ""); r.body["inbox"] != "随手记/收件箱" || r.body["name"] != "唐" {
			t.Fatal(r.raw)
		}
		if r := c.do("PATCH", u, map[string]any{"name": "Tang", "inbox": "In"}).want(t, 200, ""); r.body["inbox"] != "In" || r.body["name"] != "Tang" {
			t.Fatal(r.raw)
		}
		c.do("PATCH", u, map[string]any{}).want(t, 400, "invalid")
		c.do("PATCH", u, map[string]any{"inbox": ""}).want(t, 400, "invalid")
		c.do("PATCH", u, map[string]any{"inbox": "assets"}).want(t, 400, "invalid")
		if got := inboxOf(tang); got != "In" {
			t.Fatalf("%v", got)
		}
		// It survives the membership cache being dropped (it is in the database).
		a.dropMembershipCache(t, "kazuha")
		if got := inboxOf(tang); got != "In" {
			t.Fatalf("%v", got)
		}
		// The setting is a name only: no folder was made.
		if _, err := os.Stat(filepath.Join(a.dir, "spaces", tang, "In")); !os.IsNotExist(err) {
			t.Fatalf("a folder was made: %v", err)
		}
		// Someone else's space does not exist for them.
		other := a.addUser("mallory", pw, "member")
		other.do("PATCH", u, map[string]any{"inbox": "X"}).want(t, 404, "not_found")
		if m := c.do("GET", "/api/meta", nil).want(t, 200, ""); m.body["maxUpload"] != float64(api.DefaultMaxUpload) {
			t.Fatal(m.raw)
		}
	})
}

// pushFails is the real git, whose pushes the remote refuses while on.
type pushFails struct {
	netSwitch
	fail bool
}

func (p *pushFails) Run(ctx context.Context, c gitstore.Cmd) (string, string, error) {
	if sub, _ := gitstore.Subcommand(c.Args); sub == "push" && p.fail {
		return "", "remote: error: GH013: Repository rule violations found\n ! [remote rejected] main -> main (push declined)", errors.New("exit status 1")
	}
	return p.netSwitch.Run(ctx, c)
}

func (c *client) upload(base, id, name string, data []byte) result {
	return c.do("POST", base+"/pages/"+id+"/assets?name="+url.QueryEscape(name), string(data), "Content-Type", "application/octet-stream")
}

func TestUploadInASpaceThatFollowsARemote(t *testing.T) {
	n := &pushFails{}
	o := accOpts{spaces: func(o *spaces.Options) {
		o.RemotePolicy = gitstore.Policy{Hosts: []string{"*"}, AllowUnsafe: true}
		o.RemoteRunner = n
		o.FetchFresh, o.FailFast = time.Nanosecond, time.Nanosecond
	}}
	eachAccounts(t, o, func(t *testing.T, a *acc) {
		n.set(false)
		n.fail = false
		c := a.setup("kazuha", pw)
		sid := c.personalSpace()
		base := "/api/spaces/" + sid
		root := filepath.Join(a.dir, "spaces", sid)
		remote := bareRemote(t)
		c.do("PUT", base+"/remote", connectBody(remote)).want(t, 200, "")
		id := c.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Doc"}, "body": "v1\n"}).want(t, 201, "").body["id"].(string)

		// Pushed with the answer.
		r := c.upload(base, id, "a.png", pngBytes).want(t, 201, "")
		rel := strings.TrimPrefix(r.body["path"].(string), "/")
		if got := rg(t, remote, "show", "main:"+rel); !strings.HasPrefix(got, "\x89PNG") {
			t.Fatalf("the remote has %q", got)
		}
		tip := rg(t, remote, "rev-parse", "main")
		// Again: nothing new, no commit.
		c.upload(base, id, "a.png", pngBytes).want(t, 200, "")
		if rg(t, remote, "rev-parse", "main") != tip {
			t.Fatal("an identical upload made a commit")
		}

		// The remote cannot be reached: 503, and no file is left.
		id2 := c.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Two"}, "body": "v1\n"}).want(t, 201, "").body["id"].(string)
		n.set(true)
		other := []byte("another file")
		if r := c.upload(base, id2, "b.png", other); r.code != 503 || r.body["error"] != "unavailable" {
			t.Fatalf("%d %s", r.code, r.raw)
		}
		if _, err := os.Stat(filepath.Join(root, "assets", id2)); !os.IsNotExist(err) {
			t.Fatalf("something was left behind: %v", err)
		}
		n.set(false)

		// The remote refuses the push: the file, its directory and the commit are taken back.
		n.fail = true
		head := rg(t, root, "rev-parse", "HEAD")
		if r := c.upload(base, id2, "b.png", other); r.code != 503 || r.body["error"] != "unavailable" {
			t.Fatalf("%d %s", r.code, r.raw)
		}
		if _, err := os.Stat(filepath.Join(root, "assets", id2)); !os.IsNotExist(err) {
			t.Fatalf("the directory was left behind: %v", err)
		}
		if got := rg(t, root, "rev-parse", "HEAD"); got != head {
			t.Fatalf("a commit was left behind: %s", got)
		}
		if out := rg(t, root, "status", "--porcelain"); out != "" {
			t.Fatalf("the clone is not clean: %q", out)
		}
		// The first page's directory, which existed before, is untouched by an undo.
		if r := c.upload(base, id, "c.png", other); r.code != 503 {
			t.Fatalf("%d %s", r.code, r.raw)
		}
		if l := listDir(t, filepath.Join(root, "assets", id)); len(l) != 1 {
			t.Fatalf("%v", l)
		}
		n.fail = false

		// And once the remote takes pushes again, the upload goes through.
		r = c.upload(base, id2, "b.png", other).want(t, 201, "")
		if got := rg(t, remote, "show", "main:"+strings.TrimPrefix(r.body["path"].(string), "/")); got != string(other) {
			t.Fatalf("%q", got)
		}
		// A file that came from the remote is served.
		wc := filepath.Join(t.TempDir(), "o")
		rg(t, t.TempDir(), "clone", "-q", remote, wc)
		os.MkdirAll(filepath.Join(wc, "pics"), 0o755)
		os.WriteFile(filepath.Join(wc, "pics", "r.txt"), []byte("from the remote"), 0o644)
		rg(t, wc, "add", "-A")
		rg(t, wc, "commit", "-qm", "add a file")
		rg(t, wc, "push", "-q", "origin", "HEAD:main")
		c.do("POST", base+"/remote/sync", nil).want(t, 200, "")
		if g := c.do("GET", base+"/files/pics/r.txt", nil); g.code != 200 || g.raw != "from the remote" {
			t.Fatalf("%d %s", g.code, g.raw)
		}
		// Members only.
		stranger := a.addUser("mallory", pw, "member")
		stranger.do("GET", base+"/files/pics/r.txt", nil).want(t, 404, "not_found")
		stranger.upload(base, id, "x.png", other).want(t, 404, "not_found")
		a.newClient("").do("GET", base+"/files/pics/r.txt", nil).want(t, 401, "unauthenticated")
	})
}
