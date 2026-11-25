package api_test

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
)

func (f *fixture) folders() string {
	f.t.Helper()
	_, r, _ := f.do("GET", base+"/tree", nil)
	var out []string
	for _, it := range r["folders"].([]any) {
		out = append(out, it.(map[string]any)["path"].(string))
	}
	return strings.Join(out, "|")
}

// The tree carries each folder's updated: the newest page in it or below it,
// in the format a page's own updated has, and none for an empty folder.
func TestTreeFolderUpdated(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.create("a", map[string]any{"title": "one"}, "x\n")
	deep := f.create("a/b/c", map[string]any{"title": "two"}, "y\n")
	f.do("POST", base+"/folders", map[string]any{"path": "empty"})
	_, l, _ := f.do("GET", base+"/pages?folder=a/b/c", nil) // the summaries' format
	p := l["items"].([]any)[0].(map[string]any)
	if p["id"] != deep["id"] {
		t.Fatalf("%v", p)
	}
	updated := map[string]any{}
	for _, it := range func() []any { _, r, _ := f.do("GET", base+"/tree", nil); return r["folders"].([]any) }() {
		m := it.(map[string]any)
		updated[m["path"].(string)] = m["updated"]
	}
	for _, path := range []string{"", "a", "a/b", "a/b/c"} {
		if updated[path] != p["updated"] {
			t.Fatalf("%q: %v, the newest page has %v (%v)", path, updated[path], p["updated"], updated)
		}
	}
	if u, ok := updated["empty"]; !ok || u != nil {
		t.Fatalf("an empty folder has no updated: %v", updated)
	}
}

