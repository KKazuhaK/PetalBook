package auth

import (
	"context"
	"errors"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// allow spends one of the address's requests this minute, shared by the
// sign-in endpoints (login, setup, invites, captcha, passkeys, the SAML ACS).
func (s *Service) allow(ctx context.Context, ip string) error {
	st := s.sec(ctx)
	wait, ok, first := s.perMin.take("ip:"+ip, st.Limits.PerMinute)
	if ok {
		return nil
	}
	if first {
		s.Audit(AuditEvent{Kind: "rate_limited", IP: ip, Detail: map[string]any{"limit": "per_minute"}})
	}
	return retryAfter(wait)
}

// gateIP reserves one failure of the address before any hash or token is
// looked at; the caller gives it back with refundIP when the attempt turns out
// well. The address's failure cap is judged here.
func (s *Service) gateIP(st *secState, ip string) error {
	wait, ok, first := s.failIP.reserve("ip:"+ip, st.Limits.MaxFailures, st.window)
	if ok {
		return nil
	}
	if first {
		s.Audit(AuditEvent{Kind: "rate_limited", IP: ip, Detail: map[string]any{"limit": "ip"}})
	}
	return retryAfter(wait)
}

func (s *Service) refundIP(ip string) { s.failIP.refund("ip:" + ip) }

// wrongToken is what a wrong setup or invite token costs: the reservation of
// the address stays.
func pairKey(ip, ukey string) string { return "p:" + ip + "|" + ukey }

// LoginParams is a password sign-in.
type LoginParams struct {
	Username, Password, IP string
	Captcha                *CaptchaAnswer
}

// LoginResult is a signed-in user and the cookie value of the session.
type LoginResult struct {
	User  *store.User
	Token string
	// Held: the person must register a passkey before anything else.
	Held bool
}

// Login checks a username and password, in the order of docs/api.md: the
// address's requests this minute, the captcha, the address's failures, the
// lockout, the password (a wrong one, an unknown user and a disabled user get
// the same answer after the same amount of work), then what the account may
// do with a password.
func (s *Service) Login(ctx context.Context, p LoginParams) (*LoginResult, error) {
	ip := p.IP
	if err := s.allow(ctx, ip); err != nil {
		return nil, err
	}
	st := s.sec(ctx)
	if err := s.checkCaptcha(ctx, st, CtxLogin, p.Username, ip, p.Captcha); err != nil {
		s.Audit(AuditEvent{Kind: "login_failed", Username: p.Username, IP: ip, Detail: map[string]any{"method": "password", "reason": "captcha_failed"}})
		return nil, err
	}
	if err := s.gateIP(st, ip); err != nil {
		return nil, err
	}
	reserved := true
	defer func() {
		if reserved {
			s.refundIP(ip)
		}
	}()
	ukey := userKey(p.Username)
	pkey := pairKey(ip, ukey)
	if st.Lockout.Enabled {
		key, table := "", (*failTable)(nil)
		switch st.Lockout.Scope {
		case "ip_account":
			key, table = pkey, s.failPair
		case "account":
			key, table = ukey, s.failUser
		}
		if table != nil {
			if wait, locked := table.lockedFor(key, st.window); locked {
				s.Audit(AuditEvent{Kind: "login_failed", Username: p.Username, IP: ip, Detail: map[string]any{"method": "password", "reason": "rate_limited"}})
				return nil, retryAfter(wait)
			}
		}
	}
	if len(p.Password) > MaxPasswordBytes {
		return nil, s.wrongPassword(st, p, ukey, pkey, "bad_password", &reserved)
	}
	var u *store.User
	hash := s.hasher.dummy
	if un, err := NormalizeUsername(p.Username); err == nil {
		switch row, err := store.GetUserByUsername(ctx, s.st.Q(), un); {
		case err == nil:
			u = row
			if h, err := store.GetPasswordHash(ctx, s.st.Q(), row.ID); err == nil {
				hash = h
			} else if !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
		case !errors.Is(err, store.ErrNotFound):
			return nil, err
		}
	}
	good, rehash, err := s.hasher.verify(ctx, hash, p.Password)
	if err != nil {
		return nil, err
	}
	if !good || u == nil || hash == s.hasher.dummy {
		return nil, s.wrongPassword(st, p, ukey, pkey, "bad_password", &reserved)
	}
	if u.Disabled {
		return nil, s.wrongPassword(st, p, ukey, pkey, "disabled", &reserved)
	}
	// The password is right: what stands against the name is forgiven.
	s.failUser.reset(ukey)
	s.failPair.reset(pkey)
	s.refundIP(ip)
	reserved = false
	if rehash {
		if nh, err := s.hasher.hash(ctx, p.Password); err == nil {
			if err := store.SetPasswordHash(ctx, s.st.Q(), u.ID, nh, s.now()); err != nil {
				s.log.Warn("could not upgrade a password hash", "error", err)
			}
		}
	}
	if s.ssoOnlyInEffect(st) && u.Role != "admin" {
		s.Audit(AuditEvent{Kind: "login_failed", User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"method": "password", "reason": "local_login_refused"}})
		return nil, errLocalRefused()
	}
	held := false
	if s.passkeyRequiredOf(st, *u) {
		n, err := store.CountPasskeys(ctx, s.st.Q(), u.ID)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			s.Audit(AuditEvent{Kind: "login_failed", User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"method": "password", "reason": "passkey_required"}})
			return nil, coded(403, "passkey_required", "your account signs in with a passkey")
		}
		held = true
	}
	tok, err := s.issueSession(ctx, u.ID, held)
	if err != nil {
		return nil, err
	}
	s.Audit(AuditEvent{Kind: "login", User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"method": "password"}})
	return &LoginResult{User: u, Token: tok, Held: held}, nil
}

