package api_test

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// setFileLimit sets the tenant's files.maxBytes as its administrator would.
func setFileLimit(t *testing.T, root *client, tenant string, n any) result {
	t.Helper()
	return root.do("PUT", "/api/admin/tenants/"+tenant+"/settings/files.maxBytes", map[string]any{"value": n})
}

func TestFileLimitUpload(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		member := a.addUser("mem", pw, "member")
		sid := member.personalSpace()
		base := "/api/spaces/" + sid
		id := member.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "P"}, "body": "x\n"}).want(t, 201, "").body["id"].(string)

		// The effective value is the ceiling until the tenant sets one.
		if got := member.do("GET", "/api/meta", nil).want(t, 200, "").body["maxUpload"]; got != float64(10<<20) {
			t.Fatalf("maxUpload %v", got)
		}
		setFileLimit(t, root, store.DefaultTenantID, 2000).want(t, 200, "")
		if got := member.do("GET", "/api/meta", nil).want(t, 200, "").body["maxUpload"]; got != float64(2000) {
			t.Fatalf("maxUpload %v, want the tenant's 2000", got)
		}
		// Not signed in: the site's.
		if got := a.newClient("").do("GET", "/api/meta", nil).want(t, 200, "").body["maxUpload"]; got != float64(10<<20) {
			t.Fatalf("maxUpload for a visitor %v", got)
		}

		// One byte over, by the declared length: refused before it is read.
		r := member.upload(base, id, "street.jpg", bytes.Repeat([]byte("x"), 2001)).want(t, 413, "file_too_large")
		if r.body["limit"] != float64(2000) || r.body["size"] != float64(2001) || r.body["name"] != "street.jpg" {
			t.Fatalf("%v", r.body)
		}
		member.upload(base, id, "ok.jpg", bytes.Repeat([]byte("x"), 2000)).want(t, 201, "")

		// A body without a length is cut off at the limit: no size.
		req := httptest.NewRequest("POST", base+"/pages/"+id+"/assets?name=chunked.bin", strings.NewReader(strings.Repeat("y", 2500)))
		req.ContentLength = -1
		req.Host = strings.TrimPrefix(a.public, "https://")
		req.RemoteAddr = "198.51.100.7:1"
		req.Header.Set("Content-Type", "application/octet-stream")
		for _, ck := range member.cookies {
			req.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value})
		}
		rec := httptest.NewRecorder()
		a.h.ServeHTTP(rec, req)
		if rec.Code != 413 || !strings.Contains(rec.Body.String(), `"file_too_large"`) || strings.Contains(rec.Body.String(), `"size"`) || !strings.Contains(rec.Body.String(), `"limit":2000`) {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		// Nothing of the refused files was written.
		if matches, _ := filepath.Glob(filepath.Join(a.dir, "spaces", sid, "assets", id, "street*")); len(matches) != 0 {
			t.Fatalf("a refused file was written: %v", matches)
		}
		if matches, _ := filepath.Glob(filepath.Join(a.dir, "spaces", sid, "assets", id, "chunked*")); len(matches) != 0 {
			t.Fatalf("a refused file was written: %v", matches)
		}

		// A value above the site's ceiling cannot be set; one at it can.
		setFileLimit(t, root, store.DefaultTenantID, 11<<20).want(t, 409, "setting_restricted")
		setFileLimit(t, root, store.DefaultTenantID, 10<<20).want(t, 200, "")
		// Lowering the ceiling later is not tested here (the flag): the value in
		// effect is never above --max-upload, see TestFileLimitNeverAboveTheFlag.
	})
}

