package spaces

// Deleting and restoring a person (milestone 3.5, docs/api.md "Deleting a
// user"). The order of a deletion is the API's (internal/api/users_delete.go): the
// person is disabled first, then each transfer and each membership removed is a
// transaction of its own, then their spaces are recycled one by one under the
// marker store.UserPendingMarker, and the last step (FinishUserDeletion) makes
// the item and marks the person in one transaction. A crash anywhere leaves a
// disabled person with fewer spaces and some items waiting under the marker,
// never a space without an owner; the deletion can be asked again and goes on
// from there. Purging deletes the person's data and leaves a tombstone.

import (
	"context"
	"errors"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// LeaveSpace removes a person from a space they do not own alone: nothing is
// removed (store.ErrLastOwnerOfSpace) when they are its only owner.
func (m *Manager) LeaveSpace(ctx context.Context, spaceID, userID string) error {
	var before, after store.Touched
	err := m.opt.Store.Tx(ctx, func(q store.Q) error {
		var err error
		before, after, err = store.LeaveSpace(ctx, q, spaceID, userID)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	m.MembershipChanged(userID)
	m.usageMoved(ctx, before, after)
	return nil
}

// FinishUserDeletion is the last step of deleting a person (see the file's
// comment); it tells the caches of the people it reached.
func (m *Manager) FinishUserDeletion(ctx context.Context, sc store.Scope, in store.FinishUserDeletionIn) (*store.RecycleItem, error) {
	in.Now = m.opt.Now()
	var item *store.RecycleItem
	var moved [][2]store.Touched
	err := m.opt.Store.Tx(ctx, func(q store.Q) error {
		moved = moved[:0]
		// A membership that slipped in after the plan (made before the person
		// was disabled) is left here, in the transaction that marks them, so a
		// deleted person is never in a live space; one they would leave a space
		// without an owner of refuses the step (ErrLastOwnerOfSpace: asked
		// again, the deletion plans that space).
		left, err := store.ListUserSpaces(ctx, q, in.UserID)
		if err != nil {
			return err
		}
		for _, sp := range left {
			before, after, err := store.LeaveSpace(ctx, q, sp.ID, in.UserID)
			if err != nil {
				return err
			}
			moved = append(moved, [2]store.Touched{before, after})
			in.Left = append(in.Left, store.UserLeft{Space: sp.ID, Name: sp.Name, Role: sp.Role})
		}
		item, err = store.FinishUserDeletion(ctx, q, sc, in)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	for _, mv := range moved {
		m.usageMoved(ctx, mv[0], mv[1])
	}
	m.MembershipChanged(in.UserID)
	m.forgetAuthor(in.UserID)
	m.usageAdded(ctx, store.Touched{TenantID: item.TenantID})
	if u, err := store.GetUserAny(ctx, m.opt.Store.Q(), in.UserID); err == nil {
		m.UserChanged(ctx, *u)
	}
	return item, nil
}

// UserRestored is what restoring a person did.
type UserRestored struct {
	User      store.User
	Spaces    []string
	Skipped   []store.RestoreSkipped
	Transfers []store.UserTransfer
	Left      []store.UserLeft
}

// RestoreUser brings a person back from the recycle bin, with the spaces that
// were recycled with them (those that can be: one that cannot is left in the bin
// and listed). The transfers and the memberships that were removed are not undone
// (the heirs may have changed things since): they are listed. Revoked tokens and
// grants stay revoked.
func (m *Manager) RestoreUser(ctx context.Context, sc store.Scope, itemID string) (*UserRestored, error) {
	it, err := store.GetRecycleItem(ctx, m.opt.Store.Q(), sc, itemID, false)
	if err != nil {
		return nil, mapItemErr(err)
	}
	if it.Kind != store.RecycleUser {
		return nil, ErrNotFound
	}
	if err := store.SetRecycleState(ctx, m.opt.Store.Q(), it.ID, store.RecycleReady, store.RecycleRestoring, m.opt.Now()); err != nil {
		return nil, mapItemErr(err)
	}
	if err := m.recycleStep("restore-user:marked"); err != nil {
		return nil, err
	}
	var out store.UserRestoreOutcome
	err = m.opt.Store.Tx(ctx, func(q store.Q) error {
		var err error
		out, err = store.RestoreUserRows(ctx, q, it, m.opt.Now())
		return err
	})
	if err != nil {
		store.SetRecycleState(context.WithoutCancel(ctx), m.opt.Store.Q(), it.ID, store.RecycleRestoring, store.RecycleReady, m.opt.Now())
		return nil, err
	}
	res := &UserRestored{Skipped: out.Skipped, Transfers: out.Transfers, Left: out.Left}
	if err := m.recycleStep("restore-user:person"); err != nil {
		return nil, err
	}
	// The spaces that went with the person, now that the person is there for the
	// personal one to belong to.
	children, err := store.UserSpaceItems(ctx, m.opt.Store.Q(), it.ID)
	if err != nil {
		return res, err
	}
	for i := range children {
		child := &children[i]
		if child.State != store.RecycleReady {
			res.Skipped = append(res.Skipped, store.RestoreSkipped{Reason: "space " + child.SpaceID + " is not at rest"})
			continue
		}
		r, err := m.restoreSpaceItem(ctx, child, "", true)
		if err != nil {
			res.Skipped = append(res.Skipped, store.RestoreSkipped{Reason: "space " + child.SpaceID + ": " + err.Error()})
			continue
		}
		res.Spaces = append(res.Spaces, r.SpaceID)
		res.Skipped = append(res.Skipped, r.Skipped...)
	}
	if err := m.opt.Store.Tx(ctx, func(q store.Q) error {
		// What could not be restored stays in the bin as an item of its own.
		if _, err := q.Exec(ctx, `UPDATE recycle_items SET parent = '' WHERE parent = ?`, it.ID); err != nil {
			return err
		}
		return store.DeleteUserItem(ctx, q, it.ID)
	}); err != nil {
		return res, err
	}
	u, err := store.GetUser(ctx, m.opt.Store.Q(), store.AllTenants(), it.UserID)
	if err != nil {
		return res, err
	}
	res.User = *u
	m.MembershipChanged(it.UserID)
	m.UserChanged(ctx, *u)
	m.usageAdded(ctx, store.Touched{TenantID: it.TenantID, UserID: it.UserID})
	m.log.Info("person restored", "user", it.UserID, "item", it.ID)
	return res, nil
}

// purgeUserItem is what purging a person's item does: the spaces recycled with
// them are purged first (each is an item of its own at rest), then their data,
// leaving the tombstone. Only this item's own spaces go (never those waiting
// under the person's marker, which a later deletion holds), and the person's
// data only while this item is the one that holds them (users.recycle_item): a
// stale item (a restore of it was cut short) leaves a person who is back, or
// whom a later item holds, as they are.
func (m *Manager) purgeUserItem(ctx context.Context, it *store.RecycleItem) error {
	children, err := store.UserSpaceItems(ctx, m.opt.Store.Q(), it.ID)
	if err != nil {
		return err
	}
	for i := range children {
		c := &children[i]
		if err := store.SetRecycleState(ctx, m.opt.Store.Q(), c.ID, store.RecycleReady, store.RecyclePurging, m.opt.Now()); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if c.State != store.RecyclePurging {
				return err
			}
		}
		if err := m.finishPurge(ctx, c); err != nil {
			return err
		}
	}
	if err := m.recycleStep("purge-user:spaces"); err != nil {
		return err
	}
	err = m.opt.Store.Tx(ctx, func(q store.Q) error { return store.PurgeUserData(ctx, q, it.UserID, it.ID, m.opt.Now()) })
	if err != nil && !errors.Is(err, store.ErrUserNotDeleted) {
		return err
	}
	m.forgetAuthor(it.UserID)
	return nil
}

// SweepTombstones deletes the rows of purged people that nothing refers to
// (the daily job; see store.SweepTombstones).
func (m *Manager) SweepTombstones(ctx context.Context) (int64, error) {
	return store.SweepTombstones(ctx, m.opt.Store.Q(), 500)
}

// tombstoneEvery is how often the sweep runs.
const tombstoneEvery = 24 * time.Hour
