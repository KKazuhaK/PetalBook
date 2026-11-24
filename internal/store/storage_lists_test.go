package store_test

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// The usage rows of 3.5 part 4 and the lists over them (docs/api.md, "Storage
// management"): where a space's bytes live decides whether they count, every
// counter is kept by difference and agrees with the recount, and the lists page
// by keyset.

func usageRow(t *testing.T, st *store.Store, kind, id string) store.Usage {
	t.Helper()
	u, err := store.GetUsage(ctx, st.Q(), kind, id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

type counters struct {
	spaces, bytes, held, remote, partial, evicted, archived, pinned, connected, dormant int64
}

// rawUsage reads the columns the contract adds.
func rawUsage(t *testing.T, st *store.Store, kind, id string) counters {
	t.Helper()
	var c counters
	err := st.Q().QueryRow(ctx, `SELECT spaces, bytes, held_bytes, remote_spaces, partial_spaces, evicted_spaces, archived_spaces, pinned_spaces, connected, dormant FROM usage WHERE kind = ? AND id = ?`, kind, id).
		Scan(&c.spaces, &c.bytes, &c.held, &c.remote, &c.partial, &c.evicted, &c.archived, &c.pinned, &c.connected, &c.dormant)
	if err != nil {
		t.Fatal(kind, id, err)
	}
	return c
}

func wantCounters(t *testing.T, st *store.Store, kind, id string, want counters) {
	t.Helper()
	if got := rawUsage(t, st, kind, id); got != want {
		t.Fatalf("%s %s:\n got %+v\nwant %+v", kind, id, got, want)
	}
}

func putRemote(t *testing.T, st *store.Store, space, state string) {
	t.Helper()
	if err := store.PutRemote(ctx, st.Q(), store.Remote{SpaceID: space, URL: "https://git.example/" + space + ".git", Branch: "main", CredType: "token", State: state, Updated: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func spaceRow(t *testing.T, st *store.Store, id string) store.AdminSpace {
	t.Helper()
	r, err := store.GetAdminSpace(ctx, st.Q(), store.AllTenants(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A space counts where it lives: on the server in full, never when its remote is
// the person's own (it is shown, measured, and its cache is the pressure
// controller's); the counted bytes follow the remote as it comes and goes, and the
// rows agree with the recount at every step.
func TestUsageCountsWhereTheStorageLives(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		q := st.Q()
		now := time.Now()
		if err := store.AddMember(ctx, q, "Sa", "Ua", "owner", now); err != nil {
			t.Fatal(err)
		}
		// Only on the server: counted in full, and what the server holds.
		measure(t, st, "Sa", 1000, 4)
		wantCounters(t, st, store.UsageUser, "Ua", counters{spaces: 1, bytes: 1000, held: 1000, dormant: 1})
		wantCounters(t, st, store.UsageTenant, tenA, counters{spaces: 1, bytes: 1000, held: 1000, dormant: 1})
		if r := spaceRow(t, st, "Sa"); r.Location != store.LocationServer || !r.Counted() {
			t.Fatalf("%+v", r)
		}
		consistent(t, st)

		// A remote of the person's own: nothing counts any more, the server still holds the clone.
		putRemote(t, st, "Sa", "ok")
		wantCounters(t, st, store.UsageUser, "Ua", counters{spaces: 1, bytes: 0, held: 1000, remote: 1})
		wantCounters(t, st, store.UsageTenant, tenA, counters{spaces: 1, bytes: 0, held: 1000, remote: 1})
		r := spaceRow(t, st, "Sa")
		if r.Location != store.LocationOwn || r.Counted() || r.SizeBytes == nil || *r.SizeBytes != 1000 || r.RemoteState != "ok" {
			t.Fatalf("shown, measured, not counted: %+v", r)
		}
		consistent(t, st)

		// It is still measured and shown, and still counts nothing.
		measure(t, st, "Sa", 1500, 4)
		wantCounters(t, st, store.UsageUser, "Ua", counters{spaces: 1, bytes: 0, held: 1500, remote: 1})
		if r := spaceRow(t, st, "Sa"); *r.SizeBytes != 1500 {
			t.Fatalf("%+v", r)
		}
		consistent(t, st)

		// The state of the remote is copied to the space, for the filter.
		if err := store.UpdateRemoteStatus(ctx, q, "Sa", store.RemoteStatus{State: "offline"}, now); err != nil {
			t.Fatal(err)
		}
		if r := spaceRow(t, st, "Sa"); r.RemoteState != "offline" {
			t.Fatalf("%+v", r)
		}
		consistent(t, st)

		// The index, a partial clone, an eviction, a pin: what the server holds follows.
		if err := store.SetSpaceIndexBytes(ctx, q, "Sa", 200); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ua", counters{spaces: 1, held: 1700, remote: 1})
		if err := store.SetSpaceCache(ctx, q, "Sa", store.CachePartial); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ua", counters{spaces: 1, held: 1700, remote: 1, partial: 1})
		if err := store.SetSpacePinned(ctx, q, "Sa", true); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ua", counters{spaces: 1, held: 1700, remote: 1, partial: 1, pinned: 1})
		if err := store.SetSpaceCache(ctx, q, "Sa", store.CacheEvicted); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ua", counters{spaces: 1, held: 200, remote: 1, evicted: 1, pinned: 1})
		wantCounters(t, st, store.UsageTenant, tenA, counters{spaces: 1, held: 200, remote: 1, evicted: 1, pinned: 1})
		consistent(t, st)
		if _, err := q.Exec(ctx, `UPDATE spaces SET cache_state = 'archived' WHERE id = 'Sa'`); err != nil { // part 5 writes it; the recount reads it
			t.Fatal(err)
		}
		if n, err := st.RecountUsage(ctx); err != nil || n == 0 {
			t.Fatalf("the recount did not notice the archive: %d %v", n, err)
		}
		wantCounters(t, st, store.UsageUser, "Ua", counters{spaces: 1, held: 0, remote: 1, archived: 1, pinned: 1})
		consistent(t, st)
		if err := store.SetSpaceCache(ctx, q, "Sa", ""); err != nil {
			t.Fatal(err)
		}
		if err := store.SetSpacePinned(ctx, q, "Sa", false); err != nil {
			t.Fatal(err)
		}

		// The remote goes: the space lives on the server again and counts what it was last measured at.
		if err := store.DeleteRemote(ctx, q, "Sa"); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ua", counters{spaces: 1, bytes: 1500, held: 1700, dormant: 1})
		wantCounters(t, st, store.UsageTenant, tenA, counters{spaces: 1, bytes: 1500, held: 1700, dormant: 1})
		if r := spaceRow(t, st, "Sa"); r.Location != store.LocationServer || !r.Counted() || r.RemoteState != "" {
			t.Fatalf("%+v", r)
		}
		consistent(t, st)

		// A change of owner moves everything the space contributes.
		if err := store.AddMember(ctx, q, "Sa", "Ua", "owner", now); err == nil {
			t.Fatal("a member twice")
		}
		mkSameTenantUser(t, st, "Ub2", tenA)
		if err := st.Tx(ctx, func(q store.Q) error {
			_, err := store.TransferOwnership(ctx, q, "Sa", "Ub2", now.Add(time.Hour))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ua", counters{dormant: 1})
		wantCounters(t, st, store.UsageUser, "Ub2", counters{spaces: 1, bytes: 1500, held: 1700, dormant: 1})
		consistent(t, st)

		// Deleting it takes it out of both rows.
		if err := st.Tx(ctx, func(q store.Q) error { _, err := store.DeleteSpace(ctx, q, store.AllTenants(), "Sa"); return err }); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ub2", counters{dormant: 1})
		wantCounters(t, st, store.UsageTenant, tenA, counters{dormant: 2})
		consistent(t, st)
	})
}

func mkSameTenantUser(t *testing.T, st *store.Store, id, tenant string) {
	t.Helper()
	if err := store.InsertUser(ctx, st.Q(), store.User{ID: id, Username: "u-" + strings.ToLower(id), Name: id, Role: "member", Created: time.Now(), TenantID: tenant}); err != nil {
		t.Fatal(err)
	}
}

// A person is dormant until they change something, connect an account or follow a
// remote; the tenant counts them, and the recount agrees.
func TestDormantFollowsUseConnectionsAndRemotes(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		q := st.Q()
		now := time.Now()
		wantCounters(t, st, store.UsageUser, "Ua", counters{dormant: 1})
		wantCounters(t, st, store.UsageTenant, tenA, counters{spaces: 1, dormant: 1})
		consistent(t, st)

		// A first change ends it, once; a second changes nothing.
		if err := st.NoteWrite(ctx, "Ua", now); err != nil {
			t.Fatal(err)
		}
		if err := st.NoteWrite(ctx, "Ua", now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ua", counters{})
		wantCounters(t, st, store.UsageTenant, tenA, counters{spaces: 1})
		if n := count(t, st, `SELECT first_write FROM users WHERE id = 'Ua'`); n != int(now.UnixMilli()) {
			t.Fatalf("first_write %d, want the first time, %d", n, now.UnixMilli())
		}
		consistent(t, st)

		// A connection of any provider, and its removal. Ub has not written anything.
		wantCounters(t, st, store.UsageUser, "Ub", counters{dormant: 1})
		if err := store.PutGitHubAuthorization(ctx, q, store.GitHubAuthorization{UserID: "Ub", GitHubID: 99, Login: "b", State: "ok", Connected: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ub", counters{connected: 1})
		wantCounters(t, st, store.UsageTenant, tenB, counters{spaces: 1, connected: 1})
		consistent(t, st)
		if err := store.DeleteGitHubAuthorization(ctx, q, "Ub"); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ub", counters{dormant: 1})
		wantCounters(t, st, store.UsageTenant, tenB, counters{spaces: 1, dormant: 1})
		consistent(t, st)

		// A connection to a 3.8 provider counts the same, and so does losing it,
		// by the connection, by the provider's deletion and by the recount.
		for _, id := range []string{"gl", "gl2"} {
			if err := store.InsertConnProviderRow(ctx, q, store.ConnProviderRow{ID: id, Type: "gitlab", Host: id + ".example.com", Scheme: "https", Name: id, Owner: store.ConnProviderSite,
				State: "ok", Created: now, Updated: now}); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.PutProviderAuthorization(ctx, q, store.ProviderAuthorization{Provider: "gl", UserID: "Ub", AccountID: "7", Login: "b", State: "ok", Connected: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ub", counters{connected: 1})
		wantCounters(t, st, store.UsageTenant, tenB, counters{spaces: 1, connected: 1})
		consistent(t, st)
		if err := store.DeleteProviderAuthorization(ctx, q, "gl", "Ub"); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ub", counters{dormant: 1})
		consistent(t, st)
		if err := store.PutProviderAuthorization(ctx, q, store.ProviderAuthorization{Provider: "gl2", UserID: "Ub", AccountID: "8", Login: "b", State: "ok", Connected: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ub", counters{connected: 1})
		if err := store.DeleteConnProviderRow(ctx, q, "gl2"); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ub", counters{dormant: 1})
		wantCounters(t, st, store.UsageTenant, tenB, counters{spaces: 1, dormant: 1})
		consistent(t, st)

		// A space that follows a remote is not dormant either, and is dormant again without it.
		if err := store.AddMember(ctx, q, "Sb", "Ub", "owner", now); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ub", counters{spaces: 1, dormant: 1})
		putRemote(t, st, "Sb", "ok")
		wantCounters(t, st, store.UsageUser, "Ub", counters{spaces: 1, remote: 1})
		wantCounters(t, st, store.UsageTenant, tenB, counters{spaces: 1, remote: 1})
		consistent(t, st)
		if err := store.DeleteRemote(ctx, q, "Sb"); err != nil {
			t.Fatal(err)
		}
		wantCounters(t, st, store.UsageUser, "Ub", counters{spaces: 1, dormant: 1})
		consistent(t, st)

		// The list says the same, as an index range.
		got, _, err := store.ListPeople(ctx, q, st.Dialect(), store.AllTenants(), store.PeopleFilter{Dormant: true})
		if err != nil || len(got) != 1 || got[0].ID != "Ub" || !got[0].Dormant {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

// A person's last use is written at most once an hour, from memory.
func TestLastActiveIsWrittenOnceAnHour(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		t0 := time.Now().Truncate(time.Millisecond)
		if err := st.NoteActive(ctx, "Ua", t0); err != nil {
			t.Fatal(err)
		}
		if err := st.NoteActive(ctx, "Ua", t0.Add(59*time.Minute)); err != nil {
			t.Fatal(err)
		}
		last := func() int {
			return count(t, st, `SELECT last_active FROM users WHERE id = 'Ua'`)
		}
		if last() != int(t0.UnixMilli()) {
			t.Fatalf("written again within the hour: %d", last())
		}
		if got := count(t, st, `SELECT last_active FROM usage WHERE kind = 'user' AND id = 'Ua'`); got != int(t0.UnixMilli()) {
			t.Fatalf("the usage row's copy: %d", got)
		}
		if err := st.NoteActive(ctx, "Ua", t0.Add(61*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if last() != int(t0.Add(61*time.Minute).UnixMilli()) {
			t.Fatalf("not written after the hour: %d", last())
		}
		consistent(t, st)
	})
}

// The counts of spaces in their grace period and past it are set from the policy and
// agree with what the list's filter says.
func TestSettleDeadlinesAgreesWithTheFilter(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		q := st.Q()
		now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
		day := 24 * time.Hour
		mk := func(id string, age time.Duration, owner string) {
			t.Helper()
			if err := store.InsertSpace(ctx, q, tenA, id, id, "", "", now.Add(-age)); err != nil {
				t.Fatal(err)
			}
			if owner != "" {
				if err := store.AddMember(ctx, q, id, owner, "owner", now); err != nil {
					t.Fatal(err)
				}
			}
		}
		mkSameTenantUser(t, st, "Uc", tenA)
		if _, err := q.Exec(ctx, `UPDATE spaces SET created = ? WHERE id IN ('Sa', 'Sb')`, now.Add(-30*day).UnixMilli()); err != nil { // before the requirement, outside it
			t.Fatal(err)
		}
		mk("N1", 10*day, "Ua") // older than since: not covered under scope "new"
		mk("G1", 2*day, "Ua")  // in grace
		mk("G2", 1*day, "Ua")  // in grace
		mk("O1", 5*day, "Uc")  // overdue
		mk("O2", 6*day, "")    // overdue, nobody owns it
		mk("R1", 5*day, "Uc")  // overdue, but has a remote
		putRemote(t, st, "R1", "ok")
		mk("E1", 5*day, "Uc") // overdue, but exempt
		if err := store.SetSpaceExempt(ctx, q, "E1", true); err != nil {
			t.Fatal(err)
		}
		since := now.Add(-8 * day)
		p := store.RemotePolicy{Require: "any", Since: since, Grace: 3 * day, Now: now}
		// N1 was made before the requirement began and the scope is new spaces only.
		if n, err := st.SettleDeadlines(ctx, p); err != nil || n == 0 {
			t.Fatalf("%d %v", n, err)
		}
		got := func(id string) (g, o int64) {
			u, err := store.GetPerson(ctx, q, st.Dialect(), store.AllTenants(), id)
			if err != nil {
				t.Fatal(err)
			}
			return u.GraceSpaces, u.OverdueSpaces
		}
		if g, o := got("Ua"); g != 2 || o != 0 {
			t.Fatalf("Ua: grace %d overdue %d", g, o)
		}
		if g, o := got("Uc"); g != 0 || o != 1 { // R1 follows a remote, E1 is exempt
			t.Fatalf("Uc: grace %d overdue %d", g, o)
		}
		sum, err := store.GetStorageSummary(ctx, q, store.InTenant(tenA))
		if err != nil || sum.InGrace != 2 || sum.Overdue != 2 { // O2 counts for the tenant only
			t.Fatalf("%+v %v", sum, err)
		}
		ids := func(deadline string) string {
			rows, _, err := store.ListAllSpaces(ctx, q, store.AllTenants(), store.SpaceFilter{Deadline: deadline, Policy: &p, Sort: "name"})
			if err != nil {
				t.Fatal(err)
			}
			var l []string
			for _, r := range rows {
				l = append(l, r.ID)
			}
			return strings.Join(l, ",")
		}
		if g, o := ids("grace"), ids("overdue"); g != "G1,G2" || o != "O1,O2" {
			t.Fatalf("grace %q overdue %q", g, o)
		}
		// Scope "all": the old space is covered too, and its deadline is since + grace, which passed.
		pAll := p
		pAll.All = true
		if _, err := st.SettleDeadlines(ctx, pAll); err != nil {
			t.Fatal(err)
		}
		if g, o := got("Ua"); g != 2 || o != 1 {
			t.Fatalf("scope all, Ua: grace %d overdue %d", g, o)
		}
		rows, _, err := store.ListAllSpaces(ctx, q, store.AllTenants(), store.SpaceFilter{Deadline: "overdue", Policy: &pAll, Sort: "name"})
		if err != nil || len(rows) != 5 || rows[0].ID != "N1" || rows[4].ID != "Sb" {
			t.Fatalf("%v %v", rows, err)
		}
		// The requirement is lifted: nobody is in grace or overdue, and the rows say so.
		if _, err := st.SettleDeadlines(ctx, store.RemotePolicy{Now: now}); err != nil {
			t.Fatal(err)
		}
		if g, o := got("Ua"); g != 0 || o != 0 {
			t.Fatalf("no policy: grace %d overdue %d", g, o)
		}
		if n, err := st.SettleDeadlines(ctx, store.RemotePolicy{Now: now}); err != nil || n != 0 {
			t.Fatalf("a second pass changed %d rows: %v", n, err)
		}
		consistent(t, st)
	})
}

// --- the lists -----------------------------------------------------------------------

type fixtureSpace struct {
	id, name        string
	created         time.Time
	size            *int64
	used            time.Time
	tenant          string
	personalOf      string
	pinned, exempt  bool
	remote, cache   string
	owner           string
	bytesForCounted int64
}

func i64(n int64) *int64 { return &n }

func insertFixture(t *testing.T, st *store.Store, f fixtureSpace) {
	t.Helper()
	q := st.Q()
	if f.tenant == "" {
		f.tenant = tenA
	}
	if err := store.InsertSpace(ctx, q, f.tenant, f.id, f.name, f.personalOf, "", f.created); err != nil {
		t.Fatal(err)
	}
	if f.owner != "" {
		if err := store.AddMember(ctx, q, f.id, f.owner, "owner", f.created); err != nil {
			t.Fatal(err)
		}
	}
	if f.size != nil {
		if err := store.SetSpaceSize(ctx, q, f.id, *f.size, f.created); err != nil {
			t.Fatal(err)
		}
	}
	used := int64(0) // never used
	if !f.used.IsZero() {
		used = f.used.UnixMilli()
	}
	if _, err := q.Exec(ctx, `UPDATE spaces SET last_used = ? WHERE id = ?`, used, f.id); err != nil {
		t.Fatal(err)
	}
	if f.remote != "" {
		putRemote(t, st, f.id, f.remote)
	}
	if f.cache != "" {
		if err := store.SetSpaceCache(ctx, q, f.id, f.cache); err != nil {
			t.Fatal(err)
		}
	}
	if f.pinned {
		if err := store.SetSpacePinned(ctx, q, f.id, true); err != nil {
			t.Fatal(err)
		}
	}
	if f.exempt {
		if err := store.SetSpaceExempt(ctx, q, f.id, true); err != nil {
			t.Fatal(err)
		}
	}
}

// listFixture makes 30 spaces of tenant A with every kind of tie and gap: equal
// creation times, equal sizes, equal names apart from case, spaces never measured,
// spaces never used.
func listFixture(t *testing.T, st *store.Store) []fixtureSpace {
	t.Helper()
	twoTenants(t, st)
	for _, id := range []string{"Sa", "Sb"} {
		if err := deleteSpace(st, id); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var all []fixtureSpace
	for i := 0; i < 30; i++ {
		f := fixtureSpace{id: fmt.Sprintf("L%03d", i), name: fmt.Sprintf("Space %02d", i%7), created: base.Add(time.Duration(i/3) * time.Hour)}
		switch i % 5 {
		case 0: // never measured
		default:
			f.size = i64(int64(100 * (i % 4)))
		}
		if i%4 != 0 {
			f.used = base.Add(time.Duration(i%6) * 24 * time.Hour)
		}
		if i%3 == 0 {
			f.remote, f.cache = "ok", []string{store.CacheFull, store.CachePartial, store.CacheEvicted}[(i/3)%3]
		}
		if i%7 == 0 {
			f.pinned = true
		}
		if i%9 == 0 {
			f.exempt = true
		}
		if i%10 == 3 {
			f.remote = "offline"
		}
		insertFixture(t, st, f)
		all = append(all, f)
	}
	// Another tenant's, which a list of tenant A never shows.
	insertFixture(t, st, fixtureSpace{id: "LB01", name: "Space B", created: base, tenant: tenB, size: i64(5)})
	return all
}

func pageThrough(t *testing.T, st *store.Store, sc store.Scope, f store.SpaceFilter, limit int) []store.AdminSpace {
	t.Helper()
	var out []store.AdminSpace
	f.Limit = limit
	for pages := 0; ; pages++ {
		if pages > 200 {
			t.Fatal("the pages do not end")
		}
		rows, next, err := store.ListAllSpaces(ctx, st.Q(), sc, f)
		if err != nil {
			t.Fatalf("%+v: %v", f, err)
		}
		out = append(out, rows...)
		if next == "" {
			return out
		}
		f.After = next
	}
}

func idsOf(rows []store.AdminSpace) string {
	l := make([]string, len(rows))
	for i, r := range rows {
		l[i] = r.ID
	}
	return strings.Join(l, " ")
}

// Every sort, both orders, any page size: each space once, in the order the sort
// says, the ones without a value last, whatever the ties.
func TestSpaceListPagesByKeysetInEverySort(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		fx := listFixture(t, st)
		sc := store.InTenant(tenA)
		byID := map[string]fixtureSpace{}
		for _, f := range fx {
			byID[f.id] = f
		}
		// The order each sort promises, from the fixture alone. Ties inside a
		// value are in id order, ascending for the default direction of the sort,
		// the other way round for the other.
		type key struct {
			has bool
			n   int64
			s   string
		}
		keyOf := map[string]func(f fixtureSpace) key{
			"created":  func(f fixtureSpace) key { return key{true, f.created.UnixMilli(), ""} },
			"name":     func(f fixtureSpace) key { return key{true, 0, store.FoldName(f.name)} },
			"bytes":    func(f fixtureSpace) key { return key{f.size != nil, derefOr(f.size), ""} },
			"lastUsed": func(f fixtureSpace) key { return key{!f.used.IsZero(), f.used.UnixMilli(), ""} },
		}
		defaultDesc := map[string]bool{"created": false, "name": false, "bytes": true, "lastUsed": true}
		for sortName, kf := range keyOf {
			for _, order := range []string{"asc", "desc"} {
				want := append([]fixtureSpace(nil), fx...)
				desc := order == "desc"
				idAsc := desc == defaultDesc[sortName]
				sort.SliceStable(want, func(i, j int) bool {
					a, b := kf(want[i]), kf(want[j])
					if a.has != b.has {
						return a.has // the ones with a value first
					}
					if a.has {
						if a.n != b.n || a.s != b.s {
							less := a.n < b.n || (a.n == b.n && a.s < b.s)
							if desc {
								return !less
							}
							return less
						}
						if idAsc {
							return want[i].id < want[j].id
						}
						return want[i].id > want[j].id
					}
					return want[i].id < want[j].id // without a value: by id
				})
				var wantIDs []string
				for _, w := range want {
					wantIDs = append(wantIDs, w.id)
				}
				for _, limit := range []int{1, 4, 7, 29, 30, 50} {
					got := pageThrough(t, st, sc, store.SpaceFilter{Sort: sortName, Order: order}, limit)
					if idsOf(got) != strings.Join(wantIDs, " ") {
						t.Fatalf("sort %s %s, pages of %d:\n got %s\nwant %s", sortName, order, limit, idsOf(got), strings.Join(wantIDs, " "))
					}
				}
			}
		}
	})
}

func derefOr(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// A cursor of one sort or order is no cursor of another, and unknown values are refused.
func TestSpaceListRefusesWhatItDoesNotKnow(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		listFixture(t, st)
		sc := store.InTenant(tenA)
		_, next, err := store.ListAllSpaces(ctx, st.Q(), sc, store.SpaceFilter{Sort: "bytes", Limit: 3})
		if err != nil || next == "" {
			t.Fatalf("%q %v", next, err)
		}
		for _, f := range []store.SpaceFilter{
			{Sort: "created", After: next},
			{Sort: "bytes", Order: "asc", After: next},
			{Sort: "bytes", After: "garbage"},
			{Sort: "bytes", After: "bytes.desc:x:L001"},
			{Sort: "bytes", After: "bytes.desc.none:"},
		} {
			if _, _, err := store.ListAllSpaces(ctx, st.Q(), sc, f); err != store.ErrBadCursor {
				t.Fatalf("%+v: %v", f, err)
			}
		}
		for _, f := range []store.SpaceFilter{{Sort: "size"}, {Order: "up"}, {Cache: "warm"}, {Remote: "bad"}, {Deadline: "soon"}} {
			if _, _, err := store.ListAllSpaces(ctx, st.Q(), sc, f); err != store.ErrBadFilter {
				t.Fatalf("%+v: %v", f, err)
			}
		}
	})
}

// A page of a list is not changed by spaces made, renamed or removed while someone
// pages through it: every space that was there throughout is listed exactly once.
func TestCursorStaysStableWhileSpacesAreInsertedAndRemoved(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		fx := listFixture(t, st)
		sc := store.InTenant(tenA)
		q := st.Q()
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		gone := map[string]bool{}
		for round, c := range []struct{ sort, order string }{{"created", "asc"}, {"created", "desc"}, {"bytes", "desc"}, {"bytes", "asc"}, {"name", "asc"}, {"lastUsed", "desc"}} {
			// Stays: the spaces the test does not touch. Removed: seen or not, no matter.
			stay := map[string]bool{}
			for _, f := range fx {
				if !gone[f.id] {
					stay[f.id] = true
				}
			}
			victim := fmt.Sprintf("L%03d", 17+round)
			var seen []string
			f := store.SpaceFilter{Sort: c.sort, Order: c.order, Limit: 5}
			pages := 0
			var extra []string
			for {
				rows, next, err := store.ListAllSpaces(ctx, q, sc, f)
				if err != nil {
					t.Fatal(err)
				}
				for _, r := range rows {
					seen = append(seen, r.ID)
				}
				if next == "" {
					break
				}
				f.After = next
				pages++
				// Between pages: spaces appear everywhere in the order (before, among
				// and after what was read), one is deleted, one gets a new size.
				id := fmt.Sprintf("X%s%s%02d", c.sort[:2], c.order[:1], pages)
				extra = append(extra, id)
				insertFixture(t, st, fixtureSpace{id: id, name: fmt.Sprintf("Space %02d", pages%7), created: base.Add(time.Duration(pages*7-20) * time.Hour), size: i64(int64(pages * 50)), used: base.Add(time.Duration(pages) * time.Hour)})
				if pages == 2 {
					if err := deleteSpace(st, victim); err != nil {
						t.Fatal(err)
					}
					delete(stay, victim)
					gone[victim] = true
				}
				if pages == 3 {
					if err := store.SetSpaceSize(ctx, q, "L001", 9999, base); err != nil {
						t.Fatal(err)
					}
					delete(stay, "L001") // moved in the order: it may be seen once or not at all, but never twice
				}
			}
			count := map[string]int{}
			for _, id := range seen {
				count[id]++
			}
			for id, n := range count {
				if n > 1 {
					t.Fatalf("%s %s: %s was listed %d times", c.sort, c.order, id, n)
				}
			}
			for id := range stay {
				if count[id] != 1 {
					t.Fatalf("%s %s: %s was listed %d times", c.sort, c.order, id, count[id])
				}
			}
			// Back to the fixture for the next round.
			for _, id := range extra {
				if err := deleteSpace(st, id); err != nil {
					t.Fatal(err)
				}
			}
		}
	})
}

// Each filter keeps exactly what it says, and they combine.
func TestSpaceListFilters(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		fx := listFixture(t, st)
		sc := store.InTenant(tenA)
		q := st.Q()
		check := func(name string, f store.SpaceFilter, want func(f fixtureSpace) bool) {
			t.Helper()
			var w []string
			for _, x := range fx {
				if want(x) {
					w = append(w, x.id)
				}
			}
			f.Sort = "name"
			rows := pageThrough(t, st, sc, f, 6)
			got := map[string]bool{}
			for _, r := range rows {
				got[r.ID] = true
			}
			if len(rows) != len(w) {
				t.Fatalf("%s: got %s, want %v", name, idsOf(rows), w)
			}
			for _, id := range w {
				if !got[id] {
					t.Fatalf("%s: got %s, want %v", name, idsOf(rows), w)
				}
			}
		}
		hasRemote := func(f fixtureSpace) bool { return f.remote != "" }
		check("cache local", store.SpaceFilter{Cache: "local"}, func(f fixtureSpace) bool { return !hasRemote(f) })
		check("cache evicted", store.SpaceFilter{Cache: "evicted"}, func(f fixtureSpace) bool { return hasRemote(f) && f.cache == store.CacheEvicted })
		check("cache partial", store.SpaceFilter{Cache: "partial"}, func(f fixtureSpace) bool { return hasRemote(f) && f.cache == store.CachePartial })
		check("cache full", store.SpaceFilter{Cache: "full"}, func(f fixtureSpace) bool { return hasRemote(f) && (f.cache == store.CacheFull || f.cache == "") })
		check("cache restoring", store.SpaceFilter{Cache: "restoring"}, func(f fixtureSpace) bool { return false })
		check("remote none", store.SpaceFilter{Remote: "none"}, func(f fixtureSpace) bool { return !hasRemote(f) })
		check("remote ok", store.SpaceFilter{Remote: "ok"}, func(f fixtureSpace) bool { return f.remote == "ok" })
		check("remote offline", store.SpaceFilter{Remote: "offline"}, func(f fixtureSpace) bool { return f.remote == "offline" })
		check("remote error", store.SpaceFilter{Remote: "error"}, func(f fixtureSpace) bool { return false })
		check("pinned", store.SpaceFilter{Pinned: true}, func(f fixtureSpace) bool { return f.pinned })
		check("exempt", store.SpaceFilter{Exempt: true}, func(f fixtureSpace) bool { return f.exempt })
		check("pinned and remote", store.SpaceFilter{Pinned: true, Remote: "ok"}, func(f fixtureSpace) bool { return f.pinned && f.remote == "ok" })
		cut := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
		check("idle", store.SpaceFilter{IdleBefore: cut}, func(f fixtureSpace) bool { return f.used.Before(cut) }) // never used (zero) is before
		check("name", store.SpaceFilter{Query: "space 03"}, func(f fixtureSpace) bool { return strings.EqualFold(f.name, "Space 03") })
		// error and rewritten are both "error".
		if err := store.UpdateRemoteStatus(ctx, q, "L006", store.RemoteStatus{State: store.RemoteRewritten}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := store.UpdateRemoteStatus(ctx, q, "L012", store.RemoteStatus{State: store.RemoteError}, time.Now()); err != nil {
			t.Fatal(err)
		}
		rows := pageThrough(t, st, sc, store.SpaceFilter{Remote: "error", Sort: "name"}, 10)
		if len(rows) != 2 {
			t.Fatalf("%s", idsOf(rows))
		}
		// An owner's spaces: counted for the first owner.
		insertFixture(t, st, fixtureSpace{id: "LO1", name: "Mine", created: time.Now(), owner: "Ua"})
		insertFixture(t, st, fixtureSpace{id: "LO2", name: "Mine too", created: time.Now(), owner: "Ua"})
		if rows := pageThrough(t, st, sc, store.SpaceFilter{Owner: "Ua"}, 1); len(rows) != 2 {
			t.Fatalf("%s", idsOf(rows))
		}
		if rows := pageThrough(t, st, store.InTenant(tenB), store.SpaceFilter{Owner: "Ua"}, 1); len(rows) != 0 {
			t.Fatalf("another tenant's scope lists %s", idsOf(rows))
		}
	})
}

// A list of one tenant never holds another's, a tenant administrator's scope leaves
// out personal spaces, and a site administrator's does not.
func TestListsAreScopedAndHidePersonalSpaces(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		listFixture(t, st)
		now := time.Now()
		q := st.Q()
		if err := store.InsertSpace(ctx, q, tenA, "PSa", "Personal of Ua", "Ua", "", now); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMember(ctx, q, "PSa", "Ua", "owner", now); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertSpace(ctx, q, tenB, "PSb", "Personal of Ub", "Ub", "", now); err != nil {
			t.Fatal(err)
		}
		tenantAdmin := store.AdminScopeOf(store.User{TenantID: tenA, Role: "admin"})
		siteAdmin := store.AdminScopeOf(store.User{TenantID: tenA, Role: "admin", SiteAdmin: true})
		all := pageThrough(t, st, tenantAdmin, store.SpaceFilter{Sort: "bytes"}, 7)
		for _, r := range all {
			if r.TenantID != tenA || r.Personal || r.ID == "PSa" {
				t.Fatalf("a tenant administrator's list holds %+v", r)
			}
		}
		if len(all) != 30 {
			t.Fatalf("%d spaces", len(all))
		}
		if rows := pageThrough(t, st, tenantAdmin, store.SpaceFilter{Owner: "Ua"}, 7); len(rows) != 0 {
			t.Fatalf("a tenant administrator sees the personal space by its owner: %s", idsOf(rows))
		}
		if rows := pageThrough(t, st, siteAdmin, store.SpaceFilter{Owner: "Ua"}, 7); idsOf(rows) != "PSa" {
			t.Fatalf("a site administrator does not: %s", idsOf(rows))
		}
		if rows := pageThrough(t, st, siteAdmin.Narrow(tenB), store.SpaceFilter{}, 7); len(rows) != 2 {
			t.Fatalf("%s", idsOf(rows))
		}
		// The people and the summary of a tenant are the tenant's.
		people, _, err := store.ListPeople(ctx, q, st.Dialect(), store.InTenant(tenA), store.PeopleFilter{})
		if err != nil || len(people) != 1 || people[0].ID != "Ua" {
			t.Fatalf("%+v %v", people, err)
		}
		if _, err := store.GetPerson(ctx, q, st.Dialect(), store.InTenant(tenA), "Ub"); err != store.ErrNotFound {
			t.Fatalf("another tenant's person: %v", err)
		}
		sum, err := store.GetStorageSummary(ctx, q, store.InTenant(tenB))
		if err != nil || sum.People != 1 {
			t.Fatalf("%+v %v", sum, err)
		}
		whole, err := store.GetStorageSummary(ctx, q, store.AllTenants())
		if err != nil || whole.People != 2 {
			t.Fatalf("%+v %v", whole, err)
		}
		if zero, err := store.GetStorageSummary(ctx, q, store.Scope{}); err != nil || zero.People != 0 || zero.Local != 0 {
			t.Fatalf("the zero scope sees %+v %v", zero, err)
		}
	})
}

// The people's list: its sorts, its filters, a deleted person left out, and the
// ones never active last.
func TestPeopleListSortsFiltersAndPages(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		q := st.Q()
		base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		for i := 0; i < 12; i++ {
			id := fmt.Sprintf("P%02d", i)
			if err := store.InsertUser(ctx, q, store.User{ID: id, Username: fmt.Sprintf("p%02d", i), Name: fmt.Sprintf("Person %d", i%3), Role: "member", Created: base.Add(time.Duration(i/2) * time.Hour), TenantID: tenA}); err != nil {
				t.Fatal(err)
			}
			if i%3 != 0 {
				if err := st.NoteActive(ctx, id, base.Add(time.Duration(i%4)*24*time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			if i%2 == 0 {
				if err := st.NoteWrite(ctx, id, base); err != nil {
					t.Fatal(err)
				}
			}
			sid := "SP" + id
			if err := store.InsertSpace(ctx, q, tenA, sid, sid, "", "", base); err != nil {
				t.Fatal(err)
			}
			if err := store.AddMember(ctx, q, sid, id, "owner", base); err != nil {
				t.Fatal(err)
			}
			measure(t, st, sid, int64(1000*(i%4)), 1)
			if i%4 == 1 {
				putRemote(t, st, sid, "ok")
			}
		}
		if _, err := q.Exec(ctx, `UPDATE users SET disabled = 1 WHERE id = 'P05'`); err != nil {
			t.Fatal(err)
		}
		consistent(t, st)
		all := func(f store.PeopleFilter, limit int) []store.PersonUsage {
			t.Helper()
			var out []store.PersonUsage
			f.Limit = limit
			for n := 0; n < 100; n++ {
				rows, next, err := store.ListPeople(ctx, q, st.Dialect(), store.InTenant(tenA), f)
				if err != nil {
					t.Fatalf("%+v %v", f, err)
				}
				out = append(out, rows...)
				if next == "" {
					return out
				}
				f.After = next
			}
			t.Fatal("no end")
			return nil
		}
		ids := func(l []store.PersonUsage) string {
			var s []string
			for _, p := range l {
				s = append(s, p.ID)
			}
			return strings.Join(s, " ")
		}
		// 13 people: Ua too.
		for _, sortName := range store.PersonSorts {
			for _, order := range []string{"asc", "desc"} {
				whole := all(store.PeopleFilter{Sort: sortName, Order: order}, 100)
				if len(whole) != 13 {
					t.Fatalf("%s %s: %d people", sortName, order, len(whole))
				}
				for _, limit := range []int{1, 3, 5} {
					if got := all(store.PeopleFilter{Sort: sortName, Order: order}, limit); ids(got) != ids(whole) {
						t.Fatalf("%s %s pages of %d:\n%s\n%s", sortName, order, limit, ids(got), ids(whole))
					}
				}
			}
		}
		// Largest first by default, and the never-active last by lastActive in either order.
		byHeld := all(store.PeopleFilter{}, 100)
		for i := 1; i < len(byHeld); i++ {
			if byHeld[i-1].HeldBytes < byHeld[i].HeldBytes {
				t.Fatalf("not by held, largest first: %s", ids(byHeld))
			}
		}
		for _, order := range []string{"asc", "desc"} {
			l := all(store.PeopleFilter{Sort: "lastActive", Order: order}, 100)
			seenNever := false
			for _, p := range l {
				if p.LastActive.IsZero() {
					seenNever = true
				} else if seenNever {
					t.Fatalf("lastActive %s: someone active after someone never active: %s", order, ids(l))
				}
			}
		}
		if got := all(store.PeopleFilter{Dormant: true}, 4); len(got) != 4+1 { // odd and never connected: P01 P03 P05 P07 P09 P11 have written nothing; those with a remote are not dormant
			// computed below instead of guessed
			var want int
			for i := 0; i < 12; i++ {
				if i%2 != 0 && i%4 != 1 {
					want++
				}
			}
			want++ // Ua, who is in tenant A and never did anything
			if len(got) != want {
				t.Fatalf("dormant: %d, want %d: %s", len(got), want, ids(got))
			}
		}
		if got := all(store.PeopleFilter{Remote: "some"}, 4); len(got) != 3 {
			t.Fatalf("remote some: %s", ids(got))
		}
		if got := all(store.PeopleFilter{Remote: "none"}, 4); len(got) != 10 {
			t.Fatalf("remote none: %s", ids(got))
		}
		off := true
		if got := all(store.PeopleFilter{Disabled: &off}, 4); ids(got) != "P05" {
			t.Fatalf("disabled: %s", ids(got))
		}
		if got := all(store.PeopleFilter{Query: "PERSON 2"}, 2); len(got) != 4 {
			t.Fatalf("q: %s", ids(got))
		}
		// Active before a day: never active counts.
		cut := base.Add(2 * 24 * time.Hour)
		got := all(store.PeopleFilter{InactiveBefore: cut}, 3)
		for _, p := range got {
			if !p.LastActive.IsZero() && !p.LastActive.Before(cut) {
				t.Fatalf("%+v", p)
			}
		}
		if len(got) == 0 || len(got) == 13 {
			t.Fatalf("inactive: %d", len(got))
		}
		// A person in the recycle bin is not listed.
		if _, err := q.Exec(ctx, `UPDATE users SET deleted_at = 1 WHERE id = 'P02'`); err != nil {
			t.Fatal(err)
		}
		if got := all(store.PeopleFilter{}, 100); len(got) != 12 {
			t.Fatalf("a deleted person is listed: %d", len(got))
		}
	})
}

func deleteSpace(st *store.Store, id string) error {
	return st.Tx(ctx, func(q store.Q) error { _, err := store.DeleteSpace(ctx, q, store.AllTenants(), id); return err })
}

// A long, mixed run of every change that moves a counter, in a fixed random
// order, leaves the counters where the daily recount would put them: the
// differences added along the way and the recount from the rows agree.
func TestCountersStayWhereTheRecountPutsThemUnderMixedChanges(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		q := st.Q()
		rng := rand.New(rand.NewSource(20261124))
		now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
		users := []string{"Ua"}
		for i := 0; i < 4; i++ {
			id := fmt.Sprintf("Ux%d", i)
			mkSameTenantUser(t, st, id, tenA)
			users = append(users, id)
		}
		spaces := []string{"Sa"}
		next := 0
		pick := func(l []string) string { return l[rng.Intn(len(l))] }
		for step := 1; step <= 400; step++ {
			now = now.Add(time.Minute)
			switch op := rng.Intn(14); {
			case op == 0 || len(spaces) < 3:
				id := fmt.Sprintf("Sm%03d", next)
				next++
				if err := store.InsertSpace(ctx, q, tenA, id, id, "", "", now); err != nil {
					t.Fatal(err)
				}
				if rng.Intn(4) != 0 {
					if err := store.AddMember(ctx, q, id, pick(users), "owner", now); err != nil {
						t.Fatal(err)
					}
				}
				spaces = append(spaces, id)
			case op == 1:
				_ = store.AddMember(ctx, q, pick(spaces), pick(users), []string{"owner", "editor", "viewer"}[rng.Intn(3)], now) // may be a member already
			case op == 2:
				if err := st.Tx(ctx, func(q store.Q) error {
					_, err := store.TransferOwnership(ctx, q, pick(spaces), pick(users), now)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case op == 3 || op == 4:
				if _, err := st.RecordMeasurement(ctx, pick(spaces), store.Measurement{Bytes: int64(rng.Intn(5000)), Pages: int64(rng.Intn(9)), HavePages: rng.Intn(2) == 0, At: now}); err != nil {
					t.Fatal(err)
				}
			case op == 5:
				putRemote(t, st, pick(spaces), []string{"ok", "offline", "error"}[rng.Intn(3)])
			case op == 6:
				if err := store.DeleteRemote(ctx, q, pick(spaces)); err != nil {
					t.Fatal(err)
				}
			case op == 7:
				if err := store.SetSpaceCache(ctx, q, pick(spaces), []string{"", store.CacheFull, store.CachePartial, store.CacheEvicted}[rng.Intn(4)]); err != nil {
					t.Fatal(err)
				}
			case op == 8:
				if err := store.SetSpacePinned(ctx, q, pick(spaces), rng.Intn(2) == 0); err != nil {
					t.Fatal(err)
				}
			case op == 9:
				if err := store.SetSpaceIndexBytes(ctx, q, pick(spaces), int64(rng.Intn(900))); err != nil {
					t.Fatal(err)
				}
			case op == 10:
				if err := store.SetSpaceSize(ctx, q, pick(spaces), int64(rng.Intn(4000)), now); err != nil {
					t.Fatal(err)
				}
			case op == 11:
				if err := st.NoteWrite(ctx, pick(users), now); err != nil {
					t.Fatal(err)
				}
			case op == 12:
				u := pick(users)
				if rng.Intn(2) == 0 {
					if err := store.PutGitHubAuthorization(ctx, q, store.GitHubAuthorization{UserID: u, GitHubID: int64(1000 + len(u)*7 + int(u[len(u)-1])), Login: "gh-" + u, State: "ok", Connected: now, Updated: now}); err != nil {
						t.Fatal(err)
					}
				} else if err := store.DeleteGitHubAuthorization(ctx, q, u); err != nil {
					t.Fatal(err)
				}
			case op == 13 && len(spaces) > 4:
				i := rng.Intn(len(spaces))
				if err := deleteSpace(st, spaces[i]); err != nil {
					t.Fatal(err)
				}
				spaces = append(spaces[:i], spaces[i+1:]...)
			}
			if step%25 == 0 {
				consistent(t, st)
			}
		}
		consistent(t, st)
	})
}

// The cursor of the name sort is the stored key of the last row, not the name
// folded again: a key that differs from the fold (an older folding, a repair)
// still pages in order, with nothing skipped or shown twice.
func TestTheNameCursorIsTheStoredKey(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		for id, name := range map[string]string{"S1": "Zed", "S2": "Bee", "S3": "Cee"} {
			mkSpace(t, st, id, name)
		}
		for id, key := range map[string]string{"S1": "a1", "S2": "b1", "S3": "c1"} {
			if _, err := st.Q().Exec(ctx, `UPDATE spaces SET name_key = ? WHERE id = ?`, key, id); err != nil {
				t.Fatal(err)
			}
		}
		var got []string
		after := ""
		for i := 0; i < 5; i++ {
			rows, next, err := store.ListAllSpaces(ctx, st.Q(), store.AllTenants(), store.SpaceFilter{Sort: "name", Limit: 1, After: after})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				got = append(got, r.ID)
			}
			if next == "" {
				break
			}
			after = next
		}
		if strings.Join(got, ",") != "S1,S2,S3" {
			t.Fatalf("paged %v, want S1,S2,S3", got)
		}
	})
}