func errLocalRefused() *Error {
	return coded(403, "local_login_refused", "signing in with a password or a passkey is not allowed here; use single sign-on")
}

// wrongPassword counts a failed password against the name and the pair (the
// address's failure is the reservation already made), and answers. A key that
// has reached the cap answers 429 at once.
func (s *Service) wrongPassword(st *secState, p LoginParams, ukey, pkey, reason string, reserved *bool) error {
	ip := p.IP
	*reserved = false // the failure of the address stays counted
	var lockUser, lockPair time.Duration
	if st.Lockout.Enabled {
		switch st.Lockout.Scope {
		case "account":
			lockUser = st.lock
		case "ip_account":
			lockPair = st.lock
		case "ip":
			if s.failIP.lockIfAtCap("ip:"+ip, st.Limits.MaxFailures, st.window, st.lock) {
				s.auditLock(st, p.Username, ip)
			}
		}
	}
	resU := s.failUser.fail(ukey, st.Limits.MaxFailures, st.window, lockUser)
	resP := s.failPair.fail(pkey, st.Limits.MaxFailures, st.window, lockPair)
	s.Audit(AuditEvent{Kind: "login_failed", Username: p.Username, IP: ip, Detail: map[string]any{"method": "password", "reason": reason}})
	for _, c := range []struct {
		res   failResult
		limit string
	}{{resU, "username"}, {resP, "pair"}} {
		if c.res.first {
			s.Audit(AuditEvent{Kind: "rate_limited", Username: p.Username, IP: ip, Detail: map[string]any{"limit": c.limit}})
		}
		if c.res.locked {
			s.auditLock(st, p.Username, ip)
		}
	}
	if resU.atCap || resP.atCap {
		return retryAfter(max(resU.wait, resP.wait))
	}
	return errUnauth()
}

func (s *Service) auditLock(st *secState, username, ip string) {
	s.Audit(AuditEvent{Kind: "lockout", Username: username, IP: ip, Detail: map[string]any{"scope": st.Lockout.Scope, "minutes": st.Lockout.Minutes}})
}

