package spaces

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/perm"
	"petalbook.app/server/internal/store"
)

// Groups (milestone 3.2): sets of people that are members of shared spaces.
// This file is the groups' side of the membership cache: every change here
// drops exactly the people it touches (see cache.go) and nobody else.

// The limits of docs/api.md, "Limits".
const (
	MaxGroups          = 10_000
	MaxGroupPeople     = 100_000
	MaxUserGroups      = 1_000
	MaxSpaceGroups     = 200
	MaxAddPeople       = 500
	maxGroupNameRunes  = 80
	maxGroupDescRunes  = 500
	defaultGroupsLimit = 50
)

var (
	// ErrGroupNotFound: no such group.
	ErrGroupNotFound = errors.New("no such group")
	// ErrGroupExists: another group has the name.
	ErrGroupExists = errors.New("a group with that name exists already")
	// ErrGroupSynced: a synced group's people come from sign-in only.
	ErrGroupSynced = errors.New("the people of a synced group come from sign-in and cannot be changed by hand")
	// ErrGroupNotSynced: a mapping can only feed a synced group.
	ErrGroupNotSynced = errors.New("only a synced group can be mapped from a provider")
	// ErrGroupLimit wraps what limit was met.
	ErrGroupLimit = errors.New("group limit reached")
	// ErrGroupNotMember: the group is not a member of the space.
	ErrGroupNotMember = errors.New("that group is not a member of this space")
	// ErrGroupMember: the group is a member of the space already.
	ErrGroupMember = errors.New("that group is a member of this space already")
	// ErrNotInGroup: the person is not in the group.
	ErrNotInGroup = errors.New("that user is not in this group")
	// ErrViaGroup: someone who reaches a space only through groups cannot leave it.
	ErrViaGroup = errors.New("you are in this space through a group")
)

// MissingUsersError is the answer to adding people who do not exist.
type MissingUsersError struct{ Missing []string }

func (e *MissingUsersError) Error() string { return "some of those users do not exist" }

// GroupInUseError: a group that is in spaces or mapped cannot be deleted without force.
type GroupInUseError struct{ Spaces, Mappings int }

func (e *GroupInUseError) Error() string {
	return fmt.Sprintf("the group is a member of %d space(s) and mapped by %d provider entries; delete it with force=1 to remove it anyway", e.Spaces, e.Mappings)
}

// ViaGroupError carries the names of the groups.
type ViaGroupError struct{ Groups []string }

func (e *ViaGroupError) Error() string {
	return "you are in this space through " + strings.Join(e.Groups, ", ") + "; ask a space owner to remove the group, or its administrator to remove you from it"
}

func limitErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrGroupLimit, fmt.Sprintf(format, a...))
}

func (m *Manager) needAccounts() error {
	if m.single != nil {
		return ErrNotFound
	}
	return nil
}

// cleanGroupName checks a group's name and description.
func cleanGroupName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxGroupNameRunes || !utf8.ValidString(name) {
		return "", fmt.Errorf("%w: a group name is 1 to %d characters", ErrInvalid, maxGroupNameRunes)
	}
	if strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("%w: a group name cannot contain control characters", ErrInvalid)
	}
	return name, nil
}

func cleanGroupDesc(d string) (string, error) {
	d = strings.TrimSpace(d)
	if utf8.RuneCountInString(d) > maxGroupDescRunes || !utf8.ValidString(d) {
		return "", fmt.Errorf("%w: a group description is at most %d characters", ErrInvalid, maxGroupDescRunes)
	}
	return d, nil
}

// --- the groups themselves (administrators) ----------------------------------------

