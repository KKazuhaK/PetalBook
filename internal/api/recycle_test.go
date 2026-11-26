package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

// confirmFor asks for the token of an action over some ids.
func confirmFor(t *testing.T, c *client, action string, key string, ids ...string) string {
	t.Helper()
	r := c.do("POST", "/api/admin/confirm", map[string]any{"action": action, key: ids}).want(t, 200, "")
	return r.body["confirm"].(string)
}

func TestTheRecycleBinListsPagesAndFilters(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		var ids []string
		for _, name := range []string{"Alpha", "Beta", "Gamma"} {
			sid := tang.do("POST", "/api/spaces", map[string]any{"name": name}).want(t, 201, "").body["id"].(string)
			ids = append(ids, admin.do("DELETE", "/api/admin/spaces/"+sid, nil).want(t, 200, "").body["id"].(string))
			a.clock.Advance(time.Second)
		}
		// Newest first, a page at a time by a cursor that is never an offset.
		r := admin.do("GET", "/api/admin/recycle?limit=2", nil).want(t, 200, "")
		l := r.body["items"].([]any)
		if len(l) != 2 || l[0].(map[string]any)["id"] != ids[2] || l[1].(map[string]any)["id"] != ids[1] || r.body["next"] == "" {
			t.Fatalf("%s", r.raw)
		}
		r2 := admin.do("GET", "/api/admin/recycle?limit=2&cursor="+r.body["next"].(string), nil).want(t, 200, "")
		l2 := r2.body["items"].([]any)
		if len(l2) != 1 || l2[0].(map[string]any)["id"] != ids[0] || r2.body["next"] != "" {
			t.Fatalf("%s", r2.raw)
		}
		if q := admin.do("GET", "/api/admin/recycle?q=BET", nil).want(t, 200, "").body["items"].([]any); len(q) != 1 || q[0].(map[string]any)["space"].(map[string]any)["name"] != "Beta" {
			t.Fatalf("%v", q)
		}
		if q := admin.do("GET", "/api/admin/recycle?kind=user", nil).want(t, 200, "").body["items"].([]any); len(q) != 0 {
			t.Fatalf("%v", q)
		}
		admin.do("GET", "/api/admin/recycle?kind=other", nil).want(t, 400, "invalid")
		admin.do("GET", "/api/admin/recycle?cursor=nonsense", nil).want(t, 400, "invalid")
		admin.do("GET", "/api/admin/recycle?limit=0", nil).want(t, 400, "invalid")
		// A member has no part in it.
		tang.do("GET", "/api/admin/recycle", nil).want(t, 403, "forbidden")
		tang.do("POST", "/api/admin/recycle/"+ids[0]+"/restore", map[string]any{}).want(t, 403, "forbidden")

		// purgeAt follows recycle.days as it is now.
		first := l[0].(map[string]any)
		del, _ := time.Parse(time.RFC3339, first["deletedAt"].(string))
		purge, _ := time.Parse(time.RFC3339, first["purgeAt"].(string))
		if purge.Sub(del) != 30*24*time.Hour {
			t.Fatalf("%v", purge.Sub(del))
		}
		admin.do("PUT", "/api/admin/settings/recycle.days", map[string]any{"value": 7}).want(t, 200, "")
		first = admin.do("GET", "/api/admin/recycle?limit=1", nil).want(t, 200, "").body["items"].([]any)[0].(map[string]any)
		purge, _ = time.Parse(time.RFC3339, first["purgeAt"].(string))
		if purge.Sub(del) != 7*24*time.Hour {
			t.Fatalf("a shorter recycle.days did not move the date: %v", purge.Sub(del))
		}
		admin.do("PUT", "/api/admin/settings/recycle.days", map[string]any{"value": 0}).want(t, 400, "invalid")
		admin.do("PUT", "/api/admin/settings/recycle.days", map[string]any{"value": 366}).want(t, 400, "invalid")
	})
}

