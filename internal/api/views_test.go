package api_test

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func viewsOf(t *testing.T, r map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	vs, ok := r["views"].([]any)
	if !ok {
		t.Fatalf("no views in %v", r)
	}
	for _, v := range vs {
		out = append(out, v.(map[string]any))
	}
	return out
}

func viewNames(vs []map[string]any) string {
	var out []string
	for _, v := range vs {
		out = append(out, v["name"].(string))
	}
	return strings.Join(out, ",")
}

// viewsScenario is what every role and both modes do with saved views, the
// groups of a board and the bodies of a listing. In single-user mode the one
// caller has every permission, and the refusals are left out.
func viewsScenario(t *testing.T, base string, owner, editor, viewer caller, refusals bool) {
	t.Helper()
	if code, r := owner("POST", base+"/folders", map[string]any{"path": "文案"}); code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	spec := map[string]any{"title": "文案", "fields": []any{
		map[string]any{"key": "status", "kind": "select", "options": []any{"草稿", "修改中", "定稿"}},
		map[string]any{"key": "points", "kind": "number"},
	}}
	if code, r := owner("PUT", base+"/types/wenan", spec); code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	if code, r := owner("PUT", base+"/folders/type", map[string]any{"folder": "文案", "type": "wenan"}); code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	mk := func(title, body string, meta map[string]any) map[string]any {
		t.Helper()
		meta["title"] = title
		code, r := editor("POST", base+"/pages", map[string]any{"folder": "文案", "meta": meta, "body": body})
		if code != 201 {
			t.Fatalf("create %s: %d %v", title, code, r)
		}
		return r
	}
	mk("甲", "第一篇\n", map[string]any{"status": "定稿", "points": 3})
	mk("乙", "第二篇\n", map[string]any{"status": "草稿", "points": 9})
	mk("丙", "第三篇\n", map[string]any{"status": "修改中"})
	mk("丁", "第四篇\n", map[string]any{"status": "已废弃"})
	mk("戊", "第五篇\n", map[string]any{})
	mk("己", "第六篇\n", map[string]any{"status": "草稿"})

	// --- the views of a folder
	code, r := viewer("GET", base+"/views?folder="+url.QueryEscape("文案"), nil)
	rev0, _ := r["rev"].(string)
	if code != 200 || len(viewsOf(t, r)) != 0 || len(rev0) != 40 {
		t.Fatalf("no views yet: %d %v", code, r)
	}
	board := map[string]any{"name": "按状态", "layout": "board", "group": "status", "sort": "-prop:points",
		"filter": []any{map[string]any{"key": "points", "op": "range", "values": []any{1, nil}}}}
	table := map[string]any{"name": "表格", "layout": "table", "hidden": []any{"points"}, "color": "teal"}
	put := func(c caller, body map[string]any) (int, map[string]any) {
		return c("PUT", base+"/views", body)
	}
	if refusals {
		code, r := put(viewer, map[string]any{"folder": "文案", "baseRev": rev0, "views": []any{board}})
		if code != 403 || r["error"] != "no_permission" || !strings.Contains(r["message"].(string), "write") {
			t.Errorf("a viewer saves views: %d %v", code, r)
		}
	}
	code, r = put(editor, map[string]any{"folder": "文案", "baseRev": rev0, "views": []any{board, table}})
	if code != 200 {
		t.Fatalf("PUT views: %d %v", code, r)
	}
	vs := viewsOf(t, r)
	rev1, _ := r["rev"].(string)
	if len(vs) != 2 || len(vs[0]["id"].(string)) != 6 || vs[0]["id"] == vs[1]["id"] || rev1 == rev0 || vs[0]["layout"] != "board" || vs[0]["group"] != "status" ||
		vs[1]["color"] != "teal" || !reflect.DeepEqual(vs[1]["hidden"], []any{"points"}) || vs[1]["sort"] != "" ||
		!reflect.DeepEqual(vs[0]["filter"], []any{map[string]any{"key": "points", "op": "range", "values": []any{float64(1), nil}}}) {
		t.Errorf("the saved views: %v", r)
	}
	// Anyone who can read gets them, the same.
	for name, c := range map[string]caller{"owner": owner, "editor": editor, "viewer": viewer} {
		code, got := c("GET", base+"/views?folder="+url.QueryEscape("文案"), nil)
		if code != 200 || !reflect.DeepEqual(got, r) {
			t.Errorf("%s reads: %d %v", name, code, got)
		}
	}
	// A save from an old rev is a conflict that shows how they are now.
	code, c := put(editor, map[string]any{"folder": "文案", "baseRev": rev0, "views": []any{}})
	cur, _ := c["current"].(map[string]any)
	if code != 409 || c["error"] != "conflict" || !reflect.DeepEqual(cur, r) {
		t.Errorf("a stale save: %d %v", code, c)
	}
	// Keep the second, rename the first, add one; ids come back for the kept ones.
	vs[0]["name"] = "看板"
	third := map[string]any{"name": "连读", "layout": "read"}
	code, r2 := put(owner, map[string]any{"folder": "文案", "baseRev": rev1, "views": []any{vs[1], vs[0], third}})
	vs2 := viewsOf(t, r2)
	if code != 200 || viewNames(vs2) != "表格,看板,连读" || vs2[0]["id"] != vs[1]["id"] || vs2[1]["id"] != vs[0]["id"] || vs2[2]["id"] == "" {
		t.Fatalf("a second save: %d %v", code, r2)
	}
	rev2, _ := r2["rev"].(string)
	// What is refused.
	for name, c := range map[string]struct {
		body map[string]any
		code int
		err  string
	}{
		"no baseRev":     {map[string]any{"folder": "文案", "views": []any{}}, 400, "invalid"},
		"no views":       {map[string]any{"folder": "文案", "baseRev": rev2}, 400, "invalid"},
		"no scope":       {map[string]any{"baseRev": rev2, "views": []any{}}, 400, "invalid"},
		"both scopes":    {map[string]any{"folder": "文案", "tag": "t", "baseRev": rev2, "views": []any{}}, 400, "invalid"},
		"a bad layout":   {map[string]any{"folder": "文案", "baseRev": rev2, "views": []any{map[string]any{"name": "v", "layout": "gantt"}}}, 400, "invalid"},
		"a bad filter":   {map[string]any{"folder": "文案", "baseRev": rev2, "views": []any{map[string]any{"name": "v", "layout": "list", "filter": []any{map[string]any{"key": "a", "op": "nope"}}}}}, 400, "invalid"},
		"a name of text": {map[string]any{"folder": "文案", "baseRev": rev2, "views": []any{map[string]any{"name": 5, "layout": "list"}}}, 400, "invalid"},
		"an unknown id":  {map[string]any{"folder": "文案", "baseRev": rev2, "views": []any{map[string]any{"id": "zzzzzz", "name": "v", "layout": "list"}}}, 400, "invalid"},
		"no folder":      {map[string]any{"folder": "没有", "baseRev": rev2, "views": []any{}}, 404, "not_found"},
		"too many":       {map[string]any{"folder": "文案", "baseRev": rev2, "views": manyViews(21)}, 400, "invalid"},
	} {
		if code, r := put(owner, c.body); code != c.code || r["error"] != c.err {
			t.Errorf("%s: %d %v", name, code, r)
		}
	}
	// The views did not change by any of it.
	if _, got := viewer("GET", base+"/views?folder="+url.QueryEscape("文案"), nil); !reflect.DeepEqual(got, r2) {
		t.Errorf("%v", got)
	}
	for _, q := range []string{"", "?folder=a&tag=b", "?folder=..%2Fx"} {
		if code, r := viewer("GET", base+"/views"+q, nil); code != 400 || r["error"] != "invalid" {
			t.Errorf("GET views%s: %d %v", q, code, r)
		}
	}
	// A folder that is not there has none.
	if code, r := viewer("GET", base+"/views?folder=Nowhere", nil); code != 200 || len(viewsOf(t, r)) != 0 {
		t.Errorf("%d %v", code, r)
	}
	// 20 views are fine.
	code, r20 := put(owner, map[string]any{"folder": "文案", "baseRev": rev2, "views": manyViews(20)})
	if code != 200 || len(viewsOf(t, r20)) != 20 {
		t.Errorf("20 views: %d", code)
	}
	if code, _ := put(owner, map[string]any{"folder": "文案", "baseRev": r20["rev"], "views": []any{table}}); code != 200 {
		t.Errorf("%d", code)
	}

	// --- the views of a tag, and of the root
	code, tr := viewer("GET", base+"/views?tag="+url.QueryEscape("栀子"), nil)
	if code != 200 || len(viewsOf(t, tr)) != 0 {
		t.Fatalf("%d %v", code, tr)
	}
	code, r = put(editor, map[string]any{"tag": "栀子", "baseRev": tr["rev"], "views": []any{map[string]any{"name": "连读", "layout": "read"}}})
	if code != 200 || viewNames(viewsOf(t, r)) != "连读" {
		t.Fatalf("a tag: %d %v", code, r)
	}
	if _, got := viewer("GET", base+"/views?tag="+url.QueryEscape("栀子"), nil); !reflect.DeepEqual(got, r) {
		t.Errorf("%v", got)
	}
	if code, c := put(editor, map[string]any{"tag": "栀子", "baseRev": tr["rev"], "views": []any{}}); code != 409 || !reflect.DeepEqual(c["current"], r) {
		t.Errorf("a stale tag save: %d %v", code, c)
	}
	if code, _ := put(editor, map[string]any{"folder": "", "baseRev": rev0, "views": []any{}}); code != 200 {
		// the root has no views, so the rev of an empty list is current
		t.Errorf("the root: %d", code)
	}
	if code, r := put(editor, map[string]any{"folder": "", "baseRev": rev0, "views": []any{map[string]any{"name": "all", "layout": "cards"}}}); code != 200 || viewNames(viewsOf(t, r)) != "all" {
		t.Errorf("the root: %d %v", code, r)
	}

	// --- the counts of the columns of a board
	g := func(c caller, query string) (int, map[string]any) {
		return c("GET", base+"/pages/groups?"+query, nil)
	}
	code, gr := g(viewer, "folder="+url.QueryEscape("文案")+"&key=status")
	// Most pages first, then by the text of the value.
	want := []any{map[string]any{"value": "草稿", "count": float64(2)}, map[string]any{"value": "修改中", "count": float64(1)},
		map[string]any{"value": "定稿", "count": float64(1)}, map[string]any{"value": "已废弃", "count": float64(1)}}
	if code != 200 || !reflect.DeepEqual(gr["groups"], want) || gr["empty"] != float64(1) || gr["truncated"] != false {
		t.Errorf("groups: %d %v", code, gr)
	}
	filt := url.QueryEscape(`[{"key":"points","op":"notEmpty"}]`)
	code, gr = g(editor, "folder="+url.QueryEscape("文案")+"&key=status&filter="+filt)
	if code != 200 || !reflect.DeepEqual(gr["groups"], []any{map[string]any{"value": "定稿", "count": float64(1)}, map[string]any{"value": "草稿", "count": float64(1)}}) || gr["empty"] != float64(0) {
		t.Errorf("groups with a filter: %d %v", code, gr)
	}
	code, gr = g(viewer, "folder="+url.QueryEscape("文案")+"&key=nothing")
	if code != 200 || !reflect.DeepEqual(gr["groups"], []any{}) || gr["empty"] != float64(6) {
		t.Errorf("a key nobody has: %d %v", code, gr)
	}
	for _, q := range []string{"folder=a", "key=status", "folder=a&key=s&recursive=maybe", "folder=a&key=s&filter=x", "folder=..%2Fx&key=s", "folder=a&key=s&filter=" + url.QueryEscape(`[{"key":"a","op":"nope"}]`)} {
		if code, r := g(viewer, q); code != 400 || r["error"] != "invalid" {
			t.Errorf("groups?%s: %d %v", q, code, r)
		}
	}
	// A tag counts the pages of the tag.
	if code, gr := g(viewer, "tag="+url.QueryEscape("栀子")+"&key=status"); code != 200 || gr["empty"] != float64(0) || len(gr["groups"].([]any)) != 0 {
		t.Errorf("a tag: %d %v", code, gr)
	}

	// --- the columns of a board are listings
	items, _ := list(t, viewer, base, "folder="+url.QueryEscape("文案")+"&limit=20&filter="+url.QueryEscape(`[{"key":"status","op":"notIn","values":["草稿","修改中","定稿"]}]`))
	if titlesOf(items) != "丁" {
		t.Errorf("the other values: %s", titlesOf(items))
	}
	items, _ = list(t, viewer, base, "folder="+url.QueryEscape("文案")+"&limit=20&filter="+url.QueryEscape(`[{"key":"status","op":"empty"}]`))
	if titlesOf(items) != "戊" {
		t.Errorf("empty: %s", titlesOf(items))
	}
	// A sort by a select follows its options: 草稿 修改中 定稿, the other values, then none.
	items, _ = list(t, viewer, base, "folder="+url.QueryEscape("文案")+"&sort=prop:status&limit=2")
	if len(items) != 2 {
		t.Fatal(items)
	}
	var all []string
	for next := "x"; next != ""; {
		q := "folder=" + url.QueryEscape("文案") + "&sort=prop:status&limit=2"
		if next != "x" {
			q += "&cursor=" + url.QueryEscape(next)
		}
		var page []map[string]any
		page, next = list(t, viewer, base, q)
		all = append(all, titlesOf(page))
	}
	if got := strings.Join(all, " "); got != "乙 己 丙 甲 丁 戊" {
		t.Errorf("sorted by the options: %s", got)
	}
	if items, _ = list(t, viewer, base, "folder="+url.QueryEscape("文案")+"&sort=-prop:status"); titlesOf(items) != "丁 甲 丙 己 乙 戊" {
		t.Errorf("reversed: %s", titlesOf(items))
	}
	// The cursor of the options is no cursor of the text order.
	_, next := list(t, viewer, base, "folder="+url.QueryEscape("文案")+"&sort=prop:status&limit=1")
	if code, r := viewer("GET", base+"/pages?sort=prop:status&limit=1&cursor="+url.QueryEscape(next), nil); code != 400 || r["error"] != "invalid" {
		t.Errorf("%d %v", code, r)
	}

	// --- bodies
	code, r = viewer("GET", base+"/pages?folder="+url.QueryEscape("文案")+"&sort=title&body=1", nil)
	its, _ := r["items"].([]any)
	if code != 200 || len(its) != 6 {
		t.Fatalf("body=1: %d %v", code, r)
	}
	bodies := map[string]string{}
	for _, it := range its {
		m := it.(map[string]any)
		b, has := m["body"].(string)
		if !has {
			t.Errorf("no body in %v", m)
		}
		bodies[m["title"].(string)] = b
	}
	if bodies["甲"] != "第一篇\n" || bodies["己"] != "第六篇\n" {
		t.Errorf("%v", bodies)
	}
	// Without it there is none; with props it still is the body of the file.
	code, r = viewer("GET", base+"/pages?folder="+url.QueryEscape("文案")+"&limit=1", nil)
	if m := r["items"].([]any)[0].(map[string]any); code != 200 || m["body"] != nil {
		t.Errorf("a body that was not asked for: %v", m)
	}
	code, r = viewer("GET", base+"/pages?folder="+url.QueryEscape("文案")+"&limit=20&body=1&props=1", nil)
	if m := r["items"].([]any)[0].(map[string]any); code != 200 || m["body"] == nil || m["rev"] == nil {
		t.Errorf("%v", m)
	}
	for _, q := range []string{"limit=21&body=1", "limit=200&body=1", "limit=0&body=1", "body=yes"} {
		if code, r := viewer("GET", base+"/pages?folder="+url.QueryEscape("文案")+"&"+q, nil); code != 400 || r["error"] != "invalid" {
			t.Errorf("%s: %d %v", q, code, r)
		}
	}
	// 20 at a time, by cursor, bodies of the next ones.
	for i := 0; i < 25; i++ {
		mk("页"+string(rune('A'+i%26))+string(rune('a'+i/26)), "正文\n", map[string]any{"status": "草稿"})
	}
	code, r = viewer("GET", base+"/pages?folder="+url.QueryEscape("文案")+"&sort=title&body=1", nil)
	if code != 200 || len(r["items"].([]any)) != 20 || r["next"] == "" {
		t.Errorf("the default of body=1 is 20: %d %d", code, len(r["items"].([]any)))
	}
	code, r = viewer("GET", base+"/pages?folder="+url.QueryEscape("文案")+"&sort=title&body=1&cursor="+url.QueryEscape(r["next"].(string)), nil)
	if code != 200 || len(r["items"].([]any)) != 11 || r["next"] != "" || r["items"].([]any)[0].(map[string]any)["body"] == nil {
		t.Errorf("the next: %d %d", code, len(r["items"].([]any)))
	}
}

