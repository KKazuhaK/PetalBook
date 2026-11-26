package auth

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/store"
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
		if e, err = s.cleanEmailFor(ctx, *email); err != nil {
			return nil, err
		}
		if e != u.Email {
			verified = false // a person changing their own address clears the mark
		}
	}
	// Another account holding the address, verified or not, is 409
	// email_taken: an address held unverified moves only to who proves it.
	if err := store.UpdateProfile(ctx, s.st.Q(), u.ID, n, e, verified); err != nil {
		return nil, emailErr(err)
	}
	if e != u.Email {
		// A link sent to the old address neither confirms the new one nor resets the password.
		if err := store.DeleteEmailTokens(ctx, s.st.Q(), u.ID, ""); err != nil {
			return nil, err
		}
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
			return errf(Invalid, "Current is required: send the password you use now.")
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
			return errf(Forbidden, "The current password is wrong.")
		}
		s.refundIP(ip)
	} else if current != nil && *current != "" {
		return errf(Invalid, "This account has no password yet; leave current out.")
	}
	nh, err := s.hasher.hash(ctx, newPassword)
	if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q store.Q) error {
		if err := store.SetPasswordHash(ctx, q, p.User.ID, nh, s.now()); err != nil {
			return err
		}
		// A password change makes a reset link made before it unusable.
		if err := store.DeleteEmailTokens(ctx, q, p.User.ID, store.TokenReset); err != nil {
			return err
		}
		return store.DeleteUserSessions(ctx, q, p.User.ID, p.SessionID, store.EndPassword, s.now())
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
	return s.unlinkIdentity(ctx, userID, identityID, userID, "user", "")
}

