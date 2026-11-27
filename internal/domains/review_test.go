package domains_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"petalbook.app/server/internal/domains"
	"petalbook.app/server/internal/domains/domainstest"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// A label that mixes scripts (a Cyrillic а in a Latin name) is what a lookalike
// is made of; the scripts that are written together (Japanese, Korean, Chinese
// with Latin) are not.
func TestMixedScriptLabelsAreFound(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]bool{
		"example.com":      false,
		"bücher.example":   false,
		"пример.рф":        false,
		"аpple.com":        true, // Cyrillic а, Latin pple
		"pаypal.com":       true,
		"ελλάδα.gr":        false,
		"gοogle.com":       true, // Greek ο
		"例え.テスト":           false,
		"日本語とカタカナ.jp":      false,
		"ab日本.jp":          false,
		"한국어.kr":           false,
		"中文.cn":            false,
		"a-1.example":      false,
		"xn--pple-43d.com": true, // the punycode of аpple
	} {
		key, ok := domains.Key(in)
		if !ok {
			t.Fatalf("Key(%q)", in)
		}
		if got := domains.MixedScript(key); got != want {
			t.Errorf("MixedScript(%q) = %v, want %v", in, got, want)
		}
	}
}

// Two servers that found both records verify at the same moment: the write
// (one conditional statement and the unique index) leaves exactly one winner,
// the other told ErrDomainClaimed.
func TestTwoVerificationsOfOneDomainRaceToOneWinner(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		ctx := context.Background()
		now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
		if err := st.CreateTenant(ctx, store.Tenant{ID: "T000000002", Slug: "acme", Name: "Acme", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		tenants := []string{store.DefaultTenantID, "T000000002"}
		for i := range 20 {
			domain := fmt.Sprintf("race%d.example", i)
			for _, tn := range tenants {
				if err := store.InsertDomain(ctx, st.Q(), store.DomainRow{TenantID: tn, Domain: domain, CodeHash: "h", CodeExpires: now.Add(time.Hour), Created: now, Updated: now}); err != nil {
					t.Fatal(err)
				}
			}
			start := make(chan struct{})
			var wg sync.WaitGroup
			oks := make([]bool, 2)
			errs := make([]error, 2)
			for j, tn := range tenants {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					oks[j], errs[j] = store.VerifyDomain(ctx, st.Q(), tn, domain, "", now, now.Add(24*time.Hour))
				}()
			}
			close(start)
			wg.Wait()
			won, lost := 0, 0
			for j := range tenants {
				switch {
				case errs[j] == nil && oks[j]:
					won++
				case errors.Is(errs[j], store.ErrDomainClaimed):
					lost++
				default:
					t.Fatalf("%s: %v %v", domain, oks[j], errs[j])
				}
			}
			if won != 1 || lost != 1 {
				t.Fatalf("%s: %d won, %d lost", domain, won, lost)
			}
		}
	})
}

// The index itself refuses a second verified holder, whatever writes it.
func TestASecondVerifiedHolderIsRefusedByTheIndex(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		ctx := context.Background()
		if err := st.CreateTenant(ctx, store.Tenant{ID: "T000000002", Slug: "acme", Name: "Acme", Created: time.Now(), Updated: time.Now()}); err != nil {
			t.Fatal(err)
		}
		ins := `INSERT INTO tenant_domains(tenant_id, domain, verified_at, created, updated) VALUES (?, 'one.example', 1, 0, 0)`
		if _, err := st.Q().Exec(ctx, ins, store.DefaultTenantID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Q().Exec(ctx, ins, "T000000002"); !store.IsUnique(err) {
			t.Fatalf("a second verified row: %v", err)
		}
	})
}

