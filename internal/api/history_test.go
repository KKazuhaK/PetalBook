package api_test

import (
	"context"
	"strings"
	"testing"
)

// commitNow commits what the single-user space has pending, so the next
// change is a version of its own.
func (f *fixture) commitNow() {
	f.t.Helper()
	if err := f.sp.Flush(); err != nil {
		f.t.Fatal(err)
	}
}

func TestHistoryEndpoints(t *testing.T) {
	f := newFixture(t)
	p := f.create("Tang", map[string]any{"title": "暮雪"}, "v1\n")
	id := p["id"].(string)
	url := base + "/pages/" + id
	f.commitNow()
	save := func(rev any, title, body string) map[string]any {
		t.Helper()
		code, r, raw := f.do("PUT", url, map[string]any{"baseRev": rev, "meta": map[string]any{"title": title}, "body": body})
		if code != 200 {
			t.Fatalf("%d %s", code, raw)
		}
		f.commitNow()
		return r
	}
	v2 := save(p["rev"], "暮雪", "v2\n")
	v3 := save(v2["rev"], "夜雪", "v2\n") // renamed
	code, mv, raw := f.do("POST", url+"/move", map[string]any{"folder": "随笔"})
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	f.commitNow()
	v5 := save(mv["rev"], "夜雪", "v3\n")
	_ = v3

	code, r, raw := f.do("GET", url+"/history", nil)
	if code != 200 || r["next"] != "" {
		t.Fatalf("%d %s", code, raw)
	}
	items := r["items"].([]any)
	var changes []string
	for _, it := range items {
		m := it.(map[string]any)
		changes = append(changes, m["change"].(string))
		for _, k := range []string{"commit", "time", "author", "path", "rev", "change"} {
			if m[k] == nil || m[k] == "" {
				t.Fatalf("item without %s: %v", k, m)
			}
		}
		if len(m["commit"].(string)) != 40 || !strings.HasSuffix(m["time"].(string), "Z") {
			t.Fatalf("%v", m)
		}
	}
	if strings.Join(changes, ",") != "edit,move,edit,edit,add" {
		t.Fatalf("%v", changes)
	}
	first := items[0].(map[string]any)
	if first["rev"] != v5["rev"] || first["path"] != "随笔/夜雪.md" || first["author"] != "PetalBook" && first["author"] == "" {
		t.Fatalf("%v", first)
	}

	// Paging.
	code, r, raw = f.do("GET", url+"/history?limit=2", nil)
	if code != 200 || len(r["items"].([]any)) != 2 || r["next"] == "" {
		t.Fatalf("%d %s", code, raw)
	}
	var all []string
	cursor := ""
	for i := 0; i < 10; i++ {
		q := "?limit=2"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		code, r, raw = f.do("GET", url+"/history"+q, nil)
		if code != 200 {
			t.Fatalf("%d %s", code, raw)
		}
		for _, it := range r["items"].([]any) {
			all = append(all, it.(map[string]any)["commit"].(string))
		}
		if cursor = r["next"].(string); cursor == "" {
			break
		}
	}
	if len(all) != 5 {
		t.Fatalf("%d versions paged", len(all))
	}
	for i, it := range items {
		if all[i] != it.(map[string]any)["commit"] {
			t.Fatal("paging changed the order")
		}
	}
	for _, bad := range []string{"limit=0", "limit=101", "limit=-1", "limit=abc", "cursor=!!!", "cursor=Zm9v", "limit=1&cursor=" + strings.Repeat("A", 4000)} {
		code, r, raw = f.do("GET", url+"/history?"+bad, nil)
		wantError(t, code, r, raw, 400, "invalid")
	}
	code, r, raw = f.do("GET", url+"/history?limit=100", nil)
	if code != 200 {
		t.Fatal(raw)
	}

	// A version.
	for _, it := range items {
		m := it.(map[string]any)
		code, v, raw := f.do("GET", url+"/versions/"+m["commit"].(string), nil)
		if code != 200 || v["commit"] != m["commit"] || v["rev"] != m["rev"] || v["path"] != m["path"] || v["author"] != m["author"] || v["time"] != m["time"] || v["meta"] == nil || v["body"] == nil {
			t.Fatalf("%d %s", code, raw)
		}
		if v["meta"].(map[string]any)["id"] != id {
			t.Fatalf("%s", raw)
		}
	}
	_, v, _ := f.do("GET", url+"/versions/"+items[3].(map[string]any)["commit"].(string), nil)
	if v["body"] != "v2\n" || v["meta"].(map[string]any)["title"] != "暮雪" {
		t.Fatalf("%v", v)
	}

	// Anything else is not a version of this page, however it is spelled.
	other := f.create("", map[string]any{"title": "Secret"}, "secret body\n")
	f.commitNow()
	_, oh, _ := f.do("GET", base+"/pages/"+other["id"].(string)+"/history", nil)
	otherCommit := oh["items"].([]any)[0].(map[string]any)["commit"]
	hostile := []string{"HEAD", "main", "master", "HEAD~1", "HEAD^{tree}", strings.Repeat("0", 40), "--output=x", otherCommit.(string),
		strings.ToUpper(items[0].(map[string]any)["commit"].(string)), items[0].(map[string]any)["commit"].(string)[:12], "stash@%7B0%7D", "HEAD:Secret.md", "%3Ftree"}
	for _, c := range hostile {
		code, r, raw = f.do("GET", url+"/versions/"+c, nil)
		wantError(t, code, r, raw, 404, "not_found")
		if strings.Contains(raw, "secret body") {
			t.Fatalf("%s leaked another page: %s", c, raw)
		}
	}
	// A page that does not exist, or whose id is not one.
	for _, u := range []string{base + "/pages/0000000000/history", base + "/pages/0000000000/versions/" + all[0], base + "/pages/short/history"} {
		code, r, raw = f.do("GET", u, nil)
		wantError(t, code, r, raw, 404, "not_found")
	}
	// The other page's own commit is its version.
	if code, v, raw := f.do("GET", base+"/pages/"+other["id"].(string)+"/versions/"+otherCommit.(string), nil); code != 200 || v["body"] != "secret body\n" {
		t.Fatalf("%d %s", code, raw)
	}
	// Only GET.
	code, r, raw = f.do("POST", url+"/history", "{}")
	wantError(t, code, r, raw, 405, "invalid")
}

