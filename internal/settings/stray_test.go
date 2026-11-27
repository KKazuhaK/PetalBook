package settings_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The stray site settings (sign-in policy, storage policy, machine access,
// tenancy, the daily text's switches) are ordinary keys: a site value, no
// tenant layer where the policy is the server's.
// (signin.ssoOnly is the exception: an organisation may require its own SSO.)
func TestTheStraySettingsAreSiteWideKeys(t *testing.T) {
	t.Parallel()
	s := settings.New(nil)
	for _, k := range []string{
		settings.FeatureTenancy, settings.FeaturePasskeys, settings.SigninMode, settings.SigninPasskeyRoles,
		settings.SigninLockoutEnabled, settings.SigninLockoutMinutes, settings.SigninLockoutScope, settings.SigninPerMinute,
		settings.SigninMaxFailures, settings.SigninWindowMinutes, settings.SessionsIdleDays, settings.SessionsMaxDays,
		settings.CaptchaProvider, settings.CaptchaContexts, settings.StorageRemotesRequire, settings.StorageRemotesScope,
		settings.StorageRemotesGraceDays, settings.StorageLocalMaxBytes, settings.StorageHosts, settings.CachePartial,
		settings.CacheEvictAfterDays, settings.CacheMinIdleHours, settings.CacheClonesBytes, settings.CacheMinFreeBytes,
		settings.CacheAssetsBytes, settings.DailyLanguages,
	} {
		d, ok := s.Def(k)
		if !ok || d.Has(settings.Tenant) || d.ForTenant || d.ServerWide == "" {
			t.Errorf("%s: %+v", k, d)
		}
	}
	// Machine access is the site's and the tenant's: the site's default, and each tenant's own.
	for _, k := range []string{settings.FeatureTokens, settings.FeatureMCP} {
		if d, _ := s.Def(k); d == nil || !d.Has(settings.Tenant) || d.Default != true {
			t.Errorf("%s: %+v", k, d)
		}
	}
	// Sealed values are not settings: nothing here can hold a secret.
	for _, d := range s.Defs() {
		switch d.Key {
		case "mail.password", "captcha.secret", "captcha.siteKey", "github.appKey":
			t.Errorf("%s is a setting", d.Key)
		}
	}
}

func TestOneServiceForAStoreAndTheNumberOfItsSiteValues(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		if settings.Of(st) != settings.Of(st) {
			t.Fatal("two services for one store: a change through one would not reach the other's cache")
		}
		s := settings.Of(st)
		_, n1, err := s.Site(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, n2, _ := s.Site(ctx); n2 != n1 {
			t.Fatal("the number moved with nothing changed")
		}
		if _, err := s.SetSite(ctx, settings.SigninPerMinute, settings.Change{HasValue: true, Value: int64(77)}); err != nil {
			t.Fatal(err)
		}
		m, n3, _ := s.Site(ctx)
		if n3 == n1 || m[settings.SigninPerMinute].Value != int64(77) {
			t.Fatalf("%d %d %v", n1, n3, m[settings.SigninPerMinute].Value)
		}
	})
}

func TestADefaultTheServerGivesIsTheSitesValueUntilItIsSet(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := settings.New(st)
		s.SetDefault(settings.SessionsIdleDays, int64(7))
		if e, _ := s.Get(ctx, "", settings.SessionsIdleDays); e.Value != int64(7) {
			t.Fatalf("%v", e.Value)
		}
		if _, err := s.SetSite(ctx, settings.SessionsIdleDays, settings.Change{HasValue: true, Value: int64(9)}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, "", settings.SessionsIdleDays); e.Value != int64(9) {
			t.Fatalf("%v", e.Value)
		}
		// A default that is not a value of the setting is the program's mistake.
		defer func() {
			if recover() == nil {
				t.Fatal("a default out of range was taken")
			}
		}()
		s.SetDefault(settings.SessionsIdleDays, int64(0))
	})
}

// A binding that only checks and follows (no Get and Set) keeps the value in
// layered_settings, runs its check before anything is written, and its After in
// the transaction of the write, so a failure there writes nothing.
func TestABindingChecksAndFollowsInTheTransactionOfTheWrite(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := settings.New(st)
		var olds, news []any
		failAfter := false
		s.Bind(settings.SigninLockoutMinutes, settings.SiteBinding{
			Check: func(_ context.Context, _ string, v any) (any, error) {
				if v == int64(13) {
					return nil, &settings.Error{Code: settings.CodeInvalid, Msg: "Unlucky."}
				}
				return v, nil
			},
			After: func(ctx context.Context, q store.Q, old, new any) error {
				olds, news = append(olds, old), append(news, new)
				if _, err := store.PutRecord(ctx, q, "followed", "yes", time.Now()); err != nil {
					return err
				}
				if failAfter {
					return errors.New("the follower failed")
				}
				return nil
			},
		})
		put := func(v int64) error {
			_, err := s.SetSite(ctx, settings.SigninLockoutMinutes, settings.Change{HasValue: true, Value: v})
			return err
		}
		if err := put(13); code(err) != settings.CodeInvalid || len(olds) != 0 {
			t.Fatalf("the check did not stop it: %v %v", err, olds)
		}
		if err := put(30); err != nil {
			t.Fatal(err)
		}
		if len(olds) != 1 || olds[0] != int64(15) || news[0] != int64(30) {
			t.Fatalf("old %v new %v", olds, news)
		}
		if raw, _, err := store.GetRecord(ctx, st.Q(), "followed"); err != nil || raw != "yes" {
			t.Fatalf("%q %v", raw, err)
		}
		// A failing follower undoes the write.
		failAfter = true
		if err := put(45); err == nil {
			t.Fatal("the failure was lost")
		}
		if e, _ := s.Get(ctx, "", settings.SigninLockoutMinutes); e.Value != int64(30) {
			t.Fatalf("the write stayed: %v", e.Value)
		}
	})
}

// A value the command line decides is read-only: the flag's value is the one in
// force and a write is a conflict.
func TestABoundValueTheFlagDecidesCannotBeWritten(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := settings.New(st)
		s.Bind(settings.StorageHosts, settings.SiteBinding{
			Get:      func(context.Context, store.Q) (any, error) { return []string{"flag.test"}, nil },
			Set:      func(context.Context, store.Q, any) error { return errors.New("flag") },
			ReadOnly: func() bool { return true },
		})
		e, err := s.Get(ctx, "", settings.StorageHosts)
		if err != nil || !e.Site.FromFlag || e.Value.([]string)[0] != "flag.test" {
			t.Fatalf("%+v %v", e, err)
		}
		if _, err := s.SetSite(ctx, settings.StorageHosts, settings.Change{HasValue: true, Value: []string{"x.test"}}); code(err) != settings.CodeConflict {
			t.Fatalf("%v", err)
		}
	})
}
