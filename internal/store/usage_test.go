package store_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// usageIs checks a usage row: spaces, bytes, pages (people for a tenant).
func usageIs(t *testing.T, st *store.Store, kind, id string, spaces, bytes, pages int64) store.Usage {
	t.Helper()
	u, err := store.GetUsage(ctx, st.Q(), kind, id)
	if err != nil {
		t.Fatal(err)
	}
	if u.Spaces != spaces || u.Bytes != bytes || u.Pages != pages {
		t.Fatalf("%s %s: spaces %d bytes %d pages %d, want %d %d %d", kind, id, u.Spaces, u.Bytes, u.Pages, spaces, bytes, pages)
	}
	return u
}

// consistent says the incremental counters agree with a recount from the rows:
// the recount has nothing to correct. Every scenario ends with it.
func consistent(t *testing.T, st *store.Store) {
	t.Helper()
	n, err := st.RecountUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the counters had drifted: the recount corrected %d rows", n)
	}
}

func measure(t *testing.T, st *store.Store, space string, bytes, pages int64) store.Touched {
	t.Helper()
	got, err := st.RecordMeasurement(ctx, space, store.Measurement{Bytes: bytes, Pages: pages, HavePages: true, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestUsageFollowsSpacesMembersAndMeasurements(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		now := time.Now()
		q := st.Q()
		// A person and a tenant count from the start: one person in each tenant.
		usageIs(t, st, store.UsageUser, "Ua", 0, 0, 0)
		if u := usageIs(t, st, store.UsageTenant, tenA, 1, 0, 0); u.People != 1 {
			t.Fatalf("people: %d", u.People)
		}
		// A space counts for its tenant at once, and for its first owner when it has one.
		usageIs(t, st, store.UsageTenant, tenB, 1, 0, 0)
		usageIs(t, st, store.UsageUser, "Ub", 0, 0, 0)
		if err := store.AddMember(ctx, q, "Sa", "Ua", "owner", now); err != nil {
			t.Fatal(err)
		}
		usageIs(t, st, store.UsageUser, "Ua", 1, 0, 0)
		// A measurement adds the difference to the owner and the tenant, and nobody else.
		if got := measure(t, st, "Sa", 1000, 7); got.UserID != "Ua" || got.TenantID != tenA {
			t.Fatalf("touched %+v", got)
		}
		u := usageIs(t, st, store.UsageUser, "Ua", 1, 1000, 7)
		if u.Measured.IsZero() {
			t.Fatal("measured is not set")
		}
		usageIs(t, st, store.UsageTenant, tenA, 1, 1000, 7)
		usageIs(t, st, store.UsageUser, "Ub", 0, 0, 0)
		measure(t, st, "Sa", 600, 5) // the space shrank: only the difference moves
		usageIs(t, st, store.UsageUser, "Ua", 1, 600, 5)
		usageIs(t, st, store.UsageTenant, tenA, 1, 600, 5)
		// Not the person's: a second owner, an editor, a viewer.
		for i, role := range []string{"owner", "editor", "viewer"} {
			id := fmt.Sprintf("X%d", i)
			if err := store.InsertUser(ctx, q, store.User{ID: id, Username: "x" + id, Name: id, Role: "member", Created: now, TenantID: tenA}); err != nil {
				t.Fatal(err)
			}
			if err := store.AddMember(ctx, q, "Sa", id, role, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			usageIs(t, st, store.UsageUser, id, 0, 0, 0)
		}
		usageIs(t, st, store.UsageUser, "Ua", 1, 600, 5)
		// Handing the space over moves what it holds.
		if err := st.Tx(ctx, func(q store.Q) error {
			if err := store.LockSpace(ctx, q, "Sa"); err != nil {
				return err
			}
			_, err := store.TransferOwnership(ctx, q, "Sa", "X1", now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		usageIs(t, st, store.UsageUser, "Ua", 0, 0, 0)
		usageIs(t, st, store.UsageUser, "X1", 1, 600, 5)
		usageIs(t, st, store.UsageUser, "X0", 0, 0, 0) // the owner it demoted
		usageIs(t, st, store.UsageTenant, tenA, 1, 600, 5)
		// The first owner leaving, or being demoted, passes it to the next owner.
		if err := store.SetMemberRole(ctx, q, "Sa", "X1", "editor"); err != nil {
			t.Fatal(err)
		}
		usageIs(t, st, store.UsageUser, "X1", 0, 0, 0)
		if err := store.SetMemberRole(ctx, q, "Sa", "X0", "owner"); err != nil {
			t.Fatal(err)
		}
		usageIs(t, st, store.UsageUser, "X0", 1, 600, 5)
		if err := store.RemoveMember(ctx, q, "Sa", "X0"); err != nil {
			t.Fatal(err)
		}
		usageIs(t, st, store.UsageUser, "X0", 0, 0, 0)
		usageIs(t, st, store.UsageUser, "Ua", 0, 0, 0)
		usageIs(t, st, store.UsageTenant, tenA, 1, 600, 5) // still the tenant's, owner or not
		consistent(t, st)
		// Deleting a space takes its numbers out of both rows.
		if err := store.SetMemberRole(ctx, q, "Sa", "Ua", "owner"); err != nil {
			t.Fatal(err)
		}
		usageIs(t, st, store.UsageUser, "Ua", 1, 600, 5)
		if err := st.Tx(ctx, func(q store.Q) error {
			_, err := store.DeleteSpace(ctx, q, store.AllTenants(), "Sa")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		usageIs(t, st, store.UsageUser, "Ua", 0, 0, 0)
		usageIs(t, st, store.UsageTenant, tenA, 0, 0, 0)
		consistent(t, st)
	})
}

func TestAMeasurementWithoutPagesKeepsThePageCount(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		if err := store.AddMember(ctx, st.Q(), "Sa", "Ua", "owner", time.Now()); err != nil {
			t.Fatal(err)
		}
		measure(t, st, "Sa", 100, 9)
		// The index could not be read: the bytes are new, the pages are what they were.
		if _, err := st.RecordMeasurement(ctx, "Sa", store.Measurement{Bytes: 250, At: time.Now()}); err != nil {
			t.Fatal(err)
		}
		usageIs(t, st, store.UsageUser, "Ua", 1, 250, 9)
		if _, err := st.RecordMeasurement(ctx, "Nope", store.Measurement{Bytes: 1, At: time.Now()}); err == nil {
			t.Fatal("a space that is gone was measured")
		}
		consistent(t, st)
	})
}

// A crash between two transactions, or a database edited by hand, leaves rows
// that differ from what they summarise; the recount puts every kind of
// difference right, counts what it corrected, and a second one has nothing to do.
func TestRecountCorrectsDriftOfEveryKind(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		now := time.Now()
		q := st.Q()
		if err := store.AddMember(ctx, q, "Sa", "Ua", "owner", now); err != nil {
			t.Fatal(err)
		}
		measure(t, st, "Sa", 500, 4)
		for _, stmt := range []string{
			`UPDATE usage SET bytes = bytes + 9999, pages = 0 WHERE kind = 'user' AND id = 'Ua'`,
			`UPDATE usage SET spaces = 40, people = 7 WHERE kind = 'tenant' AND id = '` + tenA + `'`,
			`DELETE FROM usage WHERE kind = 'tenant' AND id = '` + tenB + `'`,                           // a row that is missing
			`INSERT INTO usage(kind, id, tenant_id, spaces) VALUES ('user', 'Gone', '` + tenA + `', 5)`, // a person who is no more
			`UPDATE spaces SET usage_user = 'Ub' WHERE id = 'Sa'`,                                       // counted for the wrong person
		} {
			if _, err := q.Exec(ctx, stmt); err != nil {
				t.Fatal(stmt, err)
			}
		}
		n, err := st.RecountUsage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n < 5 {
			t.Fatalf("corrected only %d rows", n)
		}
		usageIs(t, st, store.UsageUser, "Ua", 1, 500, 4)
		usageIs(t, st, store.UsageUser, "Ub", 0, 0, 0)
		if u := usageIs(t, st, store.UsageTenant, tenA, 1, 500, 4); u.People != 1 {
			t.Fatalf("people %d", u.People)
		}
		if u := usageIs(t, st, store.UsageTenant, tenB, 1, 0, 0); u.People != 1 {
			t.Fatalf("people %d", u.People)
		}
		var gone int
		if err := q.QueryRow(ctx, `SELECT COUNT(*) FROM usage WHERE id = 'Gone'`).Scan(&gone); err != nil || gone != 0 {
			t.Fatalf("%d %v", gone, err)
		}
		consistent(t, st)
	})
}

// Measuring again is what corrects a space's own numbers: a re-measure finds the
// space as it is now and adds the difference to what the rows hold, so a
// measurement that was lost, or a size that changed while the server was down,
// never leaves the totals off by more than one pass.
func TestAMeasurementCorrectsWhatTheRowsHold(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		q := st.Q()
		if err := store.AddMember(ctx, q, "Sa", "Ua", "owner", time.Now()); err != nil {
			t.Fatal(err)
		}
		measure(t, st, "Sa", 1000, 10)
		// The space's own columns were written, but the transaction that adds to the
		// rows never ran (a crash).
		if _, err := q.Exec(ctx, `UPDATE spaces SET usage_bytes = 4000, page_count = 40 WHERE id = 'Sa'`); err != nil {
			t.Fatal(err)
		}
		usageIs(t, st, store.UsageUser, "Ua", 1, 1000, 10)
		n, err := st.RecountUsage(ctx)
		if err != nil || n == 0 {
			t.Fatalf("%d %v", n, err)
		}
		usageIs(t, st, store.UsageUser, "Ua", 1, 4000, 40)
		usageIs(t, st, store.UsageTenant, tenA, 1, 4000, 40)
		// The truth on disk is 1200 bytes and 12 pages: the next measurement fixes it.
		measure(t, st, "Sa", 1200, 12)
		usageIs(t, st, store.UsageUser, "Ua", 1, 1200, 12)
		usageIs(t, st, store.UsageTenant, tenA, 1, 1200, 12)
		consistent(t, st)
	})
}

// People and spaces made at the same time, measurements, hand-overs and deletes
// racing each other leave the rows exactly as a recount from scratch finds them.
// On PostgreSQL the transactions really run together; on SQLite they take turns.
func TestConcurrentChangesKeepTheCountersExact(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		q := st.Q()
		const people, perPerson = 6, 4
		for p := 0; p < people; p++ {
			id := fmt.Sprintf("P%d", p)
			if err := store.InsertUser(ctx, q, store.User{ID: id, Username: id, Name: id, Role: "member", Created: now, TenantID: tenA}); err != nil {
				t.Fatal(err)
			}
		}
		var wg sync.WaitGroup
		errs := make(chan error, 200)
		for p := 0; p < people; p++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				owner := fmt.Sprintf("P%d", p)
				for i := 0; i < perPerson; i++ {
					sid := fmt.Sprintf("S%d-%d", p, i)
					if err := st.Tx(ctx, func(q store.Q) error {
						if err := store.InsertSpace(ctx, q, tenA, sid, sid, "", "", now); err != nil {
							return err
						}
						return store.AddMember(ctx, q, sid, owner, "owner", now)
					}); err != nil {
						errs <- err
						return
					}
					if _, err := st.RecordMeasurement(ctx, sid, store.Measurement{Bytes: int64(100 * (i + 1)), Pages: int64(i + 1), HavePages: true, At: now}); err != nil {
						errs <- err
						return
					}
					// Half of them are handed to the next person, one is deleted, one is measured again.
					switch i {
					case 1:
						next := fmt.Sprintf("P%d", (p+1)%people)
						if err := st.Tx(ctx, func(q store.Q) error {
							if err := store.LockSpace(ctx, q, sid); err != nil {
								return err
							}
							_, err := store.TransferOwnership(ctx, q, sid, next, now)
							return err
						}); err != nil {
							errs <- err
						}
					case 2:
						if err := st.Tx(ctx, func(q store.Q) error {
							_, err := store.DeleteSpace(ctx, q, store.AllTenants(), sid)
							return err
						}); err != nil {
							errs <- err
						}
					case 3:
						if _, err := st.RecordMeasurement(ctx, sid, store.Measurement{Bytes: 7, Pages: 1, HavePages: true, At: now}); err != nil {
							errs <- err
						}
					}
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		// Each person made 4, one was deleted, and one was handed on (and taken in the
		// round): three spaces each, whoever holds them.
		var total int64
		for p := 0; p < people; p++ {
			total += mustUsage(t, st, store.UsageUser, fmt.Sprintf("P%d", p)).Spaces
		}
		if total != people*(perPerson-1) {
			t.Fatalf("%d spaces counted for people, want %d", total, people*(perPerson-1))
		}
		if u := mustUsage(t, st, store.UsageTenant, tenA); u.Spaces != total {
			t.Fatalf("the tenant has %d, the people %d", u.Spaces, total)
		}
		consistent(t, st)
	})
}

