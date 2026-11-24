package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Confirmations (docs/api.md, "Confirmations and bulk actions"). A token says
// that the administrator who asked saw the plan of an action: it is bound to
// the caller, the action, the exact set of ids and a hash of what the plan was
// made from, lasts ten minutes and works once. It is held in memory (at most
// maxConfirms; a restart forgets them and the client asks again).

const (
	confirmTTL  = 10 * time.Minute
	maxConfirms = 1000
	// maxConfirmIDs is the most things one confirmation or bulk action names.
	maxConfirmIDs = 500
)

type confirmEntry struct {
	user, action, ids, hash string
	expires                 time.Time
	// req is what the token was asked for: a bulk action runs it.
	req confirmRequest
}

type confirmStore struct {
	mu sync.Mutex
	m  map[string]*confirmEntry
}

func newConfirmStore() *confirmStore { return &confirmStore{m: map[string]*confirmEntry{}} }

// issue makes a token. Expired ones are dropped first; when the table is still
// full the oldest goes (a client that lost its token asks again).
func (c *confirmStore) issue(e confirmEntry, now time.Time) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= maxConfirms {
		var oldest string
		for k, v := range c.m {
			if !v.expires.After(now) {
				delete(c.m, k)
			} else if oldest == "" || v.expires.Before(c.m[oldest].expires) {
				oldest = k
			}
		}
		if len(c.m) >= maxConfirms {
			delete(c.m, oldest)
		}
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("api: no randomness: " + err.Error())
	}
	tok := "c_" + hex.EncodeToString(b[:])
	e.expires = now.Add(confirmTTL)
	c.m[tok] = &e
	return tok
}

// The answers of take.
type confirmResult int

const (
	confirmOK       confirmResult = iota
	confirmUnknown                // not a token, used, expired, or bound to something else: 404
	confirmStaleNow               // bound to this, but the plan changed: 409 confirm_stale
)

// take spends a token for (user, action, ids), when the plan hash is still the
// one it was made with. A token asked with another caller, action or set is
// unknown, and is not spent by the attempt; one whose plan changed is spent.
func (c *confirmStore) take(tok, user, action, ids, hash string, now time.Time) confirmResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[tok]
	if e == nil || !e.expires.After(now) {
		delete(c.m, tok)
		return confirmUnknown
	}
	if e.user != user || e.action != action || e.ids != ids {
		return confirmUnknown
	}
	delete(c.m, tok)
	if e.hash != hash {
		return confirmStaleNow
	}
	return confirmOK
}

// takeAny spends a token of the user, whatever it is for, and returns what it was
// asked for (the bulk action runs that). It is spent whether or not the plan
// still holds.
func (c *confirmStore) takeAny(tok, user string, now time.Time) (*confirmEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[tok]
	if e == nil || !e.expires.After(now) || e.user != user {
		if e != nil && !e.expires.After(now) {
			delete(c.m, tok)
		}
		return nil, false
	}
	delete(c.m, tok)
	return e, true
}

// idsKey is the exact set of ids as one string.
func idsKey(ids []string) string {
	l := append([]string(nil), ids...)
	sort.Strings(l)
	return strings.Join(l, ",")
}

func hashOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// confirmRequest is the body of POST /api/admin/confirm.
type confirmRequest struct {
	Action    string            `json:"action"`
	Spaces    []string          `json:"spaces"`
	Users     []string          `json:"users"`
	Items     []string          `json:"items"`
	Heir      string            `json:"heir"`
	Transfers map[string]string `json:"transfers"`
}

// confirmPlan is what an action would do, as the confirmation shows it.
type confirmPlan struct {
	Plan      []any
	Bytes     int64
	NeedsHeir []any
	Blockers  []any
	// Hash covers what the plan was made from.
	Hash string
}

type confirmResp struct {
	Confirm   string `json:"confirm"`
	Expires   string `json:"expires"`
	Count     int    `json:"count"`
	Plan      []any  `json:"plan"`
	Bytes     int64  `json:"bytes"`
	NeedsHeir []any  `json:"needsHeir"`
	Blockers  []any  `json:"blockers"`
}

// confirmActions are the actions that need a confirmation, and which of the
// three lists of ids each takes.
var confirmActions = map[string]string{
	"delete_spaces": "spaces", "purge": "items", "restore": "items", "evict": "spaces", "pin": "spaces", "unpin": "spaces",
	"disable_spaces": "spaces", "enable_spaces": "spaces", "delete_users": "users", "disable_users": "users", "enable_users": "users",
	"export": "spaces+items",
}

