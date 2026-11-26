package store_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

func addPerson(t *testing.T, q store.Q, id, tenant, email string, verified bool) {
	t.Helper()
	u := store.User{ID: id, Username: strings.ToLower(id), Name: id, Email: email, EmailVerified: verified, Role: "member", Created: time.Now(), TenantID: tenant}
	if err := store.InsertUser(ctx, q, u); err != nil {
		t.Fatalf("%s: %v", id, err)
	}
}

// One address is one account on the whole server: another tenant, another
// spelling of the same address, a new account or a change, all refused.
func TestAnAddressBelongsToOneAccountOnTheServer(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.Now()
		if err := st.CreateTenant(ctx, store.Tenant{ID: "T000000009", Slug: "nine", Name: "Nine", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		addPerson(t, q, "A", store.DefaultTenantID, "Ann@Example.COM", false)
		addPerson(t, q, "B", "T000000009", "", false)
		if err := store.InsertUser(ctx, q, store.User{ID: "C", Username: "c", Name: "C", Email: "ann@example.com", Role: "member", Created: now, TenantID: "T000000009"}); !errors.Is(err, store.ErrEmailTaken) {
			t.Fatalf("a new account with a taken address: %v", err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM users WHERE id = 'C'`); n != 0 {
			t.Fatal("the refused account was made")
		}
		if err := store.SetEmail(ctx, q, "B", "ANN@example.com", true, ""); !errors.Is(err, store.ErrEmailTaken) {
			t.Fatalf("setting a taken address in another tenant: %v", err)
		}
		if err := store.UpdateProfile(ctx, q, "B", "B", "ann@EXAMPLE.com", false); !errors.Is(err, store.ErrEmailTaken) {
			t.Fatalf("a profile with a taken address: %v", err)
		}
		// Its own address under another spelling is still its own.
		if err := store.SetEmail(ctx, q, "A", "ANN@example.com", false, ""); err != nil {
			t.Fatal(err)
		}
		if row, err := store.EmailOwner(ctx, q, "ann@example.com"); err != nil || row.UserID != "A" || row.Email != "ANN@example.com" {
			t.Fatalf("%+v %v", row, err)
		}
		assertCopies(t, st)
	})
}

// Whoever proves an address takes it from an account that holds it
// unverified; a verified holder, and a deleted one until it is purged, keep it.
func TestAnUnverifiedAddressGoesToWhoeverProvesIt(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		addPerson(t, q, "SQUAT", store.DefaultTenantID, "tang@example.test", false)
		addPerson(t, q, "TANG", store.DefaultTenantID, "", false)
		// Not proved: refused.
		if _, err := store.SetPrimaryEmail(ctx, q, "TANG", "tang@example.test", false, "", store.PrimaryOpts{TakeUnverified: true}); !errors.Is(err, store.ErrEmailTaken) {
			t.Fatalf("an unproved address moved: %v", err)
		}
		r, err := store.SetPrimaryEmail(ctx, q, "TANG", "Tang@Example.test", true, "", store.PrimaryOpts{TakeUnverified: true})
		if err != nil || r == nil || r.UserID != "SQUAT" || r.Email != "tang@example.test" {
			t.Fatalf("%+v %v", r, err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM users WHERE id = 'SQUAT' AND email = '' AND email_key = '' AND email_verified = 0`); n != 1 {
			t.Fatal("the earlier holder kept its copy of the address")
		}
		if u, row, err := store.UserByVerifiedEmail(ctx, q, "TANG@example.test"); err != nil || u.ID != "TANG" || row.Email != "Tang@Example.test" {
			t.Fatalf("%v %+v %v", u, row, err)
		}
		// Verified now: never displaced.
		addPerson(t, q, "LATE", store.DefaultTenantID, "", false)
		if _, err := store.SetPrimaryEmail(ctx, q, "LATE", "tang@example.test", true, "", store.PrimaryOpts{TakeUnverified: true}); !errors.Is(err, store.ErrEmailTaken) {
			t.Fatalf("a verified address moved: %v", err)
		}
		// A tenant administrator's mark alone is no hold on the address: whoever
		// proves it takes it.
		addPerson(t, q, "MARKED", store.DefaultTenantID, "", false)
		if err := store.SetEmail(ctx, q, "MARKED", "owner@example.test", true, store.VerifiedByTenantAdmin); err != nil {
			t.Fatal(err)
		}
		if r, err := store.SetPrimaryEmail(ctx, q, "LATE", "owner@example.test", true, "", store.PrimaryOpts{TakeUnverified: true}); err != nil || r == nil || r.UserID != "MARKED" {
			t.Fatalf("a tenant administrator's mark kept the address: %+v %v", r, err)
		}
		// A deleted account keeps even an unverified address, until the purge.
		addPerson(t, q, "GONE", store.DefaultTenantID, "gone@example.test", false)
		if _, err := q.Exec(ctx, `UPDATE users SET deleted_at = ?, recycle_item = 'R1' WHERE id = 'GONE'`, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
		if row, err := store.EmailOwner(ctx, q, "gone@example.test"); err != nil || !row.UserDeleted {
			t.Fatalf("%+v %v", row, err)
		}
		if _, _, err := store.UserByVerifiedEmail(ctx, q, "gone@example.test"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a deleted account was found: %v", err)
		}
		if _, err := store.SetPrimaryEmail(ctx, q, "LATE", "gone@example.test", true, "", store.PrimaryOpts{TakeUnverified: true}); !errors.Is(err, store.ErrEmailTaken) {
			t.Fatalf("a deleted account's address moved: %v", err)
		}
		if err := st.Tx(ctx, func(q store.Q) error { return store.PurgeUserData(ctx, q, "GONE", "R1", time.Now()) }); err != nil {
			t.Fatal(err)
		}
		if err := store.SetEmail(ctx, q, "LATE", "gone@example.test", false, ""); err != nil {
			t.Fatalf("after the purge: %v", err)
		}
		assertCopies(t, st)
	})
}

// An unverified address finds nobody to sign in or reset; a verified one that
// is not the primary does (accounts will have several).
func TestSignInAndResetFindAVerifiedRowOnly(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		addPerson(t, q, "A", store.DefaultTenantID, "a@example.test", false)
		if _, _, err := store.UserByVerifiedEmail(ctx, q, "a@example.test"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("an unverified address found %v", err)
		}
		if _, err := store.AddEmail(ctx, q, "A", "second@example.test", false, "", false, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.UserByVerifiedEmail(ctx, q, "second@example.test"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("an unverified second address found someone")
		}
		if err := store.SetEmailRowVerified(ctx, q, "A", "second@example.test", true, store.VerifiedByProvider("gh")); err != nil {
			t.Fatal(err)
		}
		if u, row, err := store.UserByVerifiedEmail(ctx, q, "SECOND@example.test"); err != nil || u.ID != "A" || row.Primary || row.VerifiedBy != "provider:gh" {
			t.Fatalf("%v %+v %v", u, row, err)
		}
		// The second address does not touch the copy; the primary is the UPN.
		if n := count(t, st, `SELECT COUNT(*) FROM users WHERE id = 'A' AND email = 'a@example.test' AND email_verified = 0`); n != 1 {
			t.Fatal("a second address changed the copy")
		}
		// Switching the primary keeps the old one as a second address.
		if _, err := store.SetPrimaryEmail(ctx, q, "A", "second@example.test", true, "provider:gh", store.PrimaryOpts{KeepOld: true}); err != nil {
			t.Fatal(err)
		}
		l, err := store.UserEmails(ctx, q, "A")
		if err != nil || len(l) != 2 || l[0].Email != "second@example.test" || !l[0].Primary || l[1].Primary {
			t.Fatalf("%+v %v", l, err)
		}
		if err := store.RemoveEmail(ctx, q, "A", "second@example.test"); !errors.Is(err, store.ErrPrimaryEmail) {
			t.Fatalf("removed the primary: %v", err)
		}
		if err := store.RemoveEmail(ctx, q, "A", "a@example.test"); err != nil {
			t.Fatal(err)
		}
		// A tenant administrator's mark alone finds nobody either.
		addPerson(t, q, "MARKED", store.DefaultTenantID, "", false)
		if err := store.SetEmail(ctx, q, "MARKED", "marked@example.test", true, store.VerifiedByTenantAdmin); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.UserByVerifiedEmail(ctx, q, "marked@example.test"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a tenant administrator's mark found someone: %v", err)
		}
		assertCopies(t, st)
	})
}

// Addresses outside ASCII: the domain as punycode, the local part NFC and case
// folded, so each address has one key whatever its spelling.
func TestEmailKey(t *testing.T) {
	t.Parallel()
	same := [][2]string{
		{"用户@例子.公司", "用户@xn--fsqu00a.xn--55qx5d"},
		{"用户@例子.公司", "用户@XN--FSQU00A.XN--55QX5D"},
		{"Ann@Example.COM", "ann@example.com"},
		{"ÉMILE@exämple.test", "émile@xn--exmple-cua.test"},
		{"é@example.test", "é@example.test"}, // NFC
		{"STRASSE@example.test", "strasse@example.test"},
		{"Straße@example.test", "strasse@example.test"}, // full folding
		{"bob@example.com.", "bob@example.com"},         // the root's dot
		{"bob@例子.公司.", "bob@xn--fsqu00a.xn--55qx5d"},
	}
	for _, p := range same {
		a, okA := store.EmailKey(p[0])
		b, okB := store.EmailKey(p[1])
		if !okA || !okB || a != b {
			t.Errorf("%q (%q) and %q (%q) differ", p[0], a, p[1], b)
		}
	}
	// The look-alike domain is another domain.
	if a, _ := store.EmailKey("tom@gİthub.test"); a == "tom@github.test" {
		t.Error("a dotted capital I folded into github.test")
	}
	for _, bad := range []string{"", "x", "@x", "x@", "a@b@", "x@\xff",
		// Empty labels, as typed or once punycode is decoded.
		"a@xn--.com", "a@.com", "a@example..com", "a@example.com..", "a@.", "a@例子..公司"} {
		if k, ok := store.EmailKey(bad); ok {
			t.Errorf("%q has a key %q", bad, k)
		}
	}
	// The exact form keeps what folding joins: the long s (U+017F) is not "s".
	k1, _ := store.EmailExactKey("Kim@example.test")
	k2, _ := store.EmailExactKey("sam@example.test")
	if k1 == k2 {
		t.Error("the exact form folded the long s")
	}
	if a, _ := store.EmailExactKey("bob@example.com."); a != "bob@example.com" {
		t.Errorf("the exact form kept the root's dot: %q", a)
	}
	if k, ok := store.EmailExactKey("a@xn--.com"); ok {
		t.Errorf("the exact form keyed an empty label: %q", k)
	}
}

// AddEmail that fails after it took the address from its holder says nobody
// lost it: the take is rolled back with the rest.
func TestAFailedAddEmailReleasesNobody(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		addPerson(t, q, "HOLDER", store.DefaultTenantID, "add@example.test", false)
		addPerson(t, q, "OTHER", store.DefaultTenantID, "", false)
		addPerson(t, q, "ADDER", store.DefaultTenantID, "adder@example.test", true)
		store.SetEmailInsertRaceHook(func(q store.Q) {
			store.SetEmailInsertRaceHook(nil)
			if err := store.SetEmail(ctx, q, "OTHER", "add@example.test", false, ""); err != nil {
				t.Error(err)
			}
		})
		t.Cleanup(func() { store.SetEmailInsertRaceHook(nil) })
		r, err := store.AddEmail(ctx, q, "ADDER", "add@example.test", true, "", true, time.Now())
		if !errors.Is(err, store.ErrEmailTaken) || r != nil {
			t.Fatalf("%+v %v", r, err)
		}
		if row, err := store.EmailOwner(ctx, q, "add@example.test"); err != nil || row.UserID != "HOLDER" {
			t.Fatalf("%+v %v", row, err)
		}
		assertCopies(t, st)
	})
}

