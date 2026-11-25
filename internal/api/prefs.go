package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/spaces"
)

// A person's own preferences (docs/api.md, "Preferences"): the columns and
// widths of their tables, the sort, the tour they have seen. They live in a
// file of the person's own personal space (space.PrefsFile), never in the
// database. The route takes no space: it is always the caller's own personal
// space, so there is nothing to name and nothing to get wrong, and a token or
// an administrator's view cannot reach anyone's. A write is kept in memory
// (what a read says at once) and committed in one batch after prefsQuiet, so a
// person dragging a column makes one small commit in a few minutes and not one
// for each drag.

// DefaultPrefsQuiet is how long a first write waits for others before the
// batch is committed.
const DefaultPrefsQuiet = 3 * time.Minute

const (
	maxPrefKeys     = 100
	maxPrefKeyBytes = 120
	maxPrefBytes    = 32 << 10
)

var prefKey = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,120}$`)

type prefsHub struct {
	mu      sync.Mutex
	pending map[string]*pendingPrefs
	closed  bool
}

type pendingPrefs struct {
	// set holds the values to write; a nil value is a key to remove.
	set   map[string]any
	timer *time.Timer
}

func (h *prefsHub) add(user string, set map[string]any, quiet time.Duration, fire func()) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	if h.pending == nil {
		h.pending = map[string]*pendingPrefs{}
	}
	p := h.pending[user]
	if p == nil {
		p = &pendingPrefs{set: map[string]any{}}
		h.pending[user] = p
		p.timer = time.AfterFunc(quiet, fire)
	}
	for k, v := range set {
		p.set[k] = v
	}
	return true
}

// take removes and returns what is pending for the person.
func (h *prefsHub) take(user string) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.pending[user]
	if p == nil {
		return nil
	}
	p.timer.Stop()
	delete(h.pending, user)
	return p.set
}

// peek is a copy of what is pending for the person.
func (h *prefsHub) peek(user string) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.pending[user]
	if p == nil {
		return nil
	}
	out := make(map[string]any, len(p.set))
	for k, v := range p.set {
		out[k] = v
	}
	return out
}

func (h *prefsHub) stop() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	users := make([]string, 0, len(h.pending))
	for u, p := range h.pending {
		p.timer.Stop()
		users = append(users, u)
	}
	return users
}

func (s *server) prefsQuiet() time.Duration {
	if s.opt.PrefsQuiet > 0 {
		return s.opt.PrefsQuiet
	}
	return DefaultPrefsQuiet
}

// flushPrefs commits what is pending for the person. A failure puts it back
// (older than anything written since) and tries again after the quiet period.
func (s *server) flushPrefs(ctx context.Context, userID string) {
	set := s.prefs.take(userID)
	if len(set) == 0 {
		return
	}
	err := func() error {
		sp, release, err := s.spaces.OpenPersonal(ctx, userID)
		if err != nil {
			return err
		}
		defer release()
		by, err := s.spaces.AuthorByID(ctx, userID)
		if err != nil {
			return err
		}
		return sp.WritePrefs(by, func(cur map[string]any) bool {
			changed := false
			for k, v := range set {
				old, had := cur[k]
				if v == nil {
					if had {
						delete(cur, k)
						changed = true
					}
					continue
				}
				if !had || !jsonEqual(old, v) {
					cur[k] = v
					changed = true
				}
			}
			return changed
		})
	}()
	if err == nil {
		return
	}
	if errors.Is(err, spaces.ErrNoPersonalSpace) {
		return // the person has no place for it; the browser keeps it
	}
	s.log.Warn("could not write the preferences of a person", "error", err)
	if ctx.Err() != nil {
		return
	}
	// Back in, below what came in since.
	for k, v := range s.prefs.peek(userID) {
		set[k] = v
	}
	s.prefs.add(userID, set, s.prefsQuiet(), func() { s.goBg(func(ctx context.Context) { s.flushPrefs(ctx, userID) }) })
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// flushAllPrefs commits everything pending (the server is closing).
func (s *server) flushAllPrefs() {
	for _, u := range s.prefs.stop() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		s.flushPrefs(ctx, u)
		cancel()
	}
}

func (s *server) getPrefs(c *call) {
	ctx := c.r.Context()
	prefs := map[string]any{}
	personal := true
	sp, release, err := s.spaces.OpenPersonal(ctx, c.user().ID)
	switch {
	case errors.Is(err, spaces.ErrNoPersonalSpace), errors.Is(err, spaces.ErrPersonalAway):
		personal = false
	case err != nil:
		s.fail(c.w, err)
		return
	default:
		defer release()
		if prefs, err = sp.ReadPrefs(); err != nil {
			s.log.Warn("could not read the preferences of a person", "error", err)
			prefs, personal = map[string]any{}, false
		}
	}
	// What was written and is not committed yet is what the person set last.
	for k, v := range s.prefs.peek(c.user().ID) {
		if v == nil {
			delete(prefs, k)
		} else {
			prefs[k] = v
		}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Personal bool           `json:"personal"`
		Prefs    map[string]any `json:"prefs"`
	}{personal, prefs})
}

func (s *server) patchPrefs(c *call) {
	var req struct {
		Set map[string]json.RawMessage `json:"set"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	if len(req.Set) == 0 || len(req.Set) > maxPrefKeys {
		writeError(c.w, http.StatusBadRequest, "invalid", "Set from 1 to 100 preferences.")
		return
	}
	set := make(map[string]any, len(req.Set))
	for k, raw := range req.Set {
		if !prefKey.MatchString(k) || strings.Contains(k, "..") {
			writeError(c.w, http.StatusBadRequest, "invalid", "A preference's name is up to 120 letters, digits and . _ : / -.")
			return
		}
		if len(raw) > maxPrefBytes {
			writeError(c.w, http.StatusBadRequest, "invalid", "A preference is at most 32 KiB.")
			return
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "A preference must be JSON.")
			return
		}
		set[k] = v // null removes
	}
	// A person with nowhere to keep them is told, and the browser keeps them.
	_, release, err := s.spaces.OpenPersonal(c.r.Context(), c.user().ID)
	if errors.Is(err, spaces.ErrNoPersonalSpace) || errors.Is(err, spaces.ErrPersonalAway) {
		writeJSON(c.w, http.StatusOK, struct {
			Personal bool `json:"personal"`
		}{false})
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	release()
	uid := c.user().ID
	if !s.prefs.add(uid, set, s.prefsQuiet(), func() { s.goBg(func(ctx context.Context) { s.flushPrefs(ctx, uid) }) }) {
		writeError(c.w, http.StatusServiceUnavailable, "unavailable", "The server is shutting down.")
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Personal bool `json:"personal"`
	}{true})
}
