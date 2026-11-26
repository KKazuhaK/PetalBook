package settings_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// ForceSite is the rescue write ("petalbook security reset"): several site
// values in one transaction, past the checks that look at other values, and
// counted in settings_rev.
func TestForceSiteWritesAllOrNothingPastTheChecks(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := settings.New(st)
		s.Bind(settings.SigninSSOOnly, settings.SiteBinding{Check: func(context.Context, string, any) (any, error) {
			return nil, errors.New("refused by a check")
		}})
		if _, err := s.SetSite(ctx, settings.SigninMode, settings.Change{HasValue: true, Value: "local_only"}); err != nil {
			t.Fatal(err)
		}
		rev, _ := store.SettingsRev(ctx, st.Q())
		changed, err := s.ForceSite(ctx, "cli", []settings.KeyValue{
			{Key: settings.SigninSSOOnly, Value: false}, // already false: not a change
			{Key: settings.SigninMode, Value: "dual"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(changed) != 1 || changed[0].Key != settings.SigninMode || changed[0].From != "local_only" || changed[0].To != "dual" {
			t.Fatalf("%+v", changed)
		}
		if e, _ := s.Get(ctx, "", settings.SigninMode); e.Value != "dual" {
			t.Fatal(e.Value)
		}
		if r, _ := store.SettingsRev(ctx, st.Q()); r <= rev {
			t.Fatal("settings_rev did not move: a running server would not see the reset")
		}
		// A failure on the second key leaves the first unwritten.
		s.Bind(settings.SigninLockoutEnabled, settings.SiteBinding{After: func(context.Context, store.Q, any, any) error {
			return errors.New("fails mid-way")
		}})
		if _, err := s.SetSite(ctx, settings.SigninMode, settings.Change{HasValue: true, Value: "sso_first"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ForceSite(ctx, "cli", []settings.KeyValue{
			{Key: settings.SigninMode, Value: "dual"},
			{Key: settings.SigninLockoutEnabled, Value: true},
		}); err == nil {
			t.Fatal("no error")
		}
		if e, _ := s.Get(ctx, "", settings.SigninMode); e.Value != "sso_first" {
			t.Fatalf("half a reset was written: %v", e.Value)
		}
		// A value of the wrong shape is refused before anything is written.
		if _, err := s.ForceSite(ctx, "cli", []settings.KeyValue{{Key: settings.SigninMode, Value: "nonsense"}}); err == nil {
			t.Fatal("a bad value was forced")
		}
	})
}

// Bindings that share a mutex have their check and write serialised: two
// changes that are each fine alone cannot both pass a check that the other
// would fail.
func TestBindingsSharingALockAreCheckedOneAtATime(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := settings.New(st)
		var mu sync.Mutex
		// Each check waits for the other to have started (or, serialised, for
		// a short while): unserialised, both read the other's old value.
		var entered atomic.Int32
		both := make(chan struct{})
		// At most one of mode=local_only and ssoOnly=true may hold.
		other := func(key string, bad any) func(ctx context.Context, _ string, v any) (any, error) {
			return func(ctx context.Context, _ string, v any) (any, error) {
				if entered.Add(1) == 2 {
					close(both)
				}
				select {
				case <-both:
				case <-time.After(100 * time.Millisecond):
				}
				e, err := s.Get(ctx, "", key)
				if err != nil {
					return nil, err
				}
				if e.Value == bad {
					return nil, errors.New("both")
				}
				return v, nil
			}
		}
		s.Bind(settings.SigninMode, settings.SiteBinding{Lock: &mu, Check: other(settings.SigninSSOOnly, true)})
		s.Bind(settings.SigninSSOOnly, settings.SiteBinding{Lock: &mu, Check: other(settings.SigninMode, "local_only")})
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, errs[0] = s.SetSite(ctx, settings.SigninMode, settings.Change{HasValue: true, Value: "local_only"})
		}()
		go func() {
			defer wg.Done()
			_, errs[1] = s.SetSite(ctx, settings.SigninSSOOnly, settings.Change{HasValue: true, Value: true})
		}()
		wg.Wait()
		if errs[0] == nil && errs[1] == nil {
			t.Fatal("both writes passed: the checks were not serialised")
		}
		if errs[0] != nil && errs[1] != nil {
			t.Fatalf("both refused: %v %v", errs[0], errs[1])
		}
	})
}
