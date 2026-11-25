package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/space"
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

// maxPrefsRetries is how many times a flush that failed for a reason that may
// pass (the space is away, the disk was full) is tried again, each time after
// twice the wait before, before what it held is given up.
const maxPrefsRetries = 6

type prefsHub struct {
	mu      sync.Mutex
	pending map[string]*pendingPrefs
	// written counts the flushes of each person that ended, so a write can tell
	// that the file it read is no longer the one beside what is pending.
	written map[string]uint64
	closed  bool
}

// prefVal is a value written and not committed yet; nil is a key to remove.
// seq says which write it is, so a flush takes away only what it wrote.
type prefVal struct {
	v   any
	seq uint64
}

type pendingPrefs struct {
	set   map[string]prefVal
	seq   uint64
	timer *time.Timer
	// flushing: a flush of this person is running. There is never more than
	// one, so two cannot land out of order.
	flushing bool
	// retries counts the flushes in a row that failed and may be tried again.
	retries int
}

// gen is the number of the person's flushes that have ended: read before the
// file, and handed to add, it says whether the file is still the one beside
// what is pending.
func (h *prefsHub) gen(user string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.written[user]
}

// errPrefsStale: a flush ended between the read of the file and add; read it again.
var errPrefsStale = errors.New("the preferences were written meanwhile")

// add keeps the values of a write, after check has accepted them as they would
// be with what is pending (check gets the pending values, nil for a removal).
// gen is what h.gen said before the file check uses was read (any value when
// check is nil): if a flush has ended since, it is errPrefsStale.
func (h *prefsHub) add(user string, set map[string]any, quiet time.Duration, fire func(), gen uint64, check func(pending map[string]any) error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errPrefsClosed
	}
	if check != nil && h.written[user] != gen {
		return errPrefsStale
	}
	if h.pending == nil {
		h.pending = map[string]*pendingPrefs{}
	}
	p := h.pending[user]
	if check != nil {
		cur := map[string]any{}
		if p != nil {
			for k, pv := range p.set {
				cur[k] = pv.v
			}
		}
		if err := check(cur); err != nil {
			return err
		}
	}
	if p == nil {
		p = &pendingPrefs{set: map[string]prefVal{}}
		h.pending[user] = p
	}
	for k, v := range set {
		p.seq++
		p.set[k] = prefVal{v, p.seq}
	}
	if p.timer == nil && !p.flushing {
		p.timer = time.AfterFunc(quiet, fire)
	}
	return nil
}

var errPrefsClosed = errors.New("the server is shutting down")

// begin starts the one flush of the person: what is pending now. ok is false
// when there is nothing, or a flush is running already (it schedules the next
// one when it ends).
func (h *prefsHub) begin(user string) (snap map[string]prefVal, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.pending[user]
	if p == nil || p.flushing || len(p.set) == 0 {
		return nil, false
	}
	p.flushing = true
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	snap = make(map[string]prefVal, len(p.set))
	for k, pv := range p.set {
		snap[k] = pv
	}
	return snap, true
}

// finish ends the flush that began with snap. When it is done with (written,
// or given up), the values it held go, unless they were written again since:
// those are newer and stay. When it is to be tried again, everything stays
// and the next try waits twice as long as the one before; after
// maxPrefsRetries tries it is given up (gaveUp). Whatever is left is flushed
// after a wait.
func (h *prefsHub) finish(user string, snap map[string]prefVal, retry bool, quiet time.Duration, fire func()) (gaveUp bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.pending[user]
	if p == nil {
		return false
	}
	p.flushing = false
	if h.written == nil {
		h.written = map[string]uint64{}
	}
	h.written[user]++
	wait := quiet
	if retry {
		p.retries++
		if p.retries > maxPrefsRetries {
			retry, gaveUp = false, true
		} else {
			wait = quiet << p.retries
		}
	}
	if !retry {
		p.retries = 0
		for k, pv := range snap {
			if cur, ok := p.set[k]; ok && cur.seq == pv.seq {
				delete(p.set, k)
			}
		}
	}
	if len(p.set) == 0 {
		delete(h.pending, user)
		return gaveUp
	}
	if !h.closed && p.timer == nil {
		p.timer = time.AfterFunc(wait, fire)
	}
	return gaveUp
}

// peek is a copy of what is pending for the person.
func (h *prefsHub) peek(user string) map[string]any {
	m, _ := h.peekAt(user, 0, false)
	return m
}

// peekAt is peek, and whether no flush of the person ended since gen (when
// check is set): a value is in the file or pending, but between the read of
// the file and the peek a flush may move it from one to the other.
func (h *prefsHub) peekAt(user string, gen uint64, check bool) (map[string]any, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if check && h.written[user] != gen {
		return nil, false
	}
	p := h.pending[user]
	if p == nil {
		return nil, true
	}
	out := make(map[string]any, len(p.set))
	for k, pv := range p.set {
		out[k] = pv.v
	}
	return out, true
}

