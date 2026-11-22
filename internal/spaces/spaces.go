// Package spaces knows which spaces exist, who is a member of them, and opens
// them: lazily, once, and for good until shutdown. In single-user mode it
// fronts the one space the server was started with.
package spaces

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/KazuhaHub/PetalBook/internal/gitstore"
	"github.com/KazuhaHub/PetalBook/internal/page"
	"github.com/KazuhaHub/PetalBook/internal/secret"
	"github.com/KazuhaHub/PetalBook/internal/space"
	"github.com/KazuhaHub/PetalBook/internal/store"
)

// SingleID is the id of the one space of single-user mode.
const SingleID = "default"

// maxCachedUsers bounds the membership cache.
const maxCachedUsers = 10_000

var (
	// ErrNotFound is the answer for a space that does not exist and for one the
	// user is not a member of: the two must look the same.
	ErrNotFound = errors.New("no such space")
	// ErrForbidden: a member who may not do this.
	ErrForbidden = errors.New("not allowed")
	// ErrInvalid wraps a refused name.
	ErrInvalid = errors.New("invalid")
	// ErrLimit: the user owns as many spaces as the server allows.
	ErrLimit  = errors.New("space limit reached")
	errClosed = errors.New("the server is shutting down")
)

// Info is a space as a member sees it.
type Info struct {
	ID       string
	Name     string
	Role     string
	Personal bool
	// Sync is "local", or the state of the remote the space follows (ok,
	// offline, rewritten, error). It is read when the list is asked for, not cached.
	Sync string
}

// Handle is an open space together with the caller's view of it. Release it
// when the request is done: a space in use is never closed.
type Handle struct {
	Space   *space.Space
	Info    Info
	release func()
}

// Release says the caller is done with the space.
func (h *Handle) Release() {
	if h.release != nil {
		h.release()
		h.release = nil
	}
}

// Options configures NewAccounts.
type Options struct {
	Store   *store.Store
	DataDir string // spaces live in DataDir/spaces/<id>, their index and journal in DataDir/cache/<id>
	// Committer is the Git committer of commits ("Name <email>"): the service.
	Committer             string
	CommitIdle, CommitMax time.Duration
	Logger                *slog.Logger
	Now                   func() time.Time
	// MaxSpaces is how many spaces one user may own; 0 means no limit.
	MaxSpaces int
	// IdleAfter is how long an open space may go unused before it is closed
	// (default 15 minutes), and MaxOpen how many may be open at once (default
	// 64); the least recently used go first. Closing flushes the committer;
	// the next request reopens the space.
	IdleAfter time.Duration
	MaxOpen   int
	// CloseWait is how long Close waits for spaces that requests still hold
	// (default 5 seconds) before closing them anyway.
	CloseWait time.Duration
	// OpenHook, if set, runs before a space is opened from disk (tests).
	OpenHook func(id string)

	// Secrets encrypts the credentials of remotes; by default a box keyed from
	// DataDir/secret.key or PETALBOOK_SECRET_KEY.
	Secrets *secret.Box
	// RemotePolicy says which remote addresses are accepted.
	RemotePolicy gitstore.Policy
	// PullInterval is how often a remote is fetched (default DefaultPullInterval
	// in the flag; 0 here means never by itself).
	PullInterval time.Duration
	// NetTimeout, FetchFresh and FailFast tune the remote commands (space.RemoteOptions).
	NetTimeout, FetchFresh, FailFast time.Duration
	// RemoteRunner replaces the git runner of remote spaces (tests).
	RemoteRunner gitstore.Runner
}

// Defaults for closing spaces that are not in use.
const (
	DefaultIdleAfter = 15 * time.Minute
	DefaultMaxOpen   = 64
	// DefaultMaxSpaces is the flag's default; Options.MaxSpaces 0 is unlimited.
	DefaultMaxSpaces = 20
)

