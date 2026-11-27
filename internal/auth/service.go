// Package auth is accounts and sign-in: passwords, sessions, the first-run
// setup, invites, OpenID Connect, and the rules about who may change whom. It
// knows nothing about HTTP routes; the api package asks it questions and maps
// its answers.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/KazuhaHub/authcore/captcha"
	authsaml "github.com/KazuhaHub/authcore/saml"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/mail"
	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/perm"
	"petalbook.app/server/internal/ratelimit"
	"petalbook.app/server/internal/secret"
	"petalbook.app/server/internal/secrets"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

// negative is a cookie that is no session: until when to answer so without
// asking again, and why it ended ("" when no record says).
type negative struct {
	until  time.Time
	reason string
}

// Session lifetimes of docs/api.md.
const (
	DefaultSessionIdle = 30 * 24 * time.Hour
	DefaultSessionMax  = 90 * 24 * time.Hour
	// touchEvery is the least time between two writes of a session's "last
	// used" to the database.
	touchEvery = time.Hour
	// maxSessionsPerUser bounds the rows signing in repeatedly can leave.
	maxSessionsPerUser = 50

	maxCachedSessions = 10_000
	maxNegative       = 4096
	negativeTTL       = 30 * time.Second
)

// Config configures a Service.
type Config struct {
	Store  *store.Store
	Spaces *spaces.Manager
	// PublicURL is the address people use, e.g. https://notes.example.com. Its
	// scheme decides whether cookies are Secure. May be empty (plain http,
	// loopback or testing) unless a provider is configured.
	PublicURL      string
	TrustedProxies []netip.Prefix
	Password       PasswordParams // zero: DefaultPasswordParams
	// HashConcurrency is how many password hashes may run at once (default 4).
	HashConcurrency int
	SessionIdle     time.Duration
	SessionMax      time.Duration
	// CacheTTL bounds how long a cached session or user is trusted without
	// asking the database again: changes made by another process (the
	// command-line tool) take effect within it. Default 5 minutes.
	CacheTTL time.Duration
	// HTTPClient reaches the identity providers, the SAML metadata, GitHub and
	// the captcha providers; a default client when nil (tests give their own).
	HTTPClient *http.Client
	// Secrets seals what is kept in the state database; the space manager's
	// box when nil.
	Secrets *secret.Box
	// CaptchaEndpoint replaces the siteverify address of the third-party captcha (tests).
	CaptchaEndpoint string
	// Mail is for tests: the clock, backoff and trusted roots of the mail
	// service. Mail is off until an administrator enables it, whatever this says.
	Mail mail.Options
	// OrgDomains lists the verified email domains of a tenant. Domain
	// verification arrives with 3.6 (d); until it is set no tenant has any, so
	// no tenant-owned sign-in provider can be turned on and none vouches for an
	// address (see orgSignInGate, trustsEmail).
	OrgDomains func(ctx context.Context, tenantID string) []string
	// ManagedDomain is the tenant that holds the domain of an address verified
	// and keeps others from making organisations on it ("" for none): the
	// in-memory map of internal/domains, asked by orgCreationBlocked.
	ManagedDomain func(ctx context.Context, email string) string
	// RouteDomain is where the domain of an address routes its sign-in: the
	// tenant that holds it verified and the provider it is routed to (the
	// in-memory map of internal/domains, asked by Discover).
	RouteDomain func(ctx context.Context, email string) (tenant, provider string, ok bool)
	// RoutedProviders is every provider some verified domain is routed to.
	RoutedProviders func() []string
	// Outbound spends one outgoing request of a tenant's outbound share
	// (rate.outboundPerMinute): the calls its own providers make. ok false is
	// the share used up for wait. Nil counts nothing.
	Outbound func(ctx context.Context, tenant string) (wait time.Duration, ok bool, err error)
	Logger   *slog.Logger
	Now      func() time.Time
}

