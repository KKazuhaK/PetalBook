package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

var ctx = context.Background()

func mkUser(t *testing.T, st *store.Store, id, username, role string) {
	t.Helper()
	err := store.InsertUser(ctx, st.Q(), store.User{ID: id, Username: username, Name: username, Role: role, Created: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		// Open already migrated once; running again must change nothing.
		var n int
		if err := st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != 11 {
			t.Fatalf("%d %v", n, err)
		}
		mkUser(t, st, "A", "a", "admin")
	})
}

func TestReopenKeepsData(t *testing.T) {
	path := t.TempDir() + "/s.db"
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	mkUser(t, st, "A", "a", "admin")
	st.Close()
	st, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if u, err := store.GetUserByUsername(ctx, st.Q(), "a"); err != nil || u.ID != "A" {
		t.Fatalf("%v %v", u, err)
	}
}

func TestNewerSchemaIsRefused(t *testing.T) {
	path := t.TempDir() + "/s.db"
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	st.Q().Exec(ctx, `INSERT INTO schema_migrations(version, applied) VALUES (99, 0)`)
	st.Close()
	if _, err := store.Open(ctx, path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("%v", err)
	}
}

func TestUniqueViolationsAreRecognised(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "alice", "member")
		err := store.InsertUser(ctx, st.Q(), store.User{ID: "B", Username: "alice", Name: "x", Role: "member", Created: time.Now()})
		on, ok := store.UniqueOn(err)
		if !ok || !strings.Contains(on, "username") {
			t.Fatalf("%q %v %v", on, ok, err)
		}
		err = store.InsertUser(ctx, st.Q(), store.User{ID: "A", Username: "other", Name: "x", Role: "member", Created: time.Now()})
		if on, ok := store.UniqueOn(err); !ok || strings.Contains(on, "username") {
			t.Fatalf("%q %v %v", on, ok, err)
		}
		if store.IsUnique(nil) {
			t.Fatal("no error counted as unique")
		}
		if store.IsUnique(context.Canceled) {
			t.Fatal("any error counted as unique")
		}
	})
}

func TestTransactionRollsBack(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		err := st.Tx(ctx, func(q store.Q) error {
			if err := store.InsertUser(ctx, q, store.User{ID: "A", Username: "a", Name: "a", Role: "member", Created: time.Now()}); err != nil {
				return err
			}
			return context.Canceled
		})
		if err != context.Canceled {
			t.Fatal(err)
		}
		if ok, _ := store.HasUsers(ctx, st.Q()); ok {
			t.Fatal("a rolled-back user is visible")
		}
	})
}

func TestFirstUserMarkerAllowsOneWinner(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		var wg sync.WaitGroup
		results := make([]error, 8)
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = st.Tx(ctx, func(q store.Q) error {
					if err := store.MarkFirstUser(ctx, q, true, time.Now()); err != nil {
						return err
					}
					return store.InsertUser(ctx, q, store.User{ID: "U" + string(rune('A'+i)), Username: "u" + string(rune('a'+i)), Name: "x", Role: "admin", Created: time.Now()})
				})
			}()
		}
		wg.Wait()
		wins := 0
		for _, err := range results {
			switch err {
			case nil:
				wins++
			case store.ErrExists:
			default:
				t.Errorf("unexpected: %v", err)
			}
		}
		users, _ := store.ListUsers(ctx, st.Q())
		if wins != 1 || len(users) != 1 {
			t.Fatalf("wins=%d users=%d", wins, len(users))
		}
		// The relaxed form (used by the command line) never fails.
		if err := st.Tx(ctx, func(q store.Q) error { return store.MarkFirstUser(ctx, q, false, time.Now()) }); err != nil {
			t.Fatal(err)
		}
	})
}