func TestFolderMoveAndDelete(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.create("随笔/2026", map[string]any{"title": "一"}, "x\n")["id"].(string)
	b := f.create("随笔/2026/深", map[string]any{"title": "二"}, "y\n")["id"].(string)
	f.create("别处", map[string]any{"title": "三"}, "z\n")

	code, r, raw := f.do("POST", base+"/folders/move", map[string]any{"from": "随笔/2026", "to": "归档/2026"})
	if code != 200 || r["path"] != "归档/2026" {
		t.Fatalf("%d %s", code, raw)
	}
	_, p, _ := f.do("GET", base+"/pages/"+a, nil)
	if p["path"] != "归档/2026/一.md" || p["folder"] != "归档/2026" {
		t.Fatalf("%v", p)
	}
	_, p, _ = f.do("GET", base+"/pages/"+b, nil)
	if p["path"] != "归档/2026/深/二.md" {
		t.Fatalf("%v", p)
	}
	if got := f.folders(); got != "|别处|归档|归档/2026|归档/2026/深|随笔" {
		t.Fatalf("%s", got)
	}
	// A rename is a move to another name in the same place.
	code, r, raw = f.do("POST", base+"/folders/move", map[string]any{"from": "随笔", "to": "散文"})
	if code != 200 || r["path"] != "散文" {
		t.Fatalf("%d %s", code, raw)
	}

	for _, c := range []struct {
		body   map[string]any
		status int
		code   string
	}{
		{map[string]any{"from": "nope", "to": "x"}, 404, "not_found"},
		{map[string]any{"from": "归档", "to": "别处"}, 409, "exists"},
		{map[string]any{"from": "", "to": "x"}, 400, "invalid"},
		{map[string]any{"from": "归档", "to": "归档"}, 400, "invalid"},
		{map[string]any{"from": "归档", "to": "归档/2026/里面"}, 400, "invalid"},
		{map[string]any{"from": "归档", "to": "x.md"}, 400, "invalid"},
		{map[string]any{"from": "归档", "to": "assets"}, 400, "invalid"},
		{map[string]any{"from": "归档", "to": ".hidden"}, 400, "invalid"},
	} {
		code, r, raw := f.do("POST", base+"/folders/move", c.body)
		wantError(t, code, r, raw, c.status, c.code)
	}

	// Deleting: refused while pages are inside, unless they are to go too.
	del := func(q string) (int, map[string]any, string) { return f.do("DELETE", base+"/folders"+q, nil) }
	code, r, raw = del("?path=" + url.QueryEscape("归档"))
	wantError(t, code, r, raw, 409, "conflict")
	code, r, raw = del("?path=nope")
	wantError(t, code, r, raw, 404, "not_found")
	code, r, raw = del("?path=")
	wantError(t, code, r, raw, 400, "invalid")
	code, r, raw = del("?path=x&recursive=maybe")
	wantError(t, code, r, raw, 400, "invalid")
	if code, _, raw := del("?path=" + url.QueryEscape("散文")); code != 204 {
		t.Fatalf("an empty folder: %d %s", code, raw)
	}

	// A draft of a page in the folder goes with the page.
	_, pg, _ := f.do("GET", base+"/pages/"+a, nil)
	if code, _, raw := f.do("PUT", base+"/pages/"+a+"/draft", map[string]any{"baseRev": pg["rev"], "meta": pg["meta"], "body": "draft text\n"}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if code, _, raw := del("?recursive=true&path=" + url.QueryEscape("归档")); code != 204 {
		t.Fatalf("%d %s", code, raw)
	}
	code, r, raw = f.do("GET", base+"/pages/"+a, nil)
	wantError(t, code, r, raw, 404, "not_found")
	code, r, raw = f.do("GET", base+"/pages/"+b, nil)
	wantError(t, code, r, raw, 404, "not_found")
	if got := f.folders(); got != "|别处" {
		t.Fatalf("%s", got)
	}
	// The page is recreated under another id; nothing of the old draft answers.
	if code, _, _ := f.do("POST", base+"/pages/"+a+"/commit", map[string]any{"version": "x"}); code != 404 {
		t.Fatalf("a draft of a deleted page still answers: %d", code)
	}
}

func TestBatchPages(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.create("x", map[string]any{"title": "A"}, "1\n")["id"].(string)
	b := f.create("y", map[string]any{"title": "B"}, "2\n")["id"].(string)
	c := f.create("", map[string]any{"title": "C"}, "3\n")["id"].(string)

	for _, body := range []map[string]any{
		{"op": "copy", "ids": []string{a}},
		{"op": "move", "ids": []string{}, "folder": "z"},
		{"op": "move", "ids": []string{a, a}, "folder": "z"},
		{"op": "move", "ids": []string{"bad id"}, "folder": "z"},
		{"op": "move", "ids": []string{a}, "folder": "z.md"},
		{"op": "move", "ids": []string{a}, "folder": "../up"},
		{"op": "delete", "ids": nil},
	} {
		code, r, raw := f.do("POST", base+"/pages/batch", body)
		wantError(t, code, r, raw, 400, "invalid")
	}
	code, r, raw := f.do("POST", base+"/pages/batch", map[string]any{"op": "move", "ids": []string{a, "ZZZZZZZZZZ"}, "folder": "z"})
	wantError(t, code, r, raw, 404, "not_found")
	if _, p, _ := f.do("GET", base+"/pages/"+a, nil); p["folder"] != "x" {
		t.Fatalf("a refused batch moved a page: %v", p["folder"])
	}

	code, r, raw = f.do("POST", base+"/pages/batch", map[string]any{"op": "move", "ids": []string{a, b, c}, "folder": "z/深"})
	if code != 200 || r["pages"] != float64(3) {
		t.Fatalf("%d %s", code, raw)
	}
	for _, id := range []string{a, b, c} {
		if _, p, _ := f.do("GET", base+"/pages/"+id, nil); p["folder"] != "z/深" {
			t.Fatalf("%s is in %v", id, p["folder"])
		}
	}
	// Already there: counted, left alone. And to the root.
	code, r, raw = f.do("POST", base+"/pages/batch", map[string]any{"op": "move", "ids": []string{a}, "folder": "z/深"})
	if code != 200 || r["pages"] != float64(1) {
		t.Fatalf("%d %s", code, raw)
	}
	code, r, raw = f.do("POST", base+"/pages/batch", map[string]any{"op": "move", "ids": []string{c}, "folder": ""})
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}

	_, pg, _ := f.do("GET", base+"/pages/"+a, nil)
	if code, _, raw := f.do("PUT", base+"/pages/"+a+"/draft", map[string]any{"baseRev": pg["rev"], "meta": pg["meta"], "body": "draft\n"}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	code, r, raw = f.do("POST", base+"/pages/batch", map[string]any{"op": "delete", "ids": []string{a, "ZZZZZZZZZZ"}})
	wantError(t, code, r, raw, 404, "not_found")
	if code, _, _ := f.do("GET", base+"/pages/"+a, nil); code != 200 {
		t.Fatal("a refused batch deleted a page")
	}
	code, r, raw = f.do("POST", base+"/pages/batch", map[string]any{"op": "delete", "ids": []string{a, b}})
	if code != 200 || r["pages"] != float64(2) {
		t.Fatalf("%d %s", code, raw)
	}
	for _, id := range []string{a, b} {
		code, r, raw := f.do("GET", base+"/pages/"+id, nil)
		wantError(t, code, r, raw, 404, "not_found")
	}
	if code, _, _ := f.do("GET", base+"/pages/"+c, nil); code != 200 {
		t.Fatal("a page outside the batch is gone")
	}
	// One commit for a batch.
	f.commitNow()
	head := rg(t, f.dir, "rev-parse", "HEAD")
	d := f.create("", map[string]any{"title": "D"}, "4\n")["id"].(string)
	f.commitNow()
	before := rg(t, f.dir, "rev-parse", "HEAD")
	f.do("POST", base+"/pages/batch", map[string]any{"op": "move", "ids": []string{c, d}, "folder": "w"})
	f.commitNow()
	if n := rg(t, f.dir, "rev-list", "--count", before+"..HEAD"); n != "1" || head == before {
		t.Fatalf("%s commits for one batch", n)
	}
}

func TestTagColours(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.create("", map[string]any{"title": "A", "tags": []string{"栀子", "夏"}}, "x\n")
	put := func(tag string, body any) (int, map[string]any, string) {
		return f.do("PUT", base+"/tags/"+url.PathEscape(tag), body)
	}
	code, r, raw := put("栀子", map[string]any{"color": "rose"})
	if code != 200 || r["tag"] != "栀子" || r["color"] != "rose" {
		t.Fatalf("%d %s", code, raw)
	}
	if code, _, raw := put("没在用", map[string]any{"color": "blue"}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	for _, body := range []any{map[string]any{"color": "mauve"}, map[string]any{"color": 3}, map[string]any{"color": ""}} {
		code, r, raw := put("栀子", body)
		wantError(t, code, r, raw, 400, "invalid")
	}
	_, r, _ = f.do("GET", base+"/tags", nil)
	got := map[string]any{}
	for _, it := range r["tags"].([]any) {
		m := it.(map[string]any)
		got[m["tag"].(string)] = m["color"]
	}
	if got["栀子"] != "rose" || got["夏"] != nil || len(got) != 2 {
		t.Fatalf("%v", got)
	}
	code, r, raw = put("栀子", map[string]any{"color": nil})
	if code != 200 || r["tag"] != "栀子" {
		t.Fatalf("%d %s", code, raw)
	}
	if _, has := r["color"]; has {
		t.Fatalf("%s", raw)
	}
	_, r, _ = f.do("GET", base+"/tags", nil)
	for _, it := range r["tags"].([]any) {
		if _, has := it.(map[string]any)["color"]; has {
			t.Fatalf("%v", it)
		}
	}
	// The colours are a file of the repository.
	f.commitNow()
	if tree := rg(t, f.dir, "ls-tree", "-r", "--name-only", "HEAD"); !strings.Contains(tree, ".petalbook/tags.yml") {
		t.Fatalf("%s", tree)
	}
}

func TestListFilterParameters(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.create("", map[string]any{"title": "栀子"}, "玉兰 在正文\n")
	f.create("", map[string]any{"title": "玉兰"}, "栀子 在正文\n")
	count := func(q string) int {
		code, r, raw := f.do("GET", base+"/pages"+q, nil)
		if code != 200 {
			t.Fatalf("%s: %d %s", q, code, raw)
		}
		return len(r["items"].([]any))
	}
	today := time.Now().UTC().Format("2006-01-02")
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	q := "?q=" + url.QueryEscape("栀子")
	for query, want := range map[string]int{
		q:                          2,
		q + "&in=title":            1,
		"?updatedFrom=" + today:    2,
		"?updatedTo=" + today:      2,
		"?updatedFrom=" + tomorrow: 0,
		"?updatedTo=" + yesterday:  0,
		"?createdFrom=" + today:    2,
		"?createdTo=" + yesterday:  0,
		"?createdFrom=" + yesterday + "&createdTo=" + tomorrow + "&in=title&q=" + url.QueryEscape("玉兰"): 1,
	} {
		if got := count(query); got != want {
			t.Errorf("%s: %d, want %d", query, got, want)
		}
	}
	for _, bad := range []string{"?in=body", "?updatedFrom=yesterday", "?updatedTo=2026-13-01", "?createdFrom=2026/01/01", "?createdTo=1"} {
		code, r, raw := f.do("GET", base+"/pages"+bad, nil)
		wantError(t, code, r, raw, 400, "invalid")
	}
}

// In a space that follows a remote a folder move is one commit and one push,
// and when the push is refused the folder, the index and the clone are as before.
func TestFolderMoveInASpaceThatFollowsARemote(t *testing.T) {
	t.Parallel()
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
		one := c.do("POST", base+"/pages", map[string]any{"folder": "old/deep", "meta": map[string]any{"title": "One"}, "body": "1\n"}).want(t, 201, "").body["id"].(string)
		two := c.do("POST", base+"/pages", map[string]any{"folder": "old", "meta": map[string]any{"title": "Two"}, "body": "2\n"}).want(t, 201, "").body["id"].(string)
		move := func() result {
			return c.do("POST", base+"/folders/move", map[string]any{"from": "old", "to": "new/place"})
		}
		pathOf := func(id string) string {
			return c.do("GET", base+"/pages/"+id, nil).want(t, 200, "").body["path"].(string)
		}

		// Things in the folder that Git knows nothing of: a page nobody
		// committed, another file, an empty subfolder. An undo must leave them
		// exactly where they were.
		stray := map[string]string{"old/hand.md": "written by hand, no header\n", "old/deep/notes.txt": "a text file\n"}
		for rel, text := range stray {
			if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(filepath.Join(root, "old", "empty", "inner"), 0o755); err != nil {
			t.Fatal(err)
		}

		n.fail = true
		head := rg(t, root, "rev-parse", "HEAD")
		if r := move(); r.code != 503 || r.body["error"] != "unavailable" {
			t.Fatalf("%d %s", r.code, r.raw)
		}
		if pathOf(one) != "old/deep/One.md" || pathOf(two) != "old/Two.md" {
			t.Fatalf("after the undo: %s %s", pathOf(one), pathOf(two))
		}
		for rel, text := range stray {
			if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))); err != nil || string(b) != text {
				t.Fatalf("%s after the undo: %q %v", rel, b, err)
			}
		}
		if fi, err := os.Stat(filepath.Join(root, "old", "empty", "inner")); err != nil || !fi.IsDir() {
			t.Fatalf("the empty subfolder after the undo: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
			t.Fatalf("the new folder was left behind: %v", err)
		}
		if got := rg(t, root, "rev-parse", "HEAD"); got != head {
			t.Fatal("a commit was left behind")
		}
		// (The files put there by hand are untracked, as they were.)
		if out := rg(t, root, "status", "--porcelain", "--untracked-files=no"); out != "" {
			t.Fatalf("the clone is not clean: %q", out)
		}
		var folders []string
		for _, it := range c.do("GET", base+"/tree", nil).want(t, 200, "").body["folders"].([]any) {
			folders = append(folders, it.(map[string]any)["path"].(string))
		}
		if got := strings.Join(folders, "|"); got != "|old|old/deep|old/empty|old/empty/inner" {
			t.Fatalf("the tree after the undo: %s", got)
		}
		// A recursive delete that is refused: the pages are back, and so is
		// the (to Git, invisible) empty folder that went with them.
		if r := c.do("DELETE", base+"/folders?recursive=true&path=old", nil); r.code != 503 {
			t.Fatalf("%d %s", r.code, r.raw)
		}
		if pathOf(one) != "old/deep/One.md" || pathOf(two) != "old/Two.md" {
			t.Fatalf("after the undone delete: %s %s", pathOf(one), pathOf(two))
		}
		if fi, err := os.Stat(filepath.Join(root, "old", "empty", "inner")); err != nil || !fi.IsDir() {
			t.Fatalf("the empty subfolder after the undone delete: %v", err)
		}
		if out := rg(t, root, "status", "--porcelain", "--untracked-files=no"); out != "" {
			t.Fatalf("the clone is not clean: %q", out)
		}
		n.fail = false
		for rel := range stray {
			os.Remove(filepath.Join(root, filepath.FromSlash(rel)))
		}
		os.RemoveAll(filepath.Join(root, "old", "empty"))

		before := strings.Count(rg(t, remote, "rev-list", "main"), "\n")
		move().want(t, 200, "")
		if after := strings.Count(rg(t, remote, "rev-list", "main"), "\n"); after != before+1 {
			t.Fatalf("%d commits for one move", after-before)
		}
		tree := rg(t, remote, "ls-tree", "-r", "--name-only", "main")
		if !strings.Contains(tree, "new/place/deep/One.md") || !strings.Contains(tree, "new/place/Two.md") || strings.Contains(tree, "old/") {
			t.Fatalf("the remote has:\n%s", tree)
		}
		// Deleting a folder with its pages, and a batch, are pushed too.
		c.do("DELETE", base+"/folders?recursive=true&path="+url.QueryEscape("new/place/deep"), nil).want(t, 204, "")
		c.do("POST", base+"/pages/batch", map[string]any{"op": "move", "ids": []string{two}, "folder": "third"}).want(t, 200, "")
		c.do("PUT", base+"/tags/x", map[string]any{"color": "teal"}).want(t, 200, "")
		tree = rg(t, remote, "ls-tree", "-r", "--name-only", "main")
		if strings.Contains(tree, "One.md") || !strings.Contains(tree, "third/Two.md") || !strings.Contains(tree, ".petalbook/tags.yml") {
			t.Fatalf("the remote has:\n%s", tree)
		}
		// A page and a folder renamed to another spelling of their own names
		// reach the remote under the new names, and only those.
		c.do("POST", base+"/folders/move", map[string]any{"from": "third", "to": "Third"}).want(t, 200, "")
		cur := c.do("GET", base+"/pages/"+two, nil).want(t, 200, "")
		c.do("PUT", base+"/pages/"+two, map[string]any{"baseRev": cur.body["rev"], "meta": map[string]any{"id": two, "title": "two"}, "body": "2\n"}).want(t, 200, "")
		tree = rg(t, remote, "ls-tree", "-r", "--name-only", "main")
		if !strings.Contains(tree, "Third/two.md") || strings.Contains(tree, "third/") || strings.Contains(tree, "Two.md") {
			t.Fatalf("the remote has:\n%s", tree)
		}
		// And a refused respelling leaves the old one, with the page in it.
		n.fail = true
		if r := c.do("POST", base+"/folders/move", map[string]any{"from": "Third", "to": "THIRD"}); r.code != 503 {
			t.Fatalf("%d %s", r.code, r.raw)
		}
		n.fail = false
		if got := pathOf(two); got != "Third/two.md" {
			t.Fatalf("after the undone respelling: %s", got)
		}
		if out := rg(t, root, "status", "--porcelain", "--untracked-files=no"); out != "" {
			t.Fatalf("the clone is not clean: %q", out)
		}
		c.do("POST", base+"/folders/move", map[string]any{"from": "Third", "to": "third"}).want(t, 200, "")

		// Members only.
		stranger := a.addUser("mallory", pw, "member")
		stranger.do("POST", base+"/folders/move", map[string]any{"from": "third", "to": "x"}).want(t, 404, "not_found")
		stranger.do("POST", base+"/pages/batch", map[string]any{"op": "delete", "ids": []string{two}}).want(t, 404, "not_found")
		stranger.do("PUT", base+"/tags/x", map[string]any{"color": "red"}).want(t, 404, "not_found")
	})
}