// Service is safe for concurrent use.
type Service struct {
	cfg    Config
	st     *store.Store
	log    *slog.Logger
	now    func() time.Time
	hasher *hasher
	secure bool

	usersExist atomic.Bool
	setupMu    sync.Mutex
	setupToken string // in memory only; "" once a user exists

	// Limits, all in memory and bounded: requests per address per minute, and
	// failures by address, by username and by the pair; the starts of single
	// sign-on have their own allowance.
	perMin                     *ratelimit.Window
	auditIP                    *ratelimit.Window // auditThrottled: one row per address, reason and minute
	failIP, failUser, failPair *ratelimit.Lockout
	starts                     *ratelimit.Bucket

	// The security settings in force (see security.go), the audit log, the
	// providers, passkeys and captcha.
	secPtr       atomic.Pointer[secState]
	secMu        sync.Mutex
	keysMu       sync.Mutex // the captcha's keys
	vault        *secrets.Service
	audit        *auditLog
	provs        *providerSet
	replay       *authsaml.MemoryReplayCache
	pk           *passkeyState
	capImg       *captcha.ImageGenerator
	capStore     *captcha.MemoryStore
	capTok       atomic.Pointer[tokenChecker]
	capSecretErr atomic.Bool

	// onNewUser is told whose usage rows a new account reached (its own and its
	// tenant's people), after it is made; nil: nobody is told.
	onNewUser func(ctx context.Context, touched []store.Touched)

	// adminMu serialises changes that must keep one enabled administrator.
	adminMu sync.Mutex
	// credMu serialises changes that must keep one way to sign in.
	credMu sync.Mutex

	cmu      sync.Mutex
	gen      uint64 // bumped by every eviction, so a slow lookup cannot resurrect what was evicted
	sessions map[string]*cachedSession
	users    map[string]*cachedUser
	negative map[string]negative

	pending *pendingStore
	// ptests are the results of the administrators' test sign-ins (providertest.go).
	ptests providerTests

	// Mail (see mailcfg.go): the sender, the caps of an hour, and the settings
	// that say whether registration is open (set by the API's constructor).
	mail     *mail.Service
	mailMu   sync.Mutex
	mailCaps *ratelimit.Window // messages per address and per caller
	testCaps *ratelimit.Window // test messages
	regCaps  *ratelimit.Window // registrations per tenant
	settings atomic.Pointer[settings.Service]
}

type cachedSession struct {
	userID    string
	created   time.Time
	lastUsed  time.Time
	persisted time.Time // the last_used the database has
	held      bool
	holdBy    string // what a held session waits for: "" a passkey, HoldPassword a new password
	loaded    time.Time
	// The context and the proofs reserved for 3.7, as read with the session.
	// Never written back; the map is replaced whole (UpdateSessionState drops
	// the entry), never changed in place.
	currentTenant string
	proofs        map[string]store.Proof
}

type cachedUser struct {
	u store.User
	// access is what the person holds outside spaces, built with the entry.
	access authz.Subject
	loaded time.Time
}

// Principal is who a request is from.
type Principal struct {
	User      store.User
	SessionID string // hash of the session cookie; "" for the built-in user
	// SessionCreated is when the session began (the sign-in).
	SessionCreated time.Time
	// Held: the session may only register a passkey (HeldBy "") or choose a
	// new password (HoldPassword), and sign out.
	Held   bool
	HeldBy string
	// Access is what the person holds outside the content of spaces: their
	// permission keys (internal/authz). A grant never widens it, and no route
	// outside a space takes a grant.
	Access authz.Subject
	// Grant is set when the request is made with a credential of narrower
	// reach than the person (an API token, later an OAuth access token); nil
	// for a session and for the built-in user.
	Grant *Grant
}

// Grant is what a credential other than a session may do for its owner: the
// owner's own permissions narrowed to some spaces and to read or read and
// write. The owner's current permissions still apply; a grant never adds to
// them. It is immutable once made: callers share it.
type Grant struct {
	// Kind is "token" for an API token.
	Kind string
	ID   string
	Name string
	// Write: the grant reaches write as well as read.
	Write bool
	// Spaces are the ids of the spaces it names, sorted.
	Spaces []string
	set    map[string]struct{}
}

// NewGrant makes a grant; spaces need not be sorted or free of repeats.
func NewGrant(kind, id, name string, write bool, spaceIDs []string) *Grant {
	set := make(map[string]struct{}, len(spaceIDs))
	for _, s := range spaceIDs {
		set[s] = struct{}{}
	}
	ids := make([]string, 0, len(set))
	for s := range set {
		ids = append(ids, s)
	}
	sort.Strings(ids)
	return &Grant{Kind: kind, ID: id, Name: name, Write: write, Spaces: ids, set: set}
}

