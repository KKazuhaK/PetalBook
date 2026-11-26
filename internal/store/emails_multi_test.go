package store_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// Several addresses of one account (accounts.multipleEmails): every address is
// unique on the server, and the rule of who holds it applies to each.
func TestEverySecondAddressIsUniqueAndHeldByTheSameRule(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.Now()
		addPerson(t, q, "A", store.DefaultTenantID, "a@example.test", true)
		addPerson(t, q, "B", store.DefaultTenantID, "b@example.test", true)
		// A second address of A's is B's no more than A's primary is.
		if _, err := store.AddEmail(ctx, q, "A", "extra@example.test", false, "", false, now); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AddEmail(ctx, q, "B", "EXTRA@example.test", false, "", false, now); !errors.Is(err, store.ErrEmailTaken) {
			t.Fatalf("a second address held unverified was added twice: %v", err)
		}
		if _, err := store.AddEmail(ctx, q, "B", "a@example.test", true, "", true, now); !errors.Is(err, store.ErrEmailTaken) {
			t.Fatalf("a verified primary was taken as a second address: %v", err)
		}
		// Proved, an address held unverified as a second one moves.
		r, err := store.AddEmail(ctx, q, "B", "extra@example.test", true, "", true, now)
		if err != nil || r == nil || r.UserID != "A" {
			t.Fatalf("%+v %v", r, err)
		}
		// Held verified as a second address: never displaced.
		if _, err := store.AddEmail(ctx, q, "A", "extra@example.test", true, "", true, now); !errors.Is(err, store.ErrEmailTaken) {
			t.Fatalf("a verified second address moved: %v", err)
		}
		if l, err := store.UserEmails(ctx, q, "A"); err != nil || len(l) != 1 {
			t.Fatalf("%+v %v", l, err)
		}
		assertCopies(t, st)
	})
}

// The primary moves only to an address the account holds verified (a tenant
// administrator's mark alone does not count); the old primary stays as another
// address, and the copy on users follows.
func TestMakePrimaryNeedsAnAddressTheAccountHolds(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		addPerson(t, q, "A", store.DefaultTenantID, "a@example.test", true)
		if _, err := store.AddEmail(ctx, q, "A", "second@example.test", false, "", false, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := store.MakePrimaryEmail(ctx, q, "A", "second@example.test"); !errors.Is(err, store.ErrEmailUnverified) {
			t.Fatalf("an unverified address became primary: %v", err)
		}
		if err := store.SetEmailRowVerified(ctx, q, "A", "second@example.test", true, store.VerifiedByTenantAdmin); err != nil {
			t.Fatal(err)
		}
		if err := store.MakePrimaryEmail(ctx, q, "A", "second@example.test"); !errors.Is(err, store.ErrEmailUnverified) {
			t.Fatalf("an address only a tenant administrator marked became primary: %v", err)
		}
		if err := store.MakePrimaryEmail(ctx, q, "A", "nobody@example.test"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("another account's address: %v", err)
		}
		if err := store.SetEmailRowVerified(ctx, q, "A", "second@example.test", true, ""); err != nil {
			t.Fatal(err)
		}
		if err := store.MakePrimaryEmail(ctx, q, "A", "second@example.test"); err != nil {
			t.Fatal(err)
		}
		l, err := store.UserEmails(ctx, q, "A")
		if err != nil || len(l) != 2 || l[0].Email != "second@example.test" || !l[0].Primary || l[1].Email != "a@example.test" || l[1].Primary || !l[1].Verified {
			t.Fatalf("%+v %v", l, err)
		}
		// Again is nothing.
		if err := store.MakePrimaryEmail(ctx, q, "A", "second@example.test"); err != nil {
			t.Fatal(err)
		}
		if err := store.RemoveEmail(ctx, q, "A", "second@example.test"); !errors.Is(err, store.ErrPrimaryEmail) {
			t.Fatalf("the primary was removed: %v", err)
		}
		assertCopies(t, st)
	})
}