func (h *prefsHub) stop() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	users := make([]string, 0, len(h.pending))
	for u, p := range h.pending {
		if p.timer != nil {
			p.timer.Stop()
			p.timer = nil
		}
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

func (s *server) prefsFire(userID string) func() {
	return func() { s.goBg(func(ctx context.Context) { s.flushPrefs(ctx, userID) }) }
}

// permanentPrefsError says whether a write of the preferences can never work
// as it is: the file would be too large or is refused, the person has no
// personal space, or the space is read only (past its deadline for a remote).
func permanentPrefsError(err error) bool {
	var inv *space.InvalidError
	var req *spaces.RequiredError
	return errors.As(err, &inv) || errors.As(err, &req) || errors.Is(err, spaces.ErrNoPersonalSpace)
}

// flushPrefs commits what is pending for the person, one flush at a time. A
// write that can never work is dropped (the browser keeps its copy); one that
// may work later is tried again, a few times, each after a longer wait.
func (s *server) flushPrefs(ctx context.Context, userID string) {
	snap, ok := s.prefs.begin(userID)
	if !ok {
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
			for k, pv := range snap {
				old, had := cur[k]
				if pv.v == nil {
					if had {
						delete(cur, k)
						changed = true
					}
					continue
				}
				if !had || !jsonEqual(old, pv.v) {
					cur[k] = pv.v
					changed = true
				}
			}
			return changed
		})
	}()
	retry := err != nil && !permanentPrefsError(err) && ctx.Err() == nil
	switch {
	case err == nil, errors.Is(err, spaces.ErrNoPersonalSpace):
	case retry:
		s.log.Warn("could not write the preferences of a person; trying again later", "error", err)
	default:
		s.log.Warn("the preferences of a person cannot be written and are dropped; the browser keeps them", "keys", len(snap), "error", err)
	}
	if s.prefs.finish(userID, snap, retry, s.prefsQuiet(), s.prefsFire(userID)) {
		s.log.Warn("gave up writing the preferences of a person; the browser keeps them", "keys", len(snap), "tries", maxPrefsRetries+1, "error", err)
	}
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
	uid := c.user().ID
	var prefs map[string]any
	personal := true
	sp, release, err := s.spaces.OpenPersonal(ctx, uid)
	switch {
	case errors.Is(err, spaces.ErrNoPersonalSpace), errors.Is(err, spaces.ErrPersonalAway):
		personal = false
	case err != nil:
		s.fail(c.w, err)
		return
	default:
		defer release()
	}
	// What was written and is not committed yet is what the person set last,
	// on top of the file. A flush that ends between the read of the file and
	// the look at what is pending has the file read again.
	var pending map[string]any
	for try := 0; ; try++ {
		gen := s.prefs.gen(uid)
		prefs = map[string]any{}
		if personal {
			if prefs, err = sp.ReadPrefs(); err != nil {
				s.log.Warn("could not read the preferences of a person", "error", err)
				prefs, personal = map[string]any{}, false
			}
		}
		var ok bool
		if pending, ok = s.prefs.peekAt(uid, gen, true); ok || try == 4 {
			if !ok {
				pending = s.prefs.peek(uid)
			}
			break
		}
	}
	for k, v := range pending {
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
	// A person with nowhere to keep them, or whose space cannot be written now
	// (switched off, away, read only), is told, and the browser keeps them.
	uid := c.user().ID
	sp, release, err := s.spaces.OpenPersonal(c.r.Context(), uid)
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
	if err := sp.Writable(); err != nil {
		release()
		writeJSON(c.w, http.StatusOK, struct {
			Personal bool `json:"personal"`
		}{false})
		return
	}
	// The file, with what is pending and this write, must fit: a write that
	// cannot be committed is refused now, not kept in memory. The file is read
	// again if a flush ends between its read and the check.
	var err2 error
	for try := 0; try < 5; try++ {
		gen := s.prefs.gen(uid)
		cur, rerr := sp.ReadPrefs()
		if rerr != nil {
			release()
			writeJSON(c.w, http.StatusOK, struct {
				Personal bool `json:"personal"`
			}{false})
			return
		}
		fits := func(pending map[string]any) error {
			all := make(map[string]any, len(cur)+len(pending)+len(set))
			for _, m := range []map[string]any{cur, pending, set} {
				for k, v := range m {
					if v == nil {
						delete(all, k)
					} else {
						all[k] = v
					}
				}
			}
			if len(all) > maxPrefKeys {
				return &codedError{code: "prefs_full", msg: fmt.Sprintf("A person keeps at most %d preferences.", maxPrefKeys)}
			}
			if n, err := space.PrefsSize(all); err != nil || n > space.MaxPrefsBytes {
				return &codedError{code: "prefs_full", msg: fmt.Sprintf("The preferences would be larger than %d bytes.", space.MaxPrefsBytes)}
			}
			return nil
		}
		if err2 = s.prefs.add(uid, set, s.prefsQuiet(), s.prefsFire(uid), gen, fits); !errors.Is(err2, errPrefsStale) {
			break
		}
	}
	release()
	if err2 != nil {
		var ce *codedError
		if errors.As(err2, &ce) {
			writeCoded(c.w, ce)
			return
		}
		writeError(c.w, http.StatusServiceUnavailable, "unavailable", "The server is shutting down or busy; try again.")
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Personal bool `json:"personal"`
	}{true})
}
