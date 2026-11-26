package spaces

// The recycle bin (milestone 3.5, docs/api.md "The recycle bin"). Deleting a
// space never deletes a byte: its rows move into one recycle item, in one
// transaction (internal/store/recycle.go), and its directories move to
// DATA/recycle/<item>/space and /cache. Nothing is deleted until the item is
// purged, after recycle.days, by the job of this file.
//
// The order of each action is the order that makes a crash harmless, and what a
// start does about each point is written next to it. "rows" is the transaction
// that moves the records. Every action is a job on the runner (internal/jobs):
// recycle.move, recycle.restore and recycle.purge, each with the lock key
// recycle:<item> (one at a time per item), and the item is held by it
// (recycle_items.job_id; the item is at rest when that is empty). A request that
// needs the outcome waits for its job; a job a failure or a crash left is taken
// up again at the start (the kind's Recover) and by the hourly look (RunRecycle).
//
// Recycling:
//
//	1. close the space (what it holds is committed) and mark it disabled in
//	   memory, so that nothing opens it again;
//	2. rows: the item is made (held by its move job, with the list of
//	   directories that exist), the live rows are deleted. A crash before it
//	   changes nothing; the transaction is one commit.
//	3. the job moves DATA/spaces/<id> and then DATA/cache/<id> into
//	   DATA/recycle/<item>/. A crash here leaves the item held by a job that is
//	   not running: the move is taken up again (it moves what is still in its old
//	   place). A directory that is in neither place is reported and left; the item
//	   still holds every record.
//	4. the job puts the item at rest.
//
// Restoring (only an item at rest is restored):
//
//	1. the restore job holds the item (one holder at a time);
//	2. move the directories back. A crash after this and before 3 leaves the
//	   directories at the live places and no rows: Recover (a crash) and the
//	   hourly look (a failure) move them back and the item is at rest again.
//	3. rows: the live rows are inserted and the item deleted, one transaction.
//	   A failure of it (an owner is needed, a space with the id exists) moves
//	   the directories back and the item is at rest again.
//
// Purging: the purge job holds the item (so that a restore cannot start), deletes
// the directory, deletes the item. A crash or a failure leaves the item held by
// a job that is not running, and the purge is taken up again.
//
// A restore's last transaction deletes the item only if its own job still holds
// it, and puts nothing back otherwise. A move across filesystems is a copy and a
// delete (movedir.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"petalbook.app/server/internal/jobs"
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
	// ended is told of every recycle job that has ended (guarded by mu).
	ended func(ctx context.Context, j *store.Job)
}

// SetRecycleEnded gives the manager the function told when a recycle job has
// ended (the server audits the purges that people asked for).
func (m *Manager) SetRecycleEnded(f func(ctx context.Context, j *store.Job)) {
	m.recycle.mu.Lock()
	m.recycle.ended = f
	m.recycle.mu.Unlock()
}

// PurgeResult is what a purge job's result holds once it is done: the item as
// it was, whose row is gone.
type PurgeResult struct {
	Kind     string `json:"kind"`
	SpaceID  string `json:"space,omitempty"`
	UserID   string `json:"user,omitempty"`
	Name     string `json:"name"`
	Bytes    int64  `json:"bytes"`
	Reason   string `json:"reason,omitempty"`
	TenantID string `json:"tenant"`
}

// PurgeResultOf reads the result of a purge job.
func PurgeResultOf(j *store.Job) (r PurgeResult) {
	_ = json.Unmarshal(j.Result, &r)
	return r
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
	return m.recycleLocked(ctx, sc, sp.TenantID, id, o)
}

