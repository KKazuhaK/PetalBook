package spaces

// Eviction and restoring of spaces that follow a remote (milestone 3.1,
// docs/api.md "Eviction"). An evicted space has no clone on the server:
// DATA/spaces/<id> is gone, its index (DATA/cache/<id>), its records and its
// settings stay. This file removes data, so the rules it keeps are written
// before the code:
//
//   - Only a space whose every commit and every file is on its remote is
//     evicted, and that is checked against the remote just before: the remote
//     is fetched and the space's HEAD must be contained in it. Anything that
//     exists only here keeps the space: a commit not on the remote, a file or
//     journal entry not committed, a draft, a rescued commit, a remote that is
//     not "ok" (unreachable, rewritten, an error), a clone someone changed.
//   - Nothing evicts a space that a request, a draft sweep or a pull holds: it
//     is held first, so that nothing can open it, and then looked at.
//   - A crash at any point leaves the full space or an evicted one that
//     restores. The order is: check, mark "evicting" in the database, move the
//     directory aside (one rename), mark "evicted", delete what was moved.
//     At the next start recoverEvictions finishes or undoes what it finds.
//   - A local space (no remote) is never evicted and never partially cloned.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

var (
	// ErrNotEvictable: a local or pinned space, or one already evicted (409 conflict).
	ErrNotEvictable = errors.New("the space cannot be evicted")
)

// RestoringError: the space is being restored and the request's wait is over.
type RestoringError struct{ Phase string }

func (e *RestoringError) Error() string {
	return "this space was not used for a while and is being restored from its remote repository (" + e.Phase + ")"
}

// RestoreFailed: the space could not be restored now (503 unavailable). Code is
// the remote's errorCode where there is one.
type RestoreFailed struct {
	Msg, Code string
	Err       error
	// RetryAfter is set while the space is not tried again: a restore failed
	// a moment ago (see restoreBackoff).
	RetryAfter time.Duration
}

func (e *RestoreFailed) Error() string { return e.Msg }
func (e *RestoreFailed) Unwrap() error { return e.Err }

func busyf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBusy, fmt.Sprintf(format, a...))
}

// BusyMessage is the reason in a busy error, "" for any other.
func BusyMessage(err error) string {
	if !errors.Is(err, ErrBusy) {
		return ""
	}
	why, _ := strings.CutPrefix(err.Error(), ErrBusy.Error()+": ")
	if why == err.Error() {
		return ""
	}
	return why
}

type restoreOp struct {
	done  chan struct{}
	phase atomic.Value // string
	err   error
	// indexClosed: the restore is past its stamped step and closes, or has
	// closed, the index-only handle of the space; no new one is given (under
	// ev.mu).
	indexClosed bool
	// cancel stops the restore (a deletion of the space; under ev.mu).
	cancel context.CancelFunc
}

func (o *restoreOp) setPhase(p string) { o.phase.Store(p) }
func (o *restoreOp) getPhase() string {
	if p, _ := o.phase.Load().(string); p != "" {
		return p
	}
	return "waiting"
}

// restoreFail is the last failed restore of a space: when, how many in a row,
// and what people are told meanwhile.
type restoreFail struct {
	at  time.Time
	n   int
	err *RestoreFailed
}

// restoreBackoffMax is the longest a space whose restores keep failing waits
// before the next try.
const restoreBackoffMax = 2 * time.Minute

// restoreBackoff is how long after its n-th failure in a row a space is not
// tried again: FailFast (3 seconds by default), doubled with each failure, at
// most restoreBackoffMax.
func (m *Manager) restoreBackoff(n int) time.Duration {
	d := m.opt.FailFast
	if d <= 0 {
		d = 3 * time.Second
	}
	for i := 1; i < n && d < restoreBackoffMax; i++ {
		d *= 2
	}
	return min(d, restoreBackoffMax)
}

type idxEntry struct {
	// ready is closed when the opener is done: sp is open, or err says why not.
	// The entry is in the map while it opens, holding the opener's reference,
	// so a restore waits for an open under way as for a request.
	ready chan struct{}
	sp    *space.Space
	err   error
	refs  int
	last  time.Time
	// stale: closeIndexOnly gave up waiting for its holders. It is out of the
	// map, so nothing new is given it, and its last release closes it.
	stale bool
}

type evictState struct {
	mu       sync.Mutex
	loaded   bool
	evicted  map[string]bool // spaces whose clone is gone, or being removed
	restores map[string]*restoreOp
	waiting  int // restores that wait for a turn
	sem      chan struct{}
	idx      map[string]*idxEntry    // index-only handles of evicted spaces
	failed   map[string]*restoreFail // spaces whose last restore failed, not tried again for a while
	roundMu  sync.Mutex              // one round of eviction at a time
	filling  map[string]bool         // pinned spaces being made full
	// gone are the spaces being deleted (recycled): no restore starts for them,
	// and one that runs puts nothing at their live place.
	gone map[string]bool
}

func (m *Manager) evictDir() string     { return filepath.Join(m.opt.DataDir, "evicting") }
func (m *Manager) restoringDir() string { return filepath.Join(m.opt.DataDir, "restoring") }

func (m *Manager) evictHook(step string) error {
	if m.opt.EvictHook != nil {
		return m.opt.EvictHook(step)
	}
	return nil
}

