package auth

import (
	"context"
	"errors"
	"strings"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// Finding the SSO by an address (3.6 d4, docs/api.md "Email domains and the
// SSO entry"), and an organisation's own signin.ssoOnly.

// Discovered is where an address's sign-in goes: the provider its domain is
// routed to, and whether the domain's tenant requires single sign-on.
type Discovered struct {
	Slug     string `json:"slug"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Icon     string `json:"icon"`
	Start    string `json:"start"`
	Enforced bool   `json:"enforced"`
}

// Discover answers POST /api/auth/discover. It spends one of the address's
// requests this minute (as a sign-in does), then looks at the domain of the
// address alone, in memory: never at an account, so the answer is the same
// whether one exists. Something without an @ (a username) gets nil.
func (s *Service) Discover(ctx context.Context, email, ip string) (*Discovered, error) {
	if err := s.allow(ctx, ip); err != nil {
		return nil, err
	}
	return s.route(ctx, email), nil
}

// route is where the domain of the address routes its sign-in now, or nil.
func (s *Service) route(ctx context.Context, email string) *Discovered {
	email = strings.TrimSpace(email)
	if s.cfg.RouteDomain == nil || len(email) > 254 || !strings.Contains(email, "@") {
		return nil
	}
	tenant, id, ok := s.cfg.RouteDomain(ctx, email)
	if !ok {
		return nil
	}
	rt := s.provs.getID(id)
	if rt == nil || !s.routable(ctx, rt, tenant) {
		return nil
	}
	return &Discovered{Slug: rt.row.Slug, Kind: rt.row.Kind, Name: rt.row.Name, Icon: rt.row.Icon,
		Start: "/api/auth/" + rt.row.Kind + "/" + rt.row.Slug + "/start", Enforced: s.tenantSSOOnly(ctx, tenant)}
}

// routable says whether a domain of the tenant may send its sign-in to the
// provider now: one of the tenant's own, or a site SSO provider offered to
// it, that can sign in.
func (s *Service) routable(ctx context.Context, rt *providerRT, tenant string) bool {
	if !s.signInOn(ctx, rt) {
		return false
	}
	if rt.row.Owner == store.ProviderSite {
		return rt.row.Class == ClassSSO && s.tenantMayUse(ctx, rt, tenant)
	}
	return rt.row.Owner == tenant
}

// RoutableFor says whether a tenant's domain may be routed to the provider
// with the id: one of the tenant's own (ownSso), or a site SSO provider
// offered to the tenant (siteSSO). Neither for any other.
func (s *Service) RoutableFor(ctx context.Context, id, tenant string) (own, siteSSO bool) {
	rt := s.provs.getID(id)
	switch {
	case rt == nil:
	case rt.row.Owner == tenant:
		own = true
	case rt.row.Owner == store.ProviderSite && rt.row.Class == ClassSSO && s.tenantMayUse(ctx, rt, tenant):
		siteSSO = true
	}
	return own, siteSSO
}

// OfferedSSO is the site SSO providers offered to a tenant (to all, or by
// its list), in the administrators' order: what a domain of the tenant may be
// routed to besides its own.
func (s *Service) OfferedSSO(ctx context.Context, tenant string) []OfferedProvider {
	out := []OfferedProvider{}
	for _, rt := range s.provs.all() {
		if rt.row.Owner == store.ProviderSite && rt.row.Class == ClassSSO && s.tenantMayUse(ctx, rt, tenant) {
			out = append(out, OfferedProvider{ID: rt.row.ID, Slug: rt.row.Slug, Kind: rt.row.Kind, Name: rt.row.Name, Icon: rt.row.Icon, Enabled: rt.row.Enabled})
		}
	}
	return out
}

// OfferedProvider is a site SSO provider as a tenant's administrators see it
// (read only).
type OfferedProvider struct {
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Icon    string `json:"icon"`
	Enabled bool   `json:"enabled"`
}

// anyRouted says whether some verified domain routes its sign-in to a
// provider that can sign in: the sign-in page then asks for the address first.
func (s *Service) anyRouted(ctx context.Context) bool {
	if s.cfg.RoutedProviders == nil {
		return false
	}
	for _, id := range s.cfg.RoutedProviders(ctx) {
		if rt := s.provs.getID(id); rt != nil && s.signInOn(ctx, rt) {
			return true
		}
	}
	return false
}

// identifierFirst fills what the sign-in page says of the address-first step
// and the SSO entry: by the stored mode (an effective local_only only says no
// site provider is on; a domain may route to a tenant's own).
func (s *Service) identifierFirst(ctx context.Context, st *secState, info *PublicInfo) {
	mode := st.Login.Mode
	if mode == ModeLocalOnly || !s.anyRouted(ctx) {
		return
	}
	info.SSOEntry = true
	info.IdentifierFirst = mode == ModeDual || mode == ModeSSOFirst
}

// tenantSSOOnly says whether the tenant itself requires single sign-on
// (signin.ssoOnly on in its own layer, or by the site's rule for it) and has
// a way to meet it: one of its own providers or a site SSO provider offered
// to it that can sign in. Without one nothing is refused, as the site's
// ssoOnly is not in effect while no provider is (a lock-out otherwise). The
// site's own ssoOnly is ssoOnlyInEffect. From the settings' cache and memory.
func (s *Service) tenantSSOOnly(ctx context.Context, tenant string) bool {
	return s.tenantSSOOnlyOn(ctx, tenant) && s.tenantHasSSO(ctx, tenant)
}

// tenantSSOOnlyOn says whether the tenant's signin.ssoOnly is on (in its own
// layer, or by the site's rule for it), whether or not a single sign-on can
// meet it now: what keeps its last administrator with a password.
func (s *Service) tenantSSOOnlyOn(ctx context.Context, tenant string) bool {
	st := s.settings.Load()
	if st == nil || tenant == "" {
		return false
	}
	e, err := st.Get(ctx, tenant, settings.SigninSSOOnly)
	if err != nil || e.Tenant == nil {
		return false
	}
	tv := e.Tenant
	return tv.Value == true && tv.InEffect ||
		tv.SiteRule != nil && tv.SiteRule.InEffect && tv.SiteRule.Value == true
}

// tenantSSOOnlyAdmins says whether the tenant's signin.ssoOnlyAdmins is on:
// its ssoOnly then holds its administrators too (not site administrators).
func (s *Service) tenantSSOOnlyAdmins(ctx context.Context, tenant string) bool {
	st := s.settings.Load()
	if st == nil || tenant == "" {
		return false
	}
	e, err := st.Get(ctx, tenant, settings.SigninSSOOnlyAdmins)
	return err == nil && e.Value == true
}

// errLastLocalAdmin refuses taking away a tenant's last enabled administrator
// with a password while its ssoOnly is on: the back door while its identity
// provider fails (2.8's rule, per tenant, 3.6 d4).
func errLastLocalAdmin() *Error {
	return coded(409, "last_local_admin", "This organization requires single sign-on: keep at least one enabled administrator of it with a password.")
}

// LastLocalAdmin says whether u is its tenant's last enabled administrator
// with a password while the tenant's ssoOnly is on: demoting, disabling or
// deleting them is refused (409 last_local_admin). q is the transaction the
// change is made in.
func (s *Service) LastLocalAdmin(ctx context.Context, q store.Q, u store.User) (bool, error) {
	// With ssoOnlyAdmins its administrators sign in through the SSO, so none needs a password.
	if !u.IsAdmin() || u.Disabled || !s.tenantSSOOnlyOn(ctx, u.TenantID) || s.tenantSSOOnlyAdmins(ctx, u.TenantID) {
		return false, nil
	}
	if _, err := store.GetPasswordHash(ctx, q, u.ID); errors.Is(err, store.ErrNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	n, err := store.CountTenantPasswordAdmins(ctx, q, u.TenantID)
	return n <= 1, err
}

// ErrLastLocalAdmin is the refusal LastLocalAdmin calls for.
func ErrLastLocalAdmin() error { return errLastLocalAdmin() }

// tenantHasSSO says whether the tenant's people have a single sign-on that
// can sign in now: one of the tenant's own, or a site SSO offered to it.
func (s *Service) tenantHasSSO(ctx context.Context, tenant string) bool {
	for _, rt := range s.provs.all() {
		if (rt.row.Owner == tenant || rt.row.Owner == store.ProviderSite && rt.row.Class == ClassSSO) && s.routable(ctx, rt, tenant) {
			return true
		}
	}
	return false
}

// ssoOnlyFor says whether password and passkey sign-in are refused to the
// people of the tenant who are not administrators: the site's ssoOnly in
// effect, or the tenant's own.
func (s *Service) ssoOnlyFor(ctx context.Context, st *secState, tenant string) bool {
	return s.ssoOnlyInEffect(st) || s.tenantSSOOnly(ctx, tenant)
}

// localRefused says whether password and passkey sign-in are refused to an
// account of the tenant with role (siteAdmin: of the whole server): members
// whenever ssoOnlyFor; administrators of the tenant only when its
// ssoOnlyAdmins holds them too. A site administrator keeps the password.
func (s *Service) localRefused(ctx context.Context, st *secState, tenant, role string, siteAdmin bool) bool {
	if !store.IsAdminRole(role) {
		return s.ssoOnlyFor(ctx, st, tenant)
	}
	return !siteAdmin && s.tenantSSOOnly(ctx, tenant) && s.tenantSSOOnlyAdmins(ctx, tenant)
}

// localRefusedUser is localRefused for an account.
func (s *Service) localRefusedUser(ctx context.Context, st *secState, u *store.User) bool {
	return s.localRefused(ctx, st, u.TenantID, u.Role, u.SiteAdmin)
}

// checkTenantSSOOnly is the check of a tenant turning its signin.ssoOnly on
// (its administrators, or the site's rule for it): it needs a single sign-on
// its people can use, and an enabled administrator of the tenant with a
// password, the back door while the identity provider fails.
func (s *Service) checkTenantSSOOnly(ctx context.Context, tenant string) error {
	if !s.tenantHasSSO(ctx, tenant) {
		return coded(400, "sso_provider_required", "Turn on a single sign-on provider for this organization first: its people would have no way to sign in.")
	}
	// Administrators held to the SSO too need no password back door.
	if s.tenantSSOOnlyAdmins(ctx, tenant) {
		return nil
	}
	n, err := store.CountTenantPasswordAdmins(ctx, s.st.Q(), tenant)
	if err != nil {
		return err
	}
	if n == 0 {
		return coded(400, "local_admin_required", "No enabled administrator of this organization has a password: requiring single sign-on would lock everyone out if the identity provider fails.")
	}
	return nil
}

// ssoRequired is the refusal of a password registration for an address of a
// domain whose tenant requires single sign-on: 409 sso_required with where
// to sign in instead (nil when the domain is routed nowhere usable).
func (s *Service) ssoRequired(ctx context.Context, email string) error {
	if d := s.route(ctx, email); d != nil && d.Enforced {
		return ssoRequiredErr(d)
	}
	return nil
}

// ssoRequiredOf is sso_required for a registration in a tenant that requires
// its single sign-on, with where the address's domain routes (nil when it
// routes nowhere).
func (s *Service) ssoRequiredOf(ctx context.Context, email string) error {
	return ssoRequiredErr(s.route(ctx, email))
}

func ssoRequiredErr(d *Discovered) *Error {
	e := coded(409, "sso_required", "This organization signs its people in with single sign-on, not with a password.")
	e.SSO = d
	return e
}
