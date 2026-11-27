package github_test

import (
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// A space that loses its GitHub remote, however the installation goes (the
// organisation disconnects it, GitHub says it was uninstalled, a person
// disconnects their own), gets the storage requirement's grace period again from
// that moment, even when it was long past its deadline. The clock is the test's.
func TestASpaceThatLosesItsInstallationGetsItsGraceAgain(t *testing.T) {
	grace := 30 * 24 * time.Hour
	for _, how := range []string{"organisation", "uninstalled", "person"} {
		t.Run(how, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, st *store.Store) {
				o := newOrg(t, st)
				var id int64
				var owner store.InstallOwner
				space := "S1"
				if how == "person" {
					o.f.AddRepo("tang", "notes", true)
					id, owner = o.installMine("U1", "tang"), store.InstallOwner{UserID: "U1"}
					o.spaceIn(store.DefaultTenantID, space, "U1", store.Remote{Via: "app", InstallationID: id, RepoID: 1, CredUserID: "U1"})
				} else {
					id, owner = o.installOrg("A1", "T1", "petal-team", "kaz-admin"), store.InstallOwner{TenantID: "T1"}
					o.spaceIn("T1", space, "M1", store.Remote{Via: "app", InstallationID: id, RepoID: o.f.RepoByName("petal-team/docs").ID})
				}
				since := o.now.Add(-time.Hour)
				counts := func() (int, int) {
					t.Helper()
					g, ov, _, err := store.PolicyCounts(ctx, st.Q(), store.RemotePolicy{Require: "github", Since: since, Grace: grace, Now: o.clk.Now()})
					if err != nil {
						t.Fatal(err)
					}
					return g, ov
				}
				o.clk.Advance(90 * 24 * time.Hour) // the space is far past its first 30 days
				if g, ov := counts(); g != 0 || ov != 0 {
					t.Fatalf("with its remote: %d in grace, %d overdue", g, ov)
				}
				switch how {
				case "uninstalled":
					o.f.Uninstall(id, "kaz-admin")
					o.deliver()
				default:
					if _, err := o.svc.DisconnectInstallation(ctx, owner, id); err != nil {
						t.Fatal(err)
					}
				}
				if g, ov := counts(); g != 1 || ov != 0 {
					t.Fatalf("just after the loss: %d in grace, %d overdue, want 1 and 0", g, ov)
				}
				o.clk.Advance(grace)
				if g, ov := counts(); g != 1 || ov != 0 {
					t.Fatalf("at the end of the grace: %d in grace, %d overdue, want 1 and 0", g, ov)
				}
				o.clk.Advance(time.Second)
				if g, ov := counts(); g != 0 || ov != 1 {
					t.Fatalf("after the grace: %d in grace, %d overdue, want 0 and 1", g, ov)
				}
			})
		})
	}
}
