package settings_test

import (
	"testing"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

func note(mode, text string) *settings.Lower {
	return &settings.Lower{Mode: mode, Note: text, NoteSet: true}
}

// Every kind has its own order of strictness; a key with none cannot be
// tightened.
func TestStrictnessOfEachKind(t *testing.T) {
	boolHigh := &settings.Def{Kind: settings.Bool, Strict: settings.StrictHigher}
	boolLow := &settings.Def{Kind: settings.Bool, Strict: settings.StrictLower}
	lim := &settings.Def{Kind: settings.Limit, Strict: settings.StrictLower}
	limHigh := &settings.Def{Kind: settings.Limit, Strict: settings.StrictHigher}
	ch := &settings.Def{Kind: settings.Choice, Strict: settings.StrictOrder, Choices: []string{"a", "b", "c"}}
	li := &settings.Def{Kind: settings.List, Strict: settings.StrictSubset}
	none := &settings.Def{Kind: settings.Limit}
	for name, c := range map[string]struct {
		d        *settings.Def
		v, above any
		looser   bool
	}{
		"true under true, higher":    {boolHigh, true, true, false},
		"false under true, higher":   {boolHigh, false, true, true},
		"true under false, higher":   {boolHigh, true, false, false},
		"true under false, lower":    {boolLow, true, false, true},
		"false under true, lower":    {boolLow, false, true, false},
		"more under 10, lower":       {lim, int64(11), int64(10), true},
		"equal":                      {lim, int64(10), int64(10), false},
		"fewer under 10":             {lim, int64(3), int64(10), false},
		"none under 10":              {lim, nil, int64(10), true},
		"10 under none":              {lim, int64(10), nil, false},
		"none under none":            {lim, nil, nil, false},
		"fewer, higher is stricter":  {limHigh, int64(3), int64(10), true},
		"more, higher is stricter":   {limHigh, int64(30), int64(10), false},
		"earlier choice":             {ch, "a", "b", true},
		"later choice":               {ch, "c", "b", false},
		"same choice":                {ch, "b", "b", false},
		"subset":                     {li, []string{"x"}, []string{"x", "y"}, false},
		"an entry that is not above": {li, []string{"x", "z"}, []string{"x", "y"}, true},
		"everything under some":      {li, []string{"*"}, []string{"x"}, true},
		"some under everything":      {li, []string{"x"}, []string{"*"}, false},
		"a key with no order":        {none, int64(99), int64(1), false},
	} {
		if got := settings.Looser(c.d, c.v, c.above); got != c.looser {
			t.Errorf("%s: %v", name, got)
		}
	}
	// A key without an order refuses the mode.
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		if _, err := s.SetSite(ctx, settings.DailyOwn, settings.Change{HasLower: true, Lower: lower(settings.Tighten)}); code(err) != settings.CodeInvalid {
			t.Fatalf("a key with no order: %v", err)
		}
	})
}

