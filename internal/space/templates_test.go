package space

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

const anchoredSettings = `# my settings
name: X
templates:
  folders:
    a: &t 日记   # the diary
    b: *t
    a/c: 随记
  quickNote: *t
`

func editedTemplates(t *testing.T, text string, edit func(top *yaml.Node) (bool, error)) (string, *TemplateDefaults) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".petalbook"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, spaceFile), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	data, changed, err := planSpaceYAML(root, "X", edit)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		return text, templatesOf(parseForTest(t, text))
	}
	return string(data), templatesOf(parseForTest(t, string(data)))
}

func parseForTest(t *testing.T, text string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	return &doc
}

func TestTemplateDefaultsInSettingsWithAnchors(t *testing.T) {
	want := func(got *TemplateDefaults, folders map[string]string, quick string) {
		t.Helper()
		if got == nil {
			got = &TemplateDefaults{Folders: map[string]string{}}
		}
		if !reflect.DeepEqual(got.Folders, folders) || got.QuickNote != quick {
			t.Errorf("got %v %q, want %v %q", got.Folders, got.QuickNote, folders, quick)
		}
	}
	_, d := editedTemplates(t, anchoredSettings, func(*yaml.Node) (bool, error) { return false, nil })
	want(d, map[string]string{"a": "日记", "b": "日记", "a/c": "随记"}, "日记")

	// Changing the folder whose value carries the anchor changes it alone.
	out, d := editedTemplates(t, anchoredSettings, func(top *yaml.Node) (bool, error) { return setFolderTemplate(top, "a", "周报") })
	want(d, map[string]string{"a": "周报", "b": "日记", "a/c": "随记"}, "日记")
	if !strings.Contains(out, "# my settings") {
		t.Errorf("a comment was lost:\n%s", out)
	}
	// So does the quick note.
	_, d = editedTemplates(t, anchoredSettings, func(top *yaml.Node) (bool, error) { return setQuickNoteTemplate(top, "周报") })
	want(d, map[string]string{"a": "日记", "b": "日记", "a/c": "随记"}, "周报")
	_, d = editedTemplates(t, anchoredSettings, func(top *yaml.Node) (bool, error) { return setQuickNoteTemplate(top, "") })
	want(d, map[string]string{"a": "日记", "b": "日记", "a/c": "随记"}, "")

	// A move: the folder and the folders in it follow, and nothing stale stays.
	_, d = editedTemplates(t, anchoredSettings, func(top *yaml.Node) (bool, error) { return rebindTemplates(top, "a", "z", false) })
	want(d, map[string]string{"z": "日记", "b": "日记", "z/c": "随记"}, "日记")
	_, d = editedTemplates(t, anchoredSettings, func(top *yaml.Node) (bool, error) { return rebindTemplates(top, "b", "a", false) })
	want(d, map[string]string{"a": "日记"}, "日记") // the entries at the new place, and under it, were stale
	// Recovery of a move that was made: the new keys are there, nothing to move, nothing dropped.
	_, d = editedTemplates(t, anchoredSettings, func(top *yaml.Node) (bool, error) { return rebindTemplates(top, "x", "a", true) })
	want(d, map[string]string{"a": "日记", "b": "日记", "a/c": "随记"}, "日记")

	// A folder that is gone, a template that is gone.
	_, d = editedTemplates(t, anchoredSettings, func(top *yaml.Node) (bool, error) {
		return forgetTemplateFolders(top, func(f string) bool { return f == "a" || f == "a/c" })
	})
	want(d, map[string]string{"b": "日记"}, "日记")
	out, d = editedTemplates(t, anchoredSettings, func(top *yaml.Node) (bool, error) { return forgetTemplateNamed(top, "日记") })
	want(d, map[string]string{"a/c": "随记"}, "")
	if strings.Contains(out, "quickNote") {
		t.Errorf("\n%s", out)
	}
	out, d = editedTemplates(t, anchoredSettings, func(top *yaml.Node) (bool, error) {
		a, _ := forgetTemplateNamed(top, "日记")
		b, err := forgetTemplateNamed(top, "随记")
		return a || b, err
	})
	want(d, map[string]string{}, "")
	if strings.Contains(out, "templates") {
		t.Errorf("an empty section stays:\n%s", out)
	}

	// Made from nothing, and odd shapes are written over.
	for _, start := range []string{"name: X\n", "name: X\ntemplates:\n", "name: X\ntemplates: yes\n", "name: X\ntemplates:\n  folders: 3\n"} {
		out, d = editedTemplates(t, start, func(top *yaml.Node) (bool, error) { return setFolderTemplate(top, "随笔", "日记") })
		want(d, map[string]string{"随笔": "日记"}, "")
		if _, d = editedTemplates(t, out, func(top *yaml.Node) (bool, error) { return setFolderTemplate(top, "随笔", "") }); d != nil {
			t.Errorf("not removed: %v", d)
		}
	}
	// A folder written twice counts as its last, also when it is set.
	twice := "name: X\ntemplates:\n  folders:\n    a: 甲\n    a: 乙\n"
	_, d = editedTemplates(t, twice, func(*yaml.Node) (bool, error) { return false, nil })
	want(d, map[string]string{"a": "乙"}, "")
	_, d = editedTemplates(t, twice, func(top *yaml.Node) (bool, error) { return setFolderTemplate(top, "a", "丙") })
	want(d, map[string]string{"a": "丙"}, "")
}

func TestTemplateVariables(t *testing.T) {
	local := time.Date(2026, 11, 23, 9, 5, 0, 0, time.UTC)
	tpl := Template{
		Meta: page.Meta{{Key: "title", Value: "{{date}} {{time}}"}, {Key: "type", Value: "diary"},
			{Key: "tags", Value: []any{"{{datetime}}", "{{x}}"}}, {Key: "nested", Value: page.Meta{{Key: "d", Value: "{{date}}"}}},
			{Key: "id", Value: "SHOULDNOTCOUNT"}, {Key: "n", Value: int64(3)}},
		Body: "{{date}}|{{time}}|{{datetime}}|{{date}}{{date}}|{{ date }}|{{Date}}|{{",
	}
	meta, body := mergeTemplate(tpl, nil, "", local)
	if meta.Title() != "2026-11-23 09:05" || meta.Type() != "diary" || meta.Index("id") >= 0 {
		t.Errorf("%v", meta)
	}
	if tags := meta.Tags(); len(tags) != 2 || tags[0] != "2026-11-23 09:05" || tags[1] != "{{x}}" {
		t.Errorf("%v", tags)
	}
	if v, _ := meta.Get("nested"); v.(page.Meta)[0].Value != "2026-11-23" {
		t.Errorf("%v", v)
	}
	if body != "2026-11-23|09:05|2026-11-23 09:05|2026-11-232026-11-23|{{ date }}|{{Date}}|{{" {
		t.Errorf("%q", body)
	}
	// What the request gives is not a template: its braces stay.
	meta, body = mergeTemplate(tpl, page.Meta{{Key: "title", Value: "{{date}}"}, {Key: "type", Value: "x"}, {Key: "z", Value: 1}}, "{{time}}", local)
	if meta.Title() != "{{date}}" || meta.Type() != "diary" || body != "{{time}}" || meta[len(meta)-1].Key != "z" {
		t.Errorf("%v %q", meta, body)
	}
	// A template without a type takes the request's.
	meta, _ = mergeTemplate(Template{Meta: page.Meta{{Key: "title", Value: "t"}}}, page.Meta{{Key: "type", Value: "x"}}, "", local)
	if meta.Type() != "x" {
		t.Errorf("%v", meta)
	}
}