// --- what is evicted ---------------------------------------------------------------

// loadEvicted reads which spaces have no clone. It changes nothing: what a
// crash cut short is finished by the server at its start (recoverEvictions).
func (m *Manager) loadEvicted(ctx context.Context) error {
	m.ev.mu.Lock()
	if m.ev.loaded {
		m.ev.mu.Unlock()
		return nil
	}
	m.ev.mu.Unlock()
	set, err := store.SpacesInCacheState(ctx, m.opt.Store.Q(), store.CacheEvicted)
	if err != nil {
		return err
	}
	m.ev.mu.Lock()
	defer m.ev.mu.Unlock()
	if !m.ev.loaded {
		m.ev.evicted = map[string]bool{}
		for id := range set {
			m.ev.evicted[id] = true
		}
		m.ev.restores = map[string]*restoreOp{}
		m.ev.idx = map[string]*idxEntry{}
		m.ev.failed = map[string]*restoreFail{}
		m.ev.sem = make(chan struct{}, maxRestoresAtOnce)
		m.ev.loaded = true
	}
	return nil
}

// recoverEvictions finishes or undoes an eviction that a crash stopped, and
// removes what such a crash and a failed restore left in the staging folders.
// It runs only at the start of a server (Options.Serve), which holds the lock
// of the data directory: no other process can have a restore in progress.
// A space marked "evicting" whose directory is still there was not touched
// (everything about it was checked before the mark): it is a normal space
// again. One whose directory is gone was moved aside: it is evicted, and what
// was moved is deleted. Nothing is removed that a space needs.
func (m *Manager) recoverEvictions(ctx context.Context) error {
	pending, err := store.SpacesInCacheState(ctx, m.opt.Store.Q(), store.CacheEvicting)
	if err != nil {
		return err
	}
	for id := range pending {
		root, _ := m.paths(id)
		if _, err := os.Stat(root); err == nil {
			if err := store.SetSpaceCache(ctx, m.opt.Store.Q(), id, ""); err != nil {
				return err
			}
			m.log.Warn("an eviction was cut short before the space was touched; it stays as it was", "space", id)
			continue
		}
		if err := store.SetSpaceCache(ctx, m.opt.Store.Q(), id, store.CacheEvicted); err != nil {
			return err
		}
		m.log.Warn("an eviction was cut short after the clone was set aside; it is finished", "space", id)
	}
	// A space marked evicted whose directory is there anyway (a restore moved it
	// into place and the mark was not updated): it is whole, and says so.
	evicted, err := store.SpacesInCacheState(ctx, m.opt.Store.Q(), store.CacheEvicted)
	if err != nil {
		return err
	}
	for id := range evicted {
		root, _ := m.paths(id)
		if fi, err := os.Stat(root); err == nil && fi.IsDir() {
			state := store.CacheFull
			if gitstore.New(gitstore.Options{Dir: root}).IsPartial(ctx) {
				state = store.CachePartial
			}
			if err := store.SetSpaceCache(ctx, m.opt.Store.Q(), id, state); err != nil {
				return err
			}
			m.log.Warn("a space was marked evicted but its clone is in place; it is whole again", "space", id, "cache", state)
		}
	}
	// A space that follows a remote and has no clone, whatever its record says
	// (the record overwritten, the directory removed by hand): it is evicted,
	// and is restored when it is needed, rather than refused.
	remotes, err := store.RemoteStates(ctx, m.opt.Store.Q())
	if err != nil {
		return err
	}
	for id := range remotes {
		if evicted[id] != "" || !page.ValidID(id) {
			continue
		}
		root, _ := m.paths(id)
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := store.SetSpaceCache(ctx, m.opt.Store.Q(), id, store.CacheEvicted); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue // a remote's row without its space
			}
			return err
		}
		m.log.Warn("a space that follows a remote has no clone; it is restored when it is needed", "space", id)
	}
	// What was set aside is removed; a half restored clone is removed.
	for _, dir := range []string{m.evictDir(), m.restoringDir()} {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				m.log.Warn("could not remove what an earlier run left", "path", filepath.Join(dir, e.Name()), "error", err)
			}
		}
	}
	return nil
}

// isEvicted reports whether the space has no clone on disk now.
func (m *Manager) isEvicted(id string) bool {
	m.ev.mu.Lock()
	ok := m.ev.evicted[id]
	m.ev.mu.Unlock()
	if !ok {
		return false
	}
	root, _ := m.paths(id)
	if _, err := os.Stat(root); err == nil {
		// Restored (or never gone): the set was behind.
		m.ev.mu.Lock()
		delete(m.ev.evicted, id)
		m.ev.mu.Unlock()
		return false
	}
	return true
}

// CacheState is what the space's copy on the server is: local, full, partial,
// evicted or restoring.
func (m *Manager) CacheState(stored string, hasRemote bool, id string) string {
	if !hasRemote {
		return "local"
	}
	m.ev.mu.Lock()
	op := m.ev.restores[id]
	m.ev.mu.Unlock()
	if op != nil {
		return "restoring"
	}
	switch stored {
	case store.CacheEvicted, store.CacheEvicting:
		return "evicted"
	case store.CachePartial:
		return "partial"
	}
	return "full"
}

