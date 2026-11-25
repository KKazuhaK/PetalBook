package auth

import (
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/KazuhaHub/authcore/passkey"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

const (
	maxPasskeys        = 20
	recentLoginWindow  = 10 * time.Minute
	passkeyFakeKeyName = "passkey_fake_key"
)

var (
	errClone        = errors.New("the passkey's counter went backwards")
	errPasskeyLimit = errors.New("too many passkeys")
)

type ctxKey int

const (
	ctxPasskeyName ctxKey = iota + 1
	ctxClientIP
)

// passkeyState is the passkey service and what goes with it.
type passkeyState struct {
	svc     *passkey.Service
	rpID    string
	fakeKey []byte
	users   *ceremonyUsers
	names   *ceremonyUsers
}

// passkeyHostOK says whether the public URL can be a relying party: https, or
// http on a loopback name (for trying things out).
func (s *Service) passkeyHostOK() bool {
	u, err := url.Parse(s.cfg.PublicURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return u.Scheme == "http"
	}
	ip := net.ParseIP(host)
	return u.Scheme == "http" && ip != nil && ip.IsLoopback()
}

func (s *Service) initPasskeys(ctx context.Context) error {
	if !s.passkeyHostOK() {
		return nil
	}
	u, _ := url.Parse(s.cfg.PublicURL)
	rp := strings.ToLower(u.Hostname())
	key, err := s.passkeyFakeKey(ctx)
	if err != nil {
		return err
	}
	svc, err := passkey.New(passkey.Config{
		RPID: rp, RPDisplayName: "PetalBook", RPOrigins: []string{u.Scheme + "://" + u.Host},
		AuthenticatorSelection: protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementPreferred, UserVerification: protocol.VerificationRequired},
		UserVerification:       protocol.VerificationRequired,
		Sessions:               passkey.NewMemoryStore(passkey.DefaultSessionCapacity, passkey.DefaultSessionTTL),
		Credentials:            &dbCreds{s: s}, Now: s.now,
	})
	if err != nil {
		return err
	}
	s.pk = &passkeyState{svc: svc, rpID: rp, fakeKey: key, users: newCeremonyUsers(s.now), names: newCeremonyUsers(s.now)}
	if n, err := store.PasskeysOfOtherHost(ctx, s.st.Q(), rp); err == nil && n > 0 {
		s.log.Warn("passkeys were made for another host and cannot be used here: they belong to the host of the old --public-url", "passkeys", n, "host", rp)
	}
	return nil
}

