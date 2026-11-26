package api_test

import (
	"reflect"
	"testing"
)

// A created range picks pages by the created day in their own header, which a
// save may set; a page made today is outside a range that ended earlier.
func TestCreatedRangeUsesThePagesOwnCreatedDay(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	older := f.create("", map[string]any{"title": "旧"}, "x\n")
	f.create("", map[string]any{"title": "新"}, "y\n")
	code, saved, raw := f.do("PUT", base+"/pages/"+older["id"].(string), map[string]any{
		"baseRev": older["rev"], "meta": map[string]any{"title": "旧", "created": "2020-03-15"}, "body": "x\n",
	})
	if code != 200 || saved["meta"].(map[string]any)["created"] != "2020-03-15" {
		t.Fatalf("%d %s", code, raw)
	}
	ids := func(q string) []string {
		code, r, raw := f.do("GET", base+"/pages"+q, nil)
		if code != 200 {
			t.Fatalf("%s: %d %s", q, code, raw)
		}
		var out []string
		for _, it := range r["items"].([]any) {
			out = append(out, it.(map[string]any)["id"].(string))
		}
		return out
	}
	only := []string{older["id"].(string)}
	for q, want := range map[string][]string{
		"?createdFrom=2020-03-15&createdTo=2020-03-15": only,
		"?createdTo=2020-03-14":                        nil,
		"?createdTo=2020-03-16":                        only,
	} {
		if got := ids(q); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", q, got, want)
		}
	}
}
