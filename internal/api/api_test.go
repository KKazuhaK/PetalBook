package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/drafts"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

type fixture struct {
	t    *testing.T
	dir  string
	sp   *space.Space
	h    http.Handler
	opts api.Options
}

func newFixture(t *testing.T, mod ...func(*api.Options)) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	// A directory of its own, so a test can check that nothing is written next to it.
	dir := filepath.Join(t.TempDir(), "space")
	sp, err := space.Open(space.Options{Root: dir, Name: "测试空间", CommitIdle: time.Hour, CommitMax: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sp.Close() })
	st := storetest.Open(t, "sqlite")
	mgr, err := spaces.NewSingle(context.Background(), st, sp, nil)
	if err != nil {
		t.Fatal(err)
	}
	drf, err := drafts.New(context.Background(), st, mgr, drafts.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	opts := api.Options{Store: st, Spaces: mgr, Drafts: drf, Version: "test-1"}
	for _, m := range mod {
		m(&opts)
	}
	return &fixture{t: t, dir: dir, sp: sp, h: api.New(opts), opts: opts}
}

// do sends a JSON request. body is marshalled unless it is a string or []byte,
// which is sent as it is.
func (f *fixture) do(method, path string, body any, headers ...string) (int, map[string]any, string) {
	f.t.Helper()
	var rdr io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case string:
		rdr, ct = strings.NewReader(b), "application/json"
	case []byte:
		rdr, ct = bytes.NewReader(b), "application/json"
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			f.t.Fatal(err)
		}
		rdr, ct = bytes.NewReader(raw), "application/json"
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Host = "localhost:8080"
	if ct == "" && method != "GET" && method != "HEAD" {
		ct = "application/json" // the web client sends it on every change
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	raw := rec.Body.String()
	var out map[string]any
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			f.t.Fatalf("%s %s: response is not JSON: %v\n%s", method, path, err, raw)
		}
	}
	if ct := rec.Header().Get("Content-Type"); raw != "" && !strings.HasPrefix(ct, "application/json") {
		f.t.Fatalf("%s %s: content type %q", method, path, ct)
	}
	return rec.Code, out, raw
}

const base = "/api/spaces/default"

func (f *fixture) create(folder string, meta map[string]any, body string) map[string]any {
	f.t.Helper()
	code, r, raw := f.do("POST", base+"/pages", map[string]any{"folder": folder, "meta": meta, "body": body})
	if code != 201 {
		f.t.Fatalf("create: %d %s", code, raw)
	}
	return r
}

func wantError(t *testing.T, code int, r map[string]any, raw string, status int, errCode string) {
	t.Helper()
	if code != status || r["error"] != errCode || r["message"] == "" || r["message"] == nil {
		t.Fatalf("want %d %s with a message, got %d %s", status, errCode, code, raw)
	}
}