// Another writer may take the address between SetPrimaryEmail's check and its
// insert. The unique index refuses the insert (ErrEmailTaken), and the
// caller's transaction stays usable: a refused statement aborts a PostgreSQL
// transaction, and CreateUser goes on without the address.
func TestAnAddressTakenAtTheInsertLeavesTheTransactionUsable(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		addPerson(t, q, "FIRST", store.DefaultTenantID, "", false)
		addPerson(t, q, "SECOND", store.DefaultTenantID, "second@example.test", false)
		// The other writer: another connection on PostgreSQL. SQLite has one
		// writer, so there it is the transaction itself (its row then goes with
		// the refused call's work).
		other := st.Dialect() == store.Postgres
		store.SetEmailInsertRaceHook(func(q store.Q) {
			store.SetEmailInsertRaceHook(nil)
			if other {
				q = st.Q()
			}
			if err := store.SetEmail(ctx, q, "FIRST", "race@example.test", false, ""); err != nil {
				t.Error(err)
			}
		})
		t.Cleanup(func() { store.SetEmailInsertRaceHook(nil) })
		err := st.Tx(ctx, func(q store.Q) error {
			if _, err := store.SetPrimaryEmail(ctx, q, "SECOND", "race@example.test", true, "", store.PrimaryOpts{TakeUnverified: true}); !errors.Is(err, store.ErrEmailTaken) {
				t.Errorf("the insert: %v", err)
			}
			_, err := q.Exec(ctx, `UPDATE users SET name = 'went on' WHERE id = 'SECOND'`)
			return err
		})
		if err != nil {
			t.Fatalf("the transaction: %v", err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM users WHERE id = 'SECOND' AND name = 'went on' AND email = 'second@example.test'`); n != 1 {
			t.Fatal("the refused address changed the account, or the transaction did not go on")
		}
		if row, err := store.EmailOwner(ctx, q, "race@example.test"); other && (err != nil || row.UserID != "FIRST") {
			t.Fatalf("%+v %v", row, err)
		}
		assertCopies(t, st)
	})
}

// assertCopies is the invariant: users.email* equals the primary row of
// user_emails, for everyone, and nobody has an address without a row.
func assertCopies(t *testing.T, st *store.Store) {
	t.Helper()
	if n := count(t, st, `SELECT COUNT(*) FROM users u LEFT JOIN user_emails e ON e.user_id = u.id AND e.is_primary = 1
		WHERE (e.user_id IS NULL AND (u.email <> '' OR u.email_key <> '' OR u.email_verified <> 0 OR u.email_verified_by <> ''))
		OR (e.user_id IS NOT NULL AND (u.email <> e.email OR u.email_key <> e.email_key OR u.email_verified <> e.verified OR u.email_verified_by <> e.verified_by))`); n != 0 {
		t.Fatalf("%d people whose users.email differs from their primary address", n)
	}
}

// Only the functions of emails.go write users.email (and the purge, which
// clears it with the rows): the copy cannot drift from user_emails.
func TestOnlyEmailsGoWritesTheCopy(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	update := regexp.MustCompile(`(?is)\bUPDATE\s+users\s+SET\b(.*?)\bWHERE\b`)
	sets := regexp.MustCompile(`(^|[\s,])email(_key|_verified|_verified_by)?\s*=\s*\?`)
	allowed := map[string]bool{"PurgeUserData": true}
	var bad []string
	for name, f := range pkgs["store"].Files {
		if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "emails.go") {
			continue
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil || allowed[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				for _, m := range update.FindAllStringSubmatch(s, -1) {
					if sets.MatchString(m[1]) {
						bad = append(bad, name+": "+fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("%s writes users.email outside emails.go", b)
	}
}