// RestoreCounts: restores that run and restores that wait for a turn.
func (m *Manager) RestoreCounts() (restoring, waiting int) {
	m.ev.mu.Lock()
	defer m.ev.mu.Unlock()
	return len(m.ev.restores) - m.ev.waiting, m.ev.waiting
}

// --- the index of an evicted space ---------------------------------------------------

// acquireIndex is acquire for a request that only needs what the index
// answers: an evicted space is opened by its index alone, without restoring
// it. For any other space it is acquire.
func (m *Manager) acquireIndex(ctx context.Context, id string) (*space.Space, func(), error) {
	if m.single != nil || !page.ValidID(id) {
		return m.Acquire(ctx, id)
	}
	if err := m.loadDisabled(ctx); err != nil {
		return nil, nil, err
	}
	if err := m.loadEvicted(ctx); err != nil {
		return nil, nil, err
	}
	if !m.isEvicted(id) {
		return m.acquire(ctx, id, false)
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, nil, errClosed
	}
	if m.disabled[id] {
		m.mu.Unlock()
		return nil, nil, ErrDisabled
	}
	m.mu.Unlock()
	m.ev.mu.Lock()
	if op := m.ev.restores[id]; op != nil && op.indexClosed {
		// The restore closed the index-only handle, or is about to: a new one
		// would stay beside the full space (an old head, a clean note under it).
		m.ev.mu.Unlock()
		return nil, nil, &RestoringError{Phase: op.getPhase()}
	}
	e := m.ev.idx[id]
	opener := e == nil
	if opener {
		e = &idxEntry{ready: make(chan struct{})}
		m.ev.idx[id] = e
	}
	e.refs++
	e.last = m.opt.Now()
	m.ev.mu.Unlock()
	release := m.idxRelease(id, e)
	if opener {
		sp, err := m.openIndexOnly(ctx, id)
		m.ev.mu.Lock()
		e.sp, e.err = sp, err
		if err != nil && m.ev.idx[id] == e {
			delete(m.ev.idx, id)
		}
		m.ev.mu.Unlock()
		close(e.ready)
	} else {
		select {
		case <-e.ready:
		case <-ctx.Done():
			release()
			return nil, nil, ctx.Err()
		}
	}
	if err := e.err; err != nil {
		release()
		// An index that cannot answer as it is (missing, of another version)
		// is never opened index-only: the space is restored, which builds it
		// again from the files. And a space restored meanwhile is opened in full.
		if errors.Is(err, space.ErrIndexUnusable) || !m.isEvicted(id) {
			return m.acquire(ctx, id, false)
		}
		return nil, nil, err
	}
	return e.sp, release, nil
}

func (m *Manager) openIndexOnly(ctx context.Context, id string) (*space.Space, error) {
	root, state := m.paths(id)
	_, name, err := store.SpaceOwner(ctx, m.opt.Store.Q(), id)
	if err != nil {
		return nil, err
	}
	return space.OpenIndexOnly(space.Options{Root: root, StateDir: state, Name: name, Logger: m.log.With("space", id)})
}

func (m *Manager) idxRelease(id string, e *idxEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			m.ev.mu.Lock()
			e.refs--
			e.last = m.opt.Now()
			closing := e.stale && e.refs <= 0 && e.sp != nil
			m.ev.mu.Unlock()
			if closing {
				e.sp.Close()
			}
		})
	}
}

// indexCloseWait is how long closeIndexOnly waits for the requests that use
// an index-only handle.
var indexCloseWait = 5 * time.Second

