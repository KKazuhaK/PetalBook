package spaces

import (
	"errors"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// A space that syncs with a person's own tokens (a remote via "user") loses
// access when the person's tokens go, as one through an installation does when
// the installation is disconnected: the storage requirement counts it as having
// no remote, from that moment, with its grace period again.

const ulProvider = "ulgt"

func ulProviderRow(t *testing.T, st *store.Store, now time.Time) {
	t.Helper()
	if err := store.InsertConnProviderRow(ctx, st.Q(), store.ConnProviderRow{ID: ulProvider, Type: "gitea", Host: ulProvider + ".example.com", Scheme: "https",
		Name: ulProvider, Owner: store.ConnProviderSite, State: "ok", Created: now, Updated: now}); err != nil {
		t.Fatal(err)
	}
}

// ulLink gives a person a repository link with tokens (and a sign-in with it
// when signIn: the link is then removed by DeleteLink, as a person unlinks it or
// an administrator does).
func ulLink(t *testing.T, st *store.Store, user store.User, signIn bool, now time.Time) {
	t.Helper()
	if _, err := store.PutRepoLink(ctx, st.Q(), store.RepoLink{ID: "L" + user.ID, ProviderID: ulProvider, UserID: user.ID, Subject: "sub-" + user.ID, Login: user.Username,
		SignIn: signIn, AccessEnc: "a", RefreshEnc: "r", State: store.LinkOK, Connected: now, Updated: now}); err != nil {
		t.Fatal(err)
	}
}

// ulRemote makes a space's own remote one that syncs with a person's link.
func ulRemote(t *testing.T, st *store.Store, spaceID string, user store.User, now time.Time) {
	t.Helper()
	if err := store.PutRemote(ctx, st.Q(), store.Remote{SpaceID: spaceID, URL: "https://" + ulProvider + ".example.com/" + user.Username + "/notes.git", Branch: "main", State: "ok", Updated: now,
		CredType: "connection", Provider: ulProvider, Via: "user", RepoID: 1, CredUserID: user.ID}); err != nil {
		t.Fatal(err)
	}
}

func TestASpaceThatLostItsPersonsTokensHasItsGraceAgain(t *testing.T) {
	t.Parallel()
	// Every way the tokens go: the person disconnects (or the host's
	// credentials are removed), an administrator unlinks the account, the
	// person is deleted, the authorisation expires.
	for _, c := range []struct {
		name   string
		signIn bool
		lose   func(t *testing.T, st *store.Store, link store.RepoLink, now time.Time) []string
	}{
		{"disconnect", false, func(t *testing.T, st *store.Store, l store.RepoLink, now time.Time) []string {
			ids, err := store.DeleteRepoLink(ctx, st.Q(), l.ProviderID, l.UserID, now)
			if err != nil {
				t.Fatal(err)
			}
			return ids
		}},
		{"unlink, by the person or an administrator", true, func(t *testing.T, st *store.Store, l store.RepoLink, now time.Time) []string {
			ids, err := store.DeleteLink(ctx, st.Q(), l.ID, l.UserID, now)
			if err != nil {
				t.Fatal(err)
			}
			return ids
		}},
		{"the person is deleted", false, func(t *testing.T, st *store.Store, l store.RepoLink, now time.Time) []string {
			ids, err := store.DeleteUserRepoLinks(ctx, st.Q(), l.UserID, now)
			if err != nil {
				t.Fatal(err)
			}
			return ids
		}},
		{"the authorisation expires", false, func(t *testing.T, st *store.Store, l store.RepoLink, now time.Time) []string {
			got, err := store.GetRepoLink(ctx, st.Q(), l.ProviderID, l.UserID)
			if err != nil {
				t.Fatal(err)
			}
			ok, ids, err := store.ExpireLink(ctx, st.Q(), got.ID, got.Version, now)
			if err != nil || !ok {
				t.Fatalf("%v %v", ok, err)
			}
			return ids
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			storetest.Each(t, func(t *testing.T, st *store.Store) {
				f := remoteManager(t, st)
				other := addUser(t, st, "U2", "other")
				in, h := f.space("Notes")
				defer h.Release()
				otherIn, err := f.m.Create(ctx, other.ID, "Theirs")
				if err != nil {
					t.Fatal(err)
				}
				setRequire(t, f.m, "any", "all", 30)
				now := f.clk.Now()
				ulProviderRow(t, st, now)
				ulLink(t, st, f.user, c.signIn, now)
				ulLink(t, st, other, false, now)
				ulRemote(t, st, in.ID, f.user, now)
				ulRemote(t, st, otherIn.ID, other, now)
				f.m.PolicyChanged()
				f.clk.Advance(90 * 24 * time.Hour) // far past what the requirement gave it
				for _, id := range []string{in.ID, otherIn.ID} {
					if n, err := f.m.Notice(ctx, id); err != nil || n != nil {
						t.Fatalf("with its tokens: %+v %v", n, err)
					}
				}
				lostAt := f.clk.Now()
				ids := c.lose(t, st, store.RepoLink{ID: "L" + f.user.ID, ProviderID: ulProvider, UserID: f.user.ID}, lostAt)
				if len(ids) != 1 || ids[0] != in.ID {
					t.Fatalf("the spaces that lost access: %v", ids)
				}
				f.m.AccessLost(ids)
				// In grace from the moment of the loss, with a notice, and still writable.
				n, err := f.m.Notice(ctx, in.ID)
				if err != nil || n == nil || n.Overdue || !n.Deadline.Equal(lostAt.Add(30*24*time.Hour).Truncate(time.Millisecond)) {
					t.Fatalf("just after the loss: %+v %v", n, err)
				}
				if err := f.m.CheckWritable(in.ID); err != nil {
					t.Fatalf("read only at once: %v", err)
				}
				if l, _ := f.m.List(ctx, f.user.ID); len(l) != 1 || l[0].RemoteNotice == nil || l[0].RemoteNotice.Overdue {
					t.Fatalf("the list: %+v", l)
				}
				// The remote says why, on the space too.
				var st1, kind string
				if err := st.Q().QueryRow(ctx, `SELECT r.state, r.error_kind FROM remotes r WHERE r.space_id = ? AND r.role = 'content'`, in.ID).Scan(&st1, &kind); err != nil || st1 != store.RemoteError || kind != store.AccessLost {
					t.Fatalf("the remote: %q %q %v", st1, kind, err)
				}
				var ss string
				if err := st.Q().QueryRow(ctx, `SELECT remote_state FROM spaces WHERE id = ?`, in.ID).Scan(&ss); err != nil || ss != store.RemoteError {
					t.Fatalf("the space: %q %v", ss, err)
				}
				// Another person's space is not touched.
				if n, _ := f.m.Notice(ctx, otherIn.ID); n != nil {
					t.Fatalf("another person's space: %+v", n)
				}
				if err := st.Q().QueryRow(ctx, `SELECT remote_state FROM spaces WHERE id = ?`, otherIn.ID).Scan(&ss); err != nil || ss != "ok" {
					t.Fatalf("another person's space state: %q %v", ss, err)
				}
				// At the deadline itself it is still in grace; after it, overdue and read only.
				f.clk.Advance(30 * 24 * time.Hour)
				if n, _ := f.m.Notice(ctx, in.ID); n == nil || n.Overdue {
					t.Fatalf("at the deadline: %+v", n)
				}
				if err := f.m.CheckWritable(in.ID); err != nil {
					t.Fatalf("read only at the deadline: %v", err)
				}
				f.clk.Advance(time.Millisecond)
				if n, _ := f.m.Notice(ctx, in.ID); n == nil || !n.Overdue {
					t.Fatalf("after the grace: %+v", n)
				}
				var re *RequiredError
				if err := f.m.CheckWritable(in.ID); !errors.As(err, &re) {
					t.Fatalf("after the grace: %v", err)
				}
				if err := f.m.CheckWritable(otherIn.ID); err != nil {
					t.Fatalf("another person's space is read only: %v", err)
				}
				// The lists the administration reads say the same.
				if l, _ := f.m.List(ctx, f.user.ID); len(l) != 1 || l[0].RemoteNotice == nil || !l[0].RemoteNotice.Overdue {
					t.Fatalf("the list after the grace: %+v", l)
				}
			})
		})
	}
}

// The person connecting again restores the remote with no more than the next
// sync: what the requirement knows is read again, and the grace period of the
// loss is never cleared (losing the tokens again with no good sync between
// gives no more time).
func TestAPersonConnectingAgainRestoresTheirSpaces(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := remoteManager(t, st)
		in, h := f.space("Notes")
		defer h.Release()
		setRequire(t, f.m, "any", "all", 30)
		now := f.clk.Now()
		ulProviderRow(t, st, now)
		ulLink(t, st, f.user, false, now)
		ulRemote(t, st, in.ID, f.user, now)
		f.m.PolicyChanged()
		f.clk.Advance(90 * 24 * time.Hour)
		lostAt := f.clk.Now()
		ids, err := store.DeleteRepoLink(ctx, st.Q(), ulProvider, f.user.ID, lostAt)
		if err != nil || len(ids) != 1 {
			t.Fatalf("%v %v", ids, err)
		}
		f.m.AccessLost(ids)
		if n, _ := f.m.Notice(ctx, in.ID); n == nil || n.Overdue {
			t.Fatalf("in grace: %+v", n)
		}
		// Connected again: the remote counts again, the notice is gone, and the
		// next sync (any success) puts the states right.
		f.clk.Advance(24 * time.Hour)
		ulLink(t, st, f.user, false, f.clk.Now())
		f.m.ConnectionFixed(f.user.ID)
		if n, err := f.m.Notice(ctx, in.ID); err != nil || n != nil {
			t.Fatalf("connected again: %+v %v", n, err)
		}
		if err := store.UpdateRemoteStatus(ctx, st.Q(), in.ID, store.RoleContent, store.RemoteStatus{State: "ok", LastSync: f.clk.Now()}, f.clk.Now()); err != nil {
			t.Fatal(err)
		}
		var ss string
		if err := st.Q().QueryRow(ctx, `SELECT remote_state FROM spaces WHERE id = ?`, in.ID).Scan(&ss); err != nil || ss != "ok" {
			t.Fatalf("the space after the sync: %q %v", ss, err)
		}
		// The loss stays recorded: a person who connects and disconnects with no
		// sync between does not get the grace period again; one who synced does.
		var lost int64
		if err := st.Q().QueryRow(ctx, `SELECT remote_lost_at FROM spaces WHERE id = ?`, in.ID).Scan(&lost); err != nil || lost != lostAt.UnixMilli() {
			t.Fatalf("remote_lost_at is %d, want %d: %v", lost, lostAt.UnixMilli(), err)
		}
		f.clk.Advance(24 * time.Hour)
		again := f.clk.Now()
		if _, err := store.DeleteRepoLink(ctx, st.Q(), ulProvider, f.user.ID, again); err != nil {
			t.Fatal(err)
		}
		if err := st.Q().QueryRow(ctx, `SELECT remote_lost_at FROM spaces WHERE id = ?`, in.ID).Scan(&lost); err != nil || lost != again.UnixMilli() {
			t.Fatalf("a loss after a good sync starts again: %d, want %d: %v", lost, again.UnixMilli(), err)
		}
	})
}

