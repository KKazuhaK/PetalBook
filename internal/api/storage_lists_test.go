package api_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
)

// Milestone 3.5, part 4: the summary, the sorted and filtered list of spaces and
// the list of people (docs/api.md, "Storage management").

func listIDs(r result) []string {
	var ids []string
	for _, it := range r.body["items"].([]any) {
		ids = append(ids, it.(map[string]any)["id"].(string))
	}
	return ids
}

func TestAdminSpaceListSortsFiltersAndRefusesWhatItDoesNotKnow(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		names := []string{"Bravo", "Alpha", "Delta", "Charlie"}
		ids := map[string]string{}
		for i, n := range names {
			ids[n] = tang.do("POST", "/api/spaces", map[string]any{"name": n}).want(t, 201, "").body["id"].(string)
			a.clock.Advance(time.Minute)
			if i != 3 { // Charlie is never measured
				if _, err := a.st.RecordMeasurement(ctx, ids[n], store.Measurement{Bytes: int64(1000 * (i + 1)), Pages: 1, HavePages: true, At: a.clock.Now()}); err != nil {
					t.Fatal(err)
				}
			}
		}
		// Delta follows a remote of the person's own: it is measured, shown, and not counted.
		if err := store.PutRemote(ctx, a.st.Q(), store.Remote{SpaceID: ids["Delta"], URL: "https://git.example/d.git", Branch: "main", CredType: "token", State: "offline", Updated: a.clock.Now()}); err != nil {
			t.Fatal(err)
		}
		want := func(path string, order ...string) {
			t.Helper()
			r := admin.do("GET", path, nil).want(t, 200, "")
			var got []string
			for _, it := range r.body["items"].([]any) {
				if m := it.(map[string]any); m["personal"] != true { // tang's own space is not what is being listed
					got = append(got, m["name"].(string))
				}
			}
			if strings.Join(got, ",") != strings.Join(order, ",") {
				t.Fatalf("%s: %v, want %v", path, got, order)
			}
		}
		own := "&owner=" + mustUserID(t, a, "tang")
		want("/api/admin/spaces?sort=name"+own, "Alpha", "Bravo", "Charlie", "Delta")
		want("/api/admin/spaces?sort=name&order=desc"+own, "Delta", "Charlie", "Bravo", "Alpha")
		want("/api/admin/spaces?sort=bytes"+own, "Delta", "Alpha", "Bravo", "Charlie") // never measured last
		want("/api/admin/spaces?sort=bytes&order=asc"+own, "Bravo", "Alpha", "Delta", "Charlie")
		want("/api/admin/spaces?sort=created"+own, "Bravo", "Alpha", "Delta", "Charlie")
		want("/api/admin/spaces?sort=created&order=desc"+own, "Charlie", "Delta", "Alpha", "Bravo")
		want("/api/admin/spaces?remote=offline"+own, "Delta")
		want("/api/admin/spaces?remote=none"+own+"&sort=name", "Alpha", "Bravo", "Charlie")
		want("/api/admin/spaces?cache=local&sort=name"+own, "Alpha", "Bravo", "Charlie")
		want("/api/admin/spaces?cache=full"+own, "Delta")

		// Where the bytes live, and whether they count.
		r := admin.do("GET", "/api/admin/spaces?q=delta", nil).want(t, 200, "")
		d := r.body["items"].([]any)[0].(map[string]any)
		if d["location"] != "own" || d["counted"] != false || d["sizeBytes"] != float64(3000) {
			t.Fatalf("%v", d)
		}
		r = admin.do("GET", "/api/admin/spaces?q=alpha", nil).want(t, 200, "")
		if al := r.body["items"].([]any)[0].(map[string]any); al["location"] != "server" || al["counted"] != true || al["sizeBytes"] != float64(2000) {
			t.Fatalf("%v", al)
		}
		u := admin.do("GET", "/api/admin/usage", nil).want(t, 200, "")
		_ = u // the quota's numbers are tested in the store: Delta adds nothing to them

		// A pin and an exemption are filters.
		admin.do("PUT", "/api/admin/spaces/"+ids["Bravo"]+"/storage", map[string]any{"pin": true, "exempt": true}).want(t, 200, "")
		want("/api/admin/spaces?pinned=1"+own, "Bravo")
		want("/api/admin/spaces?exempt=1"+own, "Bravo")
		want("/api/admin/spaces?pinned=1&remote=offline" + own)
		// idleDays: used (a space is used when it is made) at most n days ago leaves out.
		a.clock.Advance(40 * 24 * time.Hour)
		admin.do("POST", "/api/login", map[string]any{"identifier": "kazuha", "password": pw}).want(t, 200, "") // the session ended with the weeks
		want("/api/admin/spaces?idleDays=30&sort=name"+own, "Alpha", "Bravo", "Charlie", "Delta")
		want("/api/admin/spaces?idleDays=60&sort=name" + own)

		// Pages: any size gives the same spaces, once, whichever the sort; and a
		// cursor of another sort is refused.
		for _, sort := range []string{"created", "name", "bytes", "lastUsed"} {
			var got []string
			cursor := ""
			for pages := 0; ; pages++ {
				if pages > 20 {
					t.Fatal("no end")
				}
				r := admin.do("GET", "/api/admin/spaces?limit=2&sort="+sort+"&cursor="+url.QueryEscape(cursor), nil).want(t, 200, "")
				got = append(got, listIDs(r)...)
				if r.body["next"] == "" {
					break
				}
				cursor = r.body["next"].(string)
			}
			whole := listIDs(admin.do("GET", "/api/admin/spaces?limit=200&sort="+sort, nil).want(t, 200, ""))
			if strings.Join(got, ",") != strings.Join(whole, ",") || len(got) != 6 {
				t.Fatalf("sort %s: pages %v, whole %v", sort, got, whole)
			}
		}
		next := admin.do("GET", "/api/admin/spaces?limit=2&sort=name", nil).want(t, 200, "").body["next"].(string)
		for _, p := range []string{"sort=bytes&cursor=" + url.QueryEscape(next), "sort=name&order=desc&cursor=" + url.QueryEscape(next), "cursor=" + url.QueryEscape(next), "cursor=nonsense"} {
			admin.do("GET", "/api/admin/spaces?"+p, nil).want(t, 400, "invalid")
		}
		for _, p := range []string{"sort=size", "order=up", "cache=warm", "remote=gone", "deadline=soon", "pinned=2", "exempt=yes", "idleDays=-1", "idleDays=x", "limit=0", "limit=500", "overdue=2"} {
			admin.do("GET", "/api/admin/spaces?"+p, nil).want(t, 400, "invalid")
		}
	})
}

