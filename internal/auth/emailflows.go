package auth

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KKazuhaK/PetalBook/internal/mail"
	"github.com/KKazuhaK/PetalBook/internal/settings"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Registration, email verification and password reset (docs/api.md,
// "Registration", "Email verification" and "Password reset"). Every flow ends
// in a link carrying a 32-byte random token in its fragment; only the SHA-256
// of the token is stored, and it works once.

const (
	registrationTTL = 24 * time.Hour
	verifyTTL       = 24 * time.Hour
	resetTTL        = time.Hour
	maxPending      = 1000
	maxResetLinks   = 5
)

// UseSettings gives the service the layered settings, which say whether
// registration is open. Until it is called, registration is closed.
func (s *Service) UseSettings(st *settings.Service) { s.settings.Store(st) }

// RegRules is what a tenant's registration comes to.
type RegRules struct {
	Open       bool
	Domains    []string
	TermsURL   string
	PrivacyURL string
	PerHour    int
}

// Registration reports the rules of a tenant. Registration is open when the
// effective setting says so, mail is enabled and the server has a public
// address. It reads the cached settings, so it costs no query.
func (s *Service) Registration(ctx context.Context, tenant string) (RegRules, error) {
	st := s.settings.Load()
	if st == nil {
		return RegRules{}, nil
	}
	eff, err := st.For(ctx, tenant)
	if err != nil {
		return RegRules{}, err
	}
	var r RegRules
	r.Open, _ = eff[settings.RegistrationOpen].Value.(bool)
	r.Open = r.Open && s.MailEnabled() && !s.st.TenantDisabled(tenant)
	r.Domains = stringList(eff[settings.RegistrationDomains].Value)
	r.TermsURL, _ = eff[settings.LegalTermsURL].Value.(string)
	r.PrivacyURL, _ = eff[settings.LegalPrivacyURL].Value.(string)
	r.PerHour = 50
	switch n := eff[settings.RegistrationPerHour].Value.(type) {
	case int64:
		r.PerHour = int(n)
	case int:
		r.PerHour = n
	case float64:
		r.PerHour = int(n)
	}
	return r, nil
}

func stringList(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		var out []string
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// RegistrationOpenAnywhere says whether any tenant's effective
// registration.open is true (the check of the security settings: ssoOnly and
// open registration cannot go together).
func (s *Service) RegistrationOpenAnywhere(ctx context.Context) (bool, error) {
	st := s.settings.Load()
	if st == nil {
		return false, nil
	}
	for _, id := range s.st.TenantIDs() {
		e, err := st.Get(ctx, id, settings.RegistrationOpen)
		if err != nil {
			return false, err
		}
		if on, _ := e.Value.(bool); on {
			return true, nil
		}
	}
	return false, nil
}

// SSOOnlyStored says whether ssoOnly is stored on, in effect or not.
func (s *Service) SSOOnlyStored(ctx context.Context) bool { return s.sec(ctx).Login.SSOOnly }

// CancelClosedRegistrations deletes the pending registrations of every tenant
// whose registration is not open (closing it cancels them).
func (s *Service) CancelClosedRegistrations(ctx context.Context) error {
	ids, err := store.TenantsWithRegistrations(ctx, s.st.Q())
	if err != nil {
		return err
	}
	for _, id := range ids {
		r, err := s.Registration(ctx, id)
		if err != nil {
			return err
		}
		if !r.Open {
			if _, err := store.DeleteRegistrationsOf(ctx, s.st.Q(), id); err != nil {
				return err
			}
		}
	}
	return nil
}

// mailGate is what every flow that sends a message asks first.
func (s *Service) needMail() error {
	if !s.MailEnabled() {
		return coded(409, "mail_not_configured", "mail is not enabled")
	}
	return nil
}

// newEmailToken stores a token of the kind for the person (earlier ones of the
// kind stop working) and returns it.
func (s *Service) newEmailToken(ctx context.Context, q store.Q, kind, userID, email string, ttl time.Duration) (string, error) {
	tok := randomToken()
	now := s.now().UTC()
	err := store.ReplaceEmailToken(ctx, q, store.EmailToken{Hash: hashToken(tok), Kind: kind, UserID: userID, Email: email, Created: now, Expires: now.Add(ttl)})
	return tok, err
}

// gateToken is what the four endpoints with a token in the body do first: the
// address's requests this minute and its failures. A token that is not good
// stays counted as a failure of the address; a good one gives its place back.
func (s *Service) gateToken(ctx context.Context, ip string) error {
	if err := s.allow(ctx, ip); err != nil {
		return err
	}
	return s.gateIP(s.sec(ctx), ip)
}

func errBadLink() *Error { return errf(NotFound, "that link is not valid, or has expired") }

func validToken(t string) bool { return t != "" && len(t) <= 256 }

// --- registration --------------------------------------------------------------

// RegisterParams is POST /api/register.
type RegisterParams struct {
	TenantID    string
	Username    string
	Name        string
	Email       string
	Password    string
	AcceptTerms bool
	Captcha     *CaptchaAnswer
	Lang        string
	IP          string
}

func closedErr() *Error { return coded(403, "registration_closed", "registration is not open") }

// Register takes a registration. It answers nil whether or not a message goes
// out (see docs/api.md): the caller says 202 either way.
func (s *Service) Register(ctx context.Context, p RegisterParams) error {
	// 1. the address's requests this minute
	if err := s.allow(ctx, p.IP); err != nil {
		return err
	}
	// 2. open for the tenant
	rules, err := s.Registration(ctx, p.TenantID)
	if err != nil {
		return err
	}
	if !rules.Open {
		return closedErr()
	}
	// 3. the captcha
	st := s.sec(ctx)
	if err := s.checkCaptcha(ctx, st, CtxRegister, "", p.IP, p.Captcha); err != nil {
		return err
	}
	// 4. the fields
	username, err := NormalizeUsername(p.Username)
	if err != nil {
		return err
	}
	name, err := cleanDisplayName(p.Name)
	if err != nil {
		return err
	}
	if utf8.RuneCountInString(name) > 80 {
		return errf(Invalid, "a name is 1 to 80 characters")
	}
	email := strings.TrimSpace(p.Email)
	if email == "" || !plainAddress(email) {
		return errf(Invalid, "that does not look like an email address")
	}
	if err := CheckPassword(p.Password); err != nil {
		return err
	}
	// 5. the domain
	if len(rules.Domains) > 0 && !domainAllowed(email, rules.Domains) {
		return coded(400, "email_domain_not_allowed", "registration is open to some email domains only")
	}
	// 6. the terms
	if (rules.TermsURL != "" || rules.PrivacyURL != "") && !p.AcceptTerms {
		return coded(400, "terms_required", "accept the terms and the privacy policy to register")
	}
	// 7. the username
	if _, err := store.GetUserByUsername(ctx, s.st.Q(), username); err == nil {
		return errf(Exists, "that username is taken")
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if held, err := store.PendingUsername(ctx, s.st.Q(), username, s.now()); err != nil {
		return err
	} else if held {
		return errf(Exists, "that username is taken")
	}
	// 8. the tenant's hour and its pending registrations. Every request counts,
	// whether or not the address already has an account, so the answer is the same.
	if wait, ok := s.regCaps.take(s.now(), capKey{"reg:" + p.TenantID, rules.PerHour}); !ok {
		return retryAfter(wait)
	}
	if n, err := store.CountPendingRegistrations(ctx, s.st.Q(), p.TenantID, s.now()); err != nil {
		return err
	} else if n >= maxPending {
		return retryAfter(time.Hour)
	}
	// The password is hashed in every case: the work does not tell.
	hash, err := s.hasher.hash(ctx, p.Password)
	if err != nil {
		return err
	}
	lang := mail.PickLang(p.Lang, "")
	if exists, err := s.verifiedAccountOf(ctx, p.TenantID, email); err != nil {
		return err
	} else if exists != nil {
		return s.mailExisting(ctx, *exists, email, lang, p.IP)
	}
	if _, ok := s.mailSlot(email, p.IP); !ok {
		return nil
	}
	var terms string
	if rules.TermsURL != "" || rules.PrivacyURL != "" {
		b, _ := json.Marshal(map[string]string{"at": rfc3339(s.now()), "termsUrl": rules.TermsURL, "privacyUrl": rules.PrivacyURL})
		terms = string(b)
	}
	tok := randomToken()
	now := s.now().UTC()
	r := store.Registration{TenantID: p.TenantID, Username: username, Name: name, Email: email, PasswordHash: hash, TokenHash: hashToken(tok),
		Terms: terms, Created: now, Expires: now.Add(registrationTTL)}
	for attempt := 0; ; attempt++ {
		r.ID = s.newID()
		err = s.st.Tx(ctx, func(q store.Q) error { return store.InsertRegistration(ctx, q, r, now) })
		if err == nil {
			break
		}
		if on, ok := store.UniqueOn(err); ok {
			if strings.Contains(on, "username") {
				return errf(Exists, "that username is taken")
			}
			if attempt < 5 {
				continue
			}
		}
		return err
	}
	if err := s.sendMail(mail.KindRegister, lang, email, mail.Vars{Username: username, Link: s.link("/register/confirm", tok), Hours: 24}); err != nil {
		s.log.Warn("could not queue a registration message", "reason", err.Error())
	}
	return nil
}

func domainAllowed(email string, domains []string) bool {
	at := strings.LastIndex(email, "@")
	d := strings.ToLower(email[at+1:])
	for _, a := range domains {
		if d == a || strings.HasSuffix(d, "."+a) {
			return true
		}
	}
	return false
}

// verifiedAccountOf finds an enabled account of the tenant that has this
// address verified.
func (s *Service) verifiedAccountOf(ctx context.Context, tenant, email string) (*store.User, error) {
	us, err := store.UsersByEmail(ctx, s.st.Q(), s.st.Dialect(), store.InTenant(tenant), email, 20)
	if err != nil {
		return nil, err
	}
	for _, u := range us {
		if u.EmailVerified && !u.Disabled {
			return &u, nil
		}
	}
	return nil, nil
}

// mailExisting tells an address that already has an account so, with a link
// to reset the password (or to sign in with the provider).
func (s *Service) mailExisting(ctx context.Context, u store.User, email, lang, ip string) error {
	if _, ok := s.mailSlot(email, ip); !ok {
		return nil
	}
	links, sso, err := s.resetLinks(ctx, []store.User{u}, "user", "", ip)
	if err != nil {
		return err
	}
	if len(links) > 0 {
		if err := s.sendMail(mail.KindRegisterDup, lang, email, mail.Vars{Link: links[0].URL, Hours: 1}); err != nil {
			s.log.Warn("could not queue a message", "reason", err.Error())
		}
	} else if sso != nil {
		if err := s.sendMail(mail.KindResetSSO, lang, email, mail.Vars{Provider: s.providerNameFor(ctx, *sso)}); err != nil {
			s.log.Warn("could not queue a message", "reason", err.Error())
		}
	}
	return nil
}

// RegisterConfirm makes the account a registration link is for, and signs it in.
func (s *Service) RegisterConfirm(ctx context.Context, token, ip string) (*store.User, string, error) {
	if err := s.gateToken(ctx, ip); err != nil {
		return nil, "", err
	}
	if !validToken(token) {
		return nil, "", errBadLink()
	}
	reg, err := store.FindRegistration(ctx, s.st.Q(), hashToken(token), s.now())
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", errBadLink()
	} else if err != nil {
		return nil, "", err
	}
	if !ctEqual(reg.TokenHash, hashToken(token)) {
		return nil, "", errBadLink()
	}
	s.refundIP(ip)
	rules, err := s.Registration(ctx, reg.TenantID)
	if err != nil {
		return nil, "", err
	}
	if !rules.Open {
		return nil, "", closedErr()
	}
	st := s.sec(ctx)
	if s.ssoOnlyInEffect(st) {
		return nil, "", errLocalRefused()
	}
	u, err := s.CreateUser(ctx, CreateParams{Username: reg.Username, Name: reg.Name, Email: reg.Email, EmailVerified: true, Role: "member",
		PasswordHash: reg.PasswordHash, TenantID: reg.TenantID, RegistrationID: reg.ID, Terms: reg.Terms})
	if err != nil {
		return nil, "", err
	}
	tok, err := s.issueSession(ctx, u.ID, s.passkeyRequiredOf(st, *u))
	if err != nil {
		return nil, "", err
	}
	s.Audit(AuditEvent{Kind: "registered", Actor: u.ID, User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"email": reg.Email}})
	s.Audit(AuditEvent{Kind: "login", User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"method": "password", "created": true}})
	return u, tok, nil
}

