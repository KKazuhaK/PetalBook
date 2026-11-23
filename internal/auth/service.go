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
	"unicode/utf8"

	"github.com/KazuhaHub/authcore/captcha"
	authsaml "github.com/KazuhaHub/authcore/saml"

	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/secret"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

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
	// loopback or testing) unless OIDC is configured.
	PublicURL      string
	TrustedProxies []netip.Prefix
	// OIDC is the provider of the old --oidc-* flags: read once, to make the
	// provider "sso" of the state database (see importFlags).
	OIDC     *OIDCConfig
	Password PasswordParams // zero: DefaultPasswordParams
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
	Logger          *slog.Logger
	Now             func() time.Time
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
	perMin                     *minuteLimiter
	auditIP                    *minuteLimiter // auditThrottled: one row per address, reason and minute
	failIP, failUser, failPair *failTable
	starts                     *bucketLimiter

	// The security settings in force (see security.go), the audit log, the
	// providers, passkeys and captcha.
	secPtr       atomic.Pointer[secState]
	secMu        sync.Mutex
	audit        *auditLog
	provs        *providerSet
	replay       *authsaml.MemoryReplayCache
	pk           *passkeyState
	capImg       *captcha.ImageGenerator
	capStore     *captcha.MemoryStore
	capTok       atomic.Pointer[tokenChecker]
	capSecretErr atomic.Bool
	flagsIgnored atomic.Bool

	// adminMu serialises changes that must keep one enabled administrator.
	adminMu sync.Mutex
	// credMu serialises changes that must keep one way to sign in.
	credMu sync.Mutex

	cmu      sync.Mutex
	gen      uint64 // bumped by every eviction, so a slow lookup cannot resurrect what was evicted
	sessions map[string]*cachedSession
	users    map[string]*cachedUser
	negative map[string]time.Time

	pending *pendingStore
}

type cachedSession struct {
	userID    string
	created   time.Time
	lastUsed  time.Time
	persisted time.Time // the last_used the database has
	held      bool
	loaded    time.Time
}

type cachedUser struct {
	u      store.User
	loaded time.Time
}

// Principal is who a request is from.
type Principal struct {
	User      store.User
	SessionID string // hash of the session cookie; "" for the built-in user
	// SessionCreated is when the session began (the sign-in).
	SessionCreated time.Time
	// Held: the session may only register a passkey and sign out.
	Held bool
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
		perMin:   newMinuteLimiter(maxLimitKeys, cfg.Now),
		auditIP:  newMinuteLimiter(maxLimitKeys, cfg.Now),
		failIP:   newFailTable(maxLimitKeys, cfg.Now),
		failUser: newFailTable(maxLimitKeys, cfg.Now),
		failPair: newFailTable(maxLimitKeys, cfg.Now),
		starts:   newBucketLimiter(10_000, 30, 20, cfg.Now),
		audit:    newAuditLog(), provs: newProviderSet(), replay: authsaml.NewMemoryReplayCache(authsaml.DefaultReplayCacheCapacity),
		sessions: map[string]*cachedSession{}, users: map[string]*cachedUser{}, negative: map[string]time.Time{},
		pending: newPendingStore(cfg.Now),
	}
	s.initCaptcha()
	if cfg.PublicURL != "" {
		u, err := url.Parse(cfg.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errors.New("auth: the public URL must look like https://notes.example.com")
		}
		s.secure = u.Scheme == "https"
	}
	s.sec(ctx) // reads the settings, and builds what depends on them
	if err := s.reloadProviders(ctx); err != nil {
		return nil, err
	}
	if err := s.importFlags(ctx); err != nil {
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

func (s *Service) markUsersExist() {
	s.setupMu.Lock()
	s.usersExist.Store(true)
	s.setupToken = ""
	s.setupMu.Unlock()
}

// LegacyOIDCName is the display name of the provider that answers the pre-2.8
// endpoints, or "" when there is none.
func (s *Service) LegacyOIDCName() string {
	if rt := s.provs.legacy(); rt != nil && rt.row.Enabled {
		return rt.row.Name
	}
	return ""
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
	return &Error{Kind: RateLimited, Msg: "too many attempts; wait a little and try again", RetryAfter: d}
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
		return "", errf(Invalid, "a username is 2 to 32 characters")
	}
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
			c = b[i]
		}
		alnum := c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || (c != '.' && c != '_' && c != '-')) {
			return "", errf(Invalid, "a username uses a-z, 0-9, '.', '_' and '-', and starts with a letter or digit")
		}
	}
	return string(b), nil
}

func cleanDisplayName(in string) (string, error) {
	n := strings.TrimSpace(in)
	if c := utf8.RuneCountInString(n); c < 1 || c > 100 || !utf8.ValidString(n) {
		return "", errf(Invalid, "a name is 1 to 100 characters")
	}
	if strings.IndexFunc(n, func(r rune) bool { return r < 0x20 || r == 0x7f || r == 0x85 || r == 0x2028 || r == 0x2029 }) >= 0 {
		return "", errf(Invalid, "a name cannot contain control characters")
	}
	return n, nil
}

