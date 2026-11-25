package spaces

// The recycle bin (milestone 3.5, docs/api.md "The recycle bin"). Deleting a
// space never deletes a byte: its rows move into one recycle item, in one
// transaction (internal/store/recycle.go), and its directories move to
// DATA/recycle/<item>/space and /cache. Nothing is deleted until the item is
// purged, after recycle.days, by the job of this file.
//
// The order of each action is the order that makes a crash harmless, and what a
// start does about each point is written next to it. "rows" is the transaction
// that moves the records; the item is in state "moving" from it until the
// directories are where they belong.
//
// Recycling:
//
//	1. close the space (what it holds is committed) and mark it disabled in
//	   memory, so that nothing opens it again;
//	2. rows: the item is made (state moving, with the list of directories that
//	   exist), the live rows are deleted. A crash before it changes nothing; the
//	   transaction is one commit.
//	3. move DATA/spaces/<id> and then DATA/cache/<id> into DATA/recycle/<item>/.
//	   A crash here leaves state moving: the start moves what is still in its
//	   old place (recoverRecycle). A directory that is in neither place is
//	   reported and left; the item still holds every record.
//	4. state ready.
//
// Restoring (only an item at rest, state ready, is restored):
//
//	1. state restoring (one holder at a time);
//	2. move the directories back. A crash after this and before 3 leaves state
//	   restoring with the directories at the live places and no rows: the start
//	   moves them back and the item is ready again.
//	3. rows: the live rows are inserted and the item deleted, one transaction.
//	   A failure of it (an owner is needed, a space with the id exists) moves
//	   the directories back and the item is ready again.
//
// Purging: state purging (so that a restore cannot start), delete the
// directory, delete the item. A crash leaves state purging and the start
// finishes it.
//
// An action holds its item in memory while it runs (holdItem): the hourly look
// at the items that are not at rest skips one this process is working on,
// however long it takes, so it only ever finishes what a crash or a failure
// left. A restore's last transaction deletes the item only if it is still
// restoring, and puts nothing back otherwise. A move across filesystems is a
// copy and a delete (movedir.go).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/store"
)

var (
	// ErrBusyItem: the item is being moved, restored or purged.
	ErrBusyItem = store.ErrBusyItem
	// ErrOwnerRequired: restoring a space with no owner left without naming one.
	ErrOwnerRequired = store.ErrOwnerRequired
	// ErrRestoreConflict: a space with the id exists again.
	ErrRestoreConflict = store.ErrRestoreConflict
	// ErrNeedsPerson: a personal space is restored with its person.
	ErrNeedsPerson = store.ErrNeedsPerson
)

// NotRestorable is the refusal of restoring an item that cannot be restored now,
// with what is wrong (a directory that is not in DATA/recycle).
type NotRestorable struct{ Problems []string }

func (e *NotRestorable) Error() string {
	return "the item cannot be restored now: " + fmt.Sprint(e.Problems)
}

// Recycle options.
const (
	// DefaultRecycleDays is how long an item is kept.
	DefaultRecycleDays = 30
	// DefaultRecycleEvery is how often the purge job looks.
	DefaultRecycleEvery = time.Hour
	// DefaultPurgePause is the wait between two directories being deleted.
	DefaultPurgePause = 25 * time.Millisecond
)

func (m *Manager) recycleRoot() string { return filepath.Join(m.opt.DataDir, "recycle") }

// RecycleDir is where an item's directories are.
func (m *Manager) RecycleDir(item string) string { return filepath.Join(m.recycleRoot(), item) }

// SetRecycleDays gives the manager the number of days items are kept, read
// when the job runs and when an item is listed (the server binds it to the
// setting recycle.days). An error says the setting could not be read: the
// purge job then purges nothing (see purgeDays).
func (m *Manager) SetRecycleDays(f func(context.Context) (int, error)) {
	m.recycle.mu.Lock()
	m.recycle.days = f
	m.recycle.mu.Unlock()
}

// RecycleDays is how many days an item is kept now, as an item's date shows
// it: the default when the setting cannot be read (only a date is shown on it;
// nothing is purged on it, see purgeDays).
func (m *Manager) RecycleDays(ctx context.Context) int {
	if n, err := m.purgeDays(ctx); err == nil {
		return n
	}
	return DefaultRecycleDays
}

