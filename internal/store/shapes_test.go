package store_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// A rule's note is written and read back with the rule, and the table refuses
// one over 200 characters (counted in characters, not bytes).
func TestLayeredNoteRoundTrip(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.UnixMilli(1_700_000_000_000)
		row := store.LayeredRow{Layer: store.LayeredSite, LayerID: tenA, Key: "quota.spaces", Value: "3", Lower: `{"mode":"lock"}`, SetBy: "U1", Updated: now,
			Note: "每个空间都必须同步到远程仓库"}
		if err := store.PutLayered(ctx, q, row); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetLayered(ctx, q, store.LayeredSite, tenA, "quota.spaces", store.SourceManual)
		if err != nil || got.Note != row.Note {
			t.Fatalf("%+v %v", got, err)
		}
		// A rewrite carries the note it is given; a tenant's own value keeps its rule's note.
		row.Note = ""
		if err := store.PutLayered(ctx, q, row); err != nil {
			t.Fatal(err)
		}
		if got, _ = store.GetLayered(ctx, q, store.LayeredSite, tenA, "quota.spaces", store.SourceManual); got.Note != "" {
			t.Fatalf("note kept: %q", got.Note)
		}
		own := store.LayeredRow{Layer: store.LayeredTenant, LayerID: tenA, Key: "files.maxBytes", Value: "5", Lower: `{"mode":"allow"}`, Updated: now, Note: "n1"}
		if err := store.PutLayeredFields(ctx, q, own, true, true); err != nil {
			t.Fatal(err)
		}
		own.Value, own.Note = "6", "ignored"
		if err := store.PutLayeredFields(ctx, q, own, true, false); err != nil {
			t.Fatal(err)
		}
		if got, _ = store.GetLayered(ctx, q, store.LayeredTenant, tenA, "files.maxBytes", store.SourceManual); got.Value != "6" || got.Note != "n1" {
			t.Fatalf("%+v", got)
		}
		// 200 characters pass, 201 do not.
		row.Note = strings.Repeat("字", store.MaxLayeredNote)
		if err := store.PutLayered(ctx, q, row); err != nil {
			t.Fatal(err)
		}
		row.Note += "字"
		if err := store.PutLayered(ctx, q, row); err == nil {
			t.Fatal("a 201-character note was accepted")
		}
	})
}

// A tenant's integration records: one per kind and id, replaced by a put, gone
// with the tenant.
func TestTenantIntegrationsRoundTrip(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.UnixMilli(1_700_000_000_000)
		const ten = "T000000007"
		if err := st.CreateTenant(ctx, store.Tenant{ID: ten, Slug: "globex", Name: "Globex", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		put := func(kind, id, cfg string) {
			t.Helper()
			if err := store.PutIntegration(ctx, q, store.IntegrationRow{TenantID: ten, Kind: kind, ID: id, Config: cfg, UpdatedBy: "U1", Updated: now}); err != nil {
				t.Fatal(err)
			}
		}
		put(store.IntegrationSMTP, "", `{"host":"smtp.example.test"}`)
		put(store.IntegrationSMTP, "", `{"host":"smtp2.example.test"}`)
		put(store.IntegrationGitLab, "a", `{}`)
		put(store.IntegrationGitLab, "b", "")
		r, err := store.GetIntegration(ctx, q, ten, store.IntegrationSMTP, "")
		if err != nil || r.Config != `{"host":"smtp2.example.test"}` || r.UpdatedBy != "U1" || !r.Updated.Equal(now) {
			t.Fatalf("%+v %v", r, err)
		}
		if _, err := store.GetIntegration(ctx, q, ten, store.IntegrationCaptcha, ""); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if l, err := store.ListIntegrations(ctx, q, ten, ""); err != nil || len(l) != 3 {
			t.Fatalf("%+v %v", l, err)
		}
		if l, err := store.ListIntegrations(ctx, q, ten, store.IntegrationGitLab); err != nil || len(l) != 2 || l[0].ID != "a" || l[1].Config != "{}" {
			t.Fatalf("%+v %v", l, err)
		}
		if err := store.DeleteIntegration(ctx, q, ten, store.IntegrationSMTP, ""); err != nil {
			t.Fatal(err)
		}
		if err := store.DeleteIntegration(ctx, q, ten, store.IntegrationSMTP, ""); err != nil {
			t.Fatalf("deleting none: %v", err)
		}
		if err := store.PutIntegration(ctx, q, store.IntegrationRow{TenantID: "T999999999", Kind: "smtp", Updated: now}); err == nil {
			t.Fatal("a record of no tenant was accepted")
		}
		if err := st.DeleteTenant(ctx, ten); err != nil {
			t.Fatal(err)
		}
		if l, _ := store.ListIntegrations(ctx, q, ten, ""); len(l) != 0 {
			t.Fatalf("left behind: %+v", l)
		}
	})
}