// closeIndexOnly closes the index-only handle of a space, waiting for the
// requests that use it (they are short).
func (m *Manager) closeIndexOnly(id string) {
	deadline := time.Now().Add(indexCloseWait)
	for time.Now().Before(deadline) {
		m.ev.mu.Lock()
		e := m.ev.idx[id]
		if e == nil {
			m.ev.mu.Unlock()
			return
		}
		if e.refs <= 0 {
			delete(m.ev.idx, id)
			m.ev.mu.Unlock()
			if e.sp != nil {
				e.sp.Close()
			}
			return
		}
		m.ev.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	// Still held: it leaves the map (the space is about to be opened in full)
	// and its last holder closes it.
	m.ev.mu.Lock()
	e := m.ev.idx[id]
	if e == nil {
		m.ev.mu.Unlock()
		return
	}
	delete(m.ev.idx, id)
	refs := e.refs
	e.stale = refs > 0
	m.ev.mu.Unlock()
	if refs <= 0 {
		if e.sp != nil {
			e.sp.Close()
		}
		return
	}
	m.log.Warn("an index-only handle is still in use after a restore; its last request closes it", "space", id, "references", refs)
}

// trimIndexOnly closes index-only handles nobody has used for IdleAfter.
func (m *Manager) trimIndexOnly() {
	now := m.opt.Now()
	var closing []*idxEntry
	m.ev.mu.Lock()
	for id, e := range m.ev.idx {
		if e.refs <= 0 && e.sp != nil && now.Sub(e.last) >= m.opt.IdleAfter {
			delete(m.ev.idx, id)
			closing = append(closing, e)
		}
	}
	m.ev.mu.Unlock()
	for _, e := range closing {
		e.sp.Close()
	}
}

// --- restoring -------------------------------------------------------------------------

// ensureRestored makes the space's files exist: it starts a restore (or joins
// the one that runs) and waits for it, up to Options.RestoreWait; then
// *RestoringError.
func (m *Manager) ensureRestored(ctx context.Context, id string) error {
	m.ev.mu.Lock()
	if m.ev.gone[id] {
		m.ev.mu.Unlock()
		return ErrNotFound
	}
	op := m.ev.restores[id]
	if f := m.ev.failed[id]; op == nil && f != nil {
		// A restore failed a moment ago: it is not tried again yet.
		if left := m.restoreBackoff(f.n) - time.Since(f.at); left > 0 {
			m.ev.mu.Unlock()
			e := *f.err
			e.RetryAfter = left
			return &e
		}
	}
	if op == nil {
		op = &restoreOp{done: make(chan struct{})}
		op.setPhase("waiting")
		m.ev.restores[id] = op
		m.ev.waiting++
		if !m.goBg(func() { m.runRestore(id, op) }) {
			// Closed meanwhile: the waiters are told, nothing is restored.
			delete(m.ev.restores, id)
			m.ev.waiting--
			m.ev.mu.Unlock()
			return errClosed
		}
	}
	m.ev.mu.Unlock()
	wait := m.opt.RestoreWait
	if wait <= 0 {
		wait = 10 * time.Second
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-op.done:
		return op.err
	case <-t.C:
		return &RestoringError{Phase: op.getPhase()}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) runRestore(id string, op *restoreOp) {
	// Taking a turn: at most two restores run at a time.
	m.ev.sem <- struct{}{}
	m.ev.mu.Lock()
	m.ev.waiting--
	m.ev.mu.Unlock()
	deadline := m.opt.RestoreDeadline
	if deadline <= 0 {
		deadline = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(m.closedCtx(), deadline)
	m.ev.mu.Lock()
	op.cancel = cancel
	m.ev.mu.Unlock()
	op.err = m.restore(ctx, id, op)
	if op.err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		op.err = &RestoreFailed{Msg: "The remote repository did not answer in time.", Err: op.err}
	}
	cancel()
	<-m.ev.sem
	m.ev.mu.Lock()
	delete(m.ev.restores, id)
	if op.err == nil {
		delete(m.ev.failed, id)
	} else {
		// Remembered, so that requests meanwhile answer at once instead of
		// starting a restore that is likely to fail again.
		f := m.ev.failed[id]
		if f == nil {
			f = &restoreFail{}
			m.ev.failed[id] = f
		}
		f.at, f.n = time.Now(), f.n+1
		var rf *RestoreFailed
		if errors.As(op.err, &rf) {
			f.err = &RestoreFailed{Msg: rf.Msg, Code: rf.Code, Err: rf.Err}
		} else {
			f.err = &RestoreFailed{Msg: "The space could not be restored.", Err: op.err}
		}
	}
	m.ev.mu.Unlock()
	close(op.done)
	if op.err != nil {
		m.log.Warn("could not restore a space", "space", id, "error", op.err)
	}
}

// restore makes the clone of an evicted space again, in a directory of its
// own, and moves it into place when it is complete: a failure leaves no half
// clone. Nothing about the space's records changes but its cache state.
func (m *Manager) restore(ctx context.Context, id string, op *restoreOp) error {
	if !m.isEvicted(id) {
		return nil
	}
	if m.restoreStopped(id) {
		return errSpaceGone
	}
	if err := m.evictHook("restore:start"); err != nil {
		return err
	}
	rem, err := store.GetRemote(ctx, m.opt.Store.Q(), id)
	if err != nil || !rem.Connected() {
		return &RestoreFailed{Msg: "The space has no remote repository to restore it from.", Err: err}
	}
	root, state := m.paths(id)
	if err := os.MkdirAll(m.restoringDir(), 0o755); err != nil {
		return err
	}
	var rnd [6]byte
	rand.Read(rnd[:])
	tmp := filepath.Join(m.restoringDir(), id+"."+hex.EncodeToString(rnd[:]))
	if err := os.Mkdir(tmp, 0o755); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(tmp)
		}
	}()
	partial := m.Storage().Cache.Partial
	op.setPhase("fetching")
	repo := gitstore.New(gitstore.Options{Dir: tmp, Runner: m.opt.RemoteRunner, Cred: m.credential(id, rem), NetTimeout: m.opt.NetTimeout, AllowUnsafe: m.opt.RemotePolicy.AllowUnsafe})
	if err := repo.Init(ctx, rem.Branch); err != nil {
		return err
	}
	if err := repo.SetRemote(ctx, rem.URL); err != nil {
		return err
	}
	var ferr error
	if partial {
		ferr = repo.FetchFiltered(ctx, rem.Branch, true)
	} else {
		ferr = repo.Fetch(ctx, rem.Branch, true)
	}
	if ferr != nil {
		return m.restoreFailure(id, ferr)
	}
	tip := repo.RemoteTip(ctx, rem.Branch)
	if tip == "" {
		return &RestoreFailed{Msg: "The remote has no branch " + rem.Branch + "."}
	}
	if partial {
		if err := repo.PrefetchMove(ctx, "", tip); err != nil {
			return m.restoreFailure(id, err)
		}
	}
	if err := m.evictHook("restore:fetched"); err != nil {
		return err
	}
	op.setPhase("checkout")
	if err := repo.ResetHard(ctx, tip); err != nil {
		return err
	}
	// The history the server accepted must be in the one the remote has now,
	// else the remote was rewritten while the space was evicted: the clone is
	// restored as the remote has it and the space shows "rewritten" as at any
	// other time, to be accepted by its owner.
	if lg := rem.LastGood; lg != "" {
		if anc, err := repo.IsAncestor(ctx, lg, tip); err == nil && anc {
			repo.WriteLastGood(ctx, rem.Branch, tip)
		}
	} else {
		repo.WriteLastGood(ctx, rem.Branch, tip)
	}
	op.setPhase("indexing")
	tree, err := repo.ListTree(ctx, tip)
	if err != nil {
		return err
	}
	restored, known, err := space.RestoreStamps(tmp, filepath.Join(state, "index.db"), tree)
	if err != nil {
		return err
	}
	if err := m.evictHook("restore:stamped"); err != nil {
		return err
	}
	// In place: the index-only handle goes first (the index is opened again by
	// the full space), then one rename. From here on no request is given a new
	// one: it answers restoring (see acquireIndex).
	m.ev.mu.Lock()
	op.indexClosed = true
	m.ev.mu.Unlock()
	m.closeIndexOnly(id)
	if err := m.evictHook("restore:closed"); err != nil {
		return err
	}
	if _, err := os.Lstat(root); err == nil {
		return errors.New("the directory of the space exists while it is being restored")
	}
	if m.restoreStopped(id) {
		return errSpaceGone // deleted meanwhile: its live place stays empty
	}
	// The index is kept: its epoch and sequence numbers stay, so the cursors of
	// offline clients stay good, and the files that changed on the remote come
	// in as changes at the first sync. Without the note (it fails, or the index
	// was not closed cleanly) that sync starts a new epoch: clients walk the
	// catalogue again, which is safe.
	if err := space.NoteRestored(state); err != nil {
		m.log.Warn("could not note the restore in the index; offline clients will read the catalogue again", "space", id, "error", err)
	}
	if err := os.Rename(tmp, root); err != nil {
		return err
	}
	ok = true
	cache := store.CacheFull
	if partial {
		cache = store.CachePartial
	}
	m.ev.mu.Lock()
	delete(m.ev.evicted, id)
	m.ev.mu.Unlock()
	if err := m.setCacheState(context.WithoutCancel(ctx), id, cache); err != nil {
		m.log.Warn("could not record that a space was restored", "space", id, "error", err)
	}
	m.log.Info("restored a space", "space", id, "files", len(tree), "stamps", restored, "known", known, "partial", partial)
	if err := m.evictHook("restore:done"); err != nil {
		return err
	}
	m.goBg(func() {
		m.evictHook("resize:start")
		ctx, cancel := context.WithTimeout(m.closedCtx(), time.Minute)
		defer cancel()
		m.Resize(ctx, store.AllTenants(), id) // the size of the clone is a measurement of its own (a full one only counts for the quotas)
	})
	return nil
}

