package api_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
)

// GET /api/admin/jobs lists the jobs of every kind in one shape, in the caller's
// scope: the site sees every tenant's (narrowed by ?tenant=), a tenant
// administrator their own and never another's or the site's; ?kind= and ?state=
// narrow it and the cursor continues it.
func TestTheJobsListIsScopedFilteredAndPaged(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		// A site export is the site's; a recycle action of tenant A is A's.
		siteExport := exportJob(f.root.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{f.spaceA}}).want(t, 202, ""))
		aExport := exportJob(f.admA.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{f.spaceA}}).want(t, 202, ""))
		kept := f.admA.do("POST", "/api/spaces", map[string]any{"name": "Kept"}).want(t, 201, "").body["id"].(string)
		moved := f.admA.do("DELETE", "/api/admin/spaces/"+kept, nil).want(t, 200, "").body["id"].(string)
		waitPurges(t, a)

		ids := func(c *client, query string) (out []string, next string, items []any) {
			t.Helper()
			r := c.do("GET", "/api/admin/jobs"+query, nil).want(t, 200, "")
			items = r.body["items"].([]any)
			for _, it := range items {
				out = append(out, it.(map[string]any)["id"].(string))
			}
			next, _ = r.body["next"].(string)
			return
		}
		has := func(list []string, id string) bool {
			for _, x := range list {
				if x == id {
					return true
				}
			}
			return false
		}
		all, _, items := ids(f.root, "")
		for _, want := range []string{siteExport, aExport, f.exportB, f.jobB} {
			if !has(all, want) {
				t.Fatalf("the site does not see %s: %v", want, all)
			}
		}
		// One shape for every kind.
		for _, it := range items {
			j := it.(map[string]any)
			for _, k := range []string{"id", "kind", "subject", "state", "total", "done", "bytesTotal", "bytesDone", "failed", "error", "created", "updated", "madeBy"} {
				if _, ok := j[k]; !ok {
					t.Fatalf("a job without %q: %v", k, j)
				}
			}
		}
		mine, _, _ := ids(f.admA, "")
		if !has(mine, aExport) || has(mine, siteExport) || has(mine, f.exportB) || has(mine, f.jobB) {
			t.Fatalf("a tenant administrator's list: %v", mine)
		}
		if theirs, _, _ := ids(f.admB, ""); !has(theirs, f.exportB) || !has(theirs, f.jobB) || has(theirs, aExport) {
			t.Fatalf("the other tenant's list: %v", theirs)
		}
		// Filters.
		if exports, _, _ := ids(f.admA, "?kind=export"); len(exports) != 1 || exports[0] != aExport {
			t.Fatalf("%v", exports)
		}
		_, _, moves := ids(f.admA, "?kind=recycle.move")
		if len(moves) != 1 || moves[0].(map[string]any)["subject"] != moved || moves[0].(map[string]any)["state"] != "done" {
			t.Fatalf("the move of the deleted space: %v", moves)
		}
		if done, _, _ := ids(f.root, "?kind=export&state=done"); !has(done, siteExport) {
			t.Fatalf("%v", done)
		}
		if narrowed, _, _ := ids(f.root, "?tenant="+f.tenB); !has(narrowed, f.exportB) || has(narrowed, siteExport) {
			t.Fatalf("narrowed to a tenant: %v", narrowed)
		}
		f.root.do("GET", "/api/admin/jobs?kind=nope", nil).want(t, 400, "invalid")
		f.root.do("GET", "/api/admin/jobs?state=nope", nil).want(t, 400, "invalid")
		f.root.do("GET", "/api/admin/jobs?limit=0", nil).want(t, 400, "invalid")
		f.root.do("GET", "/api/admin/jobs?cursor=garbage", nil).want(t, 400, "invalid")
		// Pages of one cover the same list.
		var paged []string
		cursor := ""
		for range 50 {
			page, next, _ := ids(f.root, "?limit=1&cursor="+cursor)
			paged = append(paged, page...)
			if next == "" {
				break
			}
			cursor = next
		}
		if len(paged) != len(all) {
			t.Fatalf("paged %v, whole %v", paged, all)
		}
		// A member follows nothing; one job is read in the same scope.
		f.memA.do("GET", "/api/admin/jobs", nil).want(t, 403, "forbidden")
		f.admA.do("GET", "/api/admin/jobs/"+aExport, nil).want(t, 200, "")
		f.admA.do("GET", "/api/admin/jobs/"+siteExport, nil).want(t, 404, "not_found")
	})
}