func manyViews(n int) []any {
	var out []any
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{"name": "v" + string(rune('a'+i)), "layout": "list"})
	}
	return out
}

func TestViewsAndBoardsInAccountsMode(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		viewsScenario(t, "/api/spaces/"+tm.sid, asClient(tm.owner), asClient(tm.editor), asClient(tm.viewer), true)
		// A person who is not in the space does not find it.
		for _, rt := range [][3]string{{"GET", "/views?folder=a", ""}, {"PUT", "/views", "{}"}, {"GET", "/pages/groups?folder=a&key=s", ""}} {
			if r := tm.outsider.do(rt[0], "/api/spaces/"+tm.sid+rt[1], rt[2]); r.code != 404 {
				t.Errorf("%v: %d %s", rt, r.code, r.raw)
			}
		}
		// The views are in the space's file, and so are committed with it.
		if r := tm.viewer.do("GET", "/api/spaces/"+tm.sid+"/views?folder=%E6%96%87%E6%A1%88", nil); r.code != 200 {
			t.Errorf("%d", r.code)
		}
	})
}

func TestViewsAndBoardsInSingleUserMode(t *testing.T) {
	f := newFixture(t)
	c := asFixture(f)
	viewsScenario(t, base, c, c, c, false)
	// The views are written into the space's own files.
	y, err := os.ReadFile(filepath.Join(f.dir, ".petalbook", "space.yml"))
	if err != nil || !strings.Contains(string(y), "views:") || !strings.Contains(string(y), "layout: table") {
		t.Errorf("%v\n%s", err, y)
	}
	tags, err := os.ReadFile(filepath.Join(f.dir, ".petalbook", "tags.yml"))
	if err != nil || !strings.Contains(string(tags), "栀子") {
		t.Errorf("%v\n%s", err, tags)
	}
}

