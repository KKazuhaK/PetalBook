package api_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

func dailyID(t *testing.T, c caller, base, query string) (string, map[string]any) {
	t.Helper()
	code, r := c("GET", base+"/daily?"+query, nil)
	if code != 200 {
		t.Fatalf("daily %s: %d %v", query, code, r)
	}
	p, _ := r["page"].(map[string]any)
	if p == nil {
		return "", r
	}
	return p["id"].(string), r
}

func days(n int) []string {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	out := make([]string, n)
	for i := range out {
		out[i] = t0.AddDate(0, 0, i).Format("2006-01-02")
	}
	return out
}

// dailyScenario is what every role and both modes do with the daily text.
func dailyScenario(t *testing.T, base string, owner, editor, viewer caller, refusals bool, spaceYML func() string) {
	t.Helper()
	mk := func(folder, title, body string, meta map[string]any) string {
		t.Helper()
		if meta == nil {
			meta = map[string]any{}
		}
		meta["title"] = title
		code, r := editor("POST", base+"/pages", map[string]any{"folder": folder, "meta": meta, "body": body})
		if code != 201 {
			t.Fatalf("create %s: %d %v", title, code, r)
		}
		return r["id"].(string)
	}
	if code, r := owner("POST", base+"/folders", map[string]any{"path": "文案"}); code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	// No source yet: nothing, and the settings say so.
	if id, r := dailyID(t, viewer, base, "date=2026-11-23"); id != "" || r["reason"] != "no_source" {
		t.Fatalf("no source: %v", r)
	}
	if code, r := viewer("GET", base+"/daily/settings", nil); code != 200 || r["daily"] != nil {
		t.Fatalf("%d %v", code, r)
	}
	ids := map[string]string{}
	for i, name := range []string{"甲", "乙", "丙", "丁", "戊"} {
		status := "草稿"
		if i%2 == 0 {
			status = "定稿"
		}
		ids[name] = mk("文案", name, fmt.Sprintf("《%s》｜正文 %d\n", name, i), map[string]any{"status": status, "tags": []any{"诗"}})
	}
	mk("别处", "己", "elsewhere\n", map[string]any{"status": "定稿"})

	// --- who may set it, and what it may be
	set := func(c caller, daily any) (int, map[string]any) {
		return c("PUT", base+"/daily/settings", map[string]any{"daily": daily})
	}
	if refusals {
		for name, c := range map[string]caller{"editor": editor, "viewer": viewer} {
			if code, r := set(c, map[string]any{"folder": "文案"}); code != 403 || r["error"] != "no_permission" || !strings.Contains(r["message"].(string), "manage") {
				t.Errorf("%s sets the source: %d %v", name, code, r)
			}
		}
	}
	for name, bad := range map[string]any{
		"two sources":     map[string]any{"folder": "文案", "tag": "诗"},
		"no source":       map[string]any{},
		"an empty tag":    map[string]any{"tag": ""},
		"no such type":    map[string]any{"type": "nothing"},
		"an unknown key":  map[string]any{"folder": "文案", "recursive": false},
		"a bad filter":    map[string]any{"folder": "文案", "filter": []any{map[string]any{"key": "", "op": "in", "values": []any{"a"}}}},
		"a bad filter op": map[string]any{"folder": "文案", "filter": []any{map[string]any{"key": "status", "op": "like", "values": []any{"a"}}}},
		"too many":        map[string]any{"folder": "文案", "filter": manyFilters(11)},
	} {
		// A key that is no setting is read as a language, and is none.
		if code, r := set(owner, bad); code != 400 || (r["error"] != "invalid" && !(name == "an unknown key" && r["error"] == "bad_language")) {
			t.Errorf("%s: %d %v", name, code, r)
		}
	}
	if code, r := set(owner, map[string]any{"folder": "Nowhere"}); code != 404 {
		t.Errorf("a folder that is not there: %d %v", code, r)
	}
	if code, r := set(owner, map[string]any{"folder": "../x"}); code != 400 {
		t.Errorf("a path that is no folder: %d %v", code, r)
	}
	if code, r := owner("PUT", base+"/daily/settings", map[string]any{}); code != 400 {
		t.Errorf("no daily key at all: %d %v", code, r)
	}
	if id, r := dailyID(t, viewer, base, "date=2026-11-23"); id != "" || r["reason"] != "no_source" {
		t.Fatalf("a refused change changed something: %v", r)
	}

	// --- a folder as the source
	code, r := set(owner, map[string]any{"folder": "文案"})
	if code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	if d := r["daily"].(map[string]any); d["folder"] != "文案" || len(d["filter"].([]any)) != 0 || len(d) != 2 {
		t.Fatalf("%v", r)
	}
	if code, g := viewer("GET", base+"/daily/settings", nil); code != 200 || fmt.Sprint(g) != fmt.Sprint(r) {
		t.Fatalf("%d %v", code, g)
	}
	if spaceYML != nil {
		if y := spaceYML(); !strings.Contains(y, "daily:") || !strings.Contains(y, "folder: 文案") {
			t.Errorf("space.yml:\n%s", y)
		}
	}
	id0, got := dailyID(t, viewer, base, "date=2026-11-23")
	page := got["page"].(map[string]any)
	if id0 == "" || got["count"] != float64(5) || page["folder"] != "文案" || page["label"] == "" || page["body"] == "" || page["bodyTruncated"] != false {
		t.Fatalf("%v", got)
	}
	// The same day is the same page, for everybody and every time.
	for _, c := range []caller{viewer, editor, owner, viewer} {
		if id, _ := dailyID(t, c, base, "date=2026-11-23&skip=0"); id != id0 {
			t.Fatalf("the same day gave %s and %s", id0, id)
		}
	}
	// Another day is another page now and then, and every one is in the folder.
	seen := map[string]bool{}
	for _, d := range days(30) {
		id, _ := dailyID(t, viewer, base, "date="+d)
		if id == "" || (id != ids["甲"] && id != ids["乙"] && id != ids["丙"] && id != ids["丁"] && id != ids["戊"]) {
			t.Fatalf("%s: %s is not a page of the folder", d, id)
		}
		seen[id] = true
	}
	if len(seen) < 3 {
		t.Errorf("30 days showed only %d of 5 pages", len(seen))
	}
	// skip moves along, and wraps round.
	a, _ := dailyID(t, viewer, base, "date=2026-11-23&skip=1")
	b, _ := dailyID(t, viewer, base, "date=2026-11-23&skip=5")
	if a == id0 || b != id0 {
		t.Errorf("skip 1 gave %s and skip 5 gave %s, the day itself %s", a, b, id0)
	}
	// What a request may not ask.
	for _, q := range []string{"date=tomorrow", "date=2026-13-01", "date=1969-12-31", "date=2026-11-23&skip=-1", "date=2026-11-23&skip=x", "date=2026-11-23&skip=99999999"} {
		want := "invalid"
		if strings.HasSuffix(q, "99999999") {
			want = "skip_too_large"
		}
		if code, r := viewer("GET", base+"/daily?"+q, nil); code != 400 || r["error"] != want {
			t.Errorf("%s: %d %v", q, code, r)
		}
	}
	// Without a date it is today's.
	if id, _ := dailyID(t, viewer, base, ""); id == "" {
		t.Error("no date")
	}

	// --- pages added later do not change what the day showed
	for i := 0; i < 12; i++ {
		mk("文案", fmt.Sprintf("新%d", i), "n\n", nil)
	}
	if id, r := dailyID(t, viewer, base, "date=2026-11-23"); id != id0 {
		t.Fatalf("a day changed when pages were added: %s -> %s (%v)", id0, id, r["count"])
	}
	// A page that is gone is picked again, and the new pick stays.
	if code, r := editor("DELETE", base+"/pages/"+id0, nil); code != 204 {
		t.Fatalf("%d %v", code, r)
	}
	id1, _ := dailyID(t, viewer, base, "date=2026-11-23")
	if id1 == "" || id1 == id0 {
		t.Fatalf("after the delete: %s", id1)
	}
	if id, _ := dailyID(t, owner, base, "date=2026-11-23"); id != id1 {
		t.Fatalf("the new pick did not stay: %s %s", id1, id)
	}

	// --- filters
	if code, r := set(owner, map[string]any{"folder": "文案", "filter": []any{map[string]any{"key": "status", "op": "in", "values": []any{"定稿"}}}}); code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	finals := map[string]bool{ids["甲"]: true, ids["丙"]: true, ids["戊"]: true}
	delete(finals, id0) // that one was deleted above
	for _, d := range days(20) {
		id, r := dailyID(t, viewer, base, "date="+d)
		if !finals[id] || r["count"] != float64(len(finals)) {
			t.Fatalf("%s: %s %v", d, id, r["count"])
		}
	}
	// No page meets it.
	if code, r := set(owner, map[string]any{"folder": "文案", "filter": []any{map[string]any{"key": "status", "op": "in", "values": []any{"没有"}}}}); code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	if id, r := dailyID(t, viewer, base, "date=2026-11-23"); id != "" || r["reason"] != "no_match" {
		t.Fatalf("no match: %v", r)
	}

	// --- a tag, and the body cut at 2000 characters
	long := strings.Repeat("长", 1999) + "AB" + strings.Repeat("x", 500) + "\n"
	longID := mk("别处", "长文", long, map[string]any{"tags": []any{"长"}})
	if code, r := set(owner, map[string]any{"tag": "长"}); code != 200 || r["daily"].(map[string]any)["tag"] != "长" {
		t.Fatalf("%d %v", code, r)
	}
	id, got := dailyID(t, viewer, base, "date=2026-11-23")
	page = got["page"].(map[string]any)
	body := page["body"].(string)
	if id != longID || page["bodyTruncated"] != true || len([]rune(body)) != 2000 || !strings.HasSuffix(body, "长A") {
		t.Fatalf("%s truncated=%v, %d characters", id, page["bodyTruncated"], len([]rune(body)))
	}
	// Exactly 2000 is not cut.
	exact := mk("别处", "整", strings.Repeat("字", 2000), map[string]any{"tags": []any{"整"}})
	if code, _ := set(owner, map[string]any{"tag": "整"}); code != 200 {
		t.Fatal(code)
	}
	if id, got := dailyID(t, viewer, base, "date=2026-11-23"); id != exact || got["page"].(map[string]any)["bodyTruncated"] != false {
		t.Fatalf("%v", got)
	}
	if code, _ := set(owner, map[string]any{"tag": "没有这个标签"}); code != 200 {
		t.Fatal(code)
	}
	if id, r := dailyID(t, viewer, base, "date=2026-11-23"); id != "" || r["reason"] != "no_match" {
		t.Fatalf("%v", r)
	}

	// --- a type: the header's, and the folder's
	spec := map[string]any{"title": "文案", "fields": []any{map[string]any{"key": "status", "kind": "select", "options": []any{"草稿", "定稿"}}}}
	if code, r := owner("PUT", base+"/types/wenan", spec); code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	if code, r := owner("PUT", base+"/folders/type", map[string]any{"folder": "文案", "type": "wenan"}); code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	own := mk("别处", "自带类型", "t\n", map[string]any{"type": "wenan"})
	if code, r := set(owner, map[string]any{"type": "wenan"}); code != 200 || r["daily"].(map[string]any)["type"] != "wenan" {
		t.Fatalf("%d %v", code, r)
	}
	// 4 left of the 5, the 12 new ones, and the one with the type in its header.
	if _, r := dailyID(t, viewer, base, "date=2026-11-23"); r["count"] != float64(4+12+1) {
		t.Fatalf("a type: %v", r["count"])
	}
	// Every page of the type is reached by skip (a day alone may miss one).
	seen = map[string]bool{}
	for k := 0; k < 17; k++ {
		id, _ := dailyID(t, viewer, base, fmt.Sprintf("date=2026-11-23&skip=%d", k))
		seen[id] = true
	}
	if !seen[own] || len(seen) != 17 {
		t.Errorf("skip 0..16 showed %d pages, the page with the header type among them: %v", len(seen), seen[own])
	}
	// A header that says another type takes the page out of the folder's.
	other := mk("文案", "另一类", "o\n", map[string]any{"type": "task"})
	if _, r := dailyID(t, viewer, base, "date=2026-11-23"); r["count"] != float64(4+12+1) {
		t.Fatalf("a page of another type is counted: %v (%s)", r["count"], other)
	}

	// --- taking it off
	if code, r := set(owner, nil); code != 200 || r["daily"] != nil {
		t.Fatalf("%d %v", code, r)
	}
	if id, r := dailyID(t, viewer, base, "date=2026-11-23"); id != "" || r["reason"] != "no_source" {
		t.Fatalf("%v", r)
	}
	if code, r := set(owner, nil); code != 200 || r["daily"] != nil {
		t.Fatalf("taking off what is not there: %d %v", code, r)
	}
	if spaceYML != nil && strings.Contains(spaceYML(), "daily:") {
		t.Errorf("space.yml:\n%s", spaceYML())
	}

	// --- a folder moved takes the source with it
	if code, _ := set(owner, map[string]any{"folder": "文案"}); code != 200 {
		t.Fatal(code)
	}
	if code, r := owner("POST", base+"/folders/move", map[string]any{"from": "文案", "to": "歌词"}); code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	if code, r := viewer("GET", base+"/daily/settings", nil); code != 200 || r["daily"].(map[string]any)["folder"] != "歌词" {
		t.Fatalf("the source after the move: %d %v", code, r)
	}
	if id, _ := dailyID(t, viewer, base, "date=2026-11-24"); id == "" {
		t.Fatal("nothing is picked from the moved folder")
	}
}