// unlinkIdentity is UnlinkIdentity done by actor (the person, or by "admin" a
// site administrator).
func (s *Service) unlinkIdentity(ctx context.Context, userID, identityID, actor, by, ip string) error {
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
			return errf(NotFound, "No such identity.")
		}
		// What is left must be a way the person can sign in with now: a
		// password or a passkey that sign-in still accepts from them, or a
		// connected account of a provider that is set up and on.
		ways, err := s.waysToSignIn(ctx, q, userID, identityID, ids)
		if err != nil {
			return err
		}
		if ways == 0 {
			if by != "user" {
				return errf(Conflict, "This is the only way this person can sign in; they need a password or a passkey first.")
			}
			return errf(Conflict, "This is the only way you can sign in; set a password or a passkey first.")
		}
		for _, i := range ids {
			if i.ID == identityID {
				_, slug, _ := s.providerOf(i.Issuer)
				defer s.Audit(AuditEvent{Kind: "identity_unlinked", Actor: actor, User: userID, IP: ip, Detail: map[string]any{"provider": slug, "by": by}})
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
	// MakeSiteAdmin makes an administrator one of the whole server, or ends
	// it; only who manages site administrators may send it.
	MakeSiteAdmin *bool
	// RemoteExempt leaves the spaces the user owns out of the storage policy's
	// requirement of a remote (milestone 3.1). The requirement is the site's,
	// so only a site administrator may send it.
	RemoteExempt *bool
}

// UpdateUser applies a patch. It refuses to leave the instance without an
// enabled administrator, an administrator disabling themself, and, while
// password sign-in is refused to everyone else, leaving no enabled
// administrator with a password.
func (s *Service) UpdateUser(ctx context.Context, actor store.User, ip, targetID string, patch UserPatch) (*store.User, error) {
	actorID := actor.ID
	access, err := s.SubjectOf(ctx, actor)
	if err != nil {
		return nil, err
	}
	// What the actor may do beyond changing people of their tenant: change
	// administrators of the whole server (and make or end one), and exempt a
	// person from the site's requirement of a remote.
	admins, storage := access.Can(authz.SiteAdminsManage, ""), access.Can(authz.SiteStorageManage, "")
	// An address is vouched for, for merging a sign-in, by who manages people
	// across the site; one of a tenant administrator alone is marked as such.
	vouches := access.SiteWide(authz.TenantUsersManage)
	if patch.Role != nil {
		if err := checkRole(*patch.Role); err != nil {
			return nil, err
		}
	}
	var email string
	if patch.Email != nil {
		e, err := s.cleanEmailFor(ctx, *patch.Email)
		if err != nil {
			return nil, err
		}
		email = e
	}
	st := s.sec(ctx)
	if patch.PasskeyRequired != nil && *patch.PasskeyRequired && !s.passkeysOn(st) {
		return nil, coded(400, "passkey_unavailable", "Passkeys cannot be used here: they need a --public-url that is https, and passkeys switched on.")
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	var out *store.User
	changed := map[string]any{}
	exemptChanged := false
	err = s.st.Tx(ctx, func(q store.Q) error {
		// A person of another tenant is no person at all.
		u, err := store.GetUser(ctx, q, store.ScopeOf(actor), targetID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errf(NotFound, "No such user.")
			}
			return err
		}
		if u.IsSiteAdmin() && !admins {
			return errf(Forbidden, "Only a site administrator may change a site administrator.")
		}
		if patch.MakeSiteAdmin != nil && !admins {
			return errf(Forbidden, "Only a site administrator may make or end a site administrator.")
		}
		if patch.RemoteExempt != nil && !storage {
			return errf(Forbidden, "Only a site administrator may exempt a person from the site's requirement of a remote.")
		}
		role, disabled := u.Role, u.Disabled
		if patch.Role != nil {
			role = *patch.Role
		}
		if patch.Disabled != nil {
			disabled = *patch.Disabled
		}
		if disabled && !u.Disabled && u.ID == actorID {
			return errf(Conflict, "You cannot disable your own account.")
		}
		// Promoting someone in the default tenant gives what administrators of it
		// always had: the server (a site administrator may say otherwise).
		site := u.IsSiteAdmin()
		switch {
		case patch.MakeSiteAdmin != nil:
			site = *patch.MakeSiteAdmin
		case admins && store.IsAdminRole(role) && !u.IsAdmin() && u.TenantID == store.DefaultTenantID:
			site = true
		}
		if !store.IsAdminRole(role) {
			site = false
		}
		if patch.MakeSiteAdmin != nil && *patch.MakeSiteAdmin && !store.IsAdminRole(role) {
			return errf(Invalid, "A site administrator must be an administrator.")
		}
		// One of a disabled tenant cannot sign in, so is not counted: losing
		// them loses no one.
		losesAdmin := u.IsSiteAdmin() && !u.Disabled && !s.st.TenantDisabled(u.TenantID) && (!site || disabled)
		if losesAdmin {
			if n, err := store.CountEnabledAdmins(ctx, q); err != nil {
				return err
			} else if n <= 1 {
				return errf(Conflict, "There must be at least one enabled administrator.")
			}
			// Whenever ssoOnly is stored, not only while in effect: a provider
			// enabled later, or the page leaving local_only, puts it in effect.
			if st.Login.SSOOnly {
				if _, err := store.GetPasswordHash(ctx, q, u.ID); err == nil {
					if n, err := store.CountEnabledPasswordAdmins(ctx, q); err != nil {
						return err
					} else if n <= 1 {
						return errf(Conflict, "Password sign-in is refused to everyone but administrators: keep at least one enabled administrator with a password.")
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
		if site != u.IsSiteAdmin() {
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
				if err := store.DeleteUserSessions(ctx, q, u.ID, "", store.EndDisabled, s.now()); err != nil {
					return err
				}
			} else if !s.st.TenantDisabled(u.TenantID) {
				// Enabled again (and their tenant is not disabled): their old cookies no longer say disabled.
				if err := store.DeleteUserSessionEnds(ctx, q, u.ID, store.EndDisabled); err != nil {
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
			// An address a tenant administrator alone says is verified is marked
			// so: it shows as verified, but holds the address on the server no
			// more than an unverified one (store.EmailRow.Holds), and gets no
			// link that signs in: they could give a person of their tenant
			// someone else's address.
			by := ""
			if verified && !vouches {
				by = store.VerifiedByTenantAdmin
			}
			// A site administrator's true always counts: it clears the mark of
			// an address only a tenant administrator vouched for, even when the
			// address was already shown as verified.
			clearMark := false
			if verified && vouches && patch.EmailVerified != nil && newEmail == u.Email && u.EmailVerified {
				if cur, err := store.EmailVerifiedBy(ctx, q, u.ID); err != nil {
					return err
				} else {
					clearMark = cur != ""
				}
			}
			if newEmail != u.Email || verified != u.EmailVerified || clearMark {
				if err := store.SetEmail(ctx, q, u.ID, newEmail, verified, by); err != nil {
					return emailErr(err)
				}
				if newEmail != u.Email {
					changed["email"] = newEmail
					// Links sent to the old address stop working.
					if err := store.DeleteEmailTokens(ctx, q, u.ID, ""); err != nil {
						return err
					}
				}
				if verified != u.EmailVerified {
					changed["emailVerified"] = verified
				}
				u.Email, u.EmailVerified = newEmail, verified
			}
		}
		if patch.RemoteExempt != nil && *patch.RemoteExempt != u.RemoteExempt {
			if err := store.SetRemoteExempt(ctx, q, u.ID, *patch.RemoteExempt); err != nil {
				return err
			}
			u.RemoteExempt = *patch.RemoteExempt
			exemptChanged = true
			changed["remoteExempt"] = u.RemoteExempt
		}
		u.Role, u.Disabled = role, disabled
		out = u
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.evictUser(targetID, "")
	if d, ok := changed["disabled"]; ok && d == false {
		s.forgetRefusals()
	}
	// The space manager remembers a user's role and whether they are disabled
	// for a minute (the drafts sweeper asks it); a change here applies at once.
	s.cfg.Spaces.UserChanged(ctx, *out)
	if exemptChanged {
		s.cfg.Spaces.PolicyChanged()
	}
	if len(changed) > 0 {
		s.Audit(AuditEvent{Kind: "user_changed", Actor: actorID, User: out.ID, Username: out.Username, IP: ip, Detail: changed})
	}
	return out, nil
}

// --- invites -----------------------------------------------------------------

// InviteFill is what an invite fills in on the form that accepts it: the
// username and name it starts with, and the address the account gets (not
// verified). Any may be empty.
type InviteFill struct {
	Username, Name, Email string
}

// CleanInviteFill checks and trims what an invite fills in, as CreateInvite
// does; a caller that must refuse bad values before it changes anything (the
// tenant made with its first administrator) calls it first. The address
// follows the site's rule on addresses outside ASCII, and one another account
// holds is 409 email_taken (the invite would make an account it cannot have).
func (s *Service) CleanInviteFill(ctx context.Context, f InviteFill) (InviteFill, error) {
	out, err := cleanInviteFill(f)
	if err != nil || out.Email == "" {
		return out, err
	}
	if _, err := s.cleanEmailFor(ctx, out.Email); err != nil {
		return out, err
	}
	if _, err := store.EmailOwner(ctx, s.st.Q(), out.Email); err == nil {
		return out, errEmailTaken()
	} else if !errors.Is(err, store.ErrNotFound) {
		return out, err
	}
	return out, nil
}

func cleanInviteFill(f InviteFill) (InviteFill, error) {
	var out InviteFill
	if strings.TrimSpace(f.Username) != "" {
		u, err := NormalizeUsername(f.Username)
		if err != nil {
			return out, err
		}
		out.Username = u
	}
	if strings.TrimSpace(f.Name) != "" {
		n, err := cleanDisplayName(f.Name)
		if err != nil {
			return out, err
		}
		out.Name = n
	}
	e, err := cleanEmail(f.Email)
	if err != nil {
		return out, err
	}
	out.Email = e
	return out, nil
}

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
func (s *Service) CreateInvite(ctx context.Context, actor store.User, tenantID, role string, daysGiven *int, siteAdmin *bool, fill ...InviteFill) (*NewInvite, error) {
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
		return nil, errf(Invalid, "Days is 1 to 30.")
	}
	now := s.now().UTC()
	token := randomToken()
	if tenantID == "" {
		tenantID = actor.TenantID
	}
	access, err := s.SubjectOf(ctx, actor)
	if err != nil {
		return nil, err
	}
	// Inviting into another tenant is for who invites across the site; making
	// a site administrator, for who manages them.
	if !access.Can(authz.TenantInvitesManage, tenantID) {
		return nil, errf(NotFound, "No such tenant.")
	}
	admins := access.Can(authz.SiteAdminsManage, "")
	if t, err := store.GetTenant(ctx, s.st.Q(), tenantID); errors.Is(err, store.ErrNotFound) {
		return nil, errf(NotFound, "No such tenant.")
	} else if err != nil {
		return nil, err
	} else if t.Disabled {
		return nil, errf(Conflict, "That tenant is disabled.")
	}
	site := store.IsAdminRole(role) && admins && tenantID == store.DefaultTenantID
	if siteAdmin != nil {
		if *siteAdmin && (!admins || !store.IsAdminRole(role)) {
			return nil, errf(Forbidden, "Only a site administrator can invite a site administrator.")
		}
		site = *siteAdmin
	}
	inv := store.Invite{TokenHash: hashToken(token), Role: role, CreatedBy: actor.ID, Created: now, TenantID: tenantID, SiteAdmin: site,
		Expires: now.Add(time.Duration(days) * 24 * time.Hour)}
	if len(fill) > 0 {
		f, err := s.CleanInviteFill(ctx, fill[0])
		if err != nil {
			return nil, err
		}
		inv.Username, inv.Name, inv.Email = f.Username, f.Name, f.Email
		if f.Username != "" {
			if barred, err := s.retiredName(ctx, s.st.Q(), f.Username); err != nil {
				return nil, err
			} else if barred {
				return nil, errf(Exists, "That username is taken.")
			}
		}
	}
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
		return errf(NotFound, "No such invite.")
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
	s.SweepSessionEnds(ctx)
	s.trimAudit(ctx)
	go s.RunMail(ctx)
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
			s.SweepSessionEnds(ctx)
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

// SessionEndKeep is how long the record of why a session ended is kept.
const SessionEndKeep = 30 * 24 * time.Hour

// SweepSessionEnds forgets the records of ended sessions older than SessionEndKeep (daily).
func (s *Service) SweepSessionEnds(ctx context.Context) {
	if _, err := store.DeleteOldSessionEnds(ctx, s.st.Q(), s.now().Add(-SessionEndKeep)); err != nil {
		s.log.Warn("could not delete old session end records", "error", err)
	}
}

// Sweep deletes expired sessions and long-expired invites once.
func (s *Service) Sweep(ctx context.Context) {
	now := s.now()
	sec := s.sec(ctx)
	if n, err := store.DeleteExpiredSessions(ctx, s.st.Q(), now.Add(-sec.idle), now.Add(-sec.max), now); err != nil {
		s.log.Warn("could not delete expired sessions", "error", err)
	} else if n > 0 {
		s.log.Info("deleted expired sessions", "count", n)
	}
	if _, err := store.DeleteOldInvites(ctx, s.st.Q(), now.Add(-30*24*time.Hour)); err != nil {
		s.log.Warn("could not delete old invites", "error", err)
	}
	if _, err := store.DeleteExpiredRegistrations(ctx, s.st.Q(), now); err != nil {
		s.log.Warn("could not delete expired registrations", "error", err)
	}
	if err := s.CancelClosedRegistrations(ctx); err != nil {
		s.log.Warn("could not cancel pending registrations of closed tenants", "error", err)
	}
	if _, err := store.DeleteOldEmailTokens(ctx, s.st.Q(), now.Add(-24*time.Hour)); err != nil {
		s.log.Warn("could not delete old email tokens", "error", err)
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
		return nil, errf(NotFound, "No such user.")
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

// waysToSignIn counts the ways a person could sign in once the identity without
// is gone, counting only those that work now: a password or a passkey when
// password sign-in is not refused to them, and an identity of a provider that
// is configured and enabled.
func (s *Service) waysToSignIn(ctx context.Context, q store.Q, userID, without string, ids []store.Identity) (int, error) {
	u, err := store.GetUser(ctx, q, store.AllTenants(), userID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, i := range ids {
		if i.ID == without {
			continue
		}
		for _, rt := range s.provs.all() {
			if rt.row.Issuer == i.Issuer && rt.row.Enabled {
				n++
				break
			}
		}
	}
	st := s.sec(ctx)
	if s.ssoOnlyInEffect(st) && !u.IsAdmin() {
		return n, nil
	}
	if _, err := store.GetPasswordHash(ctx, q, userID); err == nil {
		n++
	} else if !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	if s.passkeysOn(st) {
		c, err := store.CountPasskeys(ctx, q, userID)
		if err != nil {
			return 0, err
		}
		n += c
	}
	return n, nil
}