// A settings file damaged by hand is not overwritten by saving views, and the
// views it cannot give are none.
func TestViewsAreNotSavedOverDamagedSettings(t *testing.T) {
	f := newFixture(t)
	if code, r, _ := f.do("POST", base+"/folders", map[string]any{"path": "F"}); code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	file := filepath.Join(f.dir, ".petalbook", "space.yml")
	os.MkdirAll(filepath.Dir(file), 0o755)
	broken := []byte("name: x\nfolders: [unclosed\n")
	os.WriteFile(file, broken, 0o644)
	code, r, _ := f.do("GET", base+"/views?folder=F", nil)
	if code != 200 || len(viewsOf(t, r)) != 0 {
		t.Fatalf("%d %v", code, r)
	}
	code, r, _ = f.do("PUT", base+"/views", map[string]any{"folder": "F", "baseRev": r["rev"], "views": []any{map[string]any{"name": "v", "layout": "list"}}})
	if code != 400 || !strings.Contains(r["message"].(string), "fixed by hand") {
		t.Errorf("%d %v", code, r)
	}
	if got, _ := os.ReadFile(file); string(got) != string(broken) {
		t.Errorf("the file was written: %s", got)
	}
}

// The shape of a view, as a client reads it.
func TestAViewIsWrittenWithEveryKey(t *testing.T) {
	f := newFixture(t)
	f.do("POST", base+"/folders", map[string]any{"path": "F"})
	_, r, _ := f.do("GET", base+"/views?folder=F", nil)
	code, r, raw := f.do("PUT", base+"/views", map[string]any{"folder": "F", "baseRev": r["rev"], "views": []any{map[string]any{"name": "v", "layout": "list"}}})
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	var got struct {
		Views []map[string]json.RawMessage `json:"views"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	v := got.Views[0]
	for _, k := range []string{"id", "name", "layout", "sort", "group", "filter", "hidden"} {
		if _, ok := v[k]; !ok {
			t.Errorf("a view has no %s: %s", k, raw)
		}
	}
	if string(v["filter"]) != "[]" || string(v["hidden"]) != "[]" || string(v["sort"]) != `""` {
		t.Errorf("%s", raw)
	}
}
