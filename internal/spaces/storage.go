package spaces

// The server's storage policy (milestone 3.1, docs/api.md "Server storage"):
// the site's settings, the requirement of a remote with its grace period, the
// limit on local spaces, and the record of when a space was used. The
// settings are in the state database (site_settings, key "storage") and are
// kept in memory: a request reads them with one atomic load, and a change
// applies to the next request. Eviction and restoring are in evict.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/github"
	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

const storageKey = "storage"

// Limits of the settings.
const (
	MinLocalMaxBytes   = 1 << 20
	MinClonesBytes     = 1 << 30
	DefaultEvictDays   = 14
	DefaultIdleHours   = 24
	DefaultFreeBytes   = 10 << 30
	DefaultAssetsBytes = 5 << 30
	MinAssetsBytes     = 64 << 20
	DefaultGraceDays   = 30
	maxGraceDays       = 365
	maxEvictDays       = 365
	maxIdleHours       = 720
	touchEvery         = time.Hour
	evictRoundEvery    = 10 * time.Minute
	maxRestoresAtOnce  = 2
)

// StorageSettings is the site's storage policy.
type StorageSettings struct {
	Remotes struct {
		Require   *string   `json:"require"` // nil, "any" or "github"
		Scope     string    `json:"scope"`   // "new" or "all"
		GraceDays int       `json:"graceDays"`
		Since     time.Time `json:"since"` // when the requirement took its present form
	} `json:"remotes"`
	Hosts struct {
		Allowed []string `json:"allowed"`
	} `json:"hosts"`
	LocalSpaces struct {
		MaxBytes *int64 `json:"maxBytes"`
	} `json:"localSpaces"`
	Cache struct {
		Partial        bool   `json:"partial"`
		EvictAfterDays *int   `json:"evictAfterDays"`
		MinIdleHours   int    `json:"minIdleHours"`
		ClonesBytes    *int64 `json:"clonesBytes"`
		MinFreeBytes   *int64 `json:"minFreeBytes"`
		AssetsBytes    int64  `json:"assetsBytes"`
	} `json:"cache"`
	Assets struct {
		NewSpaces bool `json:"newSpaces"`
	} `json:"assets"`
}

// DefaultStorage is the policy of a server whose administrator has not
// changed anything. partial says whether Git can make partial clones.
func DefaultStorage(partialOK bool) StorageSettings {
	var s StorageSettings
	s.Remotes.Scope = "new"
	s.Remotes.GraceDays = DefaultGraceDays
	s.Hosts.Allowed = append([]string{}, gitstore.DefaultHosts...)
	s.Cache.Partial = partialOK
	d := DefaultEvictDays
	s.Cache.EvictAfterDays = &d
	s.Cache.MinIdleHours = DefaultIdleHours
	f := int64(DefaultFreeBytes)
	s.Cache.MinFreeBytes = &f
	s.Cache.AssetsBytes = DefaultAssetsBytes
	return s
}

// RemoteNotice is what a space shows of the requirement it does not meet.
type RemoteNotice struct {
	Required string
	Deadline time.Time
	Overdue  bool
}

// RequiredError is the refusal of a change the storage policy does not allow
// (409 remote_required).
type RequiredError struct {
	Require  string
	Deadline time.Time // zero for a new space or a removal
	Msg      string
}

func (e *RequiredError) Error() string { return e.Msg }

// SpaceFullError is the refusal of an upload or import over the limit on
// local spaces (507 space_full).
type SpaceFullError struct{ Limit, Size int64 }

func (e *SpaceFullError) Error() string {
	return fmt.Sprintf("this space would be over the limit of %d bytes the administrator has set for spaces that are not on a remote repository", e.Limit)
}

// ErrConflictFlag: a setting the command line decided.
var ErrConflictFlag = errors.New("this setting was given on the command line, which wins")

// ErrGitTooOld: partial clones need Git 2.44.
var ErrGitTooOld = errors.New("this server's Git is older than 2.44 and cannot make partial clones")

// storageState is the part of the manager the storage policy uses.
type storageState struct {
	cur  settingsBox
	once sync.Once

	factsMu sync.Mutex
	facts   map[string]store.PolicyFacts

	touchMu sync.Mutex
	touched map[string]time.Time // space id -> when its use was last written to the database
	used    map[string]time.Time // space id -> its latest use

	gitVersion string
	gitPartial bool
}