var rfc3339 = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`)

func TestMetaAndRouting(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, r, raw := f.do("GET", "/api/meta", nil)
	if code != 200 || r["version"] != "test-1" || raw != "{\"version\":\"test-1\",\"maxUpload\":10485760,\"maxImport\":104857600,\"spaces\":[{\"id\":\"default\",\"name\":\"测试空间\",\"role\":\"owner\",\"permissions\":[\"read\",\"write\",\"members\",\"manage\"],\"personal\":true,\"sync\":\"local\",\"inbox\":\"Inbox\",\"disabled\":false,\"directRole\":\"owner\",\"viaGroups\":false}]}\n" {
		t.Fatalf("%d %s", code, raw)
	}
	code, r, raw = f.do("GET", "/api/spaces/other/pages", nil)
	wantError(t, code, r, raw, 404, "not_found")
	code, r, raw = f.do("GET", "/api/spaces/other/tree", nil)
	wantError(t, code, r, raw, 404, "not_found")
	code, r, raw = f.do("GET", "/api/nothing", nil)
	wantError(t, code, r, raw, 404, "not_found")
	code, r, raw = f.do("GET", "/", nil)
	wantError(t, code, r, raw, 404, "not_found")
	code, r, raw = f.do("PATCH", base+"/pages", nil)
	wantError(t, code, r, raw, 405, "invalid")
	code, r, raw = f.do("DELETE", base+"/tree", nil)
	wantError(t, code, r, raw, 405, "invalid")
}

func TestHostRestriction(t *testing.T) {
	t.Parallel()
	f := newFixture(t, func(o *api.Options) { o.RestrictHost = true })
	for host, want := range map[string]int{"localhost:8080": 200, "127.0.0.1:1": 200, "[::1]:9": 200, "app.localhost": 200, "evil.example.com": 403, "evil.example.com:8080": 403, "10.0.0.5": 403} {
		req := httptest.NewRequest("GET", "/api/meta", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q: %d", host, rec.Code)
		}
	}
}

func TestCreatePage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// Sent as text so the order of meta is the one a client chose.
	code, r, raw := f.do("POST", base+"/pages", `{"folder":"Tang","meta":{"id":"CLIENTID00","created":"1999-01-01","title":"暮雪","tags":["玉兰"],"series":"冬季"},"body":"你听得见么？\n"}`)
	if code != 201 {
		t.Fatalf("%d %s", code, raw)
	}
	id := r["id"].(string)
	if len(id) != 10 || id == "CLIENTID00" || r["path"] != "Tang/暮雪.md" || r["folder"] != "Tang" || r["body"] != "你听得见么？\n" {
		t.Fatalf("%s", raw)
	}
	if len(r["rev"].(string)) != 40 || !rfc3339.MatchString(r["updated"].(string)) {
		t.Fatalf("rev/updated: %s", raw)
	}
	meta := r["meta"].(map[string]any)
	if meta["id"] != id || meta["created"] != time.Now().UTC().Format("2006-01-02") || meta["series"] != "冬季" {
		t.Fatalf("meta: %v", meta)
	}
	// Field order is the file's: id first, then as given, created after tags.
	if !strings.Contains(raw, `"meta":{"id":"`+id+`","title":"暮雪","tags":["玉兰"],"created":"`) {
		t.Fatalf("meta order: %s", raw)
	}
	// All fields are optional, with or without a body.
	for _, body := range []any{"{}", nil, map[string]any{"meta": nil}} {
		code, r, raw = f.do("POST", base+"/pages", body)
		if code != 201 || r["folder"] != "" || !regexp.MustCompile(`^\d{4}-\d\d-\d\d-[0-9a-z]{4}\.md$`).MatchString(r["path"].(string)) {
			t.Fatalf("%v: %d %s", body, code, raw)
		}
	}
	// The untitled page has no title in meta.
	if _, has := r["meta"].(map[string]any)["title"]; has {
		t.Fatalf("untitled page has a title: %s", raw)
	}
}

func TestCreateRejections(t *testing.T) {
	t.Parallel()
	f := newFixture(t, func(o *api.Options) { o.MaxBody = 2048 })
	cases := []struct {
		name   string
		body   any
		extra  []string
		status int
	}{
		{"traversal", `{"folder":"../escape"}`, nil, 400},
		{"absolute", `{"folder":"/etc"}`, nil, 400},
		{"hidden", `{"folder":".git"}`, nil, 400},
		{"not json", `{nope`, nil, 400},
		{"trailing data", `{} {}`, nil, 400},
		{"wrong type", `{"folder": 5}`, nil, 400},
		{"meta not object", `{"meta": [1]}`, nil, 400},
		{"duplicate meta key", `{"meta": {"a":1,"a":2}}`, nil, 400},
		{"invalid utf-8", []byte("{\"body\":\"bad \xff byte\"}"), nil, 400},
		{"wrong content type", `{}`, []string{"Content-Type", "text/plain"}, 415},
		{"no content type", `{}`, []string{"Content-Type", ""}, 415},
		{"too large", `{"body":"` + strings.Repeat("x", 3000) + `"}`, nil, 413},
	}
	for _, c := range cases {
		code, r, raw := f.do("POST", base+"/pages", c.body, c.extra...)
		if code != c.status || r["error"] != "invalid" {
			t.Errorf("%s: %d %s", c.name, code, raw)
		}
	}
	if code, r, _ := f.do("GET", base+"/pages", nil); code != 200 || len(r["items"].([]any)) != 0 {
		t.Fatalf("a refused create left a page behind: %v", r)
	}
	if entries, _ := os.ReadDir(filepath.Dir(f.dir)); len(entries) > 1 {
		t.Fatalf("wrote outside the space")
	}
}