// ids of the request that this action takes.
func (r confirmRequest) idList() []string {
	switch confirmActions[r.Action] {
	case "spaces":
		return r.Spaces
	case "users":
		return r.Users
	case "items":
		return r.Items
	case "spaces+items":
		return append(append([]string(nil), r.Spaces...), r.Items...)
	}
	return nil
}

func (s *server) adminConfirm(c *call) {
	var req confirmRequest
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	kind, ok := confirmActions[req.Action]
	if !ok {
		writeError(c.w, http.StatusBadRequest, "invalid", "action is one of "+actionNames())
		return
	}
	ids := req.idList()
	if len(ids) == 0 || len(ids) > maxConfirmIDs {
		writeError(c.w, http.StatusBadRequest, "invalid", "name from 1 to "+itoa(maxConfirmIDs)+" "+strings.ReplaceAll(kind, "+", " or ")+" for "+req.Action)
		return
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			writeError(c.w, http.StatusBadRequest, "invalid", "an id is named twice")
			return
		}
		seen[id] = true
	}
	plan, ok := s.planFor(c, req)
	if !ok {
		return
	}
	tok := s.confirms.issue(confirmEntry{user: c.user().ID, action: req.Action, ids: idsKey(ids), hash: plan.Hash, req: req}, s.now())
	nh, bl := plan.NeedsHeir, plan.Blockers
	if nh == nil {
		nh = []any{}
	}
	if bl == nil {
		bl = []any{}
	}
	pl := plan.Plan
	if pl == nil {
		pl = []any{}
	}
	writeJSON(c.w, http.StatusOK, confirmResp{Confirm: tok, Expires: rfc3339(s.now().Add(confirmTTL)), Count: len(ids), Plan: pl, Bytes: plan.Bytes, NeedsHeir: nh, Blockers: bl})
}

func actionNames() string {
	l := make([]string, 0, len(confirmActions))
	for k := range confirmActions {
		l = append(l, k)
	}
	sort.Strings(l)
	return strings.Join(l, ", ")
}

// useConfirm checks the token of a request that needs one, answering the refusal
// itself: 404 for a token that is not valid for this, 409 confirm_stale when the
// plan changed. The plan is made again from the records at this moment.
func (s *server) useConfirm(c *call, tok string, req confirmRequest) bool {
	if tok == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "this needs a confirmation: ask POST /api/admin/confirm first")
		return false
	}
	plan, ok := s.planFor(c, req)
	if !ok {
		return false // answered (404 for something that is not in the caller's scope)
	}
	switch s.confirms.take(tok, c.user().ID, req.Action, idsKey(req.idList()), plan.Hash, s.now()) {
	case confirmUnknown:
		writeError(c.w, http.StatusNotFound, "not_found", "no such confirmation: it was used, has expired, or is for something else")
		return false
	case confirmStaleNow:
		writeError(c.w, http.StatusConflict, "confirm_stale", "what the plan was made from has changed: ask again")
		return false
	}
	return true
}

// planFor makes the plan of an action over the ids of the request, all of which
// must be in the caller's scope (404 for any that is not, and nothing is made).
func (s *server) planFor(c *call, req confirmRequest) (confirmPlan, bool) {
	sc := store.ScopeOf(c.user())
	switch confirmActions[req.Action] {
	case "spaces":
		return s.planSpaces(c, sc, req)
	case "items":
		return s.planItems(c, sc, req)
	case "users":
		return s.planUsers(c, sc, req)
	case "spaces+items":
		p1, ok := s.planSpaces(c, sc, confirmRequest{Action: req.Action, Spaces: req.Spaces})
		if !ok {
			return p1, false
		}
		p2, ok := s.planItems(c, sc, confirmRequest{Action: req.Action, Items: req.Items})
		if !ok {
			return p2, false
		}
		return confirmPlan{Plan: append(p1.Plan, p2.Plan...), Bytes: p1.Bytes + p2.Bytes, Hash: hashOf(p1.Hash, p2.Hash)}, true
	}
	writeError(c.w, http.StatusBadRequest, "invalid", req.Action+" cannot be confirmed here")
	return confirmPlan{}, false
}
