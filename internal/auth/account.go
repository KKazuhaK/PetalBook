package auth

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// AccountView is what GET /api/account shows.
type AccountView struct {
	User        store.User
	HasPassword bool
	Identities  []IdentityView
}

// IdentityView is an attached identity as shown to its owner.
type IdentityView struct {
	ID       string
	Provider string
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
		v.Identities[i] = IdentityView{ID: id.ID, Provider: s.providerName(id.Issuer), Label: id.Label, Created: id.Created}
	}
	return v, nil
}

func (s *Service) providerName(issuer string) string {
	if s.oidc != nil && s.oidc.cfg.Issuer == issuer {
		return s.oidc.cfg.Name
	}
	if u, err := url.Parse(issuer); err == nil && u.Host != "" {
		return u.Host
	}
	return issuer
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
	if email != nil {
		if e, err = cleanEmail(*email); err != nil {
			return nil, err
		}
	}
	if err := store.UpdateProfile(ctx, s.st.Q(), u.ID, n, e); err != nil {
		return nil, err
	}
	s.dropUser(u.ID)
	u.Name, u.Email = n, e
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
		ukey := userKey(p.User.Username)
		if wait := s.reserve("ip:"+ip, ukey); wait > 0 {
			return retryAfter(wait)
		}
		ok := false
		if len(*current) <= MaxPasswordBytes {
			if ok, _, err = s.hasher.verify(ctx, hash, *current); err != nil {
				s.byIP.refund("ip:" + ip)
				s.byUser.refund(ukey)
				return err
			}
		}
		if !ok {
			return errf(Forbidden, "the current password is wrong")
		}
		s.byIP.refund("ip:" + ip)
		s.byUser.refund(ukey)
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
				return errf(Conflict, "this is the only way you can sign in; set a password first")
			} else if err != nil {
				return err
			}
		}
		return store.DeleteIdentity(ctx, q, identityID, userID)
	})
}

// UserPatch is a change an administrator makes to another account.
type UserPatch struct {
	Role     *string
	Disabled *bool
	// RemoteExempt leaves the spaces the user owns out of the storage policy's
	// requirement of a remote (milestone 3.1).
	RemoteExempt *bool
}

// UpdateUser applies a patch. It refuses to leave the instance without an
// enabled administrator, and an administrator disabling themself.
func (s *Service) UpdateUser(ctx context.Context, actorID, targetID string, patch UserPatch) (*store.User, error) {
	if patch.Role != nil {
		if err := checkRole(*patch.Role); err != nil {
			return nil, err
		}
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	var out *store.User
	exemptChanged := false
	err := s.st.Tx(ctx, func(q store.Q) error {
		u, err := store.GetUser(ctx, q, targetID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errf(NotFound, "no such user")
			}
			return err
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
		losesAdmin := u.Role == "admin" && !u.Disabled && (role != "admin" || disabled)
		if losesAdmin {
			if n, err := store.CountEnabledAdmins(ctx, q); err != nil {
				return err
			} else if n <= 1 {
				return errf(Conflict, "there must be at least one enabled administrator")
			}
		}
		if role != u.Role {
			if err := store.SetRole(ctx, q, u.ID, role); err != nil {
				return err
			}
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
		}
		if patch.RemoteExempt != nil && *patch.RemoteExempt != u.RemoteExempt {
			if err := store.SetRemoteExempt(ctx, q, u.ID, *patch.RemoteExempt); err != nil {
				return err
			}
			u.RemoteExempt = *patch.RemoteExempt
			exemptChanged = true
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
	if exemptChanged {
		s.cfg.Spaces.PolicyChanged()
	}
	return out, nil
}

// --- invites -----------------------------------------------------------------

// NewInvite is an invite just made; Token is shown once.
type NewInvite struct {
	store.Invite
	Token string
}

// CreateInvite makes an invite that works once. days is 1-30; nil means 7. An
// explicit 0 is refused, not taken for "not given".
func (s *Service) CreateInvite(ctx context.Context, actorID, role string, daysGiven *int) (*NewInvite, error) {
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
	inv := store.Invite{TokenHash: hashToken(token), Role: role, CreatedBy: actorID, Created: now, Expires: now.Add(time.Duration(days) * 24 * time.Hour)}
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
func (s *Service) OpenInvites(ctx context.Context) ([]store.Invite, error) {
	return store.ListOpenInvites(ctx, s.st.Q(), s.now())
}

// DeleteInvite revokes an invite.
func (s *Service) DeleteInvite(ctx context.Context, id string) error {
	if err := store.DeleteInvite(ctx, s.st.Q(), id); errors.Is(err, store.ErrNotFound) {
		return errf(NotFound, "no such invite")
	} else {
		return err
	}
}

// CheckInvite tells whether a token names an invite that can still be used.
func (s *Service) CheckInvite(ctx context.Context, token, ip string) (*store.Invite, error) {
	if err := s.throttle(ip); err != nil {
		return nil, err
	}
	if wait := s.byIP.reserve("ip:" + ip); wait > 0 {
		return nil, retryAfter(wait)
	}
	inv, err := s.findInvite(ctx, token)
	if err == nil {
		s.byIP.refund("ip:" + ip)
	}
	return inv, err
}

func (s *Service) findInvite(ctx context.Context, token string) (*store.Invite, error) {
	if token == "" || len(token) > 256 {
		return nil, errf(NotFound, "that invite is not valid")
	}
	inv, err := store.FindUsableInvite(ctx, s.st.Q(), hashToken(token), s.now())
	if errors.Is(err, store.ErrNotFound) {
		return nil, errf(NotFound, "that invite is not valid")
	}
	return inv, err
}

// AcceptInvite makes the account an invite is for, and signs it in.
func (s *Service) AcceptInvite(ctx context.Context, token, username, name, password, ip string) (*store.User, string, error) {
	inv, err := s.CheckInvite(ctx, token, ip)
	if err != nil {
		return nil, "", err
	}
	u, err := s.CreateUser(ctx, CreateParams{Username: username, Name: name, Role: inv.Role, Password: password, InviteID: inv.ID})
	if err != nil {
		return nil, "", err
	}
	tok, err := s.issueSession(ctx, u.ID)
	return u, tok, err
}

// Run keeps the tables tidy until ctx ends: expired sessions and invites go.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		s.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sweep deletes expired sessions and long-expired invites once.
func (s *Service) Sweep(ctx context.Context) {
	now := s.now()
	if n, err := store.DeleteExpiredSessions(ctx, s.st.Q(), now.Add(-s.cfg.SessionIdle), now.Add(-s.cfg.SessionMax)); err != nil {
		s.log.Warn("could not delete expired sessions", "error", err)
	} else if n > 0 {
		s.log.Info("deleted expired sessions", "count", n)
	}
	if _, err := store.DeleteOldInvites(ctx, s.st.Q(), now.Add(-30*24*time.Hour)); err != nil {
		s.log.Warn("could not delete old invites", "error", err)
	}
}