// CreateGroup makes a group of source "local" or "synced".
func (m *Manager) CreateGroup(ctx context.Context, tenantID, name, description, source string) (*store.Group, error) {
	if err := m.needAccounts(); err != nil {
		return nil, err
	}
	if source != store.GroupLocal && source != store.GroupSynced {
		return nil, fmt.Errorf("%w: source is local or synced", ErrInvalid)
	}
	name, err := cleanGroupName(name)
	if err != nil {
		return nil, err
	}
	if description, err = cleanGroupDesc(description); err != nil {
		return nil, err
	}
	now := m.opt.Now()
	g := store.Group{TenantID: tenantID, Name: name, Description: description, Source: source, Created: now, Updated: now}
	for try := 0; ; try++ {
		g.ID = m.newGroupID(now)
		err = m.opt.Store.Tx(ctx, func(q store.Q) error {
			n, err := store.CountGroups(ctx, q, store.InTenant(tenantID))
			if err != nil {
				return err
			}
			if n >= MaxGroups {
				return limitErr("A tenant has at most %d groups; this one has %d.", MaxGroups, n)
			}
			return store.InsertGroup(ctx, q, g)
		})
		on, unique := store.UniqueOn(err)
		if !unique {
			break
		}
		// The name's index is groups.name_key (SQLite) or groups_name_key_key
		// (PostgreSQL); anything else is the id, which a new one fixes.
		if strings.Contains(on, "name_key") || strings.Contains(on, "tenant_name") || try >= 4 {
			return nil, ErrGroupExists
		}
	}
	if err != nil {
		return nil, err
	}
	return m.Group(ctx, store.AllTenants(), g.ID)
}

// newGroupID is a fresh random id (or the test's).
func (m *Manager) newGroupID(now time.Time) string {
	if m.testID != nil {
		if id := m.testID(); id != "" {
			return id
		}
	}
	return page.NewID(now)
}

// Group returns a group with its counts.
func (m *Manager) Group(ctx context.Context, sc store.Scope, id string) (*store.Group, error) {
	if err := m.needAccounts(); err != nil {
		return nil, err
	}
	g, err := store.GetGroup(ctx, m.opt.Store.Q(), sc, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrGroupNotFound
	}
	return g, err
}

// ListGroups pages the groups (see store.GroupFilter).
func (m *Manager) ListGroups(ctx context.Context, sc store.Scope, f store.GroupFilter) ([]store.Group, string, error) {
	if err := m.needAccounts(); err != nil {
		return nil, "", err
	}
	return store.ListGroups(ctx, m.opt.Store.Q(), m.opt.Store.Dialect(), sc, f)
}

// UpdateGroup renames a group and/or changes its description (nil leaves one
// as it is). It returns the group and the names of the fields that changed.
func (m *Manager) UpdateGroup(ctx context.Context, sc store.Scope, id string, name, description *string) (*store.Group, []string, error) {
	if err := m.needAccounts(); err != nil {
		return nil, nil, err
	}
	cur, err := m.Group(ctx, sc, id)
	if err != nil {
		return nil, nil, err
	}
	newName, newDesc := cur.Name, cur.Description
	var changed []string
	if name != nil {
		if newName, err = cleanGroupName(*name); err != nil {
			return nil, nil, err
		}
		if newName != cur.Name {
			changed = append(changed, "name")
		}
	}
	if description != nil {
		if newDesc, err = cleanGroupDesc(*description); err != nil {
			return nil, nil, err
		}
		if newDesc != cur.Description {
			changed = append(changed, "description")
		}
	}
	if len(changed) > 0 {
		err = store.UpdateGroup(ctx, m.opt.Store.Q(), id, newName, newDesc, m.opt.Now())
		if store.IsUnique(err) {
			return nil, nil, ErrGroupExists
		}
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, ErrGroupNotFound
		}
		if err != nil {
			return nil, nil, err
		}
	}
	g, err := m.Group(ctx, sc, id)
	return g, changed, err
}