// passkeyFakeKey is the server key the made-up credential ids of unknown
// users come from; it is made once and kept sealed.
func (s *Service) passkeyFakeKey(ctx context.Context) ([]byte, error) {
	if raw, _, err := store.GetSetting(ctx, s.st.Q(), passkeyFakeKeyName); err == nil {
		if b, err := s.open("passkey-fake", raw); err == nil {
			if k, err := base64.RawURLEncoding.DecodeString(b); err == nil && len(k) == 32 {
				return k, nil
			}
		}
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	enc, err := s.seal("passkey-fake", base64.RawURLEncoding.EncodeToString(k))
	if err != nil {
		return nil, err
	}
	if _, err := store.PutSetting(ctx, s.st.Q(), passkeyFakeKeyName, enc, s.now()); err != nil {
		return nil, err
	}
	return k, nil
}

func (s *Service) passkeysOn(st *secState) bool { return st.Passkeys.Enabled && s.pk != nil }

// PasskeysAvailable answers the "passkey" of GET /api/auth/providers.
func (s *Service) PasskeysAvailable(ctx context.Context) bool { return s.passkeysOn(s.sec(ctx)) }

func errNoPasskeys() *Error { return errf(NotFound, "Passkeys are not available here.") }

// --- the credential store -----------------------------------------------------------

type dbCreds struct{ s *Service }

func credID(id []byte) string { return base64.RawURLEncoding.EncodeToString(id) }

func storedOf(row *store.PasskeyRow) (passkey.StoredCredential, error) {
	var c webauthn.Credential
	if err := json.Unmarshal([]byte(row.Credential), &c); err != nil {
		return passkey.StoredCredential{}, err
	}
	return passkey.StoredCredential{UserHandle: []byte(row.UserID), Credential: c}, nil
}

func (d *dbCreds) FindByID(ctx context.Context, id []byte) (passkey.StoredCredential, error) {
	row, err := store.PasskeyByCredential(ctx, d.s.st.Q(), credID(id))
	if errors.Is(err, store.ErrNotFound) {
		return passkey.StoredCredential{}, passkey.ErrCredentialNotFound
	}
	if err != nil {
		return passkey.StoredCredential{}, err
	}
	return storedOf(row)
}

func (d *dbCreds) FindByUserHandle(ctx context.Context, handle []byte) ([]passkey.StoredCredential, error) {
	rows, err := store.ListPasskeys(ctx, d.s.st.Q(), string(handle))
	if err != nil {
		return nil, err
	}
	out := make([]passkey.StoredCredential, 0, len(rows))
	for i := range rows {
		sc, err := storedOf(&rows[i])
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, nil
}

func (d *dbCreds) Save(ctx context.Context, sc passkey.StoredCredential) error {
	name, _ := ctx.Value(ctxPasskeyName).(string)
	if name == "" {
		name = "Passkey"
	}
	raw, err := json.Marshal(sc.Credential)
	if err != nil {
		return err
	}
	userID := string(sc.UserHandle)
	return d.s.st.Tx(ctx, func(q store.Q) error {
		if n, err := store.CountPasskeys(ctx, q, userID); err != nil {
			return err
		} else if n >= maxPasskeys {
			return errPasskeyLimit
		}
		return store.InsertPasskey(ctx, q, store.PasskeyRow{ID: d.s.newID(), UserID: userID, CredentialID: credID(sc.Credential.ID), Credential: string(raw),
			Name: name, RPID: d.s.pk.rpID, Synced: sc.Credential.Flags.BackupState, Created: d.s.now().UTC()})
	})
}

func (d *dbCreds) UpdateSignCount(ctx context.Context, id []byte, cred webauthn.Credential, usedAt time.Time) error {
	if cred.Authenticator.CloneWarning {
		// The counter went backwards, a sign that the key was copied: refused,
		// written down, and the stored counter is not moved.
		ip, _ := ctx.Value(ctxClientIP).(string)
		ev := AuditEvent{Kind: "passkey_clone", IP: ip}
		if row, err := store.PasskeyByCredential(ctx, d.s.st.Q(), credID(id)); err == nil {
			ev.User, ev.Detail = row.UserID, map[string]any{"passkey": row.ID}
		}
		d.s.Audit(ev)
		return errClone
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	return store.PasskeyUse(ctx, d.s.st.Q(), credID(id), string(raw), cred.Flags.BackupState, usedAt)
}

// --- ceremonies of one user ----------------------------------------------------------

// ceremonyUsers remembers whose ceremony a sign-in begun with a username is,
// in memory, bounded and short-lived like the ceremonies themselves.
type ceremonyUsers struct {
	mu    sync.Mutex
	items map[string]*cuEntry
	order *list.List
	now   func() time.Time
}

type cuEntry struct {
	id      string
	handle  []byte
	expires time.Time
	elem    *list.Element
}

func newCeremonyUsers(now func() time.Time) *ceremonyUsers {
	return &ceremonyUsers{items: map[string]*cuEntry{}, order: list.New(), now: now}
}

func (c *ceremonyUsers) put(id string, handle []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for f := c.order.Front(); f != nil; f = c.order.Front() {
		e := f.Value.(*cuEntry)
		if now.Before(e.expires) && len(c.items) < passkey.DefaultSessionCapacity {
			break
		}
		c.order.Remove(f)
		delete(c.items, e.id)
	}
	e := &cuEntry{id: id, handle: handle, expires: now.Add(passkey.DefaultSessionTTL)}
	e.elem = c.order.PushBack(e)
	c.items[id] = e
}

func (c *ceremonyUsers) take(id string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.items[id]
	if e == nil {
		return nil, false
	}
	c.order.Remove(e.elem)
	delete(c.items, id)
	return e.handle, c.now().Before(e.expires)
}

// --- signing in with a passkey ---------------------------------------------------------

func errPasskeyFailed() *Error {
	return coded(401, "passkey_failed", "The passkey did not check out; try again.")
}

// PasskeyBegin starts a sign-in. With a username the answer lists that user's
// passkeys; a name nobody has, or one with no passkey, gets one made-up
// credential id (the same each time) so the answer looks like that of a user
// with one passkey.
func (s *Service) PasskeyBegin(ctx context.Context, username, ip string) (string, *protocol.CredentialAssertion, error) {
	st := s.sec(ctx)
	if !s.passkeysOn(st) {
		return "", nil, errNoPasskeys()
	}
	if err := s.allow(ctx, ip); err != nil {
		return "", nil, err
	}
	if len(username) > 128 {
		return "", nil, errf(Invalid, "The username is too long.")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		as, id, err := s.pk.svc.BeginDiscoverableLogin(ctx)
		if err != nil {
			return "", nil, err
		}
		return id, as, nil
	}
	var u *store.User
	var rows []store.PasskeyRow
	if un, err := NormalizeUsername(username); err == nil {
		if row, err := store.GetUserByUsername(ctx, s.st.Q(), un); err == nil {
			u = row
		} else if !errors.Is(err, store.ErrNotFound) {
			return "", nil, err
		}
	}
	if u != nil {
		var err error
		if rows, err = store.ListPasskeys(ctx, s.st.Q(), u.ID); err != nil {
			return "", nil, err
		}
	}
	if len(rows) > 0 {
		as, id, err := s.pk.svc.BeginLogin(ctx, []byte(u.ID))
		if err != nil {
			return "", nil, err
		}
		for i := range as.Response.AllowedCredentials {
			as.Response.AllowedCredentials[i].Transport = nil // the same shape as the made-up one
		}
		s.pk.users.put(id, []byte(u.ID))
		return id, as, nil
	}
	as, id, err := s.pk.svc.BeginDiscoverableLogin(ctx)
	if err != nil {
		return "", nil, err
	}
	mac := hmac.New(sha256.New, s.pk.fakeKey)
	mac.Write([]byte("passkey-fake\x00" + strings.ToLower(username)))
	as.Response.AllowedCredentials = []protocol.CredentialDescriptor{{Type: protocol.PublicKeyCredentialType, CredentialID: mac.Sum(nil)}}
	return id, as, nil
}

// PasskeyFinish ends a sign-in: r is the request whose body is the credential.
func (s *Service) PasskeyFinish(ctx context.Context, ceremony, ip string, r *http.Request) (*store.User, string, error) {
	st := s.sec(ctx)
	if !s.passkeysOn(st) {
		return nil, "", errNoPasskeys()
	}
	if err := s.allow(ctx, ip); err != nil {
		return nil, "", err
	}
	fail := func(reason string, u *store.User) error {
		ev := AuditEvent{Kind: "login_failed", IP: ip, Detail: map[string]any{"method": "passkey", "reason": reason}}
		if u != nil {
			ev.User, ev.Username = u.ID, u.Username
		}
		s.Audit(ev)
		return errPasskeyFailed()
	}
	ctx = context.WithValue(ctx, ctxClientIP, ip)
	var res *passkey.LoginResult
	var err error
	if handle, ok := s.pk.users.take(ceremony); ok {
		res, err = s.pk.svc.FinishLogin(ctx, handle, ceremony, r)
	} else {
		res, err = s.pk.svc.FinishDiscoverableLogin(ctx, ceremony, r)
	}
	if err != nil {
		if errors.Is(err, errClone) {
			return nil, "", fail("passkey_failed", nil)
		}
		// A ceremony that was never begun (or is spent) is not written down:
		// anyone can send one, and the log is not for them to fill.
		if errors.Is(err, passkey.ErrSessionNotFound) {
			return nil, "", errPasskeyFailed()
		}
		var db error
		if !errors.Is(err, passkey.ErrSessionNotFound) && !errors.Is(err, passkey.ErrCredentialNotFound) && !isWebAuthnError(err) {
			db = err
		}
		if db != nil {
			return nil, "", db
		}
		return nil, "", fail("passkey_failed", nil)
	}
	u, err := store.GetUser(ctx, s.st.Q(), store.AllTenants(), string(res.UserHandle)) // a sign-in: the person decides the tenant
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, "", fail("passkey_failed", nil)
		}
		return nil, "", err
	}
	if u.Disabled || s.st.TenantDisabled(u.TenantID) {
		return nil, "", fail("disabled", u)
	}
	if s.ssoOnlyInEffect(st) && u.Role != "admin" {
		s.Audit(AuditEvent{Kind: "login_failed", User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"method": "passkey", "reason": "local_login_refused"}})
		return nil, "", errLocalRefused()
	}
	tok, err := s.issueSession(ctx, u.ID, false, "passkey")
	if err != nil {
		return nil, "", err
	}
	s.Audit(AuditEvent{Kind: "login", User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"method": "passkey"}})
	return u, tok, nil
}

