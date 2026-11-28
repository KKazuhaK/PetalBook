package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/mail"
	"petalbook.app/server/internal/store"
)

// An administrator makes an account by hand (docs/design/decisions-2026-11-27.md).
// Under registration mode closed this is, with the command line, the only way
// an account is made, so it is not gated by the mode (nor by
// registration.domains): it is an administrator's act. quota.people still
// bounds it, as it bounds every way an account is made (CreateUser).

// welcomeTTL is how long the set-password link of an account made by an
// administrator works.
const welcomeTTL = 7 * 24 * time.Hour

// How a person made by an administrator gets in.
const (
	// SignInLink: they set a password with a one-time link.
	SignInLink = "link"
	// SignInSSO: the organisation requires its single sign-on for them, so no
	// password link is made.
	SignInSSO = "sso"
)

// AdminCreateParams is the account an administrator makes. Tenant is the
// tenant it belongs to ("": the administrator's own). SiteAdmin follows an
// invite's rule (CreateInvite): nil makes an administrator of the default
// tenant one of the server when the actor manages site administrators.
type AdminCreateParams struct {
	Tenant, Username, Name, Email, Role, Lang string
	SiteAdmin                                 *bool
}

// AdminCreated is the account made. Link is the set-password link, shown once
// ("" when the person signs in with the organisation's SSO); Mail says whether
// the same was mailed (MailSent, MailNone or MailLimited).
type AdminCreated struct {
	User    *store.User
	SignIn  string
	Link    string
	Expires time.Time
	Mail    string
}

// AdminCreateUser makes an account for someone else. The person's password is
// never known to the administrator: they get a link that sets it (seven days,
// once). A mailed copy of the link also proves the address; the copied one does
// not, as nothing says it reached the address.
func (s *Service) AdminCreateUser(ctx context.Context, actor store.User, access authz.Subject, p AdminCreateParams) (*AdminCreated, error) {
	if p.Role == "" {
		p.Role = "member"
	}
	if err := checkRole(p.Role); err != nil {
		return nil, err
	}
	tenantID := p.Tenant
	if tenantID == "" {
		tenantID = actor.TenantID
	}
	// Making people in another tenant is for who manages people across the site.
	if !access.Can(authz.TenantUsersManage, tenantID) {
		return nil, errf(NotFound, "No such tenant.")
	}
	if t, err := store.GetTenant(ctx, s.st.Q(), tenantID); errors.Is(err, store.ErrNotFound) {
		return nil, errf(NotFound, "No such tenant.")
	} else if err != nil {
		return nil, err
	} else if t.Disabled {
		return nil, errf(Conflict, "That tenant is disabled.")
	}
	site, err := siteAdminFor(access, tenantID, p.Role, p.SiteAdmin)
	if err != nil {
		return nil, err
	}
	// Where the organisation requires its SSO for the person, the account has no
	// password and waits for the SSO's first sign-in, which finds it by its
	// address. So it needs one the organisation's SSO can vouch for (a verified
	// domain of the tenant, or one routed to it): any other address could never
	// be claimed, and would only be kept from every other sign-in.
	viaSSO := s.localRefused(ctx, s.sec(ctx), tenantID, p.Role, site)
	if viaSSO {
		switch {
		case tenantID == store.DefaultTenantID:
			return nil, coded(400, "sso_default_tenant", "People of the default tenant cannot be made by hand while single sign-on is required: they sign in with it themselves, as the registration mode allows.")
		case strings.TrimSpace(p.Email) == "":
			return nil, errf(Invalid, "An email address is needed: this person signs in with the organisation's single sign-on, which finds the account by it.")
		}
		e, err := s.cleanEmailFor(ctx, p.Email)
		if err != nil {
			return nil, err
		}
		if !s.ssoCanVouch(ctx, tenantID, e) {
			return nil, coded(400, "email_domain_not_verified", "This address is not on a verified domain of the organisation, so its single sign-on could never match it. Verify the domain first.")
		}
	}
	// What lets the person in is written with the account, in its transaction.
	var tok string
	cp := CreateParams{Username: p.Username, Name: p.Name, Email: p.Email, Role: p.Role, TenantID: tenantID, SiteAdmin: site}
	cp.AfterCreate = func(ctx context.Context, q store.Q, u store.User) (err error) {
		if viaSSO {
			return store.SetAwaitingSSO(ctx, q, u.ID, u.TenantID, s.now())
		}
		tok, err = s.newEmailToken(ctx, q, store.TokenWelcome, u.ID, u.Email, welcomeTTL)
		return err
	}
	u, err := s.CreateUser(ctx, cp)
	if err != nil {
		return nil, err
	}
	out := &AdminCreated{User: u, SignIn: SignInLink, Mail: MailNone}
	if viaSSO {
		out.SignIn = SignInSSO
		return out, nil
	}
	out.Link, out.Expires = s.link("/reset-password", tok), s.now().Add(welcomeTTL).UTC()
	if u.Email != "" && s.MailReadyFor(ctx, u.TenantID) {
		out.Mail = s.mailWelcome(ctx, *u, p.Lang)
	}
	return out, nil
}