// Names reports whether the grant names the space.
func (g *Grant) Names(spaceID string) bool { _, ok := g.set[spaceID]; return ok }

// Access is "read" or "write".
func (g *Grant) Access() string {
	if g.Write {
		return "write"
	}
	return "read"
}

// Perms is the most the grant gives in a space it names.
func (g *Grant) Perms() perm.Set {
	if g.Write {
		return perm.Of(perm.Read, perm.Write)
	}
	return perm.Of(perm.Read)
}

// New returns the service, and while there is no user at all makes the setup
// token (read it with SetupToken).
func New(ctx context.Context, cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Spaces == nil {
		return nil, errors.New("auth: a store and a space manager are required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.SessionIdle <= 0 {
		cfg.SessionIdle = DefaultSessionIdle
	}
	if cfg.SessionMax <= 0 {
		cfg.SessionMax = DefaultSessionMax
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 5 * time.Minute
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	h, err := newHasher(cfg.Password, cfg.HashConcurrency)
	if err != nil {
		return nil, err
	}
	s := &Service{
		cfg: cfg, st: cfg.Store, log: cfg.Logger, now: cfg.Now, hasher: h,
		audit: newAuditLog(), provs: newProviderSet(), replay: authsaml.NewMemoryReplayCache(authsaml.DefaultReplayCacheCapacity),
		sessions: map[string]*cachedSession{}, users: map[string]*cachedUser{}, negative: map[string]negative{},
		pending: newPendingStore(cfg.Now),
	}
	s.initLimits(cfg.Now)
	s.vault = secrets.Of(cfg.Store, s.rawBox())
	s.settings.Store(settings.Of(cfg.Store))
	s.bindSettings()
	mo := cfg.Mail
	if mo.Now == nil {
		mo.Now = cfg.Now
	}
	if mo.Logger == nil {
		mo.Logger = cfg.Logger
	}
	s.mail = mail.New(mo)
	s.initCaptcha()
	if cfg.PublicURL != "" {
		u, err := url.Parse(cfg.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errors.New("auth: the public URL must look like https://notes.example.com")
		}
		s.secure = u.Scheme == "https"
	}
	s.sec(ctx) // reads the settings, and builds what depends on them
	if err := s.loadMail(ctx); err != nil {
		return nil, err
	}
	if err := s.reloadProviders(ctx); err != nil {
		return nil, err
	}
	if err := s.initPasskeys(ctx); err != nil {
		return nil, err
	}
	exists, err := store.HasUsers(ctx, s.st.Q())
	if err != nil {
		return nil, err
	}
	if exists {
		s.usersExist.Store(true)
	} else {
		s.setupToken = randomToken()
	}
	// A custom role held through a group follows the group's people at once.
	cfg.Spaces.OnMembershipChanged(s.membershipChanged)
	return s, nil
}

// SetupToken is the first-run token, or "" once an account exists. It is kept
// only in memory and is meant to be logged once at start-up.
func (s *Service) SetupToken() string {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if s.usersExist.Load() {
		return ""
	}
	return s.setupToken
}

// SetupOpen reports whether no account exists yet. Once one does, it stays
// true without asking the database again.
func (s *Service) SetupOpen(ctx context.Context) (bool, error) {
	if s.usersExist.Load() {
		return false, nil
	}
	ok, err := store.HasUsers(ctx, s.st.Q())
	if err != nil {
		return false, err
	}
	if ok {
		s.markUsersExist()
	}
	return !ok, nil
}

// OnNewUser installs what is told whose usage a new account reached (the quotas
// look at the person and at the tenant's people). Call it before serving.
func (s *Service) OnNewUser(fn func(ctx context.Context, touched []store.Touched)) { s.onNewUser = fn }

func (s *Service) markUsersExist() {
	s.setupMu.Lock()
	s.usersExist.Store(true)
	s.setupToken = ""
	s.setupMu.Unlock()
}

// ClientAddr is the client address used for rate limits.
func (s *Service) ClientAddr(r *http.Request) string { return ClientAddr(r, s.cfg.TrustedProxies) }

// --- cookies -----------------------------------------------------------------

// SessionCookieName is __Host-petalbook_session over https, which a browser
// then refuses to set from another host or path, and petalbook_session over
// plain http (where the prefix is not allowed).
func (s *Service) SessionCookieName() string { return s.cookieName("petalbook_session") }

func (s *Service) cookieName(base string) string {
	if s.secure {
		return "__Host-" + base
	}
	return base
}

func (s *Service) newCookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode}
}

