package api_test

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// caller sends a request as one person and answers the status and the JSON body.
type caller func(method, path string, body any) (int, map[string]any)

func asClient(c *client) caller {
	return func(method, path string, body any) (int, map[string]any) {
		r := c.do(method, path, body)
		return r.code, r.body
	}
}

func asFixture(f *fixture) caller {
	return func(method, path string, body any) (int, map[string]any) {
		code, r, _ := f.do(method, path, body)
		return code, r
	}
}

func list(t *testing.T, c caller, base, query string) ([]map[string]any, string) {
	t.Helper()
	code, r := c("GET", base+"/pages?"+query, nil)
	if code != 200 {
		t.Fatalf("list %s: %d %v", query, code, r)
	}
	var items []map[string]any
	for _, it := range r["items"].([]any) {
		items = append(items, it.(map[string]any))
	}
	next, _ := r["next"].(string)
	return items, next
}

func titlesOf(items []map[string]any) string {
	var out []string
	for _, it := range items {
		out = append(out, it["title"].(string))
	}
	return strings.Join(out, " ")
}

// typesScenario is what every role and both modes do with types, the
// properties of pages and the table-view listing. In single-user mode the one
// caller has every permission, and the checks of refusals are left out.
func typesScenario(t *testing.T, base string, owner, editor, viewer caller, refusals bool) {
	t.Helper()
	spec := map[string]any{"title": "任务", "fields": []any{
		map[string]any{"key": "status", "title": "状态", "kind": "select", "options": []any{"todo", "doing", "done"}},
		map[string]any{"key": "points", "kind": "number"},
		map[string]any{"key": "due", "kind": "date"},
		map[string]any{"key": "labels", "kind": "multiselect", "options": []any{"a", "b"}},
	}}
	if refusals {
		for name, c := range map[string]caller{"editor": editor, "viewer": viewer} {
			code, r := c("PUT", base+"/types/task", spec)
			if code != 403 || r["error"] != "no_permission" || !strings.Contains(r["message"].(string), "manage") {
				t.Errorf("%s makes a type: %d %v", name, code, r)
			}
			if code, r := c("DELETE", base+"/types/task", nil); code != 403 || r["error"] != "no_permission" {
				t.Errorf("%s deletes a type: %d %v", name, code, r)
			}
			if code, r := c("PUT", base+"/folders/type", map[string]any{"folder": "", "type": "task"}); code != 403 || r["error"] != "no_permission" {
				t.Errorf("%s binds a folder: %d %v", name, code, r)
			}
		}
	}
	code, r := owner("PUT", base+"/types/task", spec)
	if code != 200 || r["name"] != "task" || r["title"] != "任务" {
		t.Fatalf("PUT type: %d %v", code, r)
	}
	trev, _ := r["rev"].(string)
	if len(trev) != 40 {
		t.Errorf("a type has the rev of its file: %v", r["rev"])
	}
	fields := r["fields"].([]any)
	f0 := fields[0].(map[string]any)
	if len(fields) != 4 || f0["key"] != "status" || f0["title"] != "状态" || f0["kind"] != "select" || !reflect.DeepEqual(f0["options"], []any{"todo", "doing", "done"}) ||
		!reflect.DeepEqual(f0["schema"], map[string]any{"title": "状态", "type": "string", "enum": []any{"todo", "doing", "done"}}) ||
		fields[1].(map[string]any)["kind"] != "number" || fields[1].(map[string]any)["title"] != "" {
		t.Errorf("fields: %v", fields)
	}
	if code, r := owner("PUT", base+"/types/bad:name", spec); code != 400 || r["error"] != "invalid" {
		t.Errorf("a bad name: %d %v", code, r)
	}
	if code, r := owner("PUT", base+"/types/x", map[string]any{"fields": []any{map[string]any{"key": "tags", "kind": "text"}}}); code != 400 || r["error"] != "invalid" {
		t.Errorf("a built-in key: %d %v", code, r)
	}
	// Anyone who can read lists them.
	for name, c := range map[string]caller{"owner": owner, "editor": editor, "viewer": viewer} {
		code, r := c("GET", base+"/types", nil)
		types, _ := r["types"].([]any)
		if code != 200 || len(types) != 1 || types[0].(map[string]any)["name"] != "task" || r["bindings"] == nil || len(r["bindings"].([]any)) != 0 {
			t.Errorf("%s lists types: %d %v", name, code, r)
		}
	}
	// A folder is bound; a page made there has no type in its header and has it in effect.
	if code, r := owner("PUT", base+"/folders/type", map[string]any{"folder": "Work", "type": "task"}); code != 404 || r["error"] != "not_found" {
		t.Errorf("a folder that is not there: %d %v", code, r)
	}
	if code, r := editor("POST", base+"/folders", map[string]any{"path": "Work/Sub"}); code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	if code, r := owner("PUT", base+"/folders/type", map[string]any{"folder": "Work", "type": "nothing"}); code != 400 {
		t.Errorf("a type that is not there: %d %v", code, r)
	}
	if code, r := owner("PUT", base+"/folders/type", map[string]any{"folder": "Work", "type": "task"}); code != 200 || r["folder"] != "Work" || r["type"] != "task" {
		t.Fatalf("%d %v", code, r)
	}
	if _, r := viewer("GET", base+"/types", nil); !reflect.DeepEqual(r["bindings"], []any{map[string]any{"folder": "Work", "type": "task"}}) {
		t.Errorf("bindings: %v", r["bindings"])
	}
	mk := func(folder, title string, meta map[string]any) map[string]any {
		t.Helper()
		meta["title"] = title
		code, r := editor("POST", base+"/pages", map[string]any{"folder": folder, "meta": meta, "body": "正文 " + title + "\n"})
		if code != 201 {
			t.Fatalf("create %s: %d %v", title, code, r)
		}
		return r
	}
	a := mk("Work", "A", map[string]any{"status": "done", "points": 5, "due": "2026-03-01", "labels": []any{"a"}})
	mk("Work", "B", map[string]any{"status": "todo", "points": 12, "due": "2026-01-01", "labels": []any{"b", "a"}})
	c := mk("Work/Sub", "C", map[string]any{"status": "doing", "points": 7.5})
	d := mk("Work", "D", map[string]any{"type": "memo"})
	e := mk("", "E", map[string]any{"status": "todo"})
	if a["effectiveType"] != "task" || c["effectiveType"] != "task" || d["effectiveType"] != "memo" || e["effectiveType"] != "" {
		t.Errorf("effectiveType: %v %v %v %v", a["effectiveType"], c["effectiveType"], d["effectiveType"], e["effectiveType"])
	}
	if _, has := a["meta"].(map[string]any)["type"]; has {
		t.Errorf("a new page got a type in its header: %v", a["meta"])
	}
	if code, g := viewer("GET", base+"/pages/"+a["id"].(string), nil); code != 200 || g["effectiveType"] != "task" {
		t.Errorf("the page: %d %v", code, g["effectiveType"])
	}

	// The listing: summaries carry effectiveType; props and rev only when asked.
	items, _ := list(t, viewer, base, "folder=Work&recursive=true&sort=title")
	if titlesOf(items) != "A B C D" || items[0]["effectiveType"] != "task" || items[3]["effectiveType"] != "memo" || items[0]["props"] != nil || items[0]["rev"] != nil {
		t.Errorf("plain listing: %v", items)
	}
	items, _ = list(t, viewer, base, "folder=Work&recursive=true&sort=title&props=1")
	if !reflect.DeepEqual(items[0]["props"], map[string]any{"status": "done", "points": float64(5), "due": "2026-03-01", "labels": []any{"a"}}) || items[0]["rev"] != a["rev"] ||
		!reflect.DeepEqual(items[3]["props"], map[string]any{}) {
		t.Errorf("props=1: %v", items)
	}
	// Sorting by a property, both ways, missing values last; pages by cursor.
	for q, want := range map[string]string{
		"sort=prop:points":  "A C B D",
		"sort=-prop:points": "B C A D",
		"sort=prop:due":     "B A C D",
		"sort=-prop:due":    "A B C D",
		"sort=prop:status":  "B C A D", // a select: the order of its options (todo, doing, done), not of the text
		"sort=prop:labels":  "A B C D",
		"sort=prop:zzz":     "A B C D",
	} {
		all, next := "", ""
		for i := 0; i < 10; i++ {
			qq := "folder=Work&recursive=true&limit=1&" + q
			if next != "" {
				qq += "&cursor=" + url.QueryEscape(next)
			}
			var items []map[string]any
			items, next = list(t, viewer, base, qq)
			all += titlesOf(items) + " "
			if next == "" {
				break
			}
		}
		if strings.TrimSpace(all) != want {
			t.Errorf("%s: %s, want %s", q, strings.TrimSpace(all), want)
		}
	}
	filter := func(js string) string { return "filter=" + url.QueryEscape(js) }
	for name, c := range map[string]struct{ q, want string }{
		"in":               {filter(`[{"key":"status","op":"in","values":["todo","doing"]}]`), "B C E"},
		"in, a list":       {filter(`[{"key":"labels","op":"in","values":["b"]}]`), "B"},
		"range":            {filter(`[{"key":"points","op":"range","values":[6,null]}]`), "B C"},
		"range, dates":     {filter(`[{"key":"due","op":"range","values":["2026-02-01","2026-12-31"]}]`), "A"},
		"empty":            {filter(`[{"key":"due","op":"empty"}]`), "C D E"},
		"notEmpty":         {filter(`[{"key":"due","op":"notEmpty"}]`), "A B"},
		"contains":         {filter(`[{"key":"status","op":"contains","values":["DO"]}]`), "A B C E"},
		"and, with a sort": {filter(`[{"key":"status","op":"in","values":["todo","done"]},{"key":"due","op":"notEmpty"}]`) + "&sort=-prop:points", "B A"},
	} {
		q := c.q
		if !strings.Contains(q, "sort=") {
			q += "&sort=title"
		}
		items, _ := list(t, viewer, base, "recursive=true&"+q)
		if got := titlesOf(items); got != c.want {
			t.Errorf("filter %s: %s, want %s", name, got, c.want)
		}
	}
	for name, q := range map[string]string{
		"filter=nope":     "filter=nope",
		"not an array":    filter(`{"key":"a"}`),
		"unknown member":  filter(`[{"key":"a","op":"empty","x":1}]`),
		"unknown op":      filter(`[{"key":"a","op":"like","values":["x"]}]`),
		"trailing":        filter(`[] []`),
		"range of one":    filter(`[{"key":"a","op":"range","values":[1]}]`),
		"props=maybe":     "props=maybe",
		"sort=prop: only": "sort=prop:",
	} {
		if code, r := viewer("GET", base+"/pages?"+q, nil); code != 400 || r["error"] != "invalid" {
			t.Errorf("%s: %d %v", name, code, r)
		}
	}

	// PATCH props.
	id := a["id"].(string)
	patch := func(c caller, id string, body any) (int, map[string]any) {
		return c("PATCH", base+"/pages/"+id+"/props", body)
	}
	if refusals {
		code, r := patch(viewer, id, map[string]any{"baseRev": a["rev"], "set": map[string]any{"status": "todo"}})
		if code != 403 || r["error"] != "no_permission" || !strings.Contains(r["message"].(string), "write") {
			t.Errorf("a viewer patches: %d %v", code, r)
		}
		if _, g := viewer("GET", base+"/pages/"+id, nil); g["rev"] != a["rev"] {
			t.Error("a refused patch changed the page")
		}
	}
	// The editor has a draft of the page with different text; a patch must not lose it.
	code, dr := editor("PUT", base+"/pages/"+id+"/draft", map[string]any{"baseRev": a["rev"], "meta": a["meta"], "body": "typed but not saved\n"})
	if code != 200 {
		t.Fatalf("draft: %d %v", code, dr)
	}
	code, p := patch(editor, id, map[string]any{"baseRev": a["rev"], "set": map[string]any{"status": "doing", "owner": "Ann"}, "unset": []any{"due"}})
	if code != 200 || p["rev"] == a["rev"] || p["effectiveType"] != "task" || p["body"] != a["body"] {
		t.Fatalf("patch: %d %v", code, p)
	}
	m := p["meta"].(map[string]any)
	if m["status"] != "doing" || m["owner"] != "Ann" || m["due"] != nil || m["title"] != "A" || m["points"] != float64(5) {
		t.Errorf("meta after the patch: %v", m)
	}
	if !strings.Contains(p["header"].(string), "owner: Ann") || strings.Contains(p["header"].(string), "due") {
		t.Errorf("header:\n%s", p["header"])
	}
	_, g := editor("GET", base+"/pages/"+id, nil)
	draft, _ := g["draft"].(map[string]any)
	if draft == nil || draft["body"] != "typed but not saved\n" || draft["stale"] != true {
		t.Errorf("the draft after a patch: %v", g["draft"])
	}
	// The listing sees the change at once.
	items, _ = list(t, viewer, base, "recursive=true&props=1&"+filter(`[{"key":"owner","op":"in","values":["Ann"]}]`))
	if len(items) != 1 || items[0]["rev"] != p["rev"] || items[0]["props"].(map[string]any)["status"] != "doing" {
		t.Errorf("%v", items)
	}
	// A stale baseRev is the ordinary conflict, with the current page.
	code, r = patch(editor, id, map[string]any{"baseRev": a["rev"], "set": map[string]any{"status": "done"}})
	if code != 409 || r["error"] != "conflict" || r["current"].(map[string]any)["rev"] != p["rev"] || r["current"].(map[string]any)["meta"].(map[string]any)["status"] != "doing" {
		t.Errorf("stale: %d %v", code, r)
	}
	// Malformed requests, built-in fields, pages that are not there.
	rev := p["rev"]
	for name, body := range map[string]any{
		"no baseRev":       map[string]any{"set": map[string]any{"x": 1}},
		"nothing to do":    map[string]any{"baseRev": rev},
		"a built-in key":   map[string]any{"baseRev": rev, "set": map[string]any{"title": "x"}},
		"unset a built-in": map[string]any{"baseRev": rev, "unset": []any{"tags"}},
		"set and unset":    map[string]any{"baseRev": rev, "set": map[string]any{"x": 1}, "unset": []any{"x"}},
		"not an object":    map[string]any{"baseRev": rev, "set": []any{1}},
	} {
		if code, r := patch(editor, id, body); code != 400 || r["error"] != "invalid" {
			t.Errorf("%s: %d %v", name, code, r)
		}
	}
	if code, r := patch(editor, "ZZZZZZZZZZ", map[string]any{"baseRev": rev, "set": map[string]any{"x": 1}}); code != 404 || r["error"] != "not_found" {
		t.Errorf("an unknown page: %d %v", code, r)
	}
	if code, r := patch(editor, "bad", map[string]any{"baseRev": rev, "set": map[string]any{"x": 1}}); code != 404 {
		t.Errorf("a bad id: %d %v", code, r)
	}
	if code, r := editor("PATCH", base+"/pages/"+id+"/props", "{not json"); code != 400 {
		t.Errorf("%d %v", code, r)
	}

	// A type can be edited: only the keys the server knows change.
	code, r = owner("PUT", base+"/types/task", map[string]any{"baseRev": trev, "title": "任务", "fields": []any{
		map[string]any{"key": "status", "title": "状态", "options": []any{"todo", "doing", "done", "dropped"}},
		map[string]any{"key": "points"},
	}})
	if code != 200 || len(r["fields"].([]any)) != 2 || !reflect.DeepEqual(r["fields"].([]any)[0].(map[string]any)["options"], []any{"todo", "doing", "done", "dropped"}) {
		t.Errorf("%d %v", code, r)
	}
	newRev, _ := r["rev"].(string)
	if newRev == trev || len(newRev) != 40 {
		t.Errorf("the rev did not change: %v", r["rev"])
	}
	// The old rev is stale now: 409 with the type as it is, for PUT and DELETE.
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PUT", "/types/task", map[string]any{"baseRev": trev, "title": "stale"}},
		{"PUT", "/types/task", map[string]any{"title": "no rev"}},
		{"DELETE", "/types/task?baseRev=" + trev, nil},
		{"DELETE", "/types/task", nil},
	} {
		code, r := owner(c.method, base+c.path, c.body)
		cur, _ := r["current"].(map[string]any)
		if code != 409 || r["error"] != "conflict" || cur["name"] != "task" || cur["rev"] != newRev {
			t.Errorf("%s %s: %d %v", c.method, c.path, code, r)
		}
	}
	if code, r := owner("PUT", base+"/types/fresh", map[string]any{"baseRev": "0123456789012345678901234567890123456789"}); code != 409 || r["error"] != "conflict" || r["current"] != nil {
		t.Errorf("a type that is not there, from a rev: %d %v", code, r)
	}
	// Deleting a type takes the bindings; the pages keep what their headers say.
	if code, _ := owner("DELETE", base+"/types/task?baseRev="+newRev, nil); code != 204 {
		t.Fatalf("%d", code)
	}
	if code, r := owner("DELETE", base+"/types/task", nil); code != 404 || r["error"] != "not_found" {
		t.Errorf("%d %v", code, r)
	}
	_, r = viewer("GET", base+"/types", nil)
	if len(r["types"].([]any)) != 0 || len(r["bindings"].([]any)) != 0 {
		t.Errorf("%v", r)
	}
	if _, g := viewer("GET", base+"/pages/"+d["id"].(string), nil); g["effectiveType"] != "memo" || g["meta"].(map[string]any)["type"] != "memo" {
		t.Errorf("%v", g)
	}
	if _, g := viewer("GET", base+"/pages/"+id, nil); g["effectiveType"] != "" {
		t.Errorf("%v", g["effectiveType"])
	}
}