func isWebAuthnError(err error) bool {
	var pe *protocol.Error
	return errors.As(err, &pe)
}

// --- registering ---------------------------------------------------------------------------

// PasskeyList lists a user's passkeys.
type PasskeyView struct {
	ID       string
	Name     string
	Created  time.Time
	LastUsed time.Time
	Synced   bool
}

func viewPasskeys(rows []store.PasskeyRow) []PasskeyView {
	out := make([]PasskeyView, len(rows))
	for i, r := range rows {
		out[i] = PasskeyView{ID: r.ID, Name: r.Name, Created: r.Created, LastUsed: r.LastUsed, Synced: r.Synced}
	}
	return out
}

// Passkeys lists the passkeys of a user.
func (s *Service) Passkeys(ctx context.Context, userID string) ([]PasskeyView, error) {
	rows, err := store.ListPasskeys(ctx, s.st.Q(), userID)
	if err != nil {
		return nil, err
	}
	return viewPasskeys(rows), nil
}

func cleanPasskeyName(in string) (string, error) {
	n := strings.TrimSpace(in)
	if n == "" {
		return "Passkey", nil
	}
	return cleanTextErr(n, "the name", 64)
}

func cleanTextErr(v, what string, max int) (string, error) {
	if c := len([]rune(v)); c < 1 || c > max || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", errf(Invalid, "%s is 1 to %d characters.", what, max)
	}
	return v, nil
}