// FlowCookieName is the name of a cookie that ties a short flow (connecting a
// GitHub account) to the browser that started it, with the same prefix rule as
// the session cookie.
func (s *Service) FlowCookieName(base string) string { return s.cookieName(base) }

// FlowCookie carries the binding value of such a flow to the browser for ttl.
func (s *Service) FlowCookie(base, value string, ttl time.Duration) *http.Cookie {
	return s.newCookie(s.cookieName(base), value, int(ttl/time.Second))
}

// ClearFlowCookie tells the browser to drop it.
func (s *Service) ClearFlowCookie(base string) *http.Cookie {
	return s.newCookie(s.cookieName(base), "", -1)
}

// SessionCookie carries a new session token to the browser.
func (s *Service) SessionCookie(token string) *http.Cookie {
	return s.newCookie(s.SessionCookieName(), token, int(s.secPtr.Load().max/time.Second))
}

// ClearSessionCookie tells the browser to drop its session.
func (s *Service) ClearSessionCookie() *http.Cookie {
	return s.newCookie(s.SessionCookieName(), "", -1)
}

// --- helpers -----------------------------------------------------------------

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("auth: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func ctEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func retryAfter(d time.Duration) *Error {
	return &Error{Kind: RateLimited, Msg: "Too many attempts; wait a little and try again.", RetryAfter: d}
}

// newID makes an id for a row; callers retry on a unique violation.
func (s *Service) newID() string { return page.NewID(s.now()) }

// --- validation --------------------------------------------------------------

// NormalizeUsername folds ASCII upper case to lower and checks the form:
// 2-32 characters of a-z, 0-9, '.', '_', '-', starting with a letter or digit.
// Only ASCII is folded: Unicode folding would map some other characters onto
// ASCII letters.
func NormalizeUsername(in string) (string, error) {
	b := []byte(strings.TrimSpace(in))
	if len(b) < 2 || len(b) > 32 {
		return "", errf(Invalid, "A username is 2 to 32 characters.")
	}
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
			c = b[i]
		}
		alnum := c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || (c != '.' && c != '_' && c != '-')) {
			return "", errf(Invalid, "A username uses a-z, 0-9, '.', '_' and '-', and starts with a letter or digit.")
		}
	}
	return string(b), nil
}

func cleanDisplayName(in string) (string, error) {
	n := strings.TrimSpace(in)
	if c := utf8.RuneCountInString(n); c < 1 || c > 100 || !utf8.ValidString(n) {
		return "", errf(Invalid, "A name is 1 to 100 characters.")
	}
	if strings.IndexFunc(n, func(r rune) bool { return r < 0x20 || r == 0x7f || r == 0x85 || r == 0x2028 || r == 0x2029 }) >= 0 {
		return "", errf(Invalid, "A name cannot contain control characters.")
	}
	return n, nil
}

// cleanEmail checks the form of an address: an @ that is neither first nor
// last, at most 254 bytes, no space, control or format character, < or >, no
// dot at the end, and a store.EmailKey (which has no empty label). It lets characters outside ASCII through: whether those
// may be set is the site's (see Service.cleanEmailFor).
func cleanEmail(in string) (string, error) {
	e := strings.TrimSpace(in)
	if e == "" {
		return "", nil
	}
	at := strings.LastIndex(e, "@")
	if len(e) > 254 || at < 1 || at == len(e)-1 || !utf8.ValidString(e) || strings.ContainsFunc(e, func(r rune) bool {
		return r <= 0x20 || r == 0x7f || r == '<' || r == '>' || unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r)
	}) {
		return "", errf(Invalid, "That does not look like an email address.")
	}
	// The key leaves out the root's dot at the end of the domain (so that it
	// is not a second spelling); an address that has one is not stored.
	if _, ok := store.EmailKey(e); !ok || strings.HasSuffix(e, ".") {
		return "", errf(Invalid, "That does not look like an email address.")
	}
	return e, nil
}

// errNotASCII refuses an address outside ASCII while users.emailNonAscii is off.
func errNotASCII() *Error {
	return coded(400, "email_not_ascii", "Addresses with characters outside ASCII are not accepted on this server.")
}

