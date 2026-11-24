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

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/store"
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
// setting recycle.days).
func (m *Manager) SetRecycleDays(f func(context.Context) int) {
	m.recycle.mu.Lock()
	m.recycle.days = f
	m.recycle.mu.Unlock()
}

// RecycleDays is how many days an item is kept now.
func (m *Manager) RecycleDays(ctx context.Context) int {
	m.recycle.mu.Lock()
	f := m.recycle.days
	m.recycle.mu.Unlock()
	if f != nil {
		if n := f(ctx); n > 0 {
			return n
		}
	}
	return DefaultRecycleDays
}

// recycleState is the manager's part of the recycle bin.
type recycleState struct {
	mu   sync.Mutex
	days func(context.Context) int
	// swept is when the tombstones were last looked at (guarded by mu).
	swept time.Time
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
	if err := m.evict(ctx, id); err != nil {
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
	var res store.RecycledSpace
	err := m.opt.Store.Tx(ctx, func(q store.Q) error {
		var err error
		res, err = store.RecycleSpaceRows(ctx, q, sc, store.RecycleSpaceIn{ItemID: page.NewID(now), SpaceID: id, By: o.By, ByName: o.ByName,
			Reason: o.Reason, Parent: o.Parent, Dirs: dirs, Now: now})
		return err
	})
	if err != nil {
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
	for _, d := range it.Dirs {
		dst := filepath.Join(base, d)
		src := m.livePath(it.SpaceID, d)
		switch {
		case pathExists(dst):
			// moved already; whatever is at src is another space's business
		case !pathExists(src):
			m.log.Error("a deleted space has no directory to move to the recycle bin", "space", it.SpaceID, "item", it.ID, "dir", d)
		default:
			if err := os.Rename(src, dst); err != nil {
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
		if err := os.Rename(filepath.Join(m.RecycleDir(it.ID), d), m.livePath(it.SpaceID, d)); err != nil {
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
	for _, d := range it.Dirs {
		src, dst := m.livePath(it.SpaceID, d), filepath.Join(base, d)
		if pathExists(src) && !pathExists(dst) {
			if err := os.Rename(src, dst); err != nil {
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
	it, err := store.GetRecycleItem(ctx, m.opt.Store.Q(), sc, itemID, false)
	if err != nil {
		return nil, mapItemErr(err)
	}
	if err := store.SetRecycleState(ctx, m.opt.Store.Q(), it.ID, store.RecycleReady, store.RecyclePurging, m.opt.Now()); err != nil {
		return nil, mapItemErr(err)
	}
	if err := m.finishPurge(ctx, it); err != nil {
		return it, err
	}
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
		before := m.opt.Now().Add(-time.Duration(m.RecycleDays(ctx)) * 24 * time.Hour)
		it, ok, err := store.NextExpiredRecycle(ctx, m.opt.Store.Q(), before)
		if err != nil {
			return n, err
		}
		if !ok {
			return n, nil
		}
		if err := store.SetRecycleState(ctx, m.opt.Store.Q(), it.ID, store.RecycleReady, store.RecyclePurging, m.opt.Now()); err != nil {
			if errors.Is(err, store.ErrBusyItem) || errors.Is(err, store.ErrNotFound) {
				continue // somebody else has it: the next one
			}
			return n, err
		}
		if err := m.finishPurge(ctx, it); err != nil {
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
// 0.
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
		switch it.State {
		case store.RecycleMoving:
			if err := m.moveToRecycle(ctx, it); err != nil {
				m.log.Warn("could not finish moving a recycled space", "item", it.ID, "error", err)
				continue
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
				continue
			}
			m.log.Warn("a purge was cut short; it is finished", "item", it.ID)
		}
	}
	return nil
}

// FinishRecycle finishes what a failed move or a crash left in the recycle bin:
// what a start does, for a caller that wants it now.
func (m *Manager) FinishRecycle(ctx context.Context) error { return m.recoverRecycle(ctx, 0) }

// sweepTombstonesDaily deletes the rows of purged people that nothing refers to
// any more, once a day.
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
}

// StepHook runs the test hook of the recycle bin at a named step of another
// action that has crash points of its own (deleting a person); an error it
// returns stops there as a crash would (tests).
func (m *Manager) StepHook(step string) error { return m.recycleStep(step) }