func TestAdminSpaceListDeadlineFilters(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		old := tang.do("POST", "/api/spaces", map[string]any{"name": "Old"}).want(t, 201, "").body["id"].(string)
		admin.do("PUT", "/api/admin/settings/storage.remotes.require", map[string]any{"value": "any"}).want(t, 200, "")
		admin.do("PUT", "/api/admin/settings/storage.remotes.scope", map[string]any{"value": "all"}).want(t, 200, "")
		admin.do("PUT", "/api/admin/settings/storage.remotes.graceDays", map[string]any{"value": 10}).want(t, 200, "")
		a.clock.Advance(3 * 24 * time.Hour)
		// The API refuses a new space without a remote now; one made some other way (an import, a restore) is what the deadline is about.
		fresh := "FRESH00001"
		if err := store.InsertSpace(context.Background(), a.st.Q(), store.DefaultTenantID, fresh, "Fresh", "", "", a.clock.Now()); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMember(context.Background(), a.st.Q(), fresh, mustUserID(t, a, "tang"), "owner", a.clock.Now()); err != nil {
			t.Fatal(err)
		}
		a.clock.Advance(8 * 24 * time.Hour) // Old is 11 days after the requirement began: past its 10 days; Fresh is 8 days into its 10
		has := func(path string, want ...string) {
			t.Helper()
			got := listIDs(admin.do("GET", path, nil).want(t, 200, ""))
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("%s: %v, want %v", path, got, want)
			}
		}
		personal := tang.personalSpace() // made before the requirement, which is for every space: it has the same grace as Old
		has("/api/admin/spaces?deadline=overdue&owner="+mustUserID(t, a, "tang"), personal, old)
		has("/api/admin/spaces?overdue=1&owner="+mustUserID(t, a, "tang"), personal, old)
		has("/api/admin/spaces?deadline=grace&owner="+mustUserID(t, a, "tang"), fresh)
		// The stored counts: set by the settle pass (every hour, and when the policy changes).
		if _, err := a.mgr.SettleDeadlines(context.Background()); err != nil {
			t.Fatal(err)
		}
		r := admin.do("GET", "/api/admin/storage/people/"+mustUserID(t, a, "tang"), nil).want(t, 200, "")
		if r.body["graceSpaces"] != float64(1) || r.body["overdueSpaces"] != float64(2) {
			t.Fatalf("%s", r.raw)
		}
		l := admin.do("GET", "/api/admin/storage/people?deadline=overdue", nil).want(t, 200, "")
		// Both people have a space made before the requirement; only tang has one in grace.
		if ids := listIDs2(l, "user"); len(ids) != 2 {
			t.Fatalf("%s", l.raw)
		}
		if ids := listIDs2(admin.do("GET", "/api/admin/storage/people?deadline=grace", nil).want(t, 200, ""), "user"); len(ids) != 1 || ids[0] != mustUserID(t, a, "tang") {
			t.Fatalf("%v", ids)
		}
		s := admin.do("GET", "/api/admin/storage", nil).want(t, 200, "").body["summary"].(map[string]any)["remotes"].(map[string]any)
		if s["inGrace"] != float64(1) || s["overdue"] != float64(3) {
			t.Fatalf("%v", s)
		}
	})
}

