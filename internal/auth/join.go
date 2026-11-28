package auth

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"petalbook.app/server/internal/mail"
	"petalbook.app/server/internal/notify"
	"petalbook.app/server/internal/ratelimit"
	"petalbook.app/server/internal/store"
)

// Joining an organisation by the domain of an address, at sign-up (3.6 d5,
// docs/api.md "Joining by email domain"). A tenant that holds a domain
// verified chooses per domain: off (the default), auto (a confirmed address
// makes an account in the tenant at once) or request (a confirmed address
// becomes a request its administrators approve or deny). 3.6 is one person,
// one tenant: only a sign-up joins; an existing account joining another
// organisation is 3.7.

const (
	// JoinRequestTTL is how long a request waits for an administrator.
	JoinRequestTTL = 30 * 24 * time.Hour
	// JoinRequestsPerDay is how many requests one address may make in a day,
	// in every tenant together; the address is counted without a +tag.
	JoinRequestsPerDay = 3
	// MaxPendingJoins is the most requests a tenant holds waiting.
	MaxPendingJoins = 1000
)

// untagged is an address key without the +tag of its local part
// ("bob+x@acme.example" is bob@acme.example): one person's mailbox.
func untagged(key string) string {
	at := strings.LastIndexByte(key, '@')
	if at < 0 {
		return key
	}
	local, domain := key[:at], key[at:]
	if i := strings.IndexByte(local, '+'); i > 0 {
		local = local[:i]
	}
	return local + domain
}

func errJoinLimit() *Error {
	return coded(429, "join_limit", "This organization has too many requests to join waiting: try again later.")
}

func errTenantDisabled() *Error {
	return coded(409, "tenant_disabled", "This organization is disabled: nobody joins it now.")
}

// joinOf is how the domain of an address joins a tenant (Config.JoinDomain):
// ok false when it does not.
func (s *Service) joinOf(ctx context.Context, email string) (tenant, domain, mode string, ok bool) {
	if s.cfg.JoinDomain == nil {
		return "", "", "", false
	}
	return s.cfg.JoinDomain(ctx, strings.TrimSpace(email))
}

// DomainJoinOpen says whether some tenant takes sign-ups by the domain of an
// address now: mail is on, and a verified domain of a tenant that is not
// disabled joins people to it. From memory.
func (s *Service) DomainJoinOpen(ctx context.Context) bool {
	if s.cfg.JoiningTenants == nil || !s.MailEnabled() {
		return false
	}
	for _, t := range s.cfg.JoiningTenants(ctx) {
		if !s.st.TenantDisabled(t) {
			return true
		}
	}
	return false
}

// JoinStatus is a request as the person who made it sees it (the waiting page).
type JoinStatus struct {
	State      string
	TenantSlug string
	TenantName string
	Username   string
	Email      string
	Created    time.Time
	Expires    time.Time
	DecidedAt  time.Time
}

func (s *Service) joinStatus(ctx context.Context, r store.JoinRequest) *JoinStatus {
	ref, _ := s.st.TenantRefOf(r.TenantID)
	return &JoinStatus{State: r.StateAt(s.now()), TenantSlug: ref.Slug, TenantName: ref.Name, Username: r.Username, Email: r.Email,
		Created: r.Created, Expires: r.Expires, DecidedAt: r.DecidedAt}
}

func errJoinPending() *Error {
	return coded(409, "join_pending", "You have asked to join this organization already: wait for its answer, or withdraw that request first.")
}

func errJoinDecided() *Error {
	return coded(409, "join_decided", "This request is no longer waiting: it was decided, withdrawn, cancelled, or it expired.")
}

func errNoJoinRequest() *Error { return coded(404, "not_found", "No such request.") }

// enabledAdmins are the ids of the tenant's enabled administrators, sorted:
// who is told of a request.
func enabledAdmins(ctx context.Context, q store.Q, tenant string) ([]string, error) {
	admins, _, err := store.TenantAdmins(ctx, q, store.InTenant(tenant), 200)
	if err != nil {
		return nil, err
	}
	to := make([]string, 0, len(admins))
	for _, a := range admins {
		if !a.Disabled {
			to = append(to, a.ID)
		}
	}
	sort.Strings(to)
	return to, nil
}