func TestPurgingEarlyNeedsASiteAdministratorAndAFreshConfirmation(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		mk := func(c *client, name string) string {
			sid := c.do("POST", "/api/spaces", map[string]any{"name": name}).want(t, 201, "").body["id"].(string)
			return f.root.do("DELETE", "/api/admin/spaces/"+sid, nil).want(t, 200, "").body["id"].(string)
		}
		one, two := mk(f.memA, "One"), mk(f.memA, "Two")

		// A tenant administrator may not free the server's disk early: not the
		// purge, and not even the token for it.
		f.admA.do("DELETE", "/api/admin/recycle/"+one+"?confirm=c_x", nil).want(t, 403, "forbidden")
		f.admA.do("POST", "/api/admin/confirm", map[string]any{"action": "purge", "items": []string{one}}).want(t, 403, "forbidden")
		f.root.do("DELETE", "/api/admin/recycle/"+one, nil).want(t, 400, "invalid") // no token
		f.root.do("DELETE", "/api/admin/recycle/"+one+"?confirm=c_nonsense", nil).want(t, 404, "not_found")

		tok := confirmFor(t, f.root, "purge", "items", one)
		// Bound to the caller, the action and the exact items.
		f.admA.do("DELETE", "/api/admin/recycle/"+one+"?confirm="+tok, nil).want(t, 403, "forbidden")
		f.root.do("DELETE", "/api/admin/recycle/"+two+"?confirm="+tok, nil).want(t, 404, "not_found")
		if r := f.root.do("POST", "/api/admin/confirm", map[string]any{"action": "purge", "items": []string{one, "0000000000"}}); r.code != 404 {
			t.Fatalf("an item that is not there was confirmed: %d %s", r.code, r.raw)
		}
		// It works once.
		f.root.do("DELETE", "/api/admin/recycle/"+one+"?confirm="+tok, nil).want(t, 202, "")
		waitPurges(t, a)
		f.root.do("DELETE", "/api/admin/recycle/"+one+"?confirm="+tok, nil).want(t, 404, "not_found")
		if n := f.countOf(`SELECT COUNT(*) FROM recycle_items WHERE id = '` + one + `'`); n != 0 {
			t.Fatalf("the item is still there")
		}
		// It lasts ten minutes.
		tok = confirmFor(t, f.root, "purge", "items", two)
		a.clock.Advance(10*time.Minute + time.Second)
		f.root.do("DELETE", "/api/admin/recycle/"+two+"?confirm="+tok, nil).want(t, 404, "not_found")
		// It is stale when what it was made from changed.
		tok = confirmFor(t, f.root, "purge", "items", two)
		if _, err := a.st.Q().Exec(context.Background(), `UPDATE recycle_items SET deleted_at = deleted_at - 1000 WHERE id = ?`, two); err != nil {
			t.Fatal(err)
		}
		f.root.do("DELETE", "/api/admin/recycle/"+two+"?confirm="+tok, nil).want(t, 409, "confirm_stale")
		// And a stale token is spent: asking again works.
		tok = confirmFor(t, f.root, "purge", "items", two)
		f.root.do("DELETE", "/api/admin/recycle/"+two+"?confirm="+tok, nil).want(t, 202, "")
		waitPurges(t, a)
		// Purging deletes the directory too, and is audited.
		a.auth.FlushAudit(context.Background())
		if rows := f.root.audit(t, "space_purged"); len(rows) != 2 {
			t.Fatalf("%v", rows)
		}
		// B's item is not purged by anyone of A; nothing of B changed.
		f.root.do("POST", "/api/admin/confirm", map[string]any{"action": "restore", "items": []string{f.itemB}}).want(t, 200, "")
	})
}

