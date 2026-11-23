package api_test

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func tplList(t *testing.T, c caller, base string) (names []string, defaults map[string]any, raw map[string]any) {
	t.Helper()
	code, r := c("GET", base+"/templates", nil)
	if code != 200 {
		t.Fatalf("list templates: %d %v", code, r)
	}
	for _, x := range r["templates"].([]any) {
		m := x.(map[string]any)
		if _, has := m["body"]; has {
			t.Errorf("the list carries a body: %v", m)
		}
		names = append(names, m["name"].(string))
	}
	return names, r["defaults"].(map[string]any), r
}

func tplPut(t *testing.T, c caller, base, name, baseRev string, meta map[string]any, body string) map[string]any {
	t.Helper()
	code, r := c("PUT", base+"/templates/"+url.PathEscape(name), map[string]any{"baseRev": baseRev, "meta": meta, "body": body})
	if code != 200 {
		t.Fatalf("put template %s: %d %v", name, code, r)
	}
	return r
}

func folderDefaults(d map[string]any) map[string]any { return d["folders"].(map[string]any) }

// templatesScenario is what every role and both modes do with templates.
func templatesScenario(t *testing.T, base string, owner, editor, viewer caller, refusals bool) {
	t.Helper()
	names, d, _ := tplList(t, viewer, base)
	if len(names) != 0 || len(folderDefaults(d)) != 0 || d["quickNote"] != "" {
		t.Fatalf("a new space has templates: %v %v", names, d)
	}

	// --- make, read, change
	meta := map[string]any{"title": "{{date}} 日记", "tags": []any{"日记", "d-{{date}}"}, "type": "diary", "mood": "{{time}}"}
	body := "## 今天 {{time}}\n{{datetime}} {{nope}} {{ date }} {{{date}}\n"
	if refusals {
		for who, c := range map[string]caller{"editor": editor, "viewer": viewer} {
			if code, r := c("PUT", base+"/templates/日记", map[string]any{"baseRev": "", "meta": meta, "body": body}); code != 403 || r["error"] != "no_permission" {
				t.Errorf("%s may not make a template: %d %v", who, code, r)
			}
			if code, _ := c("PUT", base+"/templates/defaults", map[string]any{"quickNote": ""}); code != 403 {
				t.Errorf("%s may not set defaults: %d", who, code)
			}
			if code, _ := c("DELETE", base+"/templates/日记?baseRev=x", nil); code != 403 {
				t.Errorf("%s may not delete: %d", who, code)
			}
		}
	}
	made := tplPut(t, owner, base, "日记", "", meta, body)
	rev1, _ := made["rev"].(string)
	if len(rev1) != 40 || made["name"] != "日记" || made["path"] != ".petalbook/templates/日记.md" || made["body"] != body {
		t.Fatalf("made: %v", made)
	}
	if m := made["meta"].(map[string]any); m["title"] != "{{date}} 日记" || m["type"] != "diary" {
		t.Errorf("meta: %v", m)
	}
	// The same name again, from nothing or from a stale rev, is a conflict that says how it is.
	for _, rev := range []string{"", "stale"} {
		code, r := owner("PUT", base+"/templates/日记", map[string]any{"baseRev": rev, "meta": meta, "body": "x"})
		cur, _ := r["current"].(map[string]any)
		if code != 409 || r["error"] != "conflict" || cur == nil || cur["rev"] != rev1 || cur["body"] != body {
			t.Errorf("baseRev %q: %d %v", rev, code, r)
		}
	}
	// A change of something that is not there.
	if code, r := owner("PUT", base+"/templates/没有", map[string]any{"baseRev": "abc", "meta": map[string]any{}, "body": ""}); code != 409 || r["current"] != nil {
		t.Errorf("stale create: %d %v", code, r)
	}
	changed := tplPut(t, owner, base, "日记", rev1, meta, body+"更多\n")
	rev2, _ := changed["rev"].(string)
	if rev2 == rev1 {
		t.Error("the rev did not change")
	}
	if same := tplPut(t, owner, base, "日记", rev2, meta, body+"更多\n"); same["rev"] != rev2 {
		t.Errorf("a save of the same text changed the rev: %v", same)
	}

	code, r := viewer("GET", base+"/templates/日记", nil)
	if code != 200 || r["rev"] != rev2 || r["body"] != body+"更多\n" {
		t.Errorf("get: %d %v", code, r)
	}
	if code, r := viewer("GET", base+"/templates/没有", nil); code != 404 || r["error"] != "not_found" {
		t.Errorf("missing: %d %v", code, r)
	}
	names, _, _ = tplList(t, viewer, base)
	if strings.Join(names, ",") != "日记" {
		t.Errorf("names: %v", names)
	}

	// --- names and limits
	for _, bad := range []string{"a:b", "con", ".hidden", "a%2Fb", strings.Repeat("长", 81), "defaults"} {
		code, r := owner("PUT", base+"/templates/"+bad, map[string]any{"baseRev": "", "meta": map[string]any{}, "body": ""})
		if code != 400 || r["error"] != "invalid" {
			t.Errorf("name %q: %d %v", bad, code, r)
		}
	}
	code, r = owner("PUT", base+"/templates/大", map[string]any{"baseRev": "", "meta": map[string]any{}, "body": strings.Repeat("x", 256<<10+1)})
	if code != 400 || r["error"] != "template_too_large" || r["message"] == "" {
		t.Errorf("too large: %d %v", code, r)
	}
	if code, r = owner("PUT", base+"/templates/小", map[string]any{"baseRev": "", "meta": map[string]any{}, "body": strings.Repeat("x", 200<<10)}); code != 200 {
		t.Errorf("just under: %d %v", code, r)
	}
	if code, r = owner("DELETE", base+"/templates/小?baseRev="+r["rev"].(string), nil); code != 204 {
		t.Errorf("delete: %d %v", code, r)
	}

	// --- defaults
	if code, r := owner("PUT", base+"/templates/defaults", map[string]any{"folder": "随笔", "template": "日记"}); code != 404 {
		t.Errorf("a folder that is not there: %d %v", code, r)
	}
	if code, r := owner("POST", base+"/folders", map[string]any{"path": "随笔/2026"}); code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	for _, body := range []map[string]any{
		{"folder": "随笔", "template": "没有"}, {"quickNote": "没有"},
		{}, {"folder": "随笔"}, {"template": "日记"}, {"folder": "随笔", "template": "日记", "quickNote": "日记"},
	} {
		code, r := owner("PUT", base+"/templates/defaults", body)
		want := "invalid"
		if body["template"] == "没有" || body["quickNote"] == "没有" {
			want = "template_not_found"
		}
		if code != 400 || r["error"] != want {
			t.Errorf("%v: %d %v", body, code, r)
		}
	}
	code, r = owner("PUT", base+"/templates/defaults", map[string]any{"folder": "随笔", "template": "日记"})
	if code != 200 || folderDefaults(r)["随笔"] != "日记" {
		t.Fatalf("set: %d %v", code, r)
	}
	if code, r = owner("PUT", base+"/templates/defaults", map[string]any{"folder": "随笔/2026", "template": "日记"}); code != 200 {
		t.Fatalf("set: %d %v", code, r)
	}
	tplPut(t, owner, base, "随记", "", map[string]any{"title": "随记 {{time}}"}, "")
	if code, r = owner("PUT", base+"/templates/defaults", map[string]any{"quickNote": "随记"}); code != 200 || r["quickNote"] != "随记" {
		t.Fatalf("quick: %d %v", code, r)
	}
	_, d, _ = tplList(t, viewer, base)
	if folderDefaults(d)["随笔"] != "日记" || d["quickNote"] != "随记" || len(folderDefaults(d)) != 2 {
		t.Errorf("defaults: %v", d)
	}

	// --- templates are not pages
	items, _ := list(t, viewer, base, "")
	for _, it := range items {
		if strings.Contains(it["path"].(string), "petalbook") || strings.Contains(it["path"].(string), "templates") {
			t.Errorf("a template is listed as a page: %v", it)
		}
	}
	if items, _ := list(t, viewer, base, "q="+url.QueryEscape("nope")); len(items) != 0 {
		t.Errorf("a template is found by search: %v", items)
	}

	// --- creating pages
	today := func(tz int) (string, string) {
		l := time.Now().UTC().Add(time.Duration(tz) * time.Minute)
		return l.Format("2006-01-02"), l.Format("15:04")
	}
	create := func(c caller, req map[string]any) map[string]any {
		t.Helper()
		code, r := c("POST", base+"/pages", req)
		if code != 201 {
			t.Fatalf("create %v: %d %v", req, code, r)
		}
		return r
	}
	for _, tz := range []int{0, 480, -480, 840, -840, 330} {
		before, _ := today(tz)
		p := create(editor, map[string]any{"folder": "随笔/2026", "template": "日记", "tz": tz})
		after, _ := today(tz)
		m := p["meta"].(map[string]any)
		ok := false
		for _, day := range []string{before, after} {
			if m["title"] == day+" 日记" {
				ok = true
			}
		}
		if !ok {
			t.Errorf("tz %d: title %v, want %s 日记", tz, m["title"], before)
		}
		hhmm := regexp.MustCompile(`^\d\d:\d\d$`)
		if !hhmm.MatchString(m["mood"].(string)) || m["type"] != "diary" {
			t.Errorf("tz %d: meta %v", tz, m)
		}
		tags := m["tags"].([]any)
		if len(tags) != 2 || tags[0] != "日记" || (tags[1] != "d-"+before && tags[1] != "d-"+after) {
			t.Errorf("tz %d: tags %v", tz, tags)
		}
		b := p["body"].(string)
		wantLines := regexp.MustCompile(`^## 今天 \d\d:\d\d\n\d{4}-\d\d-\d\d \d\d:\d\d \{\{nope\}\} \{\{ date \}\} \{\d{4}-\d\d-\d\d\n更多\n$`)
		if !wantLines.MatchString(b) {
			t.Errorf("tz %d: body %q", tz, b)
		}
	}
	// Two zones apart by a whole day give different dates.
	pa := create(editor, map[string]any{"folder": "", "template": "日记", "tz": 840})
	pb := create(editor, map[string]any{"folder": "", "template": "日记", "tz": -840})
	if pa["meta"].(map[string]any)["title"] == pb["meta"].(map[string]any)["title"] && time.Now().UTC().Hour() != 10 {
		// 28 hours apart: always a different date.
		t.Errorf("one date for two zones: %v %v", pa["meta"], pb["meta"])
	}
	// Without tz it is UTC.
	d0, _ := today(0)
	if p := create(editor, map[string]any{"folder": "", "template": "日记"}); !strings.HasPrefix(p["meta"].(map[string]any)["title"].(string), d0[:7]) {
		t.Errorf("no tz: %v", p["meta"])
	}
	// The request wins, field by field; the template's type is kept.
	p := create(editor, map[string]any{"folder": "", "template": "日记", "meta": map[string]any{"title": "我的", "type": "other", "extra": 1, "tags": []any{}}, "body": "自己的\n"})
	m := p["meta"].(map[string]any)
	if m["title"] != "我的" || m["type"] != "diary" || m["extra"] != float64(1) || len(m["tags"].([]any)) != 2 || p["body"] != "自己的\n" {
		t.Errorf("precedence: %v %q", m, p["body"])
	}
	p = create(editor, map[string]any{"folder": "", "template": "日记", "meta": map[string]any{"tags": []any{"我的"}}})
	if tags := p["meta"].(map[string]any)["tags"].([]any); len(tags) != 1 || tags[0] != "我的" || !strings.Contains(p["body"].(string), "## 今天") {
		t.Errorf("tags: %v", p["meta"])
	}
	// The folder's default, blank on request, none elsewhere.
	if p = create(editor, map[string]any{"folder": "随笔"}); p["meta"].(map[string]any)["type"] != "diary" {
		t.Errorf("the folder's default: %v", p["meta"])
	}
	if p = create(editor, map[string]any{"folder": "随笔", "template": ""}); p["meta"].(map[string]any)["type"] != nil || p["body"] != "" {
		t.Errorf("blank: %v", p)
	}
	if p = create(editor, map[string]any{"folder": "别处"}); p["meta"].(map[string]any)["type"] != nil || p["body"] != "" {
		t.Errorf("no default: %v", p)
	}
	// Quick notes: the page is made in the inbox.
	if p = create(editor, map[string]any{"folder": "Inbox", "body": "想到的\n"}); p["meta"].(map[string]any)["title"] == nil || !strings.HasPrefix(p["meta"].(map[string]any)["title"].(string), "随记 ") || p["body"] != "想到的\n" {
		t.Errorf("quick note: %v", p)
	}
	if p = create(editor, map[string]any{"folder": "Inbox", "template": ""}); p["meta"].(map[string]any)["title"] != nil {
		t.Errorf("a blank quick note: %v", p)
	}
	// Refusals.
	for _, req := range []map[string]any{{"folder": "", "template": "没有"}, {"folder": "", "template": "../x"}} {
		if code, r := editor("POST", base+"/pages", req); code != 400 || r["error"] != "template_not_found" {
			t.Errorf("%v: %d %v", req, code, r)
		}
	}
	if code, r := editor("POST", base+"/pages", map[string]any{"folder": "", "template": "日记", "tz": 841}); code != 400 || r["error"] != "invalid" {
		t.Errorf("tz: %d %v", code, r)
	}
	// The pages made are pages, and the templates are still not.
	items, _ = list(t, viewer, base, "")
	for _, it := range items {
		if strings.Contains(it["path"].(string), ".petalbook") {
			t.Errorf("listed: %v", it)
		}
	}

	// --- defaults follow their folder
	if code, r := owner("POST", base+"/folders/move", map[string]any{"from": "随笔", "to": "散文"}); code != 200 {
		t.Fatalf("move: %d %v", code, r)
	}
	_, d, _ = tplList(t, viewer, base)
	fd := folderDefaults(d)
	if fd["散文"] != "日记" || fd["散文/2026"] != "日记" || fd["随笔"] != nil || len(fd) != 2 {
		t.Errorf("after the move: %v", d)
	}
	if p = create(editor, map[string]any{"folder": "散文"}); p["meta"].(map[string]any)["type"] != "diary" {
		t.Errorf("the moved folder's default: %v", p["meta"])
	}
	// A new folder at the old name has none.
	if p = create(editor, map[string]any{"folder": "随笔"}); p["meta"].(map[string]any)["type"] != nil {
		t.Errorf("the old name kept the default: %v", p["meta"])
	}
	if code, r := owner("DELETE", base+"/folders?path="+url.QueryEscape("散文/2026")+"&recursive=true", nil); code != 204 {
		t.Fatalf("delete folder: %d %v", code, r)
	}
	_, d, _ = tplList(t, viewer, base)
	if fd = folderDefaults(d); fd["散文"] != "日记" || len(fd) != 1 {
		t.Errorf("after the delete: %v", d)
	}
	// A default can be taken off, also for a folder that is gone.
	if code, r := owner("PUT", base+"/templates/defaults", map[string]any{"folder": "散文", "template": ""}); code != 200 || len(folderDefaults(r)) != 0 {
		t.Errorf("unset: %d %v", code, r)
	}
	owner("PUT", base+"/templates/defaults", map[string]any{"folder": "散文", "template": "日记"})

	// --- deleting a template removes the defaults that name it
	if code, r := owner("DELETE", base+"/templates/日记?baseRev=stale", nil); code != 409 || r["current"] == nil {
		t.Errorf("stale delete: %d %v", code, r)
	}
	if code, r := owner("DELETE", base+"/templates/日记", nil); code != 409 {
		t.Errorf("delete without rev: %d %v", code, r)
	}
	code, cur := owner("GET", base+"/templates/日记", nil)
	if code != 200 {
		t.Fatal(code)
	}
	if code, r := owner("DELETE", base+"/templates/日记?baseRev="+cur["rev"].(string), nil); code != 204 {
		t.Fatalf("delete: %d %v", code, r)
	}
	if code, r := owner("DELETE", base+"/templates/日记?baseRev="+cur["rev"].(string), nil); code != 404 {
		t.Errorf("deleted twice: %d %v", code, r)
	}
	if code, _ := viewer("GET", base+"/templates/日记", nil); code != 404 {
		t.Errorf("still there: %d", code)
	}
	names, d, _ = tplList(t, viewer, base)
	if strings.Join(names, ",") != "随记" || len(folderDefaults(d)) != 0 || d["quickNote"] != "随记" {
		t.Errorf("after the delete: %v %v", names, d)
	}
	_, rq := owner("GET", base+"/templates/随记", nil)
	if code, r := owner("DELETE", base+"/templates/随记?baseRev="+rq["rev"].(string), nil); code != 204 {
		t.Fatalf("%d %v", code, r)
	}
	if _, d, _ = tplList(t, viewer, base); d["quickNote"] != "" {
		t.Errorf("the quick-note default stayed: %v", d)
	}
	// The template of a default that was put there by hand and is gone is removed with no rev.
	// Nothing is left of the settings but the name.
}

