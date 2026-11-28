package auth

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/mail"
	"petalbook.app/server/internal/notify"
	"petalbook.app/server/internal/ratelimit"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
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
	// Mode is the tenant's registration.mode: open, invite or closed.
	Mode string
	// PasswordOpen says whether a person may sign up with a password and a
	// confirmed address: the mode is open, mail is enabled, and the tenant is
	// not disabled.
	PasswordOpen bool
	Domains      []string
	TermsURL     string
	PrivacyURL   string
	PerHour      int
}

// RegistrationMode is the registration.mode that decides who may make an
// account in a tenant. The site's value governs the default tenant, where
// people who belong to no organisation are; an organisation has its own, which
// the site's does not bound. A value that cannot be read is closed.
func (s *Service) RegistrationMode(ctx context.Context, tenant string) string {
	st := s.settings.Load()
	if st == nil {
		return settings.RegClosed
	}
	if tenant == store.DefaultTenantID {
		tenant = "" // the site's own
	}
	eff, err := st.For(ctx, tenant)
	if err != nil || eff[settings.RegistrationMode] == nil {
		return settings.RegClosed
	}
	switch m, _ := eff[settings.RegistrationMode].Value.(string); m {
	case settings.RegOpen, settings.RegInvite:
		return m
	}
	return settings.RegClosed
}

// modeErr is the refusal of a way to make an account that the tenant's mode
// does not allow: 403 registration_closed (closed) or invite_required (invite).
func modeErr(mode string) *Error {
	if mode == settings.RegInvite {
		return coded(403, "invite_required", "An invitation is needed to make an account here.")
	}
	return closedErr()
}

