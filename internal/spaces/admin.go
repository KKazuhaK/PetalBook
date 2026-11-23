package spaces

// What an administrator may do to a space they are not a member of: switch it
// off, hand it to someone else, delete it. None of these opens the space for
// its content, and none deletes a file: a deleted space's directories are moved
// into DataDir/trash, where they can be found again.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// DataDir is where spaces, their caches and the trash live.
func (m *Manager) DataDir() string { return m.opt.DataDir }

// loadDisabled reads the ids of the disabled spaces once. After that the set is
// kept up to date by SetDisabled, and checking a space costs a map lookup.
func (m *Manager) loadDisabled(ctx context.Context) error {
	m.mu.Lock()
	loaded := m.disabled != nil
	m.mu.Unlock()
	if loaded || m.single != nil {
		return nil
	}
	set, err := store.DisabledSpaces(ctx, m.opt.Store.Q())
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.disabled == nil {
		m.disabled = set
	}
	m.mu.Unlock()
	return nil
}

// markDisabled changes the in-memory flag, and reports what it was. The set
// must have been loaded.
func (m *Manager) markDisabled(id string, on bool) (was bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	was = m.disabled[id]
	if on {
		m.disabled[id] = true
	} else {
		delete(m.disabled, id)
	}
	return was
}

// adminSpace checks the id and reads the space's row, for the administration
// calls: ErrNotFound for anything that is not a space.
func (m *Manager) adminSpace(ctx context.Context, id string) (*store.SpaceInfo, error) {
	if m.single != nil || !page.ValidID(id) {
		return nil, ErrNotFound
	}
	sp, err := store.GetSpace(ctx, m.opt.Store.Q(), id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	return sp, err
}

// SyncOf is the sync state of each of the spaces ("local" or the state of the
// remote), read from what the manager already knows: no space is opened.
func (m *Manager) SyncOf(ctx context.Context, ids []string) []string {
	if m.single != nil {
		out := make([]string, len(ids))
		for i := range out {
			out[i] = "local"
		}
		return out
	}
	states := m.syncStates(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = "local"
		if st, ok := states[id]; ok {
			out[i] = st
		}
	}
	return out
}

// evict flushes and closes the space if it is open, waiting up to CloseWait
// for requests that still hold it. The caller has made sure nothing new can
// take hold of it (the space is marked disabled). ErrBusy when holders did not
// let go in time; an error when pending changes could not be committed, in
// which case the space stays open.
func (m *Manager) evict(ctx context.Context, id string) error {
	deadline := time.Now().Add(m.opt.CloseWait)
	for {
		m.mu.Lock()
		e := m.open[id]
		if e == nil {
			m.mu.Unlock()
			return nil
		}
		if c := e.closing; c != nil {
			m.mu.Unlock()
			select {
			case <-c:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if e.refs > 0 || e.sp == nil {
			m.mu.Unlock()
			if time.Now().After(deadline) {
				return ErrBusy
			}
			select {
			case <-time.After(10 * time.Millisecond):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		e.refs++ // pinned while it is flushed, as closeIdle expects
		m.mu.Unlock()
		if !m.closeIdle(id, e) {
			return fmt.Errorf("the pending changes of the space could not be committed: %w", ErrBusy)
		}
	}
}

// SetDisabled switches a space off or on. A disabled space stays in its
// members' lists (with Disabled set) but cannot be opened by anyone. Disabling
// closes it if it is open, after committing what it holds; if requests still
// use it when CloseWait is over it is left to the next trim, which closes a
// disabled space as soon as nobody holds it, and nothing new can start using it.
func (m *Manager) SetDisabled(ctx context.Context, id string, disabled bool) error {
	if _, err := m.adminSpace(ctx, id); err != nil {
		return err
	}
	if err := m.loadDisabled(ctx); err != nil {
		return err
	}
	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	if err := store.SetSpaceDisabled(ctx, m.opt.Store.Q(), id, disabled); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	m.markDisabled(id, disabled)
	if disabled {
		if err := m.evict(ctx, id); err != nil {
			m.log.Warn("a disabled space is still open; it is closed when its last request is done", "space", id, "error", err)
		}
	}
	return nil
}

// Transfer makes the user the owner of the space and every other owner an
// editor. The user must exist and be enabled; a member keeps nothing else
// changed, and a user who was not a member becomes one. A personal space
// belongs to its person and cannot be transferred. The Git author of later
// commits becomes the new owner, in the space if it is open now.
func (m *Manager) Transfer(ctx context.Context, id, userID string) error {
	sp, err := m.adminSpace(ctx, id)
	if err != nil {
		return err
	}
	if sp.Personal {
		return ErrPersonalSpace
	}
	u, err := store.GetUser(ctx, m.opt.Store.Q(), userID)
	if errors.Is(err, store.ErrNotFound) {
		return ErrUserNotFound
	}
	if err != nil {
		return err
	}
	if u.Disabled {
		return ErrUserDisabled
	}
	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	var touched []string
	err = m.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.LockSpace(ctx, q, id); err != nil {
			return err
		}
		touched, err = store.TransferOwnership(ctx, q, id, u.ID, m.opt.Now())
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	m.MembershipChanged(touched...)
	m.forgetAuthor(u.ID)
	m.mu.Lock()
	e := m.open[id]
	m.mu.Unlock()
	if e != nil {
		select {
		case <-e.done:
			if e.sp != nil {
				who := IdentityOf(*u)
				e.sp.SetAuthor(who.Name, who.Email)
			}
		default: // still opening: it reads the owner from the database
		}
	}
	return nil
}

// Deleted says what became of the files of a deleted space.
type Deleted struct {
	// Trashed is where the space's directory went, relative to the data
	// directory; "" if it could not be moved or was not there.
	Trashed string
	// CacheTrashed is the same for its index and journal.
	CacheTrashed string
	// Warnings say what went wrong after the rows were already gone: files
	// that were left where they were, never removed.
	Warnings []string
}

// DeleteSpace removes the space from the database and moves its directories
// into DataDir/trash. It closes the space first (committing what it holds) and
// refuses with ErrBusy if requests still hold it, or if its pending changes
// cannot be committed: nothing is lost then, the space is as it was. A personal
// space cannot be deleted (it goes with its person).
//
// The index and journal in DataDir/cache/<id> are moved too, not deleted: the
// index can be rebuilt, but the journal may hold a change that was never
// committed to Git, and moving costs the same as deleting. Whoever wants the
// space back moves the directory back (and, if they like, the cache with it);
// whoever wants the room back deletes the trash by hand. If a move fails the
// rows are gone already: the failure is logged as an error and reported, and the
// files stay where they are.
func (m *Manager) DeleteSpace(ctx context.Context, id string) (*Deleted, error) {
	sp, err := m.adminSpace(ctx, id)
	if err != nil {
		return nil, err
	}
	if sp.Personal {
		return nil, ErrPersonalSpace
	}
	if err := m.loadDisabled(ctx); err != nil {
		return nil, err
	}
	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	// No new request can take hold of it from here. The flag stays after the
	// delete so a request that was already on its way cannot reopen the
	// directory while it is being moved.
	was := m.markDisabled(id, true)
	if err := m.evict(ctx, id); err != nil {
		m.markDisabled(id, was)
		return nil, err
	}
	stamp := m.opt.Now().UTC().Format("20060102T150405Z")
	trash := filepath.Join(m.opt.DataDir, "trash")
	var members []string
	var draftsFile string
	err = m.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.LockSpace(ctx, q, id); err != nil {
			return err
		}
		// The members' drafts are in the database, and the rows go with the
		// space; restoring the directory from the trash would bring back the
		// pages but not the text people had typed and not saved. So they are
		// written next to it first, and if that fails nothing is deleted.
		var err error
		if draftsFile, err = m.saveDrafts(ctx, q, id, trash, stamp); err != nil {
			return fmt.Errorf("could not keep the drafts of the space: %w", err)
		}
		members, err = store.DeleteSpace(ctx, q, id)
		return err
	})
	if err != nil {
		if draftsFile != "" {
			os.Remove(draftsFile)
		}
		m.markDisabled(id, was)
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	m.MembershipChanged(members...)
	m.setSync(id, "")
	m.assetsGone(id)
	m.noteConnected(id, false)

	res := &Deleted{}
	root, state := m.paths(id)
	move := func(from, name string, must bool) string {
		if _, err := os.Lstat(from); errors.Is(err, os.ErrNotExist) {
			if must {
				res.Warnings = append(res.Warnings, fmt.Sprintf("the directory %s was not there", m.rel(from)))
				m.log.Error("a deleted space had no directory to move to the trash", "space", id, "path", from)
			}
			return ""
		}
		dst := filepath.Join(trash, name)
		err := os.MkdirAll(trash, 0o755)
		if err == nil {
			err = os.Rename(from, dst)
		}
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("could not move %s to the trash: %v; it was left where it is", m.rel(from), err))
			m.log.Error("a deleted space's files could not be moved to the trash; they were left where they are", "space", id, "path", from, "error", err)
			return ""
		}
		return m.rel(dst)
	}
	res.Trashed = move(root, id+"-"+stamp, true)
	res.CacheTrashed = move(state, id+"-"+stamp+".cache", false)
	m.log.Info("space deleted", "space", id, "name", sp.Name, "trashed", res.Trashed, "cache", res.CacheTrashed)
	return res, nil
}

// trashedDraft is one draft as it is kept in <id>-<stamp>.drafts.json.
type trashedDraft struct {
	Space    string          `json:"space"`
	Page     string          `json:"page"`
	UserID   string          `json:"userId"`
	Username string          `json:"username"`
	BaseRev  string          `json:"baseRev"`
	Meta     json.RawMessage `json:"meta"`
	Body     string          `json:"body"`
	Held     bool            `json:"held"`
	Created  time.Time       `json:"created"`
	Updated  time.Time       `json:"updated"`
}

// saveDrafts writes every draft of the space, whoever's and held or not, to
// trash/<id>-<stamp>.drafts.json beside where the directory is about to go,
// and returns the file's path ("" when there are no drafts, and no file). It
// is written whole to a temporary name, synced and renamed, so a file that is
// there is complete.
func (m *Manager) saveDrafts(ctx context.Context, q store.Q, id, trash, stamp string) (string, error) {
	ds, names, err := store.SpaceDrafts(ctx, q, id)
	if err != nil || len(ds) == 0 {
		return "", err
	}
	out := make([]trashedDraft, len(ds))
	for i, d := range ds {
		meta := json.RawMessage(d.Meta)
		if !json.Valid(meta) {
			meta, _ = json.Marshal(d.Meta)
		}
		out[i] = trashedDraft{d.SpaceID, d.PageID, d.UserID, names[i], d.BaseRev, meta, d.Body, d.Held, d.Created.UTC(), d.Updated.UTC()}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(trash, 0o755); err != nil {
		return "", err
	}
	final := filepath.Join(trash, id+"-"+stamp+".drafts.json")
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, final)
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	return final, nil
}

func (m *Manager) rel(p string) string {
	r, err := filepath.Rel(m.opt.DataDir, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}
