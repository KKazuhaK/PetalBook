package spaces

import (
	"context"

	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Heads for the offline clients (milestone 4.1): the change cursor each space
// is at, answered from memory, so that a device with many spaces can ask once a
// minute and read changes only where something changed. Nothing here opens a
// space, pulls a remote or touches the disk: a space that is open answers its
// own head (an atomic read), a space that was closed answers the head it had
// when it closed (remembered by closing, bounded by the number of spaces), and
// a space that has not been open since the server started answers the head its
// index was closed at, read from the index file read-only (about a millisecond,
// once: it is remembered as if the space had closed). An evicted space (3.1)
// keeps its index, closed: it answers like any closed space, or from its
// index-only handle while one is open, and is never opened or restored for it. A space without an index,
// or whose index was not closed cleanly, has no head to give ("": the client
// asks for its changes, which opens it).

// maxRememberedHeads bounds the table of heads of closed spaces (spaces are
// few; this is a safety net).
const maxRememberedHeads = 200_000

// rememberHead keeps the head a space had when it closed. Called with mu held.
func (m *Manager) rememberHead(id, head string) {
	if m.heads == nil {
		m.heads = map[string]string{}
	}
	if len(m.heads) >= maxRememberedHeads {
		for k := range m.heads {
			delete(m.heads, k)
			break
		}
	}
	m.heads[id] = head
}

// openID is the id the open table holds e under.
func openID(open map[string]*opening, e *opening) string {
	for id, o := range open {
		if o == e {
			return id
		}
	}
	return ""
}

// SpaceHead is a space as the heads of the offline sync see it.
type SpaceHead struct {
	// Perms are what the user may do in the space.
	Perms perm.Set
	// Head is the change cursor ("" when there is none to give: see above).
	Head string
	// Disabled: an administrator has switched the space off.
	Disabled bool
}

// Heads returns, for each of the spaces ids that the user is a member of, what
// they may do there and the head it is at. A space the user is no member of is
// not in the answer. The membership comes from the cached list (one query on a
// miss, as everywhere), the head from memory, or once from the closed index.
func (m *Manager) Heads(ctx context.Context, userID string, ids []string) (map[string]SpaceHead, error) {
	out := make(map[string]SpaceHead, len(ids))
	if m.single != nil {
		if userID == store.LocalUserID {
			for _, id := range ids {
				if id == SingleID {
					out[id] = SpaceHead{Perms: perm.All, Head: m.single.Head()}
				}
			}
		}
		return out, nil
	}
	cells, err := m.cellsOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	m.loadDisabled(ctx) // on failure nothing is shown as disabled; Open still refuses
	cold := m.heads0(cells, ids, out, m.indexOnlyHeads(ids))
	if len(cold) == 0 {
		return out, nil
	}
	// Read outside the lock: it touches the disk.
	stored := make(map[string]string, len(cold))
	for _, id := range cold {
		_, state := m.paths(id)
		h, err := space.StoredHead(state)
		if err != nil {
			m.log.Warn("reading the head of a closed space", "space", id, "error", err)
			continue
		}
		stored[id] = h
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, h := range stored {
		if h == "" {
			continue
		}
		e := out[id]
		e.Head = h
		out[id] = e
		// Remembered unless the space was opened, or closed with a newer head, meanwhile.
		if m.open[id] == nil && m.heads[id] == "" {
			m.rememberHead(id, h)
		}
	}
	return out, nil
}

// indexOnlyHeads are the heads of the evicted spaces among ids whose index is
// open for index-only reads (see acquireIndex): their index file says nothing
// while it is open. Nothing is opened or restored here.
func (m *Manager) indexOnlyHeads(ids []string) map[string]string {
	m.ev.mu.Lock()
	defer m.ev.mu.Unlock()
	if len(m.ev.idx) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, id := range ids {
		if e := m.ev.idx[id]; e != nil && !e.stale {
			out[id] = e.sp.Head()
		}
	}
	return out
}

// heads0 fills out from memory and returns the spaces whose head is not there.
func (m *Manager) heads0(cells []spaceCell, ids []string, out map[string]SpaceHead, idx map[string]string) (cold []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		cl, ok := findCell(cells, id)
		if !ok {
			continue
		}
		h := SpaceHead{Perms: cl.perms, Disabled: m.disabled[id]}
		if e := m.open[id]; e != nil {
			select {
			case <-e.done:
				if e.sp != nil {
					h.Head = e.sp.Head()
				}
			default:
			}
		}
		if h.Head == "" && m.open[id] == nil {
			// An evicted space whose index is open for index-only reads.
			h.Head = idx[id]
		}
		if h.Head == "" {
			h.Head = m.heads[id]
			if h.Head == "" && m.open[id] == nil && !h.Disabled {
				cold = append(cold, id)
			}
		}
		out[id] = h
	}
	return cold
}