func TestFileLimitPagesAndDrafts(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		c := a.addUser("mem", pw, "member")
		sid := c.personalSpace()
		base := "/api/spaces/" + sid
		setFileLimit(t, root, store.DefaultTenantID, 1000).want(t, 200, "")
		big := strings.Repeat("z", 1200)

		// Create and save: the size of the file as it would be written.
		r := c.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Big"}, "body": big}).want(t, 413, "file_too_large")
		if r.body["limit"] != float64(1000) || r.body["size"].(float64) < 1200 || r.body["name"] == "" {
			t.Fatalf("%v", r.body)
		}
		// A body just under the limit but whose header makes the file larger.
		c.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": strings.Repeat("t", 80)}, "body": strings.Repeat("z", 990)}).want(t, 413, "file_too_large")
		pg := c.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Small"}, "body": "small\n"}).want(t, 201, "")
		id, rev := pg.body["id"].(string), pg.body["rev"].(string)
		url := base + "/pages/" + id
		c.do("PUT", url, map[string]any{"baseRev": rev, "meta": map[string]any{"title": "Small"}, "body": big}).want(t, 413, "file_too_large")
		// Neither was written.
		if got := c.do("GET", url, nil).want(t, 200, "").body["body"]; got != "small\n" {
			t.Fatalf("a refused save changed the page: %v", got)
		}

		// A draft is refused at the put, as the save would be.
		c.do("PUT", url+"/draft", draftBody(rev, "Small", big)).want(t, 413, "file_too_large")
		c.draft(url, rev, "Small", "fits\n").want(t, 200, "")

		// A draft over a limit lowered since is refused at the put as well.
		setFileLimit(t, root, store.DefaultTenantID, 100).want(t, 200, "")
		c.do("PUT", url+"/draft", draftBody(rev, "Small", strings.Repeat("q", 150))).want(t, 413, "file_too_large")

		// What exists is not judged: a page over a lowered limit is still read, and
		// can be saved again smaller.
		setFileLimit(t, root, store.DefaultTenantID, 1000).want(t, 200, "")
		pg2 := c.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Grows"}, "body": strings.Repeat("g", 800)}).want(t, 201, "")
		id2, rev2 := pg2.body["id"].(string), pg2.body["rev"].(string)
		setFileLimit(t, root, store.DefaultTenantID, 500).want(t, 200, "")
		c.do("GET", base+"/pages/"+id2, nil).want(t, 200, "")
		c.do("PUT", base+"/pages/"+id2, map[string]any{"baseRev": rev2, "meta": map[string]any{"title": "Grows"}, "body": strings.Repeat("g", 900)}).want(t, 413, "file_too_large")
		c.do("PUT", base+"/pages/"+id2, map[string]any{"baseRev": rev2, "meta": map[string]any{"title": "Grows"}, "body": strings.Repeat("g", 600)}).want(t, 200, "")
	})
}

func TestFileLimitCommitOfADraft(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		c := a.addUser("mem", pw, "member")
		url, pg := draftPage(t, c, "Doc", "v1\n")
		rev := pg["rev"].(string)
		setFileLimit(t, root, store.DefaultTenantID, 600).want(t, 200, "")
		c.draft(url, rev, "Doc", strings.Repeat("d", 400)).want(t, 200, "")
		setFileLimit(t, root, store.DefaultTenantID, 300).want(t, 200, "")
		c.do("POST", url+"/commit", map[string]any{"version": c.versions[url]}).want(t, 413, "file_too_large")
		if got := c.do("GET", url, nil).want(t, 200, "").body["body"]; got != "v1\n" {
			t.Fatalf("a refused commit wrote the page: %v", got)
		}
		// The draft is still there: raising the limit lets it through.
		setFileLimit(t, root, store.DefaultTenantID, 600).want(t, 200, "")
		c.do("POST", url+"/commit", map[string]any{"version": c.versions[url]}).want(t, 200, "")
	})
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, data := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(data))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFileLimitImport(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		c := a.addUser("mem", pw, "member")
		sid := c.personalSpace()
		base := "/api/spaces/" + sid
		setFileLimit(t, root, store.DefaultTenantID, 1000).want(t, 200, "")
		imp := func(name string, data []byte) result {
			return c.do("POST", base+"/import?name="+name, string(data), "Content-Type", "application/octet-stream")
		}
		// A page of a zip over the limit: nothing of the zip is written, the
		// refusal names the page.
		z := zipOf(t, map[string]string{"a/ok.md": "# ok\n\nfine\n", "a/big.md": "# big\n\n" + strings.Repeat("b", 1500)})
		r := imp("x.zip", z).want(t, 413, "file_too_large")
		if r.body["limit"] != float64(1000) || !strings.Contains(r.body["name"].(string), "big") {
			t.Fatalf("%v", r.body)
		}
		if n := c.do("GET", base+"/pages", nil).want(t, 200, "").body["pages"]; n != nil && len(n.([]any)) != 0 {
			t.Fatalf("a refused import wrote pages: %v", n)
		}
		// An attachment over the limit.
		z = zipOf(t, map[string]string{"n.md": "# n\n\n![](img.png)\n", "img.png": strings.Repeat("p", 1500)})
		imp("y.zip", z).want(t, 413, "file_too_large")
		// A single big Markdown file.
		imp("one.md", []byte("# one\n\n"+strings.Repeat("o", 1500))).want(t, 413, "file_too_large")
		// Within the limit it works.
		z = zipOf(t, map[string]string{"a/ok.md": "# ok\n\nfine\n"})
		imp("z.zip", z).want(t, 200, "")
	})
}

