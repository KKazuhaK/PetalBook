package spaces

import (
	"context"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// syncNotice tells the owners of a space that its remote stopped working (it
// needs attention: the credential, a rewritten history, a refused push) or
// works again. A network that is only unreachable ("offline") is not told: it
// comes back by itself, and a notice for each night offline would only train
// people to ignore the bell. prev is the state before this one ("" when it was
// not known).
func (m *Manager) syncNotice(id, prev string, st space.RemoteStatus) {
	if m.single != nil || prev == "" || prev == st.State {
		return
	}
	broken := func(s string) bool { return s == space.RemoteError || s == space.RemoteRewritten }
	var event string
	switch {
	case broken(st.State):
		event = "failed"
	case st.State == space.RemoteOK && broken(prev):
		event = "ok"
	default:
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	q := m.opt.Store.Q()
	name := id
	if mt, err := m.metaFor(ctx, id); err == nil {
		name = mt.name
	}
	members, err := store.ListMembers(ctx, q, id)
	if err != nil {
		m.log.Warn("could not find the owners to tell about a remote", "space", id, "error", err)
		return
	}
	now := m.opt.Now()
	for _, mb := range members {
		if mb.Role != "owner" {
			continue
		}
		err := store.NotifyUser(ctx, q, mb.UserID, store.NotifySync, id, map[string]string{"space": name, "event": event, "state": st.State, "kind": st.Kind}, now)
		if err != nil {
			m.log.Warn("could not write a notification", "space", id, "error", err)
		}
	}
}
