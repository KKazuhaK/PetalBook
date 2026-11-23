package auth

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// AccountView is what GET /api/account shows.
type AccountView struct {
	User        store.User
	HasPassword bool
	Identities  []IdentityView
	Passkeys    []PasskeyView
	// PasskeyRequired: the person must have a passkey (by themselves or by their role).
	PasskeyRequired bool
}

// IdentityView is an attached identity as shown to its owner.
type IdentityView struct {
	ID       string
	Provider string
	Slug     string
	Kind     string
	Label    string
	Created  time.Time
}

// Account describes the signed-in user's sign-in methods.
func (s *Service) Account(ctx context.Context, u store.User) (*AccountView, error) {
	v := &AccountView{User: u}
	if _, err := store.GetPasswordHash(ctx, s.st.Q(), u.ID); err == nil {
		v.HasPassword = true
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	ids, err := store.ListIdentities(ctx, s.st.Q(), u.ID)
	if err != nil {
		return nil, err
	}
	v.Identities = make([]IdentityView, len(ids))
	for i, id := range ids {
		name, slug, kind := s.providerOf(id.Issuer)
		v.Identities[i] = IdentityView{ID: id.ID, Provider: name, Slug: slug, Kind: kind, Label: id.Label, Created: id.Created}
	}
	if v.Passkeys, err = s.Passkeys(ctx, u.ID); err != nil {
		return nil, err
	}
	return v, nil
}

// providerOf names the provider an issuer belongs to; for one that was deleted
// it is the issuer's host, with no slug and no kind.
func (s *Service) providerOf(issuer string) (name, slug, kind string) {
	for _, rt := range s.provs.all() {
		if rt.row.Issuer == issuer {
			return rt.row.Name, rt.row.Slug, rt.row.Kind
		}
	}
	if u, err := url.Parse(issuer); err == nil && u.Host != "" {
		return u.Host, "", ""
	}
	return issuer, "", ""
}

// UpdateProfile changes the signed-in user's name and email; a nil field is left alone.
func (s *Service) UpdateProfile(ctx context.Context, u store.User, name, email *string) (*store.User, error) {
	n, e := u.Name, u.Email
	var err error
	if name != nil {
		if n, err = cleanDisplayName(*name); err != nil {
			return nil, err
		}
	}
	verified := u.EmailVerified
	if email != nil {
		if e, err = cleanEmail(*email); err != nil {
			return nil, err
		}
		if !strings.EqualFold(e, u.Email) || e != u.Email {
			verified = false // a person changing their own address clears the mark
		}
	}
	if err := store.UpdateProfile(ctx, s.st.Q(), u.ID, n, e, verified); err != nil {
		return nil, err
	}
	s.dropUser(u.ID)
	u.Name, u.Email, u.EmailVerified = n, e, verified
	s.cfg.Spaces.UserChanged(ctx, u)
	return &u, nil
}

// ChangePassword sets the signed-in user's password. A user who has a password
// must give the current one (a wrong one is Forbidden and counts against the
// rate limits); one who has none (an account made through single sign-on) must
// not. The user's other sessions end; the one asking stays.
func (s *Service) ChangePassword(ctx context.Context, p *Principal, current *string, newPassword, ip string) error {
	if err := CheckPassword(newPassword); err != nil {
		return err
	}
	hash, err := store.GetPasswordHash(ctx, s.st.Q(), p.User.ID)
	hasPassword := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if hasPassword {
		if current == nil || *current == "" {
			return errf(Invalid, "current is required: send the password you use now")
		}
		st := s.sec(ctx)
		if err := s.gateIP(st, ip); err != nil {
			return err
		}
		ok := false
		if len(*current) <= MaxPasswordBytes {
			if ok, _, err = s.hasher.verify(ctx, hash, *current); err != nil {
				s.refundIP(ip)
				return err
			}
		}
		if !ok {
			return errf(Forbidden, "the current password is wrong")
		}
		s.refundIP(ip)
	} else if current != nil && *current != "" {
		return errf(Invalid, "this account has no password yet; leave current out")
	}
	nh, err := s.hasher.hash(ctx, newPassword)
	if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q store.Q) error {
		if err := store.SetPasswordHash(ctx, q, p.User.ID, nh, s.now()); err != nil {
			return err
		}
		return store.DeleteUserSessions(ctx, q, p.User.ID, p.SessionID)
	})
	if err != nil {
		return err
	}
	s.evictUser(p.User.ID, p.SessionID)
	return nil
}