func TestRestoreIsNeverRefusedForAQuotaAndAnOwnerIsNeededWhenNoneIsLeft(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{maxSpaces: 2}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		li := a.addUser("li", pw, "member")
		sid := tang.do("POST", "/api/spaces", map[string]any{"name": "First"}).want(t, 201, "").body["id"].(string)
		tang.do("POST", "/api/spaces/"+sid+"/members", map[string]any{"username": "li", "role": "editor"}).want(t, 201, "")
		item := admin.do("DELETE", "/api/admin/spaces/"+sid, nil).want(t, 200, "").body["id"].(string)
		// The space does not count while it is in the bin: its place under the limit (the personal space and one more) is free.
		tang.do("POST", "/api/spaces", map[string]any{"name": "Second"}).want(t, 201, "")
		tang.do("POST", "/api/spaces", map[string]any{"name": "Third"}).want(t, 403, "")
		// ...and the restore is not refused for it.
		r := admin.do("POST", "/api/admin/recycle/"+item+"/restore", map[string]any{}).want(t, 200, "")
		if len(r.body["skipped"].([]any)) != 0 {
			t.Fatal(r.raw)
		}
		u := tang.do("GET", "/api/account/usage", nil).want(t, 200, "")
		if sp := u.body["spaces"].(map[string]any); sp["used"] != float64(3) {
			t.Fatalf("%v", u.raw)
		}
		li.do("GET", "/api/spaces/"+sid+"/tree", nil).want(t, 200, "")

		// The only owner is gone: an owner must be named.
		item = admin.do("DELETE", "/api/admin/spaces/"+sid, nil).want(t, 200, "").body["id"].(string)
		if _, err := a.st.Q().Exec(context.Background(), `UPDATE space_members SET role = 'editor'`); err != nil {
			t.Fatal(err)
		}
		if _, err := a.st.Q().Exec(context.Background(), `UPDATE recycle_items SET records = REPLACE(records, '"owner"', '"editor"') WHERE id = ?`, item); err != nil {
			t.Fatal(err)
		}
		admin.do("POST", "/api/admin/recycle/"+item+"/restore", map[string]any{}).want(t, 409, "owner_required")
		admin.do("POST", "/api/admin/recycle/"+item+"/restore", map[string]any{"owner": "0000000000"}).want(t, 404, "user_not_found")
		r = admin.do("POST", "/api/admin/recycle/"+item+"/restore", map[string]any{"owner": mustUserID(t, a, "li")}).want(t, 200, "")
		li.do("PATCH", "/api/spaces/"+sid, map[string]any{"name": "Renamed by the new owner"}).want(t, 200, "")
		admin.do("POST", "/api/admin/recycle/"+item+"/restore", map[string]any{}).want(t, 404, "not_found")
	})
}

func TestDeletionAndRestoreAreAudited(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		sid := tang.do("POST", "/api/spaces", map[string]any{"name": "Notes"}).want(t, 201, "").body["id"].(string)
		item := admin.do("DELETE", "/api/admin/spaces/"+sid, map[string]any{"reason": "left"}).want(t, 200, "").body["id"].(string)
		admin.do("POST", "/api/admin/recycle/"+item+"/restore", map[string]any{}).want(t, 200, "")
		for kind, want := range map[string]string{"space_deleted": "left", "space_restored": "left"} {
			rows := admin.audit(t, kind)
			if len(rows) != 1 {
				t.Fatalf("%s: %v", kind, rows)
			}
			d := rows[0]["detail"].(map[string]any)
			if d["item"] != item || d["space"] != sid || d["name"] != "Notes" || d["reason"] != want {
				t.Fatalf("%s: %v", kind, d)
			}
		}
	})
}

// --- viewing -----------------------------------------------------------------