// purgeDays is recycle.days for the purge job: an error when the setting
// cannot be read, so that a round is skipped rather than run on a default that
// may be shorter than what the site set.
func (m *Manager) purgeDays(ctx context.Context) (int, error) {
	m.recycle.mu.Lock()
	f := m.recycle.days
	m.recycle.mu.Unlock()
	if f == nil {
		return DefaultRecycleDays, nil
	}
	n, err := f(ctx)
	if err != nil {
		return 0, fmt.Errorf("recycle.days cannot be read: %w", err)
	}
	if n <= 0 {
		return DefaultRecycleDays, nil
	}
	return n, nil
}

// recycleState is the manager's part of the recycle bin.
type recycleState struct {
	mu   sync.Mutex
	days func(context.Context) (int, error)
	// swept is when the tombstones were last looked at (guarded by mu).
	swept time.Time
	// held are the items an action of this process is working on (guarded by
	// mu; see holdItem).
	held map[string]bool
	// wg counts the purges started by a request (StartPurge).
	wg sync.WaitGroup
}

// holdItem marks an item as worked on by an action of this process; false when
// another action holds it. The hourly look skips held items, so it never takes
// over an action that is still running, however long it takes; a crash ends
// every hold, and the start finishes what it left.
func (m *Manager) holdItem(id string) bool {
	m.recycle.mu.Lock()
	defer m.recycle.mu.Unlock()
	if m.recycle.held[id] {
		return false
	}
	if m.recycle.held == nil {
		m.recycle.held = map[string]bool{}
	}
	m.recycle.held[id] = true
	return true
}

func (m *Manager) letItem(id string) {
	m.recycle.mu.Lock()
	delete(m.recycle.held, id)
	m.recycle.mu.Unlock()
}

// RecycleOptions say who deletes a space and why.
type RecycleOptions struct {
	By, ByName string
	Reason     string
	// Parent is the item of the person the space is recycled with.
	Parent string
	// WithPerson lets a personal space be recycled (it goes with its person).
	WithPerson bool
}

// RecycleSpace deletes a space: it goes into the recycle bin, whole, and can be
// restored until it is purged. It closes the space first (committing what it
// holds) and refuses with ErrBusy if requests still hold it, or if its pending
// changes cannot be committed: nothing is lost then, the space is as it was. A
// personal space cannot be deleted by itself (it goes with its person).
func (m *Manager) RecycleSpace(ctx context.Context, sc store.Scope, id string, o RecycleOptions) (*store.RecycleItem, error) {
	sp, err := m.adminSpace(ctx, sc, id)
	if err != nil {
		return nil, err
	}
	if sp.Personal && !o.WithPerson {
		return nil, ErrPersonalSpace
	}
	if err := m.loadDisabled(ctx); err != nil {
		return nil, err
	}
	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	return m.recycleLocked(ctx, sc, id, o)
}