// UnlinkIdentity removes one of the user's identities unless that would leave
// no way to sign in.
func (s *Service) UnlinkIdentity(ctx context.Context, userID, identityID string) error {
	s.credMu.Lock()
	defer s.credMu.Unlock()
	return s.st.Tx(ctx, func(q store.Q) error {
		ids, err := store.ListIdentities(ctx, q, userID)
		if err != nil {
			return err
		}
		found := false
		for _, i := range ids {
			found = found || i.ID == identityID
		}
		if !found {
			return errf(NotFound, "no such identity")
		}
		if len(ids) == 1 {
			if _, err := store.GetPasswordHash(ctx, q, userID); errors.Is(err, store.ErrNotFound) {
				if n, err := store.CountPasskeys(ctx, q, userID); err != nil {
					return err
				} else if n == 0 {
					return errf(Conflict, "this is the only way you can sign in; set a password or a passkey first")
				}
			} else if err != nil {
				return err
			}
		}
		for _, i := range ids {
			if i.ID == identityID {
				_, slug, _ := s.providerOf(i.Issuer)
				defer s.Audit(AuditEvent{Kind: "identity_unlinked", Actor: userID, User: userID, Detail: map[string]any{"provider": slug, "by": "user"}})
			}
		}
		return store.DeleteIdentity(ctx, q, identityID, userID)
	})
}

// UserPatch is a change an administrator makes to another account.
type UserPatch struct {
	Role            *string
	Disabled        *bool
	PasskeyRequired *bool
	Email           *string
	EmailVerified   *bool
	// SiteAdmin makes an administrator one of the whole server; only a site
	// administrator may send it.
	SiteAdmin *bool
}