// DeleteGroup deletes a group. mappings is how many provider entries map to it
// (the providers' settings are not the database's): without force a group that
// is in spaces or mapped is refused. The group's people lose what it gave them
// at their very next request; nothing else is deleted.
func (m *Manager) DeleteGroup(ctx context.Context, sc store.Scope, id string, force bool, mappings int) (*store.Group, error) {
	if err := m.needAccounts(); err != nil {
		return nil, err
	}
	var g *store.Group
	var people []string
	err := m.opt.Store.Tx(ctx, func(q store.Q) error {
		// Locked before its people are read: no one joins it until this commits.
		if err := store.LockGroup(ctx, q, id); err != nil {
			return err
		}
		var err error
		if g, err = store.GetGroup(ctx, q, sc, id); err != nil {
			return err
		}
		if !force && (g.Spaces > 0 || mappings > 0) {
			return &GroupInUseError{Spaces: g.Spaces, Mappings: mappings}
		}
		if people, err = store.GroupUserIDs(ctx, q, id); err != nil {
			return err
		}
		m.at("group-people-read")
		return store.DeleteGroup(ctx, q, id)
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrGroupNotFound
	}
	if err != nil {
		return nil, err
	}
	m.MembershipChanged(people...)
	return g, nil
}

// GroupPeople pages the people of a group.
func (m *Manager) GroupPeople(ctx context.Context, sc store.Scope, id, query, cursor string, limit int) ([]store.GroupPerson, string, error) {
	if _, err := m.Group(ctx, sc, id); err != nil {
		return nil, "", err
	}
	return store.ListGroupPeople(ctx, m.opt.Store.Q(), m.opt.Store.Dialect(), id, query, cursor, limit)
}

// GroupSpaces pages the spaces a group is a member of.
func (m *Manager) GroupSpaces(ctx context.Context, sc store.Scope, id, cursor string, limit int) ([]store.GroupSpaceRef, string, error) {
	if _, err := m.Group(ctx, sc, id); err != nil {
		return nil, "", err
	}
	return store.ListGroupSpaces(ctx, m.opt.Store.Q(), m.opt.Store.Dialect(), id, cursor, limit)
}

// AddGroupPeople adds people to a local group, all or nothing. It returns how
// many were added, how many were in it already, and the ids of those added.
func (m *Manager) AddGroupPeople(ctx context.Context, sc store.Scope, id string, userIDs []string) (added, already int, ids []string, err error) {
	if err := m.needAccounts(); err != nil {
		return 0, 0, nil, err
	}
	seen := map[string]bool{}
	var want []string
	for _, u := range userIDs {
		if !seen[u] {
			seen[u] = true
			want = append(want, u)
		}
	}
	if len(want) < 1 || len(userIDs) > MaxAddPeople {
		return 0, 0, nil, fmt.Errorf("%w: userIds holds 1 to %d ids", ErrInvalid, MaxAddPeople)
	}
	var fresh []string
	err = m.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.LockGroup(ctx, q, id); err != nil {
			return err
		}
		g, err := store.GetGroup(ctx, q, sc, id)
		if err != nil {
			return err
		}
		if g.Source != store.GroupLocal {
			return ErrGroupSynced
		}
		// The people must be of the group's own tenant: another tenant's person
		// is reported as unknown, whoever asks.
		have, err := store.ExistingUsers(ctx, q, store.InTenant(g.TenantID), want)
		if err != nil {
			return err
		}
		var missing []string
		for _, u := range want {
			if !have[u] {
				missing = append(missing, u)
			}
		}
		if len(missing) > 0 {
			return &MissingUsersError{Missing: missing}
		}
		in, groupsOf, err := store.LocalMembers(ctx, q, id, want)
		if err != nil {
			return err
		}
		for _, u := range want {
			if !in[u] {
				fresh = append(fresh, u)
			}
		}
		already = len(want) - len(fresh)
		if len(fresh) == 0 {
			return nil
		}
		for _, u := range fresh {
			if groupsOf[u]+1 > MaxUserGroups {
				return limitErr("A person is in at most %d groups.", MaxUserGroups)
			}
		}
		n, err := store.CountGroupPeople(ctx, q, id)
		if err != nil {
			return err
		}
		if n+len(fresh) > MaxGroupPeople {
			return limitErr("A group has at most %d people.", MaxGroupPeople)
		}
		return store.AddLocalMembers(ctx, q, id, fresh, m.opt.Now())
	})
	if errors.Is(err, store.ErrNotFound) {
		return 0, 0, nil, ErrGroupNotFound
	}
	if err != nil {
		return 0, 0, nil, err
	}
	m.MembershipChanged(fresh...)
	return len(fresh), already, fresh, nil
}