func TestGetPage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	p := f.create("", map[string]any{"title": "T"}, "body\n")
	code, r, raw := f.do("GET", base+"/pages/"+p["id"].(string), nil)
	if code != 200 || r["rev"] != p["rev"] || r["body"] != "body\n" {
		t.Fatalf("%d %s", code, raw)
	}
	for _, id := range []string{"0000000000", "short", "01JD3K8Q7U", "..%2F..%2Fetc"} {
		code, r, raw := f.do("GET", base+"/pages/"+id, nil)
		wantError(t, code, r, raw, 404, "not_found")
	}
	if code, _, _ := f.do("HEAD", base+"/pages/"+p["id"].(string), nil); code != 200 {
		t.Fatalf("HEAD: %d", code)
	}
}

func TestSavePage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	p := f.create("Tang", map[string]any{"title": "旧名"}, "v1\n")
	id, path := p["id"].(string), base+"/pages/"+p["id"].(string)

	// Fields already in the file keep the file's order, new ones follow in
	// request order; a new title renames. created was left out, so it is kept.
	body := `{"baseRev":"` + p["rev"].(string) + `","meta":{"zeta":[1,2],"id":"` + id + `","title":"新名","alpha":{"b":1,"a":2}},"body":"v2\n"}`
	code, r, raw := f.do("PUT", path, body)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if r["id"] != id || r["path"] != "Tang/新名.md" || r["rev"] == p["rev"] || r["body"] != "v2\n" {
		t.Fatalf("%s", raw)
	}
	if !strings.Contains(raw, `"meta":{"id":"`+id+`","title":"新名","created":"`+time.Now().UTC().Format("2006-01-02")+`","zeta":[1,2],"alpha":{"b":1,"a":2}}`) {
		t.Fatalf("meta order: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "Tang", "旧名.md")); err == nil {
		t.Fatal("old file still there")
	}
	// meta.id may be left out.
	code, r2, raw := f.do("PUT", path, map[string]any{"baseRev": r["rev"], "meta": map[string]any{"title": "新名"}, "body": "v3\n"})
	if code != 200 || r2["id"] != id || r2["meta"].(map[string]any)["id"] != id {
		t.Fatalf("%d %s", code, raw)
	}

	// A stale baseRev is a conflict that carries the current page.
	code, r3, raw := f.do("PUT", path, map[string]any{"baseRev": p["rev"], "meta": map[string]any{"title": "新名"}, "body": "mine\n"})
	wantError(t, code, r3, raw, 409, "conflict")
	cur := r3["current"].(map[string]any)
	if cur["rev"] != r2["rev"] || cur["body"] != "v3\n" || cur["id"] != id || cur["path"] != "Tang/新名.md" {
		t.Fatalf("current: %s", raw)
	}
	// A missing one is a malformed request: there is nothing to compare against.
	code, r3, raw = f.do("PUT", path, map[string]any{"meta": map[string]any{}, "body": "x"})
	wantError(t, code, r3, raw, 400, "invalid")

	// id mismatch, bad UTF-8, wrong types: 400 and nothing written.
	code, r3, raw = f.do("PUT", path, map[string]any{"baseRev": r2["rev"], "meta": map[string]any{"id": "ZZZZZZZZZZ"}, "body": "x"})
	wantError(t, code, r3, raw, 400, "invalid")
	code, r3, raw = f.do("PUT", path, []byte("{\"baseRev\":\""+r2["rev"].(string)+"\",\"body\":\"\xff\"}"))
	wantError(t, code, r3, raw, 400, "invalid")
	code, r3, raw = f.do("PUT", path, `{"baseRev": 5}`)
	wantError(t, code, r3, raw, 400, "invalid")
	code, r3, raw = f.do("PUT", path, `{"baseRev":"x"}`, "Content-Type", "text/plain")
	wantError(t, code, r3, raw, 415, "invalid")
	code, _, _ = f.do("GET", path, nil)
	if code != 200 {
		t.Fatal(code)
	}
	if got, _ := f.getBody(id); got != "v3\n" {
		t.Fatalf("a refused save changed the page: %q", got)
	}
	code, r3, raw = f.do("PUT", base+"/pages/0000000000", map[string]any{"baseRev": "x", "meta": map[string]any{}, "body": ""})
	wantError(t, code, r3, raw, 404, "not_found")

	// Leaving out the header or the body must not empty the page.
	for _, partial := range []map[string]any{
		{"baseRev": "x"},
		{"baseRev": "x", "body": "only body"},
		{"baseRev": "x", "meta": map[string]any{"title": "only meta"}},
	} {
		code, r3, raw = f.do("PUT", path, partial)
		wantError(t, code, r3, raw, 400, "invalid")
	}
	if got, _ := f.getBody(id); got != "v3\n" {
		t.Fatalf("a partial save changed the page: %q", got)
	}
}

