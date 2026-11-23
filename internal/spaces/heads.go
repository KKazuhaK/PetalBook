package spaces

import (
	"context"

	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Heads for the offline clients (milestone 4.1): the change cursor each space
// is at, answered from memory, so that a device with many spaces can ask once a
// minute and read changes only where something changed. Nothing here opens a
// space, pulls a remote or touches the disk: a space that is open answers its
// own head (an atomic read), a space that was closed answers the head it had
// when it closed (remembered by closing, bounded by the number of spaces), and
// a space that has not been open since the server started has no head to give
// ("" : the client asks for its changes, which opens it).

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
	// Head is the change cursor ("" when the server has not had the space open
	// since it started).
	Head string
	// Disabled: an administrator has switched the space off.
	Disabled bool
}

// Heads returns, for each of the spaces ids that the user is a member of, what
// they may do there and the head it is at. A space the user is no member of is
// not in the answer. The membership comes from the cached list (one query on a
// miss, as everywhere), the head from memory.
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
		if h.Head == "" {
			h.Head = m.heads[id]
		}
		out[id] = h
	}
	return out, nil
}