// Registration reports the rules of a tenant. It reads the cached settings, so
// it costs no query.
func (s *Service) Registration(ctx context.Context, tenant string) (RegRules, error) {
	st := s.settings.Load()
	if st == nil {
		return RegRules{Mode: settings.RegClosed}, nil
	}
	eff, err := st.For(ctx, tenant)
	if err != nil {
		return RegRules{}, err
	}
	var r RegRules
	r.Mode = s.RegistrationMode(ctx, tenant)
	r.PasswordOpen = r.Mode == settings.RegOpen && s.MailEnabled() && !s.st.TenantDisabled(tenant)
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

// SSOOnlyStored says whether ssoOnly is stored on, in effect or not.
func (s *Service) SSOOnlyStored(ctx context.Context) bool { return s.sec(ctx).Login.SSOOnly }

// CancelClosedRegistrations deletes the pending registrations that their
// tenant's mode no longer allows: all of them when it is closed, those that do
// not come by an email-domain join when it is invite (a join is re-checked when
// its link is used), and those of every tenant that cannot take a password
// registration (mail off, tenant disabled) when it is open. Closing
// organisation creation cancels those of the organisations people make.
func (s *Service) CancelClosedRegistrations(ctx context.Context) error {
	if !s.OrgCreation(ctx).Open {
		if _, err := store.DeleteOrgRegistrations(ctx, s.st.Q()); err != nil {
			return err
		}
	}
	ids, err := store.TenantsWithRegistrations(ctx, s.st.Q())
	if err != nil {
		return err
	}
	for _, id := range ids {
		r, err := s.Registration(ctx, id)
		if err != nil {
			return err
		}
		switch {
		case r.Mode == settings.RegClosed:
			_, err = store.DeleteRegistrationsOf(ctx, s.st.Q(), id, true)
		case !r.PasswordOpen:
			_, err = store.DeleteRegistrationsOf(ctx, s.st.Q(), id, false)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// mailGate is what every flow that sends a message asks first.
func (s *Service) needMail() error {
	if !s.MailEnabled() {
		return coded(409, "mail_not_configured", "Mail is not enabled.")
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

func errBadLink() *Error { return errf(NotFound, "That link is not valid, or has expired.") }

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
	// Org asks for an organisation of the person's own, with the person its
	// administrator, instead of a place in TenantID's tenant (which is then the
	// default tenant and is not looked at).
	Org *OrgParams
}

func closedErr() *Error { return coded(403, "registration_closed", "Registration is not open.") }

// Register takes a registration. It answers nil whether or not a message goes
// out (see docs/api.md): the caller says 202 either way.
func (s *Service) Register(ctx context.Context, p RegisterParams) error {
	// 1. the address's requests this minute
	if err := s.allow(ctx, p.IP); err != nil {
		return err
	}
	// 2. open for the tenant, or the site lets people make their own, or the
	// domain of the address joins its tenant (3.6 d5): that tenant then, as
	// the domain's tenant decided, whichever tenant the page named
	org := p.Org != nil
	if org {
		p.TenantID = store.DefaultTenantID
	}
	joinTenant, joinDomain, joinMode, joining := "", "", "", false
	if !org {
		// A domain that joins its tenant is the organisation's own opt-in, and
		// works under open and invite; closed turns every join off, which then
		// leaves the person with the tenant the page named.
		if joinTenant, joinDomain, joinMode, joining = s.joinOf(ctx, p.Email); joining {
			if s.RegistrationMode(ctx, joinTenant) == settings.RegClosed {
				joining = false
			} else {
				p.TenantID = joinTenant
			}
		}
	}
	rules, err := s.Registration(ctx, p.TenantID)
	if err != nil {
		return err
	}
	var orules OrgRules
	if org {
		if orules = s.OrgCreation(ctx); !orules.Open {
			return closedErr()
		}
	} else if joining {
		if !s.MailEnabled() || s.st.TenantDisabled(p.TenantID) {
			return closedErr()
		}
	} else if rules.Mode != settings.RegOpen {
		return modeErr(rules.Mode)
	} else if !rules.PasswordOpen {
		return closedErr()
	}
	// 3. the captcha
	st := s.sec(ctx)
	// An organisation of the person's own does not exist yet: the site's captcha.
	capTenant := p.TenantID
	if p.Org != nil {
		capTenant = ""
	}
	if err := s.checkCaptcha(ctx, st, capTenant, CtxRegister, "", p.IP, p.Captcha); err != nil {
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
		return errf(Invalid, "A name is 1 to 80 characters.")
	}
	email, err := s.cleanEmailFor(ctx, p.Email)
	if err != nil {
		return err
	}
	if email == "" || !plainAddress(email) {
		return errf(Invalid, "That does not look like an email address.")
	}
	if err := CheckPassword(p.Password); err != nil {
		return err
	}
	// 5. the domain (a joining person's; an organisation's founder is held to
	// no tenant's list, only to the hook of (d)), and the organisation's slug
	var orgName, orgSlug string
	if org {
		if orgName, orgSlug, err = s.cleanOrg(ctx, *p.Org); err != nil {
			return err
		}
		if err := s.orgCreationBlocked(ctx, email); err != nil {
			return err
		}
	} else if !joining && len(rules.Domains) > 0 && !domainAllowed(email, rules.Domains) {
		return coded(400, "email_domain_not_allowed", "Registration is open to some email domains only.")
	} else if s.tenantSSOOnly(ctx, p.TenantID) {
		// The tenant requires its single sign-on: its people come through it.
		return s.ssoRequiredOf(ctx, email)
	}
	// An address of a domain whose organisation requires its single sign-on
	// signs up there (3.6 d4): about the domain only, never the account.
	if err := s.ssoRequired(ctx, email); err != nil {
		return err
	}
	// 6. the terms
	if (rules.TermsURL != "" || rules.PrivacyURL != "") && !p.AcceptTerms {
		return coded(400, "terms_required", "Accept the terms and the privacy policy to register.")
	}
	// 7. the username
	if _, err := store.GetUserByUsername(ctx, s.st.Q(), username); err == nil {
		return errf(Exists, "That username is taken.")
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if barred, err := s.retiredName(ctx, s.st.Q(), username); err != nil {
		return err
	} else if barred {
		return errf(Exists, "That username is taken.")
	}
	if held, err := store.PendingUsername(ctx, s.st.Q(), username, s.now()); err != nil {
		return err
	} else if held {
		return errf(Exists, "That username is taken.")
	}
	// 8. the tenant's hour and its pending registrations. Every request counts,
	// whether or not the address already has an account, so the answer is the same.
	// An organisation's caps are checked before anything is written; over one,
	// the answer is the same 202 and the message says that none was made.
	capped := false
	if org {
		capped = s.orgCapped(orules)
		if !capped {
			if n, err := store.CountPendingOrgRegistrations(ctx, s.st.Q(), s.now()); err != nil {
				return err
			} else if n >= maxPending {
				capped = true
			}
		}
	} else {
		if wait, ok := s.regCaps.TakeAll(ratelimit.Cost{Key: "reg:" + p.TenantID, Limit: rules.PerHour}); !ok {
			return retryAfter(wait)
		}
		if n, err := store.CountPendingRegistrations(ctx, s.st.Q(), p.TenantID, s.now()); err != nil {
			return err
		} else if n >= maxPending {
			return retryAfter(time.Hour)
		}
	}
	// The password is hashed in every case: the work does not tell.
	hash, err := s.hasher.hash(ctx, p.Password)
	if err != nil {
		return err
	}
	lang := mail.PickLang(p.Lang, "")
	decoy, exists, err := s.heldAddress(ctx, email)
	if err != nil {
		return err
	}
	// The address's allowance, then the username is held, whether or not the
	// address has an account: for one that has, the registration is a decoy
	// whose link is never sent, so a second request for the username gets the
	// same answer either way.
	if _, ok := s.mailSlot(email, p.IP); !ok {
		return nil
	}
	// The request is charged one message, by the request and not by what becomes of
	// it: to the tenant the page named, or to no tenant for an organisation of the
	// person's own. Every branch below sends without counting, so that nothing an
	// administrator can see in their usage depends on whether the address has an
	// account, is disabled, deleted or cannot be mailed, or whether a cap is reached.
	if !org {
		s.chargeMail(p.TenantID)
	}
	if capped {
		// Nothing is held: not the username, not the slug.
		if exists != nil {
			return s.mailExisting(ctx, *exists, lang, p.IP)
		}
		if !decoy {
			if err := s.sendMail("", mail.KindRegisterOrgRefused, lang, email, mail.Vars{Org: orgName}); err != nil {
				s.log.Warn("could not queue a message", "reason", err.Error())
			}
		}
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
		Terms: terms, Created: now, Expires: now.Add(registrationTTL), OrgName: orgName, OrgSlug: orgSlug, JoinDomain: joinDomain}
	for attempt := 0; ; attempt++ {
		r.ID = s.newID()
		err = s.st.Tx(ctx, func(q store.Q) error { return store.InsertRegistration(ctx, q, r, now) })
		if err == nil {
			break
		}
		if on, ok := store.UniqueOn(err); ok {
			if strings.Contains(on, "org_slug") {
				return errSlugTaken()
			}
			if strings.Contains(on, "username") {
				return errf(Exists, "That username is taken.")
			}
			if attempt < 5 {
				continue
			}
		}
		return err
	}
	if exists != nil {
		return s.mailExisting(ctx, *exists, lang, p.IP)
	}
	if decoy {
		return nil
	}
	kind := mail.KindRegister
	switch {
	case org:
		kind = mail.KindRegisterOrg
	case joinMode == store.JoinModeRequest:
		kind, orgName = mail.KindRegisterJoinRequest, s.tenantName(p.TenantID)
	case joining:
		kind, orgName = mail.KindRegisterJoin, s.tenantName(p.TenantID)
	}
	if err := s.sendMail("", kind, lang, email, mail.Vars{Username: username, Org: orgName, Link: s.link("/register/confirm", tok), Hours: 24}); err != nil {
		s.log.Warn("could not queue a registration message", "reason", err.Error())
	}
	return nil
}

// inDomain says whether the address is of one of an organisation's verified
// domains (keys, as internal/domains keeps them), exactly: a subdomain is
// another domain, which the organisation has not verified.
func inDomain(email string, domains []string) bool {
	key, ok := store.EmailKey(email)
	if !ok {
		return false
	}
	return slices.Contains(domains, key[strings.LastIndex(key, "@")+1:])
}

// domainAllowed is registration.domains' rule (a domain or below it),
// compared in EmailKey form: a domain outside ASCII is compared as its
// punycode.
func domainAllowed(email string, domains []string) bool {
	key, ok := store.EmailKey(email)
	if !ok {
		return false
	}
	d := key[strings.LastIndex(key, "@")+1:]
	for _, a := range domains {
		if d == a || strings.HasSuffix(d, "."+a) {
			return true
		}
	}
	return false
}

// heldAddress says what a registration for an address meets, on the whole
// server. decoy: another account holds it verified (store.EmailRow.Holds), or
// is deleted and keeps it until the purge; the registration is then never
// confirmed (its link is not sent). exists: that account, with the address as
// stored, when it may be told (enabled, and the address one that may get
// account mail). An address held only unverified by a live account, or marked
// verified by a tenant administrator alone, is no obstacle: whoever confirms
// it takes it (CreateUser's TakeUnverified).
func (s *Service) heldAddress(ctx context.Context, email string) (decoy bool, exists *store.User, err error) {
	row, err := store.EmailOwner(ctx, s.st.Q(), email)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil, nil
	} else if err != nil {
		return false, nil, err
	}
	if !row.Holds() && !row.UserDeleted {
		return false, nil, nil
	}
	if row.UserDeleted || !s.rowMailable(ctx, *row) {
		return true, nil, nil
	}
	u, err := store.GetUser(ctx, s.st.Q(), store.AllTenants(), row.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return true, nil, nil
	} else if err != nil {
		return false, nil, err
	}
	if u.Disabled || s.st.TenantDisabled(u.TenantID) {
		return true, nil, nil
	}
	u.Email = row.Email
	return true, u, nil
}

// rowMailable says whether an address may be sent a link that signs in to its
// account: it is verified, by the person, a trusted sign-in or a site
// administrator (a tenant administrator's mark alone does not count: they
// could give a person of their tenant their own address), and mailable.
func (s *Service) rowMailable(ctx context.Context, row store.EmailRow) bool {
	return row.Holds() && s.mailable(ctx, row.Email)
}

// resetAddressOK says whether an account's primary address may be sent a
// link that signs in to it (rowMailable for that address).
func (s *Service) resetAddressOK(ctx context.Context, u store.User) (bool, error) {
	if u.Email == "" || !u.EmailVerified || !s.mailable(ctx, u.Email) {
		return false, nil
	}
	by, err := store.EmailVerifiedBy(ctx, s.st.Q(), u.ID)
	if err != nil {
		return false, err
	}
	return by != store.VerifiedByTenantAdmin, nil
}

// emailReleased tells an account that lost an address it held unverified to
// the one that proved it, and audits the move.
func (s *Service) emailReleased(ctx context.Context, r store.Released, to store.User) {
	if err := store.DeleteEmailTokens(ctx, s.st.Q(), r.UserID, ""); err != nil {
		s.log.Warn("could not drop the links of a released address", "error", err)
	}
	s.dropUser(r.UserID)
	if u, err := store.GetUser(ctx, s.st.Q(), store.AllTenants(), r.UserID); err == nil {
		s.cfg.Spaces.UserChanged(ctx, *u)
	}
	err := notify.WithAudit(ctx, s.st.Q(), notify.EmailReleased, notify.Entry{Actor: to.ID, User: r.UserID,
		Detail: map[string]any{"email": r.Email, "to": to.ID}, To: []string{r.UserID}, Payload: map[string]string{"email": r.Email}, Now: s.now()})
	if err != nil {
		s.log.Warn("could not write a notification", "error", err)
	}
}

// mailExisting tells an account's stored address, never the address typed,
// that it already has an account, with a link to reset the password (or to
// sign in with the provider). The caller has spent the address's allowance.
//
// It sends without counting: Register has charged the request already.
func (s *Service) mailExisting(ctx context.Context, u store.User, lang, ip string) error {
	email := u.Email
	links, sso, err := s.resetLinks(ctx, []store.User{u}, "user", "", ip)
	if err != nil {
		return err
	}
	if len(links) > 0 {
		if err := s.sendMail("", mail.KindRegisterDup, lang, email, mail.Vars{Link: links[0].URL, Hours: 1}); err != nil {
			s.log.Warn("could not queue a message", "reason", err.Error())
		}
	} else if sso != nil {
		if err := s.sendMail("", mail.KindResetSSO, lang, email, mail.Vars{Provider: s.providerNameFor(ctx, *sso)}); err != nil {
			s.log.Warn("could not queue a message", "reason", err.Error())
		}
	}
	return nil
}

// tenantName is a tenant's name, from memory.
func (s *Service) tenantName(id string) string {
	ref, _ := s.st.TenantRefOf(id)
	return ref.Name
}

// Registered is what a followed registration link made: the account and its
// session, and the organisation when it made one (Slug was taken in the
// meantime when Renamed), or the organisation it joined by the domain of its
// address (Joined). A registration that asks to join makes no account: its
// Request is the request it became (3.6 d5), and User is nil.
type Registered struct {
	User    *store.User
	Token   string
	Org     *store.Tenant
	Renamed bool
	Joined  *store.TenantRef
	Request *JoinStatus
	// Held and HeldBy: as LoginResult's.
	Held   bool
	HeldBy string
}

// RegisterConfirm makes the account a registration link is for, and signs it in.
// A registration that asked for an organisation makes the tenant with it, in
// one transaction, and the person is its administrator.
func (s *Service) RegisterConfirm(ctx context.Context, token, lang, ip string) (*Registered, error) {
	if err := s.gateToken(ctx, ip); err != nil {
		return nil, err
	}
	if !validToken(token) {
		return nil, errBadLink()
	}
	reg, err := store.FindRegistration(ctx, s.st.Q(), hashToken(token), s.now())
	if errors.Is(err, store.ErrNotFound) {
		return nil, errBadLink()
	} else if err != nil {
		return nil, err
	}
	if !ctEqual(reg.TokenHash, hashToken(token)) {
		return nil, errBadLink()
	}
	s.refundIP(ip)
	org := reg.OrgSlug != ""
	var orules OrgRules
	// A registration that joins by its domain asks the domain again: its mode
	// now decides (off falls back to the tenant's own registration).
	joinMode := ""
	if !org && reg.JoinDomain != "" {
		if t, d, m, ok := s.joinOf(ctx, reg.Email); ok && t == reg.TenantID && d == reg.JoinDomain && s.MailEnabled() && !s.st.TenantDisabled(t) && s.RegistrationMode(ctx, t) != settings.RegClosed {
			joinMode = m
		}
	}
	if org {
		if orules = s.OrgCreation(ctx); !orules.Open {
			return nil, closedErr()
		}
		// The hour's cap again, now that the person has the link: the requests
		// that were answered before it was lowered, or all at once, are counted.
		if orules.PerHour > 0 {
			n, err := store.CountSelfTenantsSince(ctx, s.st.Q(), s.now().Add(-time.Hour))
			if err != nil {
				return nil, err
			}
			if n >= orules.PerHour {
				return nil, errOrgLimit
			}
		}
	} else if joinMode != "" {
		// Joined by the domain: the tenant's own registration is not asked.
	} else if rules, err := s.Registration(ctx, reg.TenantID); err != nil {
		return nil, err
	} else if rules.Mode != settings.RegOpen {
		return nil, modeErr(rules.Mode)
	} else if !rules.PasswordOpen {
		return nil, closedErr()
	} else if reg.JoinDomain != "" && len(rules.Domains) > 0 && !domainAllowed(reg.Email, rules.Domains) {
		// A registration taken by its domain's join (which skipped the tenant's
		// domain list) whose join is off now: the tenant's registration, with
		// every rule of it.
		return nil, coded(400, "email_domain_not_allowed", "Registration is open to some email domains only.")
	}
	st := s.sec(ctx)
	if s.ssoOnlyInEffect(st) {
		return nil, errLocalRefused()
	}
	// An organisation that has since come to require its single sign-on
	// (for the tenant joined, or for the domain of the address) makes no
	// password account: the link answers as the registration would now.
	if !org && s.tenantSSOOnly(ctx, reg.TenantID) {
		return nil, s.ssoRequiredOf(ctx, reg.Email)
	}
	if err := s.ssoRequired(ctx, reg.Email); err != nil {
		return nil, err
	}
	if joinMode == store.JoinModeRequest {
		req, err := s.requestJoin(ctx, reg, mail.PickLang(lang, ""), ip)
		if err != nil {
			return nil, err
		}
		return &Registered{Request: req}, nil
	}
	p := CreateParams{Username: reg.Username, Name: reg.Name, Email: reg.Email, EmailVerified: true, TakeUnverified: true, Role: "member",
		PasswordHash: reg.PasswordHash, TenantID: reg.TenantID, RegistrationID: reg.ID, Terms: reg.Terms}
	if joinMode == store.JoinModeAuto {
		p.Role = s.joinRole(ctx, reg.TenantID)
	}
	if org {
		p.Role = "admin"
		p.NewTenant = &NewTenant{Name: reg.OrgName, Slug: reg.OrgSlug, PlanSlug: orules.PlanSlug, MaxPerPerson: orules.PerPerson}
	}
	u, err := s.CreateUser(ctx, p)
	if err != nil {
		var ae *Error
		if errors.Is(err, store.ErrTenantLimit) {
			return nil, coded(409, "tenant_limit", "A server has at most %d tenants.", store.MaxTenants)
		} else if org && errors.As(err, &ae) && ae.Code == "org_limit" {
			return nil, ae
		}
		return nil, err
	}
	held := s.passkeyRequiredOf(ctx, st, *u)
	tok, err := s.issueSession(ctx, u.ID, held, "registration")
	if err != nil {
		return nil, err
	}
	res := &Registered{User: u, Token: tok, Held: held}
	s.Audit(AuditEvent{Kind: "registered", Actor: u.ID, User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"email": reg.Email}})
	if joinMode == store.JoinModeAuto {
		ref, _ := s.st.TenantRefOf(u.TenantID)
		res.Joined = &ref
		err := notify.WithAudit(ctx, s.st.Q(), notify.JoinAuto, notify.Entry{Actor: u.ID, User: u.ID, Username: u.Username, IP: ip, Tenant: u.TenantID, Now: s.now(),
			Detail: map[string]any{"username": u.Username, "email": reg.Email, "domain": reg.JoinDomain}})
		if err != nil {
			s.log.Warn("could not audit a join", "error", err)
		}
	}
	if org {
		t := p.NewTenant.Made
		res.Org, res.Renamed = &t, t.Slug != reg.OrgSlug
		s.Audit(AuditEvent{Kind: "tenant_created", Actor: u.ID, Tenant: t.ID, IP: ip, Detail: map[string]any{"tenant": t.ID, "slug": t.Slug, "origin": store.OriginSelf}})
	}
	s.Audit(AuditEvent{Kind: "login", Tenant: u.TenantID, User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"method": "password", "created": true}})
	return res, nil
}

// --- email verification ----------------------------------------------------------

// RequestEmailVerify sends the signed-in person a link to confirm their address.
func (s *Service) RequestEmailVerify(ctx context.Context, u store.User, lang, ip string) error {
	if err := s.needMail(); err != nil {
		return err
	}
	if u.Email == "" {
		return errf(Invalid, "This account has no email address; set one first.")
	}
	if !s.mailable(ctx, u.Email) {
		if _, ok := store.EmailKey(u.Email); ok {
			return errNotASCII()
		}
		return errf(Invalid, "This address cannot be used; set it again.")
	}
	// Already verified, unless only a tenant administrator marked it: the
	// person confirms it themself, which clears the mark (store.EmailRow.Holds).
	if u.EmailVerified {
		by, err := store.EmailVerifiedBy(ctx, s.st.Q(), u.ID)
		if err != nil {
			return err
		}
		if by != store.VerifiedByTenantAdmin {
			return coded(409, "email_verified", "This address is already verified.")
		}
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
	return s.sendMail(u.TenantID, mail.KindVerify, mail.PickLang(lang, ""), u.Email, mail.Vars{Name: u.Name, Username: u.Username, Email: u.Email, Link: s.link("/verify-email", tok), Hours: 24})
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
	if errors.Is(err, store.ErrNotFound) || err == nil && (u.Disabled || s.st.TenantDisabled(u.TenantID) || !s.mailable(ctx, t.Email) || !ctEqual(t.Hash, hash)) {
		return errBadLink()
	} else if err != nil {
		return err
	}
	primary := u.Email == t.Email
	if primary {
		err = s.st.Tx(ctx, func(q store.Q) error {
			if ok, err := store.UseEmailToken(ctx, q, hash, s.now()); err != nil {
				return err
			} else if !ok {
				return errBadLink()
			}
			return store.SetEmailRowVerified(ctx, q, u.ID, t.Email, true, "")
		})
	} else {
		// Another address of the account (accounts.multipleEmails), or one it
		// claimed from an account that held it unverified.
		var released *store.Released
		if released, err = s.confirmOtherEmail(ctx, *u, *t, hash); err == nil && released != nil {
			defer s.emailReleased(ctx, *released, *u)
		}
	}
	if err != nil {
		return err
	}
	s.refundIP(ip)
	s.dropUser(u.ID)
	if primary {
		u.EmailVerified = true
		s.cfg.Spaces.UserChanged(ctx, *u)
	}
	s.Audit(AuditEvent{Kind: "email_verified", Actor: u.ID, User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"email": t.Email, "primary": primary}})
	return nil
}

// NoteEmailChange tells the old, verified address that the account's address
// was changed. The note has no link; nothing is said when mail is off or the
// address has had its messages for the hour.
func (s *Service) NoteEmailChange(old store.User, lang, ip string) {
	if !s.MailEnabled() || old.Email == "" || !s.mailable(context.Background(), old.Email) || !old.EmailVerified {
		return
	}
	if _, ok := s.mailSlot(old.Email, ip); !ok {
		return
	}
	if err := s.sendMail(old.TenantID, mail.KindEmailChanged, mail.PickLang(lang, ""), old.Email, mail.Vars{}); err != nil {
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
		return coded(403, "password_reset_off", "Password reset is not available.")
	}
	st := s.sec(ctx)
	if err := s.checkCaptcha(ctx, st, p.TenantID, CtxForgot, "", p.IP, p.Captcha); err != nil {
		return err
	}
	login := strings.TrimSpace(p.Login)
	if login == "" || len(login) > 254 {
		return errf(Invalid, "Login is a username or an email address.")
	}
	var accounts []store.User
	if strings.Contains(login, "@") {
		// By a verified address of an account, on the whole server (an
		// address belongs to one account; one that is not the primary while
		// the tenant's accounts.multipleEmails is on); the mail goes to that
		// address as stored. An address outside ASCII finds nobody while the site does
		// not accept them.
		if store.EmailIsASCII(login) || s.nonASCIIEmail(ctx) {
			u, row, err := s.userBySignInEmail(ctx, login)
			switch {
			case err == nil:
				if !u.Disabled && !s.st.TenantDisabled(u.TenantID) && s.rowMailable(ctx, *row) {
					u.Email = row.Email
					accounts = append(accounts, *u)
				}
			case !errors.Is(err, store.ErrNotFound):
				return err
			}
		}
	} else if un, err := NormalizeUsername(login); err == nil {
		switch u, err := store.GetUserByUsername(ctx, s.st.Q(), un); {
		case err == nil:
			if !u.Disabled && !s.st.TenantDisabled(u.TenantID) {
				if ok, err := s.resetAddressOK(ctx, *u); err != nil {
					return err
				} else if ok {
					accounts = append(accounts, *u)
				}
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
		if err := s.sendMail("", mail.KindResetSSO, lang, to, mail.Vars{Provider: s.providerNameFor(ctx, *sso)}); err != nil {
			s.log.Warn("could not queue a message", "reason", err.Error())
		}
		return nil
	default:
		return nil
	}
	if err := s.sendMail("", mail.KindReset, lang, to, v); err != nil {
		s.log.Warn("could not queue a message", "reason", err.Error())
	}
	return nil
}

// canUsePassword says whether the account could sign in with a password: it
// has one, and ssoOnly does not refuse it. A reset never adds a password the
// person could not already use.
func (s *Service) canUsePassword(ctx context.Context, q store.Q, u store.User) (bool, error) {
	if _, err := store.GetPasswordHash(ctx, q, u.ID); errors.Is(err, store.ErrNotFound) {
		// An account an administrator made has none until its person uses the
		// link it was given (still open), has no sign-in of any provider, and
		// where no SSO is required for it, a password is one the person could
		// use: a reset sets it.
		open := 0
		for _, kind := range []string{store.TokenWelcome, store.TokenWelcomeMail} {
			n, err := store.CountEmailTokens(ctx, q, u.ID, kind, s.now())
			if err != nil {
				return false, err
			}
			open += n
		}
		if open == 0 {
			return false, nil
		}
		if links, err := store.ListLinks(ctx, q, u.ID); err != nil || len(links) > 0 {
			return false, err
		}
		return !s.localRefusedUser(ctx, s.sec(ctx), &u), nil
	} else if err != nil {
		return false, err
	}
	return !s.localRefusedUser(ctx, s.sec(ctx), &u), nil
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
		s.AuditIn(ctx, AuditEvent{Kind: "password_reset_requested", Actor: actor, User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"by": by}})
	}
	return links, sso, nil
}

// providerNameFor is how a message names where an account signs in: the
// provider of its identity, else the enabled providers, else "single sign-on".
func (s *Service) providerNameFor(ctx context.Context, u store.User) string {
	var names []string
	if ids, err := store.ListLinks(ctx, s.st.Q(), u.ID); err == nil {
		for _, id := range ids {
			if n, _, _ := s.providerByID(id.ProviderID); n != "" && !slices.Contains(names, n) {
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
	// A reset link, or the link that sets the first password of an account an
	// administrator made (mailed, or copied by the administrator).
	var t *store.EmailToken
	for _, kind := range []string{store.TokenReset, store.TokenWelcomeMail, store.TokenWelcome} {
		var err error
		if t, err = store.FindEmailToken(ctx, s.st.Q(), kind, hash, s.now()); err == nil {
			break
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, nil, err
		}
		t = nil
	}
	if t == nil {
		return nil, nil, errBadLink()
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
	// The link is for the address it was sent to: once the account no longer
	// signs in by that address (it was changed or removed, or it is not the
	// primary and the switch is off) it is no good, however that came about.
	if u.Disabled || s.st.TenantDisabled(u.TenantID) {
		return nil, nil, errBadLink()
	}
	if t.Email != u.Email {
		owner, _, err := s.userBySignInEmail(ctx, t.Email)
		if errors.Is(err, store.ErrNotFound) || err == nil && owner.ID != u.ID {
			return nil, nil, errBadLink()
		} else if err != nil {
			return nil, nil, err
		}
	}
	// A reset never adds a password the person could not already use; the first
	// password of an account made by an administrator has none yet, and is
	// refused only where the person's sign-in is the organisation's SSO.
	if t.Kind == store.TokenReset {
		if ok, err := s.canUsePassword(ctx, s.st.Q(), *u); err != nil {
			return nil, nil, err
		} else if !ok {
			return nil, nil, errBadLink()
		}
	} else if s.localRefusedUser(ctx, s.sec(ctx), u) {
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
	verifies := t.Kind == store.TokenWelcomeMail && t.Email != "" && t.Email == u.Email
	err = s.st.Tx(ctx, func(q store.Q) error {
		if ok, err := store.UseEmailToken(ctx, q, t.Hash, s.now()); err != nil {
			return err
		} else if !ok {
			return errBadLink()
		}
		if err := store.SetPasswordHash(ctx, q, u.ID, hash, s.now()); err != nil {
			return err
		}
		// The mailed first-password link reached the address: it proves it.
		if verifies {
			if err := store.SetEmailRowVerified(ctx, q, u.ID, t.Email, true, ""); err != nil {
				return err
			}
		}
		if err := store.DeleteResetTokens(ctx, q, u.ID); err != nil {
			return err
		}
		return store.DeleteUserSessions(ctx, q, u.ID, "", store.EndPassword, s.now())
	})
	if err != nil {
		return err
	}
	s.refundIP(ip)
	s.evictUser(u.ID, "")
	if verifies {
		s.dropUser(u.ID)
		u.EmailVerified = true
		s.cfg.Spaces.UserChanged(ctx, *u)
	}
	ukey := userKey(u.Username)
	s.failUser.Reset(ukey)
	s.failPair.ResetWhere(func(k string) bool { return strings.HasSuffix(k, "|"+ukey) })
	by := "user"
	if t.Kind != store.TokenReset {
		by = "welcome"
	}
	s.Audit(AuditEvent{Kind: "password_reset", Actor: u.ID, User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"by": by}})
	return nil
}

// AdminPasswordReset sends a reset link to a person's verified address.
func (s *Service) AdminPasswordReset(ctx context.Context, actor store.User, access authz.Subject, ip, targetID, lang string) error {
	u, err := store.GetUser(ctx, s.st.Q(), store.ScopeOf(actor), targetID)
	if errors.Is(err, store.ErrNotFound) {
		return errf(NotFound, "No such user.")
	} else if err != nil {
		return err
	}
	if u.IsSiteAdmin() && !access.Can(authz.SiteAdminsManage, "") {
		return errf(Forbidden, "Only a site administrator may reset a site administrator's password.")
	}
	if err := s.needMail(); err != nil {
		return err
	}
	if ok, err := s.resetAddressOK(ctx, *u); err != nil {
		return err
	} else if !ok {
		return coded(409, "email_unverified", "This account has no verified email address.")
	}
	ok, err := s.canUsePassword(ctx, s.st.Q(), *u)
	if err != nil {
		return err
	}
	if !ok || u.Disabled {
		return errf(Conflict, "This account has no password it could sign in with.")
	}
	if wait, ok := s.mailSlot(u.Email, ""); !ok {
		return retryAfter(wait)
	}
	links, _, err := s.resetLinks(ctx, []store.User{*u}, "admin", actor.ID, ip)
	if err != nil {
		return err
	}
	return s.sendMail(u.TenantID, mail.KindReset, mail.PickLang(lang, ""), u.Email, mail.Vars{Link: links[0].URL, Hours: 1})
}

// codePeopleLimit is the refusal of an account that would take a tenant over
// its quota.people.
const codePeopleLimit = "people_limit"

func errPeopleLimit() *Error {
	return coded(409, codePeopleLimit, "This organization has reached the number of people it may have.")
}

// PeopleCap is the tenant's quota.people in force (nil: none). It reads the
// settings, which may take a connection of the pool: call it before a
// transaction, never inside one. A read that fails is an error: nobody is let
// in without the cap being known.
func (s *Service) PeopleCap(ctx context.Context, tenant string) (*int64, error) {
	st := s.settings.Load()
	if st == nil {
		return nil, nil
	}
	e, err := st.Get(ctx, tenant, settings.QuotaPeople)
	if err != nil {
		return nil, err
	}
	var n int64
	switch v := e.Value.(type) {
	case int64:
		n = v
	case int:
		n = int64(v)
	case float64:
		n = int64(v)
	default:
		return nil, nil
	}
	return &n, nil
}

// withinPeople refuses an account that took the tenant over limit (its
// quota.people, read before the transaction; nil: none), called in the
// transaction q that made the account, after the account was counted (so the
// count includes it). Every way an account is made goes through CreateUser, so
// this is the one place that holds the cap. first is the command line's account:
// the very first of the tenant is not held to it. It touches nothing but q.
func withinPeople(ctx context.Context, q store.Q, tenant string, limit *int64, first bool) error {
	if limit == nil {
		return nil
	}
	u, err := store.GetUsage(ctx, q, store.UsageTenant, tenant)
	if err != nil {
		return err
	}
	if u.People > *limit && !(first && u.People <= 1) {
		return errPeopleLimit()
	}
	return nil
}