// RemoveGroupPerson removes a person from a local group.
func (m *Manager) RemoveGroupPerson(ctx context.Context, sc store.Scope, id, userID string) error {
	if err := m.needAccounts(); err != nil {
		return err
	}
	err := m.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.LockGroup(ctx, q, id); err != nil {
			return err
		}
		g, err := store.GetGroup(ctx, q, sc, id)
		if err != nil {
			return err
		}
		if g.Source != store.GroupLocal {
			return ErrGroupSynced
		}
		return store.RemoveLocalMember(ctx, q, id, userID)
	})
	if errors.Is(err, store.ErrNotFound) {
		if _, gerr := m.Group(ctx, sc, id); gerr != nil {
			return gerr
		}
		return ErrNotInGroup
	}
	if err != nil {
		return err
	}
	m.MembershipChanged(userID)
	return nil
}

// FindGroups finds groups by part of the name, for anyone signed in.
func (m *Manager) FindGroups(ctx context.Context, sc store.Scope, query string, limit int) ([]store.GroupRef, error) {
	if err := m.needAccounts(); err != nil {
		return nil, err
	}
	return store.SearchGroups(ctx, m.opt.Store.Q(), m.opt.Store.Dialect(), sc, query, limit)
}

// GroupsOf lists a person's groups.
func (m *Manager) GroupsOf(ctx context.Context, userID string) ([]store.GroupRef, error) {
	if err := m.needAccounts(); err != nil {
		return nil, err
	}
	return store.UserGroups(ctx, m.opt.Store.Q(), userID)
}

// --- groups as members of a space ---------------------------------------------------

// MemberView is a direct member with how groups add to them.
type MemberView struct {
	store.Member
	// ViaGroups: a group of theirs is a member of the space too.
	ViaGroups     bool
	EffectiveRole string
}

// MembersView lists the direct members (owners first) and the groups of the
// open space. Neither is expanded: a space has at most MaxSpaceGroups groups.
func (m *Manager) MembersView(ctx context.Context, h *Handle, userID string) ([]MemberView, []store.SpaceGroup, error) {
	if m.single != nil {
		return nil, nil, ErrNotFound
	}
	q := m.opt.Store.Q()
	l, err := store.ListMembers(ctx, q, h.Info.ID)
	if err != nil {
		return nil, nil, err
	}
	groups, err := store.ListSpaceGroups(ctx, q, h.Info.ID, userID)
	if err != nil {
		return nil, nil, err
	}
	var via map[string][]string
	if len(groups) > 0 {
		if via, err = store.MemberViaGroups(ctx, q, h.Info.ID); err != nil {
			return nil, nil, err
		}
	}
	out := make([]MemberView, len(l))
	for i, mem := range l {
		ps, _ := perm.ForRole(mem.Role)
		for _, r := range via[mem.UserID] {
			gp, _ := perm.ForRole(r)
			ps |= gp
		}
		out[i] = MemberView{Member: mem, ViaGroups: len(via[mem.UserID]) > 0, EffectiveRole: perm.RoleFor(ps)}
	}
	return out, groups, nil
}

// EffectiveMembers pages everyone who can reach the open space, with how.
func (m *Manager) EffectiveMembers(ctx context.Context, h *Handle, query, cursor string, limit int) ([]store.EffectivePerson, string, error) {
	if m.single != nil {
		return nil, "", ErrNotFound
	}
	return store.ListEffective(ctx, m.opt.Store.Q(), m.opt.Store.Dialect(), h.Info.ID, query, cursor, limit)
}

// EffectiveRole is the role whose permissions are the union of a direct role
// ("" for none) and the roles of groups.
func EffectiveRole(direct string, groups []store.SpaceGroup) string {
	ps, _ := perm.ForRole(direct)
	for _, g := range groups {
		gp, _ := perm.ForRole(g.Role)
		ps |= gp
	}
	return perm.RoleFor(ps)
}