// opening is one space that is open, being opened, or being closed.
type opening struct {
	done chan struct{} // closed when the open has finished
	sp   *space.Space
	err  error

	// Guarded by Manager.mu.
	refs     int           // requests (and sweeps) using it right now
	last     time.Time     // when it was last acquired or released
	closing  chan struct{} // set while it is being closed; closed when that is done
	nextPull time.Time     // when the remote is pulled next; zero for a local space
}

// Manager is safe for concurrent use.
type Manager struct {
	opt    Options
	single *space.Space
	log    *slog.Logger

	mu     sync.Mutex
	open   map[string]*opening
	lists  map[string][]Info // membership cache: user id -> spaces, personal first
	closed bool

	syncs    map[string]string // space id -> state of its remote, for Info.Sync (guarded by mu; nil until first needed)
	remoteMu sync.Mutex        // one connect, disconnect or key change at a time
	pullWake chan struct{}     // a space with a remote was just opened: pull it now

	trimMu   sync.Mutex // one trim at a time
	trimming bool       // a background trim is queued or running (guarded by mu)
	createMu sync.Mutex // space creation is one at a time, so the limit cannot be passed by racing
}

// NewAccounts returns the manager of accounts mode.
func NewAccounts(opt Options) (*Manager, error) {
	if opt.Store == nil || opt.DataDir == "" {
		return nil, errors.New("spaces: a store and a data directory are required")
	}
	abs, err := filepath.Abs(opt.DataDir)
	if err != nil {
		return nil, err
	}
	// The index database cannot live at a path with these (see index.Open);
	// better to say so now than to fail the first time a space is opened.
	if strings.ContainsAny(abs, "?#") {
		return nil, fmt.Errorf("the data directory %q: '?' and '#' in the path are not supported", abs)
	}
	opt.DataDir = abs
	if err := os.MkdirAll(filepath.Join(abs, "spaces"), 0o755); err != nil {
		return nil, err
	}
	if opt.Secrets == nil {
		opt.Secrets = secret.NewBox(abs)
	}
	return newManager(opt, nil), nil
}

// NewSingle returns the manager of single-user mode around sp, and makes sure
// the built-in user and space exist in the database (drafts refer to them).
func NewSingle(ctx context.Context, st *store.Store, sp *space.Space, log *slog.Logger) (*Manager, error) {
	if err := store.EnsureLocal(ctx, st.Q(), SingleID, sp.Name(), time.Now()); err != nil {
		return nil, err
	}
	return newManager(Options{Store: st, Logger: log}, sp), nil
}

func newManager(opt Options, single *space.Space) *Manager {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	log := opt.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if opt.IdleAfter <= 0 {
		opt.IdleAfter = DefaultIdleAfter
	}
	if opt.MaxOpen <= 0 {
		opt.MaxOpen = DefaultMaxOpen
	}
	if opt.CloseWait <= 0 {
		opt.CloseWait = 5 * time.Second
	}
	return &Manager{opt: opt, single: single, log: log, open: map[string]*opening{}, lists: map[string][]Info{}, pullWake: make(chan struct{}, 1)}
}

// Single reports single-user mode.
func (m *Manager) Single() bool { return m.single != nil }

// List returns the spaces the user is a member of. After the first call for a
// user it does not touch the database until something about their spaces changes.
func (m *Manager) List(ctx context.Context, userID string) ([]Info, error) {
	l, err := m.list(ctx, userID)
	if err != nil {
		return nil, err
	}
	return m.withSync(ctx, l), nil
}