func TestTemplatesInAccountsMode(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		b := "/api/spaces/" + tm.sid
		templatesScenario(t, b, asClient(tm.owner), asClient(tm.editor), asClient(tm.viewer), true)
		if r := tm.outsider.do("GET", b+"/templates", nil); r.code != 404 {
			t.Errorf("an outsider: %d", r.code)
		}
		if r := tm.outsider.do("PUT", b+"/templates/x", "{}"); r.code != 404 {
			t.Errorf("an outsider: %d", r.code)
		}
	})
}

func TestTemplatesInSingleUserMode(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	c := asFixture(f)
	templatesScenario(t, base, c, c, c, false)
}

func TestTemplateLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	c := asFixture(f)
	for i := 0; i < 200; i++ {
		if code, r := c("PUT", fmt.Sprintf("%s/templates/t%03d", base, i), map[string]any{"baseRev": "", "meta": map[string]any{}, "body": "x"}); code != 200 {
			t.Fatalf("%d: %d %v", i, code, r)
		}
	}
	code, r := c("PUT", base+"/templates/t200", map[string]any{"baseRev": "", "meta": map[string]any{}, "body": "x"})
	if code != 400 || r["error"] != "template_limit" {
		t.Fatalf("%d %v", code, r)
	}
	// One that is there can still be changed.
	_, cur := c("GET", base+"/templates/t005", nil)
	if code, r := c("PUT", base+"/templates/t005", map[string]any{"baseRev": cur["rev"], "meta": map[string]any{}, "body": "y"}); code != 200 {
		t.Errorf("%d %v", code, r)
	}
	if names, _, _ := tplList(t, c, base); len(names) != 200 {
		t.Errorf("%d", len(names))
	}
}

