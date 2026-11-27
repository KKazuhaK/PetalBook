package store_test

import (
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// A space that loses its GitHub remote gets its grace again from that moment,
// whoever disconnected (an organisation or a person), and an old space that was
// already past its deadline is not read only at once. The clock is a value the
// test passes; nothing waits.
func TestASpaceThatLosesItsGitHubRemoteGetsItsGraceAgain(t *testing.T) {
	t.Parallel()
	for _, who := range []string{"organisation", "person"} {
		t.Run(who, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, st *store.Store) {
				q := st.Q()
				made := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
				day := 24 * time.Hour
				grace := 30 * day
				disconnected := made.Add(90 * 24 * time.Hour) // long past the 30 days
				mkGitHubRow(t, st, made)
				owner, tenant := store.InstallOwner{UserID: "U1"}, store.DefaultTenantID
				if who == "organisation" {
					if err := st.CreateTenant(ctx, store.Tenant{ID: "T1", Slug: "t1", Name: "T1", Created: made}); err != nil {
						t.Fatal(err)
					}
					owner, tenant = store.InstallOwner{TenantID: "T1"}, "T1"
				} else if err := store.InsertUser(ctx, q, store.User{ID: "U1", Username: "u1", Name: "u1", Role: "member", Created: made}); err != nil {
					t.Fatal(err)
				}
				claim := func() {
					t.Helper()
					if err := store.ClaimGitHubInstallation(ctx, q, store.GitHubInstallation{ID: 5, Account: "Acme", AccountType: "Organization",
						OwnerUser: owner.UserID, OwnerTenant: owner.TenantID, Created: made, Updated: made}); err != nil {
						t.Fatal(err)
					}
				}
				claim()
				if err := store.InsertSpace(ctx, q, tenant, "S1", "old", "", "", made); err != nil {
					t.Fatal(err)
				}
				put := func(inst int64) {
					t.Helper()
					r := store.Remote{SpaceID: "S1", URL: "https://github.com/o/s1.git", Branch: "main", State: "ok", Updated: made, CredType: "connection",
						Provider: "github", Via: "app", InstallationID: inst, RepoID: 1, CredUserID: owner.UserID}
					if err := store.PutRemote(ctx, q, r); err != nil {
						t.Fatal(err)
					}
				}
				put(5)
				policy := func(now time.Time) store.RemotePolicy {
					return store.RemotePolicy{Require: "github", Since: made.Add(-time.Hour), Grace: grace, Now: now}
				}
				// The counts, the two lists and the settled usage numbers agree on what
				// the space is at a time.
				at := func(now time.Time, wantGrace, wantOverdue int) {
					t.Helper()
					g, o, _, err := store.PolicyCounts(ctx, q, policy(now))
					if err != nil || g != wantGrace || o != wantOverdue {
						t.Fatalf("at %v: in grace %d, overdue %d, want %d and %d (%v)", now, g, o, wantGrace, wantOverdue, err)
					}
					if _, err := st.SettleDeadlines(ctx, policy(now)); err != nil {
						t.Fatal(err)
					}
					if sum, err := store.GetStorageSummary(ctx, q, store.InTenant(tenant)); err != nil || int(sum.InGrace) != wantGrace || int(sum.Overdue) != wantOverdue {
						t.Fatalf("at %v: settled %d in grace, %d overdue, want %d and %d (%v)", now, sum.InGrace, sum.Overdue, wantGrace, wantOverdue, err)
					}
					for _, c := range []struct {
						which string
						want  int
					}{{"grace", wantGrace}, {"overdue", wantOverdue}} {
						p := policy(now)
						rows, _, err := store.ListAllSpaces(ctx, q, store.AllTenants(), store.SpaceFilter{Deadline: c.which, Policy: &p, Limit: 50})
						if err != nil || len(rows) != c.want {
							t.Fatalf("at %v: the %s list has %d spaces, want %d (%v)", now, c.which, len(rows), c.want, err)
						}
					}
				}
				at(disconnected, 0, 0) // it has its remote
				if _, err := store.DisconnectGitHubInstallation(ctx, q, 5, owner, "lost", disconnected); err != nil {
					t.Fatal(err)
				}
				at(disconnected, 1, 0) // its grace, from the disconnect
				at(disconnected.Add(grace), 1, 0)
				at(disconnected.Add(grace+time.Millisecond), 0, 1)
				// Connected again: it has its remote, whenever.
				claim()
				at(disconnected.Add(10*day), 0, 0)
				// Connected and disconnected again with no successful sync between:
				// no new grace, the first loss counts.
				if _, err := store.DisconnectGitHubInstallation(ctx, q, 5, owner, "lost", disconnected.Add(20*day)); err != nil {
					t.Fatal(err)
				}
				at(disconnected.Add(20*day), 1, 0)
				at(disconnected.Add(grace+time.Millisecond), 0, 1)
				// It syncs well again, and its next loss gets a grace of its own.
				claim()
				if err := store.UpdateRemoteStatus(ctx, q, "S1", store.RoleContent, store.RemoteStatus{State: "ok", LastSync: disconnected.Add(40 * day)}, disconnected.Add(40*day)); err != nil {
					t.Fatal(err)
				}
				if _, err := store.DisconnectGitHubInstallation(ctx, q, 5, owner, "lost", disconnected.Add(50*day)); err != nil {
					t.Fatal(err)
				}
				at(disconnected.Add(50*day), 1, 0)
				at(disconnected.Add(80*day), 1, 0)
				at(disconnected.Add(80*day+time.Millisecond), 0, 1)
			})
		})
	}
}