// UpdateUser applies a patch. It refuses to leave the instance without an
// enabled administrator, an administrator disabling themself, and, while
// password sign-in is refused to everyone else, leaving no enabled
// administrator with a password.
func (s *Service) UpdateUser(ctx context.Context, actor store.User, ip, targetID string, patch UserPatch) (*store.User, error) {
	actorID := actor.ID
	if patch.Role != nil {
		if err := checkRole(*patch.Role); err != nil {
			return nil, err
		}
	}
	var email string
	if patch.Email != nil {
		e, err := cleanEmail(*patch.Email)
		if err != nil {
			return nil, err
		}
		email = e
	}
	st := s.sec(ctx)
	if patch.PasskeyRequired != nil && *patch.PasskeyRequired && !s.passkeysOn(st) {
		return nil, coded(400, "passkey_unavailable", "passkeys cannot be used here: they need a --public-url that is https, and passkeys switched on")
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	var out *store.User
	changed := map[string]any{}
	err := s.st.Tx(ctx, func(q store.Q) error {
		// A person of another tenant is no person at all.
		u, err := store.GetUser(ctx, q, store.ScopeOf(actor), targetID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errf(NotFound, "no such user")
			}
			return err
		}
		isSite := actor.SiteAdmin && actor.Role == "admin"
		if u.SiteAdmin && !isSite {
			return errf(Forbidden, "only a site administrator may change a site administrator")
		}
		if patch.SiteAdmin != nil && !isSite {
			return errf(Forbidden, "only a site administrator may make or end a site administrator")
		}
		role, disabled := u.Role, u.Disabled
		if patch.Role != nil {
			role = *patch.Role
		}
		if patch.Disabled != nil {
			disabled = *patch.Disabled
		}
		if disabled && !u.Disabled && u.ID == actorID {
			return errf(Conflict, "you cannot disable your own account")
		}
		// Promoting someone in the default tenant gives what administrators of it
		// always had: the server (a site administrator may say otherwise).
		site := u.SiteAdmin
		switch {
		case patch.SiteAdmin != nil:
			site = *patch.SiteAdmin
		case isSite && role == "admin" && u.Role != "admin" && u.TenantID == store.DefaultTenantID:
			site = true
		}
		if role != "admin" {
			site = false
		}
		if patch.SiteAdmin != nil && *patch.SiteAdmin && role != "admin" {
			return errf(Invalid, "a site administrator must be an administrator")
		}
		losesAdmin := u.SiteAdmin && !u.Disabled && (!site || disabled)
		if losesAdmin {
			if n, err := store.CountEnabledAdmins(ctx, q); err != nil {
				return err
			} else if n <= 1 {
				return errf(Conflict, "there must be at least one enabled administrator")
			}
			// Whenever ssoOnly is stored, not only while in effect: a provider
			// enabled later, or the page leaving local_only, puts it in effect.
			if st.Login.SSOOnly {
				if _, err := store.GetPasswordHash(ctx, q, u.ID); err == nil {
					if n, err := store.CountEnabledPasswordAdmins(ctx, q); err != nil {
						return err
					} else if n <= 1 {
						return errf(Conflict, "password sign-in is refused to everyone but administrators: keep at least one enabled administrator with a password")
					}
				}
			}
		}
		if role != u.Role {
			if err := store.SetRole(ctx, q, u.ID, role); err != nil {
				return err
			}
			changed["role"] = role
		}
		if site != u.SiteAdmin {
			if err := store.SetSiteAdmin(ctx, q, u.ID, site); err != nil {
				return err
			}
			changed["siteAdmin"] = site
			u.SiteAdmin = site
		}
		if disabled != u.Disabled {
			if err := store.SetDisabled(ctx, q, u.ID, disabled); err != nil {
				return err
			}
			if disabled {
				if err := store.DeleteUserSessions(ctx, q, u.ID, ""); err != nil {
					return err
				}
			}
			changed["disabled"] = disabled
		}
		if patch.PasskeyRequired != nil && *patch.PasskeyRequired != u.PasskeyRequired {
			if err := store.SetPasskeyRequired(ctx, q, u.ID, *patch.PasskeyRequired); err != nil {
				return err
			}
			u.PasskeyRequired = *patch.PasskeyRequired
			changed["passkeyRequired"] = u.PasskeyRequired
		}
		if patch.Email != nil || patch.EmailVerified != nil {
			newEmail, verified := u.Email, u.EmailVerified
			if patch.Email != nil {
				newEmail = email
				if newEmail != u.Email {
					verified = false // setting an address without saying it is verified leaves it unverified
				}
			}
			if patch.EmailVerified != nil {
				verified = *patch.EmailVerified && newEmail != ""
			}
			if newEmail != u.Email || verified != u.EmailVerified {
				if err := store.SetEmail(ctx, q, u.ID, newEmail, verified); err != nil {
					return err
				}
				if newEmail != u.Email {
					changed["email"] = newEmail
				}
				if verified != u.EmailVerified {
					changed["emailVerified"] = verified
				}
				u.Email, u.EmailVerified = newEmail, verified
			}
		}
		u.Role, u.Disabled = role, disabled
		out = u
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.evictUser(targetID, "")
	// The space manager remembers a user's role and whether they are disabled
	// for a minute (the drafts sweeper asks it); a change here applies at once.
	s.cfg.Spaces.UserChanged(ctx, *out)
	if len(changed) > 0 {
		s.Audit(AuditEvent{Kind: "user_changed", Actor: actorID, User: out.ID, Username: out.Username, IP: ip, Detail: changed})
	}
	return out, nil
}

// --- invites -----------------------------------------------------------------

// NewInvite is an invite just made; Token is shown once.
type NewInvite struct {
	store.Invite
	Token string
}

// CreateInvite makes an invite that works once, for an account in tenantID.
// days is 1-30; nil means 7. An explicit 0 is refused, not taken for "not
// given". An administrator invited to the default tenant by a site
// administrator is a site administrator (what every administrator of the
// server was before tenants), unless siteAdmin says otherwise; nobody else
// can make one.
func (s *Service) CreateInvite(ctx context.Context, actor store.User, tenantID, role string, daysGiven *int, siteAdmin *bool) (*NewInvite, error) {
	if role == "" {
		role = "member"
	}
	if err := checkRole(role); err != nil {
		return nil, err
	}
	days := 7
	if daysGiven != nil {
		days = *daysGiven
	}
	if days < 1 || days > 30 {
		return nil, errf(Invalid, "days is 1 to 30")
	}
	now := s.now().UTC()
	token := randomToken()
	if tenantID == "" {
		tenantID = actor.TenantID
	}
	isSite := actor.SiteAdmin && actor.Role == "admin"
	if !isSite && tenantID != actor.TenantID {
		return nil, errf(NotFound, "no such tenant")
	}
	if t, err := store.GetTenant(ctx, s.st.Q(), tenantID); errors.Is(err, store.ErrNotFound) {
		return nil, errf(NotFound, "no such tenant")
	} else if err != nil {
		return nil, err
	} else if t.Disabled {
		return nil, errf(Conflict, "that tenant is disabled")
	}
	site := role == "admin" && isSite && tenantID == store.DefaultTenantID
	if siteAdmin != nil {
		if *siteAdmin && (!isSite || role != "admin") {
			return nil, errf(Forbidden, "only a site administrator can invite a site administrator")
		}
		site = *siteAdmin
	}
	inv := store.Invite{TokenHash: hashToken(token), Role: role, CreatedBy: actor.ID, Created: now, TenantID: tenantID, SiteAdmin: site,
		Expires: now.Add(time.Duration(days) * 24 * time.Hour)}
	for attempt := 0; ; attempt++ {
		inv.ID = s.newID()
		err := store.InsertInvite(ctx, s.st.Q(), inv)
		if err == nil {
			break
		}
		if !store.IsUnique(err) || attempt >= 5 {
			return nil, err
		}
	}
	return &NewInvite{Invite: inv, Token: token}, nil
}

// OpenInvites lists the invites neither used nor expired.
func (s *Service) OpenInvites(ctx context.Context, sc store.Scope) ([]store.Invite, error) {
	return store.ListOpenInvites(ctx, s.st.Q(), sc, s.now())
}

// DeleteInvite revokes an invite.
func (s *Service) DeleteInvite(ctx context.Context, sc store.Scope, id string) error {
	if err := store.DeleteInvite(ctx, s.st.Q(), sc, id); errors.Is(err, store.ErrNotFound) {
		return errf(NotFound, "no such invite")
	} else {
		return err
	}
}

// Run keeps the tables tidy until ctx ends: expired sessions and invites go.
func (s *Service) Run(ctx context.Context) {
	hourly := time.NewTicker(time.Hour)
	defer hourly.Stop()
	flush := time.NewTicker(time.Second)
	defer flush.Stop()
	daily := time.NewTicker(24 * time.Hour)
	defer daily.Stop()
	s.Sweep(ctx)
	s.trimAudit(ctx)
	for {
		select {
		case <-ctx.Done():
			s.FlushAudit(context.WithoutCancel(ctx))
			return
		case <-flush.C:
			s.FlushAudit(ctx)
			s.sec(ctx) // notices a change made by another process within CacheTTL
		case <-hourly.C:
			s.Sweep(ctx)
			s.trimAudit(ctx)
		case <-daily.C:
			s.refreshSAMLMetadata(ctx)
		}
	}
}

// refreshSAMLMetadata fetches the metadata given by address again, every 24 hours.
func (s *Service) refreshSAMLMetadata(ctx context.Context) {
	providerMu.Lock()
	defer providerMu.Unlock()
	changed := false
	for _, rt := range s.provs.all() {
		if rt.row.Kind != KindSAML || rt.cfg.MetadataURL == "" {
			continue
		}
		if err := s.refresh(ctx, rt); err != nil && !errors.Is(err, errUnreachable) {
			s.log.Warn("the SAML metadata could not be used", "provider", rt.row.Slug, "reason", sanitizeLog(err.Error()))
		}
		changed = true
	}
	if changed {
		var slugs []string
		for _, rt := range s.provs.all() {
			slugs = append(slugs, rt.row.Slug)
		}
		if err := s.reloadProviders(ctx, slugs...); err != nil {
			s.log.Warn("could not reload the providers", "error", err)
		}
	}
}

// Sweep deletes expired sessions and long-expired invites once.
func (s *Service) Sweep(ctx context.Context) {
	now := s.now()
	sec := s.sec(ctx)
	if n, err := store.DeleteExpiredSessions(ctx, s.st.Q(), now.Add(-sec.idle), now.Add(-sec.max)); err != nil {
		s.log.Warn("could not delete expired sessions", "error", err)
	} else if n > 0 {
		s.log.Info("deleted expired sessions", "count", n)
	}
	if _, err := store.DeleteOldInvites(ctx, s.st.Q(), now.Add(-30*24*time.Hour)); err != nil {
		s.log.Warn("could not delete old invites", "error", err)
	}
}

// PasskeyRequiredOf says whether the person must use a passkey, by themselves
// or by their role (for showing it; sign-in also needs passkeys to work).
func (s *Service) PasskeyRequiredOf(u store.User) bool {
	st := s.secPtr.Load()
	return u.PasskeyRequired || (st != nil && st.roles[u.Role])
}

// Counts are how many passkeys and identities each user has.
type Counts struct{ Passkeys, SSO map[string]int }

// UserCounts counts the passkeys and identities of every user: two queries.
func (s *Service) UserCounts(ctx context.Context) (*Counts, error) {
	p, err := store.PasskeyCounts(ctx, s.st.Q())
	if err != nil {
		return nil, err
	}
	i, err := store.IdentityCounts(ctx, s.st.Q())
	if err != nil {
		return nil, err
	}
	return &Counts{Passkeys: p, SSO: i}, nil
}

// UserPasskeys lists a user's passkeys for an administrator of the scope.
func (s *Service) UserPasskeys(ctx context.Context, sc store.Scope, userID string) ([]PasskeyView, error) {
	if _, err := store.GetUser(ctx, s.st.Q(), sc, userID); errors.Is(err, store.ErrNotFound) {
		return nil, errf(NotFound, "no such user")
	} else if err != nil {
		return nil, err
	}
	return s.Passkeys(ctx, userID)
}

// PublicInfo is what GET /api/auth/providers says, from memory.
type PublicInfo struct {
	Providers []PublicProvider
	Mode      string
	Local     bool
	SSO       bool
	Passkey   bool
}

// PublicInfo describes the sign-in page. It asks the database nothing.
func (s *Service) PublicInfo() PublicInfo {
	st := s.secPtr.Load()
	info := PublicInfo{Providers: s.provs.publicList(), Mode: s.effectiveMode(st)}
	info.Local, info.SSO = info.Mode != ModeSSORedirect, info.Mode != ModeLocalOnly
	info.Passkey = s.passkeysOn(st)
	return info
}
