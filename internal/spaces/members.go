package spaces

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"petalbook.app/server/internal/perm"
	"petalbook.app/server/internal/store"
)

// Members are who belong to a space and in what role. Every change checks the
// caller's permission itself (the route asks for it too) and, once it is
// committed, drops the membership cache of exactly the people it touched, so the
// next request of a removed member is refused rather than served from memory.

func (m *Manager) needMembers(h *Handle) error {
	if m.single != nil {
		return ErrNotFound
	}
	if !h.Info.Perms.Has(perm.Members) {
		return ErrForbidden
	}
	return nil
}

func checkRole(role string) error {
	if !perm.ValidRole(role) {
		return fmt.Errorf("%w: a role is one of %s", ErrInvalid, strings.Join(perm.RoleNames(), ", "))
	}
	return nil
}

// Members lists the direct members of the open space, owners first.
func (m *Manager) Members(ctx context.Context, h *Handle) ([]store.Member, error) {
	if m.single != nil {
		return nil, ErrNotFound
	}
	return store.ListMembers(ctx, m.opt.Store.Q(), h.Info.ID)
}

// AddMember makes the enabled user with this username a member in role.
func (m *Manager) AddMember(ctx context.Context, h *Handle, username, role string) (*store.Member, error) {
	if err := m.needMembers(h); err != nil {
		return nil, err
	}
	if err := checkRole(role); err != nil {
		return nil, err
	}
	if h.Info.Personal {
		return nil, ErrPersonal
	}
	u, err := store.GetUserByUsername(ctx, m.opt.Store.Q(), asciiLower(strings.TrimSpace(username)))
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.Disabled) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	now := m.opt.Now()
	// Under the space lock like every other change of its members, so it
	// cannot interleave with an ownership transfer or a removal.
	var before, after store.Touched
	err = m.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.LockSpace(ctx, q, h.Info.ID); err != nil {
			return err
		}
		if before, err = store.SpaceCountedFor(ctx, q, h.Info.ID); err != nil {
			return err
		}
		if err := store.AddMember(ctx, q, h.Info.ID, u.ID, role, now); err != nil {
			return err
		}
		after, err = store.SpaceCountedFor(ctx, q, h.Info.ID)
		return err
	})
	if err != nil {
		if store.IsUnique(err) {
			return nil, ErrAlreadyMember
		}
		if errors.Is(err, store.ErrNotFound) {
			// The statement joins the space and the user on one tenant: a person
			// of another tenant is, as ever, a username nobody has.
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	m.MembershipChanged(u.ID)
	m.usageMoved(ctx, before, after)
	return &store.Member{UserID: u.ID, Username: u.Username, Name: u.Name, Role: role, Created: now}, nil
}

// SetMemberRole changes a member's role. The last owner cannot be demoted.
func (m *Manager) SetMemberRole(ctx context.Context, h *Handle, userID, role string) (*store.Member, error) {
	if err := m.needMembers(h); err != nil {
		return nil, err
	}
	if err := checkRole(role); err != nil {
		return nil, err
	}
	var out *store.Member
	var before, after store.Touched
	err := m.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.LockSpace(ctx, q, h.Info.ID); err != nil {
			return err
		}
		var err error
		if before, err = store.SpaceCountedFor(ctx, q, h.Info.ID); err != nil {
			return err
		}
		cur, err := store.MemberRole(ctx, q, h.Info.ID, userID)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotMember
		}
		if err != nil {
			return err
		}
		if cur != role {
			if err := m.keepOneOwner(ctx, q, h.Info.ID, userID, cur); err != nil {
				return err
			}
			if err := store.SetMemberRole(ctx, q, h.Info.ID, userID, role); err != nil {
				return err
			}
		}
		if after, err = store.SpaceCountedFor(ctx, q, h.Info.ID); err != nil {
			return err
		}
		out, err = store.GetMember(ctx, q, h.Info.ID, userID)
		return err
	})
	if err != nil {
		return nil, err
	}
	m.MembershipChanged(userID)
	m.usageMoved(ctx, before, after)
	return out, nil
}

// RemoveMember removes a member, or lets a member leave (userID is the caller's
// own): leaving needs no more than being a member, and does not open the space
// (h may come from OpenMembership), so it works in a disabled one too. The last
// owner who can sign in cannot go.
func (m *Manager) RemoveMember(ctx context.Context, actor store.User, h *Handle, userID string) error {
	if m.single != nil {
		return ErrNotFound
	}
	if userID != actor.ID || h.Info.ViaAdmin {
		if err := m.needMembers(h); err != nil {
			return err
		}
	}
	var before, after store.Touched
	err := m.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.LockSpace(ctx, q, h.Info.ID); err != nil {
			return err
		}
		var err error
		if before, err = store.SpaceCountedFor(ctx, q, h.Info.ID); err != nil {
			return err
		}
		cur, err := store.MemberRole(ctx, q, h.Info.ID, userID)
		if errors.Is(err, store.ErrNotFound) {
			if userID == actor.ID {
				// Reached only through groups: there is no membership of one's own to leave.
				gs, gerr := store.UserSpaceGroups(ctx, q, h.Info.ID, userID)
				if gerr != nil {
					return gerr
				}
				if len(gs) > 0 {
					names := make([]string, len(gs))
					for i, g := range gs {
						names[i] = g.Name
					}
					slices.Sort(names)
					return &ViaGroupError{Groups: names}
				}
			}
			return ErrNotMember
		}
		if err != nil {
			return err
		}
		if err := m.keepOneOwner(ctx, q, h.Info.ID, userID, cur); err != nil {
			return err
		}
		if err := store.RemoveMember(ctx, q, h.Info.ID, userID); err != nil {
			return err
		}
		after, err = store.SpaceCountedFor(ctx, q, h.Info.ID)
		return err
	})
	if err != nil {
		return err
	}
	m.MembershipChanged(userID)
	m.usageMoved(ctx, before, after)
	return nil
}

// keepOneOwner refuses to take the owner role away (as demotion or removal)
// from userID, whose role is role, when no other owner who can sign in would
// be left: a disabled user cannot be the one remaining owner. Call it inside a
// transaction that holds LockSpace.
func (m *Manager) keepOneOwner(ctx context.Context, q store.Q, spaceID, userID, role string) error {
	if role != perm.Owner {
		return nil
	}
	n, err := store.CountOtherEnabledOwners(ctx, q, spaceID, userID)
	if err != nil {
		return err
	}
	if n < 1 {
		return ErrLastOwner
	}
	return nil
}

// asciiLower folds only A-Z, as usernames are stored.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}