func TestInviteIsUsedOnce(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "admin")
		now := time.Now()
		if err := store.InsertInvite(ctx, st.Q(), store.Invite{ID: "I", TokenHash: "h", Role: "member", CreatedBy: "A", Created: now, Expires: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wins := make(chan bool, 6)
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, err := store.UseInvite(ctx, st.Q(), "I", "A", now)
				if err != nil {
					t.Error(err)
				}
				wins <- ok
			}()
		}
		wg.Wait()
		close(wins)
		n := 0
		for w := range wins {
			if w {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%d winners", n)
		}
		if _, err := store.FindUsableInvite(ctx, st.Q(), "h", now); err != store.ErrNotFound {
			t.Fatalf("a used invite is still usable: %v", err)
		}
		// Expired invites are not usable and not listed.
		store.InsertInvite(ctx, st.Q(), store.Invite{ID: "J", TokenHash: "h2", Role: "member", CreatedBy: "A", Created: now, Expires: now.Add(time.Minute)})
		if _, err := store.FindUsableInvite(ctx, st.Q(), "h2", now.Add(2*time.Minute)); err != store.ErrNotFound {
			t.Fatal(err)
		}
		if l, _ := store.ListOpenInvites(ctx, st.Q(), now.Add(2*time.Minute)); len(l) != 0 {
			t.Fatalf("%v", l)
		}
	})
}

func TestSpacesAndMembership(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "admin")
		mkUser(t, st, "B", "b", "member")
		now := time.Now()
		for _, s := range []struct{ id, name, personal, owner string }{
			{"S1", "Zed", "", "A"}, {"S2", "My notes", "A", "A"}, {"S3", "Alpha", "", "A"}, {"S4", "Other", "B", "B"},
		} {
			if err := store.InsertSpace(ctx, st.Q(), s.id, s.name, s.personal, "", now); err != nil {
				t.Fatal(err)
			}
			store.AddMember(ctx, st.Q(), s.id, s.owner, "owner", now)
		}
		got, err := store.ListUserSpaces(ctx, st.Q(), "A")
		if err != nil || len(got) != 3 {
			t.Fatalf("%v %v", got, err)
		}
		// A second personal space for the same user is refused by the database.
		if err := store.InsertSpace(ctx, st.Q(), "S5", "again", "A", "", now); !store.IsUnique(err) {
			t.Fatalf("%v", err)
		}
		o, name, err := store.SpaceOwner(ctx, st.Q(), "S4")
		if err != nil || o.ID != "B" || name != "Other" {
			t.Fatalf("%v %v %v", o, name, err)
		}
		if _, _, err := store.SpaceOwner(ctx, st.Q(), "nope"); err != store.ErrNotFound {
			t.Fatal(err)
		}
	})
}

func TestDraftsLifecycle(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "admin")
		store.InsertSpace(ctx, st.Q(), "S", "s", "", "", time.Now())
		t0 := time.UnixMilli(1_000_000)
		idle, max := 2*time.Minute, 10*time.Minute
		version := ""
		put := func(rev string, at time.Time) store.Draft {
			d, err := store.PutDraft(ctx, st.Q(), store.Draft{SpaceID: "S", PageID: "P", UserID: "A", BaseRev: rev, Meta: `{"b":1,"a":2}`, Body: "x", Updated: at}, version, idle, max)
			if err != nil {
				t.Fatal(err)
			}
			version = d.Version
			return d
		}
		d := put("r1", t0)
		if !d.Due.Equal(t0.Add(idle)) {
			t.Fatalf("due %v", d.Due)
		}
		// Typing on keeps the first-change time, so the max rule counts from it.
		d = put("r1", t0.Add(9*time.Minute))
		if !d.Created.Equal(t0) || !d.Due.Equal(t0.Add(max)) {
			t.Fatalf("created %v due %v", d.Created, d.Due)
		}
		if got, _ := store.DueDrafts(ctx, st.Q(), t0.Add(max-time.Second), 10); len(got) != 0 {
			t.Fatal("due too early")
		}
		got, _ := store.DueDrafts(ctx, st.Q(), t0.Add(max), 10)
		if len(got) != 1 || got[0].Meta != `{"b":1,"a":2}` {
			t.Fatalf("%v", got)
		}
		// Held drafts are skipped; storing again releases them with a fresh count.
		store.HoldDraft(ctx, st.Q(), "S", "P", "A", d.Version)
		if got, _ := store.DueDrafts(ctx, st.Q(), t0.Add(time.Hour), 10); len(got) != 0 {
			t.Fatal("held draft is due")
		}
		later := t0.Add(2 * time.Hour)
		d = put("r1", later)
		if d.Held || !d.Created.Equal(later) {
			t.Fatalf("%+v", d)
		}
		// A different base also starts the count again.
		d = put("r2", later.Add(time.Minute))
		if !d.Created.Equal(later.Add(time.Minute)) {
			t.Fatalf("%+v", d)
		}
		// Deleting by an older version leaves a newer one.
		old := d.Version
		d = put("r2", later.Add(2*time.Minute))
		if ok, _ := store.DeleteDraftVersion(ctx, st.Q(), "S", "P", "A", old); ok {
			t.Fatal("deleted a newer version")
		}
		if _, err := store.GetDraft(ctx, st.Q(), "S", "P", "A"); err != nil {
			t.Fatal(err)
		}
		store.HoldDraft(ctx, st.Q(), "S", "P", "A", old) // an old version cannot hold the new one
		if cur, _ := store.GetDraft(ctx, st.Q(), "S", "P", "A"); cur.Held {
			t.Fatal("held by a stale version")
		}
		store.DeletePageDrafts(ctx, st.Q(), "S", "P")
		if _, err := store.GetDraft(ctx, st.Q(), "S", "P", "A"); err != store.ErrNotFound {
			t.Fatal(err)
		}
	})
}