// The site (or its rule for one tenant) sets a minimum: a tenant may be as
// strict or stricter; the tenant's value in effect is never looser.
func TestTightenHoldsATenantToTheSitesValue(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		n, err := s.SetSite(ctx, settings.RegistrationPerHour, settings.Change{HasValue: true, Value: int64(20), HasLower: true,
			Lower: note(settings.Tighten, "Company policy: at most 20 an hour.")})
		if err != nil || n != 0 {
			t.Fatalf("%d %v", n, err)
		}
		for _, v := range []int64{21, 50} {
			if err := s.SetTenant(ctx, ten, settings.RegistrationPerHour, settings.Change{HasValue: true, Value: v}); code(err) != settings.CodeRestricted {
				t.Fatalf("%d: %v", v, err)
			}
		}
		for _, v := range []int64{20, 5} {
			if err := s.SetTenant(ctx, ten, settings.RegistrationPerHour, settings.Change{HasValue: true, Value: v}); err != nil {
				t.Fatalf("%d: %v", v, err)
			}
			if e, _ := s.Get(ctx, ten, settings.RegistrationPerHour); e.Value != v || e.Source != settings.Tenant || !e.Tenant.InEffect || e.Floor != int64(20) {
				t.Fatalf("%d: %+v %+v", v, e, e.Tenant)
			}
		}
		// The site becomes stricter than the tenant's value: the tenant's looser
		// value is kept and not in effect, the site's applies.
		if err := s.SetTenant(ctx, ten, settings.RegistrationPerHour, settings.Change{HasValue: true, Value: int64(15)}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetSite(ctx, settings.RegistrationPerHour, settings.Change{HasValue: true, Value: int64(10)}); err != nil {
			t.Fatal(err)
		}
		e, _ := s.Get(ctx, ten, settings.RegistrationPerHour)
		if e.Value != int64(10) || e.Source != settings.Site || e.Tenant.InEffect || e.Tenant.Value != int64(15) {
			t.Fatalf("%+v %+v", e, e.Tenant)
		}
		// And back: the tenant's value counts again.
		if _, err := s.SetSite(ctx, settings.RegistrationPerHour, settings.Change{HasValue: true, Value: int64(20)}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, ten, settings.RegistrationPerHour); e.Value != int64(15) || !e.Tenant.InEffect {
			t.Fatalf("%+v %+v", e, e.Tenant)
		}
		// Changing the rule to tighten counts the tenants whose value is looser.
		if _, err := s.SetSite(ctx, settings.RegistrationPerHour, settings.Change{HasLower: true, Lower: lower(settings.Allow)}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTenant(ctx, ten, settings.RegistrationPerHour, settings.Change{HasValue: true, Value: int64(40)}); err != nil {
			t.Fatal(err)
		}
		if n, err := s.SetSite(ctx, settings.RegistrationPerHour, settings.Change{HasLower: true, Lower: lower(settings.Tighten)}); err != nil || n != 1 {
			t.Fatalf("affected %d: %v", n, err)
		}
	})
}

func TestTightenOfAChoiceAndOfATenantsRule(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		// open < invite < closed. The site's own value of this key governs its
		// own people only, so a tenant's value is free until a rule for the
		// tenant holds it.
		if err := s.SetTenant(ctx, ten, settings.RegistrationMode, settings.Change{HasValue: true, Value: settings.RegOpen}); err != nil {
			t.Fatal(err)
		}
		// The site's rule for this tenant has its own value and its own minimum:
		// the tenant is held to the rule's value.
		rule := &settings.Rule{Value: settings.RegClosed, Lower: note(settings.Tighten, "Closed for this tenant.")}
		if err := s.SetTenant(ctx, ten, settings.RegistrationMode, settings.Change{Site: true, HasSiteRule: true, SiteRule: rule}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTenant(ctx, ten, settings.RegistrationMode, settings.Change{HasValue: true, Value: settings.RegInvite}); code(err) != settings.CodeRestricted {
			t.Fatalf("%v", err)
		}
		e, _ := s.Get(ctx, ten, settings.RegistrationMode)
		if !e.ByRule || e.Tenant.InEffect {
			t.Fatalf("the open value stays, not in effect: %+v %+v", e, e.Tenant)
		}
		if e.Floor != settings.RegClosed || e.Above.Mode != settings.Tighten || e.Above.Note != "Closed for this tenant." || e.Value != settings.RegClosed {
			t.Fatalf("%+v", e)
		}
		// A tenant's own rule for what is below it must be at least as strict.
		if err := s.SetTenant(ctx, ten, settings.RegistrationMode, settings.Change{HasLower: true, Lower: lower(settings.Allow)}); code(err) != settings.CodeRestricted {
			t.Fatalf("%v", err)
		}
	})
}