func (m *Manager) recycleLocked(ctx context.Context, sc store.Scope, id string, o RecycleOptions) (*store.RecycleItem, error) {
	// No new request can take hold of it from here. The flag stays after the
	// delete so a request that was already on its way cannot reopen the
	// directory while it is being moved.
	was := m.markDisabled(id, true)
	// A restore of its clone from the remote (3.1) is stopped and waited for,
	// and its index-only handle closed, so nothing is written at its live place
	// after the move.
	if err := m.stopRestores(ctx, id); err != nil {
		m.allowRestores(id)
		m.markDisabled(id, was)
		return nil, err
	}
	if err := m.evict(ctx, id); err != nil {
		m.allowRestores(id)
		m.markDisabled(id, was)
		return nil, err
	}
	root, state := m.paths(id)
	var dirs []string
	if pathExists(root) {
		dirs = append(dirs, "space")
	}
	if pathExists(state) {
		dirs = append(dirs, "cache")
	}
	now := m.opt.Now()
	itemID := page.NewID(now)
	m.holdItem(itemID) // a new id: nobody else holds it
	defer m.letItem(itemID)
	var res store.RecycledSpace
	err := m.opt.Store.Tx(ctx, func(q store.Q) error {
		var err error
		res, err = store.RecycleSpaceRows(ctx, q, sc, store.RecycleSpaceIn{ItemID: itemID, SpaceID: id, By: o.By, ByName: o.ByName,
			Reason: o.Reason, Parent: o.Parent, Dirs: dirs, Now: now})
		return err
	})
	if err != nil {
		m.allowRestores(id)
		m.markDisabled(id, was)
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	m.MembershipChanged(res.Members...)
	m.usageAdded(ctx, res.Counted)
	m.spaceChanged(id)
	m.setSync(id, "")
	m.assetsGone(id)
	m.noteConnected(id, false)
	m.forgetCacheState(id)
	m.forgetNotices(id)
	if err := m.recycleStep("recycle:rows"); err != nil {
		return res.Item, err
	}
	if err := m.moveToRecycle(ctx, res.Item); err != nil {
		// The records are safe in the item; the move is tried again by the job.
		m.log.Error("a deleted space's directories could not be moved to the recycle bin; they stay where they are and the move is tried again", "space", id, "item", res.Item.ID, "error", err)
		return res.Item, nil
	}
	res.Item.State = store.RecycleReady
	m.log.Info("space recycled", "space", id, "name", res.Item.Name, "item", res.Item.ID)
	return res.Item, nil
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// recycleStep runs the test hook at a named step; an error stops there, as a
// crash would.
func (m *Manager) recycleStep(step string) error {
	if m.opt.RecycleHook != nil {
		return m.opt.RecycleHook(step)
	}
	return nil
}

// livePath is where a directory of a space is when it is in use: "space" is
// DATA/spaces/<id>, "cache" DATA/cache/<id>.
func (m *Manager) livePath(spaceID, dir string) string {
	root, state := m.paths(spaceID)
	if dir == "cache" {
		return state
	}
	return root
}

// moveToRecycle moves the directories of an item in state moving to their
// place in the recycle bin and makes it ready. A directory already there is
// left alone and one in neither place is reported; moving is repeatable, so a
// crash anywhere is finished by calling it again.
func (m *Manager) moveToRecycle(ctx context.Context, it *store.RecycleItem) error {
	base := m.RecycleDir(it.ID)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	syncDir(m.recycleRoot())
	for _, d := range it.Dirs {
		dst := filepath.Join(base, d)
		src := m.livePath(it.SpaceID, d)
		if err := removeLeftovers(src, dst); err != nil {
			return err
		}
		switch {
		case pathExists(dst) && pathExists(src):
			// A copy across filesystems that finished (a destination is only
			// ever there whole) before its source was set aside: the space is
			// deleted and not restorable while it moves, so the source is the
			// copy's leftover.
			if err := os.RemoveAll(src); err != nil {
				return err
			}
		case pathExists(dst):
			// moved already
		case !pathExists(src):
			m.log.Error("a deleted space has no directory to move to the recycle bin", "space", it.SpaceID, "item", it.ID, "dir", d)
		default:
			if err := m.moveDir(src, dst); err != nil {
				return err
			}
		}
		if err := m.recycleStep("recycle:moved-" + d); err != nil {
			return err
		}
	}
	return store.SetRecycleState(ctx, m.opt.Store.Q(), it.ID, store.RecycleMoving, store.RecycleReady, m.opt.Now())
}

// Problems says why an item cannot be restored now ("" list: it can): a
// directory it moved is not in the recycle bin any more.
func (m *Manager) Problems(it *store.RecycleItem) []string {
	var out []string
	if it.State != store.RecycleReady {
		return []string{"the item is not at rest (" + it.State + ")"}
	}
	for _, d := range it.Dirs {
		if !pathExists(filepath.Join(m.RecycleDir(it.ID), d)) {
			out = append(out, "the "+d+" directory is missing from the recycle bin")
		}
	}
	return out
}

// RestoreResult is what a restore did.
type RestoreResult struct {
	SpaceID string
	Name    string
	Skipped []store.RestoreSkipped
}

// RestoreSpace brings a space back from the recycle bin: its directories and
// its rows, under the same ids, in its tenant. People and groups that are gone
// are left out and listed; when that leaves no owner, owner must name one
// (ErrOwnerRequired). It is never refused for a quota.
func (m *Manager) RestoreSpace(ctx context.Context, sc store.Scope, itemID, owner string) (*RestoreResult, error) {
	it, err := store.GetRecycleItem(ctx, m.opt.Store.Q(), sc, itemID, false)
	if err != nil {
		return nil, mapItemErr(err)
	}
	if it.Kind != store.RecycleSpace {
		return nil, fmt.Errorf("%w: only a space is restored here", ErrInvalid)
	}
	return m.restoreSpaceItem(ctx, it, owner, false)
}

func mapItemErr(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// restoreSpaceItem is RestoreSpace for an item already found; withPerson says
// the space's person is being restored with it.
func (m *Manager) restoreSpaceItem(ctx context.Context, it *store.RecycleItem, owner string, withPerson bool) (*RestoreResult, error) {
	if it.Personal && !withPerson {
		return nil, ErrNeedsPerson
	}
	if err := m.loadDisabled(ctx); err != nil {
		return nil, err
	}
	if !m.holdItem(it.ID) {
		return nil, ErrBusyItem
	}
	defer m.letItem(it.ID)
	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	if p := m.Problems(it); len(p) > 0 {
		return nil, &NotRestorable{Problems: p}
	}
	for _, d := range it.Dirs {
		if pathExists(m.livePath(it.SpaceID, d)) {
			return nil, ErrRestoreConflict
		}
	}
	q := m.opt.Store.Q()
	if err := store.SetRecycleState(ctx, q, it.ID, store.RecycleReady, store.RecycleRestoring, m.opt.Now()); err != nil {
		return nil, mapItemErr(err)
	}
	undo := func() {
		m.moveBack(it)
		if err := store.SetRecycleState(context.WithoutCancel(ctx), m.opt.Store.Q(), it.ID, store.RecycleRestoring, store.RecycleReady, m.opt.Now()); err != nil {
			m.log.Error("a recycle item could not be made ready again after a restore failed", "item", it.ID, "error", err)
		}
	}
	if err := m.recycleStep("restore:marked"); err != nil {
		return nil, err
	}
	for _, d := range it.Dirs {
		if err := m.moveDir(filepath.Join(m.RecycleDir(it.ID), d), m.livePath(it.SpaceID, d)); err != nil {
			undo()
			return nil, err
		}
		if err := m.recycleStep("restore:moved-" + d); err != nil {
			return nil, err
		}
	}
	var out store.RestoreOutcome
	err := m.opt.Store.Tx(ctx, func(tq store.Q) error {
		var err error
		out, err = store.RestoreSpaceRows(ctx, tq, it, owner, withPerson, m.opt.Now())
		return err
	})
	if err != nil {
		undo()
		if errors.Is(err, store.ErrNotFound) && owner != "" {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	os.RemoveAll(m.RecycleDir(it.ID))
	m.allowRestores(it.SpaceID)
	m.markDisabled(it.SpaceID, out.Disabled)
	m.MembershipChanged(out.Members...)
	m.spaceChanged(it.SpaceID)
	m.setSync(it.SpaceID, out.Remote)
	m.noteConnected(it.SpaceID, out.AssetsRemote)
	m.restoreCacheState(it.SpaceID, out.Cache)
	m.usageAdded(ctx, out.Counted)
	m.log.Info("space restored", "space", it.SpaceID, "item", it.ID)
	return &RestoreResult{SpaceID: it.SpaceID, Name: it.Name, Skipped: out.Skipped}, nil
}

// moveBack puts the directories of an item that are at their live places back
// in the recycle bin, for a restore that did not finish.
func (m *Manager) moveBack(it *store.RecycleItem) {
	base := m.RecycleDir(it.ID)
	os.MkdirAll(base, 0o755)
	syncDir(m.recycleRoot())
	for _, d := range it.Dirs {
		src, dst := m.livePath(it.SpaceID, d), filepath.Join(base, d)
		// A copy to the live place cut short, or the bin's copy set aside and
		// cut short while it was deleted (its live copy is whole).
		if err := removeLeftovers(src, dst); err != nil {
			m.log.Error("could not remove what a move of a restore left", "item", it.ID, "dir", d, "error", err)
		}
		switch {
		case pathExists(src) && pathExists(dst):
			// A copy to the live place that finished before the bin's was set
			// aside: both are whole, the live one goes.
			if err := os.RemoveAll(src); err != nil {
				m.log.Error("could not remove a copy of a directory the restore did not finish", "item", it.ID, "dir", d, "error", err)
			}
		case pathExists(src):
			if err := m.moveDir(src, dst); err != nil {
				m.log.Error("could not put a directory back in the recycle bin", "item", it.ID, "dir", d, "error", err)
			}
		}
	}
}

// forgetCacheState drops what the manager remembers of a space's clone.
func (m *Manager) forgetCacheState(id string) {
	m.cs.mu.Lock()
	delete(m.cs.m, id)
	m.cs.mu.Unlock()
	m.ev.mu.Lock()
	if m.ev.evicted != nil {
		delete(m.ev.evicted, id)
	}
	m.ev.mu.Unlock()
}

// restoreCacheState tells the in-memory sets of a restored space what its
// state database row says.
func (m *Manager) restoreCacheState(id, state string) {
	m.forgetCacheState(id)
	if state == "" {
		return
	}
	m.cs.mu.Lock()
	if m.cs.loaded {
		m.cs.m[id] = state
	}
	m.cs.mu.Unlock()
	if state == store.CacheEvicted {
		m.ev.mu.Lock()
		if m.ev.evicted != nil {
			m.ev.evicted[id] = true
		}
		m.ev.mu.Unlock()
	}
}

// --- purging ---------------------------------------------------------------------

// PurgeItem deletes an item for good: its directories and then its row. Only an
// item at rest is purged (ErrBusyItem otherwise), and the state purging keeps a
// restore from starting meanwhile.
func (m *Manager) PurgeItem(ctx context.Context, sc store.Scope, itemID string) (*store.RecycleItem, error) {
	it, err := m.markPurging(ctx, sc, itemID)
	if err != nil {
		return nil, err
	}
	defer m.letItem(it.ID)
	if err := m.finishPurge(ctx, it); err != nil {
		return it, err
	}
	return it, nil
}

// markPurging finds an item at rest, holds it and marks it purging; the caller
// lets it go when the purge is over.
func (m *Manager) markPurging(ctx context.Context, sc store.Scope, itemID string) (*store.RecycleItem, error) {
	it, err := store.GetRecycleItem(ctx, m.opt.Store.Q(), sc, itemID, false)
	if err != nil {
		return nil, mapItemErr(err)
	}
	if !m.holdItem(it.ID) {
		return nil, ErrBusyItem
	}
	if err := store.SetRecycleState(ctx, m.opt.Store.Q(), it.ID, store.RecycleReady, store.RecyclePurging, m.opt.Now()); err != nil {
		m.letItem(it.ID)
		return nil, mapItemErr(err)
	}
	it.State = store.RecyclePurging
	return it, nil
}

// WaitPurges waits for the purges StartPurge started (tests, and a caller that
// wants them over).
func (m *Manager) WaitPurges() { m.recycle.wg.Wait() }

// StartPurge marks an item purging (so nothing else starts on it) and purges it
// in the background, on the manager's own time: a purge is paced and may take
// long, longer than a request. done is called when it is over, with what went
// wrong; a purge that fails or is cut short stays purging and the hourly job (or
// the next start) finishes it.
func (m *Manager) StartPurge(ctx context.Context, sc store.Scope, itemID string, done func(*store.RecycleItem, error)) (*store.RecycleItem, error) {
	it, err := m.markPurging(ctx, sc, itemID)
	if err != nil {
		return nil, err
	}
	m.recycle.wg.Add(1)
	go func() {
		defer m.recycle.wg.Done()
		defer m.letItem(it.ID)
		err := m.finishPurge(m.closedCtx(), it)
		if err != nil {
			m.log.Warn("a purge did not finish; it is finished by the hourly job", "item", it.ID, "error", err)
		}
		if done != nil {
			done(it, err)
		}
	}()
	return it, nil
}

// finishPurge does what a purge does after the item is marked: also what a
// start does for an item a crash left purging.
func (m *Manager) finishPurge(ctx context.Context, it *store.RecycleItem) error {
	if err := m.recycleStep("purge:marked"); err != nil {
		return err
	}
	if err := removeAllPaced(ctx, m.RecycleDir(it.ID), m.purgePause()); err != nil {
		return err
	}
	if err := m.recycleStep("purge:dirs"); err != nil {
		return err
	}
	if it.Kind == store.RecycleUser {
		if err := m.purgeUserItem(ctx, it); err != nil {
			return err
		}
	}
	err := store.DeleteRecycleItem(ctx, m.opt.Store.Q(), it.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	m.log.Info("recycle item purged", "item", it.ID, "kind", it.Kind, "space", it.SpaceID)
	return nil
}

func (m *Manager) purgePause() time.Duration {
	if m.opt.PurgePause != 0 {
		return m.opt.PurgePause
	}
	return DefaultPurgePause
}

// removeAllPaced deletes a directory tree, waiting between the entries of each
// directory it lists, so that purging a big repository is one slow walk and not
// a burst of disk work (the 1.7 measurement is paced the same way). A pause <= 0
// does not wait.
func removeAllPaced(ctx context.Context, dir string, pause time.Duration) error {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return os.RemoveAll(dir)
	}
	for _, e := range ents {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := filepath.Join(dir, e.Name())
		if e.IsDir() && !isSymlink(e) {
			if err := removeAllPaced(ctx, p, pause); err != nil {
				return err
			}
		}
		if err := os.RemoveAll(p); err != nil {
			return err
		}
		if pause > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pause):
			}
		}
	}
	return os.RemoveAll(dir)
}

func isSymlink(e os.DirEntry) bool { return e.Type()&os.ModeSymlink != 0 }

// PurgeExpired purges, one at a time, the items that were deleted more than
// recycle.days ago. It returns how many it purged.
func (m *Manager) PurgeExpired(ctx context.Context) (int, error) {
	n := 0
	for ctx.Err() == nil {
		days, err := m.purgeDays(ctx)
		if err != nil {
			return n, err // never purge on a default
		}
		before := m.opt.Now().Add(-time.Duration(days) * 24 * time.Hour)
		it, ok, err := store.NextExpiredRecycle(ctx, m.opt.Store.Q(), before)
		if err != nil {
			return n, err
		}
		if !ok {
			return n, nil
		}
		if !m.holdItem(it.ID) {
			return n, nil // an action of this process has it: the next round
		}
		if err := store.SetRecycleState(ctx, m.opt.Store.Q(), it.ID, store.RecycleReady, store.RecyclePurging, m.opt.Now()); err != nil {
			m.letItem(it.ID)
			if errors.Is(err, store.ErrBusyItem) || errors.Is(err, store.ErrNotFound) {
				continue // somebody else has it: the next one
			}
			return n, err
		}
		err = m.finishPurge(ctx, it)
		m.letItem(it.ID)
		if err != nil {
			return n, err
		}
		n++
	}
	return n, ctx.Err()
}

// RunRecycle runs the purge job: every RecycleEvery (an hour) it purges what is
// past its date and finishes what a failed move left.
func (m *Manager) RunRecycle(ctx context.Context) {
	every := m.opt.RecycleEvery
	if every == 0 {
		every = DefaultRecycleEvery
	}
	if every < 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.recycleRound(ctx)
		}
	}
}