func (f *fixture) getBody(id string) (string, string) {
	_, r, _ := f.do("GET", base+"/pages/"+id, nil)
	return r["body"].(string), r["rev"].(string)
}

func TestMoveAndDeletePage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	p := f.create("", map[string]any{"title": "T"}, "")
	id := p["id"].(string)
	code, r, raw := f.do("POST", base+"/pages/"+id+"/move", map[string]any{"folder": "随笔/2026"})
	if code != 200 || r["path"] != "随笔/2026/T.md" || r["folder"] != "随笔/2026" || r["id"] != id {
		t.Fatalf("%d %s", code, raw)
	}
	_, tree, _ := f.do("GET", base+"/tree", nil)
	if fmt.Sprint(tree["folders"]) != "[map[pages:0 path:] map[pages:0 path:随笔] map[pages:1 path:随笔/2026]]" {
		t.Fatalf("%v", tree)
	}
	for _, folder := range []string{"../x", "/abs", ".git", "a//b"} {
		code, r, raw = f.do("POST", base+"/pages/"+id+"/move", map[string]any{"folder": folder})
		wantError(t, code, r, raw, 400, "invalid")
	}
	code, r, raw = f.do("POST", base+"/pages/0000000000/move", map[string]any{"folder": "x"})
	wantError(t, code, r, raw, 404, "not_found")
	code, r, raw = f.do("POST", base+"/pages/"+id+"/move", `{"folder": 3}`)
	wantError(t, code, r, raw, 400, "invalid")

	code, _, raw = f.do("DELETE", base+"/pages/"+id, nil)
	if code != 204 || raw != "" {
		t.Fatalf("%d %q", code, raw)
	}
	code, r, raw = f.do("GET", base+"/pages/"+id, nil)
	wantError(t, code, r, raw, 404, "not_found")
	code, r, raw = f.do("DELETE", base+"/pages/"+id, nil)
	wantError(t, code, r, raw, 404, "not_found")
	if _, err := os.Stat(filepath.Join(f.dir, "随笔", "2026", "T.md")); err == nil {
		t.Fatal("file not removed")
	}
}

func TestFoldersTreeAndTags(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, r, raw := f.do("POST", base+"/folders", map[string]any{"path": "随笔/2027"})
	if code != 201 || r["path"] != "随笔/2027" {
		t.Fatalf("%d %s", code, raw)
	}
	code, r, raw = f.do("POST", base+"/folders", map[string]any{"path": "随笔/2027"})
	wantError(t, code, r, raw, 409, "exists")
	for _, p := range []string{"../x", "/x", "", ".hidden", "a//b", "assets"} {
		code, r, raw = f.do("POST", base+"/folders", map[string]any{"path": p})
		if p == "" {
			wantError(t, code, r, raw, 409, "exists") // the root is always there
			continue
		}
		wantError(t, code, r, raw, 400, "invalid")
	}
	f.create("", map[string]any{"title": "a", "tags": []string{"栀子", "玉兰"}}, "")
	f.create("随笔", map[string]any{"title": "b", "tags": []string{"栀子"}}, "")
	f.create("随笔", map[string]any{"title": "c", "tags": []string{"栀子", "黄昏"}}, "")
	_, tree, raw := f.do("GET", base+"/tree", nil)
	if raw != `{"folders":[{"path":"","pages":1},{"path":"随笔","pages":2},{"path":"随笔/2027","pages":0}]}`+"\n" {
		t.Fatalf("%s", raw)
	}
	_ = tree
	_, _, raw = f.do("GET", base+"/tags", nil)
	if raw != `{"tags":[{"tag":"栀子","pages":3},{"tag":"玉兰","pages":1},{"tag":"黄昏","pages":1}]}`+"\n" {
		t.Fatalf("%s", raw)
	}
	// With nothing there the lists are empty arrays, not null.
	g := newFixture(t)
	_, _, raw = g.do("GET", base+"/tags", nil)
	if raw != `{"tags":[]}`+"\n" {
		t.Fatalf("%s", raw)
	}
}