type settingsBox struct {
	mu sync.RWMutex
	s  StorageSettings
	ok bool
}

func (b *settingsBox) get() StorageSettings {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.s
}

func (b *settingsBox) set(s StorageSettings) {
	b.mu.Lock()
	b.s, b.ok = s, true
	b.mu.Unlock()
}

// initStorage reads the Git version and the settings; it is called once.
func (m *Manager) initStorage(ctx context.Context) {
	m.st.once.Do(func() {
		m.st.gitVersion, m.st.gitPartial = gitstore.GitVersion(ctx, m.opt.RemoteRunner)
		if m.opt.GitSupportsPartial != nil {
			m.st.gitPartial = *m.opt.GitSupportsPartial
		}
		s := m.defaultStorage()
		if v, ok, err := store.GetSiteSetting(ctx, m.opt.Store.Q(), storageKey); err != nil {
			m.log.Warn("could not read the storage settings; the defaults apply", "error", err)
		} else if ok {
			if err := json.Unmarshal([]byte(v), &s); err != nil {
				m.log.Warn("the stored storage settings are not usable; the defaults apply", "error", err)
				s = m.defaultStorage()
			}
			if !m.st.gitPartial {
				s.Cache.Partial = false
			}
		}
		m.st.cur.set(s)
	})
}

// defaultStorage is DefaultStorage with the hosts the server was started with
// (the default of --remote-host) as the starting point of the setting.
func (m *Manager) defaultStorage() StorageSettings {
	s := DefaultStorage(m.st.gitPartial)
	if len(m.opt.RemotePolicy.Hosts) > 0 {
		s.Hosts.Allowed = append([]string{}, m.opt.RemotePolicy.Hosts...)
	}
	return s
}

// Storage is the site's storage policy as it is now.
func (m *Manager) Storage() StorageSettings {
	if m.single != nil {
		return DefaultStorage(false)
	}
	m.initStorage(context.Background())
	s := m.st.cur.get()
	if m.opt.HostsFromFlag {
		s.Hosts.Allowed = append([]string{}, m.opt.RemotePolicy.Hosts...)
	}
	return s
}

// GitInfo is the Git the server runs and whether it can make partial clones.
func (m *Manager) GitInfo() (version string, partial bool) {
	m.initStorage(context.Background())
	return m.st.gitVersion, m.st.gitPartial
}

// HostsFromFlag reports that --remote-host decided the allowed hosts.
func (m *Manager) HostsFromFlag() bool { return m.opt.HostsFromFlag }

// remotePolicy is the requirement of a remote, for the queries.
func (m *Manager) remotePolicy() store.RemotePolicy {
	s := m.Storage()
	p := store.RemotePolicy{Now: m.opt.Now()}
	if s.Remotes.Require != nil {
		p.Require = *s.Remotes.Require
	}
	p.All = s.Remotes.Scope == "all"
	p.Since = s.Remotes.Since
	p.Grace = time.Duration(s.Remotes.GraceDays) * 24 * time.Hour
	return p
}

// hostPolicy is the address policy in force: the hosts of the settings, unless
// the flag was given.
func (m *Manager) hostPolicy() gitstore.Policy {
	p := m.opt.RemotePolicy
	if !m.opt.HostsFromFlag && m.single == nil {
		p.Hosts = m.Storage().Hosts.Allowed
	}
	return p
}

// StoragePatch is a change to the settings; a nil section stays as it is.
type StoragePatch func(*StorageSettings) error

// Affected is how many spaces the policy asks something of.
type Affected struct{ InGrace, Overdue, Exempt int }