// errEmailTaken is an address another account holds.
func errEmailTaken() *Error {
	return coded(409, CodeEmailTaken, "Another account has that email address.")
}

// emailErr turns store.ErrEmailTaken into its answer.
func emailErr(err error) error {
	if errors.Is(err, store.ErrEmailTaken) {
		return errEmailTaken()
	}
	return err
}

// nonASCIIEmail says whether addresses outside ASCII may be set (the site's
// users.emailNonAscii, from the settings cache; off when there is none).
func (s *Service) nonASCIIEmail(ctx context.Context) bool {
	st := s.settings.Load()
	if st == nil {
		return false
	}
	e, err := st.Get(ctx, "", settings.UsersEmailNonASCII)
	if err != nil {
		return false
	}
	on, _ := e.Value.(bool)
	return on
}

// cleanEmailFor is cleanEmail with the site's rule on addresses outside ASCII.
func (s *Service) cleanEmailFor(ctx context.Context, in string) (string, error) {
	e, err := cleanEmail(in)
	if err != nil || e == "" {
		return e, err
	}
	if !store.EmailIsASCII(e) && !s.nonASCIIEmail(ctx) {
		return "", errNotASCII()
	}
	return e, nil
}

// mailable says whether account mail (a link that signs in, a reset) may go
// to a stored address: it has a key, and it is ASCII or the site accepts
// addresses outside ASCII (sending to one needs a relay with SMTPUTF8).
func (s *Service) mailable(ctx context.Context, email string) bool {
	if _, ok := store.EmailKey(email); email == "" || !ok {
		return false
	}
	return store.EmailIsASCII(email) || s.nonASCIIEmail(ctx)
}

func checkRole(r string) error {
	if r != "admin" && r != "member" {
		return errf(Invalid, "Role is admin or member.")
	}
	return nil
}

// --- creating accounts -------------------------------------------------------

// CreateParams describes an account to make.
type CreateParams struct {
	Username string
	Name     string // defaults to the username
	Email    string
	Role     string // admin or member
	Password string // "" for an account that signs in through an identity only
	Identity *IdentityParams
	InviteID string   // the invite this account uses up
	Groups   []string // local groups of the tenant the account joins, from the invite
	// Setup makes this the first account: it fails with ErrExists if any
	// account exists, even when two setups run at once. FirstOK is the
	// command-line form: it also records that an account exists, but never refuses.
	Setup, FirstOK bool
	// EmailVerified: the address was proved (a confirmed registration,
	// VerifiedBy "") or a trusted sign-in vouched for it (VerifiedBy
	// store.VerifiedByProvider).
	EmailVerified bool
	VerifiedBy    string
	// TakeUnverified takes a verified address from another account that holds
	// it unverified; that account is told and the move audited. DropTakenEmail
	// makes the account without an address when another account holds it (a
	// sign-in); otherwise that is 409 email_taken.
	TakeUnverified, DropTakenEmail bool
	// RetryUsername lets the name get a numeric suffix when it is taken (accounts made from an
	// identity provider's claims).
	RetryUsername bool
	// TenantID is the tenant the account is made in (the default tenant when
	// empty); SiteAdmin makes an administrator one of the whole server. An
	// invite decides both (see AcceptInvite), and the first account is always a
	// site administrator of the default tenant.
	TenantID  string
	SiteAdmin bool
	// PasswordHash is a hash already made (a confirmed registration); it is
	// used instead of Password. RegistrationID is the pending registration the
	// account uses up, and Terms what the person accepted ("" for none).
	PasswordHash   string
	RegistrationID string
	Terms          string
	// NewTenant makes the account's tenant in the same transaction, with the
	// account as its administrator (Role must be admin; SiteAdmin and TenantID
	// are ignored). CreateUser fills in the tenant it made.
	NewTenant *NewTenant
}

// NewTenant is the tenant an account makes with itself (an organisation a
// person registers). Slug may be given a numeric suffix when it is taken in the
// meantime; Made is the tenant as it was stored.
type NewTenant struct {
	Name, Slug string
	// PlanSlug is the plan it starts with, "" for none (a plan that does not
	// exist is none).
	PlanSlug string
	// MaxPerPerson is how many the person may have made; the count is read in
	// the transaction.
	MaxPerPerson int
	Made         store.Tenant
}