// Two accounts that both prove one address at once: one has it, the other is
// told it is taken, and its transaction stays usable (the savepoint).
func TestTwoAccountsProvingOneAddressAtOnce(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		addPerson(t, q, "ONE", store.DefaultTenantID, "one@example.test", true)
		addPerson(t, q, "TWO", store.DefaultTenantID, "two@example.test", true)
		// The other writer: another connection on PostgreSQL; SQLite has one
		// writer, so there it is the transaction itself.
		other := st.Dialect() == store.Postgres
		store.SetEmailInsertRaceHook(func(q store.Q) {
			store.SetEmailInsertRaceHook(nil)
			if other {
				q = st.Q()
			}
			if _, err := store.AddEmail(ctx, q, "ONE", "both@example.test", true, "", true, time.Now()); err != nil {
				t.Error(err)
			}
		})
		t.Cleanup(func() { store.SetEmailInsertRaceHook(nil) })
		err := st.Tx(ctx, func(q store.Q) error {
			if _, err := store.AddEmail(ctx, q, "TWO", "both@example.test", true, "", true, time.Now()); !errors.Is(err, store.ErrEmailTaken) {
				t.Errorf("the second: %v", err)
			}
			_, err := q.Exec(ctx, `UPDATE users SET name = 'went on' WHERE id = 'TWO'`)
			return err
		})
		if err != nil {
			t.Fatalf("the transaction: %v", err)
		}
		if row, err := store.EmailOwner(ctx, q, "both@example.test"); other && (err != nil || row.UserID != "ONE" || !row.Verified) {
			t.Fatalf("%+v %v", row, err)
		}
		assertCopies(t, st)
	})
}

// Many accounts adding one free address at the same moment: exactly one has a
// row; the others are refused, none half-written.
func TestManyAccountsAddingOneAddressOneWins(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		const n = 6
		ids := []string{"P0", "P1", "P2", "P3", "P4", "P5"}
		for _, id := range ids {
			addPerson(t, q, id, store.DefaultTenantID, "", false)
		}
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = store.AddEmail(ctx, st.Q(), id, "shared@example.test", false, "", false, time.Now())
			}()
		}
		wg.Wait()
		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, store.ErrEmailTaken):
				t.Fatalf("unexpected: %v", err)
			}
		}
		if won != 1 || count(t, st, `SELECT COUNT(*) FROM user_emails WHERE email_key = 'shared@example.test'`) != 1 {
			t.Fatalf("%d won: %v", won, errs)
		}
	})
}

// A verification link is per address: asking for one address's link leaves
// the link of another address working; removing an address drops its links.
func TestVerifyLinksArePerAddress(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		addPerson(t, q, "A", store.DefaultTenantID, "a@example.test", false)
		now := time.Now()
		tok := func(hash, email string) store.EmailToken {
			return store.EmailToken{Hash: hash, Kind: store.TokenVerify, UserID: "A", Email: email, Created: now, Expires: now.Add(time.Hour)}
		}
		for _, x := range []store.EmailToken{tok("h1", "a@example.test"), tok("h2", "b@example.test"), tok("h3", "b@example.test")} {
			if err := st.Tx(ctx, func(q store.Q) error { return store.ReplaceEmailToken(ctx, q, x) }); err != nil {
				t.Fatal(err)
			}
		}
		live := func(h string) bool {
			_, err := store.FindEmailToken(ctx, q, store.TokenVerify, h, now)
			return err == nil
		}
		if !live("h1") || live("h2") || !live("h3") {
			t.Fatalf("h1 %v h2 %v h3 %v", live("h1"), live("h2"), live("h3"))
		}
		if err := store.DeleteEmailTokensFor(ctx, q, "A", "b@example.test"); err != nil {
			t.Fatal(err)
		}
		if !live("h1") || live("h3") {
			t.Fatal("removing an address's links touched another's, or kept its own")
		}
	})
}

