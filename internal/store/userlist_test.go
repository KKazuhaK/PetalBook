package store_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// The administration's list of people (3.10): keyset pages in each sort, the
// filters, the scope, and the counts read for the page alone.
func TestUserRowsSortFilterAndPage(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st) // Ua in tenant A, Ub in tenant B
		q := st.Q()
		base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		for i := 0; i < 12; i++ {
			id := fmt.Sprintf("P%02d", i)
			role := "member"
			if i%5 == 0 {
				role = "admin"
			}
			u := store.User{ID: id, Username: fmt.Sprintf("p%02d", i), Name: fmt.Sprintf("Person %d", i%3), Role: role, Created: base.Add(time.Duration(i/2) * time.Hour), TenantID: tenA, SiteAdmin: i == 5}
			if err := store.InsertUser(ctx, q, u); err != nil {
				t.Fatal(err)
			}
			if i%3 != 0 {
				if err := st.NoteActive(ctx, id, base.Add(time.Duration(i%4)*24*time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			if i == 2 || i == 3 {
				if err := store.InsertPasskey(ctx, q, store.PasskeyRow{ID: fmt.Sprintf("K%d", i), UserID: id, CredentialID: fmt.Sprintf("c%d", i), Credential: "x", Name: "key", Created: base}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if _, err := q.Exec(ctx, `UPDATE users SET disabled = 1 WHERE id = 'P05'`); err != nil {
			t.Fatal(err)
		}
		if _, err := q.Exec(ctx, `UPDATE users SET deleted_at = 1 WHERE id = 'P11'`); err != nil {
			t.Fatal(err)
		}
		all := func(sc store.Scope, f store.UserFilter, limit int) []store.UserRow {
			t.Helper()
			var out []store.UserRow
			f.Limit = limit
			for n := 0; n < 100; n++ {
				rows, next, err := store.ListUserRows(ctx, q, sc, f)
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
		ids := func(l []store.UserRow) string {
			var s []string
			for _, p := range l {
				s = append(s, p.ID)
			}
			return strings.Join(s, " ")
		}
		inA := store.InTenant(tenA)
		// 11 of the 12 (one is deleted) and Ua: 12 in tenant A; the other tenant's is not here.
		for _, sortName := range store.UserSorts {
			for _, order := range []string{"asc", "desc"} {
				whole := all(inA, store.UserFilter{Sort: sortName, Order: order}, 100)
				if len(whole) != 12 || strings.Contains(ids(whole), "Ub") || strings.Contains(ids(whole), "P11") {
					t.Fatalf("%s %s: %s", sortName, order, ids(whole))
				}
				for _, limit := range []int{1, 3, 5} {
					if got := all(inA, store.UserFilter{Sort: sortName, Order: order}, limit); ids(got) != ids(whole) {
						t.Fatalf("%s %s pages of %d:\n%s\n%s", sortName, order, limit, ids(got), ids(whole))
					}
				}
			}
		}
		if n := len(all(store.AllTenants(), store.UserFilter{}, 100)); n != 13 {
			t.Fatalf("a site administrator sees %d people, want 13", n)
		}
		// By name, then id; the people who were never active come last by lastActive, in either order.
		byName := all(inA, store.UserFilter{Sort: "name"}, 100)
		for i := 1; i < len(byName); i++ {
			a, b := byName[i-1], byName[i]
			if a.Name > b.Name || a.Name == b.Name && a.ID > b.ID {
				t.Fatalf("not by name: %s", ids(byName))
			}
		}
		for _, order := range []string{"asc", "desc"} {
			rows := all(inA, store.UserFilter{Sort: "lastActive", Order: order}, 4)
			seenNever := false
			for _, r := range rows {
				if r.LastActive.IsZero() {
					seenNever = true
				} else if seenNever {
					t.Fatalf("lastActive %s: someone active after a never-active one: %s", order, ids(rows))
				}
			}
		}
		// Filters.
		for _, c := range []struct {
			f    store.UserFilter
			want string
		}{
			{store.UserFilter{Query: "person 1"}, "P01 P04 P07 P10"},
			{store.UserFilter{Role: "admin"}, "P00 P05 P10"},
			{store.UserFilter{Role: "site"}, "P05"},
			{store.UserFilter{State: "disabled"}, "P05"},
			{store.UserFilter{State: "enabled", Role: "admin"}, "P00 P10"},
			{store.UserFilter{InactiveBefore: base.Add(24 * time.Hour)}, "P00 P03 P04 P06 P08 P09 Ua"},
			{store.UserFilter{ActiveSince: base.Add(24 * time.Hour)}, "P01 P02 P05 P07 P10"},
		} {
			got := all(inA, c.f, 100)
			var want []string
			for _, id := range strings.Fields(c.want) {
				want = append(want, id)
			}
			set := map[string]bool{}
			for _, r := range got {
				set[r.ID] = true
			}
			if len(got) != len(want) {
				t.Fatalf("%+v: got %s, want %s", c.f, ids(got), c.want)
			}
			for _, id := range want {
				if !set[id] {
					t.Fatalf("%+v: got %s, want %s", c.f, ids(got), c.want)
				}
			}
		}
		// The counts are the page's own.
		rows := all(inA, store.UserFilter{Query: "p0"}, 100)
		for _, r := range rows {
			want := 0
			if r.ID == "P02" || r.ID == "P03" {
				want = 1
			}
			if r.Passkeys != want || r.Identities != 0 {
				t.Fatalf("%s: %d passkeys, %d identities", r.ID, r.Passkeys, r.Identities)
			}
		}
		// A value the list does not have, and a cursor of another sort.
		if _, _, err := store.ListUserRows(ctx, q, inA, store.UserFilter{Role: "king"}); err != store.ErrBadFilter {
			t.Fatalf("%v", err)
		}
		_, next, _ := store.ListUserRows(ctx, q, inA, store.UserFilter{Limit: 2})
		if _, _, err := store.ListUserRows(ctx, q, inA, store.UserFilter{Sort: "name", After: next}); err != store.ErrBadCursor {
			t.Fatalf("%v", err)
		}
		// A cursor of the spaces list is not one of this list, even where the
		// sort has the same name and direction.
		if _, _, err := store.ListUserRows(ctx, q, inA, store.UserFilter{After: "spaces.created.asc:1700000000000:P03"}); err != store.ErrBadCursor {
			t.Fatalf("%v", err)
		}
		// The search finds a person by the address too.
		if err := store.InsertUser(ctx, q, store.User{ID: "P90", Username: "zed", Name: "Zed", Email: "Zed.Mail@Example.org", Role: "member", Created: base, TenantID: tenA}); err != nil {
			t.Fatal(err)
		}
		if rows := all(inA, store.UserFilter{Query: "zed.mail@exam"}, 100); len(rows) != 1 || rows[0].ID != "P90" {
			t.Fatalf("%+v", rows)
		}
		// One person, in scope or not.
		if _, err := store.GetUserRow(ctx, q, inA, "Ub"); err != store.ErrNotFound {
			t.Fatalf("%v", err)
		}
		if r, err := store.GetUserRow(ctx, q, inA, "P03"); err != nil || r.Passkeys != 1 {
			t.Fatalf("%+v %v", r, err)
		}
	})
}

// A person's sessions: listed by last use, ended one at a time or together with
// the reason recorded, and only the owner's can be ended through their id.
func TestUserSessionsAreListedAndEnded(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "alice", "member")
		mkUser(t, st, "B", "bob", "member")
		now := time.Now().Truncate(time.Millisecond)
		long := strings.Repeat("x", 500)
		for i, s := range []store.Session{
			{ID: "s1", UserID: "A", Created: now, LastUsed: now.Add(-time.Hour), UserAgent: "Firefox", Method: "password"},
			{ID: "s2", UserID: "A", Created: now, LastUsed: now, UserAgent: long, Method: "sso:github"},
			{ID: "s3", UserID: "B", Created: now, LastUsed: now},
		} {
			if err := store.InsertSession(ctx, st.Q(), s); err != nil {
				t.Fatal(i, err)
			}
		}
		rows, err := store.ListUserSessions(ctx, st.Q(), "A")
		if err != nil || len(rows) != 2 || rows[0].ID != "s2" || rows[1].UserAgent != "Firefox" || rows[1].Method != "password" {
			t.Fatalf("%+v %v", rows, err)
		}
		if len(rows[0].UserAgent) != store.MaxUserAgent {
			t.Fatalf("the user agent is %d bytes long", len(rows[0].UserAgent))
		}
		// Not Bob's.
		if err := store.EndUserSession(ctx, st.Q(), "s3", "A", store.EndElsewhere, now); err != store.ErrNotFound {
			t.Fatalf("%v", err)
		}
		if err := store.EndUserSession(ctx, st.Q(), "s1", "A", store.EndElsewhere, now); err != nil {
			t.Fatal(err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM session_ends WHERE id = 's1' AND reason = 'signed_out_elsewhere'`); n != 1 {
			t.Fatalf("%d records", n)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM sessions WHERE user_id = 'A'`); n != 1 {
			t.Fatalf("%d sessions left", n)
		}
	})
}