func (m *Manager) list(ctx context.Context, userID string) ([]Info, error) {
	if m.single != nil {
		if userID != store.LocalUserID {
			return nil, nil
		}
		return []Info{{ID: SingleID, Name: m.single.Name(), Role: "owner", Personal: true, Sync: "local"}}, nil
	}
	m.mu.Lock()
	l, ok := m.lists[userID]
	m.mu.Unlock()
	if ok {
		return l, nil
	}
	rows, err := store.ListUserSpaces(ctx, m.opt.Store.Q(), userID)
	if err != nil {
		return nil, err
	}
	l = make([]Info, len(rows))
	for i, r := range rows {
		l[i] = Info{ID: r.ID, Name: r.Name, Role: r.Role, Personal: r.Personal}
	}
	sort.Slice(l, func(i, j int) bool {
		a, b := l[i], l[j]
		if a.Personal != b.Personal {
			return a.Personal
		}
		if an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name); an != bn {
			return an < bn
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	m.mu.Lock()
	if len(m.lists) >= maxCachedUsers {
		for k := range m.lists { // bounded: drop an arbitrary entry
			delete(m.lists, k)
			break
		}
	}
	m.lists[userID] = l
	m.mu.Unlock()
	return l, nil
}

// Open returns the space if the user is a member of it, and ErrNotFound
// otherwise. This is the one place space access is decided. The caller must
// Release the handle.
func (m *Manager) Open(ctx context.Context, userID, spaceID string) (*Handle, error) {
	l, err := m.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, in := range l {
		if in.ID == spaceID {
			sp, release, err := m.Acquire(ctx, spaceID)
			if err != nil {
				return nil, err
			}
			return &Handle{Space: sp, Info: in, release: release}, nil
		}
	}
	return nil, ErrNotFound
}

// Acquire opens a space by id without asking who is looking: for the draft
// sweeper, which acts for a draft's author. Request handlers use Open. The
// space stays open until release is called, and is opened again transparently
// if it was closed for being idle.
func (m *Manager) Acquire(ctx context.Context, id string) (*space.Space, func(), error) {
	if m.single != nil {
		if id != SingleID {
			return nil, nil, ErrNotFound
		}
		return m.single, func() {}, nil
	}
	if !page.ValidID(id) {
		return nil, nil, ErrNotFound
	}
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, nil, errClosed
		}
		e := m.open[id]
		if e != nil && e.closing != nil {
			// Being closed: wait, so two copies of one space never run at once.
			c := e.closing
			m.mu.Unlock()
			select {
			case <-c:
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		opener := false
		if e == nil {
			e = &opening{done: make(chan struct{})}
			m.open[id] = e
			opener = true
		}
		e.refs++
		e.last = m.opt.Now()
		m.mu.Unlock()
		if opener {
			// Opening can take as long as an index rebuild; it must not be cut
			// short because the request that happened to start it went away.
			e.sp, e.err = m.openSpace(context.WithoutCancel(ctx), id)
			if e.err != nil {
				m.mu.Lock()
				delete(m.open, id)
				m.mu.Unlock()
			}
			if e.err == nil {
				if _, ok := e.sp.Remote(); ok {
					// Someone is about to look at it: bring it up to date now.
					m.mu.Lock()
					e.nextPull = m.opt.Now()
					m.mu.Unlock()
					m.wakePull()
				}
			}
			close(e.done)
			if e.err == nil {
				m.trimSoon()
			}
		}
		select {
		case <-e.done:
		case <-ctx.Done():
			m.unref(e)
			return nil, nil, ctx.Err()
		}
		if e.err != nil {
			return nil, nil, e.err
		}
		var once sync.Once
		return e.sp, func() { once.Do(func() { m.unref(e) }) }, nil
	}
}

func (m *Manager) unref(e *opening) {
	m.mu.Lock()
	e.refs--
	e.last = m.opt.Now()
	m.mu.Unlock()
}

func (m *Manager) paths(id string) (root, state string) {
	return filepath.Join(m.opt.DataDir, "spaces", id), filepath.Join(m.opt.DataDir, "cache", id)
}

func (m *Manager) openSpace(ctx context.Context, id string) (*space.Space, error) {
	if m.opt.OpenHook != nil {
		m.opt.OpenHook(id)
	}
	root, state := m.paths(id)
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		// space.Open would quietly make an empty repository here.
		return nil, fmt.Errorf("the directory of space %s is missing", id)
	}
	owner, name, err := store.SpaceOwner(ctx, m.opt.Store.Q(), id)
	if err != nil {
		return nil, err
	}
	var author string
	if owner != nil {
		author = IdentityOf(*owner).String()
	}
	var rem *space.RemoteOptions
	switch r, err := store.GetRemote(ctx, m.opt.Store.Q(), id); {
	case err == nil && r.Connected():
		rem = m.remoteOptions(id, r)
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	return space.Open(space.Options{
		Root: root, Name: name, StateDir: state, Committer: m.opt.Committer, Author: author,
		CommitIdle: m.opt.CommitIdle, CommitMax: m.opt.CommitMax, Logger: m.log.With("space", id), Remote: rem,
	})
}

// ValidName checks a space name: 1-80 characters once trimmed, no control characters.
func ValidName(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n < 1 || n > 80 || !utf8.ValidString(name) {
		return "", fmt.Errorf("%w: a space name is 1 to 80 characters", ErrInvalid)
	}
	if strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("%w: a space name cannot contain control characters", ErrInvalid)
	}
	return name, nil
}

// Identity is a person as Git records them.
type Identity struct {
	Name, Email string
}

// String is the "Name <email>" form the space package takes.
func (i Identity) String() string { return i.Name + " <" + i.Email + ">" }

// IdentityOf is the Git author for a user: their name and email, with the
// email falling back to <username>@users.petalbook.invalid. Characters Git
// would mangle or that would break the "Name <email>" form are dropped.
func IdentityOf(u store.User) Identity {
	clean := func(s string) string {
		return strings.TrimSpace(strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f || r == '<' || r == '>' || r == '\n' {
				return -1
			}
			return r
		}, s))
	}
	name, email := clean(u.Name), clean(u.Email)
	if name == "" {
		name = clean(u.Username)
	}
	if name == "" {
		name = "unknown"
	}
	if email == "" || strings.ContainsAny(email, " \t") {
		email = u.Username + "@users.petalbook.invalid"
	}
	return Identity{Name: name, Email: email}
}