// SetStorage applies a change to the settings, checks the result, stores it
// and makes it the policy of the next request. It returns the policy as it is
// then and how many spaces it now asks something of.
func (m *Manager) SetStorage(ctx context.Context, patch StoragePatch) (StorageSettings, Affected, error) {
	if m.single != nil {
		return StorageSettings{}, Affected{}, ErrNotFound
	}
	m.initStorage(ctx)
	m.storageMu.Lock()
	defer m.storageMu.Unlock()
	cur := m.st.cur.get()
	next := cur
	next.Hosts.Allowed = append([]string{}, cur.Hosts.Allowed...)
	if err := patch(&next); err != nil {
		return StorageSettings{}, Affected{}, err
	}
	if err := m.validateStorage(&next); err != nil {
		return StorageSettings{}, Affected{}, err
	}
	// "since" is when the requirement took its present form: a tightening
	// starts the grace period afresh, a relaxation clears it.
	reqOld, reqNew := deref(cur.Remotes.Require), deref(next.Remotes.Require)
	switch {
	case reqNew == "":
		next.Remotes.Since = time.Time{}
	case reqOld != reqNew || cur.Remotes.Scope != next.Remotes.Scope:
		next.Remotes.Since = m.opt.Now().UTC().Truncate(time.Millisecond)
	}
	b, err := json.Marshal(next)
	if err != nil {
		return StorageSettings{}, Affected{}, err
	}
	if err := store.SetSiteSetting(ctx, m.opt.Store.Q(), storageKey, string(b), m.opt.Now()); err != nil {
		return StorageSettings{}, Affected{}, err
	}
	m.st.cur.set(next)
	m.dropFacts("")
	aff, err := m.Affected(ctx)
	return m.Storage(), aff, err
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (m *Manager) validateStorage(s *StorageSettings) error {
	bad := func(f string, a ...any) error { return fmt.Errorf("%w: "+f, append([]any{ErrInvalid}, a...)...) }
	if r := s.Remotes.Require; r != nil && *r != "any" && *r != "github" {
		return bad(`remotes.require is null, "any" or "github"`)
	}
	if s.Remotes.Scope != "new" && s.Remotes.Scope != "all" {
		return bad(`remotes.scope is "new" or "all"`)
	}
	if s.Remotes.GraceDays < 0 || s.Remotes.GraceDays > maxGraceDays {
		return bad("remotes.graceDays is 0 to %d", maxGraceDays)
	}
	if deref(s.Remotes.Require) == "github" && m.opt.GitHub == nil {
		return &github.Error{Status: http.StatusConflict, Code: github.CodeNotConfigured, Msg: "the server has no GitHub App; set it up before requiring GitHub"}
	}
	if m.single == nil && !m.opt.HostsFromFlag {
		if len(s.Hosts.Allowed) == 0 {
			return bad("hosts.allowed needs at least one host (or *)")
		}
		for i, h := range s.Hosts.Allowed {
			h = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(h, ".")))
			if h != "*" && (h == "" || strings.ContainsAny(h, " /:@\\") || len(h) > 253) {
				return bad("hosts.allowed: %q is not a host name", s.Hosts.Allowed[i])
			}
			s.Hosts.Allowed[i] = h
		}
	}
	if v := s.LocalSpaces.MaxBytes; v != nil && *v < MinLocalMaxBytes {
		return bad("localSpaces.maxBytes is null or at least 1 MiB")
	}
	c := &s.Cache
	if c.EvictAfterDays != nil && (*c.EvictAfterDays < 1 || *c.EvictAfterDays > maxEvictDays) {
		return bad("cache.evictAfterDays is 1 to %d, or null", maxEvictDays)
	}
	if c.MinIdleHours < 1 || c.MinIdleHours > maxIdleHours {
		return bad("cache.minIdleHours is 1 to %d", maxIdleHours)
	}
	if c.ClonesBytes != nil && *c.ClonesBytes < MinClonesBytes {
		return bad("cache.clonesBytes is null or at least 1 GiB")
	}
	if c.MinFreeBytes != nil && *c.MinFreeBytes < 0 {
		return bad("cache.minFreeBytes is null or at least 0")
	}
	if c.AssetsBytes < MinAssetsBytes {
		return bad("cache.assetsBytes is at least 64 MiB")
	}
	if c.Partial && !m.st.gitPartial {
		return ErrGitTooOld
	}
	return nil
}

// Affected counts the spaces the requirement asks something of now: one query.
func (m *Manager) Affected(ctx context.Context) (Affected, error) {
	g, o, e, err := store.PolicyCounts(ctx, m.opt.Store.Q(), m.remotePolicy())
	return Affected{g, o, e}, err
}

// --- the requirement of a remote --------------------------------------------------