func TestAnAdministratorReadsASpaceOnlyInAnAuditedViewAndNeverWrites(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		sid := f.memA.do("POST", "/api/spaces", map[string]any{"name": "Members' notes"}).want(t, 201, "").body["id"].(string)
		base := "/api/spaces/" + sid
		pg := f.memA.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Secret plan"}, "body": "the text\n"}).want(t, 201, "")
		id, rev := pg.body["id"].(string), pg.body["rev"].(string)

		// Not a member: no reading at all, with or without being an administrator;
		// what managing the people needs stays open.
		for _, c := range []*client{f.admA, f.root} {
			c.do("GET", base+"/pages", nil).want(t, 403, "no_permission")
			c.do("GET", base+"/pages/"+id, nil).want(t, 403, "no_permission")
			c.do("GET", base+"/tree", nil).want(t, 403, "no_permission")
			c.do("GET", base+"/files/assets/x.png", nil).want(t, 403, "no_permission")
			c.do("PUT", base+"/pages/"+id, map[string]any{"baseRev": rev, "meta": map[string]any{"title": "x"}, "body": "x"}).want(t, 403, "no_permission")
			c.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "x"}}).want(t, 403, "no_permission")
			c.do("GET", base+"/members", nil).want(t, 200, "")
		}
		// Opening a view needs a reason.
		f.admA.do("POST", "/api/admin/spaces/"+sid+"/view", map[string]any{}).want(t, 400, "invalid")
		f.admA.do("POST", "/api/admin/spaces/"+sid+"/view", map[string]any{"reason": "  "}).want(t, 400, "invalid")
		f.admA.do("POST", "/api/admin/spaces/"+sid+"/view", map[string]any{"reason": strings.Repeat("x", 501)}).want(t, 400, "invalid")
		f.memA.do("POST", "/api/admin/spaces/"+sid+"/view", map[string]any{"reason": "me"}).want(t, 403, "forbidden")

		start := a.clock.Now()
		r := f.admA.do("POST", "/api/admin/spaces/"+sid+"/view", map[string]any{"reason": "Checking a reported page"}).want(t, 200, "")
		until, _ := time.Parse(time.RFC3339, r.body["until"].(string))
		if r.body["space"] != sid || until.Sub(start) != time.Hour {
			t.Fatalf("%s", r.raw)
		}
		// Read, exactly as a viewer: the page, the list, the tree, history.
		got := f.admA.do("GET", base+"/pages/"+id, nil).want(t, 200, "")
		if got.body["body"] != "the text\n" {
			t.Fatal(got.raw)
		}
		f.admA.do("GET", base+"/pages", nil).want(t, 200, "")
		f.admA.do("GET", base+"/tree", nil).want(t, 200, "")
		f.admA.do("GET", base+"/pages/"+id+"/history", nil).want(t, 200, "")
		// ...and nothing else.
		f.admA.do("PUT", base+"/pages/"+id, map[string]any{"baseRev": rev, "meta": map[string]any{"title": "x"}, "body": "x"}).want(t, 403, "no_permission")
		f.admA.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "x"}}).want(t, 403, "no_permission")
		f.admA.do("DELETE", base+"/pages/"+id, nil).want(t, 403, "no_permission")
		f.admA.do("PUT", base+"/pages/"+id+"/draft", map[string]any{"baseRev": rev, "meta": map[string]any{"title": "x"}, "body": "x"}).want(t, 403, "no_permission")
		f.admA.do("POST", base+"/folders", map[string]any{"path": "F"}).want(t, 403, "no_permission")
		if got := f.memA.do("GET", base+"/pages/"+id, nil).want(t, 200, "").body["body"]; got != "the text\n" {
			t.Fatalf("the page changed: %v", got)
		}
		// The view is this session's alone: another session of the same person has none.
		other := a.newClient("")
		other.do("POST", "/api/login", map[string]any{"username": "adm-a", "password": pw}).want(t, 200, "")
		other.do("GET", base+"/pages/"+id, nil).want(t, 403, "no_permission")
		// A token of the same person never gets one.
		_, secret := mkToken(t, f.admA, "read", nil, f.spaceA)
		a.bearer(secret, "").do("GET", base+"/pages/"+id, nil).want(t, 403, "token_scope")
		// It is another tenant's space for another tenant's administrator.
		f.admB.do("POST", "/api/admin/spaces/"+sid+"/view", map[string]any{"reason": "x"}).want(t, 404, "not_found")
		f.admB.do("GET", base+"/pages/"+id, nil).want(t, 404, "not_found")

		// Audited: the view with its reason, and each page the first time in the hour.
		f.admA.do("GET", base+"/pages/"+id, nil).want(t, 200, "")
		a.auth.FlushAudit(context.Background())
		viewed := f.admA.audit(t, "space_viewed")
		if len(viewed) != 1 {
			t.Fatalf("%v", viewed)
		}
		d := viewed[0]["detail"].(map[string]any)
		if d["space"] != sid || d["reason"] != "Checking a reported page" || d["until"] == "" || viewed[0]["actor"] == nil {
			t.Fatalf("%v", viewed[0])
		}
		pv := f.admA.audit(t, "page_viewed")
		n := 0
		for _, row := range pv {
			if row["detail"].(map[string]any)["page"] == id {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("the page was opened %d times and audited %d times: %v", 4, n, pv)
		}
		// An hour later it is audited again; and the view has ended.
		a.clock.Advance(59 * time.Minute)
		f.admA.do("GET", base+"/pages/"+id, nil).want(t, 200, "")
		a.clock.Advance(2 * time.Minute)
		f.admA.do("GET", base+"/pages/"+id, nil).want(t, 403, "no_permission")
		// Calling it again writes a new row and extends the hour.
		f.admA.do("POST", "/api/admin/spaces/"+sid+"/view", map[string]any{"reason": "Again"}).want(t, 200, "")
		f.admA.do("GET", base+"/pages/"+id, nil).want(t, 200, "")
		a.auth.FlushAudit(context.Background())
		if rows := f.admA.audit(t, "space_viewed"); len(rows) != 2 {
			t.Fatalf("%v", rows)
		}
		n = 0
		for _, row := range f.admA.audit(t, "page_viewed") {
			if row["detail"].(map[string]any)["page"] == id {
				n++
			}
		}
		if n != 2 {
			t.Fatalf("audited %d times, want 2 (one in each hour)", n)
		}
		// It can be ended at once.
		f.admA.do("DELETE", "/api/admin/spaces/"+sid+"/view", nil).want(t, 204, "")
		f.admA.do("GET", base+"/pages/"+id, nil).want(t, 403, "no_permission")
		// Members read without any of it, and nothing of theirs is audited.
		before := len(f.admA.audit(t, "page_viewed"))
		f.memA.do("GET", base+"/pages/"+id, nil).want(t, 200, "")
		a.auth.FlushAudit(context.Background())
		if len(f.admA.audit(t, "page_viewed")) != before {
			t.Fatal("a member's read was audited as a view")
		}
	})
}