func TestPutDraftChecksThePreviousVersion(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "admin")
		store.InsertSpace(ctx, st.Q(), "S", "s", "", "", time.Now())
		now := time.UnixMilli(5_000)
		put := func(prev, body string) (store.Draft, error) {
			return store.PutDraft(ctx, st.Q(), store.Draft{SpaceID: "S", PageID: "P", UserID: "A", BaseRev: "r", Meta: "{}", Body: body, Updated: now}, prev, time.Minute, time.Hour)
		}
		conflict := func(err error) *store.DraftConflict {
			t.Helper()
			var dc *store.DraftConflict
			if !errors.As(err, &dc) {
				t.Fatalf("not a conflict: %v", err)
			}
			return dc
		}
		// Knowing of a draft that is not there.
		if _, err := put("ghost", "x"); conflict(err).Current != nil {
			t.Fatal("a draft appeared from nowhere")
		}
		v1, err := put("", "one")
		if err != nil {
			t.Fatal(err)
		}
		// Knowing of none while there is one: the one there is comes back.
		if c := conflict(func() error { _, err := put("", "other device"); return err }()); c.Current == nil || c.Current.Version != v1.Version || c.Current.Body != "one" {
			t.Fatalf("%+v", c.Current)
		}
		if _, err := put("not-the-version", "x"); conflict(err).Current.Version != v1.Version {
			t.Fatal("wrong draft reported")
		}
		// Same millisecond, new version every time.
		v2, err := put(v1.Version, "two")
		if err != nil {
			t.Fatal(err)
		}
		v3, err := put(v2.Version, "three")
		if err != nil || v1.Version == v2.Version || v2.Version == v3.Version || v1.Version == v3.Version {
			t.Fatalf("%v %q %q %q", err, v1.Version, v2.Version, v3.Version)
		}
		if _, err := put(v1.Version, "stale"); err == nil { // the replaced version is gone for good
			t.Fatal("an old version replaced a newer draft")
		}
		if cur, _ := store.GetDraft(ctx, st.Q(), "S", "P", "A"); cur.Body != "three" {
			t.Fatalf("%q", cur.Body)
		}
		// Gone (deleted or committed) while the caller still knows of it.
		store.DeleteDraft(ctx, st.Q(), "S", "P", "A")
		if _, err := put(v3.Version, "x"); conflict(err).Current != nil {
			t.Fatal("not reported as gone")
		}
	})
}

