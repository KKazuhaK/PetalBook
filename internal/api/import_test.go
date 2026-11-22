package api_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
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

func (f *fixture) importFile(query string, data []byte) (int, map[string]any) {
	f.t.Helper()
	rec := f.send("POST", base+"/import"+query, data, "Content-Type", "application/octet-stream")
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		f.t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Code, out
}

func zipBytes(t *testing.T, files ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i+1 < len(files); i += 2 {
		w, _ := zw.Create(files[i])
		w.Write([]byte(files[i+1]))
	}
	zw.Close()
	return buf.Bytes()
}

func (f *fixture) list(query string) []map[string]any {
	f.t.Helper()
	_, l, _ := f.do("GET", base+"/pages"+query, nil)
	var out []map[string]any
	for _, it := range l["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

func TestImportOneMarkdownFile(t *testing.T) {
	f := newFixture(t)
	code, r := f.importFile("?folder=收集&name="+url.QueryEscape("暮雪.md"), []byte("---\nid: 0000000000\nseries: 冬\ncreated: 2020-01-02\n---\n# 标题在这\n\n正文\n"))
	if code != 200 || r["pages"] != float64(1) || r["files"] != float64(0) || len(r["skipped"].([]any)) != 0 {
		t.Fatalf("%d %v", code, r)
	}
	items := f.list("?folder=" + url.QueryEscape("收集"))
	if len(items) != 1 || items[0]["title"] != "标题在这" || items[0]["created"] != "2020-01-02" || items[0]["id"] == "0000000000" {
		t.Fatalf("%v", items)
	}
	_, p, _ := f.do("GET", base+"/pages/"+items[0]["id"].(string), nil)
	if p["body"] != "正文\n" || p["meta"].(map[string]any)["series"] != "冬" {
		t.Fatalf("%v", p)
	}
	// Again: nothing is replaced.
	if code, _ := f.importFile("?folder=收集&name="+url.QueryEscape("暮雪.md"), []byte("# 标题在这\n\n另一份\n")); code != 200 {
		t.Fatal(code)
	}
	if items := f.list("?folder=" + url.QueryEscape("收集")); len(items) != 2 {
		t.Fatalf("%v", items)
	}
}

func TestImportAZipWithAttachments(t *testing.T) {
	f := newFixture(t)
	data := zipBytes(t,
		"notes/一.md", "# 一\n\n![图](img/a.png)\n",
		"notes/sub/二.md", "二\n",
		"notes/img/a.png", "\x89PNG picture",
		"notes/left.docx", "x",
	)
	code, r := f.importFile("?folder=In", data)
	if code != 200 || r["pages"] != float64(2) || r["files"] != float64(1) || len(r["skipped"].([]any)) != 1 {
		t.Fatalf("%d %v", code, r)
	}
	items := f.list("?folder=" + url.QueryEscape("In/notes"))
	if len(items) != 1 || items[0]["title"] != "一" {
		t.Fatalf("%v", items)
	}
	id := items[0]["id"].(string)
	_, p, _ := f.do("GET", base+"/pages/"+id, nil)
	body := p["body"].(string)
	prefix := "![图](</assets/" + id + "/a-"
	if !strings.HasPrefix(body, prefix) {
		t.Fatalf("%q", body)
	}
	addr := body[len("![图](<"):strings.Index(body, ">)")]
	if rec := f.send("GET", base+"/files"+addr, nil); rec.Code != 200 || rec.Body.String() != "\x89PNG picture" {
		t.Fatalf("%d", rec.Code)
	}
	if sub := f.list("?folder=" + url.QueryEscape("In/notes/sub")); len(sub) != 1 || sub[0]["title"] != "二" {
		t.Fatalf("%v", sub)
	}
	// One commit holds all of it.
	if err := f.sp.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "In", "notes", "一.md")); err != nil {
		t.Fatal(err)
	}
}