// passkeyRequiredOf says whether the person must sign in with a passkey, by
// themselves or by their role, while passkeys can be used at all.
func (s *Service) passkeyRequiredOf(st *secState, u store.User) bool {
	if !st.Passkeys.Enabled || s.pk == nil {
		return false
	}
	return u.PasskeyRequired || st.roles[u.Role]
}

// Setup creates the first administrator from the first-run token.
func (s *Service) Setup(ctx context.Context, token, username, name, password, ip string, cap *CaptchaAnswer) (*store.User, string, error) {
	if err := s.allow(ctx, ip); err != nil {
		return nil, "", err
	}
	st := s.sec(ctx)
	if err := s.checkCaptcha(ctx, st, CtxSetup, "", ip, cap); err != nil {
		return nil, "", err
	}
	if err := s.gateIP(st, ip); err != nil {
		return nil, "", err
	}
	good := false
	defer func() {
		if good {
			s.refundIP(ip)
		}
	}()
	open, err := s.SetupOpen(ctx)
	if err != nil {
		return nil, "", err
	}
	if !open {
		return nil, "", errf(Exists, "setup is already done")
	}
	if !s.setupTokenOK(token) {
		return nil, "", errf(Forbidden, "the setup token is wrong")
	}
	good = true
	u, err := s.CreateUser(ctx, CreateParams{Username: username, Name: name, Role: "admin", Password: password, Setup: true})
	if err != nil {
		return nil, "", err
	}
	tok, err := s.issueSession(ctx, u.ID, false)
	if err == nil {
		s.Audit(AuditEvent{Kind: "login", User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"method": "password", "created": true}})
	}
	return u, tok, err
}

// setupTokenOK compares in constant time; it is false once an account exists.
func (s *Service) setupTokenOK(given string) bool {
	want := s.SetupToken()
	return want != "" && ctEqual(given, want)
}

// inviteGate is what the two public invite endpoints do before they look at
// the token: the address's requests, the captcha, the address's failures. A
// token that is not good counts as a failure of the address.
func (s *Service) inviteGate(ctx context.Context, token, ip string, cap *CaptchaAnswer) (*store.Invite, *secState, error) {
	if err := s.allow(ctx, ip); err != nil {
		return nil, nil, err
	}
	st := s.sec(ctx)
	if err := s.checkCaptcha(ctx, st, CtxInvite, "", ip, cap); err != nil {
		return nil, nil, err
	}
	if err := s.gateIP(st, ip); err != nil {
		return nil, nil, err
	}
	inv, err := s.findInvite(ctx, token)
	if err == nil {
		s.refundIP(ip)
	}
	return inv, st, err
}

// CheckInvite tells whether a token names an invite that can still be used.
func (s *Service) CheckInvite(ctx context.Context, token, ip string, cap *CaptchaAnswer) (*store.Invite, error) {
	inv, _, err := s.inviteGate(ctx, token, ip, cap)
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

// AcceptInvite makes the account an invite is for, and signs it in. While
// ssoOnly is in effect a member cannot be made with a password: the account
// could never sign in.
func (s *Service) AcceptInvite(ctx context.Context, token, username, name, password, ip string, cap *CaptchaAnswer) (*store.User, string, error) {
	inv, st, err := s.inviteGate(ctx, token, ip, cap)
	if err != nil {
		return nil, "", err
	}
	if s.ssoOnlyInEffect(st) && inv.Role != "admin" {
		return nil, "", errLocalRefused()
	}
	u, err := s.CreateUser(ctx, CreateParams{Username: username, Name: name, Role: inv.Role, Password: password, InviteID: inv.ID})
	if err != nil {
		return nil, "", err
	}
	tok, err := s.issueSession(ctx, u.ID, false)
	if err == nil {
		s.Audit(AuditEvent{Kind: "login", User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"method": "password", "created": true}})
	}
	return u, tok, err
}