func TestFileLimitMCP(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		e := newMCP(t, a)
		setFileLimit(t, root, store.DefaultTenantID, 500).want(t, 200, "")
		r := e.w.tool("page_create", e.args("title", "T", "body", strings.Repeat("m", 800))).refused(t, "file_too_large")
		d := r.out["details"].(map[string]any)
		if d["limit"] != float64(500) || d["size"].(float64) < 800 {
			t.Fatalf("%v", d)
		}
		pg := e.w.tool("page_create", e.args("title", "T", "body", "short\n")).ok(t).page(t)
		e.w.tool("page_update", e.args("id", pg["id"], "baseRev", pg["rev"], "body", strings.Repeat("m", 800))).refused(t, "file_too_large")
		e.w.tool("page_update", e.args("id", pg["id"], "baseRev", pg["rev"], "body", "tiny\n")).ok(t)
	})
}

// Another tenant's limit is its own: one tenant's setting never reaches another's writes.
func TestFileLimitIsPerTenant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		setFileLimit(t, f.root, f.tenB, 500).want(t, 200, "")
		sa, sb := f.memA.personalSpace(), f.memB.personalSpace()
		for who, c := range map[string]*client{"A": f.memA, "B": f.memB} {
			sid := sa
			if who == "B" {
				sid = sb
			}
			r := c.do("POST", "/api/spaces/"+sid+"/pages", map[string]any{"meta": map[string]any{"title": "x" + who}, "body": strings.Repeat("t", 800)})
			if who == "A" {
				r.want(t, 201, "")
			} else {
				r.want(t, 413, "file_too_large")
			}
		}
		// A tenant administrator may set their own tenant's value (never above the site's), not another's.
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/files.maxBytes", map[string]any{"value": 700}).want(t, 200, "")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/files.maxBytes", map[string]any{"value": 11 << 20}).want(t, 409, "setting_restricted")
		f.admA.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/files.maxBytes", map[string]any{"value": 1}).want(t, 404, "not_found")
	})
}

// The value in effect is never above --max-upload, whatever the layers say: a
// site that lifts the restriction does not let a tenant past the ceiling, and
// the site's own value is the flag's (it cannot be written).
func TestFileLimitNeverAboveTheFlag(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		c := a.addUser("mem", pw, "member")
		root.do("PUT", "/api/admin/settings/files.maxBytes", map[string]any{"value": 1000}).want(t, 409, "conflict")
		root.do("PUT", "/api/admin/settings/files.maxBytes", map[string]any{"lower": map[string]any{"mode": "allow"}}).want(t, 200, "")
		setFileLimit(t, root, store.DefaultTenantID, 100<<20).want(t, 200, "")
		if got := c.do("GET", "/api/meta", nil).want(t, 200, "").body["maxUpload"]; got != float64(10<<20) {
			t.Fatalf("maxUpload %v, want the flag's 10 MiB", got)
		}
		base := "/api/spaces/" + c.personalSpace()
		id := c.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "P"}, "body": "x"}).want(t, 201, "").body["id"].(string)
		c.upload(base, id, "big.bin", bytes.Repeat([]byte("x"), 10<<20+1)).want(t, 413, "file_too_large")
	})
}
