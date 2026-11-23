package api_test

import (
	"strings"
	"testing"
)

func TestLinksEndpointsSingleUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.create("Tang", map[string]any{"title": "暮雪"}, "x\n")
	aid := a["id"].(string)
	b := f.create("Notes", map[string]any{"title": "引用"}, "见[暮雪](../Tang/暮雪.md) 和[无](无.md)\n")
	bid := b["id"].(string)

	code, r, raw := f.do("GET", base+"/pages/"+aid+"/backlinks", nil)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	items := r["items"].([]any)
	if len(items) != 1 || r["total"].(float64) != 1 || r["next"] != "" {
		t.Fatal(raw)
	}
	it := items[0].(map[string]any)
	if it["id"] != bid || it["title"] != "引用" || it["excerpt"] != "见暮雪 和无" || it["path"] != "Notes/引用.md" {
		t.Fatal(raw)
	}
	code, r, raw = f.do("GET", base+"/pages/"+bid+"/links", nil)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	ls := r["items"].([]any)
	l0, l1 := ls[0].(map[string]any), ls[1].(map[string]any)
	if len(ls) != 2 || l0["broken"] != false || l0["target"].(map[string]any)["title"] != "暮雪" || l1["broken"] != true || l1["target"] != nil {
		t.Fatal(raw)
	}
	// A rename follows into the referrer, and the links list shows the new title.
	code, _, raw = f.do("PUT", base+"/pages/"+aid, map[string]any{"baseRev": a["rev"], "meta": map[string]any{"title": "夜雪"}, "body": "x\n"})
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	_, r, raw = f.do("GET", base+"/pages/"+bid, nil)
	if !strings.Contains(r["body"].(string), "[暮雪](../Tang/夜雪.md \""+aid+"\")") {
		t.Fatal(raw)
	}
	_, r, raw = f.do("GET", base+"/pages/"+bid+"/links", nil)
	if r["items"].([]any)[0].(map[string]any)["target"].(map[string]any)["title"] != "夜雪" {
		t.Fatal(raw)
	}
	for _, u := range []string{base + "/pages/0000000000/backlinks", base + "/pages/0000000000/links", base + "/pages/short/links"} {
		if code, _, _ := f.do("GET", u, nil); code != 404 {
			t.Fatalf("%s: %d", u, code)
		}
	}
	if code, _, _ := f.do("GET", base+"/pages/"+aid+"/backlinks?cursor=x", nil); code != 400 {
		t.Fatalf("%d", code)
	}
}

func TestLinksEndpointsNeedMembership(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		sid := tang.personalSpace()
		p := tang.do("POST", "/api/spaces/"+sid+"/pages", map[string]any{"meta": map[string]any{"title": "Mine"}, "body": "x\n"}).want(t, 201, "")
		url := "/api/spaces/" + sid + "/pages/" + p.body["id"].(string)
		tang.do("GET", url+"/backlinks", nil).want(t, 200, "")
		tang.do("GET", url+"/links", nil).want(t, 200, "")
		stranger := a.addUser("stranger", pw, "member")
		stranger.do("GET", url+"/backlinks", nil).want(t, 404, "not_found")
		stranger.do("GET", url+"/links", nil).want(t, 404, "not_found")
	})
}