func (m *Manager) recycleRound(ctx context.Context) {
	if err := m.recoverRecycle(ctx, time.Minute); err != nil && ctx.Err() == nil {
		m.log.Warn("could not finish the moves of the recycle bin", "error", err)
	}
	if _, err := m.PurgeExpired(ctx); err != nil && ctx.Err() == nil {
		m.log.Warn("could not purge the recycle bin", "error", err)
	}
	if err := m.ExpireExports(ctx); err != nil && ctx.Err() == nil {
		m.log.Warn("could not delete the exports that are past their days", "error", err)
	}
	m.sweepTombstonesDaily(ctx)
}

// recoverRecycle finishes what a crash (or a failed move) left: items moving
// get their directories moved, items restoring get theirs put back, items
// purging are purged. older leaves out items changed more recently than that
// (the job's look must not take an action that is running now); the start passes
// 0. Each item is read again once it is held, and left alone unless it is still
// in the state it was listed in: an action may have finished on it since.
func (m *Manager) recoverRecycle(ctx context.Context, older time.Duration) error {
	items, err := store.RecycleNotAtRest(ctx, m.opt.Store.Q())
	if err != nil {
		return err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Kind > items[j].Kind }) // spaces before people
	for i := range items {
		it := &items[i]
		if older > 0 && m.opt.Now().Sub(it.StateAt) < older {
			continue
		}
		// An action of this process that is still running holds its item: it is
		// not taken over, however long it has been at it.
		if !m.holdItem(it.ID) {
			continue
		}
		// The list is as it was when it was read: an action may have finished
		// on the item and let it go since (a restore that is done must not be
		// moved back). It is read again under the hold, and left alone unless it
		// is still in the state it was listed in.
		cur, err := store.GetRecycleItem(ctx, m.opt.Store.Q(), store.AllTenants(), it.ID, false)
		if errors.Is(err, store.ErrNotFound) || (err == nil && (cur.State != it.State || cur.State == store.RecycleReady ||
			(older > 0 && m.opt.Now().Sub(cur.StateAt) < older))) {
			m.letItem(it.ID)
			continue
		}
		if err != nil {
			m.letItem(it.ID)
			return err
		}
		it = cur
		if err := m.recoverItem(ctx, it); err != nil {
			m.letItem(it.ID)
			return err
		}
		m.letItem(it.ID)
	}
	return nil
}

