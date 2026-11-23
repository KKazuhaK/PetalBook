package api_test

import (
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/api"
)

func TestSingleUserSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, r, raw := f.do("GET", "/api/session", nil)
	if code != 200 {
		t.Fatal(raw)
	}
	u := r["user"].(map[string]any)
	if r["mode"] != "single" || r["setup"] != false || r["methods"] != nil || u["id"] != "local" || u["username"] != "local" || u["name"] != "Local user" || u["role"] != "admin" || u["disabled"] != false || u["email"] != "" {
		t.Fatal(raw)
	}
	if !rfc3339.MatchString(u["created"].(string)) {
		t.Fatal(raw)
	}
	if strings.Contains(raw, `"methods"`) {
		t.Fatalf("methods must be absent in single-user mode: %s", raw)
	}
	code, r, raw = f.do("GET", "/api/spaces", nil)
	l := r["spaces"].([]any)
	if code != 200 || len(l) != 1 || l[0].(map[string]any)["id"] != "default" || l[0].(map[string]any)["role"] != "owner" || l[0].(map[string]any)["personal"] != true {
		t.Fatal(raw)
	}
}

func TestSingleUserHasNoAccountEndpoints(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	n := 0
	for _, rt := range api.Routes() {
		if !rt.AccountsOnly {
			continue
		}
		n++
		path := strings.NewReplacer("{space}", "default", "{id}", "01JD3K8Q7Z").Replace(rt.Pattern)
		if rt.Method == "POST" && strings.HasSuffix(rt.Pattern, "/acs") {
			continue // a form post, answered 404 in single-user mode too (the next test)
		}
		code, r, raw := f.do(rt.Method, path, "{}")
		if code != 404 || r["error"] != "not_found" {
			t.Errorf("%s %s: %d %s", rt.Method, rt.Pattern, code, raw)
		}
	}
	if n < 12 {
		t.Fatalf("only %d accounts-only routes", n)
	}
	// What single-user mode does have still works.
	if code, _, raw := f.do("GET", base+"/tree", nil); code != 200 {
		t.Fatal(raw)
	}
	// And there is no way to name another space.
	if code, r, raw := f.do("GET", "/api/spaces/other/tree", nil); code != 404 || r["error"] != "not_found" {
		t.Fatal(raw)
	}
}

func TestSingleUserHasNoMembers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, c := range [][2]string{{"GET", ""}, {"POST", ""}, {"PATCH", "/local"}, {"DELETE", "/local"}} {
		if code, r, raw := f.do(c[0], base+"/members"+c[1], "{}"); code != 404 || r["error"] != "not_found" {
			t.Errorf("%s members%s: %d %s", c[0], c[1], code, raw)
		}
	}
	// The one local user may do everything, with no membership table involved.
	if code, _, raw := f.do("POST", base+"/reindex", nil); code != 200 {
		t.Fatal(raw)
	}
}

func TestSingleUserDrafts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	p := f.create("", map[string]any{"title": "T"}, "one\n")
	url := base + "/pages/" + p["id"].(string)
	body := func(rev any, text string) map[string]any {
		return map[string]any{"baseRev": rev, "meta": map[string]any{"title": "T"}, "body": text}
	}
	code, r, raw := f.do("PUT", url+"/draft", body(p["rev"], "two\n"))
	if code != 200 || r["stale"] != false || r["version"] == "" || r["version"] == nil {
		t.Fatal(raw)
	}
	v1 := r["version"].(string)
	_, g, _ := f.do("GET", url, nil)
	if g["body"] != "one\n" || g["draft"].(map[string]any)["body"] != "two\n" {
		t.Fatalf("%v", g)
	}
	code, r, raw = f.do("POST", url+"/commit", map[string]any{"version": v1})
	if code != 200 || r["body"] != "two\n" || r["rev"] == p["rev"] {
		t.Fatal(raw)
	}
	if _, g, _ := f.do("GET", url, nil); g["draft"] != nil {
		t.Fatal("draft after commit")
	}
	// Stale: the file moves under a draft.
	_, d3, _ := f.do("PUT", url+"/draft", body(r["rev"], "three\n"))
	f.do("PUT", url, body(r["rev"], "four\n"))
	b5 := body(r["rev"], "five\n")
	b5["prev"] = d3["version"]
	_, d5, raw5 := f.do("PUT", url+"/draft", b5)
	if d5["version"] == nil || d5["version"] == d3["version"] {
		t.Fatal(raw5)
	}
	code, c, raw := f.do("POST", url+"/commit", map[string]any{"version": d5["version"]})
	if code != 409 || c["error"] != "conflict" || c["current"].(map[string]any)["body"] != "four\n" {
		t.Fatal(raw)
	}
	if code, _, _ := f.do("DELETE", url+"/draft", nil); code != 204 {
		t.Fatal(code)
	}
}