// The daily usage rows: adds count even when many run at once, a set replaces,
// and the list is in export order within a range of days.
func TestTenantUsageDaily(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.UnixMilli(1_700_000_000_000)
		const ten = "T000000007"
		if err := st.CreateTenant(ctx, store.Tenant{ID: ten, Slug: "globex", Name: "Globex", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		day := store.UsageDay(now)
		if day != "2023-11-14" {
			t.Fatalf("day %q", day)
		}
		const workers, each = 8, 25
		var wg sync.WaitGroup
		errs := make(chan error, workers)
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range each {
					if err := store.AddUsageDaily(ctx, q, ten, day, store.UsageKeyAPIRequests, 2); err != nil {
						errs <- err
						return
					}
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(store.AddUsageDaily(ctx, q, ten, day, store.UsageKeyMail, 3))
		must(store.SetUsageDaily(ctx, q, ten, day, store.UsageKeyBytes, 10))
		must(store.SetUsageDaily(ctx, q, ten, day, store.UsageKeyBytes, 12))
		must(store.SetUsageDaily(ctx, q, ten, "2023-11-13", store.UsageKeyPeople, 4))
		must(store.AddUsageDaily(ctx, q, ten, "2023-11-16", store.UsageKeyMCPRequests, 1))
		rows, err := store.ListUsageDaily(ctx, q, ten, "2023-11-13", "2023-11-15")
		if err != nil {
			t.Fatal(err)
		}
		want := []store.UsageDailyRow{
			{ten, "2023-11-13", "people", 4},
			{ten, "2023-11-14", "apiRequests", workers * each * 2},
			{ten, "2023-11-14", "bytes", 12},
			{ten, "2023-11-14", "mail", 3},
		}
		if len(rows) != len(want) {
			t.Fatalf("%+v", rows)
		}
		for i := range want {
			if rows[i] != want[i] {
				t.Fatalf("row %d: %+v, want %+v", i, rows[i], want[i])
			}
		}
		if err := store.AddUsageDaily(ctx, q, "T999999999", day, "mail", 1); err == nil {
			t.Fatal("usage of no tenant was accepted")
		}
		must(st.DeleteTenant(ctx, ten))
		if rows, _ = store.ListUsageDaily(ctx, q, ten, "2000-01-01", "2100-01-01"); len(rows) != 0 {
			t.Fatalf("left behind: %+v", rows)
		}
	})
}

// A storage target keeps its state, its last error and who it is shared with;
// a tenant's go when it does.
func TestStorageTargetRoundTrip(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.UnixMilli(1_700_000_000_000)
		const ten, other = "T000000007", "T000000008"
		for _, id := range []string{ten, other} {
			if err := st.CreateTenant(ctx, store.Tenant{ID: id, Slug: "t" + id, Name: id, Created: now, Updated: now}); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.InsertConnProviderRow(ctx, q, store.ConnProviderRow{ID: "gl", Type: "gitlab", Host: "gitlab.example.test", Scheme: "https", Name: "GL", Owner: store.ConnProviderSite, Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		tg := store.StorageTarget{ID: "TG1", Owner: ten, ProviderID: "gl", Namespace: "notes/petalbook", Prefix: "pb-", InUse: true, UpdatedBy: "U1", Created: now, Updated: now}
		if err := store.PutStorageTarget(ctx, q, tg); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetStorageTarget(ctx, q, "TG1")
		if err != nil || got.State != store.TargetChecking || got.StateError != "" || !got.CheckedAt.IsZero() || !got.InUse || got.Prefix != "pb-" || got.Owner != ten {
			t.Fatalf("%+v %v", got, err)
		}
		later := now.Add(time.Minute)
		if err := store.SetStorageTargetState(ctx, q, "TG1", store.TargetBroken, "namespace not found", later); err != nil {
			t.Fatal(err)
		}
		got, _ = store.GetStorageTarget(ctx, q, "TG1")
		if got.State != store.TargetBroken || got.StateError != "namespace not found" || !got.CheckedAt.Equal(later) {
			t.Fatalf("%+v", got)
		}
		if err := store.SetStorageTargetState(ctx, q, "nope", store.TargetOK, "", later); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if err := store.SetStorageTargetState(ctx, q, "TG1", "unknown", "", later); err == nil {
			t.Fatal("an unknown state was accepted")
		}
		// A site target offered to two tenants, then to one.
		site := store.StorageTarget{ID: "TG2", Owner: "site", ProviderID: "gl", Namespace: "shared", App: "", Created: now, Updated: now}
		if err := store.PutStorageTarget(ctx, q, site); err != nil {
			t.Fatal(err)
		}
		if err := store.SetStorageTargetShares(ctx, q, "TG2", []string{ten, other, ten}); err != nil {
			t.Fatal(err)
		}
		if got, _ = store.GetStorageTarget(ctx, q, "TG2"); len(got.SharedWith) != 2 {
			t.Fatalf("%+v", got)
		}
		if err := store.SetStorageTargetShares(ctx, q, "TG2", []string{other}); err != nil {
			t.Fatal(err)
		}
		if l, err := store.ListSharedStorageTargets(ctx, q, other); err != nil || len(l) != 1 || l[0].ID != "TG2" {
			t.Fatalf("%+v %v", l, err)
		}
		if l, _ := store.ListSharedStorageTargets(ctx, q, ten); len(l) != 0 {
			t.Fatalf("%+v", l)
		}
		if l, err := store.ListStorageTargets(ctx, q, ten); err != nil || len(l) != 1 || l[0].ID != "TG1" {
			t.Fatalf("%+v %v", l, err)
		}
		if err := store.SetStorageTargetShares(ctx, q, "TG2", []string{"T999999999"}); err == nil {
			t.Fatal("sharing with no tenant was accepted")
		}
		// The tenant's target goes with the tenant; the site's stays, minus the sharing.
		if err := st.DeleteTenant(ctx, ten); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetStorageTarget(ctx, q, "TG1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if err := st.DeleteTenant(ctx, other); err != nil {
			t.Fatal(err)
		}
		if got, err = store.GetStorageTarget(ctx, q, "TG2"); err != nil || len(got.SharedWith) != 0 {
			t.Fatalf("%+v %v", got, err)
		}
		if err := store.DeleteStorageTarget(ctx, q, "TG2"); err != nil {
			t.Fatal(err)
		}
		if err := store.DeleteStorageTarget(ctx, q, "TG2"); err != nil {
			t.Fatal(err)
		}
	})
}