func TestHistoryInAccountsModeIsForEveryMember(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		sid := tang.personalSpace()
		p := tang.do("POST", "/api/spaces/"+sid+"/pages", map[string]any{"meta": map[string]any{"title": "Mine"}, "body": "x\n"}).want(t, 201, "")
		sp, release, err := a.mgr.Acquire(ctxBG, sid)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if err := sp.Flush(); err != nil {
			t.Fatal(err)
		}
		url := "/api/spaces/" + sid + "/pages/" + p.body["id"].(string)
		h := tang.do("GET", url+"/history", nil).want(t, 200, "")
		items := h.body["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["change"] != "add" {
			t.Fatal(h.raw)
		}
		commit := items[0].(map[string]any)["commit"].(string)
		tang.do("GET", url+"/versions/"+commit, nil).want(t, 200, "")
		// Someone who is not a member is told the space is not there.
		stranger := a.addUser("stranger", pw, "member")
		stranger.do("GET", url+"/history", nil).want(t, 404, "not_found")
		stranger.do("GET", url+"/versions/"+commit, nil).want(t, 404, "not_found")
		// The author on the commit is the space's owner.
		if items[0].(map[string]any)["author"] != "tang" {
			t.Fatalf("%v", items[0])
		}
	})
}

var ctxBG = context.Background()

// Each commit carries the person who made the change, and the history says so,
// whoever else shares the space.
func TestHistoryNamesThePersonWhoMadeEachChange(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		tm := newTeam(t, a)
		pages := "/api/spaces/" + tm.sid + "/pages"
		pa := tm.owner.do("POST", pages, map[string]any{"meta": map[string]any{"title": "Alpha"}, "body": "a\n"}).want(t, 201, "")
		pb := tm.editor.do("POST", pages, map[string]any{"meta": map[string]any{"title": "Beta"}, "body": "b\n"}).want(t, 201, "")
		// Both are in one commit window of the server; Flush closes it.
		sp, release, err := a.mgr.Acquire(ctxBG, tm.sid)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if err := sp.Flush(); err != nil {
			t.Fatal(err)
		}
		tm.editor.do("PUT", pages+"/"+pa.body["id"].(string), map[string]any{"baseRev": pa.body["rev"], "meta": map[string]any{"title": "Alpha"}, "body": "edited by edith\n"}).want(t, 200, "")
		if err := sp.Flush(); err != nil {
			t.Fatal(err)
		}
		authors := func(id string) []string {
			var out []string
			for _, it := range tm.owner.do("GET", pages+"/"+id+"/history", nil).want(t, 200, "").body["items"].([]any) {
				out = append(out, it.(map[string]any)["author"].(string))
			}
			return out
		}
		if got := strings.Join(authors(pa.body["id"].(string)), ","); got != "edith,olive" {
			t.Fatalf("Alpha: %s", got)
		}
		if got := strings.Join(authors(pb.body["id"].(string)), ","); got != "edith" {
			t.Fatalf("Beta: %s", got)
		}
	})
}
