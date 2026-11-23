package api_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func checksGet(t *testing.T, c caller, base, query string) (counts map[string]any, items []map[string]any, next string) {
	t.Helper()
	code, r := c("GET", base+"/checks"+query, nil)
	if code != 200 {
		t.Fatalf("GET checks%s: %d %v", query, code, r)
	}
	counts, _ = r["counts"].(map[string]any)
	for _, it := range r["items"].([]any) {
		items = append(items, it.(map[string]any))
	}
	next, _ = r["next"].(string)
	return counts, items, next
}

func checkPaths(items []map[string]any) string {
	var out []string
	for _, it := range items {
		out = append(out, it["kind"].(string)+" "+it["path"].(string))
	}
	return strings.Join(out, ", ")
}

func writeRaw(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// checksScenario is what every role and both modes do with the check page.
func checksScenario(t *testing.T, base, root string, owner, editor, viewer caller, refusals bool) {
	t.Helper()
	counts, items, _ := checksGet(t, viewer, base, "")
	if len(items) != 0 || len(counts) != 8 {
		t.Fatalf("a new space has problems: %v %v", counts, items)
	}
	for k, n := range counts {
		if n != float64(0) {
			t.Errorf("count %s = %v", k, n)
		}
	}

	// A type, a folder bound to it, and pages of every kind.
	if code, r := owner("PUT", base+"/types/task", map[string]any{"title": "Task", "fields": []any{
		map[string]any{"key": "status", "kind": "select", "options": []any{"todo", "done"}},
		map[string]any{"key": "points", "kind": "number"},
	}}); code != 200 {
		t.Fatalf("type: %d %v", code, r)
	}
	if code, r := editor("POST", base+"/folders", map[string]any{"path": "Work"}); code != 201 {
		t.Fatalf("folder: %d %v", code, r)
	}
	if code, r := owner("PUT", base+"/folders/type", map[string]any{"folder": "Work", "type": "task"}); code != 200 {
		t.Fatalf("bind: %d %v", code, r)
	}
	mk := func(folder string, meta map[string]any, body string) map[string]any {
		t.Helper()
		code, r := editor("POST", base+"/pages", map[string]any{"folder": folder, "meta": meta, "body": body, "template": ""})
		if code != 201 {
			t.Fatalf("create %v: %d %v", meta, code, r)
		}
		return r
	}
	a := mk("Work", map[string]any{"title": "Alpha", "status": "todo"}, "fine\n")
	b := mk("Work", map[string]any{"title": "Beta", "status": "later", "points": "many", "colour": "red"}, "body of beta\n")
	mk("", map[string]any{"title": "Same"}, "one\n")
	mk("", map[string]any{"title": "Same"}, "two\n")
	e := mk("", map[string]any{"title": "Pictures"}, "![x](/assets/"+a["id"].(string)+"/gone.png)\n")
	aid, bid := a["id"].(string), b["id"].(string)
	orphan := "---\ntitle: Orphan\ncustom: 1\n---\nthe body\n"
	writeRaw(t, root, "noid.md", orphan)
	writeRaw(t, root, "broken.md", "---\ntitle: [x\n---\nbroken\n")
	writeRaw(t, root, "twin.md", "---\nid: "+aid+"\ntitle: Twin\n---\nx\n")
	if code, r := owner("POST", base+"/reindex", nil); code != 200 {
		t.Fatalf("reindex: %d %v", code, r)
	}

	counts, items, next := checksGet(t, viewer, base, "")
	want := map[string]float64{"no_id": 1, "duplicate_id": 1, "bad_header": 1, "type_mismatch": 1, "not_in_type": 1, "broken_link": 0, "missing_asset": 1, "duplicate_title": 2}
	for k, n := range want {
		if counts[k] != n {
			t.Errorf("count %s = %v, want %v", k, counts[k], n)
		}
	}
	if next != "" {
		t.Errorf("one page has a cursor: %q", next)
	}
	if got := checkPaths(items); got != "no_id noid.md, duplicate_id twin.md, bad_header broken.md, type_mismatch Work/Beta.md, not_in_type Work/Beta.md, missing_asset Pictures.md, duplicate_title Same 2.md, duplicate_title Same.md" &&
		got != "no_id noid.md, duplicate_id twin.md, bad_header broken.md, type_mismatch Work/Beta.md, not_in_type Work/Beta.md, missing_asset Pictures.md, duplicate_title Same.md, duplicate_title Same 2.md" {
		t.Fatalf("items: %s", got)
	}
	by := map[string]map[string]any{}
	for _, it := range items {
		by[it["kind"].(string)] = it
		if it["message"] == "" || it["message"] == nil {
			t.Errorf("no message: %v", it)
		}
		if _, ok := it["fix"]; !ok {
			t.Errorf("fix is always there: %v", it)
		}
		if r, _ := it["rev"].(string); len(r) != 40 {
			t.Errorf("every problem has the rev of its file: %v", it)
		}
	}
	rev := func(it map[string]any) string { return it["rev"].(string) }
	if by["no_id"]["fix"] != "add_id" || len(rev(by["no_id"])) != 40 || by["no_id"]["id"] != nil || by["no_id"]["detail"] != nil {
		t.Errorf("no_id: %v", by["no_id"])
	}
	if by["bad_header"]["fix"] != nil || !reflect.DeepEqual(by["bad_header"]["detail"], map[string]any{"line": float64(2)}) {
		t.Errorf("bad_header: %v", by["bad_header"])
	}
	if d := by["duplicate_id"]; d["id"] != aid || d["fix"] != nil || !reflect.DeepEqual(d["detail"], map[string]any{"paths": []any{"Work/Alpha.md", "twin.md"}}) {
		t.Errorf("duplicate_id: %v", d)
	}
	if d := by["type_mismatch"]; d["id"] != bid || d["fix"] != nil || !reflect.DeepEqual(d["detail"], map[string]any{"fields": []any{"points", "status"}}) && !reflect.DeepEqual(d["detail"], map[string]any{"fields": []any{"status", "points"}}) {
		t.Errorf("type_mismatch: %v", d)
	}
	if d := by["not_in_type"]; d["id"] != bid || d["fix"] != "remove_fields" || !reflect.DeepEqual(d["detail"], map[string]any{"fields": []any{"colour"}}) {
		t.Errorf("not_in_type: %v", d)
	}
	if d := by["missing_asset"]; d["id"] != e["id"] || d["fix"] != nil || !reflect.DeepEqual(d["detail"], map[string]any{"target": "/assets/" + aid + "/gone.png"}) {
		t.Errorf("missing_asset: %v", d)
	}
	if d := by["duplicate_title"]; d["fix"] != nil || !reflect.DeepEqual(d["detail"], map[string]any{"paths": []any{"Same 2.md", "Same.md"}}) && !reflect.DeepEqual(d["detail"], map[string]any{"paths": []any{"Same.md", "Same 2.md"}}) {
		t.Errorf("duplicate_title: %v", d)
	}

	// A kind filter, and refusals of bad input.
	if _, its, _ := checksGet(t, viewer, base, "?kind=duplicate_title"); len(its) != 2 {
		t.Errorf("kind filter: %v", checkPaths(its))
	}
	if code, r := viewer("GET", base+"/checks?kind=nope", nil); code != 400 || r["error"] != "invalid" {
		t.Errorf("unknown kind: %d %v", code, r)
	}
	if code, r := viewer("GET", base+"/checks?cursor=nope", nil); code != 400 || r["error"] != "invalid" {
		t.Errorf("bad cursor: %d %v", code, r)
	}
	if refusals {
		fix := map[string]any{"kind": "not_in_type", "path": "Work/Beta.md", "baseRev": "x"}
		if code, r := viewer("POST", base+"/checks/fix", fix); code != 403 || r["error"] != "no_permission" {
			t.Errorf("a viewer fixes: %d %v", code, r)
		}
	}

	// --- not_in_type: only the listed fields go, through the normal save.
	code, pg := editor("GET", base+"/pages/"+bid, nil)
	if code != 200 {
		t.Fatalf("%d %v", code, pg)
	}
	brev, header := pg["rev"].(string), pg["header"].(string)
	if by["not_in_type"]["rev"] != brev {
		t.Errorf("the rev listed is not the page's: %v, %s", by["not_in_type"], brev)
	}
	// The owner has a draft of the page; a fix keeps it, as a save does.
	if code, r := owner("PUT", base+"/pages/"+bid+"/draft", map[string]any{"baseRev": brev, "meta": pg["meta"], "body": "body of beta, edited\n"}); code != 200 {
		t.Fatalf("draft: %d %v", code, r)
	}
	if code, r := editor("POST", base+"/checks/fix", map[string]any{"kind": "not_in_type", "path": "Work/Beta.md", "baseRev": "0000000000000000000000000000000000000000"}); code != 409 || r["error"] != "conflict" || r["current"] == nil {
		t.Errorf("stale fix: %d %v", code, r)
	}
	for _, kind := range []string{"type_mismatch", "duplicate_id", "bad_header", "broken_link", "missing_asset", "duplicate_title", "nope", ""} {
		if code, r := editor("POST", base+"/checks/fix", map[string]any{"kind": kind, "path": "Work/Beta.md", "baseRev": brev}); code != 400 || r["error"] != "not_fixable" {
			t.Errorf("kind %q: %d %v", kind, code, r)
		}
	}
	if code, r := editor("POST", base+"/checks/fix", map[string]any{"kind": "not_in_type", "path": "Work/Beta.md"}); code != 400 || r["error"] != "invalid" {
		t.Errorf("no baseRev: %d %v", code, r)
	}
	if code, r := editor("POST", base+"/checks/fix", map[string]any{"kind": "not_in_type", "path": "Work/None.md", "baseRev": brev}); code != 404 {
		t.Errorf("no such page: %d %v", code, r)
	}
	code, fixed := editor("POST", base+"/checks/fix", map[string]any{"kind": "not_in_type", "path": "Work/Beta.md", "baseRev": brev})
	if code != 200 || fixed["id"] != bid || fixed["rev"] == brev || fixed["body"] != "body of beta\n" {
		t.Fatalf("fix not_in_type: %d %v", code, fixed)
	}
	if h := fixed["header"].(string); h != strings.Replace(header, "colour: red\n", "", 1) || strings.Contains(h, "colour") {
		t.Errorf("header was %q, is %q", header, h)
	}
	if m := fixed["meta"].(map[string]any); m["colour"] != nil || m["status"] != "later" || m["points"] != "many" {
		t.Errorf("meta: %v", m)
	}
	if _, r := owner("GET", base+"/pages/"+bid, nil); r["draft"] == nil {
		t.Errorf("the draft was dropped by a fix: %v", r)
	}
	counts, _, _ = checksGet(t, viewer, base, "")
	if counts["not_in_type"] != float64(0) || counts["type_mismatch"] != float64(1) {
		t.Errorf("counts after the fix: %v", counts)
	}
	// The same fix again: the page is as it should be, the rev has moved.
	if code, r := editor("POST", base+"/checks/fix", map[string]any{"kind": "not_in_type", "path": "Work/Beta.md", "baseRev": brev}); code != 409 {
		t.Errorf("fix on the old rev: %d %v", code, r)
	}

	// --- no_id: a new id in the header, the rest of the file as it was.
	nrev := rev(by["no_id"])
	if code, r := editor("POST", base+"/checks/fix", map[string]any{"kind": "no_id", "path": "noid.md", "baseRev": "0000000000000000000000000000000000000000"}); code != 409 || r["error"] != "conflict" {
		t.Errorf("stale no_id fix: %d %v", code, r)
	}
	if code, r := editor("POST", base+"/checks/fix", map[string]any{"kind": "no_id", "path": "broken.md", "baseRev": nrev}); code != 409 && code != 400 {
		t.Errorf("a broken header: %d %v", code, r)
	}
	if code, r := editor("POST", base+"/checks/fix", map[string]any{"kind": "no_id", "path": "../x.md", "baseRev": nrev}); code != 400 {
		t.Errorf("a path outside: %d %v", code, r)
	}
	if code, r := editor("POST", base+"/checks/fix", map[string]any{"kind": "no_id", "path": "nowhere.md", "baseRev": nrev}); code != 404 {
		t.Errorf("a file that is not there: %d %v", code, r)
	}
	code, made := editor("POST", base+"/checks/fix", map[string]any{"kind": "no_id", "path": "noid.md", "baseRev": nrev})
	nid, _ := made["id"].(string)
	if code != 200 || len(nid) != 10 || made["path"] != "noid.md" || made["body"] != "the body\n" || made["meta"].(map[string]any)["custom"] != float64(1) {
		t.Fatalf("fix no_id: %d %v", code, made)
	}
	data, err := os.ReadFile(filepath.Join(root, "noid.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "---\nid: "+nid+"\ntitle: Orphan\ncustom: 1\n---\nthe body\n" {
		t.Errorf("file: %q", data)
	}
	if code, r := viewer("GET", base+"/pages/"+nid, nil); code != 200 || r["title"] != nil && r["meta"].(map[string]any)["title"] != "Orphan" {
		t.Errorf("the page: %d %v", code, r)
	}
	counts, _, _ = checksGet(t, viewer, base, "")
	if counts["no_id"] != float64(0) {
		t.Errorf("no_id after the fix: %v", counts)
	}

	// --- counts follow saves that fix or make problems.
	code, ed := editor("GET", base+"/pages/"+e["id"].(string), nil)
	if code != 200 {
		t.Fatal(code)
	}
	if code, r := editor("PUT", base+"/pages/"+e["id"].(string), map[string]any{"baseRev": ed["rev"], "meta": ed["meta"], "body": "no picture now\n"}); code != 200 {
		t.Fatalf("save: %d %v", code, r)
	}
	code, al := editor("GET", base+"/pages/"+aid, nil)
	if code != 200 {
		t.Fatal(code)
	}
	meta := al["meta"].(map[string]any)
	meta["status"] = "nope"
	meta["extra"] = "x"
	if code, r := editor("PUT", base+"/pages/"+aid, map[string]any{"baseRev": al["rev"], "meta": meta, "body": al["body"]}); code != 200 {
		t.Fatalf("save: %d %v", code, r)
	}
	counts, items, _ = checksGet(t, viewer, base, "")
	if counts["missing_asset"] != float64(0) || counts["type_mismatch"] != float64(2) || counts["not_in_type"] != float64(1) {
		t.Errorf("counts after saves: %v", counts)
	}
	if _, its, _ := checksGet(t, viewer, base, "?kind=not_in_type"); checkPaths(its) != "not_in_type Work/Alpha.md" {
		t.Errorf("not_in_type: %s", checkPaths(its))
	}
	// A type that loses a field makes its pages' values stray.
	if code, r := owner("PUT", base+"/types/task", map[string]any{"title": "Task", "fields": []any{map[string]any{"key": "status", "kind": "select", "options": []any{"todo", "done", "later", "nope"}}}, "baseRev": typeRev(t, owner, base, "task")}); code != 200 {
		t.Fatalf("type: %d %v", code, r)
	}
	counts, _, _ = checksGet(t, viewer, base, "")
	if counts["type_mismatch"] != float64(0) || counts["not_in_type"] != float64(2) {
		t.Errorf("counts after the type changed: %v", counts)
	}
	// Deleting the binding takes the pages out of the type's judgement.
	if code, r := owner("PUT", base+"/folders/type", map[string]any{"folder": "Work", "type": ""}); code != 200 {
		t.Fatalf("unbind: %d %v", code, r)
	}
	counts, _, _ = checksGet(t, viewer, base, "")
	if counts["not_in_type"] != float64(0) || counts["type_mismatch"] != float64(0) {
		t.Errorf("counts after the binding went: %v", counts)
	}

	// --- paging: 50 to a page, in order, with a cursor.
	for i := 0; i < 60; i++ {
		writeRaw(t, root, fmt.Sprintf("bulk/p%02d.md", i), "---\ntitle: p\n---\nx\n")
	}
	if code, r := owner("POST", base+"/reindex", nil); code != 200 {
		t.Fatalf("reindex: %d %v", code, r)
	}
	counts, p1, next := checksGet(t, viewer, base, "?kind=no_id")
	if len(p1) != 50 || next == "" || counts["no_id"] != float64(60) {
		t.Fatalf("page 1: %d items, next %q, counts %v", len(p1), next, counts)
	}
	_, p2, next2 := checksGet(t, viewer, base, "?kind=no_id&cursor="+next)
	if len(p2) != 10 || next2 != "" || p1[0]["path"] != "bulk/p00.md" || p2[0]["path"] != "bulk/p50.md" || p2[9]["path"] != "bulk/p59.md" {
		t.Fatalf("page 2: %d items, next %q: %v", len(p2), next2, checkPaths(p2))
	}
	// Without a filter the cursor runs on into the next kinds.
	_, a1, n1 := checksGet(t, viewer, base, "")
	_, a2, n2 := checksGet(t, viewer, base, "?cursor="+n1)
	if len(a1) != 50 || len(a2) != 10+2+0 && len(a2) < 11 || n2 != "" {
		t.Errorf("unfiltered pages: %d, %d, %q: %s", len(a1), len(a2), n2, checkPaths(a2))
	}
}

func typeRev(t *testing.T, c caller, base, name string) string {
	t.Helper()
	_, r := c("GET", base+"/types", nil)
	for _, x := range r["types"].([]any) {
		if m := x.(map[string]any); m["name"] == name {
			return m["rev"].(string)
		}
	}
	t.Fatalf("no type %s", name)
	return ""
}

func TestChecksInAccountsMode(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		b := "/api/spaces/" + tm.sid
		checksScenario(t, b, filepath.Join(a.dir, "spaces", tm.sid), asClient(tm.owner), asClient(tm.editor), asClient(tm.viewer), true)
		if r := tm.outsider.do("GET", b+"/checks", nil); r.code != 404 {
			t.Errorf("an outsider reads: %d", r.code)
		}
		if r := tm.outsider.do("POST", b+"/checks/fix", map[string]any{"kind": "no_id", "path": "x.md", "baseRev": "x"}); r.code != 404 {
			t.Errorf("an outsider fixes: %d", r.code)
		}
	})
}

func TestChecksInSingleUserMode(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	c := asFixture(f)
	checksScenario(t, base, f.dir, c, c, c, false)
}