func listIDs2(r result, key string) []string {
	var ids []string
	for _, it := range r.body["items"].([]any) {
		ids = append(ids, it.(map[string]any)[key].(map[string]any)["id"].(string))
	}
	return ids
}

// The summary is the stored numbers of a tenant or of the server; a tenant
// administrator reads their tenant's, and no disk.
func TestStorageSummaryIsStoredNumbersPerTenant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newTenFx(t, a)
		// A is measured: the one space, plus personal spaces.
		for _, id := range []string{f.spaceA, f.spaceB} {
			if _, err := a.st.RecordMeasurement(ctx, id, store.Measurement{Bytes: 4096, Pages: 2, HavePages: true, At: a.clock.Now()}); err != nil {
				t.Fatal(err)
			}
		}
		sumOf := func(c *client, path string) map[string]any {
			t.Helper()
			r := c.do("GET", path, nil).want(t, 200, "")
			if r.body["remotes"] == nil || r.body["git"] == nil || r.body["affected"] == nil {
				t.Fatalf("what is not a setting is not there: %s", r.raw)
			}
			return r.body["summary"].(map[string]any)
		}
		sa, sb, site := sumOf(f.admA, "/api/admin/storage"), sumOf(f.admB, "/api/admin/storage"), sumOf(f.root, "/api/admin/storage")
		if sa["tenant"] != store.DefaultTenantID || sb["tenant"] != f.tenB || site["tenant"] != nil {
			t.Fatalf("%v %v %v", sa["tenant"], sb["tenant"], site["tenant"])
		}
		if sa["usageBytes"] != float64(4096) || sb["usageBytes"] != float64(4096) || site["usageBytes"] != float64(8192) {
			t.Fatalf("usage: %v %v %v", sa["usageBytes"], sb["usageBytes"], site["usageBytes"])
		}
		if sa["heldBytes"] != float64(4096) || site["heldBytes"] != float64(8192) {
			t.Fatalf("held: %v %v", sa["heldBytes"], site["heldBytes"])
		}
		// People: the one tenant's, and every tenant's.
		var pa, pb int
		for _, p := range []struct {
			n *int
			c *client
		}{{&pa, f.admA}, {&pb, f.admB}} {
			*p.n = len(listIDs2(p.c.do("GET", "/api/admin/storage/people?limit=200", nil).want(t, 200, ""), "user"))
		}
		if float64(pa) != sa["people"] || float64(pb) != sb["people"] || sa["people"].(float64)+sb["people"].(float64) != site["people"].(float64) {
			t.Fatalf("people: %d %d %v %v %v", pa, pb, sa["people"], sb["people"], site["people"])
		}
		// The disk is the site's; a tenant administrator is not told.
		if _, ok := sa["diskFreeBytes"]; ok {
			t.Fatalf("a tenant administrator is told the disk: %v", sa)
		}
		// Recycle bin and exports of B are B's.
		if sb["recycle"].(map[string]any)["items"] != float64(1) || sa["recycle"].(map[string]any)["items"] != float64(0) || site["recycle"].(map[string]any)["items"] != float64(1) {
			t.Fatalf("recycle: %v %v %v", sa["recycle"], sb["recycle"], site["recycle"])
		}
		// A site administrator may narrow it; a tenant administrator may not look elsewhere.
		n := f.root.do("GET", "/api/admin/storage?tenant="+f.tenB, nil).want(t, 200, "").body["summary"].(map[string]any)
		if n["tenant"] != f.tenB || n["usageBytes"] != float64(4096) {
			t.Fatalf("%v", n)
		}
		f.admA.do("GET", "/api/admin/storage?tenant="+f.tenB, nil).want(t, 404, "not_found")
		// Writing the settings is still the site's.
		f.admA.do("PUT", "/api/admin/settings/cache.minIdleHours", map[string]any{"value": 5}).want(t, 403, "forbidden")
		f.memA.do("GET", "/api/admin/storage", nil).want(t, 403, "forbidden")
		f.memA.do("GET", "/api/admin/storage/people", nil).want(t, 403, "forbidden")
		f.tokA.do("GET", "/api/admin/storage/people", nil).want(t, 403, "token_scope")
	})
}