// AddSpaceGroup makes a group a member of the open (shared) space.
func (m *Manager) AddSpaceGroup(ctx context.Context, h *Handle, userID, groupID, role string) (*store.SpaceGroup, error) {
	if err := m.needMembers(h); err != nil {
		return nil, err
	}
	if err := checkRole(role); err != nil {
		return nil, err
	}
	if h.Info.Personal {
		return nil, ErrPersonal
	}
	var people []string
	err := m.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.LockSpace(ctx, q, h.Info.ID); err != nil {
			return err
		}
		n, err := store.CountSpaceGroups(ctx, q, h.Info.ID)
		if err != nil {
			return err
		}
		if n >= MaxSpaceGroups {
			return limitErr("A space has at most %d groups.", MaxSpaceGroups)
		}
		// The space first, then the group: the order every change here takes.
		if err := store.LockGroup(ctx, q, groupID); err != nil {
			return err
		}
		if err := store.AddSpaceGroup(ctx, q, h.Info.ID, groupID, role, m.opt.Now()); err != nil {
			return err
		}
		if people, err = store.GroupUserIDs(ctx, q, groupID); err != nil {
			return err
		}
		return viaAdminOwnership(h, slices.Contains(people, userID), role)
	})
	if store.IsUnique(err) {
		return nil, ErrGroupMember
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrGroupNotFound
	}
	if err != nil {
		return nil, err
	}
	m.MembershipChanged(people...)
	return store.GetSpaceGroup(ctx, m.opt.Store.Q(), h.Info.ID, groupID, userID)
}

// SetSpaceGroupRole changes a group's role in the open space. It returns the
// old role too, for the audit row.
func (m *Manager) SetSpaceGroupRole(ctx context.Context, h *Handle, userID, groupID, role string) (*store.SpaceGroup, string, error) {
	if err := m.needMembers(h); err != nil {
		return nil, "", err
	}
	if err := checkRole(role); err != nil {
		return nil, "", err
	}
	var people []string
	var old string
	err := m.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.LockSpace(ctx, q, h.Info.ID); err != nil {
			return err
		}
		// Locked before its people are read: no one joins it until this commits.
		if err := store.LockGroup(ctx, q, groupID); err != nil {
			return err
		}
		var err error
		if old, err = store.SpaceGroupRole(ctx, q, h.Info.ID, groupID); err != nil {
			return err
		}
		if people, err = store.GroupUserIDs(ctx, q, groupID); err != nil {
			return err
		}
		m.at("group-people-read")
		if err := viaAdminOwnership(h, slices.Contains(people, userID), old, role); err != nil {
			return err
		}
		if old != role {
			return store.SetSpaceGroupRole(ctx, q, h.Info.ID, groupID, role)
		}
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", ErrGroupNotMember
	}
	if err != nil {
		return nil, "", err
	}
	if old != role {
		m.MembershipChanged(people...)
	}
	g, err := store.GetSpaceGroup(ctx, m.opt.Store.Q(), h.Info.ID, groupID, userID)
	return g, old, err
}

// RemoveSpaceGroup takes a group out of the open space. It returns the role it had.
func (m *Manager) RemoveSpaceGroup(ctx context.Context, h *Handle, groupID string) (string, error) {
	if err := m.needMembers(h); err != nil {
		return "", err
	}
	var people []string
	var old string
	err := m.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.LockSpace(ctx, q, h.Info.ID); err != nil {
			return err
		}
		// Locked before its people are read: no one joins it until this commits.
		if err := store.LockGroup(ctx, q, groupID); err != nil {
			return err
		}
		var err error
		if old, err = store.SpaceGroupRole(ctx, q, h.Info.ID, groupID); err != nil {
			return err
		}
		if people, err = store.GroupUserIDs(ctx, q, groupID); err != nil {
			return err
		}
		m.at("group-people-read")
		if err := viaAdminOwnership(h, false, old); err != nil {
			return err
		}
		return store.RemoveSpaceGroup(ctx, q, h.Info.ID, groupID)
	})
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrGroupNotMember
	}
	if err != nil {
		return "", err
	}
	m.MembershipChanged(people...)
	return old, nil
}

// --- sync from sign-in ---------------------------------------------------------------

// SyncResult is what a sign-in's sync changed.
type SyncResult struct {
	Added, Removed []string // group ids
	// Limited: some groups were not added because the person is in too many.
	Limited bool
}