func TestConcurrentDraftStoresHaveOneWinner(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "admin")
		store.InsertSpace(ctx, st.Q(), "S", "s", "", "", time.Now())
		for _, prev := range []string{"", "next"} {
			base := ""
			if prev != "" {
				d, err := store.PutDraft(ctx, st.Q(), store.Draft{SpaceID: "S", PageID: "Q", UserID: "A", BaseRev: "r", Meta: "{}", Body: "base", Updated: time.Now()}, "", time.Minute, time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				base = d.Version
			}
			page := "P"
			if prev != "" {
				page = "Q"
			}
			var wg sync.WaitGroup
			var mu sync.Mutex
			wins := 0
			for i := range 10 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := store.PutDraft(ctx, st.Q(), store.Draft{SpaceID: "S", PageID: page, UserID: "A", BaseRev: "r", Meta: "{}", Body: fmt.Sprint(i), Updated: time.Now()}, base, time.Minute, time.Hour)
					var dc *store.DraftConflict
					switch {
					case err == nil:
						mu.Lock()
						wins++
						mu.Unlock()
					case !errors.As(err, &dc):
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			if wins != 1 {
				t.Fatalf("prev=%q: %d stores won", base, wins)
			}
		}
	})
}

func TestRescheduleAppliesNewTimes(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "admin")
		store.InsertSpace(ctx, st.Q(), "S", "s", "", "", time.Now())
		t0 := time.UnixMilli(5_000_000)
		store.PutDraft(ctx, st.Q(), store.Draft{SpaceID: "S", PageID: "P", UserID: "A", BaseRev: "r", Meta: "{}", Body: "", Updated: t0}, "", time.Hour, 2*time.Hour)
		if err := store.RescheduleDrafts(ctx, st.Q(), time.Minute, 5*time.Minute); err != nil {
			t.Fatal(err)
		}
		d, _ := store.GetDraft(ctx, st.Q(), "S", "P", "A")
		if !d.Due.Equal(t0.Add(time.Minute)) {
			t.Fatalf("%v", d.Due)
		}
	})
}

func TestStateDatabaseSurvivesAPowerCut(t *testing.T) {
	st := storetest.Open(t, "sqlite")
	var mode int
	if err := st.Q().QueryRow(ctx, `PRAGMA synchronous`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != 2 { // FULL: an acknowledged draft is on disk
		t.Fatalf("synchronous=%d, want 2 (FULL)", mode)
	}
}

func TestDraftDueUsesTheIndex(t *testing.T) {
	st := storetest.Open(t, "sqlite")
	rows, err := st.Q().Query(ctx, `EXPLAIN QUERY PLAN SELECT space_id FROM drafts WHERE held = 0 AND due <= ? ORDER BY due LIMIT 50`, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var a, b, c int
		var d string
		rows.Scan(&a, &b, &c, &d)
		plan.WriteString(d + "\n")
	}
	if !strings.Contains(plan.String(), "drafts_due") || strings.Contains(plan.String(), "SCAN drafts") {
		t.Fatalf("the sweeper's query does not use its index:\n%s", plan.String())
	}
}

func TestRedactHidesThePassword(t *testing.T) {
	_, err := store.Open(ctx, "postgres://user:s3cretpw@127.0.0.1:1/db?connect_timeout=1")
	if err == nil {
		t.Fatal("connected to nothing")
	}
	if strings.Contains(err.Error(), "s3cretpw") {
		t.Fatalf("password in error: %v", err)
	}
}

// The two stores meet between the read of the current draft and the write, so
// only the SQL itself can tell them apart: with no lock of any kind above it.
func TestTheDraftWriteIsItselfAGuard(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "admin")
		store.InsertSpace(ctx, st.Q(), "S", "s", "", "", time.Now())
		put := func(page, prev, body string) (store.Draft, error) {
			return store.PutDraft(ctx, st.Q(), store.Draft{SpaceID: "S", PageID: page, UserID: "A", BaseRev: "r", Meta: "{}", Body: body, Updated: time.Now()}, prev, time.Minute, time.Hour)
		}
		v, err := put("U", "", "base") // a draft to replace, for the update path
		if err != nil {
			t.Fatal(err)
		}
		for name, c := range map[string]struct{ page, prev string }{"insert": {"P", ""}, "update": {"U", v.Version}} {
			var arrived sync.WaitGroup
			arrived.Add(2)
			store.SetDraftRaceHook(func() {
				arrived.Done()
				arrived.Wait() // neither writes before both have read
			})
			results := make(chan error, 2)
			for i := range 2 {
				go func() {
					_, err := put(c.page, c.prev, fmt.Sprint("writer ", i))
					results <- err
				}()
			}
			errs := []error{<-results, <-results}
			store.SetDraftRaceHook(nil)
			wins := 0
			for _, err := range errs {
				var dc *store.DraftConflict
				switch {
				case err == nil:
					wins++
				case !errors.As(err, &dc):
					t.Fatalf("%s: %v", name, err)
				}
			}
			if wins != 1 {
				t.Fatalf("%s: %d of 2 stores that read the same draft both won", name, wins)
			}
		}
	})
}

