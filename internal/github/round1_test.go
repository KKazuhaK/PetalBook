package github_test

import (
	"context"
	"net/url"
	"strconv"
	"testing"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// Whether a person's last way in may move with their link to another GitHub
// account is asked in the transaction that moves it: what the guard reads and
// does there is part of the write, and a refusal undoes it with the move.
func TestTheLastWayInIsCheckedInTheMovesTransaction(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.configure()
		h.user("U1", "a")
		ida := h.f.AddUser("gh-a").ID
		h.f.AddUser("gh-b")
		if err := store.InsertLink(ctx, st.Q(), store.Link{ID: "L1", ProviderID: "github", UserID: "U1", Subject: strconv.FormatInt(ida, 10), Login: "gh-a",
			SignIn: true, Created: h.clk.Now()}); err != nil {
			t.Fatal(err)
		}
		allow := false
		h.svc.SetSigninGuard(func(ctx context.Context, q store.Q, user, link string) (bool, error) {
			// A write in the guard's transaction stands or falls with the move.
			if _, err := q.Exec(ctx, `UPDATE users SET name = 'asked' WHERE id = ?`, user); err != nil {
				return false, err
			}
			return allow, nil
		})
		name := func() string {
			var n string
			if err := st.Q().QueryRow(ctx, `SELECT name FROM users WHERE id = 'U1'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		before := name()
		authURL, binding, _ := h.svc.StartAuthorization(ctx, "U1", "/")
		back, _ := url.Parse(h.f.Authorize(authURL, "gh-b"))
		if to, _ := h.svc.CompleteAuthorization(ctx, binding, "U1", back.Query()); to != "/settings?provider=github&connection_error=last_method" {
			t.Fatalf("%q", to)
		}
		if got := name(); got != before {
			t.Fatalf("the refused move kept what its transaction wrote: %q", got)
		}
		allow = true
		authURL, binding, _ = h.svc.StartAuthorization(ctx, "U1", "/")
		back, _ = url.Parse(h.f.Authorize(authURL, "gh-b"))
		if to, _ := h.svc.CompleteAuthorization(ctx, binding, "U1", back.Query()); to != "/" {
			t.Fatalf("%q", to)
		}
		if got := name(); got != "asked" {
			t.Fatalf("the guard was not asked in the move's transaction: %q", got)
		}
		links, _ := store.ListUserLinks(ctx, st.Q(), "U1")
		if len(links) != 1 || links[0].Login != "gh-b" || links[0].SignIn {
			t.Fatalf("%+v", links)
		}
	})
}