// A space that syncs through an installation is not touched by the person's
// tokens going at GitHub (it has its own rule), and nor is a space that syncs
// with the person's link at another provider.
func TestARemoteThroughAnInstallationIsNotAffectedByTheTokensGoing(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := remoteManager(t, st)
		app, h1 := f.space("App")
		defer h1.Release()
		tok, h2 := f.space("Token")
		defer h2.Release()
		setRequire(t, f.m, "any", "all", 30)
		now := f.clk.Now()
		ulProviderRow(t, st, now)
		if err := store.PutGitHubApp(ctx, st.Q(), store.GitHubApp{App: `{"appId":"1","fingerprint":"f"}`, ClientID: "Iv1.x", BaseURL: "https://github.com"}, now); err != nil {
			t.Fatal(err)
		}
		if err := store.ClaimGitHubInstallation(ctx, st.Q(), store.GitHubInstallation{ID: 5, Account: "me", AccountType: "User", OwnerUser: f.user.ID, Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRemote(ctx, st.Q(), store.Remote{SpaceID: app.ID, URL: "https://github.com/me/notes.git", Branch: "main", State: "ok", Updated: now,
			CredType: "connection", Provider: "github", Via: "app", InstallationID: 5, RepoID: 1, CredUserID: f.user.ID}); err != nil {
			t.Fatal(err)
		}
		// The person's link at the other provider, and a space that syncs with it.
		ulLink(t, st, f.user, false, now)
		ulRemote(t, st, tok.ID, f.user, now)
		f.m.PolicyChanged()
		f.clk.Advance(90 * 24 * time.Hour)
		// Their link at GitHub goes: the installation's space does not care.
		if _, err := store.PutRepoLink(ctx, st.Q(), store.RepoLink{ID: "LG", ProviderID: store.ProviderGitHub, UserID: f.user.ID, Subject: "g1", Login: "me", AccessEnc: "a", State: store.LinkOK, Connected: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		ids, err := store.DeleteRepoLink(ctx, st.Q(), store.ProviderGitHub, f.user.ID, f.clk.Now())
		if err != nil || len(ids) != 0 {
			t.Fatalf("%v %v", ids, err)
		}
		f.m.AccessLost(ids)
		for name, id := range map[string]string{"installation": app.ID, "link at another provider": tok.ID} {
			if n, err := f.m.Notice(ctx, id); err != nil || n != nil {
				t.Fatalf("%s: %+v %v", name, n, err)
			}
			var lost int64
			var state string
			if err := st.Q().QueryRow(ctx, `SELECT remote_lost_at, remote_state FROM spaces WHERE id = ?`, id).Scan(&lost, &state); err != nil || lost != 0 || state != "ok" {
				t.Fatalf("%s: %d %q %v", name, lost, state, err)
			}
		}
	})
}