// The people's list and a person's item: scoped to the tenant, sorted, filtered.
func TestStoragePeopleListAndItem(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		// B's member has done something; B's administrator has not.
		base := "/api/admin/storage/people"
		r := f.admB.do("GET", base, nil).want(t, 200, "")
		for _, it := range r.body["items"].([]any) {
			m := it.(map[string]any)
			if m["tenant"] != f.tenB {
				t.Fatalf("a tenant administrator is shown %v", m)
			}
			for _, k := range []string{"user", "tenant", "created", "lastActive", "firstWrite", "dormant", "connected", "spaces", "remoteSpaces", "evictedSpaces", "pinnedSpaces", "graceSpaces", "overdueSpaces", "usageBytes", "heldBytes", "measuredAt"} {
				if _, ok := m[k]; !ok {
					t.Fatalf("%s is missing: %v", k, m)
				}
			}
		}
		for _, id := range listIDs2(r, "user") {
			if id == f.userA {
				t.Fatalf("tenant A's person in B's list")
			}
		}
		// B's member has signed in and wrote a page; B's administrator made a space and wrote nothing.
		f.memB.do("POST", "/api/spaces/"+f.spaceB+"/pages", map[string]any{"meta": map[string]any{"title": "hello"}}).want(t, 201, "")
		item := f.admB.do("GET", base+"/"+f.userB, nil).want(t, 200, "")
		if item.body["dormant"] != false || item.body["firstWrite"] == nil || item.body["lastActive"] == nil {
			t.Fatalf("%s", item.raw)
		}
		adm := f.admB.do("GET", base+"/"+mustUserID(t, a, "adm-b"), nil).want(t, 200, "")
		if adm.body["dormant"] != true || adm.body["firstWrite"] != nil || adm.body["spaces"] != float64(2) {
			t.Fatalf("%s", adm.raw)
		}
		// The spaces counted for them, without their personal one for a tenant administrator.
		if l := adm.body["items"].([]any); len(l) != 1 || l[0].(map[string]any)["id"] != f.spaceB || l[0].(map[string]any)["owner"] == nil {
			t.Fatalf("a person's item lists the spaces counted for them: %s", adm.raw)
		}
		if l := f.root.do("GET", base+"/"+mustUserID(t, a, "adm-b"), nil).want(t, 200, "").body["items"].([]any); len(l) < 2 {
			t.Fatalf("a site administrator sees the personal space too: %v", l)
		}
		f.admB.do("GET", base+"/"+f.userA, nil).want(t, 404, "not_found")
		f.admB.do("GET", base+"/"+fakeID, nil).want(t, 404, "not_found")
		// Filters and sorts.
		dormant := listIDs2(f.root.do("GET", base+"?dormant=1&limit=200", nil).want(t, 200, ""), "user")
		for _, id := range dormant {
			if id == f.userB {
				t.Fatalf("a person who wrote is dormant: %v", dormant)
			}
		}
		for _, p := range []string{"sort=held", "sort=bytes", "sort=lastActive", "sort=created", "sort=spaces", "sort=held&order=asc", "remote=none", "remote=some", "deadline=grace", "disabled=0", "disabled=1", "inactiveDays=1", "q=mem", "dormant=1&sort=created"} {
			f.root.do("GET", base+"?"+p, nil).want(t, 200, "")
		}
		for _, p := range []string{"sort=name", "order=sideways", "remote=maybe", "deadline=never", "disabled=2", "inactiveDays=a", "dormant=3", "limit=0", "limit=201", "cursor=bytes.desc:1:x"} {
			f.root.do("GET", base+"?"+p, nil).want(t, 400, "invalid")
		}
		// A page of two: every person once.
		var got []string
		cursor := ""
		for n := 0; n < 20; n++ {
			r := f.root.do("GET", base+"?limit=2&sort=created&cursor="+url.QueryEscape(cursor), nil).want(t, 200, "")
			got = append(got, listIDs2(r, "user")...)
			if r.body["next"] == "" {
				break
			}
			cursor = r.body["next"].(string)
		}
		whole := listIDs2(f.root.do("GET", base+"?limit=200&sort=created", nil).want(t, 200, ""), "user")
		if strings.Join(got, ",") != strings.Join(whole, ",") {
			t.Fatalf("%v\n%v", got, whole)
		}
		// q finds by username or name.
		if q := listIDs2(f.root.do("GET", base+"?q=MEM-B", nil).want(t, 200, ""), "user"); len(q) != 1 || q[0] != f.userB {
			t.Fatalf("%v", q)
		}
	})
}