func mustUsage(t *testing.T, st *store.Store, kind, id string) store.Usage {
	t.Helper()
	u, err := store.GetUsage(ctx, st.Q(), kind, id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUsageListIsByBytesWithACursorAndAScope(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		now := time.Now()
		q := st.Q()
		for i, b := range []int64{300, 100, 300, 0, 50} {
			id := fmt.Sprintf("L%d", i)
			if err := store.InsertUser(ctx, q, store.User{ID: id, Username: id, Name: "Person " + id, Role: "member", Created: now, TenantID: tenA}); err != nil {
				t.Fatal(err)
			}
			sid := "SL" + id
			if err := store.InsertSpace(ctx, q, tenA, sid, sid, "", "", now); err != nil {
				t.Fatal(err)
			}
			if err := store.AddMember(ctx, q, sid, id, "owner", now); err != nil {
				t.Fatal(err)
			}
			measure(t, st, sid, b, 1)
		}
		var ids []string
		after := ""
		for {
			rows, next, err := store.ListUserUsage(ctx, q, store.InTenant(tenA), store.UsageFilter{After: after, Limit: 2})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				ids = append(ids, r.ID)
			}
			if next == "" {
				break
			}
			after = next
		}
		// 300 (L0, L2 by id), 100, 50, then the ones with nothing, by id (L3 and Ua).
		want := []string{"L0", "L2", "L1", "L4", "L3", "Ua"}
		if fmt.Sprint(ids) != fmt.Sprint(want) {
			t.Fatalf("%v, want %v", ids, want)
		}
		// Another tenant's people are not in the list; the site's scope sees both.
		rows, _, err := store.ListUserUsage(ctx, q, store.InTenant(tenB), store.UsageFilter{})
		if err != nil || len(rows) != 1 || rows[0].ID != "Ub" || rows[0].Username != "user-b" {
			t.Fatalf("%v %v", rows, err)
		}
		if rows, _, _ = store.ListUserUsage(ctx, q, store.AllTenants(), store.UsageFilter{}); len(rows) != 7 {
			t.Fatalf("%d", len(rows))
		}
		// A person marked over a limit is what over=1 keeps.
		if _, err := store.SetUsageOver(ctx, q, store.UsageUser, "L1", []string{"bytes"}); err != nil {
			t.Fatal(err)
		}
		rows, _, _ = store.ListUserUsage(ctx, q, store.AllTenants(), store.UsageFilter{OverOnly: true})
		if len(rows) != 1 || rows[0].ID != "L1" || fmt.Sprint(rows[0].Over) != "[bytes]" {
			t.Fatalf("%v", rows)
		}
		if _, _, err := store.ListUserUsage(ctx, q, store.AllTenants(), store.UsageFilter{After: "garbage"}); err == nil {
			t.Fatal("a bad cursor was taken")
		}
		// Setting the same set again changes nothing, which is how a crossing is noticed once.
		if changed, _ := store.SetUsageOver(ctx, q, store.UsageUser, "L1", []string{"bytes"}); changed {
			t.Fatal("the same set was a change")
		}
	})
}