// recoverItem finishes what a crash or a failure left of one item.
func (m *Manager) recoverItem(ctx context.Context, it *store.RecycleItem) error {
	switch it.State {
	case store.RecycleMoving:
		if err := m.moveToRecycle(ctx, it); err != nil {
			m.log.Warn("could not finish moving a recycled space", "item", it.ID, "error", err)
			return nil
		}
		m.log.Warn("a deletion was cut short; its directories are in the recycle bin now", "item", it.ID)
	case store.RecycleRestoring:
		m.moveBack(it)
		if err := store.SetRecycleState(ctx, m.opt.Store.Q(), it.ID, store.RecycleRestoring, store.RecycleReady, m.opt.Now()); err != nil {
			return err
		}
		m.log.Warn("a restore was cut short; the item is in the recycle bin again", "item", it.ID)
	case store.RecyclePurging:
		if err := m.finishPurge(ctx, it); err != nil {
			m.log.Warn("could not finish a purge", "item", it.ID, "error", err)
			return nil
		}
		m.log.Warn("a purge was cut short; it is finished", "item", it.ID)
	}
	return nil
}

// FinishRecycle finishes what a failed move or a crash left in the recycle bin:
// what a start does, for a caller that wants it now.
func (m *Manager) FinishRecycle(ctx context.Context) error { return m.recoverRecycle(ctx, 0) }