func TestPersonalSpacesAreNeverViewedOrExportedByATenantAdministrator(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		own := f.memA.personalSpace()
		for _, c := range []*client{f.admA} {
			c.do("POST", "/api/admin/spaces/"+own+"/view", map[string]any{"reason": "curious"}).want(t, 404, "not_found")
			c.do("GET", "/api/spaces/"+own+"/pages", nil).want(t, 404, "not_found")
			c.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{own}}).want(t, 404, "not_found")
			c.do("POST", "/api/admin/confirm", map[string]any{"action": "export", "spaces": []string{own}}).want(t, 404, "not_found")
		}
		// The same answer as for a space that is not there.
		f.admA.do("POST", "/api/admin/spaces/"+fakeID+"/view", map[string]any{"reason": "curious"}).want(t, 404, "not_found")
		// A site administrator may.
		f.root.do("POST", "/api/admin/spaces/"+own+"/view", map[string]any{"reason": "a court order"}).want(t, 200, "")
		f.root.do("GET", "/api/spaces/"+own+"/pages", nil).want(t, 200, "")
		a.auth.FlushAudit(context.Background())
		if rows := f.root.audit(t, "space_viewed"); len(rows) != 1 {
			t.Fatalf("%v", rows)
		}
	})
}

// --- exports -----------------------------------------------------------------

