package spaces

import (
	"context"
	"errors"

	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/space"
)

// ErrNoPersonalSpace: the person has no personal space of their own to open.
var ErrNoPersonalSpace = errors.New("no personal space")

// ErrPersonalAway: the person's personal space is switched off or has no files
// on the server right now (evicted), so a file of it cannot be read or written.
var ErrPersonalAway = errors.New("the personal space is not available right now")

// OpenPersonal opens the person's own personal space (the one the person is the
// owner of: no one else's, whoever asks) for a read or write of a file of it.
// The caller releases it. A space that is evicted is not restored for this: it
// is ErrPersonalAway.
func (m *Manager) OpenPersonal(ctx context.Context, userID string) (*space.Space, func(), error) {
	l, err := m.list(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	for _, in := range l {
		if !in.Personal || in.DirectRole != perm.Owner {
			continue
		}
		if m.loadDisabled(ctx) == nil {
			m.mu.Lock()
			off := m.disabled[in.ID]
			m.mu.Unlock()
			if off {
				return nil, nil, ErrPersonalAway
			}
		}
		sp, release, err := m.acquireIndex(ctx, in.ID)
		if err != nil {
			return nil, nil, err
		}
		if sp.Evicted() {
			release()
			return nil, nil, ErrPersonalAway
		}
		return sp, release, nil
	}
	return nil, nil, ErrNoPersonalSpace
}
