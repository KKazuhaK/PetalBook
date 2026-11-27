package quota

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

var ctx = context.Background()

func n(v int64) *int64 { return &v }

func TestOwnLimitsRoundTripAndEmptyIsNone(t *testing.T) {
	if (Own{}).Encode() != "" || ParseOwn("") != (Own{}) || ParseOwn("garbage") != (Own{}) {
		t.Fatal("an empty or unreadable value is no limits")
	}
	o := Own{Spaces: n(5), Pages: n(100)}
	back := ParseOwn(o.Encode())
	if *back.Spaces != 5 || *back.Pages != 100 || back.Bytes != nil {
		t.Fatalf("%+v", back)
	}
}

// A tenant's totals are its quota.* settings: null is no limit.
func TestATenantsTotalsAreItsQuotaSettings(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		set := settings.New(st)
		svc := New(st, set, nil, nil)
		if svc.Totals(ctx, store.DefaultTenantID) != (Tenant{}) {
			t.Fatal("the defaults are no limits")
		}
		put := func(k string, v any) {
			if err := set.SetTenant(ctx, store.DefaultTenantID, k, settings.Change{Site: true, HasSiteRule: true, SiteRule: &settings.Rule{Value: v}}); err != nil {
				t.Fatal(err)
			}
		}
		put(settings.QuotaPeople, int64(10))
		put(settings.QuotaBytes, int64(5))
		tl := svc.Totals(ctx, store.DefaultTenantID)
		if *tl.People != 10 || tl.Spaces != nil || *tl.Bytes != 5 || tl.Pages != nil {
			t.Fatalf("%+v", tl)
		}
	})
}

func TestOverMeansMoreThanTheLimit(t *testing.T) {
	u := store.Usage{Spaces: 3, Bytes: 10, Pages: 5, People: 2}
	if got := OverPerson(u, Person{Spaces: n(3), Bytes: n(9), Pages: nil}); fmt.Sprint(got) != "[bytes]" {
		t.Fatalf("at the limit is not over: %v", got)
	}
	if got := OverTenant(u, Tenant{People: n(1), Spaces: n(3), Bytes: n(10), Pages: n(4)}); fmt.Sprint(got) != "[people pages]" {
		t.Fatalf("%v", got)
	}
	if got := OverPerson(u, Person{}); got != nil {
		t.Fatalf("no limits, %v", got)
	}
}

func newService(t *testing.T, st *store.Store) (*Service, *[]map[string]any, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var rows []map[string]any
	svc := New(st, settings.New(st), func(tenant, user string, detail map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		d := map[string]any{"tenant": tenant, "user": user}
		for k, v := range detail {
			d[k] = v
		}
		rows = append(rows, d)
	}, nil)
	return svc, &rows, &mu
}

func TestLimitsLayerPersonOverTenantOverSite(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		svc, _, _ := newService(t, st)
		l, err := svc.Limits(ctx, store.DefaultTenantID, Own{})
		if err != nil || *l.Spaces != 3 || l.Bytes != nil || l.Pages != nil {
			t.Fatalf("%+v %v", l, err)
		}
		if _, err := svc.set.SetSite(ctx, settings.QuotaBytesPerPerson, settings.Change{HasValue: true, Value: int64(1000)}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.set.SetSite(ctx, settings.QuotaSpacesPerPerson, settings.Change{HasValue: true, Value: int64(0)}); err != nil {
			t.Fatal(err)
		}
		l, _ = svc.Limits(ctx, store.DefaultTenantID, Own{})
		if l.Spaces != nil || *l.Bytes != 1000 { // 0 spaces is no limit
			t.Fatalf("%+v", l)
		}
		l, _ = svc.Limits(ctx, store.DefaultTenantID, Own{Bytes: n(5), Spaces: n(7)})
		if *l.Spaces != 7 || *l.Bytes != 5 {
			t.Fatalf("%+v", l)
		}
	})
}