// errOrgLimit is the refusal of an organisation over the person's cap.
var errOrgLimit = coded(403, "org_limit", "No more organisations can be made.")

// IdentityParams is a linked account to attach: the provider's own id (never
// its issuer), the subject there, and what the provider said of the address.
type IdentityParams struct {
	ProviderID, Subject, Label string
	Email                      string
	EmailTrusted               bool
}

// errInUse is an account another person has linked already.
var errInUse = errf(Conflict, "That account is linked to another person.")

// CreateUser makes an account with its personal space. Everything that must
// stand or fall together (the row, the password, the identity, the invite, the
// space) happens in one transaction, and the repository is made first and
// removed again if the transaction fails, so no half-made account or space is
// ever visible.
func (s *Service) CreateUser(ctx context.Context, p CreateParams) (*store.User, error) {
	username, err := NormalizeUsername(p.Username)
	if err != nil {
		return nil, err
	}
	if p.Name == "" {
		p.Name = username
	}
	name, err := cleanDisplayName(p.Name)
	if err != nil {
		return nil, err
	}
	email, err := s.cleanEmailFor(ctx, p.Email)
	if err != nil {
		return nil, err
	}
	if err := checkRole(p.Role); err != nil {
		return nil, err
	}
	pwHash := p.PasswordHash
	if p.Password != "" {
		if err := CheckPassword(p.Password); err != nil {
			return nil, err
		}
		if pwHash, err = s.hasher.hash(ctx, p.Password); err != nil {
			return nil, err
		}
	}
	// Cheap refusals before a repository is made.
	if p.Setup {
		if open, err := s.SetupOpen(ctx); err != nil {
			return nil, err
		} else if !open {
			return nil, errf(Exists, "An account already exists.")
		}
	}
	if _, err := store.GetUserByUsername(ctx, s.st.Q(), username); err == nil {
		if !p.RetryUsername {
			return nil, errf(Exists, "That username is taken.")
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if barred, err := s.retiredName(ctx, s.st.Q(), username); err != nil {
		return nil, err
	} else if barred && !p.RetryUsername {
		return nil, errf(Exists, "That username is taken.")
	}

	// An account made without saying where (setup, the command line) is in the
	// default tenant, and an administrator of it is one of the server, as every
	// administrator was before tenants. An invite or a provider says where, and
	// whether.
	if p.Setup {
		p.TenantID, p.SiteAdmin = store.DefaultTenantID, true
	}
	if p.TenantID == "" {
		p.TenantID, p.SiteAdmin = store.DefaultTenantID, store.IsAdminRole(p.Role)
	}
	var planID string
	if nt := p.NewTenant; nt != nil {
		if !store.IsAdminRole(p.Role) {
			return nil, errf(Invalid, "The first account of an organisation is its administrator.")
		}
		nt.Made = store.Tenant{ID: s.newID(), Slug: nt.Slug, Name: nt.Name, Origin: store.OriginSelf, Created: s.now().UTC(), Updated: s.now().UTC()}
		p.TenantID, p.SiteAdmin = nt.Made.ID, false
		if nt.PlanSlug != "" {
			if pl, err := store.GetPlanBySlug(ctx, s.st.Q(), nt.PlanSlug); err == nil {
				planID = pl.ID
			} else if !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
		}
	}
	u := store.User{Username: username, Name: name, Email: email, EmailVerified: p.EmailVerified && email != "", Role: p.Role, Created: s.now().UTC(),
		TenantID: p.TenantID, SiteAdmin: store.IsSiteAdminRole(p.Role, p.SiteAdmin)}
	stg, err := s.cfg.Spaces.Stage("My notes", spaces.IdentityOf(u))
	if err != nil {
		return nil, err
	}
	var released *store.Released
	var stamped func()
	for attempt := 0; ; attempt++ {
		u.ID = s.newID()
		cand := u
		released, stamped = nil, nil
		if attempt > 0 && p.RetryUsername {
			cand.Username = suffixed(username, attempt)
		}
		if p.NewTenant != nil {
			p.NewTenant.Made.CreatedBy = cand.ID
		}
		err = s.st.Tx(ctx, func(q store.Q) error {
			switch {
			case p.Setup:
				if err := store.MarkFirstUser(ctx, q, true, s.now()); err != nil {
					return err
				}
			case p.FirstOK:
				if err := store.MarkFirstUser(ctx, q, false, s.now()); err != nil {
					return err
				}
			}
			if p.InviteID != "" {
				if ok, err := store.UseInvite(ctx, q, p.InviteID, cand.ID, s.now()); err != nil {
					return err
				} else if !ok {
					return errf(NotFound, "That invite is not valid.")
				}
			}
			if p.RegistrationID != "" {
				if ok, err := store.DeleteRegistration(ctx, q, p.RegistrationID); err != nil {
					return err
				} else if !ok {
					return errf(NotFound, "That link is not valid, or has expired.")
				}
			}
			// An organisation the account makes: the tenant first (the user row
			// refers to it), within the cap of the person and of the server.
			if nt := p.NewTenant; nt != nil {
				if n, err := store.CountTenantsCreatedBy(ctx, q, cand.ID); err != nil {
					return err
				} else if n >= nt.MaxPerPerson {
					return errOrgLimit
				}
				if err := store.InsertTenant(ctx, q, nt.Made); err != nil {
					return err
				}
				var err error
				if stamped, err = s.settings.Load().StampNewTenant(ctx, q, nt.Made.ID, cand.ID, ""); err != nil {
					return err
				}
				if planID != "" {
					if err := store.SetTenantPlan(ctx, q, nt.Made.ID, planID); err != nil {
						return err
					}
				}
			}
			// The tenant must exist, and take new people: an invite of a disabled
			// tenant is no invite.
			if t, err := store.GetTenant(ctx, q, cand.TenantID); errors.Is(err, store.ErrNotFound) || err == nil && t.Disabled {
				return errf(NotFound, "That invite is not valid.")
			} else if err != nil {
				return err
			}
			if barred, err := s.retiredName(ctx, q, cand.Username); err != nil {
				return err
			} else if barred {
				return errRetiredName
			}
			// The row without its address, then the address by the rules of
			// every address (store.SetPrimaryEmail), in the same transaction.
			// It checks who holds the address in this transaction; another
			// writer that takes it in between meets the unique index, and the
			// call's savepoint keeps this transaction usable for DropTakenEmail.
			row := cand
			row.Email, row.EmailVerified = "", false
			if err := store.InsertUser(ctx, q, row); err != nil {
				return err
			}
			if cand.Email != "" {
				r, err := store.SetPrimaryEmail(ctx, q, cand.ID, cand.Email, cand.EmailVerified, p.VerifiedBy,
					store.PrimaryOpts{TakeUnverified: p.TakeUnverified && cand.EmailVerified, Now: cand.Created})
				switch {
				case errors.Is(err, store.ErrEmailTaken) && p.DropTakenEmail:
					cand.Email, cand.EmailVerified = "", false
				case err != nil:
					return emailErr(err)
				}
				released = r
			}
			if pwHash != "" {
				if err := store.SetPasswordHash(ctx, q, cand.ID, pwHash, s.now()); err != nil {
					return err
				}
			}
			if len(p.Groups) > 0 {
				if err := s.joinInvitedGroups(ctx, q, cand, p.Groups); err != nil {
					return err
				}
			}
			if p.Terms != "" {
				if err := store.SetTermsAccepted(ctx, q, cand.ID, p.Terms); err != nil {
					return err
				}
			}
			if p.Identity != nil {
				if err := store.InsertLink(ctx, q, store.Link{ID: s.newID(), ProviderID: p.Identity.ProviderID, UserID: cand.ID, Subject: p.Identity.Subject,
					Email: p.Identity.Email, EmailTrusted: p.Identity.EmailTrusted, SignIn: true, Label: p.Identity.Label, Created: s.now(), LastUsed: s.now()}); err != nil {
					return err
				}
			}
			if err := store.InsertSpace(ctx, q, cand.TenantID, stg.ID(), stg.Name(), cand.ID, "", s.now()); err != nil {
				return err
			}
			return store.AddMember(ctx, q, stg.ID(), cand.ID, "owner", s.now())
		})
		if err == nil {
			u = cand
			break
		}
		if errors.Is(err, errRetiredName) {
			if p.RetryUsername && attempt < 20 {
				continue
			}
			err = errf(Exists, "That username is taken.")
		}
		if errors.Is(err, store.ErrLinkSubjectTaken) {
			err = errInUse
		}
		if on, ok := store.UniqueOn(err); ok {
			switch {
			case p.NewTenant != nil && store.TenantSlugClash(err) && attempt < 20:
				// Someone took the slug since it was checked: a number is added.
				p.NewTenant.Made.Slug = suffixedSlug(p.NewTenant.Slug, attempt)
				continue
			case strings.Contains(on, "username"):
				if p.RetryUsername && attempt < 20 {
					continue
				}
				err = errf(Exists, "That username is taken.")
			case attempt < 5:
				continue // the generated id was taken: try another
			}
		}
		if errors.Is(err, store.ErrExists) {
			err = errf(Exists, "An account already exists.")
		}
		stg.Abort()
		return nil, err
	}
	stg.Commit(u.ID)
	if stamped != nil {
		stamped()
	}
	if p.NewTenant != nil {
		if err := s.st.TenantStateReload(ctx); err != nil {
			s.log.Warn("could not reload the tenants", "error", err)
		}
	}
	s.markUsersExist()
	if released != nil {
		s.emailReleased(ctx, *released, u)
	}
	if s.onNewUser != nil {
		s.onNewUser(context.WithoutCancel(ctx), []store.Touched{{UserID: u.ID, TenantID: u.TenantID}})
	}
	return &u, nil
}

func suffixed(base string, n int) string {
	suf := "-" + itoa(n+1)
	if len(base)+len(suf) > 32 {
		base = base[:32-len(suf)]
	}
	return base + suf
}

// suffixedSlug is slug with -2, -3, ... added (n counts the tries before).
func suffixedSlug(base string, n int) string {
	suf := "-" + itoa(n+2)
	if len(base)+len(suf) > 32 {
		base = base[:32-len(suf)]
	}
	return base + suf
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}

// SetPassword sets a user's password without asking for the old one and ends
// all their sessions: the command-line tool, for when nobody can sign in.
func (s *Service) SetPassword(ctx context.Context, username, password string) error {
	un, err := NormalizeUsername(username)
	if err != nil {
		return err
	}
	if err := CheckPassword(password); err != nil {
		return err
	}
	u, err := store.GetUserByUsername(ctx, s.st.Q(), un)
	if errors.Is(err, store.ErrNotFound) {
		return errf(NotFound, "No user %q.", un)
	} else if err != nil {
		return err
	}
	hash, err := s.hasher.hash(ctx, password)
	if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q store.Q) error {
		if err := store.SetPasswordHash(ctx, q, u.ID, hash, s.now()); err != nil {
			return err
		}
		if err := store.DeleteEmailTokens(ctx, q, u.ID, store.TokenReset); err != nil {
			return err
		}
		return store.DeleteUserSessions(ctx, q, u.ID, "", store.EndPassword, s.now())
	})
	if err == nil {
		s.evictUser(u.ID, "")
	}
	return err
}