// A person's last use and first change are recorded where people use the server,
// and a person who has done neither is dormant until they do.
func TestLastActiveAndFirstWriteAreRecorded(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		id := mustUserID(t, a, "tang")
		item := func() map[string]any {
			return admin.do("GET", "/api/admin/storage/people/"+id, nil).want(t, 200, "").body
		}
		if it := item(); it["lastActive"] != nil || it["firstWrite"] != nil || it["dormant"] != true {
			t.Fatalf("before: %v", it)
		}
		// Using the server (a session) is activity; reading is not a change.
		sp := tang.personalSpace()
		tang.do("GET", "/api/spaces/"+sp+"/pages", nil).want(t, 200, "")
		if it := item(); it["lastActive"] == nil || it["firstWrite"] != nil || it["dormant"] != true {
			t.Fatalf("after reading: %v", it)
		}
		t0 := item()["lastActive"]
		// A second request within the hour writes nothing.
		a.clock.Advance(10 * time.Minute)
		tang.do("GET", "/api/spaces/"+sp+"/pages", nil).want(t, 200, "")
		if item()["lastActive"] != t0 {
			t.Fatalf("written again within the hour: %v then %v", t0, item()["lastActive"])
		}
		// Changing something is the first write, and ends the dormancy.
		tang.do("POST", "/api/spaces/"+sp+"/pages", map[string]any{"meta": map[string]any{"title": "first"}}).want(t, 201, "")
		it := item()
		if it["firstWrite"] == nil || it["dormant"] != false {
			t.Fatalf("after writing: %v", it)
		}
		first := it["firstWrite"]
		a.clock.Advance(2 * time.Hour)
		tang.do("POST", "/api/spaces/"+sp+"/pages", map[string]any{"meta": map[string]any{"title": "second"}}).want(t, 201, "")
		it = item()
		if it["firstWrite"] != first || it["lastActive"] == t0 {
			t.Fatalf("first write moved or last use did not: %v", it)
		}
		// A token is use too.
		r := tang.do("POST", "/api/account/tokens", map[string]any{"name": "t", "access": "read", "spaces": []string{sp}}).want(t, 201, "")
		tok := a.newClient("")
		tok.headers["Authorization"] = "Bearer " + r.body["secret"].(string)
		a.clock.Advance(2 * time.Hour)
		before := item()["lastActive"]
		tok.do("GET", "/api/spaces/"+sp+"/pages", nil).want(t, 200, "")
		if item()["lastActive"] == before {
			t.Fatal("a token's use is not recorded")
		}
		// The recount agrees.
		if n, err := a.st.RecountUsage(context.Background()); err != nil || n != 0 {
			t.Fatalf("the counters drifted: %d %v", n, err)
		}
		_ = fmt.Sprint
	})
}

// The server's own details are the site's: a tenant administrator is not told
// the Git version, whether a flag sets the hosts, the disk floor or the
// server-wide restores that run.
func TestStorageDetailsOfTheServerAreForSiteAdministrators(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		tenant := f.admA.do("GET", "/api/admin/storage", nil).want(t, 200, "")
		site := f.root.do("GET", "/api/admin/storage", nil).want(t, 200, "")
		for _, key := range []string{`"version"`, `"restoring"`} {
			if strings.Contains(tenant.raw, key) {
				t.Errorf("a tenant administrator is told %s: %s", key, tenant.raw)
			}
			if !strings.Contains(site.raw, key) {
				t.Errorf("a site administrator is not told %s: %s", key, site.raw)
			}
		}
		// The policy is settings, which only a site administrator reads (the disk
		// floor among them); here since, what the requirement asks of spaces and
		// the summary stay.
		f.admA.do("GET", "/api/admin/settings", nil).want(t, 403, "forbidden")
		if st := f.root.do("GET", "/api/admin/settings", nil).want(t, 200, ""); !strings.Contains(st.raw, `"cache.minFreeBytes"`) {
			t.Errorf("a site administrator is not told the disk floor: %s", st.raw)
		}
		if tenant.body["remotes"] == nil || tenant.body["cache"] != nil {
			t.Fatalf("a tenant administrator reads more or less than since and the summary: %s", tenant.raw)
		}
	})
}