func TestTypesAndTableViewInAccountsMode(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		typesScenario(t, "/api/spaces/"+tm.sid, asClient(tm.owner), asClient(tm.editor), asClient(tm.viewer), true)
		// A person who is not in the space does not find it.
		for _, rt := range [][2]string{{"GET", "/types"}, {"PUT", "/types/x"}, {"PUT", "/folders/type"}, {"PATCH", "/pages/ZZZZZZZZZZ/props"}} {
			if r := tm.outsider.do(rt[0], "/api/spaces/"+tm.sid+rt[1], "{}"); r.code != 404 {
				t.Errorf("%v: %d %s", rt, r.code, r.raw)
			}
		}
	})
}

func TestTypesAndTableViewInSingleUserMode(t *testing.T) {
	f := newFixture(t)
	c := asFixture(f)
	typesScenario(t, base, c, c, c, false)
	// Everything was written into the space's own files.
	if _, err := os.Stat(filepath.Join(f.dir, ".petalbook", "space.yml")); err != nil {
		t.Error(err)
	}
}

// A type file that was damaged by hand is listed with what is wrong, and is
// not overwritten by a request to change it.
func TestDamagedTypeFileIsReportedNotReplaced(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Join(f.dir, ".petalbook", "types"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.dir, ".petalbook", "types", "broken.json")
	if err := os.WriteFile(file, []byte(`{"properties": {`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, r, raw := f.do("GET", base+"/types", nil)
	types, _ := r["types"].([]any)
	if code != 200 || len(types) != 1 || !strings.Contains(fmt.Sprint(types[0].(map[string]any)["error"]), "fixed by hand") || len(types[0].(map[string]any)["fields"].([]any)) != 0 {
		t.Fatalf("%d %s", code, raw)
	}
	code, r, _ = f.do("PUT", base+"/types/broken", map[string]any{"fields": []any{map[string]any{"key": "a", "kind": "text"}}})
	if code != 400 || r["error"] != "invalid" || !strings.Contains(r["message"].(string), "fixed by hand") {
		t.Errorf("%d %v", code, r)
	}
	if b, _ := os.ReadFile(file); string(b) != `{"properties": {` {
		t.Errorf("the file was replaced: %s", b)
	}
}

// A summary says which properties the listing holds only in part, so that a
// table does not write a shortened copy back.
func TestListingSaysWhichPropsArePartial(t *testing.T) {
	f := newFixture(t)
	code, r, raw := f.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "P", "status": "todo",
		"note": strings.Repeat("字", 301), "obj": map[string]any{"a": 1}}, "body": "x\n"})
	if code != 201 {
		t.Fatalf("%d %s", code, raw)
	}
	want := []any{"note", "obj"}
	if !reflect.DeepEqual(r["partialProps"], want) {
		t.Errorf("create: %v", r["partialProps"])
	}
	id, rev := r["id"].(string), r["rev"].(string)
	// The page responses carry the same list, so a client has one judge.
	if code, g, _ := f.do("GET", base+"/pages/"+id, nil); code != 200 || !reflect.DeepEqual(g["partialProps"], want) {
		t.Errorf("get: %d %v", code, g["partialProps"])
	}
	code, pr, raw := f.do("PATCH", base+"/pages/"+id+"/props", map[string]any{"baseRev": rev, "set": map[string]any{"status": "doing"}})
	if code != 200 || !reflect.DeepEqual(pr["partialProps"], want) {
		t.Errorf("patch: %d %s", code, raw)
	}
	if code, c, _ := f.do("PATCH", base+"/pages/"+id+"/props", map[string]any{"baseRev": rev, "set": map[string]any{"status": "x"}}); code != 409 || !reflect.DeepEqual(c["current"].(map[string]any)["partialProps"], want) {
		t.Errorf("409: %d %v", code, c)
	}
	f.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Q", "status": "done"}, "body": "x\n"})
	code, r, raw = f.do("GET", base+"/pages?props=1&sort=title", nil)
	items, _ := r["items"].([]any)
	if code != 200 || len(items) != 2 {
		t.Fatalf("%d %s", code, raw)
	}
	p, q := items[0].(map[string]any), items[1].(map[string]any)
	if !reflect.DeepEqual(p["partialProps"], []any{"note", "obj"}) {
		t.Errorf("partialProps: %v", p["partialProps"])
	}
	if _, has := q["partialProps"]; has {
		t.Errorf("a page with nothing partial has no partialProps: %v", q)
	}
	if code, r, _ = f.do("GET", base+"/pages?sort=title", nil); code != 200 || strings.Contains(fmt.Sprint(r["items"]), "partialProps") {
		t.Errorf("without props=1: %d %v", code, r)
	}
}