func manyFilters(n int) []any {
	var out []any
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{"key": fmt.Sprintf("k%d", i), "op": "notEmpty", "values": []any{}})
	}
	return out
}

func TestDailyInAccountsMode(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid
		dailyScenario(t, base, asClient(tm.owner), asClient(tm.editor), asClient(tm.viewer), true, nil)
		// A person who is not in the space does not find it.
		for _, rt := range [][3]string{{"GET", "/daily", ""}, {"GET", "/daily/settings", ""}, {"PUT", "/daily/settings", "{}"}} {
			if r := tm.outsider.do(rt[0], base+rt[1], rt[2]); r.code != 404 {
				t.Errorf("%v: %d %s", rt, r.code, r.raw)
			}
		}
	})
}

// Two spaces with the same pages do not pick the same page every day.
func TestDailyDiffersBetweenSpaces(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		o := a.addUser("olive", pw, "member")
		var picks []string
		for i := 0; i < 2; i++ {
			sid := o.do("POST", "/api/spaces", map[string]any{"name": fmt.Sprintf("S%d", i)}).want(t, 201, "").body["id"].(string)
			base := "/api/spaces/" + sid
			c := asClient(o)
			for j := 0; j < 40; j++ {
				if code, r := c("POST", base+"/pages", map[string]any{"folder": "f", "meta": map[string]any{"title": fmt.Sprint(j)}, "body": "x\n"}); code != 201 {
					t.Fatalf("%d %v", code, r)
				}
			}
			if code, r := c("PUT", base+"/daily/settings", map[string]any{"daily": map[string]any{"folder": "f"}}); code != 200 {
				t.Fatalf("%d %v", code, r)
			}
			var line []string
			for _, d := range days(10) {
				_, r := c("GET", base+"/daily?date="+d, nil)
				line = append(line, r["page"].(map[string]any)["title"].(string))
			}
			picks = append(picks, strings.Join(line, ","))
		}
		if picks[0] == picks[1] {
			t.Errorf("two spaces picked alike: %s", picks[0])
		}
	})
}

