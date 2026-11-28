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
	// address: so it needs one.
	viaSSO := s.localRefused(ctx, s.sec(ctx), tenantID, p.Role, site)
	if viaSSO && strings.TrimSpace(p.Email) == "" {
		return nil, errf(Invalid, "An email address is needed: this person signs in with the organisation's single sign-on, which finds the account by it.")
	}
	u, err := s.CreateUser(ctx, CreateParams{Username: p.Username, Name: p.Name, Email: p.Email, Role: p.Role, TenantID: tenantID, SiteAdmin: site})
	if err != nil {
		return nil, err
	}
	out := &AdminCreated{User: u, SignIn: SignInLink, Mail: MailNone}
	if viaSSO {
		out.SignIn = SignInSSO
		if err := store.SetAwaitingSSO(ctx, s.st.Q(), u.ID, u.TenantID, s.now()); err != nil {
			return nil, err
		}
		return out, nil
	}
	// The link the administrator copies, and, when mail can reach the person,
	// one of its own for the mail.
	var tok string
	if err := s.st.Tx(ctx, func(q store.Q) (err error) {
		tok, err = s.newEmailToken(ctx, q, store.TokenWelcome, u.ID, u.Email, welcomeTTL)
		return err
	}); err != nil {
		return nil, err
	}
	out.Link, out.Expires = s.link("/reset-password", tok), s.now().Add(welcomeTTL).UTC()
	if u.Email != "" && s.MailEnabled() {
		out.Mail = s.mailWelcome(ctx, *u, p.Lang)
	}
	return out, nil
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
	err := s.sendMail(mail.KindWelcome, mail.PickLang(lang, ""), u.Email, mail.Vars{Name: u.Name, Username: u.Username, Link: s.link("/reset-password", tok), Hours: int(welcomeTTL.Hours())})
	if err == nil {
		return MailSent
	}
	_ = s.st.Tx(ctx, func(q store.Q) error { return store.DeleteEmailTokens(ctx, q, u.ID, store.TokenWelcomeMail) })
	if ae := new(Error); errors.As(err, &ae) && ae.Status == 503 {
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
	if siteAdmin != nil {
		if *siteAdmin && (!admins || !store.IsAdminRole(role)) {
			return false, errf(Forbidden, "Only a site administrator can make a site administrator.")
		}
		site = *siteAdmin
	}
	return site, nil
}
