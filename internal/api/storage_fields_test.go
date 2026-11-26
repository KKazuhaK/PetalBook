package api_test

import (
	"context"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
)

// The fields the administration screens need (docs/api.md, "Storage management"):
// what the server used to leave the page to guess.

func TestRecycleDaysIsInTheUserPlanAndTheStorageSummary(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		a.addUser("tang", pw, "member")
		uid := mustUserID(t, a, "tang")
		days := func() (float64, float64) {
			plan := admin.do("GET", "/api/admin/users/"+uid+"/deletion", nil).want(t, 200, "").body["recycleDays"]
			sum := admin.do("GET", "/api/admin/storage", nil).want(t, 200, "").body["summary"].(map[string]any)["recycleDays"]
			p, _ := plan.(float64)
			s, _ := sum.(float64)
			return p, s
		}
		if p, s := days(); p != 30 || s != 30 {
			t.Fatalf("plan %v, summary %v", p, s)
		}
		admin.do("PUT", "/api/admin/settings/recycle.days", map[string]any{"value": 7}).want(t, 200, "")
		if p, s := days(); p != 7 || s != 7 {
			t.Fatalf("after the setting: plan %v, summary %v", p, s)
		}
		// The confirmation's plan carries it too (it is the preview of each person).
		c := admin.do("POST", "/api/admin/confirm", map[string]any{"action": "delete_users", "users": []string{uid}}).want(t, 200, "")
		if line := c.body["plan"].([]any)[0].(map[string]any); line["recycleDays"] != float64(7) {
			t.Fatalf("%v", line)
		}
	})
}