// --- email verification ----------------------------------------------------------

// RequestEmailVerify sends the signed-in person a link to confirm their address.
func (s *Service) RequestEmailVerify(ctx context.Context, u store.User, lang, ip string) error {
	if err := s.needMail(); err != nil {
		return err
	}
	if u.Email == "" {
		return errf(Invalid, "this account has no email address; set one first")
	}
	if u.EmailVerified {
		return coded(409, "email_verified", "this address is already verified")
	}
	if wait, ok := s.mailSlot(u.Email, ip); !ok {
		return retryAfter(wait)
	}
	var tok string
	err := s.st.Tx(ctx, func(q store.Q) (err error) {
		tok, err = s.newEmailToken(ctx, q, store.TokenVerify, u.ID, u.Email, verifyTTL)
		return err
	})
	if err != nil {
		return err
	}
	return s.sendMail(mail.KindVerify, mail.PickLang(lang, ""), u.Email, mail.Vars{Email: u.Email, Link: s.link("/verify-email", tok), Hours: 24})
}

// ConfirmEmail marks the address a link was sent to as verified, by the person
// themselves (which clears any mark of a tenant administrator's).
func (s *Service) ConfirmEmail(ctx context.Context, token, ip string) error {
	if err := s.gateToken(ctx, ip); err != nil {
		return err
	}
	if !validToken(token) {
		return errBadLink()
	}
	hash := hashToken(token)
	t, err := store.FindEmailToken(ctx, s.st.Q(), store.TokenVerify, hash, s.now())
	if errors.Is(err, store.ErrNotFound) {
		return errBadLink()
	} else if err != nil {
		return err
	}
	u, err := store.GetUser(ctx, s.st.Q(), store.AllTenants(), t.UserID)
	if errors.Is(err, store.ErrNotFound) || err == nil && (u.Disabled || s.st.TenantDisabled(u.TenantID) || u.Email != t.Email || !ctEqual(t.Hash, hash)) {
		return errBadLink()
	} else if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q store.Q) error {
		if ok, err := store.UseEmailToken(ctx, q, hash, s.now()); err != nil {
			return err
		} else if !ok {
			return errBadLink()
		}
		return store.SetEmailVerified(ctx, q, u.ID, true)
	})
	if err != nil {
		return err
	}
	s.refundIP(ip)
	s.dropUser(u.ID)
	u.EmailVerified = true
	s.cfg.Spaces.UserChanged(ctx, *u)
	s.Audit(AuditEvent{Kind: "email_verified", Actor: u.ID, User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"email": t.Email}})
	return nil
}