func waitExport(t *testing.T, c *client, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if row := exportRow(t, c, id); row != nil {
			switch row["state"] {
			case "ready", "failed":
				return row
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the export did not finish")
	return nil
}

// exportRow is an export as the list of exports has it (nil: not listed for
// the caller); GET /api/admin/exports/{id} is gone: the list and
// GET /api/admin/jobs/{id} answer.
func exportRow(t *testing.T, c *client, id string) map[string]any {
	t.Helper()
	for _, it := range c.do("GET", "/api/admin/exports", nil).want(t, 200, "").body["items"].([]any) {
		if row := it.(map[string]any); row["id"] == id {
			return row
		}
	}
	return nil
}

// exportJob is the id of the job POST /api/admin/exports answered with.
func exportJob(r result) string { return r.body["job"].(map[string]any)["id"].(string) }

func unzipped(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestAnExportHoldsTheFilesTheDraftsAndTheRemoteAndIsAudited(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		sid := f.memA.do("POST", "/api/spaces", map[string]any{"name": "Trip"}).want(t, 201, "").body["id"].(string)
		base := "/api/spaces/" + sid
		pg := f.memA.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Day one"}, "body": "we walked\n"}).want(t, 201, "")
		f.memA.draft(base+"/pages/"+pg.body["id"].(string), pg.body["rev"], "Day one", "we walked, then ran\n").want(t, 200, "")

		r := f.admA.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{sid}}).want(t, 202, "")
		id := exportJob(r)
		if j := r.body["job"].(map[string]any); j["kind"] != "export" || j["subject"] != "" || j["state"] != "queued" {
			t.Fatal(r.raw)
		}
		done := waitExport(t, f.admA, id)
		if done["state"] != "ready" || done["bytes"].(float64) <= 0 {
			t.Fatalf("%v", done)
		}
		exp, _ := time.Parse(time.RFC3339, done["expires"].(string))
		if made, _ := time.Parse(time.RFC3339, done["created"].(string)); exp.Sub(made) != 7*24*time.Hour {
			t.Fatalf("kept for %v", exp.Sub(made))
		}
		dl := f.admA.do("GET", "/api/admin/exports/"+id+"/download", nil)
		if dl.code != 200 || !strings.HasPrefix(dl.resp.Header.Get("Content-Disposition"), "attachment;") || dl.resp.Header.Get("Content-Type") != "application/zip" {
			t.Fatalf("%d %v", dl.code, dl.resp.Header)
		}
		files := unzipped(t, []byte(dl.raw))
		folder := "Trip-" + sid + "/"
		if files[folder+"Day one.md"] == "" || !strings.Contains(files[folder+"Day one.md"], "we walked") || strings.Contains(files[folder+"Day one.md"], "then ran") {
			t.Fatalf("the page: %v", files)
		}
		var drafts []map[string]any
		if err := json.Unmarshal([]byte(files[folder+"drafts.json"]), &drafts); err != nil || len(drafts) != 1 || !strings.Contains(drafts[0]["body"].(string), "then ran") || drafts[0]["username"] != "mem-a" {
			t.Fatalf("%v %s", err, files[folder+"drafts.json"])
		}
		if !strings.Contains(files[folder+"REMOTE.txt"], "no remote") {
			t.Fatalf("%q", files[folder+"REMOTE.txt"])
		}
		for name := range files {
			if strings.Contains(name, ".git/") || strings.HasSuffix(name, "history.bundle") {
				t.Fatalf("history in an export without history: %s", name)
			}
		}
		// Ranges are served.
		req := httptest.NewRequest("GET", "/api/admin/exports/"+id+"/download", nil)
		_ = req
		part := f.admA.do("GET", "/api/admin/exports/"+id+"/download", nil, "Range", "bytes=0-9")
		if part.code != 206 || len(part.raw) != 10 {
			t.Fatalf("range: %d %d", part.code, len(part.raw))
		}
		// Audited: made, downloaded; the other tenant sees nothing of it.
		a.auth.FlushAudit(context.Background())
		if rows := f.admA.audit(t, "export_created"); len(rows) != 1 || rows[0]["detail"].(map[string]any)["export"] != id {
			t.Fatalf("%v", rows)
		}
		if rows := f.admA.audit(t, "export_downloaded"); len(rows) != 2 {
			t.Fatalf("%v", rows)
		}
		for _, row := range f.admB.audit(t, "") {
			if strings.Contains(row["kind"].(string), "export_") && row["detail"].(map[string]any)["export"] == id {
				t.Fatalf("B sees A's export: %v", row)
			}
		}
		// The history is for site administrators.
		f.admA.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{sid}, "history": true}).want(t, 403, "forbidden")
		h := f.root.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{sid}, "history": true}).want(t, 202, "")
		if done := waitExport(t, f.root, exportJob(h)); done["state"] != "ready" {
			t.Fatalf("%v", done)
		}
		hf := unzipped(t, []byte(f.root.do("GET", "/api/admin/exports/"+exportJob(h)+"/download", nil).raw))
		if !strings.Contains(hf[folder+"history.bundle"], "# v2 git bundle") {
			t.Fatalf("no bundle in %v", keys(hf))
		}
		// A tenant administrator sees their tenant's exports; the site sees all.
		if l := f.admA.do("GET", "/api/admin/exports", nil).want(t, 200, "").body["items"].([]any); len(l) < 1 {
			t.Fatal("the export is not listed")
		}
		// Deleting removes the file.
		f.admA.do("DELETE", "/api/admin/exports/"+id, nil).want(t, 204, "")
		f.admA.do("GET", "/api/admin/exports/"+id+"/download", nil).want(t, 404, "not_found")
		// An export of a space in the recycle bin comes from its directory.
		item := f.admA.do("DELETE", "/api/admin/spaces/"+sid, nil).want(t, 200, "").body["id"].(string)
		x := f.admA.do("POST", "/api/admin/exports", map[string]any{"items": []string{item}}).want(t, 202, "")
		if done := waitExport(t, f.admA, exportJob(x)); done["state"] != "ready" {
			t.Fatalf("%v", done)
		}
		xf := unzipped(t, []byte(f.admA.do("GET", "/api/admin/exports/"+exportJob(x)+"/download", nil).raw))
		if !strings.Contains(xf[folder+"Day one.md"], "we walked") || !strings.Contains(xf[folder+"drafts.json"], "then ran") {
			t.Fatalf("%v", keys(xf))
		}
	})
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestExportsAreLimitedAndExpire(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		sid := tang.personalSpace()
		admin.do("POST", "/api/admin/exports", map[string]any{}).want(t, 400, "invalid")
		admin.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{fakeID}}).want(t, 404, "not_found")
		admin.do("POST", "/api/admin/exports", map[string]any{"items": []string{fakeID}}).want(t, 404, "not_found")
		tang.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{sid}}).want(t, 403, "forbidden")
		// Beyond exports.maxBytes: 507, and nothing is made.
		if _, err := a.st.Q().Exec(context.Background(), `INSERT INTO jobs(id, tenant_id, kind, made_by, state, bytes_done, result, created, updated, expires) VALUES ('EX00000001', ?, 'export', 'x', 'done', ?, '{"path":"exports/EX00000001.zip"}', 1, 1, 99999999999999)`, store.DefaultTenantID, int64(21)<<30); err != nil {
			t.Fatal(err)
		}
		admin.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{sid}}).want(t, 507, "exports_full")
		if n := a.mgrExports(t); n != 1 {
			t.Fatalf("%d exports", n)
		}
		admin.do("PUT", "/api/admin/settings/exports.maxBytes", map[string]any{"value": 100 << 30}).want(t, 200, "")
		r := admin.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{sid}}).want(t, 202, "")
		waitExport(t, admin, exportJob(r))
		// Seven days later (exports.days) it is gone, file and row.
		a.clock.Advance(7*24*time.Hour + time.Minute)
		if err := a.mgr.ExpireExports(context.Background()); err != nil {
			t.Fatal(err)
		}
		admin.do("GET", "/api/admin/jobs/"+exportJob(r), nil).want(t, 404, "not_found")
		if exportRow(t, admin, exportJob(r)) != nil {
			t.Fatal("an expired export is still listed")
		}
	})
}