func TestListFilterSortAndPaginate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for i := 0; i < 12; i++ {
		meta := map[string]any{"tags": []string{"all"}}
		if i%3 != 0 { // every third page is untitled
			meta["title"] = fmt.Sprintf("标题%02d", 11-i)
		}
		if i%2 == 0 {
			meta["tags"] = []string{"all", "even"}
		}
		folder := []string{"", "a", "a/b"}[i%3]
		f.create(folder, meta, fmt.Sprintf("栀子花 第%d篇\n", i))
		time.Sleep(2 * time.Millisecond)
	}
	list := func(q string) ([]map[string]any, string) {
		t.Helper()
		code, r, raw := f.do("GET", base+"/pages"+q, nil)
		if code != 200 {
			t.Fatalf("%s: %d %s", q, code, raw)
		}
		var items []map[string]any
		for _, it := range r["items"].([]any) {
			items = append(items, it.(map[string]any))
		}
		return items, r["next"].(string)
	}
	// Default: newest first.
	items, next := list("")
	if len(items) != 12 || next != "" {
		t.Fatalf("%d %q", len(items), next)
	}
	for i := 1; i < len(items); i++ {
		if items[i-1]["updated"].(string) < items[i]["updated"].(string) {
			t.Fatal("not newest first")
		}
	}
	// Summary shape.
	first := items[0]
	for _, k := range []string{"id", "path", "folder", "label", "untitled", "tags", "type", "created", "updated", "excerpt"} {
		if _, ok := first[k]; !ok {
			t.Fatalf("summary lacks %q: %v", k, first)
		}
	}
	if !rfc3339.MatchString(first["updated"].(string)) {
		t.Fatalf("%v", first["updated"])
	}
	// Untitled pages carry no title key and a label cut from the body.
	untitled := 0
	for _, it := range items {
		if it["untitled"] == true {
			untitled++
			if _, has := it["title"]; has {
				t.Fatalf("untitled page with a title: %v", it)
			}
			if !strings.HasPrefix(it["label"].(string), "栀子花 第") {
				t.Fatalf("label %v", it["label"])
			}
		}
	}
	if untitled != 4 {
		t.Fatalf("%d untitled", untitled)
	}

	// Pagination with a cursor visits everything once.
	seen := map[string]bool{}
	q := "?limit=5&sort=title"
	var titles []string
	for n := 0; n < 10; n++ {
		items, next := list(q)
		for _, it := range items {
			if seen[it["id"].(string)] {
				t.Fatal("duplicate across pages")
			}
			seen[it["id"].(string)] = true
			titles = append(titles, fmt.Sprint(it["title"]))
		}
		if next == "" {
			break
		}
		q = "?limit=5&sort=title&cursor=" + next
	}
	if len(seen) != 12 {
		t.Fatalf("%d pages seen", len(seen))
	}
	// Title sort puts the untitled last.
	if got := strings.Join(titles[8:], ","); got != "<nil>,<nil>,<nil>,<nil>" {
		t.Fatalf("untitled not last: %v", titles)
	}
	if titles[0] != "标题00" {
		t.Fatalf("%v", titles)
	}
	items, _ = list("?sort=-title&limit=3")
	if items[0]["title"].(string) < items[1]["title"].(string) {
		t.Fatalf("descending title: %v", items)
	}

	// Filters.
	if items, _ = list("?folder="); len(items) != 4 {
		t.Fatalf("root only: %d", len(items))
	}
	if items, _ = list("?folder=a"); len(items) != 4 {
		t.Fatalf("folder a: %d", len(items))
	}
	if items, _ = list("?folder=a&recursive=true"); len(items) != 8 {
		t.Fatalf("a recursive: %d", len(items))
	}
	if items, _ = list("?folder=&recursive=true"); len(items) != 12 {
		t.Fatalf("root recursive: %d", len(items))
	}
	if items, _ = list("?tag=even"); len(items) != 6 {
		t.Fatalf("tag: %d", len(items))
	}
	if items, _ = list("?tag=even&tag=all&folder=a&recursive=true"); len(items) != 4 {
		t.Fatalf("tags and folder: %d", len(items))
	}
	if items, _ = list("?tag=even&tag=missing"); len(items) != 0 {
		t.Fatalf("all tags must match: %d", len(items))
	}
	// Search, 1 and 2 characters of Chinese, and relevance order with a limit.
	for _, q := range []string{"栀", "栀子", "栀子花", "第3篇", "%E7%AC%AC"} {
		if items, _ = list("?q=" + q); len(items) == 0 {
			t.Fatalf("search %q found nothing", q)
		}
	}
	if items, _ = list("?q=" + "栀子+第3篇"); len(items) != 1 {
		t.Fatalf("AND of two terms: %d", len(items))
	}
	if items, _ = list("?q=花栀"); len(items) != 0 {
		t.Fatalf("order matters in a phrase: %d", len(items))
	}
	items, next = list("?q=栀子&limit=5")
	if len(items) != 5 || next == "" {
		t.Fatalf("%d %q", len(items), next)
	}
	items2, _ := list("?q=栀子&limit=5&cursor=" + next)
	if len(items2) != 5 || items2[0]["id"] == items[0]["id"] {
		t.Fatal("relevance cursor")
	}
	if items, _ = list("?q=栀子&sort=-created&tag=even"); len(items) != 6 {
		t.Fatalf("search with sort and tag: %d", len(items))
	}
	if items, _ = list("?q=" + "，"); len(items) != 0 {
		t.Fatalf("punctuation-only query: %d", len(items))
	}

	// Bad parameters.
	for _, q := range []string{"?limit=0", "?limit=201", "?limit=-3", "?limit=abc", "?sort=name", "?sort=-", "?cursor=!!", "?recursive=maybe", "?folder=../x", "?folder=a/", "?cursor=" + next + "&sort=title"} {
		code, r, raw := f.do("GET", base+"/pages"+q, nil)
		if code != 400 || r["error"] != "invalid" {
			t.Errorf("%s: %d %s", q, code, raw)
		}
	}
	if items, _ = list("?limit=200"); len(items) != 12 {
		t.Fatal("limit 200")
	}
}

func TestReindexAndIndexFollowsExternalEdits(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.create("", map[string]any{"title": "A"}, "x")
	os.WriteFile(filepath.Join(f.dir, "ext.md"), []byte("---\nid: EXTERN4001\ntitle: 外来\n---\n外来的页\n"), 0o644)
	_, r, _ := f.do("GET", base+"/pages", nil)
	if len(r["items"].([]any)) != 1 {
		t.Fatal("the index should not see the file before a reindex")
	}
	code, r, raw := f.do("POST", base+"/reindex", nil)
	if code != 200 || r["pages"] != float64(2) {
		t.Fatalf("%d %s", code, raw)
	}
	if _, ok := r["ms"].(float64); !ok {
		t.Fatalf("%s", raw)
	}
	if code, r, _ = f.do("GET", base+"/pages?q=外来", nil); code != 200 || len(r["items"].([]any)) != 1 {
		t.Fatalf("%v", r)
	}
	if code, r, _ = f.do("GET", base+"/pages/EXTERN4001", nil); code != 200 || r["body"] != "外来的页\n" {
		t.Fatalf("%v", r)
	}
}