// NoteEmailChange tells the old, verified address that the account's address
// was changed. The note has no link; nothing is said when mail is off or the
// address has had its messages for the hour.
func (s *Service) NoteEmailChange(old store.User, lang, ip string) {
	if !s.MailEnabled() || old.Email == "" || !old.EmailVerified {
		return
	}
	if _, ok := s.mailSlot(old.Email, ip); !ok {
		return
	}
	if err := s.sendMail(mail.KindEmailChanged, mail.PickLang(lang, ""), old.Email, mail.Vars{}); err != nil {
		s.log.Warn("could not queue a message", "reason", err.Error())
	}
}

// --- password reset --------------------------------------------------------------

// ForgotParams is POST /api/password/forgot.
type ForgotParams struct {
	TenantID string
	Login    string
	Captcha  *CaptchaAnswer
	Lang     string
	IP       string
}

// Forgot sends a reset link, or a note to sign in with a provider, to the
// verified address of the account named, and answers nil whatever it did.
func (s *Service) Forgot(ctx context.Context, p ForgotParams) error {
	if err := s.allow(ctx, p.IP); err != nil {
		return err
	}
	if !s.MailEnabled() {
		return coded(403, "password_reset_off", "password reset is not available")
	}
	st := s.sec(ctx)
	if err := s.checkCaptcha(ctx, st, CtxForgot, "", p.IP, p.Captcha); err != nil {
		return err
	}
	login := strings.TrimSpace(p.Login)
	if login == "" || len(login) > 254 {
		return errf(Invalid, "login is a username or an email address")
	}
	var accounts []store.User
	if strings.Contains(login, "@") {
		us, err := store.UsersByEmail(ctx, s.st.Q(), s.st.Dialect(), store.InTenant(p.TenantID), login, 20)
		if err != nil {
			return err
		}
		for _, u := range us {
			if u.EmailVerified && !u.Disabled && !s.st.TenantDisabled(u.TenantID) && len(accounts) < maxResetLinks {
				accounts = append(accounts, u)
			}
		}
	} else if un, err := NormalizeUsername(login); err == nil {
		switch u, err := store.GetUserByUsername(ctx, s.st.Q(), un); {
		case err == nil:
			if u.EmailVerified && u.Email != "" && !u.Disabled && !s.st.TenantDisabled(u.TenantID) {
				accounts = append(accounts, *u)
			}
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
	}
	if len(accounts) == 0 {
		return nil
	}
	to := accounts[0].Email
	if _, ok := s.mailSlot(to, p.IP); !ok {
		return nil
	}
	lang := mail.PickLang(p.Lang, "")
	links, sso, err := s.resetLinks(ctx, accounts, "user", "", p.IP)
	if err != nil {
		return err
	}
	v := mail.Vars{Hours: 1}
	switch {
	case len(links) == 1:
		v.Link = links[0].URL
	case len(links) > 1:
		v.Links = links
	case sso != nil:
		if err := s.sendMail(mail.KindResetSSO, lang, to, mail.Vars{Provider: s.providerNameFor(ctx, *sso)}); err != nil {
			s.log.Warn("could not queue a message", "reason", err.Error())
		}
		return nil
	default:
		return nil
	}
	if err := s.sendMail(mail.KindReset, lang, to, v); err != nil {
		s.log.Warn("could not queue a message", "reason", err.Error())
	}
	return nil
}

// canUsePassword says whether the account could sign in with a password: it
// has one, and ssoOnly does not refuse it. A reset never adds a password the
// person could not already use.
func (s *Service) canUsePassword(ctx context.Context, q store.Q, u store.User) (bool, error) {
	if _, err := store.GetPasswordHash(ctx, q, u.ID); errors.Is(err, store.ErrNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return !(s.ssoOnlyInEffect(s.sec(ctx)) && u.Role != "admin"), nil
}

// resetLinks makes a reset link for each account that can use a password and
// returns them; sso is the first account that cannot (to say "sign in with a
// provider"). Each link is a token of its own that replaces the earlier ones.
func (s *Service) resetLinks(ctx context.Context, accounts []store.User, by, actor, ip string) (links []mail.Link, sso *store.User, err error) {
	for i := range accounts {
		u := accounts[i]
		ok, err := s.canUsePassword(ctx, s.st.Q(), u)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			if sso == nil {
				sso = &accounts[i]
			}
			continue
		}
		var tok string
		if err := s.st.Tx(ctx, func(q store.Q) (err error) {
			tok, err = s.newEmailToken(ctx, q, store.TokenReset, u.ID, u.Email, resetTTL)
			return err
		}); err != nil {
			return nil, nil, err
		}
		links = append(links, mail.Link{Username: u.Username, URL: s.link("/reset-password", tok)})
		s.Audit(AuditEvent{Kind: "password_reset_requested", Actor: actor, User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"by": by}})
	}
	return links, sso, nil
}

