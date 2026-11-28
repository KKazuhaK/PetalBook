package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/notify"
	"petalbook.app/server/internal/store"
)

// AccountView is what GET /api/account shows.
type AccountView struct {
	User        store.User
	HasPassword bool
	Passkeys    []PasskeyView
	// PasskeyRequired: the person must have a passkey (by themselves or by their role).
	PasskeyRequired bool
	// Methods is the one list of ways to sign in: the password, each linked
	// sign-in, each passkey, in that order. CanUnlink: removing one of them
	// would still leave a way that works now.
	Methods   []MethodView
	CanUnlink bool
}

// Kinds of MethodView.
const (
	MethodPassword = "password"
	MethodIdentity = "identity"
	MethodPasskey  = "passkey"
)

// MethodView is one way the person signs in. Added is when it was made (for
// the password, when it was last set); LastUsed is zero when it never signed
// the person in. Removable says whether the person may remove it now: what is
// left must still be a way that works.
type MethodView struct {
	Type      string
	ID        string
	Added     time.Time
	LastUsed  time.Time
	Removable bool
	// Identity: the provider's name, slug, kind and class, and the label of the
	// identity there (an address or a login).
	Provider, ProviderSlug, Kind, Class, Label string
	// Passkey.
	Name   string
	Synced bool
}

// LinkView is a linked account (a sign-in link) as shown to its owner.
type LinkView struct {
	ID         string
	ProviderID string
	Provider   string
	Slug       string
	Kind       string
	Label      string
	SignIn     bool
	Created    time.Time
}

// Links lists a person's linked accounts, oldest first.
func (s *Service) Links(ctx context.Context, userID string) ([]LinkView, error) {
	ids, err := store.ListLinks(ctx, s.st.Q(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]LinkView, len(ids))
	for i, l := range ids {
		name, slug, kind := s.providerByID(l.ProviderID)
		out[i] = LinkView{ID: l.ID, ProviderID: l.ProviderID, Provider: name, Slug: slug, Kind: kind, Label: l.Label, SignIn: l.SignIn, Created: l.Created}
	}
	return out, nil
}