// policyFacts reads what the requirement looks at in a space; cached, and
// dropped when a remote or an exemption changes.
func (m *Manager) policyFacts(ctx context.Context, id string) (store.PolicyFacts, error) {
	m.st.factsMu.Lock()
	f, ok := m.st.facts[id]
	m.st.factsMu.Unlock()
	if ok {
		return f, nil
	}
	f, err := store.GetPolicyFacts(ctx, m.opt.Store.Q(), id)
	if err != nil {
		return f, err
	}
	m.st.factsMu.Lock()
	if m.st.facts == nil || len(m.st.facts) > 50_000 {
		m.st.facts = map[string]store.PolicyFacts{}
	}
	m.st.facts[id] = f
	m.st.factsMu.Unlock()
	return f, nil
}

// dropFacts forgets what is cached about a space's requirement ("" for all).
func (m *Manager) dropFacts(id string) {
	m.st.factsMu.Lock()
	if id == "" {
		m.st.facts = nil
	} else {
		delete(m.st.facts, id)
	}
	m.st.factsMu.Unlock()
}

// PolicyChanged tells the manager that an exemption of a user changed.
func (m *Manager) PolicyChanged() { m.dropFacts("") }

// acceptable reports whether a credential type meets the requirement.
func acceptable(require, cred string) bool {
	switch require {
	case "":
		return true
	case "github":
		return cred == CredGitHub
	}
	return cred != ""
}

// Notice says what requirement a space does not meet, with its deadline; nil
// when it meets it, is exempt, or is not asked.
func (m *Manager) Notice(ctx context.Context, id string) (*RemoteNotice, error) {
	if m.single != nil {
		return nil, nil
	}
	p := m.remotePolicy()
	if !p.Active() {
		return nil, nil
	}
	f, err := m.policyFacts(ctx, id)
	if err != nil {
		return nil, err
	}
	if f.Exempt || f.OwnerExempt || acceptable(p.Require, f.CredType) {
		return nil, nil
	}
	dl, ok := p.Deadline(f.Created)
	if !ok {
		return nil, nil
	}
	return &RemoteNotice{Required: p.Require, Deadline: dl, Overdue: !p.Now.Before(dl)}, nil
}

// CheckWritable refuses a change to the files of a space that is past its
// deadline (409 remote_required): it is read only until it follows a remote of
// the required kind. Drafts do not come here; they are not files of the space.
func (m *Manager) CheckWritable(id string) error {
	if m.single != nil {
		return nil
	}
	m.initStorage(context.Background())
	if m.st.cur.get().Remotes.Require == nil {
		return nil // the usual case: one load
	}
	n, err := m.Notice(context.Background(), id)
	if err != nil || n == nil || !n.Overdue {
		return nil // a failed lookup does not stop someone writing
	}
	return &RequiredError{Require: n.Required, Deadline: n.Deadline,
		Msg: "This space is past its deadline for following a remote repository (" + describeRequire(n.Required) + "), so it is read only until it does."}
}

func describeRequire(r string) string {
	if r == "github" {
		return "on GitHub"
	}
	return "on an allowed host"
}

// CheckNewSpace refuses a new space that does not have what the policy
// requires, before anything is made. cred is the credential type the request
// brings ("" for none); exempt users are not asked.
func (m *Manager) CheckNewSpace(ctx context.Context, userID, cred string) error {
	if m.single != nil {
		return nil
	}
	p := m.remotePolicy()
	if !p.Active() || acceptable(p.Require, cred) {
		return nil
	}
	if u, err := store.GetUser(ctx, m.opt.Store.Q(), userID); err == nil && u.RemoteExempt {
		return nil
	}
	msg := "This server requires a space to follow a remote repository"
	if p.Require == "github" {
		msg += " on GitHub: make it with a github repository"
	} else {
		msg += " on an allowed host: give a remote or a github repository"
	}
	return &RequiredError{Require: p.Require, Msg: msg + "."}
}

// CheckRemoval refuses removing the remote of a space the policy asks for one.
func (m *Manager) CheckRemoval(ctx context.Context, id string) error {
	if m.single != nil {
		return nil
	}
	p := m.remotePolicy()
	if !p.Active() {
		return nil
	}
	f, err := m.policyFacts(ctx, id)
	if err != nil {
		return err
	}
	if f.Exempt || f.OwnerExempt {
		return nil
	}
	if _, ok := p.Deadline(f.Created); !ok {
		return nil // not covered by a requirement of new spaces only
	}
	return &RequiredError{Require: p.Require, Msg: "This server requires the space to follow a remote repository; it cannot be removed."}
}