func (a *acc) mgrExports(t *testing.T) int {
	t.Helper()
	var n int
	if err := a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM jobs WHERE kind = 'export'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// An export a site administrator made is the site's: a tenant administrator
// never lists, reads or downloads it, whatever spaces it holds (a personal space,
// a history bundle, or spaces of several tenants). One a tenant administrator
// made is their tenant's administrators' and the site's.
func TestAnExportOfTheSiteIsNeverSeenByATenantAdministrator(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		own := f.memA.personalSpace()
		made := map[string]string{
			"personal, with history":   f.root.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{own}, "history": true}).want(t, 202, "").body["job"].(map[string]any)["id"].(string),
			"spaces of two tenants":    f.root.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{f.spaceA, f.spaceB}}).want(t, 202, "").body["job"].(map[string]any)["id"].(string),
			"a space of the tenant":    f.root.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{f.spaceA}}).want(t, 202, "").body["job"].(map[string]any)["id"].(string),
			"a space of the other one": f.root.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{f.spaceB}}).want(t, 202, "").body["job"].(map[string]any)["id"].(string),
		}
		for what, id := range made {
			if done := waitExport(t, f.root, id); done["state"] != "ready" {
				t.Fatalf("%s: %v", what, done)
			}
			for _, c := range []*client{f.admA, f.admB} {
				c.do("GET", "/api/admin/jobs/"+id, nil).want(t, 404, "not_found")
				c.do("GET", "/api/admin/exports/"+id+"/download", nil).want(t, 404, "not_found")
				c.do("DELETE", "/api/admin/exports/"+id, nil).want(t, 404, "not_found")
				if r := c.do("GET", "/api/admin/exports", nil).want(t, 200, ""); strings.Contains(r.raw, id) {
					t.Fatalf("%s is listed for a tenant administrator: %s", what, r.raw)
				}
			}
			f.root.do("GET", "/api/admin/exports/"+id+"/download", nil).want(t, 200, "")
		}
		// A tenant administrator's own export is theirs and the site's.
		mine := exportJob(f.admA.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{f.spaceA}}).want(t, 202, ""))
		waitExport(t, f.admA, mine)
		f.admA.do("GET", "/api/admin/exports/"+mine+"/download", nil).want(t, 200, "")
		if exportRow(t, f.root, mine) == nil || exportRow(t, f.admA, mine) == nil || exportRow(t, f.admB, mine) != nil {
			t.Fatal("the site and the tenant's administrators list it, the other tenant's do not")
		}
		f.root.do("GET", "/api/admin/jobs/"+mine, nil).want(t, 200, "")
		f.admB.do("GET", "/api/admin/jobs/"+mine, nil).want(t, 404, "not_found")
	})
}