// providerNameFor is how a message names where an account signs in: the
// provider of its identity, else the enabled providers, else "single sign-on".
func (s *Service) providerNameFor(ctx context.Context, u store.User) string {
	var names []string
	if ids, err := store.ListIdentities(ctx, s.st.Q(), u.ID); err == nil {
		for _, id := range ids {
			if n, _, _ := s.providerOf(id.Issuer); n != "" && !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	if len(names) == 0 {
		for _, p := range s.provs.publicList() {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return "single sign-on"
	}
	return strings.Join(names, ", ")
}

func (s *Service) findReset(ctx context.Context, token string) (*store.EmailToken, *store.User, error) {
	if !validToken(token) {
		return nil, nil, errBadLink()
	}
	hash := hashToken(token)
	t, err := store.FindEmailToken(ctx, s.st.Q(), store.TokenReset, hash, s.now())
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, errBadLink()
	} else if err != nil {
		return nil, nil, err
	}
	if !ctEqual(t.Hash, hash) {
		return nil, nil, errBadLink()
	}
	u, err := store.GetUser(ctx, s.st.Q(), store.AllTenants(), t.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, errBadLink()
	} else if err != nil {
		return nil, nil, err
	}
	if u.Disabled || s.st.TenantDisabled(u.TenantID) {
		return nil, nil, errBadLink()
	}
	if ok, err := s.canUsePassword(ctx, s.st.Q(), *u); err != nil {
		return nil, nil, err
	} else if !ok {
		return nil, nil, errBadLink()
	}
	return t, u, nil
}

// ResetCheck says whether a reset link can still be used, and for whom.
func (s *Service) ResetCheck(ctx context.Context, token, ip string) (username string, expires time.Time, err error) {
	if err := s.gateToken(ctx, ip); err != nil {
		return "", time.Time{}, err
	}
	t, u, err := s.findReset(ctx, token)
	if err != nil {
		return "", time.Time{}, err
	}
	s.refundIP(ip)
	return u.Username, t.Expires, nil
}

// ResetPassword sets a new password with a reset link, ends every session of
// the account and forgives the failures counted against its name. It starts
// no session: the person signs in, so the rules of sign-in apply.
func (s *Service) ResetPassword(ctx context.Context, token, password, ip string) error {
	if err := s.gateToken(ctx, ip); err != nil {
		return err
	}
	t, u, err := s.findReset(ctx, token)
	if err != nil {
		return err
	}
	if err := CheckPassword(password); err != nil {
		s.refundIP(ip)
		return err
	}
	hash, err := s.hasher.hash(ctx, password)
	if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q store.Q) error {
		if ok, err := store.UseEmailToken(ctx, q, t.Hash, s.now()); err != nil {
			return err
		} else if !ok {
			return errBadLink()
		}
		if err := store.SetPasswordHash(ctx, q, u.ID, hash, s.now()); err != nil {
			return err
		}
		if err := store.DeleteEmailTokens(ctx, q, u.ID, store.TokenReset); err != nil {
			return err
		}
		return store.DeleteUserSessions(ctx, q, u.ID, "")
	})
	if err != nil {
		return err
	}
	s.refundIP(ip)
	s.evictUser(u.ID, "")
	ukey := userKey(u.Username)
	s.failUser.reset(ukey)
	s.failPair.resetWhere(func(k string) bool { return strings.HasSuffix(k, "|"+ukey) })
	s.Audit(AuditEvent{Kind: "password_reset", Actor: u.ID, User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"by": "user"}})
	return nil
}