// Users lists the accounts in the scope, oldest first (administrators only; the API checks).
func (s *Service) Users(ctx context.Context, sc store.Scope) ([]store.User, error) {
	return store.ListUsers(ctx, s.st.Q(), sc)
}

// FindUsers lists the accounts whose username or name contains query (ignoring
// case), oldest first, at most limit of them (limit <= 0: no limit). enabledOnly
// leaves the disabled accounts out before the limit counts.
func (s *Service) FindUsers(ctx context.Context, sc store.Scope, query string, enabledOnly bool, limit int) ([]store.User, error) {
	return store.SearchUsers(ctx, s.st.Q(), s.st.Dialect(), sc, query, enabledOnly, limit)
}

// errRetiredName is a username that a purge freed and the site never gives out
// again (users.reuseUsernames is "never").
var errRetiredName = errors.New("that username was used by someone who was purged")

// retiredName says whether a username may not be taken because a purge freed it
// and the setting is "never". Every purge records the name whatever the setting
// was, so switching to "never" later protects the names freed before. The
// setting is read from the settings cache; the table only when it is "never".
func (s *Service) retiredName(ctx context.Context, q store.Q, username string) (bool, error) {
	st := s.settings.Load()
	if st == nil {
		return false, nil
	}
	e, err := st.Get(ctx, "", settings.UsersReuseUsernames)
	if err != nil {
		return false, err
	}
	if v, _ := e.Value.(string); v != "never" {
		return false, nil
	}
	return store.UsernameRetired(ctx, q, username)
}
