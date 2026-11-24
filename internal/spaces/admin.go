package spaces

// What an administrator may do to a space they are not a member of: switch it
// off, hand it to someone else, delete it (into the recycle bin, see
// recycle.go). None of these opens the space for its content, and none deletes
// a file.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// DataDir is where spaces, their caches and the recycle bin live.
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
func (m *Manager) adminSpace(ctx context.Context, sc store.Scope, id string) (*store.SpaceInfo, error) {
	if m.single != nil || !page.ValidID(id) {
		return nil, ErrNotFound
	}
	sp, err := store.GetSpace(ctx, m.opt.Store.Q(), sc, id)
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
func (m *Manager) SetDisabled(ctx context.Context, sc store.Scope, id string, disabled bool) error {
	if _, err := m.adminSpace(ctx, sc, id); err != nil {
		return err
	}
	if err := m.loadDisabled(ctx); err != nil {
		return err
	}
	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	if err := store.SetSpaceDisabled(ctx, m.opt.Store.Q(), sc, id, disabled); err != nil {
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
func (m *Manager) Transfer(ctx context.Context, sc store.Scope, id, userID string) error {
	sp, err := m.adminSpace(ctx, sc, id)
	if err != nil {
		return err
	}
	if sp.Personal {
		return ErrPersonalSpace
	}
	u, err := store.GetUser(ctx, m.opt.Store.Q(), store.InTenant(sp.TenantID), userID)
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
	var before, after store.Touched
	err = m.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.LockSpace(ctx, q, id); err != nil {
			return err
		}
		if before, err = store.SpaceCountedFor(ctx, q, id); err != nil {
			return err
		}
		if touched, err = store.TransferOwnership(ctx, q, id, u.ID, m.opt.Now()); err != nil {
			return err
		}
		after, err = store.SpaceCountedFor(ctx, q, id)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	m.MembershipChanged(touched...)
	m.usageMoved(ctx, before, after)
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

func (m *Manager) rel(p string) string {
	r, err := filepath.Rel(m.opt.DataDir, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}