// PostgreSQL, step by step: A verifies inside a transaction it has not
// committed; B's verification is held by the index until A commits, and then
// told ErrDomainClaimed. (SQLite has one writer at a time: there B waits for
// the lock instead, which TestTwoVerificationsOfOneDomainRaceToOneWinner
// covers by its outcome.)
func TestAVerificationHeldByAnotherIsToldClaimedWhenItCommits(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		if st.Dialect() != store.Postgres {
			t.Skip("PostgreSQL only: SQLite has one writer at a time")
		}
		ctx := context.Background()
		now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
		if err := st.CreateTenant(ctx, store.Tenant{ID: "T000000002", Slug: "acme", Name: "Acme", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		for _, tn := range []string{store.DefaultTenantID, "T000000002"} {
			if err := store.InsertDomain(ctx, st.Q(), store.DomainRow{TenantID: tn, Domain: "held.example", CodeHash: "h", CodeExpires: now.Add(time.Hour), Created: now, Updated: now}); err != nil {
				t.Fatal(err)
			}
		}
		done := make(chan error, 1)
		err := st.Tx(ctx, func(q store.Q) error {
			if ok, err := store.VerifyDomain(ctx, q, store.DefaultTenantID, "held.example", "", now, now.Add(time.Hour)); err != nil || !ok {
				t.Fatalf("A: %v %v", ok, err)
			}
			go func() {
				_, err := store.VerifyDomain(ctx, st.Q(), "T000000002", "held.example", "", now, now.Add(time.Hour))
				done <- err
			}()
			// B is waiting on A's row of the index before A commits.
			deadline := time.Now().Add(10 * time.Second)
			for {
				var n int
				if err := st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE 'UPDATE tenant_domains%'`).Scan(&n); err != nil {
					return err
				}
				if n > 0 {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("B finished before A committed: %v", err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("B never waited on A")
				}
				time.Sleep(5 * time.Millisecond)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, store.ErrDomainClaimed) {
			t.Fatalf("B: %v", err)
		}
	})
}

func countAudit(t *testing.T, st *store.Store, kind string) int {
	t.Helper()
	var n int
	if err := st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM audit_log WHERE kind = ?`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The lapse, its notice and its audit row are one transaction: when any of it
// fails, the claim is still verified and nothing was told.
func TestALapseIsOneTransaction(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		ctx := context.Background()
		clk := &fakeClock{t: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)}
		dns := domainstest.New()
		verified(t, st, dns, store.DefaultTenantID, "gone.example", clk.Now(), clk.Now().Add(time.Hour))
		dns.SetTXT("_petalbook.gone.example")
		svc := domains.New(domains.Options{Store: st, Resolver: dns, Now: clk.Now})
		for range 8 {
			clk.Advance(24 * time.Hour)
			svc.RecheckDue(ctx)
			r, _ := store.GetDomain(ctx, st.Q(), store.DefaultTenantID, "gone.example")
			if !r.Verified() {
				t.Fatal("lapsed early")
			}
			if !r.MissingSince.IsZero() && clk.Now().Sub(r.MissingSince) >= 6*24*time.Hour {
				break // the next recheck lapses it
			}
		}
		svc.SetLapseFault(func() error { return errors.New("the notice could not be written") })
		clk.Advance(24 * time.Hour)
		svc.RecheckDue(ctx)
		if r, err := store.GetDomain(ctx, st.Q(), store.DefaultTenantID, "gone.example"); err != nil || !r.Verified() || !r.LapsedAt.IsZero() {
			t.Fatalf("a lapse whose notice failed was kept: %+v %v", r, err)
		}
		if n := countAudit(t, st, "domain_lapsed"); n != 0 {
			t.Fatalf("audited without the lapse: %d", n)
		}
		svc.SetLapseFault(nil)
		clk.Advance(2 * time.Hour) // past the retry the failure set
		svc.RecheckDue(ctx)
		if r, _ := store.GetDomain(ctx, st.Q(), store.DefaultTenantID, "gone.example"); r.Verified() || r.LapsedAt.IsZero() {
			t.Fatalf("not lapsed: %+v", r)
		}
		if n := countAudit(t, st, "domain_lapsed"); n != 1 {
			t.Fatalf("domain_lapsed: %d", n)
		}
	})
}

// A recheck that cannot read or write the database does not spin: it waits,
// longer each time up to a bound, and goes back to the schedule once it works.
func TestTheRecheckBacksOffWhileTheDatabaseFails(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		clk := &fakeClock{t: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)}
		dns := domainstest.New()
		verified(t, st, dns, store.DefaultTenantID, "due.example", clk.Now(), clk.Now().Add(-time.Minute))
		tm := &fakeTimer{asked: make(chan time.Duration), fire: make(chan time.Time)}
		svc := domains.New(domains.Options{Store: st, Resolver: dns, Now: clk.Now, After: tm.After})
		var failing atomic.Bool
		failing.Store(true)
		svc.SetDueFault(func() error {
			if failing.Load() {
				return errors.New("database is gone")
			}
			return nil
		})
		done := make(chan struct{})
		go func() { svc.Run(ctx); close(done) }()
		var waits []time.Duration
		for range 12 {
			d := <-tm.asked
			waits = append(waits, d)
			clk.Advance(d)
			tm.fire <- clk.Now()
		}
		for i, d := range waits {
			if d <= 0 || d > domains.MaxBackoff {
				t.Fatalf("wait %d is %v: %v", i, d, waits)
			}
			if i > 0 && d < waits[i-1] {
				t.Fatalf("the waits do not grow: %v", waits)
			}
		}
		if waits[len(waits)-1] != domains.MaxBackoff {
			t.Fatalf("the waits are not bounded at %v: %v", domains.MaxBackoff, waits)
		}
		// It works again: the due domain is rechecked, and the next sleep is the schedule's.
		failing.Store(false)
		<-tm.asked
		tm.fire <- clk.Now()
		if d := <-tm.asked; d != 24*time.Hour {
			t.Fatalf("back on the schedule: %v", d)
		}
		if dns.Asked("_petalbook.due.example") != 1 {
			t.Fatal("the due domain was not rechecked once")
		}
		cancel()
		<-done
	})
}
