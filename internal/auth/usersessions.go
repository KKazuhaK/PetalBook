package auth

import (
	"context"
	"errors"
	"time"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/store"
)

// What a site administrator sees of a person's sign-ins and may end (milestone
// 3.10). Another tenant's administrator never reaches these: the API answers
// them as it does for a person who does not exist, and each function checks it
// again.

// SessionView is one sign-in of a person as the administrator sees it.
type SessionView struct {
	ID                string
	UserAgent, Method string
	Created, LastUsed time.Time
	Held              bool
	// Current is the session the administrator is using for this request.
	Current bool
}

// siteTarget is the person looked at by who reads people across the site (a
// site administrator), or NotFound for anyone else asking.
func (s *Service) siteTarget(ctx context.Context, actor *Principal, userID string) (*store.User, error) {
	if actor == nil || !actor.Access.SiteWide(authz.TenantUsersRead) {
		return nil, errf(NotFound, "No such user.")
	}
	u, err := store.GetUser(ctx, s.st.Q(), store.AllTenants(), userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errf(NotFound, "No such user.")
	}
	return u, err
}

// siteTargetToChange is siteTarget for a change: a site administrator's
// sessions and accounts are changed only by who holds site.admins.manage.
func (s *Service) siteTargetToChange(ctx context.Context, actor *Principal, userID string) (*store.User, error) {
	u, err := s.siteTarget(ctx, actor, userID)
	if err != nil {
		return nil, err
	}
	if u.IsSiteAdmin() && !actor.Access.Can(authz.SiteAdminsManage, "") {
		return nil, errf(Forbidden, "Only a site administrator may change a site administrator.")
	}
	return u, nil
}

// UserSessions lists a person's sessions, the one used last first.
func (s *Service) UserSessions(ctx context.Context, actor *Principal, userID string) ([]SessionView, error) {
	if _, err := s.siteTarget(ctx, actor, userID); err != nil {
		return nil, err
	}
	rows, err := store.ListUserSessions(ctx, s.st.Q(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]SessionView, len(rows))
	for i, r := range rows {
		out[i] = SessionView{r.ID, r.UserAgent, r.Method, r.Created, r.LastUsed, r.Held, actor.SessionID != "" && r.ID == actor.SessionID}
	}
	return out, nil
}

// EndUserSession ends one session of a person. The browser that held it is told,
// the next time it comes, that it was signed out elsewhere.
func (s *Service) EndUserSession(ctx context.Context, actor *Principal, userID, id, ip string) error {
	u, err := s.siteTargetToChange(ctx, actor, userID)
	if err != nil {
		return err
	}
	// The delete and the record of why are one change: a session that is gone
	// with no reason kept would tell its browser nothing.
	if err := s.st.Tx(ctx, func(q store.Q) error {
		return store.EndUserSession(ctx, q, id, userID, store.EndElsewhere, s.now())
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errf(NotFound, "No such session.")
		}
		return err
	}
	s.cmu.Lock()
	s.gen++
	delete(s.sessions, id)
	s.cmu.Unlock()
	s.Audit(AuditEvent{Kind: "sessions_ended", Actor: actor.User.ID, User: userID, Username: u.Username, IP: ip, Detail: map[string]any{"by": "admin", "count": 1}})
	return nil
}

// EndUserSessions ends every session of a person and says how many there were.
func (s *Service) EndUserSessions(ctx context.Context, actor *Principal, userID, ip string) (int, error) {
	u, err := s.siteTargetToChange(ctx, actor, userID)
	if err != nil {
		return 0, err
	}
	rows, err := store.ListUserSessions(ctx, s.st.Q(), userID)
	if err != nil {
		return 0, err
	}
	if err := s.SignOutEverywhere(ctx, userID, ""); err != nil {
		return 0, err
	}
	s.Audit(AuditEvent{Kind: "sessions_ended", Actor: actor.User.ID, User: userID, Username: u.Username, IP: ip, Detail: map[string]any{"by": "admin", "count": len(rows)}})
	return len(rows), nil
}

// UserIdentities lists the accounts a person has connected for signing in.
func (s *Service) UserIdentities(ctx context.Context, actor *Principal, userID string) ([]IdentityView, error) {
	if _, err := s.siteTarget(ctx, actor, userID); err != nil {
		return nil, err
	}
	ids, err := store.ListIdentities(ctx, s.st.Q(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]IdentityView, len(ids))
	for i, id := range ids {
		name, slug, kind := s.providerOf(id.Issuer)
		out[i] = IdentityView{ID: id.ID, Provider: name, Slug: slug, Kind: kind, Label: id.Label, Created: id.Created}
	}
	return out, nil
}

// DisconnectIdentity removes one connected account of a person, unless it is the
// only way they can sign in.
func (s *Service) DisconnectIdentity(ctx context.Context, actor *Principal, userID, identityID, ip string) error {
	if _, err := s.siteTargetToChange(ctx, actor, userID); err != nil {
		return err
	}
	return s.unlinkIdentity(ctx, userID, identityID, actor.User.ID, "admin", ip)
}
