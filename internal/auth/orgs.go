package auth

import (
	"context"

	"petalbook.app/server/internal/ratelimit"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// Organisations people create themselves (docs/design/3-6-c.md, 2): the rules
// that come from the site's settings, and the checks of an organisation's name
// and slug. The registration that makes one is in emailflows.go.

// OrgRules is what the site's settings say of organisations people create.
type OrgRules struct {
	// Open: the switch is on, tenants may exist (features.tenancy) and mail is
	// enabled, which a registration needs.
	Open bool
	// PerPerson is how many one person may have made; PerHour the cap for the
	// whole server in an hour (0: none); PlanSlug the plan a new one starts with.
	PerPerson int
	PerHour   int
	PlanSlug  string
}

// OrgCreation reads the rules from the settings' cache: no query.
func (s *Service) OrgCreation(ctx context.Context) OrgRules {
	st := s.settings.Load()
	if st == nil {
		return OrgRules{}
	}
	on := func(key string) bool {
		e, err := st.Get(ctx, "", key)
		if err != nil {
			return false
		}
		b, _ := e.Value.(bool)
		return b
	}
	num := func(key string) int {
		e, err := st.Get(ctx, "", key)
		if err != nil {
			return 0
		}
		switch n := e.Value.(type) {
		case int64:
			return int(n)
		case int:
			return n
		case float64:
			return int(n)
		}
		return 0
	}
	r := OrgRules{Open: on(settings.FeatureOrgCreation) && on(settings.FeatureTenancy) && s.MailEnabled(),
		PerPerson: num(settings.OrgCreationPerPerson), PerHour: num(settings.OrgCreationPerHour)}
	if e, err := st.Get(ctx, "", settings.OrgCreationPlan); err == nil {
		r.PlanSlug, _ = e.Value.(string)
	}
	return r
}

// OrgParams is the organisation a registration asks for.
type OrgParams struct{ Name, Slug string }

// cleanOrg checks the name and the slug as a site administrator's tenant is
// checked, and that the slug is free (no tenant, no live registration holds it).
func (s *Service) cleanOrg(ctx context.Context, o OrgParams) (name, slug string, err error) {
	name, ok := store.CleanTenantName(o.Name)
	if !ok || !store.TenantSlugRE.MatchString(o.Slug) {
		return "", "", errf(Invalid, "Slug is 2 to 32 characters of a-z, 0-9 and -, starting with a letter or digit; name is 1 to 80 characters.")
	}
	if _, taken := s.st.TenantIDBySlug(o.Slug); taken {
		return "", "", errSlugTaken()
	}
	if held, err := store.PendingOrgSlug(ctx, s.st.Q(), o.Slug, s.now()); err != nil {
		return "", "", err
	} else if held {
		return "", "", errSlugTaken()
	}
	return name, o.Slug, nil
}

func errSlugTaken() *Error { return coded(409, "slug_taken", "That organization address is taken.") }

// orgCreationBlocked is where (d) says that an email domain belongs to an
// organisation that does not want others made on it (restrictOrgs on a verified
// domain). Until then nothing is blocked.
func (s *Service) orgCreationBlocked(ctx context.Context, email string) error { return nil }

// orgCapped says whether no organisation may be made now, before anything is
// written: a person may have none (perPerson 0), the server has its most, or
// the hour's cap is used up. Taking a place of the hour is the last test, so a
// refusal for another reason does not spend one.
func (s *Service) orgCapped(r OrgRules) bool {
	if r.PerPerson <= 0 || s.st.TenantCount() >= store.MaxTenants {
		return true
	}
	if r.PerHour > 0 {
		if _, ok := s.regCaps.TakeAll(ratelimit.Cost{Key: "org", Limit: r.PerHour}); !ok {
			return true
		}
	}
	return false
}