// CheckCredential refuses a credential the requirement does not accept.
func (m *Manager) CheckCredential(ctx context.Context, id, cred string) error {
	if m.single != nil {
		return nil
	}
	p := m.remotePolicy()
	if !p.Active() || acceptable(p.Require, cred) {
		return nil
	}
	f, err := m.policyFacts(ctx, id)
	if err != nil {
		return err
	}
	if f.Exempt || f.OwnerExempt {
		return nil
	}
	if _, ok := p.Deadline(f.Created); !ok {
		return nil // not covered by a requirement of new spaces only
	}
	return &RequiredError{Require: p.Require, Msg: "This server requires a remote repository on GitHub; use a github credential."}
}

// --- a limit on local spaces -------------------------------------------------------

// CheckRoom refuses an upload or import of extra bytes into a local space that
// would take it over the limit (507 space_full). The size is the stored
// measurement, up to hours old; within 10 % of the limit the space is measured
// again first. A space that follows a remote is not limited: its copy is a cache.
func (m *Manager) CheckRoom(ctx context.Context, h *Handle, extra int64) error {
	if m.single != nil {
		return nil
	}
	s := m.Storage()
	limit := s.LocalSpaces.MaxBytes
	if limit == nil || h.Info.Sync != "local" {
		return nil
	}
	row, err := store.GetAdminSpace(ctx, m.opt.Store.Q(), h.Info.ID)
	if err != nil {
		return err
	}
	var size int64
	if row.SizeBytes != nil {
		size = *row.SizeBytes
	}
	if row.SizeBytes == nil || size+extra > *limit || float64(size+extra) > 0.9*float64(*limit) {
		if n, _, err := m.Resize(ctx, h.Info.ID); err == nil {
			size = n
		}
	}
	if size+extra > *limit {
		return &SpaceFullError{Limit: *limit, Size: size}
	}
	return nil
}

// --- when a space was used ---------------------------------------------------------

// noteUse records that a request opened the space. The database is written at
// most once an hour per space; the time in memory is what the eviction reads
// first.
func (m *Manager) noteUse(id string) {
	if m.single != nil {
		return
	}
	now := m.opt.Now()
	m.st.touchMu.Lock()
	if m.st.touched == nil {
		m.st.touched, m.st.used = map[string]time.Time{}, map[string]time.Time{}
	}
	if len(m.st.used) > 100_000 {
		m.st.used = map[string]time.Time{}
	}
	m.st.used[id] = now
	last, seen := m.st.touched[id]
	if seen && now.Sub(last) < touchEvery {
		m.st.touchMu.Unlock()
		return
	}
	if len(m.st.touched) > 100_000 {
		m.st.touched = map[string]time.Time{}
	}
	m.st.touched[id] = now
	m.st.touchMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := store.TouchSpaceUsed(ctx, m.opt.Store.Q(), id, now); err != nil {
			m.log.Warn("could not record the use of a space", "space", id, "error", err)
		}
	}()
}

// lastUsed is the time of the space's last use: the later of the database's
// and the one in memory.
func (m *Manager) lastUsed(db time.Time, id string) time.Time {
	m.st.touchMu.Lock()
	t := m.st.used[id]
	m.st.touchMu.Unlock()
	if t.After(db) {
		return t
	}
	return db
}

// RemotePolicy is the requirement of a remote as the queries use it, as of now.
func (m *Manager) RemotePolicy() store.RemotePolicy { return m.remotePolicy() }

// LastUsed is the time of the space's last use: the later of the stored one and
// the one in memory.
func (m *Manager) LastUsed(db time.Time, id string) time.Time { return m.lastUsed(db, id) }

// NoticeOfRow is Notice for a row the administration's list has read.
func (m *Manager) NoticeOfRow(r store.AdminSpace) *RemoteNotice {
	p := m.remotePolicy()
	if !p.Active() || r.Storage.Exempt || r.OwnerExempt || acceptable(p.Require, r.CredType) {
		return nil
	}
	dl, ok := p.Deadline(r.Created)
	if !ok {
		return nil
	}
	return &RemoteNotice{Required: p.Require, Deadline: dl, Overdue: !p.Now.Before(dl)}
}