// Deleting an empty tenant takes its usage row with it.
func TestDeletingATenantDeletesItsUsage(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		if err := st.CreateTenant(ctx, store.Tenant{ID: "T000000009", Slug: "tmp", Name: "Tmp", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertSpace(ctx, st.Q(), "T000000009", "Stmp", "Tmp space", "", "", now); err != nil {
			t.Fatal(err)
		}
		usageIs(t, st, store.UsageTenant, "T000000009", 1, 0, 0)
		if err := st.Tx(ctx, func(q store.Q) error {
			_, err := store.DeleteSpace(ctx, q, store.AllTenants(), "Stmp")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteTenant(ctx, "T000000009"); err != nil {
			t.Fatal(err)
		}
		var left int
		if err := st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM usage WHERE tenant_id = 'T000000009'`).Scan(&left); err != nil || left != 0 {
			t.Fatalf("%d rows left, %v", left, err)
		}
		consistent(t, st)
	})
}

// A recount that runs while people make, measure and hand over spaces, and new
// people arrive, must not undo what they wrote: when they are all done, a recount
// from scratch finds nothing to correct. On PostgreSQL the recount's reads are
// several statements, so without a lock a write committed between them would be
// counted twice or undone; the test repeats the race to make that likely.
func TestARecountDuringWritesLeavesTheCountersExact(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		for round := 0; round < 8; round++ {
			const writers, perWriter = 8, 6
			var wg sync.WaitGroup
			errs := make(chan error, 1000)
			stop := make(chan struct{})
			recounts := make(chan int, 1)
			go func() {
				n := 0
				defer func() { recounts <- n }()
				for {
					select {
					case <-stop:
						return
					default:
					}
					if _, err := st.RecountUsage(ctx); err != nil {
						errs <- err
						return
					}
					n++
				}
			}()
			for w := 0; w < writers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					owner := fmt.Sprintf("R%d-%d", round, w)
					next := owner + "b"
					for _, id := range []string{owner, next} {
						if err := store.InsertUser(ctx, st.Q(), store.User{ID: id, Username: id, Name: id, Role: "member", Created: now, TenantID: tenA}); err != nil {
							errs <- err
							return
						}
					}
					for i := 0; i < perWriter; i++ {
						sid := fmt.Sprintf("%s-S%d", owner, i)
						if err := st.Tx(ctx, func(q store.Q) error {
							if err := store.InsertSpace(ctx, q, tenA, sid, sid, "", "", now); err != nil {
								return err
							}
							return store.AddMember(ctx, q, sid, owner, "owner", now)
						}); err != nil {
							errs <- err
							return
						}
						if _, err := st.RecordMeasurement(ctx, sid, store.Measurement{Bytes: int64(100 * (i + 1)), Pages: int64(i + 1), HavePages: true, At: now}); err != nil {
							errs <- err
							return
						}
						if i%3 == 1 {
							if err := st.Tx(ctx, func(q store.Q) error {
								if err := store.LockSpace(ctx, q, sid); err != nil {
									return err
								}
								_, err := store.TransferOwnership(ctx, q, sid, next, now)
								return err
							}); err != nil {
								errs <- err
							}
						}
					}
				}()
			}
			wg.Wait()
			close(stop)
			n := <-recounts
			close(errs)
			for err := range errs {
				t.Fatal(err)
			}
			if n == 0 {
				t.Fatal("no recount ran during the writes")
			}
			c, err := st.RecountUsage(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if c != 0 {
				t.Fatalf("round %d: after %d recounts during the writes, a recount from scratch corrected %d rows", round, n, c)
			}
		}
	})
}
