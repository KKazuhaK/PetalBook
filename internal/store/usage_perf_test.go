package store_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// What the usage table costs at the size of a large server. It prints the
// timings (the design notes quote them): set PETALBOOK_PERF=1 to run it, on
// SQLite and, with scripts/test-postgres.sh, on PostgreSQL.
func TestUsageCosts(t *testing.T) {
	if os.Getenv("PETALBOOK_PERF") == "" {
		t.Skip("set PETALBOOK_PERF=1 to measure")
	}
	const people, spacesN = 20000, 40000
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		q := st.Q()
		// People and spaces by plain statements in one transaction: the rows of a server that grew for years.
		if err := st.Tx(ctx, func(q store.Q) error {
			for i := 0; i < people; i++ {
				id := fmt.Sprintf("U%07d", i)
				if _, err := q.Exec(ctx, `INSERT INTO users(id, username, name, email, role, disabled, created, email_verified, passkey_required, tenant_id, site_admin) VALUES (?, ?, ?, '', 'member', 0, ?, 0, 0, ?, 0)`,
					id, id, id, now.UnixMilli(), store.DefaultTenantID); err != nil {
					return err
				}
			}
			for i := 0; i < spacesN; i++ {
				sid := fmt.Sprintf("S%07d", i)
				if _, err := q.Exec(ctx, `INSERT INTO spaces(id, name, name_key, created, tenant_id, usage_bytes, page_count) VALUES (?, ?, ?, ?, ?, ?, ?)`,
					sid, sid, sid, now.UnixMilli(), store.DefaultTenantID, 1_000_000+i, 100+i%50); err != nil {
					return err
				}
				if _, err := q.Exec(ctx, `INSERT INTO space_members(space_id, user_id, role, created) VALUES (?, ?, 'owner', ?)`, sid, fmt.Sprintf("U%07d", i%people), now.UnixMilli()); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		time1 := func(name string, f func()) time.Duration {
			start := time.Now()
			f()
			d := time.Since(start)
			t.Logf("PERF %-58s %10s", name, d.Round(time.Microsecond))
			return d
		}
		var corrected int
		if _, err := q.Exec(ctx, `DELETE FROM usage`); err != nil { // the one row the empty database made
			t.Fatal(err)
		}
		time1(fmt.Sprintf("the migration's step: %d people, %d spaces", people, spacesN), func() {
			if err := st.Tx(ctx, func(q store.Q) error { return store.SeedUsage(ctx, q) }); err != nil {
				t.Fatal(err)
			}
		})
		consistent(t, st) // what the migration made is what a recount finds
		if _, err := q.Exec(ctx, `DELETE FROM usage`); err != nil {
			t.Fatal(err)
		}
		time1("first recount from nothing (every row made by an upsert)", func() { corrected, _ = st.RecountUsage(ctx) })
		t.Logf("PERF   corrected %d rows", corrected)
		time1("daily recount with nothing to correct", func() { corrected, _ = st.RecountUsage(ctx) })
		if corrected != 0 {
			t.Fatalf("%d", corrected)
		}
		// 1,000 rows drifted.
		if _, err := q.Exec(ctx, `UPDATE usage SET bytes = bytes + 7 WHERE kind = 'user' AND id < 'U0001000'`); err != nil {
			t.Fatal(err)
		}
		time1("daily recount correcting 1,000 rows", func() { corrected, _ = st.RecountUsage(ctx) })
		t.Logf("PERF   corrected %d rows", corrected)

		avg := func(name string, n int, f func(i int)) {
			start := time.Now()
			for i := 0; i < n; i++ {
				f(i)
			}
			t.Logf("PERF %-58s %10s each (%d runs)", name, (time.Since(start) / time.Duration(n)).Round(time.Microsecond), n)
		}
		avg("read a person's usage (GetUsage, the space-count check)", 2000, func(i int) {
			if _, err := store.GetUsage(ctx, q, store.UsageUser, fmt.Sprintf("U%07d", i*7%people)); err != nil {
				t.Fatal(err)
			}
		})
		avg("one page of 50 people by bytes (site, all tenants)", 200, func(i int) {
			if _, _, err := store.ListUserUsage(ctx, q, store.AllTenants(), store.UsageFilter{Limit: 50}); err != nil {
				t.Fatal(err)
			}
		})
		_, deep, err := store.ListUserUsage(ctx, q, store.AllTenants(), store.UsageFilter{Limit: 10000})
		if err != nil || deep == "" {
			t.Fatal(err)
		}
		avg("the page after the first 10,000 people (cursor deep in the list)", 50, func(i int) {
			rows, _, err := store.ListUserUsage(ctx, q, store.AllTenants(), store.UsageFilter{Limit: 50, After: deep})
			if err != nil || len(rows) != 50 {
				t.Fatal(err, len(rows))
			}
		})
		avg("one page of 50 people of one tenant", 200, func(i int) {
			if _, _, err := store.ListUserUsage(ctx, q, store.InTenant(store.DefaultTenantID), store.UsageFilter{Limit: 50}); err != nil {
				t.Fatal(err)
			}
		})
		avg("record one measurement (RecordMeasurement)", 500, func(i int) {
			if _, err := st.RecordMeasurement(ctx, fmt.Sprintf("S%07d", i*13%spacesN), store.Measurement{Bytes: int64(2_000_000 + i), Pages: 120, HavePages: true, At: time.Now()}); err != nil {
				t.Fatal(err)
			}
		})
		avg("make a space with its owner, usage kept (InsertSpace + AddMember)", 500, func(i int) {
			sid := fmt.Sprintf("N%07d", i)
			if err := st.Tx(ctx, func(q store.Q) error {
				if err := store.InsertSpace(ctx, q, store.DefaultTenantID, sid, sid, "", "", now); err != nil {
					return err
				}
				return store.AddMember(ctx, q, sid, fmt.Sprintf("U%07d", i), "owner", now)
			}); err != nil {
				t.Fatal(err)
			}
		})
		avg("hand a space over (TransferOwnership, usage moved)", 200, func(i int) {
			sid := fmt.Sprintf("S%07d", i)
			if err := st.Tx(ctx, func(q store.Q) error {
				if err := store.LockSpace(ctx, q, sid); err != nil {
					return err
				}
				_, err := store.TransferOwnership(ctx, q, sid, fmt.Sprintf("U%07d", (i+5)%people), now)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
		consistent(t, st)
	})
}