// A crossing is reported when it happens, not each time it is looked at; and a
// pass over everyone finds the people a changed limit now concerns.
func TestACrossingIsAuditedOnceAndEvaluateScopeFindsEveryone(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		svc, rows, mu := newService(t, st)
		now := time.Now()
		q := st.Q()
		// 600 people (more than a page of 500) each with a space of 100 bytes.
		for i := 0; i < 600; i++ {
			id := fmt.Sprintf("U%04d", i)
			if err := store.InsertUser(ctx, q, store.User{ID: id, Username: id, Name: id, Role: "member", Created: now, TenantID: store.DefaultTenantID}); err != nil {
				t.Fatal(err)
			}
			if i%10 != 0 {
				continue // a tenth of them own a space
			}
			sid := "S" + id
			if err := st.Tx(ctx, func(q store.Q) error {
				if err := store.InsertSpace(ctx, q, store.DefaultTenantID, sid, sid, "", "", now); err != nil {
					return err
				}
				return store.AddMember(ctx, q, sid, id, "owner", now)
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := st.RecordMeasurement(ctx, sid, store.Measurement{Bytes: 100, Pages: 2, HavePages: true, At: now}); err != nil {
				t.Fatal(err)
			}
		}
		count := func() int { mu.Lock(); defer mu.Unlock(); return len(*rows) }
		if err := svc.EvaluateScope(ctx, store.AllTenants()); err != nil || count() != 0 {
			t.Fatalf("%d %v", count(), err)
		}
		if _, err := svc.set.SetSite(ctx, settings.QuotaBytesPerPerson, settings.Change{HasValue: true, Value: int64(50)}); err != nil {
			t.Fatal(err)
		}
		if err := svc.EvaluateScope(ctx, store.AllTenants()); err != nil || count() != 60 {
			t.Fatalf("%d rows for 60 people over: %v", count(), err)
		}
		// Looking again finds nothing new.
		if err := svc.EvaluateScope(ctx, store.AllTenants()); err != nil || count() != 60 {
			t.Fatalf("%d", count())
		}
		if err := svc.Evaluate(ctx, []store.Touched{{UserID: "U0000", TenantID: store.DefaultTenantID}}); err != nil || count() != 60 {
			t.Fatalf("%d", count())
		}
		d := (*rows)[0]
		if d["quota"] != "bytes" || d["limit"] != int64(50) || d["used"] != int64(100) || d["scope"] != "person" || d["tenant"] != store.DefaultTenantID || d["user"] == "" {
			t.Fatalf("%v", d)
		}
		// One person gets a limit of their own above it: no longer over, and over again later is new.
		var own = Own{Bytes: n(500)}.Encode()
		if err := store.SetUserQuota(ctx, q, store.AllTenants(), "U0000", own); err != nil {
			t.Fatal(err)
		}
		if err := svc.Evaluate(ctx, []store.Touched{{UserID: "U0000"}}); err != nil {
			t.Fatal(err)
		}
		if u, _ := store.GetUsage(ctx, q, store.UsageUser, "U0000"); len(u.Over) != 0 {
			t.Fatalf("%v", u.Over)
		}
		if err := store.SetUserQuota(ctx, q, store.AllTenants(), "U0000", Own{Bytes: n(10)}.Encode()); err != nil {
			t.Fatal(err)
		}
		if err := svc.Evaluate(ctx, []store.Touched{{UserID: "U0000"}}); err != nil || count() != 61 {
			t.Fatalf("%d %v", count(), err)
		}
	})
}

// The limits the space manager asks for are the person's and the tenant's.
func TestSpaceLimitsAreThePersonsAndTheTenants(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		svc, _, _ := newService(t, st)
		u := store.User{ID: "U1", Username: "one", Name: "one", Role: "member", Created: time.Now(), TenantID: store.DefaultTenantID}
		if err := store.InsertUser(ctx, st.Q(), u); err != nil {
			t.Fatal(err)
		}
		if got, err := svc.SpaceLimits(ctx, u); err != nil || got != (spaces.SpaceLimits{Person: 3}) {
			t.Fatalf("%+v %v", got, err)
		}
		if err := store.SetUserQuota(ctx, st.Q(), store.AllTenants(), "U1", Own{Spaces: n(4)}.Encode()); err != nil {
			t.Fatal(err)
		}
		if err := svc.set.SetTenant(ctx, store.DefaultTenantID, settings.QuotaSpaces, settings.Change{Site: true, HasSiteRule: true, SiteRule: &settings.Rule{Value: int64(9)}}); err != nil {
			t.Fatal(err)
		}
		if got, err := svc.SpaceLimits(ctx, u); err != nil || got != (spaces.SpaceLimits{Person: 4, Tenant: 9}) {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

// What looking at everyone costs, after a limit changed or the daily recount:
// the usage rows read in pages of 500, and a write for each person whose state
// changed. It prints the timings (the design notes quote them): PETALBOOK_PERF=1.
func TestEvaluatingEveryoneCosts(t *testing.T) {
	if os.Getenv("PETALBOOK_PERF") == "" {
		t.Skip("set PETALBOOK_PERF=1 to measure")
	}
	const people = 20000
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		svc, rows, mu := newService(t, st)
		now := time.Now().UnixMilli()
		if err := st.Tx(ctx, func(q store.Q) error {
			for i := 0; i < people; i++ {
				id := fmt.Sprintf("U%07d", i)
				if _, err := q.Exec(ctx, `INSERT INTO usage(kind, id, tenant_id, spaces, bytes, pages) VALUES ('user', ?, ?, 2, ?, 100)`, id, store.DefaultTenantID, 1_000_000+i); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		_ = now
		time1 := func(name string, f func()) {
			start := time.Now()
			f()
			t.Logf("PERF %-62s %10s", name, time.Since(start).Round(time.Millisecond))
		}
		time1(fmt.Sprintf("look at %d people, nobody over a limit", people), func() {
			if err := svc.EvaluateScope(ctx, store.AllTenants()); err != nil {
				t.Fatal(err)
			}
		})
		if _, err := svc.set.SetSite(ctx, settings.QuotaBytesPerPerson, settings.Change{HasValue: true, Value: int64(1_010_000)}); err != nil {
			t.Fatal(err)
		}
		time1(fmt.Sprintf("a limit that 10,000 of %d people are over: state stored, audit written", people), func() {
			if err := svc.EvaluateScope(ctx, store.AllTenants()); err != nil {
				t.Fatal(err)
			}
		})
		mu.Lock()
		t.Logf("PERF   %d crossings reported", len(*rows))
		mu.Unlock()
		time1("the same look again: nothing changed, nothing written", func() {
			if err := svc.EvaluateScope(ctx, store.AllTenants()); err != nil {
				t.Fatal(err)
			}
		})
	})
}