// ssoCanVouch says whether the tenant's single sign-on may vouch for the
// address: it is on a domain the tenant verified, or on one routed to the
// tenant (a site SSO offered to it), the two ways claimWaiting finds it.
func (s *Service) ssoCanVouch(ctx context.Context, tenantID, email string) bool {
	if inDomains(email, s.orgDomains(ctx, tenantID)) {
		return true
	}
	if s.cfg.RouteDomain == nil {
		return false
	}
	routed, _, ok := s.cfg.RouteDomain(ctx, email)
	return ok && routed == tenantID
}

// mailWelcome sends the person the link that sets their password and proves
// their address, and says how it went.
func (s *Service) mailWelcome(ctx context.Context, u store.User, lang string) string {
	if _, ok := s.mailSlot(u.Email, ""); !ok {
		return MailLimited
	}
	var tok string
	if err := s.st.Tx(ctx, func(q store.Q) (err error) {
		tok, err = s.newEmailToken(ctx, q, store.TokenWelcomeMail, u.ID, u.Email, welcomeTTL)
		return err
	}); err != nil {
		s.log.Warn("could not make the mailed link of a new account", "error", err)
		return MailNone
	}
	err := s.sendMail(u.TenantID, mail.KindWelcome, mail.PickLang(lang, ""), u.Email, mail.Vars{Name: u.Name, Username: u.Username, Link: s.link("/reset-password", tok), Hours: int(welcomeTTL.Hours())})
	if err == nil {
		return MailSent
	}
	_ = s.st.Tx(ctx, func(q store.Q) error { return store.DeleteEmailTokens(ctx, q, u.ID, store.TokenWelcomeMail) })
	if ae := new(Error); errors.As(err, &ae) && (ae.Status == 503 || ae.Kind == RateLimited) {
		return MailLimited
	}
	s.log.Warn("could not queue a message", "reason", err.Error())
	return MailNone
}

// siteAdminFor is whether an account made in the tenant is an administrator of
// the whole server: as for an invite (CreateInvite), an administrator of the
// default tenant made by who manages site administrators is one, unless
// siteAdmin says otherwise; nobody else can make one.
func siteAdminFor(access authz.Subject, tenantID, role string, siteAdmin *bool) (bool, error) {
	admins := access.Can(authz.SiteAdminsManage, "")
	site := store.IsAdminRole(role) && admins && tenantID == store.DefaultTenantID
	// An administrator of the default tenant made by one who manages site
	// administrators would silently be one of the server: it has to be said.
	if site && siteAdmin == nil {
		return false, coded(400, "site_admin_explicit", "An administrator of the default tenant is an administrator of the whole server: send siteAdmin true to make one, or false for an administrator of the default tenant only.")
	}
	if siteAdmin != nil {
		if *siteAdmin && (!admins || !store.IsAdminRole(role)) {
			return false, errf(Forbidden, "Only a site administrator can make a site administrator.")
		}
		site = *siteAdmin
	}
	return site, nil
}