// Staged is a space whose repository exists on disk but that no one can see
// yet: it has no database row. Commit it in the same step that makes the row,
// or Abort it, so a failure part-way leaves nothing visible.
type Staged struct {
	m    *Manager
	id   string
	name string
	sp   *space.Space
	done bool
}

// ID is the id the space will have.
func (s *Staged) ID() string { return s.id }

// Name is the display name the row must be made with.
func (s *Staged) Name() string { return s.name }

// Stage creates the directory and Git repository of a new space, with author
// as the Git author of its commits. Nothing refers to it until Commit.
func (m *Manager) Stage(name string, author Identity) (*Staged, error) {
	if m.single != nil {
		return nil, errors.New("single-user mode has one space")
	}
	name, err := ValidName(name)
	if err != nil {
		return nil, err
	}
	var id, root, state string
	for i := 0; ; i++ {
		id = page.NewID(m.opt.Now())
		root, state = m.paths(id)
		// Mkdir fails when the directory exists: it claims the id.
		if err := os.Mkdir(root, 0o755); err == nil {
			break
		} else if !os.IsExist(err) || i >= 10 {
			return nil, err
		}
	}
	sp, err := space.Open(space.Options{
		Root: root, Name: name, StateDir: state, Committer: m.opt.Committer, Author: author.String(),
		CommitIdle: m.opt.CommitIdle, CommitMax: m.opt.CommitMax, Logger: m.log.With("space", id),
	})
	if err != nil {
		os.RemoveAll(root)
		os.RemoveAll(state)
		return nil, err
	}
	return &Staged{m: m, id: id, name: name, sp: sp}, nil
}

// Commit makes the space known to the manager, after its database row exists.
func (s *Staged) Commit(ownerID string) {
	if s.done {
		return
	}
	s.done = true
	m := s.m
	e := &opening{done: make(chan struct{}), sp: s.sp, last: m.opt.Now()}
	close(e.done)
	m.mu.Lock()
	closed := m.closed
	if !closed {
		m.open[s.id] = e
	}
	delete(m.lists, ownerID)
	m.mu.Unlock()
	if closed {
		s.sp.Close()
		return
	}
	m.trimSoon()
}