// sweepTombstonesDaily deletes the rows of purged people that nothing refers to
// any more, the bulk jobs that ended more than BulkJobKeep ago and old notifications, once a day.
func (m *Manager) sweepTombstonesDaily(ctx context.Context) {
	m.recycle.mu.Lock()
	due := m.opt.Now().Sub(m.recycle.swept) >= tombstoneEvery
	if due {
		m.recycle.swept = m.opt.Now()
	}
	m.recycle.mu.Unlock()
	if !due {
		return
	}
	if n, err := m.SweepTombstones(ctx); err != nil && ctx.Err() == nil {
		m.log.Warn("could not sweep the rows of purged people", "error", err)
	} else if n > 0 {
		m.log.Info("deleted the rows of purged people that nothing refers to", "count", n)
	}
	if n, err := store.PruneBulkJobs(ctx, m.opt.Store.Q(), m.opt.Now().Add(-store.BulkJobKeep)); err != nil && ctx.Err() == nil {
		m.log.Warn("could not delete the bulk jobs that ended long ago", "error", err)
	} else if n > 0 {
		m.log.Info("deleted the bulk jobs that ended long ago", "count", n)
	}
	if n, err := store.TrimNotifications(ctx, m.opt.Store.Q(), m.opt.Now()); err != nil && ctx.Err() == nil {
		m.log.Warn("could not trim the notifications", "error", err)
	} else if n > 0 {
		m.log.Info("deleted old notifications", "count", n)
	}
}

// StepHook runs the test hook of the recycle bin at a named step of another
// action that has crash points of its own (deleting a person); an error it
// returns stops there as a crash would (tests).
func (m *Manager) StepHook(step string) error { return m.recycleStep(step) }