// setItemState makes an item look as it does in a state: a job of the kind of
// that state (one that failed, so nothing waits for it) holds it, or none does ("ready").
func setItemState(t *testing.T, a *acc, id, state string) {
	t.Helper()
	q := a.st.Q()
	if state == "ready" {
		if _, err := q.Exec(context.Background(), `UPDATE recycle_items SET job_id = '' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		return
	}
	kind := map[string]string{"moving": store.JobRecycleMove, "restoring": store.JobRecycleRestore, "purging": store.JobRecyclePurge}[state]
	job := "J" + state + id
	if _, err := q.Exec(context.Background(), `INSERT INTO jobs(id, tenant_id, kind, subject, state, created, updated) VALUES (?, ?, ?, ?, 'failed', 1, 1)`, job, store.DefaultTenantID, kind, id); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Exec(context.Background(), `UPDATE recycle_items SET job_id = ? WHERE id = ?`, job, id); err != nil {
		t.Fatal(err)
	}
}

func TestARecycleItemSaysItsState(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		sid := tang.do("POST", "/api/spaces", map[string]any{"name": "Alpha"}).want(t, 201, "").body["id"].(string)
		item := admin.do("DELETE", "/api/admin/spaces/"+sid, nil).want(t, 200, "")
		if item.body["state"] != "ready" {
			t.Fatalf("%s", item.raw)
		}
		id := item.body["id"].(string)
		state := func() any {
			return admin.do("GET", "/api/admin/recycle", nil).want(t, 200, "").body["items"].([]any)[0].(map[string]any)["state"]
		}
		if state() != "ready" {
			t.Fatal(state())
		}
		for _, want := range []string{"moving", "restoring", "purging"} {
			setItemState(t, a, id, want)
			if got := state(); got != want {
				t.Fatalf("state %v, want %s", got, want)
			}
		}
		// A purge answers with the item as it is now: purging.
		setItemState(t, a, id, "ready")
		tok := confirmFor(t, admin, "purge", "items", id)
		r := admin.do("DELETE", "/api/admin/recycle/"+id+"?confirm="+tok, nil).want(t, 202, "")
		if r.body["state"] != "purging" {
			t.Fatalf("%s", r.raw)
		}
		waitPurges(t, a)
	})
}

func TestAnAdministratorSeesWhetherTheyHaveAViewOpen(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		sid := f.spaceA
		get := func(c *client) any {
			one := c.do("GET", "/api/admin/spaces/"+sid, nil).want(t, 200, "").body["view"]
			for _, it := range c.do("GET", "/api/admin/spaces", nil).want(t, 200, "").body["items"].([]any) {
				if m := it.(map[string]any); m["id"] == sid {
					if m["view"] == nil && one != nil || m["view"] != nil && one == nil {
						t.Fatalf("the list says %v, the space %v", m["view"], one)
					}
				}
			}
			return one
		}
		if v, ok := f.admA.do("GET", "/api/admin/spaces/"+sid, nil).want(t, 200, "").body["view"]; !ok || v != nil {
			t.Fatalf("a view is null when there is none: %v %v", v, ok)
		}
		start := a.clock.Now()
		f.admA.do("POST", "/api/admin/spaces/"+sid+"/view", map[string]any{"reason": "check"}).want(t, 200, "")
		v, _ := get(f.admA).(map[string]any)
		since, _ := time.Parse(time.RFC3339, v["since"].(string))
		until, _ := time.Parse(time.RFC3339, v["until"].(string))
		if !since.Equal(start.Truncate(time.Second)) || until.Sub(since) != time.Hour {
			t.Fatalf("%v (opened %v)", v, start)
		}
		// It is the session's: another session of the same person, and another administrator, have none.
		other := a.newClient("")
		other.do("POST", "/api/login", map[string]any{"username": "adm-a", "password": pw}).want(t, 200, "")
		if get(other) != nil || get(f.root) != nil {
			t.Fatal("a view showed in another session")
		}
		// Calling again moves both ends.
		a.clock.Advance(20 * time.Minute)
		f.admA.do("POST", "/api/admin/spaces/"+sid+"/view", map[string]any{"reason": "again"}).want(t, 200, "")
		v, _ = get(f.admA).(map[string]any)
		if s, _ := time.Parse(time.RFC3339, v["since"].(string)); s.Sub(since) != 20*time.Minute {
			t.Fatalf("%v", v)
		}
		f.admA.do("DELETE", "/api/admin/spaces/"+sid+"/view", nil).want(t, 204, "")
		if get(f.admA) != nil {
			t.Fatal("an ended view still shows")
		}
		// An hour later it is over without being ended.
		f.admA.do("POST", "/api/admin/spaces/"+sid+"/view", map[string]any{"reason": "again"}).want(t, 200, "")
		a.clock.Advance(time.Hour + time.Second)
		if get(f.admA) != nil {
			t.Fatal("an expired view still shows")
		}
	})
}

func TestARestoreNamesWhoWasLeftOutAndWhy(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		g := admin.mkGroup(t, "Readers", "local")
		tm.owner.do("POST", "/api/spaces/"+tm.sid+"/groups", map[string]any{"groupId": g, "role": "viewer"}).want(t, 201, "")
		item := admin.do("DELETE", "/api/admin/spaces/"+tm.sid, nil).want(t, 200, "").body["id"].(string)
		admin.do("DELETE", "/api/admin/groups/"+g, nil).want(t, 204, "")
		admin.do("DELETE", "/api/users/"+tm.viewerID, map[string]any{}).want(t, 200, "")

		r := admin.do("POST", "/api/admin/recycle/"+item+"/restore", map[string]any{}).want(t, 200, "")
		got := map[string]map[string]any{}
		for _, s := range r.body["skipped"].([]any) {
			m := s.(map[string]any)
			key, _ := m["user"].(string)
			if key == "" {
				key, _ = m["group"].(string)
			}
			got[key] = m
		}
		if len(got) != 2 {
			t.Fatalf("%s", r.raw)
		}
		if m := got[tm.viewerID]; m["name"] != "victor" || m["reason"] != "deleted" {
			t.Fatalf("%v", m)
		}
		if m := got[g]; m["name"] != "Readers" || m["reason"] != "deleted" {
			t.Fatalf("%v", m)
		}
	})
}

func TestARestoreOfAPersonNamesTheSpacesAndGroupsLeftOut(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		g := f.admin.mkGroup(t, "Writers", "local")
		f.admin.groupAdd(t, g, f.ownerID).want(t, 200, "")
		r := f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{"heir": f.viewerID}).want(t, 200, "")
		item := r.body["id"].(string)
		f.admin.do("DELETE", "/api/admin/groups/"+g, nil).want(t, 204, "")
		// One of the spaces that went with them is busy.
		var busy string
		for _, w := range r.body["with"].([]any) {
			busy = w.(map[string]any)["id"].(string)
		}
		if busy == "" {
			t.Fatalf("%s", r.raw)
		}
		setItemState(t, a, busy, "purging")
		var space, name string
		if err := a.st.Q().QueryRow(context.Background(), `SELECT space_id, name FROM recycle_items WHERE id = ?`, busy).Scan(&space, &name); err != nil {
			t.Fatal(err)
		}
		res := f.admin.do("POST", "/api/admin/recycle/"+item+"/restore", map[string]any{}).want(t, 200, "")
		var sawGroup, sawSpace bool
		for _, s := range res.body["skipped"].([]any) {
			m := s.(map[string]any)
			if m["group"] == g && m["name"] == "Writers" && m["reason"] == "deleted" {
				sawGroup = true
			}
			if m["space"] == space && m["name"] == name && m["reason"] == "busy" {
				sawSpace = true
			}
		}
		if !sawGroup || !sawSpace {
			t.Fatalf("%s", res.raw)
		}
	})
}

func TestAnExportSaysWhichSpacesItHoldsAndWhetherTheyCanBeOpened(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		live := tang.do("POST", "/api/spaces", map[string]any{"name": "Live"}).want(t, 201, "").body["id"].(string)
		gone := tang.do("POST", "/api/spaces", map[string]any{"name": "Gone"}).want(t, 201, "").body["id"].(string)
		binned := admin.do("DELETE", "/api/admin/spaces/"+gone, nil).want(t, 200, "").body["id"].(string)
		mk := func(body map[string]any) string {
			id := exportJob(admin.do("POST", "/api/admin/exports", body).want(t, 202, ""))
			waitExport(t, admin, id)
			return id
		}
		one := mk(map[string]any{"spaces": []string{live}})
		two := mk(map[string]any{"items": []string{binned}})
		sources := func(id string) map[string]any {
			for _, it := range admin.do("GET", "/api/admin/exports", nil).want(t, 200, "").body["items"].([]any) {
				if m := it.(map[string]any); m["id"] == id {
					return m["spaces"].([]any)[0].(map[string]any)
				}
			}
			t.Fatalf("export %s is not listed", id)
			return nil
		}
		if s := sources(one); s["space"] != live || s["spaceId"] != live || s["name"] != "Live" || s["open"] != true {
			t.Fatalf("%v", s)
		}
		// A space in the bin has its id and cannot be opened; restored, it can.
		if s := sources(two); s["item"] != binned || s["spaceId"] != gone || s["name"] != "Gone" || s["open"] != false {
			t.Fatalf("%v", s)
		}
		admin.do("POST", "/api/admin/recycle/"+binned+"/restore", map[string]any{}).want(t, 200, "")
		if s := sources(two); s["open"] != true {
			t.Fatalf("%v", s)
		}
		// A space that was deleted for good is not one to link to.
		if _, err := a.st.Q().Exec(context.Background(), `DELETE FROM spaces WHERE id = ?`, live); err != nil {
			t.Fatal(err)
		}
		if s := sources(one); s["open"] != false || s["spaceId"] != live {
			t.Fatalf("%v", s)
		}
	})
}

func TestAnExportOfAPersonalSpaceIsOpenOnlyToWhoMayOpenIt(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		personal := f.memA.personalSpace()
		id := exportJob(f.root.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{personal}}).want(t, 202, ""))
		open := func(c *client) any {
			return exportRow(t, c, id)["spaces"].([]any)[0].(map[string]any)["open"]
		}
		if open(f.root) != true {
			t.Fatal("a site administrator may open it")
		}
		// The ones a tenant administrator sees never hold a personal space, so this is the store's rule on its own.
		ids, err := store.OpenableSpaces(context.Background(), a.st.Q(), store.AdminScopeOf(mustUser(t, a, "adm-a")), []string{personal, f.spaceA, f.spaceB})
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || !ids[f.spaceA] {
			t.Fatalf("%v", ids)
		}
	})
}