func TestDailyInSingleUserMode(t *testing.T) {
	f := newFixture(t)
	c := asFixture(f)
	dailyScenario(t, base, c, c, c, false, func() string {
		b, _ := os.ReadFile(filepath.Join(f.dir, ".petalbook", "space.yml"))
		return string(b)
	})
}

// The source is read from the settings written by hand, comments stay when the
// server writes it, and a file that cannot be used still gives the last source
// that could be read.
func TestDailySettingsFile(t *testing.T) {
	f := newFixture(t)
	c := asFixture(f)
	if code, r := c("POST", base+"/folders", map[string]any{"path": "文案"}); code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	file := filepath.Join(f.dir, ".petalbook", "space.yml")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	hand := "# my space\nname: 测试空间\ndaily:\n  # where it comes from\n  folder: 文案  # the poems\n  note: keep this\nfolders:\n  文案:\n    type: x\n"
	if err := os.WriteFile(file, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, r := c("GET", base+"/daily/settings", nil); code != 200 || r["daily"].(map[string]any)["folder"] != "文案" {
		t.Fatalf("%d %v", code, r)
	}
	// Setting the same thing writes nothing.
	if code, _ := c("PUT", base+"/daily/settings", map[string]any{"daily": map[string]any{"folder": "文案"}}); code != 200 {
		t.Fatal(code)
	}
	if b, _ := os.ReadFile(file); string(b) != hand {
		t.Fatalf("an unchanged source rewrote the file:\n%s", b)
	}
	if code, r := c("PUT", base+"/daily/settings", map[string]any{"daily": map[string]any{"tag": "诗"}}); code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	b, _ := os.ReadFile(file)
	for _, want := range []string{"# my space", "# where it comes from", "note: keep this", "tag: 诗", "文案:", "type: x"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%q is gone:\n%s", want, b)
		}
	}
	if strings.Contains(string(b), "folder: 文案  #") {
		t.Errorf("the old source stays:\n%s", b)
	}
	// A file that cannot be read is read as the version before it.
	if err := os.WriteFile(file, []byte("name: x\ndaily: [unclosed\nfolders: {{\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Nothing is committed yet, so there is no version to go back to: no source, and the space works.
	if code, r := c("GET", base+"/daily/settings", nil); code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	if code, r := c("PUT", base+"/daily/settings", map[string]any{"daily": map[string]any{"folder": "文案"}}); code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	b, _ = os.ReadFile(file)
	if !strings.Contains(string(b), "folder: 文案") || strings.Contains(string(b), "unclosed") {
		t.Fatalf("%s", b)
	}
	matches, _ := filepath.Glob(file + ".unreadable-*")
	if len(matches) != 1 {
		t.Fatalf("the file as it was is kept: %v", matches)
	}
}

// --- favourites -------------------------------------------------------------------

func favList(t *testing.T, c caller, base string) []map[string]any {
	t.Helper()
	code, r := c("GET", base+"/favorites", nil)
	if code != 200 || r["limit"] != float64(200) {
		t.Fatalf("favourites: %d %v", code, r)
	}
	return favItems(t, r)
}

func favItems(t *testing.T, r map[string]any) []map[string]any {
	t.Helper()
	items, ok := r["items"].([]any)
	if !ok {
		t.Fatalf("no items in %v", r)
	}
	var out []map[string]any
	for _, it := range items {
		out = append(out, it.(map[string]any))
	}
	return out
}

// A folder move whose request did not see the favourites through (a crash, a
// lost connection, a failure of the state database) is finished the next time
// the favourites are read; a recorded move that did not happen is forgotten.
func TestFavoritesFollowAMoveTheRequestDidNotFinish(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid
		o, v := asClient(tm.owner), asClient(tm.viewer)
		for _, p := range []string{"Poems", "Stays"} {
			if code, r := o("POST", base+"/folders", map[string]any{"path": p}); code != 201 {
				t.Fatalf("%d %v", code, r)
			}
		}
		for _, p := range []string{"Poems", "Stays"} {
			if code, r := v("POST", base+"/favorites", map[string]any{"kind": "folder", "target": p}); code != 200 {
				t.Fatalf("%d %v", code, r)
			}
		}
		// The move is recorded and made, and the server stops before the favourites follow.
		ctx := context.Background()
		if err := store.RecordFolderMove(ctx, a.st.Q(), tm.sid, "Poems", "Verse", time.Now()); err != nil {
			t.Fatal(err)
		}
		sp, release, err := a.mgr.Acquire(ctx, tm.sid)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sp.MoveFolder(space.Author{}, "Poems", "Verse"); err != nil {
			t.Fatal(err)
		}
		release()
		// A move recorded long ago whose folder is still where it was did not happen;
		// one recorded just now may be under way.
		old := time.Now().Add(-time.Hour)
		store.RecordFolderMove(ctx, a.st.Q(), tm.sid, "Stays", "Gone", old)
		store.RecordFolderMove(ctx, a.st.Q(), tm.sid, "Stays", "Soon", time.Now())
		if got := favLine(favList(t, v, base)); got != "folder:Verse folder:Stays" {
			t.Fatalf("%s", got)
		}
		l, err := store.PendingFolderMoves(ctx, a.st.Q(), tm.sid)
		if err != nil || len(l) != 1 || l[0].To != "Soon" {
			t.Fatalf("%v %v", l, err)
		}
		// A move through the request leaves no record behind.
		if code, r := o("POST", base+"/folders/move", map[string]any{"from": "Verse", "to": "Rhyme"}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		if l, _ := store.PendingFolderMoves(ctx, a.st.Q(), tm.sid); len(l) != 1 {
			t.Fatalf("%v", l)
		}
		// A move that is refused leaves none either.
		if code, _ := o("POST", base+"/folders/move", map[string]any{"from": "Rhyme", "to": "Stays"}); code == 200 {
			t.Fatal("a move onto a folder that is there")
		}
		if l, _ := store.PendingFolderMoves(ctx, a.st.Q(), tm.sid); len(l) != 1 {
			t.Fatalf("%v", l)
		}
		if got := favLine(favList(t, v, base)); got != "folder:Rhyme folder:Stays" {
			t.Fatalf("%s", got)
		}
	})
}

func favLine(items []map[string]any) string {
	var out []string
	for _, it := range items {
		s := fmt.Sprintf("%s:%v", it["kind"], it["name"])
		if it["missing"] == true {
			s += "!"
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

func favURL(base, kind, target string) string {
	return base + "/favorites?kind=" + kind + "&target=" + url.QueryEscape(target)
}

// favoritesScenario is what both modes do with a person's favourites.
func favoritesScenario(t *testing.T, base string, owner, editor, viewer caller, refusals bool) (pageA, pageB string) {
	t.Helper()
	mk := func(title string) string {
		t.Helper()
		code, r := editor("POST", base+"/pages", map[string]any{"folder": "唐诗", "meta": map[string]any{"title": title}, "body": "x\n"})
		if code != 201 {
			t.Fatalf("%d %v", code, r)
		}
		return r["id"].(string)
	}
	if code, r := owner("POST", base+"/folders", map[string]any{"path": "唐诗/七言"}); code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	pageA, pageB = mk("暮雪"), mk("春晓")
	// A saved view to favourite.
	code, r := owner("PUT", base+"/views", map[string]any{"folder": "唐诗", "baseRev": viewsRev(t, owner, base, "folder="+url.QueryEscape("唐诗")),
		"views": []any{map[string]any{"name": "按作者", "layout": "table"}}})
	if code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	viewID := r["views"].([]any)[0].(map[string]any)["id"].(string)
	viewT := "folder:唐诗#" + viewID

	if got := favList(t, viewer, base); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	post := func(c caller, kind, target string) (int, map[string]any) {
		return c("POST", base+"/favorites", map[string]any{"kind": kind, "target": target})
	}
	// Anybody who can read keeps favourites, their own.
	for _, c := range []caller{viewer, editor} {
		if code, r := post(c, "page", pageA); code != 200 || favLine(favItems(t, r)) != "page:暮雪" {
			t.Fatalf("%d %v", code, r)
		}
	}
	for _, kt := range [][2]string{{"folder", "唐诗/七言"}, {"view", viewT}, {"page", pageB}} {
		if code, r := post(viewer, kt[0], kt[1]); code != 200 {
			t.Fatalf("%v: %d %v", kt, code, r)
		}
	}
	// Adding again changes nothing, not even the place.
	code, r = post(viewer, "page", pageA)
	if code != 200 || favLine(favItems(t, r)) != "page:暮雪 folder:七言 view:按作者 page:春晓" {
		t.Fatalf("%d %s", code, favLine(favItems(t, r)))
	}
	items := favItems(t, r)
	if items[0]["target"] != pageA || items[0]["path"] != "唐诗/"+"暮雪.md" || items[0]["missing"] != false || items[0]["created"] == "" {
		t.Errorf("%v", items[0])
	}
	if items[1]["path"] != "唐诗/七言" || items[2]["of"] != "唐诗" || items[2]["ofKind"] != "folder" || items[2]["target"] != viewT {
		t.Errorf("%v %v", items[1], items[2])
	}
	// The editor's list is not the viewer's.
	if got := favLine(favList(t, editor, base)); refusals && got != "page:暮雪" {
		t.Fatalf("%s", got)
	}
	// What cannot be a favourite.
	for name, body := range map[string]map[string]any{
		"no such page":      {"kind": "page", "target": "01JD3K8Q7Z"},
		"not an id":         {"kind": "page", "target": "../x"},
		"no such folder":    {"kind": "folder", "target": "Nowhere"},
		"the root":          {"kind": "folder", "target": ""},
		"a bad kind":        {"kind": "star", "target": "x"},
		"no such view":      {"kind": "view", "target": "folder:唐诗#nope"},
		"a view of nowhere": {"kind": "view", "target": "folder:Nowhere#x"},
		"not a view":        {"kind": "view", "target": "唐诗"},
	} {
		code, r := viewer("POST", base+"/favorites", body)
		if code != 400 && code != 404 {
			t.Errorf("%s: %d %v", name, code, r)
		}
	}
	if code, _ := viewer("POST", base+"/favorites", map[string]any{"kind": "page"}); code != 400 {
		t.Errorf("no target: %d", code)
	}
	if got := favLine(favList(t, viewer, base)); got != "page:暮雪 folder:七言 view:按作者 page:春晓" {
		t.Fatalf("a refused favourite changed the list: %s", got)
	}

	// --- the order
	order := func(c caller, items ...[2]string) (int, map[string]any) {
		l := []any{}
		for _, it := range items {
			l = append(l, map[string]any{"kind": it[0], "target": it[1]})
		}
		return c("PUT", base+"/favorites/order", map[string]any{"items": l})
	}
	code, r = order(viewer, [2]string{"page", pageB}, [2]string{"view", viewT}, [2]string{"page", "gone"}, [2]string{"page", pageA}, [2]string{"page", pageB})
	if code != 200 || favLine(favItems(t, r)) != "page:春晓 view:按作者 page:暮雪 folder:七言" {
		t.Fatalf("a partial order: %d %s", code, favLine(favItems(t, r)))
	}
	if got := favLine(favList(t, viewer, base)); got != "page:春晓 view:按作者 page:暮雪 folder:七言" {
		t.Fatalf("%s", got)
	}
	if code, _ := viewer("PUT", base+"/favorites/order", map[string]any{}); code != 400 {
		t.Errorf("no items: %d", code)
	}
	if code, _ := viewer("PUT", base+"/favorites/order", map[string]any{"items": []any{map[string]any{"kind": "star", "target": "x"}}}); code != 400 {
		t.Errorf("a bad kind: %d", code)
	}
	if got := favLine(favList(t, editor, base)); refusals && got != "page:暮雪" {
		t.Fatalf("the editor's list moved: %s", got)
	}

	// --- what is gone shows as missing, and can be taken off
	if code, r := editor("DELETE", base+"/pages/"+pageB, nil); code != 204 {
		t.Fatalf("%d %v", code, r)
	}
	if code, r := owner("PUT", base+"/views", map[string]any{"folder": "唐诗", "baseRev": viewsRev(t, owner, base, "folder="+url.QueryEscape("唐诗")), "views": []any{}}); code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	if code, r := owner("DELETE", base+"/folders?path="+url.QueryEscape("唐诗/七言"), nil); code != 204 {
		t.Fatalf("%d %v", code, r)
	}
	items = favList(t, viewer, base)
	if len(items) != 4 || favLine(items) != "page:! view:! page:暮雪 folder:七言!" {
		t.Fatalf("%s", favLine(items))
	}
	for _, it := range items {
		if it["kind"] == "page" && it["target"] == pageA && it["missing"] != false {
			t.Errorf("%v", it)
		}
	}
	for _, kt := range [][2]string{{"page", pageB}, {"view", viewT}, {"folder", "唐诗/七言"}} {
		code, r := viewer("DELETE", favURL(base, kt[0], kt[1]), nil)
		if code != 200 {
			t.Fatalf("%v: %d %v", kt, code, r)
		}
	}
	if got := favLine(favList(t, viewer, base)); got != "page:暮雪" {
		t.Fatalf("%s", got)
	}
	// Taking off what is not there is fine; asking badly is not.
	if code, _ := viewer("DELETE", favURL(base, "page", pageB), nil); code != 200 {
		t.Errorf("a second delete: %d", code)
	}
	for _, q := range []string{"", "?kind=page", "?target=x", "?kind=star&target=x"} {
		if code, _ := viewer("DELETE", base+"/favorites"+q, nil); code != 400 {
			t.Errorf("delete %q: %d", q, code)
		}
	}
	return pageA, pageB
}

func viewsRev(t *testing.T, c caller, base, query string) string {
	t.Helper()
	code, r := c("GET", base+"/views?"+query, nil)
	if code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	return r["rev"].(string)
}

// favouriteFolderScenario: a folder and its views are followed by their favourites when it moves.
func favouriteFolderScenario(t *testing.T, base string, owner, viewer caller) {
	t.Helper()
	if code, r := owner("POST", base+"/folders", map[string]any{"path": "Poems/Long"}); code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	code, r := owner("PUT", base+"/views", map[string]any{"folder": "Poems/Long", "baseRev": viewsRev(t, owner, base, "folder=Poems%2FLong"),
		"views": []any{map[string]any{"name": "wide", "layout": "cards"}}})
	if code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	vid := r["views"].([]any)[0].(map[string]any)["id"].(string)
	for _, kt := range [][2]string{{"folder", "Poems"}, {"folder", "Poems/Long"}, {"view", "folder:Poems/Long#" + vid}} {
		if code, r := viewer("POST", base+"/favorites", map[string]any{"kind": kt[0], "target": kt[1]}); code != 200 {
			t.Fatalf("%v: %d %v", kt, code, r)
		}
	}
	if code, r := owner("POST", base+"/folders/move", map[string]any{"from": "Poems", "to": "Verse"}); code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	items := favList(t, viewer, base)
	if favLine(items) != "page:暮雪 folder:Verse folder:Long view:wide" || items[1]["path"] != "Verse" || items[2]["path"] != "Verse/Long" || items[3]["target"] != "folder:Verse/Long#"+vid {
		t.Fatalf("after the move: %s %v", favLine(items), items)
	}
	// Renamed in place, spelled differently.
	if code, r := owner("POST", base+"/folders/move", map[string]any{"from": "Verse/Long", "to": "Verse/Epic"}); code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	if got := favLine(favList(t, viewer, base)); got != "page:暮雪 folder:Verse folder:Epic view:wide" {
		t.Fatalf("after the rename: %s", got)
	}
}

func TestFavoritesInAccountsMode(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		boss := a.setup("kazuha", pw) // an administrator, a member of nothing but their own space
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid
		o, e, v := asClient(tm.owner), asClient(tm.editor), asClient(tm.viewer)
		pageA, _ := favoritesScenario(t, base, o, e, v, true)
		favouriteFolderScenario(t, base, o, v)
		// Another person's favourites in the same space moved with the folder too.
		if got := favLine(favList(t, e, base)); !strings.HasPrefix(got, "page:") {
			t.Fatalf("%s", got)
		}
		if code, _ := e("POST", base+"/favorites", map[string]any{"kind": "folder", "target": "Verse"}); code != 200 {
			t.Fatal(code)
		}
		if code, r := o("POST", base+"/folders/move", map[string]any{"from": "Verse", "to": "Rhyme"}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		for name, c := range map[string]caller{"editor": e, "viewer": v} {
			items := favList(t, c, base)
			found := false
			for _, it := range items {
				if it["kind"] == "folder" && it["path"] == "Rhyme" && it["missing"] == false {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: %s", name, favLine(items))
			}
		}
		// A person who is not in the space does not find them.
		for _, rt := range [][3]string{{"GET", "/favorites", ""}, {"POST", "/favorites", "{}"}, {"DELETE", "/favorites?kind=page&target=x", ""}, {"PUT", "/favorites/order", "{}"}} {
			if r := tm.outsider.do(rt[0], base+rt[1], rt[2]); r.code != 404 {
				t.Errorf("%v: %d %s", rt, r.code, r.raw)
			}
		}

		// --- a member who is removed: hidden, kept, and back with them
		ctx := context.Background()
		before, err := store.ListFavorites(ctx, a.st.Q(), tm.viewerID, tm.sid)
		if err != nil || len(before) == 0 {
			t.Fatalf("%v %v", before, err)
		}
		tm.owner.do("DELETE", base+"/members/"+tm.viewerID, nil).want(t, 204, "")
		tm.viewer.do("GET", base+"/favorites", nil).want(t, 404, "not_found")
		kept, err := store.ListFavorites(ctx, a.st.Q(), tm.viewerID, tm.sid)
		if err != nil || len(kept) != len(before) {
			t.Fatalf("favourites of a removed member: %d, were %d (%v)", len(kept), len(before), err)
		}
		// Not shown to the administrator who opens the space either, whoever they are.
		if r := boss.do("GET", base+"/favorites", nil).want(t, 200, ""); len(favItems(t, r.body)) != 0 {
			t.Fatalf("%s", r.raw)
		}
		boss.do("POST", base+"/favorites", map[string]any{"kind": "page", "target": pageA}).want(t, 403, "forbidden")
		tm.owner.do("POST", base+"/members", map[string]any{"username": "victor", "role": "viewer"}).want(t, 201, "")
		if got := favList(t, v, base); len(got) != len(before) {
			t.Fatalf("back in the space: %s", favLine(got))
		}

		// --- deleting the space deletes them
		boss.do("DELETE", "/api/admin/spaces/"+tm.sid, nil).want(t, 200, "")
		var n int
		a.st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM favorites WHERE space_id = ?`, tm.sid).Scan(&n)
		if n != 0 {
			t.Fatalf("%d favourites of a deleted space", n)
		}
	})
}

func TestFavoritesAreKeptPerSpaceAndPerson(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		o := a.addUser("olive", pw, "member")
		oc := asClient(o)
		s1 := o.personalSpace()
		s2 := o.do("POST", "/api/spaces", map[string]any{"name": "Two"}).want(t, 201, "").body["id"].(string)
		var pid string
		for _, s := range []string{s1, s2} {
			code, r := oc("POST", "/api/spaces/"+s+"/pages", map[string]any{"meta": map[string]any{"title": "T " + s}, "body": "x\n"})
			if code != 201 {
				t.Fatalf("%d %v", code, r)
			}
			pid = r["id"].(string)
			if code, _ := oc("POST", "/api/spaces/"+s+"/favorites", map[string]any{"kind": "page", "target": pid}); code != 200 {
				t.Fatal(code)
			}
		}
		for _, s := range []string{s1, s2} {
			if got := favList(t, oc, "/api/spaces/"+s); len(got) != 1 || got[0]["name"] != "T "+s {
				t.Fatalf("%s: %v", s, got)
			}
		}
		// A page of the other space is not a favourite here.
		if code, _ := oc("POST", "/api/spaces/"+s1+"/favorites", map[string]any{"kind": "page", "target": pid}); code != 404 {
			t.Errorf("a page of another space: %d", code)
		}
	})
}

func TestFavoritesInSingleUserMode(t *testing.T) {
	f := newFixture(t)
	c := asFixture(f)
	favoritesScenario(t, base, c, c, c, false)
	favouriteFolderScenario(t, base, c, c)
	// 200 at most.
	ctx := context.Background()
	have := len(favList(t, c, base))
	code, r := c("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "last"}, "body": "x\n"})
	if code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	last := r["id"].(string)
	for i := 0; i < store.MaxFavorites-have-1; i++ {
		if _, err := f.opts.Store.Q().Exec(ctx, `INSERT INTO favorites(user_id, space_id, kind, target, position, created) VALUES (?, 'default', 'page', ?, ?, 0)`,
			store.LocalUserID, fmt.Sprintf("filler%03d", i), 1000+i); err != nil {
			t.Fatal(err)
		}
	}
	if code, r := c("POST", base+"/favorites", map[string]any{"kind": "page", "target": last}); code != 200 || len(favItems(t, r)) != store.MaxFavorites {
		t.Fatalf("the 200th: %d %d", code, len(favItems(t, r)))
	}
	code, r = c("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "one more"}, "body": "x\n"})
	if code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	if code, r := c("POST", base+"/favorites", map[string]any{"kind": "page", "target": r["id"]}); code != 400 || r["error"] != "invalid" || !strings.Contains(r["message"].(string), "200") {
		t.Fatalf("the 201st: %d %v", code, r)
	}
	// One that is there already is not "one more".
	if code, _ := c("POST", base+"/favorites", map[string]any{"kind": "page", "target": last}); code != 200 {
		t.Fatalf("%d", code)
	}
	if got := len(favList(t, c, base)); got != store.MaxFavorites {
		t.Fatalf("%d", got)
	}
	// The missing ones are listed in one request that does not read files: 200 favourites, one answer.
	if code, _ := c("DELETE", favURL(base, "page", "filler000"), nil); code != 200 {
		t.Fatal(code)
	}
}

// Recorded moves are settled by following them: a chain takes the favourites
// of the first folder to the last place, whatever order the records are
// settled in and however old they are, and a folder made where one was moved
// from keeps none of the old one's favourites. A settlement that fails changes
// nothing, and is done in full later.
func TestUnsettledFolderMovesAreFollowedInChains(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid
		o, v := asClient(tm.owner), asClient(tm.viewer)
		ctx := context.Background()
		mkdir := func(p string) {
			t.Helper()
			if code, r := o("POST", base+"/folders", map[string]any{"path": p}); code != 201 {
				t.Fatalf("%s: %d %v", p, code, r)
			}
		}
		fav := func(p string) {
			t.Helper()
			if code, r := v("POST", base+"/favorites", map[string]any{"kind": "folder", "target": p}); code != 200 {
				t.Fatalf("%s: %d %v", p, code, r)
			}
		}
		// A move made on disk whose favourites did not follow (a crash).
		crashMove := func(from, to string, at time.Time) {
			t.Helper()
			if err := store.RecordFolderMove(ctx, a.st.Q(), tm.sid, from, to, at); err != nil {
				t.Fatal(err)
			}
			sp, release, err := a.mgr.Acquire(ctx, tm.sid)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if _, err := sp.MoveFolder(space.Author{}, from, to); err != nil {
				t.Fatal(err)
			}
		}
		pending := func() int {
			l, err := store.PendingFolderMoves(ctx, a.st.Q(), tm.sid)
			if err != nil {
				t.Fatal(err)
			}
			return len(l)
		}
		now := time.Now()

		// A to B is not settled; B to C, through the request, carries A's favourites to C.
		mkdir("A")
		mkdir("A/Sub")
		fav("A")
		fav("A/Sub")
		crashMove("A", "B", now.Add(-time.Second))
		if code, r := o("POST", base+"/folders/move", map[string]any{"from": "B", "to": "C"}); code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		if got := favLine(favList(t, v, base)); got != "folder:C folder:Sub" || pending() != 0 {
			t.Fatalf("%s (%d pending)", got, pending())
		}

		// Two moves of a chain both unsettled, the second one just now: settled
		// on the next read, in order.
		mkdir("D")
		fav("D")
		crashMove("D", "E", now.Add(-time.Hour))
		crashMove("E", "F", now)
		if got := favLine(favList(t, v, base)); got != "folder:C folder:Sub folder:F" || pending() != 0 {
			t.Fatalf("%s (%d pending)", got, pending())
		}

		// A folder made at the old place, just after: it keeps none of the old
		// favourites, and receives none when it is moved in turn.
		mkdir("G")
		fav("G")
		crashMove("G", "H", now)
		mkdir("G")
		crashMove("G", "I", now)
		if got := favLine(favList(t, v, base)); got != "folder:C folder:Sub folder:F folder:H" || pending() != 0 {
			t.Fatalf("%s (%d pending)", got, pending())
		}
		mkdir("J")
		fav("J")
		crashMove("J", "K", now)
		mkdir("J")
		if got := favLine(favList(t, v, base)); got != "folder:C folder:Sub folder:F folder:H folder:K" || pending() != 0 {
			t.Fatalf("%s (%d pending)", got, pending())
		}

		// A move that failed with its folder in place moves nothing, and is
		// forgotten once it is old; one onto a folder that is there is not even recorded.
		store.RecordFolderMove(ctx, a.st.Q(), tm.sid, "J", "Nowhere", now.Add(-time.Hour))
		if code, _ := o("POST", base+"/folders/move", map[string]any{"from": "J", "to": "K"}); code == 200 {
			t.Fatal("a move onto a folder that is there")
		}
		if got := favLine(favList(t, v, base)); got != "folder:C folder:Sub folder:F folder:H folder:K" || pending() != 0 {
			t.Fatalf("%s (%d pending)", got, pending())
		}

		// The favourites cannot be updated: the move is made, nothing of the
		// chain is settled, and all of it is on the next read.
		mkdir("L")
		fav("L")
		crashMove("L", "M", now.Add(-time.Minute))
		if _, err := a.st.Q().Exec(ctx, `ALTER TABLE favorites RENAME TO favorites_away`); err != nil {
			t.Fatal(err)
		}
		code, r := o("POST", base+"/folders/move", map[string]any{"from": "M", "to": "N"})
		if _, err := a.st.Q().Exec(ctx, `ALTER TABLE favorites_away RENAME TO favorites`); err != nil {
			t.Fatal(err)
		}
		if code != 200 || pending() != 2 {
			t.Fatalf("%d %v (%d pending)", code, r, pending())
		}
		if got := favLine(favList(t, v, base)); got != "folder:C folder:Sub folder:F folder:H folder:K folder:N" || pending() != 0 {
			t.Fatalf("%s (%d pending)", got, pending())
		}
	})
}