// errSpaceGone: the space is being deleted; its restore stops.
var errSpaceGone = errors.New("the space is being deleted")

func (m *Manager) restoreStopped(id string) bool {
	m.ev.mu.Lock()
	defer m.ev.mu.Unlock()
	return m.ev.gone[id]
}

// stopRestores keeps a space that is being deleted from being restored from its
// remote (3.1): no restore starts for it from now on, one that runs is cancelled
// and waited for (it puts nothing at the space's live place once stopped), and
// its index-only handle (4.1) is closed. allowRestores undoes it, for a deletion
// that did not happen or a space that comes back from the bin.
func (m *Manager) stopRestores(ctx context.Context, id string) error {
	m.ev.mu.Lock()
	if m.ev.gone == nil {
		m.ev.gone = map[string]bool{}
	}
	m.ev.gone[id] = true
	op := m.ev.restores[id]
	var cancel context.CancelFunc
	if op != nil {
		cancel = op.cancel
	}
	m.ev.mu.Unlock()
	if op != nil {
		if cancel != nil {
			cancel()
		}
		select {
		case <-op.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.closeIndexOnly(id)
	return nil
}

func (m *Manager) allowRestores(id string) {
	m.ev.mu.Lock()
	delete(m.ev.gone, id)
	delete(m.ev.failed, id) // a restore stopped by the deletion is no failure
	m.ev.mu.Unlock()
}

// restoreFailure turns a failed fetch into the refusal people see, and notes
// it on the remote's state as a failed pull would.
func (m *Manager) restoreFailure(id string, err error) error {
	msg, code := "the remote repository cannot be reached", ""
	var ne *gitstore.NetError
	if errors.As(err, &ne) {
		msg = gitstore.Scrub(ne.Msg)
	}
	state, kind := store.RemoteOffline, ""
	var ce *gitstore.CredentialError
	switch {
	case errors.As(err, &ce) && ce.Code != "" && !ce.Offline:
		state, kind, code = store.RemoteError, ce.Code, ce.Code
	case gitstore.KindOf(err) == gitstore.KindAuth:
		state, kind = store.RemoteError, space.KindAuth
	case gitstore.KindOf(err) == gitstore.KindHostKey:
		state, kind = store.RemoteError, space.KindHostKey
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if cur, gerr := store.GetRemote(ctx, m.opt.Store.Q(), id); gerr == nil && cur.State != store.RemoteRewritten && !(cur.State == store.RemoteError && cur.ErrorKind == space.KindRescued) {
		store.UpdateRemoteStatus(ctx, m.opt.Store.Q(), id, store.RemoteStatus{State: state, ErrorKind: kind, LastError: msg, LastGood: cur.LastGood, RemoteTip: cur.RemoteTip}, m.opt.Now())
		m.setSync(id, state)
	}
	return &RestoreFailed{Msg: msg, Code: code, Err: err}
}

// --- evicting --------------------------------------------------------------------------

// EvictOptions say which rules apply to one eviction.
type EvictOptions struct {
	// AdminRequest: the administrator asked (POST …/evict): minIdleHours does
	// not apply. Every rule about what is on the remote does.
	AdminRequest bool
}

// Evict removes the clone of one space if all the rules allow it (see the top
// of this file). It returns ErrNotEvictable for a local, pinned or already
// evicted space, a busy error (BusyMessage says why) when something keeps it
// here, and a *RestoreFailed-like unavailable error when the remote cannot be
// asked.
func (m *Manager) Evict(ctx context.Context, id string, o EvictOptions) error {
	if m.single != nil {
		return ErrNotFound
	}
	if err := m.loadEvicted(ctx); err != nil {
		return err
	}
	q := m.opt.Store.Q()
	if _, err := store.GetSpace(ctx, q, store.AllTenants(), id); err != nil { // the eviction is the site's, by space id
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	st, err := store.GetSpaceStorage(ctx, q, id)
	if err != nil {
		return err
	}
	rem, err := store.GetRemote(ctx, q, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !rem.Connected()) {
		return fmt.Errorf("%w: it does not follow a remote repository, so it is the only copy of its pages", ErrNotEvictable)
	}
	if err != nil {
		return err
	}
	if st.Pinned {
		return fmt.Errorf("%w: it is pinned", ErrNotEvictable)
	}
	if st.Cache == store.CacheEvicted || m.isEvicted(id) {
		return fmt.Errorf("%w: it is evicted already", ErrNotEvictable)
	}
	if rem.State != store.RemoteOK {
		return busyf("Its remote repository is %s, so what the space holds cannot be checked against it.", rem.State)
	}
	if n, err := store.CountDrafts(ctx, q, id); err != nil {
		return err
	} else if n > 0 {
		return busyf("It has %d draft(s) not yet committed.", n)
	}
	m.ev.roundMu.Lock()
	defer m.ev.roundMu.Unlock()

	// Held: nothing can open it from here until it is released.
	release, sp, err := m.holdForEviction(id)
	if err != nil {
		return err
	}
	released := false
	done := func() {
		if !released {
			released = true
			release()
		}
	}
	defer done()
	if sp != nil {
		// A request, a draft sweep or a pull holds a space that is open (that
		// is a reference); this one is not held by anything. Its pending
		// changes are committed, then it is closed.
		if err := sp.Flush(); err != nil {
			return busyf("Its pending changes could not be committed yet.")
		}
		if err := sp.Close(); err != nil {
			return busyf("It could not be closed cleanly: %v.", err)
		}
		// The head it closed at, for the offline heads (as closeIdle keeps it).
		m.mu.Lock()
		m.rememberHead(id, sp.Head())
		m.mu.Unlock()
	}
	if err := m.evictHook("held"); err != nil {
		return err
	}
	if !o.AdminRequest {
		// minIdleHours, once more, now that nobody else can use it.
		idle := time.Duration(m.Storage().Cache.MinIdleHours) * time.Hour
		if m.opt.Now().Sub(m.lastUsed(st.LastUsed, id)) < idle {
			return busyf("It was used within the last %d hours.", m.Storage().Cache.MinIdleHours)
		}
	}
	root, state := m.paths(id)
	if err := m.verifyOnRemote(ctx, id, root, state, rem); err != nil {
		return err
	}
	if err := m.verifyAssets(ctx, id, root); err != nil {
		return err
	}
	if err := m.evictHook("verified"); err != nil {
		return err
	}
	q = m.opt.Store.Q()
	if err := m.setCacheState(ctx, id, store.CacheEvicting); err != nil {
		return err
	}
	if err := m.evictHook("marked"); err != nil {
		return err
	}
	if err := os.MkdirAll(m.evictDir(), 0o755); err != nil {
		return err
	}
	var rnd [6]byte
	rand.Read(rnd[:])
	aside := filepath.Join(m.evictDir(), id+"."+hex.EncodeToString(rnd[:]))
	if err := os.Rename(root, aside); err != nil {
		m.setCacheState(ctx, id, st.Cache) // nothing was touched
		return err
	}
	syncDir(filepath.Dir(root))
	if err := m.evictHook("moved"); err != nil {
		return err
	}
	m.ev.mu.Lock()
	m.ev.evicted[id] = true
	m.ev.mu.Unlock()
	if err := m.setCacheState(ctx, id, store.CacheEvicted); err != nil {
		return err
	}
	if err := m.evictHook("recorded"); err != nil {
		return err
	}
	if err := os.RemoveAll(aside); err != nil {
		m.log.Warn("could not remove the clone of an evicted space", "space", id, "path", aside, "error", err)
	}
	if err := m.evictHook("removed"); err != nil {
		return err
	}
	// The cached attachments of the space go with it, and its handle on the
	// attachment repository; the repository's table stays for the next open.
	m.forgetAssetRepo(id)
	if c := m.AssetCache(); c != nil && m.single == nil {
		if err := c.DropSpace(id); err != nil {
			m.log.Warn("could not remove the cached attachments of an evicted space", "space", id, "error", err)
		}
	}
	// What was measured of the clone is gone; the index is measured again.
	m.noteEvictedSize(ctx, id)
	m.log.Info("evicted a space", "space", id)
	return nil
}

// syncDir makes a rename durable where the platform allows.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

func (m *Manager) noteEvictedSize(ctx context.Context, id string) {
	q := m.opt.Store.Q()
	store.SetSpaceSize(ctx, q, id, 0, m.opt.Now())
	_, state := m.paths(id)
	if n, err := dirSize(ctx, state); err == nil {
		store.SetSpaceIndexBytes(ctx, q, id, n)
	}
}

// holdForEviction takes hold of a space so that nothing can open it, failing
// with a busy error when a request, a sweep or a pull holds it. It returns
// the release function and the space if it was open (not yet closed).
func (m *Manager) holdForEviction(id string) (release func(), sp *space.Space, err error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, nil, errClosed
	}
	e := m.open[id]
	if e != nil {
		select {
		case <-e.done:
		default:
			m.mu.Unlock()
			return nil, nil, busyf("It is being opened.")
		}
		if e.refs > 0 || e.closing != nil {
			m.mu.Unlock()
			return nil, nil, busyf("A request or a sync is using it right now.")
		}
		sp = e.sp
	} else {
		e = &opening{done: make(chan struct{}), last: m.opt.Now()}
		close(e.done)
		m.open[id] = e
	}
	closing := make(chan struct{})
	e.closing = closing
	m.mu.Unlock()
	release = func() {
		m.mu.Lock()
		if m.open[id] == e {
			delete(m.open, id) // closed, or never opened: the next request opens it from disk
		}
		close(closing)
		m.mu.Unlock()
	}
	return release, sp, nil
}

// verifyOnRemote checks, with the space closed and held, that everything the
// space has is on its remote. It never changes anything but the clone's
// remote-tracking refs.
func (m *Manager) verifyOnRemote(ctx context.Context, id, root, state string, rem *store.Remote) error {
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return busyf("Its directory is not there.")
	}
	if why := space.StateClean(state); why != "" {
		return busyf("%s.", why)
	}
	repo := gitstore.New(gitstore.Options{Dir: root, Runner: m.opt.RemoteRunner, Cred: m.credential(id, rem), NetTimeout: m.opt.NetTimeout, AllowUnsafe: m.opt.RemotePolicy.AllowUnsafe})
	if what, err := repo.Busy(ctx); err != nil {
		return busyf("The clone could not be looked at: %v.", gitstore.Scrub(err.Error()))
	} else if what != "" {
		return busyf("The clone is in the middle of a %s.", what)
	}
	if mod, err := repo.ModifiedTracked(ctx); err != nil {
		return busyf("The clone could not be looked at: %v.", gitstore.Scrub(err.Error()))
	} else if len(mod) > 0 {
		sort.Strings(mod)
		return busyf("The clone has changes that are not committed (%s).", strings.Join(mod[:min(3, len(mod))], ", "))
	}
	if files, err := repo.Untracked(ctx); err != nil {
		return busyf("The clone could not be looked at: %v.", gitstore.Scrub(err.Error()))
	} else if len(files) > 0 {
		sort.Strings(files)
		return busyf("The clone has files no commit has (%s).", strings.Join(files[:min(3, len(files))], ", "))
	}
	if refs, err := repo.UnpushedRefs(ctx); err != nil {
		return busyf("The clone could not be looked at: %v.", gitstore.Scrub(err.Error()))
	} else if len(refs) > 0 {
		return busyf("It holds commits that were set aside because they never reached the remote (%s).", refs[0])
	}
	head := repo.Head(ctx)
	if head == "" {
		return busyf("It has no commit.")
	}
	// Ask the remote now, not the last time someone did.
	fetchCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := repo.Fetch(fetchCtx, rem.Branch, false); err != nil {
		return m.evictUnreachable(err)
	}
	lastGood := repo.ReadLastGood(ctx, rem.Branch)
	if lastGood == "" {
		lastGood = rem.LastGood
	}
	if _, err := repo.CheckRemote(ctx, rem.Branch, lastGood); err != nil {
		var hr *gitstore.HistoryRewrittenError
		if errors.As(err, &hr) {
			return busyf("The history of the remote was rewritten.")
		}
		if errors.Is(err, gitstore.ErrNoBranch) {
			return busyf("The remote has no branch %s any more.", rem.Branch)
		}
		return busyf("The remote could not be checked: %v.", gitstore.Scrub(err.Error()))
	}
	tip := repo.RemoteTip(ctx, rem.Branch)
	if tip == "" {
		return busyf("The remote's branch is not known.")
	}
	anc, err := repo.IsAncestor(ctx, head, tip)
	if err != nil {
		return busyf("The remote could not be checked: %v.", gitstore.Scrub(err.Error()))
	}
	if !anc {
		n, _ := repo.CountNotIn(ctx, tip)
		return busyf("%d commit(s) of the space are not on the remote.", n)
	}
	return nil
}

// evictUnreachable maps a failed fetch to the answer of POST …/evict: the
// space is not evicted without checking that the remote has everything.
func (m *Manager) evictUnreachable(err error) error {
	msg := "the remote repository cannot be reached"
	var ne *gitstore.NetError
	if errors.As(err, &ne) {
		msg = gitstore.Scrub(ne.Msg)
	}
	return &space.UnavailableError{Msg: msg + "; the space is not evicted without checking that the remote has everything.", Err: err}
}

// --- the rounds ---------------------------------------------------------------------------

// DiskFree is how much room the disk of the data directory has; replaced in tests.
var DiskFree = diskFree

// RunEvict evicts idle spaces every EvictEvery (10 minutes) until ctx ends.
func (m *Manager) RunEvict(ctx context.Context) {
	if m.single != nil {
		return
	}
	every := m.opt.EvictEvery
	if every == 0 {
		every = evictRoundEvery
	}
	if every < 0 {
		return
	}
	m.StampUnused(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.EvictRound(ctx); err != nil && ctx.Err() == nil {
				m.log.Warn("a round of eviction failed", "error", err)
			}
		}
	}
}

// StampUnused gives the spaces that were never used since the column exists
// the time of now, so that an upgrade does not make every space look idle.
func (m *Manager) StampUnused(ctx context.Context) {
	if _, err := m.opt.Store.Q().Exec(ctx, `UPDATE spaces SET last_used = ? WHERE last_used = 0`, m.opt.Now().UnixMilli()); err != nil {
		m.log.Warn("could not note the time of the spaces that were never used", "error", err)
	}
}

// EvictRound is one round: the spaces not used for evictAfterDays, and, while
// the clones exceed clonesBytes or the disk has less than minFreeBytes free,
// the least recently used ones. A space used within minIdleHours is never
// chosen. The candidates come from one query; the checks that need a clone are
// made one space at a time, by Evict. It returns what stopped it, not what it
// skipped (a space that is not evictable now is looked at again next round).
func (m *Manager) EvictRound(ctx context.Context) error {
	if m.single != nil {
		return nil
	}
	s := m.Storage()
	if s.Cache.EvictAfterDays == nil && s.Cache.ClonesBytes == nil && s.Cache.MinFreeBytes == nil {
		return nil
	}
	now := m.opt.Now()
	idleBefore := now.Add(-time.Duration(s.Cache.MinIdleHours) * time.Hour)
	cands, err := store.EvictionCandidates(ctx, m.opt.Store.Q(), idleBefore, 500)
	if err != nil {
		return err
	}
	var total int64
	if s.Cache.ClonesBytes != nil {
		if total, err = store.CloneBytesOfRemoteSpaces(ctx, m.opt.Store.Q()); err != nil {
			return err
		}
	}
	free := func() (uint64, bool) { return DiskFree(m.opt.DataDir) }
	pressed := func() bool {
		if s.Cache.ClonesBytes != nil && total > *s.Cache.ClonesBytes {
			return true
		}
		if s.Cache.MinFreeBytes != nil {
			if f, ok := free(); ok && int64(f) < *s.Cache.MinFreeBytes {
				return true
			}
		}
		return false
	}
	for _, c := range cands {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		last := m.lastUsed(c.LastUsed, c.ID)
		if !last.Before(idleBefore) {
			continue // used since the query
		}
		expired := s.Cache.EvictAfterDays != nil && now.Sub(last) >= time.Duration(*s.Cache.EvictAfterDays)*24*time.Hour
		if !expired && !pressed() {
			// Candidates are in order of use, so the later ones are younger: the
			// expired ones have all been seen, and there is no pressure.
			if s.Cache.EvictAfterDays == nil || now.Sub(last) < time.Duration(*s.Cache.EvictAfterDays)*24*time.Hour {
				break
			}
		}
		err := m.Evict(ctx, c.ID, EvictOptions{})
		switch {
		case err == nil:
			total -= c.CloneBytes
		case errors.Is(err, ErrBusy), errors.Is(err, ErrNotEvictable):
			m.log.Debug("a space is not evicted now", "space", c.ID, "why", err.Error())
		default:
			var ue *space.UnavailableError
			if errors.As(err, &ue) {
				continue // its remote is down: next round
			}
			if ctx.Err() == nil {
				m.log.Warn("could not evict a space", "space", c.ID, "error", err)
			}
		}
	}
	return nil
}

// Pin keeps the space's files on the server: it is never evicted, an evicted
// one is restored, and (milestone 3.1 Part 1) its clone is made full when the
// remote can be reached. A pinned local space is the same as an unpinned one.
func (m *Manager) SetPinned(ctx context.Context, id string, pinned bool) error {
	if err := store.SetSpacePinned(ctx, m.opt.Store.Q(), id, pinned); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	m.notePinned(id, pinned)
	if pinned {
		if rem, err := store.GetRemote(ctx, m.opt.Store.Q(), id); err == nil && rem.Connected() {
			m.makeFullSoon(id)
		}
	}
	return nil
}

// SetExempt leaves one space out of the requirement of a remote.
func (m *Manager) SetExempt(ctx context.Context, id string, exempt bool) error {
	if err := store.SetSpaceExempt(ctx, m.opt.Store.Q(), id, exempt); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	m.dropFacts(id)
	return nil
}
