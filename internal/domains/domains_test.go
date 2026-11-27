package domains_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/domains"
	"petalbook.app/server/internal/domains/domainstest"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

func TestKeyIsTheIDNAFormInLowerCase(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"Example.COM":            "example.com",
		"example.com.":           "example.com",
		" bücher.example ":       "xn--bcher-kva.example",
		"BÜCHER.example":         "xn--bcher-kva.example",
		"xn--bcher-kva.EXAMPLE":  "xn--bcher-kva.example",
		"ＥＸＡＭＰＬＥ．ＣＯＭ":            "example.com", // full-width letters and dot are mapped
		"straße.example":         "xn--strae-oqa.example",
		"例え.テスト":                 "xn--r8jz45g.xn--zckzah",
		"mail.sub.example.co.uk": "mail.sub.example.co.uk",
	} {
		if got, ok := domains.Key(in); !ok || got != want {
			t.Errorf("Key(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", ".", "localhost", "example", "1.2.3.4", "a..example", "-a.example", "a-.example", "a_b.example",
		"exa mple.com", "user@example.com", "https://example.com", "example.com/x", "xn--.example", "xn--bcher-kvb.example"} {
		if got, ok := domains.Key(in); ok {
			t.Errorf("Key(%q) = %q: not a domain one may claim", in, got)
		}
	}
	if d := domains.Display("xn--bcher-kva.example"); d != "bücher.example" {
		t.Errorf("Display: %q", d)
	}
	if d := domains.EmailDomain("Ann@BÜCHER.example"); d != "xn--bcher-kva.example" {
		t.Errorf("EmailDomain: %q", d)
	}
	if !domains.Public("gmail.com") || !domains.Public("qq.com") || domains.Public("example.com") {
		t.Error("Public")
	}
}

// fakeTimer is the recheck's clock: each After the loop asks for is sent to
// asked, and fire makes the latest one go off.
type fakeTimer struct {
	asked chan time.Duration
	fire  chan time.Time
}

func (f *fakeTimer) After(d time.Duration) (<-chan time.Time, func()) {
	f.asked <- d
	return f.fire, func() {}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// verified stores a verified claim whose recheck is due at next, with its
// record published.
func verified(t *testing.T, st *store.Store, dns *domainstest.Fake, tenant, domain string, now, next time.Time) {
	t.Helper()
	ctx := context.Background()
	code := domains.NewCode()
	if err := store.InsertDomain(ctx, st.Q(), store.DomainRow{TenantID: tenant, Domain: domain, CodeHash: domains.HashCode(code), CodeExpires: now.Add(time.Hour), Created: now, Updated: now}); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.VerifyDomain(ctx, st.Q(), tenant, domain, "", now, next); err != nil || !ok {
		t.Fatal(ok, err)
	}
	dns.SetTXT(domains.RecordName(domain), domains.TXTValue(code))
}

func TestTheRecheckTimerSleepsUntilTheNextDueDomain(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		clk := &fakeClock{t: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)}
		now := clk.Now()
		if err := st.CreateTenant(ctx, store.Tenant{ID: "T000000002", Slug: "acme", Name: "Acme", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		dns := domainstest.New()
		verified(t, st, dns, "T000000002", "five.example", now, now.Add(5*time.Hour))
		verified(t, st, dns, "T000000002", "two.example", now, now.Add(2*time.Hour))
		verified(t, st, dns, store.DefaultTenantID, "nine.example", now, now.Add(9*time.Hour))
		tm := &fakeTimer{asked: make(chan time.Duration), fire: make(chan time.Time)}
		svc := domains.New(domains.Options{Store: st, Resolver: dns, Now: clk.Now, After: tm.After, SiteHost: "notes.example.test"})
		if err := svc.Load(ctx); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { svc.Run(ctx); close(done) }()

		// It sleeps until the soonest, having asked DNS nothing.
		if d := <-tm.asked; d != 2*time.Hour {
			t.Fatalf("the first sleep: %v", d)
		}
		if dns.Asked("_petalbook.two.example")+dns.Asked("_petalbook.five.example")+dns.Asked("_petalbook.nine.example") != 0 {
			t.Fatal("asked DNS before anything was due")
		}
		// It wakes then, rechecks that one only, and sleeps until the next.
		clk.Advance(2 * time.Hour)
		tm.fire <- clk.Now()
		if d := <-tm.asked; d != 3*time.Hour {
			t.Fatalf("the second sleep: %v", d)
		}
		if dns.Asked("_petalbook.two.example") != 1 || dns.Asked("_petalbook.five.example") != 0 {
			t.Fatal("rechecked what was not due")
		}
		// A change wakes it: a domain due sooner is slept for.
		verified(t, st, dns, "T000000002", "soon.example", clk.Now(), clk.Now().Add(time.Minute))
		svc.Changed(ctx)
		if d := <-tm.asked; d != time.Minute {
			t.Fatalf("after the change: %v", d)
		}
		// Two due at once are both rechecked in one pass.
		clk.Advance(5 * time.Hour)
		tm.fire <- clk.Now()
		if d := <-tm.asked; d != 2*time.Hour {
			t.Fatalf("after the pass: %v", d) // nine.example, due at 9h; it is 7h
		}
		if dns.Asked("_petalbook.soon.example") != 1 || dns.Asked("_petalbook.five.example") != 1 || dns.Asked("_petalbook.two.example") != 1 {
			t.Fatal("the due ones are rechecked once each")
		}
		if svc.Count() != 4 {
			t.Fatalf("the map: %d", svc.Count())
		}
		cancel()
		<-done
	})
}