func (m *Manager) recycleLocked(ctx context.Context, sc store.Scope, tenant, id string, o RecycleOptions) (*store.RecycleItem, error) {
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
	itemID, jobID := page.NewID(now), page.NewID(now)
	var res store.RecycledSpace
	err := m.opt.Store.Tx(ctx, func(q store.Q) error {
		var err error
		res, err = store.RecycleSpaceRows(ctx, q, sc, store.RecycleSpaceIn{ItemID: itemID, JobID: jobID, SpaceID: id, By: o.By, ByName: o.ByName,
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
	// The directories are moved by the item's job. The item is held by it from
	// the transaction above; a crash between that and the job's insert leaves an
	// item whose job is not there, which the hourly look takes up as a move.
	_, done, err := m.runner().EnqueueWait(ctx, store.Job{ID: jobID, TenantID: tenant, Kind: store.JobRecycleMove, Subject: itemID, LockKey: store.RecycleLock(itemID),
		MadeBy: o.By, MadeByName: o.ByName, Params: recycleParamsJSON(RecycleParams{}), Total: 1, BytesTotal: res.Item.Bytes}, nil)
	if err != nil {
		// The records are safe in the item; the move is tried again by the hourly look.
		m.log.Error("a deleted space's directories could not be queued for the recycle bin; they stay where they are and the move is tried again", "space", id, "item", itemID, "error", err)
		return res.Item, nil
	}
	select {
	case r := <-done:
		if r.Err != nil {
			// The records are safe in the item; the move is tried again by the hourly look.
			m.log.Error("a deleted space's directories could not be moved to the recycle bin; they stay where they are and the move is tried again", "space", id, "item", itemID, "error", r.Err)
			return res.Item, nil
		}
	case <-ctx.Done():
		return res.Item, nil // the job goes on
	}
	res.Item.State, res.Item.JobID = store.RecycleReady, ""
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
	return store.ReleaseRecycleItem(ctx, m.opt.Store.Q(), it.ID, it.JobID)
}

// Problems says why an item cannot be restored now ("" list: it can): it is
// not at rest, or a directory it moved is not in the recycle bin any more.
func (m *Manager) Problems(it *store.RecycleItem) []string {
	if it.State != store.RecycleReady {
		return []string{"the item is not at rest (" + it.State + ")"}
	}
	return m.dirProblems(it)
}

func (m *Manager) dirProblems(it *store.RecycleItem) []string {
	var out []string
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

// RecycleParams is what a recycle job's params hold.
type RecycleParams struct {
	// Owner names the owner of a restored space that has none left.
	Owner string `json:"owner,omitempty"`
	// WithPerson: the space's person is being restored with it.
	WithPerson bool `json:"withPerson,omitempty"`
	// IP is the address of the request that asked for it, for its audit row.
	IP string `json:"ip,omitempty"`
}

// RecycleParamsOf reads the params of a recycle job.
func RecycleParamsOf(j *store.Job) (p RecycleParams) {
	_ = json.Unmarshal(j.Params, &p)
	return p
}

func recycleParamsJSON(p RecycleParams) json.RawMessage {
	b, _ := json.Marshal(p)
	return b
}

// holdAndEnqueue adds a job of the kind on an item at rest, in one transaction
// with the item's being held by it: store.ErrBusyItem when a job holds it,
// ErrNotFound when it is gone. The channel gets the job's result.
func (m *Manager) holdAndEnqueue(ctx context.Context, it *store.RecycleItem, kind, by, byName, capacity string, params RecycleParams) (*store.Job, <-chan jobs.Result, error) {
	id := page.NewID(m.opt.Now())
	j, done, err := m.runner().EnqueueWait(ctx, store.Job{ID: id, TenantID: it.TenantID, Kind: kind, Subject: it.ID, LockKey: store.RecycleLock(it.ID), MadeBy: by, MadeByName: byName,
		Capacity: capacity, Params: recycleParamsJSON(params), Total: 1, BytesTotal: it.Bytes},
		func(q store.Q) error { return store.HoldRecycleItem(ctx, q, it.ID, id) })
	switch {
	case errors.Is(err, store.ErrJobLocked):
		return nil, nil, ErrBusyItem
	case err != nil:
		return nil, nil, mapItemErr(err)
	}
	return j, done, nil
}

// wait is the result of a job, or the request's end.
func wait(ctx context.Context, done <-chan jobs.Result) (jobs.Result, error) {
	select {
	case r := <-done:
		return r, nil
	case <-ctx.Done():
		return jobs.Result{}, ctx.Err()
	}
}

// restoreSpaceItem is RestoreSpace for an item already found; withPerson says
// the space's person is being restored with it. The restore is a job; this
// waits for it.
func (m *Manager) restoreSpaceItem(ctx context.Context, it *store.RecycleItem, owner string, withPerson bool) (*RestoreResult, error) {
	if it.Personal && !withPerson {
		return nil, ErrNeedsPerson
	}
	if err := m.loadDisabled(ctx); err != nil {
		return nil, err
	}
	if it.JobID != "" {
		// A job that is running holds it: busy. One that failed left it not at
		// rest, and it cannot be restored until that is finished.
		if j, err := store.GetJob(ctx, m.opt.Store.Q(), store.AllTenants(), it.JobID); err == nil && !store.JobEnded(j.State) {
			return nil, ErrBusyItem
		}
		return nil, &NotRestorable{Problems: m.Problems(it)}
	}
	if p := m.dirProblems(it); len(p) > 0 {
		return nil, &NotRestorable{Problems: p}
	}
	_, done, err := m.holdAndEnqueue(ctx, it, store.JobRecycleRestore, "", "", "", RecycleParams{Owner: owner, WithPerson: withPerson})
	if err != nil {
		return nil, err
	}
	r, err := wait(ctx, done)
	if err != nil {
		return nil, err
	}
	if r.Err != nil {
		return nil, r.Err
	}
	return r.Value.(*RestoreResult), nil
}

// recycleRestoreRun is what a restore job does.
func (m *Manager) recycleRestoreRun(ctx context.Context, j *store.Job, p *jobs.Progress) error {
	it, err := m.heldItem(ctx, j)
	if err != nil || it == nil {
		return err
	}
	if it.Kind == store.RecycleUser {
		res, err := m.restoreUserItem(ctx, it)
		p.Return(res) // what was done is told even when the rest failed
		if err != nil {
			return err
		}
		p.Step(1, it.Bytes)
		return nil
	}
	rp := RecycleParamsOf(j)
	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	if pr := m.dirProblems(it); len(pr) > 0 {
		m.releaseHeld(ctx, it)
		return &NotRestorable{Problems: pr}
	}
	for _, d := range it.Dirs {
		if pathExists(m.livePath(it.SpaceID, d)) {
			m.releaseHeld(ctx, it)
			return ErrRestoreConflict
		}
	}
	undo := func() {
		m.moveBack(it)
		m.releaseHeld(ctx, it)
	}
	if err := m.recycleStep("restore:marked"); err != nil {
		return err
	}
	for _, d := range it.Dirs {
		if err := m.moveDir(filepath.Join(m.RecycleDir(it.ID), d), m.livePath(it.SpaceID, d)); err != nil {
			undo()
			return err
		}
		if err := m.recycleStep("restore:moved-" + d); err != nil {
			return err
		}
	}
	var out store.RestoreOutcome
	err = m.opt.Store.Tx(ctx, func(tq store.Q) error {
		var err error
		out, err = store.RestoreSpaceRows(ctx, tq, it, rp.Owner, rp.WithPerson, m.opt.Now())
		return err
	})
	if err != nil {
		undo()
		if errors.Is(err, store.ErrNotFound) && rp.Owner != "" {
			return ErrUserNotFound
		}
		return err
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
	p.Return(&RestoreResult{SpaceID: it.SpaceID, Name: it.Name, Skipped: out.Skipped})
	p.Step(1, it.Bytes)
	return nil
}

// heldItem is the item a job of the recycle bin works on, read with its job as
// the holder; nil when it is gone or another job holds it now (a job the hourly
// look replaced): there is nothing for this one to do.
func (m *Manager) heldItem(ctx context.Context, j *store.Job) (*store.RecycleItem, error) {
	it, err := store.GetRecycleItem(ctx, m.opt.Store.Q(), store.AllTenants(), j.Subject, false)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil || it.JobID != j.ID {
		return nil, err
	}
	return it, nil
}

// releaseHeld puts an item its job leaves at rest again (the job's context may
// have ended).
func (m *Manager) releaseHeld(ctx context.Context, it *store.RecycleItem) {
	if err := store.ReleaseRecycleItem(context.WithoutCancel(ctx), m.opt.Store.Q(), it.ID, it.JobID); err != nil && !errors.Is(err, store.ErrBusyItem) && !errors.Is(err, store.ErrNotFound) {
		m.log.Error("a recycle item could not be made ready again after a restore failed", "item", it.ID, "error", err)
	}
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
// item at rest is purged (ErrBusyItem otherwise); the purge is a job, which this
// waits for.
func (m *Manager) PurgeItem(ctx context.Context, sc store.Scope, itemID string) (*store.RecycleItem, error) {
	it, done, err := m.StartPurge(ctx, sc, itemID, PurgeBy{})
	if err != nil {
		return nil, err
	}
	r, err := wait(ctx, done)
	if err != nil {
		return it, err
	}
	return it, r.Err
}

// WaitPurges returns once no job of the runner is queued or running: the
// purges StartPurge made are over (tests, and a caller that wants them over).
func (m *Manager) WaitPurges() { _ = m.runner().WaitIdle(context.Background()) }

// PurgeBy says who asks for a purge (and from where, for the audit row its end
// makes); the server's own purge of an item past its days has none.
type PurgeBy struct {
	ID, Name, Capacity, IP string
}

// StartPurge makes a job that purges an item, and returns the item as it is now
// (purging) and the channel that gets the job's result. A purge is paced and may
// take long, longer than a request: it runs on the runner. One that fails or is
// cut short leaves the item held and the hourly look takes it up again.
func (m *Manager) StartPurge(ctx context.Context, sc store.Scope, itemID string, by PurgeBy) (*store.RecycleItem, <-chan jobs.Result, error) {
	it, err := store.GetRecycleItem(ctx, m.opt.Store.Q(), sc, itemID, false)
	if err != nil {
		return nil, nil, mapItemErr(err)
	}
	j, done, err := m.holdAndEnqueue(ctx, it, store.JobRecyclePurge, by.ID, by.Name, by.Capacity, RecycleParams{IP: by.IP})
	if err != nil {
		return nil, nil, err
	}
	it.JobID, it.State = j.ID, store.RecyclePurging
	return it, done, nil
}

// recyclePurgeRun is what a purge job does.
func (m *Manager) recyclePurgeRun(ctx context.Context, j *store.Job, p *jobs.Progress) error {
	it, err := m.heldItem(ctx, j)
	if err != nil || it == nil {
		return err
	}
	if err := m.finishPurge(ctx, it); err != nil {
		m.log.Warn("a purge did not finish; the hourly look takes it up again", "item", it.ID, "error", err)
		return err
	}
	res, _ := json.Marshal(PurgeResult{Kind: it.Kind, SpaceID: it.SpaceID, UserID: it.UserID, Name: it.Name, Bytes: it.Bytes, Reason: it.Reason, TenantID: it.TenantID})
	p.SetResult(res)
	p.Step(1, it.Bytes)
	return nil
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

// PurgeExpired purges, one at a time (each as a job it waits for), the items
// that were deleted more than recycle.days ago. It returns how many it purged.
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
		_, done, err := m.holdAndEnqueue(ctx, it, store.JobRecyclePurge, "", "", "", RecycleParams{})
		if errors.Is(err, ErrBusyItem) || errors.Is(err, ErrNotFound) {
			continue // somebody else has it: the next one
		}
		if err != nil {
			return n, err
		}
		r, err := wait(ctx, done)
		if err != nil {
			return n, err
		}
		if r.Err != nil {
			return n, r.Err
		}
		n++
	}
	return n, ctx.Err()
}

// RunRecycle runs the purge check: it takes up what a failure or a stop left
// at once, and then every RecycleEvery (an hour) purges what is past its date
// and takes up what a failure left again.
func (m *Manager) RunRecycle(ctx context.Context) {
	every := m.opt.RecycleEvery
	if every == 0 {
		every = DefaultRecycleEvery
	}
	if every < 0 {
		return
	}
	m.recycleStuckLogged(ctx)
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
	m.recycleStuckLogged(ctx)
	if _, err := m.PurgeExpired(ctx); err != nil && ctx.Err() == nil {
		m.log.Warn("could not purge the recycle bin", "error", err)
	}
	if err := m.ExpireExports(ctx); err != nil && ctx.Err() == nil {
		m.log.Warn("could not delete the exports that are past their days", "error", err)
	}
	m.sweepTombstonesDaily(ctx)
}

func (m *Manager) recycleStuckLogged(ctx context.Context) {
	if err := m.recycleStuck(ctx); err != nil && ctx.Err() == nil {
		m.log.Warn("could not finish the moves of the recycle bin", "error", err)
	}
}

// recycleStuck takes up what a failure or a stop left: the items held by a job
// that is not queued or running. A move or a purge gets a new job (each waited
// for, spaces before people); a restore is undone, its directories put back.
func (m *Manager) recycleStuck(ctx context.Context) error {
	items, err := store.RecycleStuck(ctx, m.opt.Store.Q())
	if err != nil {
		return err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Kind > items[j].Kind }) // spaces before people
	for i := range items {
		it := &items[i]
		switch it.State {
		case store.RecycleRestoring:
			m.undoRestore(ctx, it)
			m.log.Warn("a restore was cut short; the item is in the recycle bin again", "item", it.ID)
			continue
		}
		kind := store.JobRecycleMove
		if it.State == store.RecyclePurging {
			kind = store.JobRecyclePurge
		}
		id := page.NewID(m.opt.Now())
		old := it.JobID
		_, done, err := m.runner().EnqueueWait(ctx, store.Job{ID: id, TenantID: it.TenantID, Kind: kind, Subject: it.ID, LockKey: store.RecycleLock(it.ID), Total: 1, BytesTotal: it.Bytes},
			func(q store.Q) error { return store.HandRecycleItem(ctx, q, it.ID, old, id) })
		if err != nil {
			if errors.Is(err, store.ErrJobLocked) || errors.Is(err, store.ErrBusyItem) || errors.Is(err, store.ErrNotFound) {
				continue // somebody has it: the next look
			}
			return err
		}
		r, err := wait(ctx, done)
		if err != nil {
			return err
		}
		if r.Err != nil {
			m.log.Warn("could not finish what was left of a recycled item", "item", it.ID, "kind", kind, "error", r.Err)
			continue
		}
		m.log.Warn("a recycle action was cut short; it is finished", "item", it.ID, "kind", kind)
	}
	return nil
}

// undoRestore puts an item a restore left half done back in the recycle bin:
// its directories back, and the item at rest.
func (m *Manager) undoRestore(ctx context.Context, it *store.RecycleItem) {
	m.moveBack(it)
	m.releaseHeld(ctx, it)
}

// FinishRecycle finishes what a failed move or a crash left in the recycle bin:
// what the hourly look does, for a caller that wants it now.
func (m *Manager) FinishRecycle(ctx context.Context) error { return m.recycleStuck(ctx) }

// recycleKind runs the jobs of the recycle bin: a move and a purge are taken up
// again after a crash, a restore is undone.
type recycleKind struct {
	m    *Manager
	kind string
}

func (recycleKind) Class() jobs.Class { return jobs.Recycle }

// Ended tells the server that a recycle job has ended.
func (k recycleKind) Ended(ctx context.Context, j *store.Job) {
	k.m.recycle.mu.Lock()
	f := k.m.recycle.ended
	k.m.recycle.mu.Unlock()
	if f != nil {
		f(ctx, j)
	}
}

func (k recycleKind) Recover(ctx context.Context, j *store.Job) jobs.Outcome {
	if k.kind != store.JobRecycleRestore {
		return jobs.Resume
	}
	if it, err := k.m.heldItem(ctx, j); err == nil && it != nil {
		k.m.undoRestore(ctx, it)
	}
	return jobs.Fail
}

func (k recycleKind) Run(ctx context.Context, j *store.Job, p *jobs.Progress) error {
	switch k.kind {
	case store.JobRecycleRestore:
		return k.m.recycleRestoreRun(ctx, j, p)
	case store.JobRecyclePurge:
		return k.m.recyclePurgeRun(ctx, j, p)
	}
	return k.m.recycleMoveRun(ctx, j, p)
}

// recycleMoveRun is what a move job does: it moves the directories of the recycled
// space into the recycle bin and puts the item at rest.
func (m *Manager) recycleMoveRun(ctx context.Context, j *store.Job, p *jobs.Progress) error {
	it, err := m.heldItem(ctx, j)
	if err != nil || it == nil {
		return err
	}
	if err := m.recycleStep("recycle:rows"); err != nil {
		return err
	}
	if err := m.moveToRecycle(ctx, it); err != nil {
		return err
	}
	p.Step(1, it.Bytes)
	return nil
}

// sweepTombstonesDaily deletes the rows of purged people that nothing refers to
// any more, the jobs that ended more than JobKeep ago and old notifications, once a day.
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
	if n, err := store.PruneJobs(ctx, m.opt.Store.Q(), m.opt.Now().Add(-store.JobKeep)); err != nil && ctx.Err() == nil {
		m.log.Warn("could not delete the jobs that ended long ago", "error", err)
	} else if n > 0 {
		m.log.Info("deleted the jobs that ended long ago", "count", n)
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