// Account describes the signed-in user's sign-in methods.
func (s *Service) Account(ctx context.Context, u store.User) (*AccountView, error) {
	v := &AccountView{User: u}
	q := s.st.Q()
	set, used, err := store.PasswordInfo(ctx, q, u.ID)
	switch {
	case err == nil:
		v.HasPassword = true
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	if v.Passkeys, err = s.Passkeys(ctx, u.ID); err != nil {
		return nil, err
	}
	links, err := store.ListLinks(ctx, q, u.ID)
	if err != nil {
		return nil, err
	}
	// The same count as the guards of unlinking and deleting: a method may go
	// while a way that works remains.
	pkIDs := make([]string, len(v.Passkeys))
	for i, p := range v.Passkeys {
		pkIDs[i] = p.ID
	}
	ways := s.signInWaysOf(ctx, &u, v.HasPassword, links, pkIDs)
	v.CanUnlink = ways.total() >= 2
	if v.HasPassword {
		// The password has no remove action: it is changed, not deleted.
		v.Methods = append(v.Methods, MethodView{Type: MethodPassword, Added: set, LastUsed: used})
	}
	for _, l := range links {
		name, slug, kind := s.providerByID(l.ProviderID)
		class := ""
		if rt := s.provs.getID(l.ProviderID); rt != nil {
			class = rt.row.Class
		}
		v.Methods = append(v.Methods, MethodView{Type: MethodIdentity, ID: l.ID, Added: l.Created, LastUsed: l.LastUsed, Removable: ways.removable(wayLink, l.ID),
			Provider: name, ProviderSlug: slug, Kind: kind, Class: class, Label: l.Label})
	}
	for _, p := range v.Passkeys {
		v.Methods = append(v.Methods, MethodView{Type: MethodPasskey, ID: p.ID, Added: p.Created, LastUsed: p.LastUsed, Removable: ways.removable(wayPasskey, p.ID),
			Name: p.Name, Synced: p.Synced})
	}
	return v, nil
}

// providerByID names a provider by its id: from memory, no query.
func (s *Service) providerByID(id string) (name, slug, kind string) {
	if rt := s.provs.getID(id); rt != nil {
		return rt.row.Name, rt.row.Slug, rt.row.Kind
	}
	return "", "", ""
}

// UpdateProfile changes the signed-in user's name, username and email; a nil
// field is left alone. A change is audited with what it replaced.
func (s *Service) UpdateProfile(ctx context.Context, u store.User, name, username, email *string, ip string) (*store.User, error) {
	n, e := u.Name, u.Email
	un := u.Username
	var err error
	if username != nil {
		if un, err = NormalizeUsername(*username); err != nil {
			return nil, err
		}
		if un != u.Username {
			if barred, err := s.retiredName(ctx, s.st.Q(), un); err != nil {
				return nil, err
			} else if barred {
				return nil, errf(Exists, "That username is taken.")
			}
		}
	}
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
	err = s.st.Tx(ctx, func(q store.Q) error {
		if err := store.UpdateProfile(ctx, q, u.ID, n, e, verified); err != nil {
			return emailErr(err)
		}
		if un != u.Username {
			if err := store.SetUsername(ctx, q, u.ID, un); store.IsUnique(err) {
				return errf(Exists, "That username is taken.")
			} else if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	old, changed := map[string]any{}, map[string]any{}
	if n != u.Name {
		old["name"], changed["name"] = u.Name, n
	}
	if un != u.Username {
		old["username"], changed["username"] = u.Username, un
	}
	if e != u.Email {
		old["email"], changed["email"] = u.Email, e
	}
	if len(changed) > 0 {
		changed["old"] = old
		s.Audit(AuditEvent{Kind: "user_changed", Actor: u.ID, User: u.ID, Username: un, IP: ip, Detail: changed})
	}
	if e != u.Email {
		// A link sent to the old address neither confirms the new one nor resets the password.
		if err := store.DeleteEmailTokens(ctx, s.st.Q(), u.ID, ""); err != nil {
			return nil, err
		}
	}
	s.dropUser(u.ID)
	u.Name, u.Username, u.Email, u.EmailVerified = n, un, e, verified
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
		// The temporary password again is not a password of one's own: the
		// administrator who chose it would still know it.
		if p.Held && p.HeldBy == HoldPassword && newPassword == *current {
			return errf(Invalid, "Choose a new password, not the temporary one.")
		}
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
		if err := store.DeleteResetTokens(ctx, q, p.User.ID); err != nil {
			return err
		}
		return store.DeleteUserSessions(ctx, q, p.User.ID, p.SessionID, store.EndPassword, s.now())
	})
	if err != nil {
		return err
	}
	// Choosing a password of one's own ends the hold a temporary one put on
	// this session, unless the person must have a passkey and has none of this
	// host: then the session is held for one, as a password sign-in would be.
	if p.Held && p.HeldBy == HoldPassword {
		needKey := false
		if s.passkeyRequiredOf(ctx, s.sec(ctx), p.User) {
			n, err := store.CountPasskeysForRP(ctx, s.st.Q(), p.User.ID, s.pk.rpID)
			if err != nil {
				return err
			}
			needKey = n == 0
		}
		if needKey {
			if err := store.HoldSessionForPasskey(ctx, s.st.Q(), p.SessionID); err != nil {
				return err
			}
			s.holdForPasskey(p.SessionID)
		} else {
			if err := store.ReleaseSession(ctx, s.st.Q(), p.SessionID); err != nil {
				return err
			}
			s.releaseHold(p.SessionID)
		}
	}
	s.evictUser(p.User.ID, p.SessionID)
	return nil
}

// requirementOf is the site's requirement of a remote now.
func (s *Service) requirement() string {
	if r := s.cfg.Spaces.Storage().Remotes.Require; r != nil {
		return *r
	}
	return ""
}

// UnlinkAccount removes one of the user's linked accounts unless that would
// leave no way to sign in (last_method).
func (s *Service) UnlinkAccount(ctx context.Context, userID, linkID string) error {
	return s.unlinkAccount(ctx, userID, linkID, userID, "user", "")
}

// unlinkAccount is UnlinkAccount done by actor (the person, or by "admin" a
// site administrator).
func (s *Service) unlinkAccount(ctx context.Context, userID, linkID, actor, by, ip string) error {
	s.credMu.Lock()
	defer s.credMu.Unlock()
	var lost []string
	require := s.requirement() // read before the transaction
	err := s.st.Tx(ctx, func(q store.Q) error {
		ids, err := store.ListLinks(ctx, q, userID)
		if err != nil {
			return err
		}
		found := false
		for _, i := range ids {
			found = found || i.ID == linkID
		}
		if !found {
			return errf(NotFound, "No such linked account.")
		}
		// What is left must be a way the person can sign in with now: a
		// password or a passkey that sign-in still accepts from them, or a
		// connected account of a provider that is set up and on.
		ways, err := s.loadWays(ctx, q, userID)
		if err != nil {
			return err
		}
		if !ways.removable(wayLink, linkID) {
			if by != "user" {
				return coded(409, "last_method", "This is the only way this person can sign in; they need a password or a passkey first.")
			}
			return coded(409, "last_method", "This is the only way you can sign in; set a password or a passkey first.")
		}
		for _, i := range ids {
			if i.ID == linkID {
				_, slug, _ := s.providerByID(i.ProviderID)
				defer s.AuditIn(ctx, AuditEvent{Kind: "identity_unlinked", Actor: actor, User: userID, IP: ip, Detail: map[string]any{"provider": slug, "by": by}})
			}
		}
		// The tokens it held go with it: the remotes that synced with them lose
		// access in this transaction.
		lost, err = store.DeleteLink(ctx, q, linkID, userID, require, s.now())
		return err
	})
	if err == nil {
		s.cfg.Spaces.AccessLost(lost) // the spaces show it at once
	}
	return err
}

// UserPatch is a change an administrator makes to another account.
type UserPatch struct {
	// Name and Username change how the person is shown and signs in. Both are
	// the person's own to change as well; an administrator may too.
	Name            *string
	Username        *string
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
func (s *Service) UpdateUser(ctx context.Context, actor store.User, access authz.Subject, ip, targetID string, patch UserPatch) (*store.User, error) {
	actorID := actor.ID
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
	// A waiting account (made for its tenant's SSO, which finds it by its
	// address) keeps an address that tenant can vouch for: another would only
	// lock that address out of every other sign-in.
	if patch.Email != nil {
		if wait, err := store.AwaitingSSO(ctx, s.st.Q(), targetID); err != nil {
			return nil, err
		} else if wait != "" && (email == "" || !s.ssoCanVouch(ctx, wait, email)) {
			return nil, coded(400, "email_domain_not_verified", "This account waits for its organisation's single sign-on, which finds it by its address: it has to stay on a verified domain of the organisation.")
		}
	}
	var name, username string
	if patch.Name != nil {
		n, err := cleanDisplayName(*patch.Name)
		if err != nil {
			return nil, err
		}
		name = n
	}
	if patch.Username != nil {
		u, err := NormalizeUsername(*patch.Username)
		if err != nil {
			return nil, err
		}
		username = u
	}
	st := s.sec(ctx)
	if patch.PasskeyRequired != nil && *patch.PasskeyRequired && !s.passkeysOn(st) {
		return nil, coded(400, "passkey_unavailable", "Passkeys cannot be used here: they need a --public-url that is https, and passkeys switched on.")
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	var out *store.User
	changed := map[string]any{}
	// old holds what each changed field was, for the audit row (never a password).
	old := map[string]any{}
	exemptChanged := false
	err := s.st.Tx(ctx, func(q store.Q) error {
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
		if patch.Name != nil && name != u.Name {
			if err := store.UpdateProfile(ctx, q, u.ID, name, u.Email, u.EmailVerified); err != nil {
				return err
			}
			old["name"], changed["name"] = u.Name, name
			u.Name = name
		}
		if patch.Username != nil && username != u.Username {
			if barred, err := s.retiredName(ctx, q, username); err != nil {
				return err
			} else if barred {
				return errf(Exists, "That username is taken.")
			}
			if err := store.SetUsername(ctx, q, u.ID, username); store.IsUnique(err) {
				return errf(Exists, "That username is taken.")
			} else if err != nil {
				return err
			}
			old["username"], changed["username"] = u.Username, username
			u.Username = username
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
		// While the tenant's ssoOnly is on, its last administrator with a
		// password stays one, enabled (3.6 d4).
		if !store.IsAdminRole(role) || disabled {
			if last, err := s.LastLocalAdmin(ctx, q, *u); err != nil {
				return err
			} else if last {
				return errLastLocalAdmin()
			}
		}
		if role != u.Role {
			if err := store.SetRole(ctx, q, u.ID, role); err != nil {
				return err
			}
			old["role"], changed["role"] = u.Role, role
		}
		if site != u.IsSiteAdmin() {
			if err := store.SetSiteAdmin(ctx, q, u.ID, site); err != nil {
				return err
			}
			old["siteAdmin"], changed["siteAdmin"] = u.IsSiteAdmin(), site
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
			old["disabled"], changed["disabled"] = u.Disabled, disabled
		}
		if patch.PasskeyRequired != nil && *patch.PasskeyRequired != u.PasskeyRequired {
			if err := store.SetPasskeyRequired(ctx, q, u.ID, *patch.PasskeyRequired); err != nil {
				return err
			}
			old["passkeyRequired"], changed["passkeyRequired"] = u.PasskeyRequired, *patch.PasskeyRequired
			u.PasskeyRequired = *patch.PasskeyRequired
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
					old["email"], changed["email"] = u.Email, newEmail
					// Links sent to the old address stop working.
					if err := store.DeleteEmailTokens(ctx, q, u.ID, ""); err != nil {
						return err
					}
				}
				if verified != u.EmailVerified {
					old["emailVerified"], changed["emailVerified"] = u.EmailVerified, verified
				}
				u.Email, u.EmailVerified = newEmail, verified
			}
		}
		if patch.RemoteExempt != nil && *patch.RemoteExempt != u.RemoteExempt {
			if err := store.SetRemoteExempt(ctx, q, u.ID, *patch.RemoteExempt); err != nil {
				return err
			}
			old["remoteExempt"], changed["remoteExempt"] = u.RemoteExempt, *patch.RemoteExempt
			u.RemoteExempt = *patch.RemoteExempt
			exemptChanged = true
		}
		u.Role, u.Disabled = role, disabled
		out = u
		if len(changed) == 0 {
			return nil
		}
		// The new values are the keys of the detail; `old` holds the ones they replaced.
		detail := map[string]any{"old": old}
		for k, v := range changed {
			detail[k] = v
		}
		e := notify.Entry{Actor: actorID, User: u.ID, Username: u.Username, IP: ip, Detail: detail, Now: s.now()}
		if fields := accountChangeFields(actor, u.ID, changed); len(fields) > 0 {
			e.To, e.Payload = []string{u.ID}, map[string]any{"fields": fields, "by": actor.Name}
		}
		return notify.WithAudit(ctx, q, notify.AccountChanged, e)
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
	out, err := s.cleanInviteFillFor(ctx, f)
	if err != nil || out.Email == "" {
		return out, err
	}
	if _, err := store.EmailOwner(ctx, s.st.Q(), out.Email); err == nil {
		return out, errEmailTaken()
	} else if !errors.Is(err, store.ErrNotFound) {
		return out, err
	}
	return out, nil
}

// cleanInviteFillFor is CleanInviteFill without the look for an account that
// holds the address (a batch looks for all of them at once).
func (s *Service) cleanInviteFillFor(ctx context.Context, f InviteFill) (InviteFill, error) {
	out, err := cleanInviteFill(f)
	if err != nil || out.Email == "" {
		return out, err
	}
	if _, err := s.cleanEmailFor(ctx, out.Email); err != nil {
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
func (s *Service) CreateInvite(ctx context.Context, actor store.User, access authz.Subject, tenantID, role string, daysGiven *int, siteAdmin *bool, fill ...InviteFill) (*NewInvite, error) {
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
			slugs = append(slugs, rt.row.ID)
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
	if n, err := store.DeleteExpiredSessions(ctx, s.st.Q(), now.Add(-sec.idle), now.Add(-sec.max), now, s.sessionCutoffs(ctx, sec, now)); err != nil {
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
	if err := store.SweepJoinRequests(ctx, s.st.Q(), now); err != nil {
		s.log.Warn("could not sweep the requests to join", "error", err)
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
	return u.PasskeyRequired || (st != nil && s.passkeyRoleHeld(context.Background(), st, tenantOfPerson(&u), u.Role))
}

// Counts are how many passkeys and identities each user has.
type Counts struct{ Passkeys, SSO map[string]int }

// UserCounts counts the passkeys and identities of every user: two queries.
func (s *Service) UserCounts(ctx context.Context) (*Counts, error) {
	p, err := store.PasskeyCounts(ctx, s.st.Q())
	if err != nil {
		return nil, err
	}
	i, err := store.LinkCounts(ctx, s.st.Q())
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
	// IdentifierFirst and SSOEntry: some verified domain routes its sign-in to
	// a provider that can sign in (3.6 d4), and the stored mode asks for the
	// address first (dual, sso_first) or offers the SSO entry (any but
	// local_only).
	IdentifierFirst, SSOEntry bool
}

// PublicInfo describes the sign-in page. It asks the database nothing: the
// sign-in policy is read through the settings' cache, so a change made by an
// administrator shows on the page at once.
func (s *Service) PublicInfo(ctx context.Context) PublicInfo {
	st := s.sec(ctx)
	info := PublicInfo{Providers: s.provs.publicList(), Mode: s.effectiveMode(st)}
	info.Local, info.SSO = info.Mode != ModeSSORedirect, info.Mode != ModeLocalOnly
	info.Passkey = s.passkeysOn(st)
	s.identifierFirst(ctx, st.Login.Mode, &info)
	return info
}

// PublicInfoFor is PublicInfo for the sign-in page of one tenant: the
// providers its people may use (PublicProvidersFor), and the mode of the
// tenant (signin.mode as it has it; see modeOf), which never shows a looser
// page than the server enforces.
func (s *Service) PublicInfoFor(ctx context.Context, tenant string) PublicInfo {
	st := s.sec(ctx)
	info := PublicInfo{Providers: s.PublicProvidersFor(ctx, tenant), Mode: s.modeOf(ctx, st, tenant)}
	info.Local, info.SSO = info.Mode != ModeSSORedirect, info.Mode != ModeLocalOnly
	info.Passkey = s.passkeysOn(st)
	s.identifierFirst(ctx, s.storedModeOf(ctx, st, tenant), &info)
	return info
}

// Kinds of way to sign in that can be removed.
const (
	wayLink    = "link"
	wayPasskey = "passkey"
)

// signInWays is what a person can sign in with now: the password and the
// passkeys when local sign-in is not refused to them (the passkeys also need to
// be on), and each linked account whose provider is set up and on. It is the
// one rule behind the guards of unlinking and deleting a passkey and behind the
// `removable` of the account's list of methods.
type signInWays struct {
	password bool
	links    map[string]bool // id -> works now
	passkeys map[string]bool
}

func (s *Service) signInWaysOf(ctx context.Context, u *store.User, hasPassword bool, links []store.Link, passkeyIDs []string) *signInWays {
	w := &signInWays{links: map[string]bool{}, passkeys: map[string]bool{}}
	st := s.sec(ctx)
	local := !s.localRefusedUser(ctx, st, u)
	w.password = hasPassword && local
	for _, id := range passkeyIDs {
		w.passkeys[id] = local && s.passkeysOn(st)
	}
	for _, l := range links {
		rt := s.provs.getID(l.ProviderID)
		w.links[l.ID] = rt != nil && s.signInOn(ctx, rt)
	}
	return w
}

// loadWays reads what signInWaysOf needs, in q.
func (s *Service) loadWays(ctx context.Context, q store.Q, userID string) (*signInWays, error) {
	u, err := store.GetUser(ctx, q, store.AllTenants(), userID)
	if err != nil {
		return nil, err
	}
	hasPassword := true
	if _, err := store.GetPasswordHash(ctx, q, userID); errors.Is(err, store.ErrNotFound) {
		hasPassword = false
	} else if err != nil {
		return nil, err
	}
	links, err := store.ListLinks(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	pks, err := store.ListPasskeys(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(pks))
	for i, p := range pks {
		ids[i] = p.ID
	}
	return s.signInWaysOf(ctx, u, hasPassword, links, ids), nil
}

func (w *signInWays) total() int {
	n := 0
	if w.password {
		n++
	}
	for _, ok := range w.links {
		if ok {
			n++
		}
	}
	for _, ok := range w.passkeys {
		if ok {
			n++
		}
	}
	return n
}

// remaining counts the ways that work once one link or passkey is gone.
func (w *signInWays) remaining(kind, id string) int {
	n := w.total()
	m := w.links
	if kind == wayPasskey {
		m = w.passkeys
	}
	if m[id] {
		n--
	}
	return n
}

// removable says whether the link or passkey may go: a way must remain.
func (w *signInWays) removable(kind, id string) bool { return w.remaining(kind, id) > 0 }

// accountChangeFields says which of what changed someone else's account tells
// the person (name, username, address): the fields, never the values. Nothing is
// said of what they did themselves.
func accountChangeFields(actor store.User, targetID string, changed map[string]any) []string {
	if actor.ID == targetID {
		return nil
	}
	var fields []string
	for _, k := range []string{"name", "username", "email", "emailVerified"} {
		if _, ok := changed[k]; ok {
			fields = append(fields, k)
		}
	}
	return fields
}

// SetTemporaryPassword gives a person a password an administrator chose. It
// is replaced at their next sign-in (the session that signs in with it may only
// choose a new one), and every session they have ends now. The administrator
// never sees a password after this: it is the one they typed. Who may change
// whom is as for UpdateUser: a site administrator's password needs
// site.admins.manage, and a tenant's administrator reaches their own tenant.
func (s *Service) SetTemporaryPassword(ctx context.Context, actor store.User, access authz.Subject, ip, targetID, password string) error {
	if err := CheckPassword(password); err != nil {
		return err
	}
	u, err := store.GetUser(ctx, s.st.Q(), store.ScopeOf(actor), targetID)
	if errors.Is(err, store.ErrNotFound) {
		return errf(NotFound, "No such user.")
	} else if err != nil {
		return err
	}
	if u.IsSiteAdmin() && !access.Can(authz.SiteAdminsManage, "") {
		return errf(Forbidden, "Only a site administrator may change a site administrator.")
	}
	hash, err := s.hasher.hash(ctx, password)
	if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q store.Q) error {
		if err := store.SetTemporaryPassword(ctx, q, u.ID, hash, s.now()); err != nil {
			return err
		}
		// A reset link made before this stops working, and so does every session.
		if err := store.DeleteResetTokens(ctx, q, u.ID); err != nil {
			return err
		}
		if err := store.DeleteUserSessions(ctx, q, u.ID, "", store.EndPassword, s.now()); err != nil {
			return err
		}
		return notify.WithAudit(ctx, q, notify.PasswordSet, notify.Entry{Actor: actor.ID, User: u.ID, Username: u.Username, IP: ip,
			Detail: map[string]any{"temporary": true}, To: []string{u.ID}, Payload: map[string]any{"by": actor.Name}, Now: s.now()})
	})
	if err != nil {
		return err
	}
	s.evictUser(u.ID, "")
	return nil
}

// SigninLinkRemovable says whether the person could still sign in some other
// way if the sign-in of one of their links (linkID) were gone: a password, a
// passkey, or a link of another provider that works now. It is what the
// last_method rule asks, for the callers that change a link without deleting it
// (connecting repositories with another account moves the link, and its sign-in
// with it). A link that does not sign the person in is removable. q is the
// transaction of the change, so that the answer holds for it.
func (s *Service) SigninLinkRemovable(ctx context.Context, q store.Q, userID, linkID string) (bool, error) {
	ids, err := store.ListLinks(ctx, q, userID)
	if err != nil {
		return false, err
	}
	found := false
	for _, l := range ids {
		found = found || l.ID == linkID
	}
	if !found {
		return true, nil
	}
	ways, err := s.loadWays(ctx, q, userID)
	if err != nil {
		return false, err
	}
	return ways.removable(wayLink, linkID), nil
}

// ProviderInfo names a sign-in provider by its id, from memory: false when it is
// not one (a GitLab or Gitea host, which only holds repositories).
func (s *Service) ProviderInfo(id string) (name, slug, kind string, ok bool) {
	rt := s.provs.getID(id)
	if rt == nil {
		return "", "", "", false
	}
	return rt.row.Name, rt.row.Slug, rt.row.Kind, true
}