// A person who holds only the key of one kind of job follows that kind on the
// jobs routes (their exports with tenant.data.export, the recycle bin's with
// tenant.recycle.read) and never sees a kind their keys do not reach.
func TestEachKindsKeyAloneFollowsItsJobs(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		exp := a.addUser("exp", pw, "member")
		rec := a.addUser("rec", pw, "member")
		ctx, now := context.Background(), time.Now()
		for i, r := range []struct{ user, key string }{{"exp", "tenant.data.export"}, {"rec", "tenant.recycle.read"}} {
			id := "RJ" + strconv.Itoa(i)
			if err := a.st.InsertRole(ctx, a.st.Q(), store.Role{ID: id, TenantID: store.DefaultTenantID, Scope: store.RoleScopeTenant, Name: r.user,
				Keys: []string{r.key}, Created: now, Updated: now}); err != nil {
				t.Fatal(err)
			}
			if err := store.InsertRoleGrant(ctx, a.st.Q(), store.RoleGrant{RoleID: id, SubjectKind: store.GrantToUser, SubjectID: mustUserID(t, a, r.user), Created: now}); err != nil {
				t.Fatal(err)
			}
		}
		a.auth.RolesChanged()

		sp := root.do("POST", "/api/spaces", map[string]any{"name": "Kept"}).want(t, 201, "").body["id"].(string)
		gone := root.do("POST", "/api/spaces", map[string]any{"name": "Gone"}).want(t, 201, "").body["id"].(string)
		tok := root.do("POST", "/api/admin/confirm", map[string]any{"action": "export", "spaces": []string{sp}}).want(t, 200, "").body["confirm"].(string)
		bulk := root.do("POST", "/api/admin/bulk", map[string]any{"confirm": tok}).want(t, 202, "").body["job"].(map[string]any)["id"].(string)
		export := exportJob(exp.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{sp}}).want(t, 202, ""))
		root.do("DELETE", "/api/admin/spaces/"+gone, nil).want(t, 200, "")
		waitPurges(t, a)
		list := func(c *client, query string) (kinds map[string]string) {
			t.Helper()
			kinds = map[string]string{}
			for _, it := range c.do("GET", "/api/admin/jobs"+query, nil).want(t, 200, "").body["items"].([]any) {
				j := it.(map[string]any)
				kinds[j["id"].(string)] = j["kind"].(string)
			}
			return
		}

		// The exporter follows their export, and nothing else.
		exp.do("GET", "/api/admin/jobs/"+export, nil).want(t, 200, "")
		exp.do("GET", "/api/admin/jobs/"+bulk, nil).want(t, 404, "not_found")
		if got := list(exp, ""); len(got) != 1 || got[export] != "export" {
			t.Fatalf("the exporter's list: %v", got)
		}
		if got := list(exp, "?kind=bulk"); len(got) != 0 {
			t.Fatalf("the exporter's bulk jobs: %v", got)
		}

		// The reader of the recycle bin follows its moves, and nothing else.
		var move string
		for id, k := range list(rec, "") {
			if k != "recycle.move" {
				t.Fatalf("the recycle reader sees a %s job (%s)", k, id)
			}
			move = id
		}
		if move == "" {
			t.Fatal("the recycle reader sees no move")
		}
		rec.do("GET", "/api/admin/jobs/"+move, nil).want(t, 200, "")
		rec.do("GET", "/api/admin/jobs/"+bulk, nil).want(t, 404, "not_found")
		rec.do("GET", "/api/admin/jobs/"+export, nil).want(t, 404, "not_found")
		exp.do("GET", "/api/admin/jobs/"+move, nil).want(t, 404, "not_found")

		// Someone with none of the keys is refused at the route, naming the first.
		mem := a.addUser("mem", pw, "member")
		if r := mem.do("GET", "/api/admin/jobs/"+export, nil).want(t, 403, "forbidden"); r.body["permission"] != "tenant.bulk.run" {
			t.Fatal(r.raw)
		}
	})
}

// waitPurges waits until the jobs runner has nothing queued or running.
func waitPurges(t *testing.T, a *acc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.mgr.WaitPurges(ctx); err != nil {
		t.Fatalf("the jobs did not end: %v", err)
	}
}