// requestJoin turns a confirmed registration into a pending request, in one
// transaction with the registration's end and the administrators' notice. At
// most JoinRequestsPerDay a day for one address.
func (s *Service) requestJoin(ctx context.Context, reg *store.Registration, lang, ip string) (*JoinStatus, error) {
	key, ok := store.EmailKey(reg.Email)
	if !ok {
		return nil, errBadLink()
	}
	cost := ratelimit.Cost{Key: "join:" + untagged(key), Limit: JoinRequestsPerDay}
	if wait, ok := s.joinCaps.Allows(cost); !ok {
		return nil, retryAfter(wait)
	}
	now := s.now().UTC()
	r := store.JoinRequest{ID: s.newID(), TenantID: reg.TenantID, Username: reg.Username, Name: reg.Name, Email: reg.Email, EmailKey: key,
		Domain: reg.JoinDomain, PasswordHash: reg.PasswordHash, Terms: reg.Terms, Lang: lang, TokenHash: reg.TokenHash, State: store.JoinPending,
		Created: now, Expires: now.Add(JoinRequestTTL)}
	if n, err := store.CountPendingJoins(ctx, s.st.Q(), reg.TenantID, now); err != nil {
		return nil, err
	} else if n >= MaxPendingJoins {
		return nil, errJoinLimit()
	}
	err := s.st.Tx(ctx, func(q store.Q) error {
		if ok, err := store.DeleteRegistration(ctx, q, reg.ID); err != nil {
			return err
		} else if !ok {
			return errBadLink()
		}
		if err := store.InsertJoinRequest(ctx, q, r, now); err != nil {
			return err
		}
		to, err := enabledAdmins(ctx, q, r.TenantID)
		if err != nil {
			return err
		}
		return notify.WithAudit(ctx, q, notify.JoinRequested, notify.Entry{Tenant: r.TenantID, Username: r.Username, IP: ip, Now: now,
			Detail:  map[string]any{"request": r.ID, "username": r.Username, "email": r.Email, "domain": r.Domain},
			To:      to,
			Ref:     r.ID,
			Payload: map[string]any{"username": r.Username, "name": r.Name, "domain": r.Domain}})
	})
	switch {
	case errors.Is(err, store.ErrJoinPending):
		return nil, errJoinPending()
	case errors.Is(err, store.ErrJoinUsername):
		return nil, errf(Exists, "That username is taken.")
	case err != nil:
		return nil, err
	}
	s.joinCaps.TakeAll(cost)
	return s.joinStatus(ctx, r), nil
}

// findJoin is the request of a registration link's token, for the waiting page.
func (s *Service) findJoin(ctx context.Context, token, ip string) (store.JoinRequest, error) {
	if err := s.gateToken(ctx, ip); err != nil {
		return store.JoinRequest{}, err
	}
	if !validToken(token) {
		return store.JoinRequest{}, errBadLink()
	}
	r, err := store.JoinRequestByToken(ctx, s.st.Q(), hashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return r, errBadLink()
	} else if err != nil {
		return r, err
	}
	if !ctEqual(r.TokenHash, hashToken(token)) {
		return r, errBadLink()
	}
	s.refundIP(ip)
	return r, nil
}

// JoinRequestStatus is the request of a registration link's token, as the
// waiting page shows it.
func (s *Service) JoinRequestStatus(ctx context.Context, token, ip string) (*JoinStatus, error) {
	r, err := s.findJoin(ctx, token, ip)
	if err != nil {
		return nil, err
	}
	return s.joinStatus(ctx, r), nil
}

// WithdrawJoinRequest is the person withdrawing their pending request from
// the waiting page: the username is free again.
func (s *Service) WithdrawJoinRequest(ctx context.Context, token, ip string) (*JoinStatus, error) {
	r, err := s.findJoin(ctx, token, ip)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	err = s.st.Tx(ctx, func(q store.Q) error {
		if ok, err := store.WithdrawJoinRequest(ctx, q, r.TokenHash, now); err != nil {
			return err
		} else if !ok {
			return errJoinDecided()
		}
		return notify.WithAudit(ctx, q, notify.JoinWithdrawn, notify.Entry{Tenant: r.TenantID, Username: r.Username, IP: ip, Now: now,
			Detail: map[string]any{"request": r.ID, "username": r.Username, "email": r.Email, "domain": r.Domain}})
	})
	if err != nil {
		return nil, err
	}
	r.State, r.DecidedAt = store.JoinWithdrawn, now
	return s.joinStatus(ctx, r), nil
}