func TestImportRefusals(t *testing.T) {
	f := newFixture(t, func(o *api.Options) { o.MaxImport = 64 })
	for _, c := range []struct {
		query string
		data  []byte
		code  int
	}{
		{"", []byte("# x\n"), 400},                         // a single file needs its name
		{"?name=a.txt", []byte("x"), 400},                  // not Markdown
		{"?name=a.md", nil, 400},                           // empty
		{"?name=a.md&folder=.hidden", []byte("x"), 400},    // not a folder path
		{"?name=a.md&folder=assets", []byte("x"), 400},     // reserved
		{"?name=a.md", bytes.Repeat([]byte("x"), 65), 413}, // too large
	} {
		if code, r := f.importFile(c.query, c.data); code != c.code {
			t.Errorf("%q: %d %v", c.query, code, r)
		}
	}
	if rec := f.send("POST", base+"/import?name=a.md", []byte(`{}`), "Content-Type", "application/json"); rec.Code != 415 {
		t.Fatalf("%d", rec.Code)
	}
	if items := f.list(""); len(items) != 0 {
		t.Fatalf("something was imported: %v", items)
	}
	g := newFixture(t)
	if code, r := g.importFile("", zipBytes(t, "a.txt", "x")); code != 400 { // a zip without Markdown
		t.Fatalf("%d %v", code, r)
	}
	if code, r := g.importFile("", []byte("PK\x03\x04garbage")); code != 400 { // not really a zip
		t.Fatalf("%d %v", code, r)
	}
}

// In a space that follows a remote an import is one commit and one push, and
// when the push is refused nothing of it is left.
func TestImportInASpaceThatFollowsARemote(t *testing.T) {
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
		data := string(zipBytes(t, "n/一.md", "# 一\n\n![图](a.png)\n", "n/二.md", "# 二\n", "n/三.md", "三\n", "n/a.png", "\x89PNG"))
		send := func() result {
			return c.do("POST", base+"/import?folder=In", data, "Content-Type", "application/octet-stream")
		}

		// Refused by the remote: no page, no file, no folder, no commit.
		n.fail = true
		head := rg(t, root, "rev-parse", "HEAD")
		if r := send(); r.code != 503 || r.body["error"] != "unavailable" {
			t.Fatalf("%d %s", r.code, r.raw)
		}
		if _, err := os.Stat(filepath.Join(root, "In")); !os.IsNotExist(err) {
			t.Fatalf("the folder was left behind: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "assets")); !os.IsNotExist(err) {
			t.Fatalf("attachments were left behind: %v", err)
		}
		if got := rg(t, root, "rev-parse", "HEAD"); got != head {
			t.Fatal("a commit was left behind")
		}
		if out := rg(t, root, "status", "--porcelain"); out != "" {
			t.Fatalf("the clone is not clean: %q", out)
		}
		if l := c.do("GET", base+"/pages?folder=In%2Fn", nil).want(t, 200, "").body["items"].([]any); len(l) != 0 {
			t.Fatalf("the index still lists %d pages", len(l))
		}
		n.fail = false

		before := strings.Count(rg(t, remote, "rev-list", "main"), "\n")
		r := send().want(t, 200, "")
		if r.body["pages"] != float64(3) || r.body["files"] != float64(1) {
			t.Fatalf("%s", r.raw)
		}
		if after := strings.Count(rg(t, remote, "rev-list", "main"), "\n"); after != before+1 {
			t.Fatalf("%d commits for one import", after-before)
		}
		tree := rg(t, remote, "ls-tree", "-r", "--name-only", "main")
		for _, want := range []string{"In/n/一.md", "In/n/二.md", "In/n/三.md", "/a-"} {
			if !strings.Contains(tree, want) {
				t.Fatalf("%q is not on the remote:\n%s", want, tree)
			}
		}
	})
}

// A file in a folder the server would not make is left out and named; the
// rest is imported, and nothing is refused after pages were made.
func TestImportSkipsFoldersThatCannotBe(t *testing.T) {
	f := newFixture(t)
	code, r := f.importFile("?folder=In", zipBytes(t, "a.md", "# a\n", "x.md/c.md", "# c\n", "assets/d.md", "# d\n", strings.Repeat("长", 400)+"/e.md", "# e\n"))
	// (`assets` is reserved at the root of the space only: below In it is a folder like any other.)
	if code != 200 || r["pages"] != float64(2) || len(r["skipped"].([]any)) != 2 {
		t.Fatalf("%d %v", code, r)
	}
	if items := f.list("?folder=In"); len(items) != 1 || items[0]["title"] != "a" {
		t.Fatalf("%v", items)
	}
	// At the root that folder is the one for attachments, and not for pages.
	if code, r := f.importFile("", zipBytes(t, "assets/d.md", "# d\n", "ok.md", "# ok\n")); code != 200 || r["pages"] != float64(1) || len(r["skipped"].([]any)) != 1 {
		t.Fatalf("%d %v", code, r)
	}
	// The target folder itself is checked before anything is read.
	if code, r := f.importFile("?name=a.md&folder=notes.md", []byte("x")); code != 400 {
		t.Fatalf("%d %v", code, r)
	}
}