func cleanEmail(in string) (string, error) {
	e := strings.TrimSpace(in)
	if e == "" {
		return "", nil
	}
	at := strings.Index(e, "@")
	if len(e) > 254 || at < 1 || at == len(e)-1 || strings.ContainsFunc(e, func(r rune) bool { return r <= 0x20 || r == 0x7f || r == '<' || r == '>' }) || !utf8.ValidString(e) {
		return "", errf(Invalid, "that does not look like an email address")
	}
	return e, nil
}

func checkRole(r string) error {
	if r != "admin" && r != "member" {
		return errf(Invalid, "role is admin or member")
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
	InviteID string // the invite this account uses up
	// Setup makes this the first account: it fails with ErrExists if any
	// account exists, even when two setups run at once. FirstOK is the
	// command-line form: it also records that an account exists, but never refuses.
	Setup, FirstOK bool
	// EmailVerified: the address came from a provider that vouched for it.
	EmailVerified bool
	// RetryUsername lets the name get a numeric suffix when it is taken (accounts made from an
	// identity provider's claims).
	RetryUsername bool
}

// IdentityParams is an external identity to attach.
type IdentityParams struct {
	Issuer, Subject, Label string
}

// errInUse is an identity another account already has.
var errInUse = errf(Conflict, "that identity belongs to another account")

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
	email, err := cleanEmail(p.Email)
	if err != nil {
		return nil, err
	}
	if err := checkRole(p.Role); err != nil {
		return nil, err
	}
	var pwHash string
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
			return nil, errf(Exists, "an account already exists")
		}
	}
	if _, err := store.GetUserByUsername(ctx, s.st.Q(), username); err == nil {
		if !p.RetryUsername {
			return nil, errf(Exists, "that username is taken")
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	u := store.User{Username: username, Name: name, Email: email, EmailVerified: p.EmailVerified && email != "", Role: p.Role, Created: s.now().UTC()}
	stg, err := s.cfg.Spaces.Stage("My notes", spaces.IdentityOf(u))
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		u.ID = s.newID()
		cand := u
		if attempt > 0 && p.RetryUsername {
			cand.Username = suffixed(username, attempt)
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
					return errf(NotFound, "that invite is not valid")
				}
			}
			if err := store.InsertUser(ctx, q, cand); err != nil {
				return err
			}
			if pwHash != "" {
				if err := store.SetPasswordHash(ctx, q, cand.ID, pwHash, s.now()); err != nil {
					return err
				}
			}
			if p.Identity != nil {
				if err := store.InsertIdentity(ctx, q, store.Identity{ID: s.newID(), UserID: cand.ID, Issuer: p.Identity.Issuer,
					Subject: p.Identity.Subject, Label: p.Identity.Label, Created: s.now()}); err != nil {
					return err
				}
			}
			if err := store.InsertSpace(ctx, q, stg.ID(), stg.Name(), cand.ID, "", s.now()); err != nil {
				return err
			}
			return store.AddMember(ctx, q, stg.ID(), cand.ID, "owner", s.now())
		})
		if err == nil {
			u = cand
			break
		}
		if on, ok := store.UniqueOn(err); ok {
			switch {
			case strings.Contains(on, "username"):
				if p.RetryUsername && attempt < 20 {
					continue
				}
				err = errf(Exists, "that username is taken")
			case strings.Contains(on, "identities") || strings.Contains(on, "subject"):
				err = errInUse
			case attempt < 5:
				continue // the generated id was taken: try another
			}
		}
		if errors.Is(err, store.ErrExists) {
			err = errf(Exists, "an account already exists")
		}
		stg.Abort()
		return nil, err
	}
	stg.Commit(u.ID)
	s.markUsersExist()
	return &u, nil
}

func suffixed(base string, n int) string {
	suf := "-" + itoa(n+1)
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
		return errf(NotFound, "no user %q", un)
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
		return store.DeleteUserSessions(ctx, q, u.ID, "")
	})
	if err == nil {
		s.evictUser(u.ID, "")
	}
	return err
}

// Users lists every account, oldest first (administrators only; the API checks).
func (s *Service) Users(ctx context.Context) ([]store.User, error) {
	return store.ListUsers(ctx, s.st.Q())
}

// FindUsers lists the accounts whose username or name contains query (ignoring
// case), oldest first, at most limit of them (limit <= 0: no limit). enabledOnly
// leaves the disabled accounts out before the limit counts.
func (s *Service) FindUsers(ctx context.Context, query string, enabledOnly bool, limit int) ([]store.User, error) {
	return store.SearchUsers(ctx, s.st.Q(), s.st.Dialect(), query, enabledOnly, limit)
}