// pendingJoin is a tenant's request that is still waiting: 404 when the
// tenant has none such, 409 join_decided when it is not waiting any more.
func (s *Service) pendingJoin(ctx context.Context, tenant, id string) (store.JoinRequest, error) {
	r, err := store.GetJoinRequest(ctx, s.st.Q(), tenant, id)
	if errors.Is(err, store.ErrNotFound) {
		return r, errNoJoinRequest()
	} else if err != nil {
		return r, err
	}
	if r.StateAt(s.now()) != store.JoinPending {
		return r, errJoinDecided()
	}
	return r, nil
}

// ApproveJoinRequest is a tenant administrator approving a request: the
// account is made in the tenant as a member, with the address verified and
// the password chosen at sign-up, in one transaction with the request's
// approval; the account is told, and the address is mailed. role is
// "member" ("" for it): an organisation's viewer role is not yet a role of
// accounts. The tenant requiring its single sign-on makes no password
// account: 409 sso_required.
func (s *Service) ApproveJoinRequest(ctx context.Context, actor store.User, tenant, id, role, ip string) (store.JoinRequest, *store.User, error) {
	if role == "" {
		role = "member"
	}
	if role != "member" {
		return store.JoinRequest{}, nil, errf(Invalid, "Role is member.")
	}
	r, err := s.pendingJoin(ctx, tenant, id)
	if err != nil {
		return r, nil, err
	}
	if s.st.TenantDisabled(tenant) {
		return r, nil, errTenantDisabled()
	}
	if s.ssoOnlyInEffect(s.sec(ctx)) {
		return r, nil, errLocalRefused()
	}
	if s.tenantSSOOnly(ctx, tenant) {
		return r, nil, s.ssoRequiredOf(ctx, r.Email)
	}
	u, err := s.CreateUser(ctx, CreateParams{Username: r.Username, Name: r.Name, Email: r.Email, EmailVerified: true, TakeUnverified: true, Role: role,
		PasswordHash: r.PasswordHash, TenantID: tenant, Terms: r.Terms, JoinRequestID: r.ID, JoinDecidedBy: actor.ID})
	if err != nil {
		return r, nil, err
	}
	now := s.now().UTC()
	ref, _ := s.st.TenantRefOf(tenant)
	err = notify.WithAudit(ctx, s.st.Q(), notify.JoinApproved, notify.Entry{Actor: actor.ID, User: u.ID, Username: u.Username, IP: ip, Tenant: tenant, Now: now,
		Detail:  map[string]any{"request": r.ID, "username": r.Username, "email": r.Email, "domain": r.Domain, "role": role},
		To:      []string{u.ID},
		Ref:     r.ID,
		Payload: map[string]any{"tenant": ref.Name, "by": actor.Name}})
	if err != nil {
		s.log.Warn("could not write a notification", "error", err)
	}
	if err := s.sendMail(mail.KindJoinApproved, r.Lang, r.Email, mail.Vars{Username: r.Username, Org: ref.Name, Link: strings.TrimRight(s.cfg.PublicURL, "/") + "/login"}); err != nil {
		s.log.Warn("could not queue a message", "reason", err.Error())
	}
	r.State, r.DecidedAt, r.DecidedBy, r.MadeUser, r.PasswordHash = store.JoinApproved, now, actor.ID, u.ID, ""
	return r, u, nil
}

// DenyJoinRequest is a tenant administrator denying a request: the username
// is free again, and the address is mailed.
func (s *Service) DenyJoinRequest(ctx context.Context, actor store.User, tenant, id, ip string) (store.JoinRequest, error) {
	r, err := s.pendingJoin(ctx, tenant, id)
	if err != nil {
		return r, err
	}
	now := s.now().UTC()
	err = s.st.Tx(ctx, func(q store.Q) error {
		if ok, err := store.DecideJoinRequest(ctx, q, tenant, id, store.JoinDenied, actor.ID, "", now); err != nil {
			return err
		} else if !ok {
			return errJoinDecided()
		}
		return notify.WithAudit(ctx, q, notify.JoinDenied, notify.Entry{Actor: actor.ID, Username: r.Username, IP: ip, Tenant: tenant, Now: now,
			Detail: map[string]any{"request": r.ID, "username": r.Username, "email": r.Email, "domain": r.Domain}})
	})
	if err != nil {
		return r, err
	}
	ref, _ := s.st.TenantRefOf(tenant)
	if err := s.sendMail(mail.KindJoinDenied, r.Lang, r.Email, mail.Vars{Username: r.Username, Org: ref.Name}); err != nil {
		s.log.Warn("could not queue a message", "reason", err.Error())
	}
	r.State, r.DecidedAt, r.DecidedBy, r.PasswordHash = store.JoinDenied, now, actor.ID, ""
	return r, nil
}
