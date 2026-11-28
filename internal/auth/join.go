package auth

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"petalbook.app/server/internal/mail"
	"petalbook.app/server/internal/notify"
	"petalbook.app/server/internal/ratelimit"
	"petalbook.app/server/internal/settings"
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

// joinRole is the role a person who joins the tenant by its domain gets: the
// organisation's domains.joinRole, or the default when it cannot be read or is
// no longer a role a joiner may have.
func (s *Service) joinRole(ctx context.Context, tenant string) string {
	if st := s.settings.Load(); st != nil {
		if e, err := st.Get(ctx, tenant, settings.DomainsJoinRole); err == nil {
			if r, _ := e.Value.(string); slices.Contains(store.JoinRoles, r) {
				return r
			}
		}
	}
	return store.DefaultJoinRole
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
	r := store.JoinRequest{TenantID: reg.TenantID, Username: reg.Username, Name: reg.Name, Email: reg.Email,
		Domain: reg.JoinDomain, PasswordHash: reg.PasswordHash, Terms: reg.Terms, Lang: lang, TokenHash: reg.TokenHash}
	return s.submitJoin(ctx, r, reg.ID, ip)
}

// submitJoin stores r (its tenant, who, address, domain, token hash and the
// password hash or identity it will make the account with) as a pending
// request: the day's cap per address and the tenant's waiting cap apply. A
// registration it comes from (regID) is used up in the same transaction as
// the administrators' notice.
func (s *Service) submitJoin(ctx context.Context, r store.JoinRequest, regID, ip string) (*JoinStatus, error) {
	key, ok := store.EmailKey(r.Email)
	if !ok {
		return nil, errBadLink()
	}
	cost := ratelimit.Cost{Key: "join:" + untagged(key), Limit: JoinRequestsPerDay}
	if wait, ok := s.joinCaps.Allows(cost); !ok {
		return nil, retryAfter(wait)
	}
	now := s.now().UTC()
	r.ID, r.EmailKey, r.State, r.Created, r.Expires = s.newID(), key, store.JoinPending, now, now.Add(JoinRequestTTL)
	if n, err := store.CountPendingJoins(ctx, s.st.Q(), r.TenantID, now); err != nil {
		return nil, err
	} else if n >= MaxPendingJoins {
		return nil, errJoinLimit()
	}
	err := s.st.Tx(ctx, func(q store.Q) error {
		if regID != "" {
			if ok, err := store.DeleteRegistration(ctx, q, regID); err != nil {
				return err
			} else if !ok {
				return errBadLink()
			}
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

// ssoWaiting is the end of a social sign-in that became a request to join
// (finishSSO): nobody is signed in, and the person goes to the waiting page
// with the request's token.
type ssoWaiting struct{ token string }

func (e *ssoWaiting) Error() string { return "waiting" }

// socialJoin is a new account of a site social provider's autoCreate whose
// verified address is on a domain that joins people to a tenant (3.6 d5): the
// tenant's rules as for a sign-up by password. auto makes the account in the
// tenant with its join role; request keeps the identity in a pending request
// and ends in ssoWaiting. The tenant's own rules for the provider apply
// (signin.social, ssoOnly), and a disabled tenant takes nobody.
func (s *Service) socialJoin(ctx context.Context, rt *providerRT, c claimed, email, tenant, domain, mode, ip string) (*store.User, bool, error) {
	if s.st.TenantDisabled(tenant) {
		return nil, false, ssoErr("no_account")
	}
	if !s.tenantMayUse(ctx, rt, tenant) {
		return nil, false, ssoErr("not_allowed")
	}
	if mode != store.JoinModeRequest {
		u, err := s.createFromClaims(ctx, c, s.joinRole(ctx, tenant), "", false, tenant, false)
		if err != nil {
			return nil, false, err
		}
		err = notify.WithAudit(ctx, s.st.Q(), notify.JoinAuto, notify.Entry{Actor: u.ID, User: u.ID, Username: u.Username, IP: ip, Tenant: tenant, Now: s.now(),
			Detail: map[string]any{"username": u.Username, "email": email, "domain": domain}})
		if err != nil {
			s.log.Warn("could not audit a join", "error", err)
		}
		return u, true, nil
	}
	tok := randomToken()
	base := usernameFrom(c)
	for attempt := 0; ; attempt++ {
		name := base
		if attempt > 0 {
			name = suffixed(base, attempt-1)
		}
		if taken, err := s.usernameHeld(ctx, name); err != nil {
			return nil, false, err
		} else if taken && attempt < 20 {
			continue
		}
		r := store.JoinRequest{TenantID: tenant, Username: name, Name: nameFrom(c, base), Email: email, Domain: domain, Lang: mail.PickLang("", ""),
			TokenHash: hashToken(tok), ProviderID: rt.row.ID, Subject: c.subject, IdentityLabel: c.label()}
		_, err := s.submitJoin(ctx, r, "", ip)
		var ae *Error
		switch {
		case err == nil:
			return nil, false, &ssoWaiting{token: tok}
		case errors.As(err, &ae) && ae.Kind == Exists && attempt < 20:
			continue // the name was taken since it was looked at
		case errors.As(err, &ae) && ae.Code == "join_pending":
			return nil, false, ssoErr("join_pending")
		case errors.As(err, &ae) && (ae.Code == "join_limit" || ae.Kind == RateLimited):
			return nil, false, ssoErr("join_limit")
		}
		return nil, false, err
	}
}

// usernameHeld says whether a username is an account's, a retired one, or held
// by a pending registration or request.
func (s *Service) usernameHeld(ctx context.Context, username string) (bool, error) {
	if _, err := store.GetUserByUsername(ctx, s.st.Q(), username); err == nil {
		return true, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	if barred, err := s.retiredName(ctx, s.st.Q(), username); err != nil || barred {
		return barred, err
	}
	return store.PendingUsername(ctx, s.st.Q(), username, s.now())
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
// approval; the account is told, and the address is mailed. role is one of
// store.JoinRoles ("" for the organisation's domains.joinRole). A request made
// with a social sign-in makes the account with that identity and no password.
// The tenant requiring its single sign-on makes no password account:
// 409 sso_required.
func (s *Service) ApproveJoinRequest(ctx context.Context, actor store.User, tenant, id, role, ip string) (store.JoinRequest, *store.User, error) {
	if role == "" {
		role = s.joinRole(ctx, tenant)
	}
	if !slices.Contains(store.JoinRoles, role) {
		return store.JoinRequest{}, nil, errf(Invalid, "Role is one of: %s.", strings.Join(store.JoinRoles, ", "))
	}
	r, err := s.pendingJoin(ctx, tenant, id)
	if err != nil {
		return r, nil, err
	}
	if s.st.TenantDisabled(tenant) {
		return r, nil, errTenantDisabled()
	}
	// A request made with a social sign-in makes no password account: only the
	// password ones are held to ssoOnly (the social one is asked below).
	if r.ProviderID == "" {
		if s.ssoOnlyInEffect(s.sec(ctx)) {
			return r, nil, errLocalRefused()
		}
		if s.tenantSSOOnly(ctx, tenant) {
			return r, nil, s.ssoRequiredOf(ctx, r.Email)
		}
	}
	p := CreateParams{Username: r.Username, Name: r.Name, Email: r.Email, EmailVerified: true, TakeUnverified: true, Role: role,
		PasswordHash: r.PasswordHash, TenantID: tenant, Terms: r.Terms, JoinRequestID: r.ID, JoinDecidedBy: actor.ID}
	if r.ProviderID != "" {
		// Made with a social sign-in: the account is that identity's, with no password.
		p.Identity = &IdentityParams{ProviderID: r.ProviderID, Subject: r.Subject, Label: r.IdentityLabel, Email: r.Email, EmailTrusted: true}
		rt := s.provs.getID(r.ProviderID)
		if rt == nil || !s.tenantMayUse(ctx, rt, tenant) {
			return r, nil, coded(409, "provider_unavailable", "The sign-in this request was made with is not available to this organization any more.")
		}
		p.VerifiedBy = store.VerifiedByProvider(rt.row.Slug)
		p.RetryUsername = true
	}
	u, err := s.CreateUser(ctx, p)
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