// AdminPasswordReset sends a reset link to a person's verified address.
func (s *Service) AdminPasswordReset(ctx context.Context, actor store.User, ip, targetID, lang string) error {
	u, err := store.GetUser(ctx, s.st.Q(), store.ScopeOf(actor), targetID)
	if errors.Is(err, store.ErrNotFound) {
		return errf(NotFound, "no such user")
	} else if err != nil {
		return err
	}
	if u.SiteAdmin && !(actor.SiteAdmin && actor.Role == "admin") {
		return errf(Forbidden, "only a site administrator may reset a site administrator's password")
	}
	if err := s.needMail(); err != nil {
		return err
	}
	if u.Email == "" || !u.EmailVerified {
		return coded(409, "email_unverified", "this account has no verified email address")
	}
	ok, err := s.canUsePassword(ctx, s.st.Q(), *u)
	if err != nil {
		return err
	}
	if !ok || u.Disabled {
		return errf(Conflict, "this account has no password it could sign in with")
	}
	if wait, ok := s.mailSlot(u.Email, ""); !ok {
		return retryAfter(wait)
	}
	links, _, err := s.resetLinks(ctx, []store.User{*u}, "admin", actor.ID, ip)
	if err != nil {
		return err
	}
	return s.sendMail(mail.KindReset, mail.PickLang(lang, ""), u.Email, mail.Vars{Link: links[0].URL, Hours: 1})
}