// Abort removes a space that was never committed.
func (s *Staged) Abort() {
	if s.done {
		return
	}
	s.done = true
	root, state := s.m.paths(s.id)
	s.sp.Close()
	os.RemoveAll(root)
	os.RemoveAll(state)
}

// Create makes a space owned by the user, unless they own as many as the
// server allows. The repository is made first and the row second, so a failure
// in either leaves nothing visible.
func (m *Manager) Create(ctx context.Context, userID, name string) (Info, error) {
	if m.single != nil {
		return Info{}, errors.New("single-user mode has one space")
	}
	m.createMu.Lock()
	defer m.createMu.Unlock()
	u, err := store.GetUser(ctx, m.opt.Store.Q(), userID)
	if err != nil {
		return Info{}, err
	}
	if m.opt.MaxSpaces > 0 {
		n, err := store.CountOwnedSpaces(ctx, m.opt.Store.Q(), userID)
		if err != nil {
			return Info{}, err
		}
		if n >= m.opt.MaxSpaces {
			return Info{}, fmt.Errorf("%w: you already own %d spaces, the most this server allows", ErrLimit, n)
		}
	}
	stg, err := m.Stage(name, IdentityOf(*u))
	if err != nil {
		return Info{}, err
	}
	now := m.opt.Now()
	err = m.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.InsertSpace(ctx, q, stg.ID(), stg.Name(), "", now); err != nil {
			return err
		}
		return store.AddMember(ctx, q, stg.ID(), userID, "owner", now)
	})
	if err != nil {
		stg.Abort()
		return Info{}, err
	}
	stg.Commit(userID)
	return Info{ID: stg.ID(), Name: stg.Name(), Role: "owner", Sync: "local"}, nil
}

// Rename changes a space's display name; only an owner may.
func (m *Manager) Rename(ctx context.Context, userID, spaceID, name string) (Info, error) {
	if m.single != nil {
		return Info{}, ErrNotFound
	}
	h, err := m.Open(ctx, userID, spaceID)
	if err != nil {
		return Info{}, err
	}
	defer h.Release()
	if h.Info.Role != "owner" {
		return Info{}, ErrForbidden
	}
	name, err = ValidName(name)
	if err != nil {
		return Info{}, err
	}
	var members []string
	err = m.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.RenameSpace(ctx, q, spaceID, name); err != nil {
			return err
		}
		members, err = store.SpaceMemberIDs(ctx, q, spaceID)
		return err
	})
	if err != nil {
		return Info{}, err
	}
	m.mu.Lock()
	for _, id := range members {
		delete(m.lists, id)
	}
	m.mu.Unlock()
	h.Info.Name = name
	return h.Info, nil
}

// UserChanged tells the manager a user's name or email changed, so spaces they
// own that are open commit under the new identity.
func (m *Manager) UserChanged(ctx context.Context, u store.User) {
	if m.single != nil {
		return
	}
	l, err := m.List(ctx, u.ID)
	if err != nil {
		return
	}
	for _, in := range l {
		if in.Role != "owner" {
			continue
		}
		m.mu.Lock()
		e := m.open[in.ID]
		m.mu.Unlock()
		if e == nil {
			continue
		}
		select {
		case <-e.done:
			if e.sp != nil {
				id := IdentityOf(u)
				e.sp.SetAuthor(id.Name, id.Email)
			}
		default:
		}
	}
}

// Forget drops what is cached about a user's spaces, so the next look reads
// the database. Memberships change only through this package; this is for the
// command-line tool's sake and for tests that change them directly.
func (m *Manager) Forget(userID string) {
	m.mu.Lock()
	delete(m.lists, userID)
	m.mu.Unlock()
}