// RemoveEmail never deletes a primary address, whatever happened since it
// looked, and says which refusal it is.
func TestRemoveEmailKeepsThePrimary(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		addPerson(t, q, "A", store.DefaultTenantID, "a@example.test", true)
		if _, err := store.AddEmail(ctx, q, "A", "second@example.test", true, "", false, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := store.RemoveEmail(ctx, q, "A", "a@example.test"); !errors.Is(err, store.ErrPrimaryEmail) {
			t.Fatalf("the primary: %v", err)
		}
		if err := store.RemoveEmail(ctx, q, "A", "nobody@example.test"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("an unknown address: %v", err)
		}
		// The other address becomes primary: the same call now refuses it, and
		// the old primary goes.
		if err := store.MakePrimaryEmail(ctx, q, "A", "second@example.test"); err != nil {
			t.Fatal(err)
		}
		if err := store.RemoveEmail(ctx, q, "A", "second@example.test"); !errors.Is(err, store.ErrPrimaryEmail) {
			t.Fatalf("the new primary: %v", err)
		}
		if err := store.RemoveEmail(ctx, q, "A", "a@example.test"); err != nil {
			t.Fatal(err)
		}
		if l, err := store.UserEmails(ctx, q, "A"); err != nil || len(l) != 1 || !l[0].Primary {
			t.Fatalf("%+v %v", l, err)
		}
		assertCopies(t, st)
	})
}

// Two makes-primary at once: the second one's promotion meets the unique
// index on the primary and is ErrPrimaryRace, not a bare database error; the
// account keeps one primary.
func TestMakePrimaryRaceIsAConflict(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		addPerson(t, q, "A", store.DefaultTenantID, "a@example.test", true)
		for _, e := range []string{"x@example.test", "y@example.test"} {
			if _, err := store.AddEmail(ctx, q, "A", e, true, "", false, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		store.SetPrimaryRaceHook(func(q store.Q) {
			store.SetPrimaryRaceHook(nil)
			// The other change's promotion, as the unique index meets it. (Its
			// own connection could not do this on PostgreSQL: the index waits
			// for the open transaction that demoted the old primary.)
			if _, err := q.Exec(ctx, `UPDATE user_emails SET is_primary = 1 WHERE user_id = 'A' AND email = 'y@example.test'`); err != nil {
				t.Error(err)
			}
		})
		t.Cleanup(func() { store.SetPrimaryRaceHook(nil) })
		if err := store.MakePrimaryEmail(ctx, q, "A", "x@example.test"); !errors.Is(err, store.ErrPrimaryRace) {
			t.Fatalf("%v", err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM user_emails WHERE user_id = 'A' AND is_primary = 1`); n != 1 {
			t.Fatalf("%d primaries", n)
		}
	})
}

// A claim of an address another account holds unverified waits as a link: the
// account lists it (PendingEmailClaims) until the address is its row, and
// withdrawing it drops the link.
func TestPendingEmailClaims(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.Now()
		addPerson(t, q, "A", store.DefaultTenantID, "a@example.test", true)
		if _, err := store.AddEmail(ctx, q, "A", "mine@example.test", false, "", false, now); err != nil {
			t.Fatal(err)
		}
		for _, e := range []string{"mine@example.test", "claim@example.test"} {
			if err := store.ReplaceEmailToken(ctx, q, store.EmailToken{Hash: "h-" + e, Kind: store.TokenVerify, UserID: "A", Email: e, Created: now, Expires: now.Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.ReplaceEmailToken(ctx, q, store.EmailToken{Hash: "old", Kind: store.TokenVerify, UserID: "A", Email: "old@example.test", Created: now, Expires: now.Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		got, err := store.PendingEmailClaims(ctx, q, "A", now)
		if err != nil || len(got) != 1 || got[0].Email != "claim@example.test" {
			t.Fatalf("%+v %v", got, err)
		}
	})
}
