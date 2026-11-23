package spaces

import (
	"container/list"
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// The membership cache (1.7, reshaped for groups in 3.2).
//
// Per person it keeps the spaces they can reach, each with one byte of
// permissions: the union of their direct role and the roles of the groups of
// theirs that are members of the space. A request asks only this small sorted
// table; groups are expanded once, in the single query that fills it on a
// miss, and never per request. Names and inboxes are kept apart, once per
// space, so a group that is in hundreds of spaces costs each of its people
// hundreds of (id, byte) pairs and a rename touches nobody's list.
//
// The cache is bounded by entries (those pairs), least recently used first.
// Every change drops exactly the people it touches; listGen, bumped by every
// change, stops a list read before a change from being stored after it.

// DefaultCacheEntries is the default bound: space-person pairs, about 24 bytes
// each, so roughly 50 MB at the limit.
const DefaultCacheEntries = 2_000_000

// maxSpaceMeta bounds the table of space names (spaces are few; this is a
// safety net, not something that is met).
const maxSpaceMeta = 200_000

const (
	cellDirect uint8 = 1 << iota
	cellGroup
)

// spaceCell is what one person can do in one space.
type spaceCell struct {
	id     string
	perms  perm.Set // the union
	direct perm.Set // the direct role's permissions
	flags  uint8
}

type spaceMeta struct {
	id, name, inbox string
	personal        bool
}

type userCells struct {
	user  string
	cells []spaceCell // sorted by id; never changed once stored
	el    *list.Element
}

// accessCache is guarded by Manager.mu.
type accessCache struct {
	users   map[string]*userCells
	lru     *list.List // of *userCells, most recently used first
	entries int
	max     int
	meta    map[string]*spaceMeta
}

func newAccessCache(max int) *accessCache {
	if max <= 0 {
		max = DefaultCacheEntries
	}
	return &accessCache{users: map[string]*userCells{}, lru: list.New(), max: max, meta: map[string]*spaceMeta{}}
}

func (c *accessCache) get(user string) *userCells {
	u := c.users[user]
	if u != nil {
		c.lru.MoveToFront(u.el)
	}
	return u
}

func (c *accessCache) drop(user string) {
	if u := c.users[user]; u != nil {
		c.lru.Remove(u.el)
		c.entries -= len(u.cells)
		delete(c.users, user)
	}
}

func (c *accessCache) put(user string, cells []spaceCell) {
	c.drop(user)
	if len(cells) > c.max {
		return
	}
	for c.entries+len(cells) > c.max && c.lru.Len() > 0 {
		c.drop(c.lru.Back().Value.(*userCells).user)
	}
	u := &userCells{user: user, cells: cells}
	u.el = c.lru.PushFront(u)
	c.users[user] = u
	c.entries += len(cells)
}

func (c *accessCache) putMeta(mt spaceMeta) *spaceMeta {
	if len(c.meta) >= maxSpaceMeta {
		for k := range c.meta {
			delete(c.meta, k)
			break
		}
	}
	p := &mt
	c.meta[mt.id] = p
	return p
}

func findCell(cells []spaceCell, id string) (spaceCell, bool) {
	i := sort.Search(len(cells), func(i int) bool { return cells[i].id >= id })
	if i < len(cells) && cells[i].id == id {
		return cells[i], true
	}
	return spaceCell{}, false
}

func (cl spaceCell) info(mt *spaceMeta) Info {
	return Info{ID: mt.id, Name: mt.name, Role: perm.RoleFor(cl.perms), Perms: cl.perms, DirectRole: perm.RoleFor(cl.direct), ViaGroups: cl.flags&cellGroup != 0,
		Personal: mt.personal, Inbox: mt.inbox}
}

// cellsOf returns a person's cells, from the cache or, on a miss, from the
// one query that reads their direct memberships and their groups' together.
func (m *Manager) cellsOf(ctx context.Context, userID string) ([]spaceCell, error) {
	m.mu.Lock()
	if u := m.cache.get(userID); u != nil {
		m.mu.Unlock()
		return u.cells, nil
	}
	gen := m.listGen
	m.mu.Unlock()

	rows, err := store.ListUserAccess(ctx, m.opt.Store.Q(), userID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.listGen == gen
	byID := map[string]int{}
	cells := make([]spaceCell, 0, len(rows))
	for _, r := range rows {
		i, seen := byID[r.SpaceID]
		if !seen {
			// The canonical string of the id is the table's, shared by everyone's cells.
			id := r.SpaceID
			if mt := m.cache.meta[id]; mt != nil {
				id = mt.id
			} else if cur {
				id = m.cache.putMeta(spaceMeta{id: id, name: r.Name, inbox: r.Inbox, personal: r.Personal}).id
			}
			i = len(cells)
			byID[r.SpaceID] = i
			cells = append(cells, spaceCell{id: id})
		}
		c := &cells[i]
		// A role this build does not know holds nothing: the person sees the
		// space in their list and is refused everywhere in it.
		if r.Direct {
			ps, _ := perm.ForRole(r.Role)
			c.perms |= ps
			c.direct, c.flags = ps, c.flags|cellDirect
		} else {
			ps, _ := perm.ForRole(r.GroupRole)
			c.perms |= ps
			c.flags |= cellGroup
		}
		if cur {
			// Names read now are as current as the list (no change happened meanwhile).
			if mt := m.cache.meta[r.SpaceID]; mt == nil {
				m.cache.putMeta(spaceMeta{id: r.SpaceID, name: r.Name, inbox: r.Inbox, personal: r.Personal})
			}
		}
	}
	sort.Slice(cells, func(i, j int) bool { return cells[i].id < cells[j].id })
	if cur {
		m.cache.put(userID, cells)
	}
	return cells, nil
}

// metaFor returns the name table entry of a space, reading the space when the
// table has none (it was dropped by a change, or evicted).
func (m *Manager) metaFor(ctx context.Context, id string) (*spaceMeta, error) {
	m.mu.Lock()
	if mt := m.cache.meta[id]; mt != nil {
		m.mu.Unlock()
		return mt, nil
	}
	gen := m.listGen
	m.mu.Unlock()
	sp, err := store.GetSpace(ctx, m.opt.Store.Q(), store.AllTenants(), id) // a member's space, already resolved by id
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	mt := spaceMeta{id: sp.ID, name: sp.Name, inbox: sp.Inbox, personal: sp.Personal}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listGen == gen {
		return m.cache.putMeta(mt), nil
	}
	return &mt, nil
}

// spaceChanged says a space was renamed, its inbox changed or it was deleted:
// only the shared name table is touched, no one's list.
func (m *Manager) spaceChanged(id string) {
	m.mu.Lock()
	m.listGen++
	delete(m.cache.meta, id)
	m.mu.Unlock()
}

// infoOf returns the caller's view of a space: the cell, the name table.
func (m *Manager) infoOf(ctx context.Context, userID, spaceID string) (Info, bool, error) {
	cells, err := m.cellsOf(ctx, userID)
	if err != nil {
		return Info{}, false, err
	}
	cl, ok := findCell(cells, spaceID)
	if !ok {
		return Info{}, false, nil
	}
	mt, err := m.metaFor(ctx, spaceID)
	if err != nil {
		return Info{}, false, err
	}
	return cl.info(mt), true, nil
}

// permsOf is infoOf without the names: what a person may do in a space.
func (m *Manager) permsOf(ctx context.Context, userID, spaceID string) (perm.Set, bool, error) {
	cells, err := m.cellsOf(ctx, userID)
	if err != nil {
		return 0, false, err
	}
	cl, ok := findCell(cells, spaceID)
	return cl.perms, ok, err
}

// sortedInfos builds the list shown to a person: the personal space first,
// then by name.
func sortedInfos(l []Info) {
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
}
