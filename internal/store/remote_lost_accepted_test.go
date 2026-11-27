package store_test

import (
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// Losing a remote starts a grace only when the remote was one the requirement
// accepts: a space that is overdue because its remote is on another provider
// than the required one stays overdue when that remote is lost (else
// disconnecting a remote that does not count would restart the grace, again and
// again). The clock is a value the test passes.
func TestALossOfARemoteTheRequirementDoesNotAcceptKeepsTheSpaceOverdue(t *testing.T) {
	t.Parallel()
	day := 24 * time.Hour
	made := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	now := made.Add(90 * day)
	for _, c := range []struct {
		name    string
		require string // of the policy and of the loss
		lose    func(t *testing.T, st *store.Store, require string, at time.Time)
		remote  func(t *testing.T, st *store.Store) // the space's remote, on the other provider
	}{
		{"a person's link at another provider", "github",
			func(t *testing.T, st *store.Store, require string, at time.Time) {
				if _, err := store.DeleteRepoLink(ctx, st.Q(), "gt", "U1", require, at); err != nil {
					t.Fatal(err)
				}
			},
			func(t *testing.T, st *store.Store) {
				mkForge(t, st, "gt")
				if _, err := store.PutRepoLink(ctx, st.Q(), repoLink("L1", "gt", "U1", "7", time.Time{})); err != nil {
					t.Fatal(err)
				}
				if err := store.PutRemote(ctx, st.Q(), store.Remote{SpaceID: "S1", URL: "https://gt.example.com/o/s1.git", Branch: "main", State: "ok", Updated: made,
					CredType: "connection", Provider: "gt", Via: "user", RepoID: 1, CredUserID: "U1"}); err != nil {
					t.Fatal(err)
				}
			}},
		{"an installation, with another provider required", "gt",
			func(t *testing.T, st *store.Store, require string, at time.Time) {
				if _, err := store.DisconnectGitHubInstallation(ctx, st.Q(), 5, store.InstallOwner{UserID: "U1"}, "lost", require, at); err != nil {
					t.Fatal(err)
				}
			},
			func(t *testing.T, st *store.Store) {
				mkGitHubRow(t, st, made)
				mkForge(t, st, "gt")
				if err := store.ClaimGitHubInstallation(ctx, st.Q(), store.GitHubInstallation{ID: 5, Account: "Acme", AccountType: "User", OwnerUser: "U1", Created: made, Updated: made}); err != nil {
					t.Fatal(err)
				}
				if err := store.PutRemote(ctx, st.Q(), store.Remote{SpaceID: "S1", URL: "https://github.com/o/s1.git", Branch: "main", State: "ok", Updated: made,
					CredType: "connection", Provider: "github", Via: "app", InstallationID: 5, RepoID: 1, CredUserID: "U1"}); err != nil {
					t.Fatal(err)
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			storetest.Each(t, func(t *testing.T, st *store.Store) {
				mkUser(t, st, "U1", "u1", "member")
				if err := store.InsertSpace(ctx, st.Q(), store.DefaultTenantID, "S1", "old", "", "", made); err != nil {
					t.Fatal(err)
				}
				c.remote(t, st)
				p := store.RemotePolicy{Require: c.require, Since: made.Add(-time.Hour), Grace: 30 * day, Now: now}
				// The remote is not one the requirement accepts, so the space is
				// past its deadline with it and without it.
				counts := func(wantGrace, wantOverdue int) {
					t.Helper()
					g, o, _, err := store.PolicyCounts(ctx, st.Q(), p)
					if err != nil || g != wantGrace || o != wantOverdue {
						t.Fatalf("in grace %d, overdue %d, want %d and %d (%v)", g, o, wantGrace, wantOverdue, err)
					}
				}
				counts(0, 1)
				c.lose(t, st, c.require, now)
				counts(0, 1) // still overdue: the loss gave it no new grace
				var lost int64
				if err := st.Q().QueryRow(ctx, `SELECT remote_lost_at FROM spaces WHERE id = 'S1'`).Scan(&lost); err != nil || lost != 0 {
					t.Fatalf("remote_lost_at is %d: %v", lost, err)
				}
			})
		})
	}
}