// A rule's note is shown with the rule, to the tenant too; a writer that sends
// none keeps it; an empty one clears it; it is at most 200 characters.
func TestARulesNoteIsKeptUntilItIsReplaced(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		key := settings.RegistrationPerHour
		if _, err := s.SetSite(ctx, key, settings.Change{HasLower: true, Lower: note(settings.Tighten, "why")}); err != nil {
			t.Fatal(err)
		}
		for _, who := range []string{"", ten} {
			if e, _ := s.Get(ctx, who, key); e.Site.Lower.Note != "why" {
				t.Fatalf("%q: %+v", who, e.Site.Lower)
			}
		}
		if e, _ := s.Get(ctx, ten, key); e.Above.Note != "why" {
			t.Fatalf("%+v", e.Above)
		}
		// A value alone, or a rule without a note, keeps it.
		if _, err := s.SetSite(ctx, key, settings.Change{HasValue: true, Value: int64(30)}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetSite(ctx, key, settings.Change{HasLower: true, Lower: lower(settings.Tighten)}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, "", key); e.Site.Lower.Note != "why" {
			t.Fatalf("%+v", e.Site.Lower)
		}
		// Too long: counted in characters, not bytes.
		long := make([]rune, settings.MaxNote+1)
		for i := range long {
			long[i] = '字'
		}
		if _, err := s.SetSite(ctx, key, settings.Change{HasLower: true, Lower: note(settings.Tighten, string(long))}); code(err) != settings.CodeInvalid {
			t.Fatalf("%v", err)
		}
		if _, err := s.SetSite(ctx, key, settings.Change{HasLower: true, Lower: note(settings.Tighten, string(long[:settings.MaxNote]))}); err != nil {
			t.Fatal(err)
		}
		// A rule that allows everything has no note: allowing clears it.
		if _, err := s.SetSite(ctx, key, settings.Change{HasLower: true, Lower: note(settings.Allow, "x")}); code(err) != settings.CodeInvalid {
			t.Fatalf("%v", err)
		}
		if _, err := s.SetSite(ctx, key, settings.Change{HasLower: true, Lower: lower(settings.Allow)}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetSite(ctx, key, settings.Change{HasLower: true, Lower: lower(settings.Tighten)}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, "", key); e.Site.Lower.Note != "" {
			t.Fatalf("%+v", e.Site.Lower)
		}
		// An empty note clears one.
		if _, err := s.SetSite(ctx, key, settings.Change{HasLower: true, Lower: note(settings.Lock, "a")}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetSite(ctx, key, settings.Change{HasLower: true, Lower: note(settings.Lock, "")}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, "", key); e.Site.Lower.Note != "" || e.Site.Lower.Mode != settings.Lock {
			t.Fatalf("%+v", e.Site.Lower)
		}

		// The same for the site's rule for one tenant.
		rule := &settings.Rule{Value: int64(7), Lower: note(settings.Lock, "Trial tenant.")}
		if err := s.SetTenant(ctx, ten, settings.QuotaSpaces, settings.Change{Site: true, HasSiteRule: true, SiteRule: rule}); err != nil {
			t.Fatal(err)
		}
		e, _ := s.Get(ctx, ten, settings.QuotaSpaces)
		if e.Above.Note != "Trial tenant." || e.Tenant.SiteRule.Lower.Note != "Trial tenant." {
			t.Fatalf("%+v", e.Above)
		}
		// Locked again by the short form: the note stays.
		if err := s.SetTenant(ctx, ten, settings.QuotaSpaces, settings.Change{Site: true, HasSiteLock: true, SiteLock: true}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, ten, settings.QuotaSpaces); e.Above.Note != "Trial tenant." {
			t.Fatalf("%+v", e.Above)
		}
	})
}

// A rule read from JSON knows whether it carried a note.
func TestLowerJSONKnowsWhetherANoteWasSent(t *testing.T) {
	var l settings.Lower
	if err := l.UnmarshalJSON([]byte(`{"mode":"lock"}`)); err != nil || l.NoteSet || l.Mode != "lock" {
		t.Fatalf("%+v %v", l, err)
	}
	if err := l.UnmarshalJSON([]byte(`{"mode":"lock","note":""}`)); err != nil || !l.NoteSet || l.Note != "" {
		t.Fatalf("%+v %v", l, err)
	}
	if err := l.UnmarshalJSON([]byte(`{"mode":"restrict","max":3,"note":"x"}`)); err != nil || !l.NoteSet || l.Note != "x" || *l.Max != 3 {
		t.Fatalf("%+v %v", l, err)
	}
}