// What the document says is refused, and that nothing changes then.
func TestBatchAndFilterRefusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.create("x", map[string]any{"title": "A"}, "a\n")["id"].(string)
	// A move must say where to; "" is the top level and is a place.
	if code, r, raw := f.do("POST", base+"/pages/batch", map[string]any{"op": "move", "ids": []string{a}}); code != 400 {
		wantError(t, code, r, raw, 400, "invalid")
	}
	if _, p, _ := f.do("GET", base+"/pages/"+a, nil); p["folder"] != "x" {
		t.Fatalf("the page moved: %v", p["folder"])
	}
	// A file where the folder should be.
	if err := os.WriteFile(filepath.Join(f.dir, "afile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, r, raw := f.do("POST", base+"/pages/batch", map[string]any{"op": "move", "ids": []string{a}, "folder": "afile"})
	wantError(t, code, r, raw, 400, "invalid")
	if code, _, raw := f.do("POST", base+"/pages/batch", map[string]any{"op": "move", "ids": []string{a}, "folder": ""}); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	// Days the index cannot compare are refused, not read as "no bound".
	for _, q := range []string{"updatedTo=1969-12-31", "updatedFrom=2300-01-01", "createdFrom=0001-01-01", "createdTo=9999-12-31", "updatedFrom=2262-01-01"} {
		code, r, raw := f.do("GET", base+"/pages?"+q, nil)
		wantError(t, code, r, raw, 400, "invalid")
	}
	for _, q := range []string{"updatedFrom=1970-01-01", "updatedTo=2261-12-31", "createdFrom=1970-01-01&createdTo=2261-12-31"} {
		if code, _, raw := f.do("GET", base+"/pages?"+q, nil); code != 200 {
			t.Fatalf("%s: %d %s", q, code, raw)
		}
	}
}

func TestTagColoursOfOnesOwn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.create("x", map[string]any{"title": "A", "tags": []string{"栀子"}}, "a\n")
	tagURL := base + "/tags/" + url.PathEscape("栀子")
	code, r, raw := f.do("PUT", tagURL, map[string]any{"color": "#A1b2C3"})
	if code != 200 || r["color"] != "#a1b2c3" {
		t.Fatalf("%d %s", code, raw)
	}
	_, l, raw := f.do("GET", base+"/tags", nil)
	if tags := l["tags"].([]any); len(tags) != 1 || tags[0].(map[string]any)["color"] != "#a1b2c3" {
		t.Fatalf("%s", raw)
	}
	for _, bad := range []any{"#abc", "#gggggg", "abcdef", "rgb(1,2,3)", "", 7} {
		code, r, raw := f.do("PUT", tagURL, map[string]any{"color": bad})
		wantError(t, code, r, raw, 400, "invalid")
	}
	if _, l, raw := f.do("GET", base+"/tags", nil); l["tags"].([]any)[0].(map[string]any)["color"] != "#a1b2c3" {
		t.Fatalf("a refused colour changed something: %s", raw)
	}
}
