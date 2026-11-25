package api_test

import (
	"fmt"
	"testing"
)

func TestAppearanceSourcesAndFieldRename(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if code, _, raw := f.do("POST", "/api/spaces/default/folders", map[string]any{"path": "Tang"}); code != 201 {
		t.Fatal(raw)
	}
	code, r, raw := f.do("GET", "/api/spaces/default/appearance", nil)
	if code != 200 || r["sourcePresets"] != nil || len(r["folders"].([]any)) != 0 {
		t.Fatal(code, raw)
	}
	if code, _, raw := f.do("PUT", "/api/spaces/default/folders/style", map[string]any{"folder": "Tang", "icon": "book-open", "color": "teal"}); code != 200 {
		t.Fatal(code, raw)
	}
	if code, r, raw := f.do("PUT", "/api/spaces/default/folders/style", map[string]any{"folder": "Tang", "icon": "x", "color": "mauve"}); code != 400 || r["error"] != "invalid" {
		t.Fatal(code, raw)
	}
	if code, _, raw := f.do("PUT", "/api/spaces/default/sources/presets", map[string]any{"presets": []string{"书", "电影"}}); code != 200 {
		t.Fatal(code, raw)
	}
	_, r, raw = f.do("GET", "/api/spaces/default/appearance", nil)
	fs := r["folders"].([]any)
	if len(fs) != 1 || fs[0].(map[string]any)["icon"] != "book-open" || fmt.Sprint(r["sourcePresets"]) != "[书 电影]" {
		t.Fatal(raw)
	}
	if code, _, raw := f.do("POST", "/api/spaces/default/pages", map[string]any{"folder": "Tang", "meta": map[string]any{"title": "A", "source": []string{"书", "电影"}}, "body": "x\n"}); code != 201 && code != 200 {
		t.Fatal(code, raw)
	}
	_, r, raw = f.do("GET", "/api/spaces/default/sources", nil)
	list := r["sources"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["pages"] != float64(1) {
		t.Fatal(raw)
	}
	if code, r, raw := f.do("POST", "/api/spaces/default/types/nope/rename-field", map[string]any{"baseRev": "", "from": "a", "to": "b"}); code != 404 || r["error"] != "not_found" {
		t.Fatal(code, raw)
	}
}

func TestIconsAreCheckedAndEmptyPresetsStayEmpty(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if code, _, raw := f.do("POST", "/api/spaces/default/folders", map[string]any{"path": "Tang"}); code != 201 {
		t.Fatal(raw)
	}
	for _, icon := range []string{"constructor", "__proto__", "not-an-icon", "<b>"} {
		if code, r, raw := f.do("PUT", "/api/spaces/default/folders/style", map[string]any{"folder": "Tang", "icon": icon}); code != 400 || r["error"] != "invalid" {
			t.Errorf("icon %q: %d %s", icon, code, raw)
		}
	}
	if code, r, raw := f.do("POST", "/api/spaces/default/pages", map[string]any{"folder": "Tang", "meta": map[string]any{"title": "A", "icon": "constructor"}, "body": "x\n"}); code != 400 || r["error"] != "invalid" {
		t.Errorf("a page with the icon constructor: %d %s", code, raw)
	}
	if code, r, raw := f.do("PUT", "/api/spaces/default/types/poem", map[string]any{"baseRev": "", "title": "Poem", "icon": "constructor"}); code != 400 || r["error"] != "invalid" {
		t.Errorf("a type with the icon constructor: %d %s", code, raw)
	}
}