// PasskeyRegisterBegin starts registering a passkey for the signed-in user.
func (s *Service) PasskeyRegisterBegin(ctx context.Context, p *Principal, name string) (string, *protocol.CredentialCreation, error) {
	st := s.sec(ctx)
	if !s.passkeysOn(st) {
		return "", nil, errNoPasskeys()
	}
	n, err := cleanPasskeyName(name)
	if err != nil {
		return "", nil, err
	}
	if s.now().Sub(p.SessionCreated) > recentLoginWindow {
		return "", nil, coded(403, "recent_login_required", "Sign in again to add a passkey: it needs a sign-in from the last 10 minutes.")
	}
	count, err := store.CountPasskeys(ctx, s.st.Q(), p.User.ID)
	if err != nil {
		return "", nil, err
	}
	if count >= maxPasskeys {
		return "", nil, coded(409, "too_many_passkeys", "An account has at most %d passkeys.", maxPasskeys)
	}
	cc, id, err := s.pk.svc.BeginRegistration(ctx, []byte(p.User.ID), p.User.Username, p.User.Name)
	if err != nil {
		return "", nil, err
	}
	s.pk.names.put(id, []byte(n))
	return id, cc, nil
}

// PasskeyRegisterFinish ends it; r is the request whose body is the credential.
func (s *Service) PasskeyRegisterFinish(ctx context.Context, p *Principal, ceremony, ip string, r *http.Request) ([]PasskeyView, error) {
	st := s.sec(ctx)
	if !s.passkeysOn(st) {
		return nil, errNoPasskeys()
	}
	nameBytes, _ := s.pk.names.take(ceremony)
	ctx = context.WithValue(ctx, ctxPasskeyName, string(nameBytes))
	cred, err := s.pk.svc.FinishRegistration(ctx, []byte(p.User.ID), ceremony, r)
	switch {
	case err == nil:
	case errors.Is(err, errPasskeyLimit):
		return nil, coded(409, "too_many_passkeys", "An account has at most %d passkeys.", maxPasskeys)
	case errors.Is(err, passkey.ErrSessionNotFound) || isWebAuthnError(err):
		return nil, coded(400, "passkey_failed", "The passkey could not be registered; try again.")
	case store.IsUnique(err):
		return nil, coded(409, "exists", "That passkey is registered already.")
	default:
		return nil, err
	}
	_ = cred
	// The first passkey of someone whose session was held lifts the hold.
	if p.Held {
		if err := store.ReleaseSession(ctx, s.st.Q(), p.SessionID); err != nil {
			return nil, err
		}
		s.releaseHold(p.SessionID)
	}
	rows, err := store.ListPasskeys(ctx, s.st.Q(), p.User.ID)
	if err != nil {
		return nil, err
	}
	var id string
	if len(rows) > 0 {
		id = rows[len(rows)-1].ID
	}
	s.Audit(AuditEvent{Kind: "passkey_added", Actor: p.User.ID, User: p.User.ID, Username: p.User.Username, IP: ip, Detail: map[string]any{"passkey": id, "by": "user"}})
	return viewPasskeys(rows), nil
}