func TestHeldDraftsAreListedForRecovery(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "alice", "admin")
		mkUser(t, st, "B", "bob", "member")
		store.InsertSpace(ctx, st.Q(), "S", "s", "", "", time.Now())
		put := func(user, page, body string) store.Draft {
			d, err := store.PutDraft(ctx, st.Q(), store.Draft{SpaceID: "S", PageID: page, UserID: user, BaseRev: "r", Meta: `{"title":"T"}`, Body: body, Updated: time.UnixMilli(1000)}, "", time.Minute, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			return d
		}
		held := put("A", "P1", "alice's words")
		put("B", "P1", "bob's words")
		put("A", "P2", "not held")
		store.HoldDraft(ctx, st.Q(), "S", "P1", "A", held.Version)
		l, err := store.ListHeldDrafts(ctx, st.Q())
		if err != nil || len(l) != 1 || l[0].Username != "alice" || l[0].PageID != "P1" || l[0].Chars != len("alice's words") {
			t.Fatalf("%+v %v", l, err)
		}
		ds, names, err := store.PageDrafts(ctx, st.Q(), "S", "P1")
		if err != nil || len(ds) != 2 || len(names) != 2 || ds[0].Body == "" {
			t.Fatalf("%v %v", ds, err)
		}
	})
}

func TestSpaceInbox(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "admin")
		now := time.Now()
		for _, s := range []struct{ id, inbox string }{{"S1", ""}, {"S2", "收件箱"}} {
			if err := store.InsertSpace(ctx, st.Q(), s.id, s.id, "", s.inbox, now); err != nil {
				t.Fatal(err)
			}
			store.AddMember(ctx, st.Q(), s.id, "A", "owner", now)
		}
		inbox := func() map[string]string {
			got, err := store.ListUserSpaces(ctx, st.Q(), "A")
			if err != nil {
				t.Fatal(err)
			}
			m := map[string]string{}
			for _, s := range got {
				m[s.ID] = s.Inbox
			}
			return m
		}
		if m := inbox(); m["S1"] != store.DefaultInbox || m["S2"] != "收件箱" {
			t.Fatalf("%v", m)
		}
		if err := store.SetSpaceInbox(ctx, st.Q(), "S1", "随手记/今天"); err != nil {
			t.Fatal(err)
		}
		if m := inbox(); m["S1"] != "随手记/今天" || m["S2"] != "收件箱" {
			t.Fatalf("%v", m)
		}
		if err := store.SetSpaceInbox(ctx, st.Q(), "nope", "x"); err != store.ErrNotFound {
			t.Fatalf("%v", err)
		}
		// The space of single-user mode has the default too.
		if err := store.EnsureLocal(ctx, st.Q(), "default", "Mine", now); err != nil {
			t.Fatal(err)
		}
		got, err := store.ListUserSpaces(ctx, st.Q(), store.LocalUserID)
		if err != nil || len(got) != 1 || got[0].Inbox != store.DefaultInbox {
			t.Fatalf("%v %v", got, err)
		}
	})
}