// Close commits what the open spaces have pending and closes them. Call it
// after the HTTP server has stopped. Single-user mode's space belongs to the caller.
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true // from here on nothing new is acquired
	m.mu.Unlock()
	// Spaces that requests still hold are given a few seconds to be released:
	// closing one under a running request would end it half done. If they are
	// not released in time they are closed anyway (changes made after that are
	// refused with space.ErrClosed, before anything is written).
	deadline := time.Now().Add(m.opt.CloseWait)
	for {
		m.mu.Lock()
		held := 0
		for _, e := range m.open {
			held += e.refs
		}
		m.mu.Unlock()
		if held == 0 {
			break
		}
		if time.Now().After(deadline) {
			m.log.Warn("closing spaces that requests still hold: they did not finish in time", "references", held, "waited", m.opt.CloseWait.String())
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.mu.Lock()
	open := m.open
	m.open = map[string]*opening{}
	m.mu.Unlock()
	var errs []error
	for _, e := range open {
		if e.closing != nil {
			<-e.closing // already on its way out
			continue
		}
		<-e.done
		if e.sp != nil {
			if err := e.sp.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// --- closing spaces that are not in use -------------------------------------

// Run closes idle spaces once a minute until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	if m.single != nil {
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Trim()
		}
	}
}

// OpenCount is how many spaces are open or being opened.
func (m *Manager) OpenCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.open)
}

// trimSoon closes spaces over the cap in the background, so the request that
// opened one does not wait for another to be flushed.
func (m *Manager) trimSoon() {
	m.mu.Lock()
	if m.trimming || len(m.open) <= m.opt.MaxOpen || m.closed {
		m.mu.Unlock()
		return
	}
	m.trimming = true
	m.mu.Unlock()
	go func() {
		m.Trim()
		m.mu.Lock()
		m.trimming = false
		m.mu.Unlock()
	}()
}

// Trim closes the spaces that have been idle for IdleAfter and, while more
// than MaxOpen are open, the least recently used ones. A space with a request
// in flight is never closed. Each is flushed first, and one whose pending
// changes cannot be committed (the repository is in the middle of a merge, say)
// stays open and is tried again at the next trim: nothing is closed with
// uncommitted writes it could not flush.
func (m *Manager) Trim() {
	if m.single != nil {
		return
	}
	m.trimMu.Lock()
	defer m.trimMu.Unlock()
	now := m.opt.Now()
	skip := map[*opening]bool{} // flush failed: not again in this trim
	for {
		id, e := m.pickIdle(now, skip)
		if e == nil {
			return
		}
		if !m.closeIdle(id, e) {
			skip[e] = true
		}
	}
}

// pickIdle chooses the next space to close and pins it (so nobody starts using
// it while it is flushed); nil when there is none.
func (m *Manager) pickIdle(now time.Time, skip map[*opening]bool) (string, *opening) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", nil
	}
	over := len(m.open) - m.opt.MaxOpen
	var bestID string
	var best *opening
	for id, e := range m.open {
		if e.refs > 0 || e.closing != nil || e.sp == nil || skip[e] {
			continue
		}
		idle := now.Sub(e.last) >= m.opt.IdleAfter
		if !idle && over <= 0 {
			continue
		}
		if best == nil || e.last.Before(best.last) {
			bestID, best = id, e
		}
	}
	if best != nil {
		best.refs++
	}
	return bestID, best
}

// closeIdle reports whether it closed the space.
func (m *Manager) closeIdle(id string, e *opening) bool {
	// Flush while pinned; a failure leaves the space open.
	if err := e.sp.Flush(); err != nil {
		m.log.Warn("not closing an idle space: its changes could not be committed yet", "space", id, "error", err)
		m.mu.Lock()
		e.refs-- // not a use: it stays as idle as it was
		m.mu.Unlock()
		return false
	}
	m.mu.Lock()
	e.refs--
	if e.refs > 0 || m.closed {
		m.mu.Unlock() // someone started using it meanwhile
		return true
	}
	e.closing = make(chan struct{})
	m.mu.Unlock()
	if err := e.sp.Close(); err != nil {
		m.log.Warn("closing an idle space", "space", id, "error", err)
	}
	m.mu.Lock()
	if m.open[id] == e {
		delete(m.open, id)
	}
	close(e.closing)
	m.mu.Unlock()
	return true
}
