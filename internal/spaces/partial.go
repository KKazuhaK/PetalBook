package spaces

import (
	"context"
	"sync"
	"time"

	"petalbook.app/server/internal/space"
	"petalbook.app/server/internal/store"
)

// markCache records, in the state database, what an opened space's clone is:
// full or partial. A pinned space that is being made full keeps its record
// until that is done.
func (m *Manager) markCache(id string, sp *space.Space) {
	want := store.CacheFull
	if sp.Partial() {
		want = store.CachePartial
	}
	m.goBg(func() { m.recordCache(id, want) })
}

// recordCache is the write of markCache.
func (m *Manager) recordCache(id, want string) {
	ctx, cancel := context.WithTimeout(m.closedCtx(), 10*time.Second)
	defer cancel()
	st, err := store.GetSpaceStorage(ctx, m.opt.Store.Q(), id)
	if err != nil {
		return
	}
	if st.Pinned && want == store.CachePartial {
		m.makeFullSoon(id)
		if st.Cache == store.CacheFull {
			return
		}
	}
	if st.Cache == want || st.Cache == store.CacheEvicted || st.Cache == store.CacheEvicting {
		return
	}
	m.evictHook("cache:read")
	if err := m.keepCacheState(ctx, id, want); err != nil {
		m.log.Warn("could not record what a space's clone is", "space", id, "error", err)
	}
}

// makeFullSoon fetches the old versions of a pinned partial space in the
// background, once at a time per space. A failure (the remote cannot be
// reached) leaves the space as it is; the next time it is opened tries again.
func (m *Manager) makeFullSoon(id string) {
	m.ev.mu.Lock()
	if m.ev.filling == nil {
		m.ev.filling = map[string]bool{}
	}
	if m.ev.filling[id] {
		m.ev.mu.Unlock()
		return
	}
	m.ev.filling[id] = true
	m.ev.mu.Unlock()
	forget := func() {
		m.ev.mu.Lock()
		delete(m.ev.filling, id)
		m.ev.mu.Unlock()
	}
	if !m.goBg(func() {
		defer forget()
		ctx, cancel := context.WithTimeout(m.closedCtx(), 30*time.Minute)
		defer cancel()
		sp, release, err := m.Acquire(ctx, id) // an evicted space is restored first
		if err != nil {
			m.log.Warn("could not open a pinned space to make its clone full", "space", id, "error", err)
			return
		}
		defer release()
		if err := sp.MakeFull(ctx); err != nil {
			m.log.Warn("could not fetch every version of a pinned space", "space", id, "error", err)
			return
		}
		// Its attachments stay in the cache too.
		if r, _, _ := m.assetRepo(ctx, id); r != nil {
			if _, _, err := r.Refresh(ctx, false); err == nil {
				if err := r.FetchAll(ctx); err != nil {
					m.log.Warn("could not fetch every attachment of a pinned space", "space", id, "error", err)
				}
			}
		}
		if err := m.keepCacheState(ctx, id, store.CacheFull); err != nil {
			m.log.Warn("could not record that a pinned space is full", "space", id, "error", err)
		}
	}) {
		forget()
	}
}

// cacheStates is the stored cache state of every space, in memory: lists of
// spaces show it without asking the database.
type cacheStates struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]string
}

func (m *Manager) loadCacheStates() {
	m.cs.mu.Lock()
	defer m.cs.mu.Unlock()
	if m.cs.loaded {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	set, err := store.SpacesInCacheState(ctx, m.opt.Store.Q(), store.CachePartial, store.CacheEvicted, store.CacheEvicting, store.CacheFull)
	if err != nil {
		m.log.Warn("could not read the cache states of the spaces", "error", err)
		set = map[string]string{}
	}
	m.cs.m, m.cs.loaded = set, true
}

// cacheOf is what the space's copy is, as the API says it.
func (m *Manager) cacheOf(id string, hasRemote bool) string {
	if !hasRemote || m.single != nil {
		return "local"
	}
	m.loadCacheStates()
	m.cs.mu.Lock()
	stored := m.cs.m[id]
	m.cs.mu.Unlock()
	return m.CacheState(stored, true, id)
}

// holdsWhole reports whether the server holds the whole of the space: a local
// space or a full clone, not a partial clone and not an evicted space (nor one
// being evicted). It is what a measurement may count for the quotas.
func (m *Manager) holdsWhole(id string) bool {
	if m.single != nil {
		return true
	}
	m.loadCacheStates()
	m.cs.mu.Lock()
	stored := m.cs.m[id]
	m.cs.mu.Unlock()
	switch stored {
	case store.CachePartial, store.CacheEvicted, store.CacheEvicting:
		return false
	}
	return true
}

// keepCacheState is setCacheState for the record of what an open space's clone
// is (partial or full): it is written only if the space is not evicted or being
// evicted, decided by the database in the same statement, so an eviction marked
// after the space was looked at is never undone.
func (m *Manager) keepCacheState(ctx context.Context, id, state string) error {
	wrote, err := store.SetSpaceCacheKept(ctx, m.opt.Store.Q(), id, state)
	if err != nil || !wrote {
		return err
	}
	m.loadCacheStates()
	m.cs.mu.Lock()
	if cur := m.cs.m[id]; cur != store.CacheEvicted && cur != store.CacheEvicting {
		m.cs.m[id] = state
	}
	m.cs.mu.Unlock()
	return nil
}

// setCacheState records what a space's copy is, in the database and in memory.
func (m *Manager) setCacheState(ctx context.Context, id, state string) error {
	if err := store.SetSpaceCache(ctx, m.opt.Store.Q(), id, state); err != nil {
		return err
	}
	m.loadCacheStates()
	m.cs.mu.Lock()
	if state == "" {
		delete(m.cs.m, id)
	} else {
		m.cs.m[id] = state
	}
	m.cs.mu.Unlock()
	return nil
}