// RenamePasskey renames one of the user's passkeys.
func (s *Service) RenamePasskey(ctx context.Context, userID, id, name string) (*PasskeyView, error) {
	n, err := cleanTextErr(strings.TrimSpace(name), "the name", 64)
	if err != nil {
		return nil, err
	}
	if err := store.RenamePasskey(ctx, s.st.Q(), id, userID, n); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errf(NotFound, "No such passkey.")
		}
		return nil, err
	}
	rows, err := store.ListPasskeys(ctx, s.st.Q(), userID)
	if err != nil {
		return nil, err
	}
	for _, v := range viewPasskeys(rows) {
		if v.ID == id {
			return &v, nil
		}
	}
	return nil, errf(NotFound, "No such passkey.")
}

// DeletePasskey removes one of the user's passkeys, unless that would leave no
// way to sign in. An administrator deleting (by "admin") may leave the person
// with none; someone who must have a passkey is held at the next sign-in.
func (s *Service) DeletePasskey(ctx context.Context, actor *Principal, userID, id, ip string) error {
	by := "user"
	if actor.User.ID != userID {
		by = "admin"
		// A person of another tenant is no person at all.
		u, err := store.GetUser(ctx, s.st.Q(), store.ScopeOf(actor.User), userID)
		if errors.Is(err, store.ErrNotFound) {
			return errf(NotFound, "No such user.")
		} else if err != nil {
			return err
		}
		// The rule of UpdateUser: a site administrator is changed by site administrators only.
		if u.SiteAdmin && !(actor.User.SiteAdmin && actor.User.Role == "admin") {
			return errf(Forbidden, "Only a site administrator may change a site administrator.")
		}
	}
	s.credMu.Lock()
	defer s.credMu.Unlock()
	if by == "user" {
		rows, err := store.ListPasskeys(ctx, s.st.Q(), userID)
		if err != nil {
			return err
		}
		if len(rows) == 1 && rows[0].ID == id {
			if ok, err := s.hasOtherWay(ctx, userID); err != nil {
				return err
			} else if !ok {
				return errf(Conflict, "This is the only way you can sign in; set a password first.")
			}
		}
	}
	if err := store.DeletePasskey(ctx, s.st.Q(), id, userID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errf(NotFound, "No such passkey.")
		}
		return err
	}
	u, _ := store.GetUser(ctx, s.st.Q(), store.AllTenants(), userID)
	ev := AuditEvent{Kind: "passkey_removed", Actor: actor.User.ID, User: userID, IP: ip, Detail: map[string]any{"passkey": id, "by": by}}
	if u != nil {
		ev.Username = u.Username
	}
	s.Audit(ev)
	return nil
}

// hasOtherWay says whether the user could sign in without a passkey, with a
// password or an identity.
func (s *Service) hasOtherWay(ctx context.Context, userID string) (bool, error) {
	if _, err := store.GetPasswordHash(ctx, s.st.Q(), userID); err == nil {
		return true, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	ids, err := store.ListIdentities(ctx, s.st.Q(), userID)
	return len(ids) > 0, err
}