// An early purge is long paced work: the request starts it and answers 202 with
// the item at once; the item is purging (a restore of it is busy) until it is
// gone, and the purge is audited when it is over.
func TestAnEarlyPurgeRunsInTheBackground(t *testing.T) {
	t.Parallel()
	// Each database's server has a hook of its own (the subtests may run at
	// once), found by its data directory.
	type pause struct{ held, release chan struct{} }
	var pauses sync.Map
	opts := accOpts{spaces: func(o *spaces.Options) {
		p := pause{make(chan struct{}), make(chan struct{})}
		pauses.Store(o.DataDir, p)
		var once sync.Once
		o.RecycleHook = func(s string) error {
			if s == "purge:marked" {
				once.Do(func() { close(p.held); <-p.release })
			}
			return nil
		}
	}}
	eachAccounts(t, opts, func(t *testing.T, a *acc) {
		v, ok := pauses.Load(a.mgr.DataDir())
		if !ok {
			t.Fatal("no hook for this server")
		}
		held, release := v.(pause).held, v.(pause).release
		admin := a.setup("kazuha", pw)
		sid := admin.do("POST", "/api/spaces", map[string]any{"name": "Big"}).want(t, 201, "").body["id"].(string)
		item := admin.do("DELETE", "/api/admin/spaces/"+sid, nil).want(t, 200, "").body["id"].(string)
		tok := confirmFor(t, admin, "purge", "items", item)
		answered := make(chan result, 1)
		go func() { answered <- admin.do("DELETE", "/api/admin/recycle/"+item+"?confirm="+tok, nil) }()
		<-held
		var r result
		select {
		case r = <-answered:
		case <-time.After(10 * time.Second):
			close(release)
			t.Fatal("the request waited for the purge")
		}
		r.want(t, 202, "")
		if r.body["id"] != item {
			t.Fatalf("%s", r.raw)
		}
		admin.do("POST", "/api/admin/recycle/"+item+"/restore", map[string]any{}).want(t, 409, "busy")
		close(release)
		waitPurges(t, a)
		if n := a.countRows(t, `SELECT COUNT(*) FROM recycle_items WHERE id = '`+item+`'`); n != 0 {
			t.Fatal("the purge did not finish")
		}
		a.auth.FlushAudit(context.Background())
		if rows := admin.audit(t, "space_purged"); len(rows) != 1 || rows[0]["detail"].(map[string]any)["item"] != item {
			t.Fatalf("%v", rows)
		}
	})
}