// TestTemplateLinksAreNeverFollowed: a link in .petalbook/templates, or in the
// place of the folder, is not read, listed or written through.
func TestTemplateLinksAreNeverFollowed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	c := asFixture(f)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secret, []byte("---\ntitle: 秘密\n---\n不该看到\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tplPut(t, c, base, "好的", "", map[string]any{"title": "好"}, "好\n")
	dir := filepath.Join(f.dir, ".petalbook", "templates")
	if err := os.Symlink(secret, filepath.Join(dir, "外.md")); err != nil {
		t.Skip("no symbolic links here:", err)
	}
	names, _, _ := tplList(t, c, base)
	if strings.Join(names, ",") != "好的" {
		t.Errorf("a link is listed: %v", names)
	}
	if code, r := c("GET", base+"/templates/外", nil); code != 404 {
		t.Errorf("a link is read: %d %v", code, r)
	}
	if code, r := c("POST", base+"/pages", map[string]any{"folder": "", "template": "外"}); code != 400 || r["error"] != "template_not_found" {
		t.Errorf("a link is used: %d %v", code, r)
	}
	// Writing replaces the link; what it led to is left alone.
	tplPut(t, c, base, "外", "", map[string]any{"title": "自己的"}, "本地\n")
	if b, _ := os.ReadFile(secret); !strings.Contains(string(b), "不该看到") {
		t.Errorf("written through a link: %q", b)
	}
	if fi, err := os.Lstat(filepath.Join(dir, "外.md")); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("%v %v", fi, err)
	}
	// A link in the place of the folder.
	other := filepath.Join(outside, "dir")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "外.md"), []byte("---\ntitle: 外\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+"-real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, dir); err != nil {
		t.Fatal(err)
	}
	if names, _, _ := tplList(t, c, base); len(names) != 0 {
		t.Errorf("a linked folder is listed: %v", names)
	}
	if code, _ := c("GET", base+"/templates/外", nil); code != 404 {
		t.Errorf("a linked folder is read: %d", code)
	}
	if code, r := c("PUT", base+"/templates/新", map[string]any{"baseRev": "", "meta": map[string]any{}, "body": "x"}); code == 200 {
		t.Errorf("written through a linked folder: %v", r)
	}
	if es, _ := os.ReadDir(other); len(es) != 1 {
		t.Errorf("something was written outside: %v", es)
	}
}