// Changed reports whether anything was written.
func (r SyncResult) Changed() bool { return len(r.Added) > 0 || len(r.Removed) > 0 }

// SyncProviderGroups brings the memberships a provider has brought a person to
// want (group id and claim value pairs). It reads those memberships with one
// query, compares, and writes only when something differs; only then is the
// person's cached list dropped. Memberships of other providers and of local
// groups are never touched. version is the provider's groups version the
// sign-in read its mapping with: if the provider has changed or gone since,
// nothing is written (the next sign-in brings the person up to date).
func (m *Manager) SyncProviderGroups(ctx context.Context, userID, provider string, version int64, want []store.SyncedMembership) (SyncResult, error) {
	var res SyncResult
	if m.single != nil {
		return res, nil
	}
	cur, err := store.UserSyncedMemberships(ctx, m.opt.Store.Q(), userID, provider)
	if err != nil {
		return res, err
	}
	var rem, add []store.SyncedMembership
	for _, c := range cur {
		if !slices.Contains(want, c) {
			rem = append(rem, c)
		}
	}
	for _, w := range want {
		if !slices.Contains(cur, w) {
			add = append(add, w)
		}
	}
	if len(rem) == 0 && len(add) == 0 {
		return res, nil
	}
	err = m.opt.Store.Tx(ctx, func(q store.Q) error {
		res = SyncResult{}
		held := map[string]bool{}
		// The provider first (a change of its mapping holds it while it drops
		// what the old mapping brought), then the groups.
		ok, err := store.LockProviderGroups(ctx, q, provider, version)
		if err != nil || !ok {
			return err
		}
		// Every group this touches, locked at once in id order (ApplySynced
		// locks again what it touches, which is already held).
		var touched []string
		for _, c := range append(slices.Clone(rem), add...) {
			touched = append(touched, c.GroupID)
		}
		if err := store.LockGroups(ctx, q, touched); err != nil {
			return err
		}
		if err := store.ApplySynced(ctx, q, userID, provider, rem, nil, m.opt.Now()); err != nil {
			return err
		}
		if len(add) > 0 {
			// What is left of the person's groups once these memberships are gone.
			ids, err := store.UserGroupIDs(ctx, q, userID)
			if err != nil {
				return err
			}
			for _, id := range ids {
				held[id] = true
			}
		}
		var okAdd []store.SyncedMembership
		for _, a := range add {
			if !held[a.GroupID] {
				if len(held) >= MaxUserGroups {
					res.Limited = true
					continue
				}
				held[a.GroupID] = true
			}
			okAdd = append(okAdd, a)
		}
		if err := store.ApplySynced(ctx, q, userID, provider, nil, okAdd, m.opt.Now()); err != nil {
			return err
		}
		res.Added, res.Removed = groupSet(okAdd), groupSet(rem)
		return nil
	})
	if err != nil {
		return SyncResult{}, err
	}
	if len(res.Added) > 0 || len(res.Removed) > 0 {
		m.MembershipChanged(userID)
	}
	return res, nil
}

// groupSet is the distinct group ids of memberships, sorted.
func groupSet(l []store.SyncedMembership) []string {
	var out []string
	for _, m := range l {
		if !slices.Contains(out, m.GroupID) {
			out = append(out, m.GroupID)
		}
	}
	slices.Sort(out)
	return out
}

// DropMapped removes what one claim value of a provider brought (except into
// keepGroup, "" for none) from everyone, at once. It runs in the transaction
// that writes the provider's row, which holds the row against syncs; the
// caller passes the people it returns to MembershipChanged once that commits.
func (m *Manager) DropMapped(ctx context.Context, q store.Q, provider, value, keepGroup string) ([]string, error) {
	if m.single != nil {
		return nil, nil
	}
	return store.RemoveMapped(ctx, q, provider, value, keepGroup)
}

// DropProvider removes every membership a provider brought, in the
// transaction that deletes the provider (see DropMapped).
func (m *Manager) DropProvider(ctx context.Context, q store.Q, provider string) ([]string, error) {
	if m.single != nil {
		return nil, nil
	}
	return store.RemoveProviderMemberships(ctx, q, provider)
}
