package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"petalbook.app/server/internal/store"
)

// The notification centre (docs/api.md, "Notifications"). Everything here is
// the caller's own: a list, a count and the marks of one person, read by the
// caller's id and tenant, never by an id from the request.

// derivedPrefix starts the id of a derived notification: `problems:<space id>`.
const derivedPrefix = "problems:"

// unreadCap is where the badge stops counting.
const unreadCap = 100

type notificationResp struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Payload any    `json:"payload"`
	// At is when it happened (RFC 3339); null for a derived one, which is not an
	// event but a state that holds now.
	At      *string `json:"at"`
	Read    bool    `json:"read"`
	Derived bool    `json:"derived,omitempty"`
}

// derived are the problems of the spaces the caller writes in, as items, with
// what the caller last read of each, and how many more spaces have problems
// than are listed. It is built once per request and handed on.
type derived struct {
	items []notificationResp
	more  int
}

func (s *server) derivedItems(c *call) (derived, error) {
	probs, more, err := s.spaces.SpaceProblems(c.r.Context(), c.user().ID)
	if err != nil {
		return derived{}, err
	}
	if len(probs) == 0 {
		return derived{more: more}, nil
	}
	seen, err := store.ListSeen(c.r.Context(), s.opt.Store.Q(), c.user().ID)
	if err != nil {
		return derived{}, err
	}
	out := make([]notificationResp, len(probs))
	for i, p := range probs {
		key := derivedPrefix + p.ID
		out[i] = notificationResp{ID: key, Kind: store.NotifyProblems, Ref: p.ID, Derived: true, Read: seen[key] == p.Sig,
			Payload: map[string]any{"space": p.Name, "total": p.Total, "counts": p.Counts, "sig": p.Sig}}
	}
	return derived{out, more}, nil
}

// derivedUnread is how many of the derived items the caller has not read, for
// the badge asked for every minute: no items are built, and the marks are only
// read when some space has problems.
func (s *server) derivedUnread(c *call) (int, error) {
	probs, _, err := s.spaces.SpaceProblems(c.r.Context(), c.user().ID)
	if err != nil || len(probs) == 0 {
		return 0, err
	}
	seen, err := store.ListSeen(c.r.Context(), s.opt.Store.Q(), c.user().ID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range probs {
		if seen[derivedPrefix+p.ID] != p.Sig {
			n++
		}
	}
	return n, nil
}

func (s *server) listNotifications(c *call) {
	q := c.r.URL.Query()
	kind := q.Get("kind")
	if kind != "" && !store.KnownNotifyKind(kind) {
		writeError(c.w, http.StatusBadRequest, "invalid", "That is not a kind of notification.")
		return
	}
	unreadOnly := q.Get("unread") == "1"
	limit := 20
	if v := q.Get("limit"); v != "" {
		n, ok := atoiIn(v, 1, 100)
		if !ok {
			writeError(c.w, http.StatusBadRequest, "invalid", "The limit must be a number from 1 to 100.")
			return
		}
		limit = n
	}
	cursor := q.Get("cursor")
	items := []notificationResp{}
	// The derived ones come first, on the first page only.
	der, err := s.derivedItems(c)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	more := 0
	if cursor == "" && (kind == "" || kind == store.NotifyProblems) {
		more = der.more
		for _, d := range der.items {
			if !unreadOnly || !d.Read {
				items = append(items, d)
			}
		}
	}
	next := ""
	if kind != store.NotifyProblems {
		rows, err := store.ListNotifications(c.r.Context(), s.opt.Store.Q(), c.user().ID, c.user().TenantID,
			store.NotifyFilter{Kind: kind, UnreadOnly: unreadOnly, Before: cursor, Limit: limit})
		if err != nil {
			s.fail(c.w, err)
			return
		}
		if len(rows) > limit {
			rows = rows[:limit]
			next = rows[limit-1].ID
		}
		for _, n := range rows {
			at := rfc3339(n.Created)
			items = append(items, notificationResp{ID: n.ID, Kind: n.Kind, Ref: n.Ref, Payload: json.RawMessage(n.Payload), At: &at, Read: n.Read})
		}
	}
	count, err := s.unreadCount(c, unreadOf(der.items))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items  []notificationResp `json:"items"`
		Next   string             `json:"next"`
		Unread int                `json:"unread"`
		// MoreProblems is how many more spaces have problems than are listed.
		MoreProblems int `json:"moreProblems,omitempty"`
	}{items, next, count, more})
}

func unreadOf(items []notificationResp) int {
	n := 0
	for _, d := range items {
		if !d.Read {
			n++
		}
	}
	return n
}

// unreadCount is the stored unread rows (one index range, counted up to
// unreadCap) and the derived ones the caller has not read as they are now.
func (s *server) unreadCount(c *call, derivedUnread int) (int, error) {
	n, err := store.UnreadCapped(c.r.Context(), s.opt.Store.Q(), c.user().ID, c.user().TenantID, unreadCap)
	if err != nil {
		return 0, err
	}
	return min(n+derivedUnread, unreadCap), nil
}

func (s *server) getUnreadNotifications(c *call) {
	d, err := s.derivedUnread(c)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	n, err := s.unreadCount(c, d)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Count int `json:"count"`
	}{n})
}

func (s *server) markNotificationsRead(c *call) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 100 {
		writeError(c.w, http.StatusBadRequest, "invalid", "Name from 1 to 100 notifications.")
		return
	}
	var stored, keys []string
	for _, id := range req.IDs {
		if len(id) > 200 {
			writeError(c.w, http.StatusBadRequest, "invalid", "That is not a notification id.")
			return
		}
		if strings.HasPrefix(id, derivedPrefix) {
			keys = append(keys, id)
		} else {
			stored = append(stored, id)
		}
	}
	ctx, now := c.r.Context(), s.now()
	if len(stored) > 0 {
		if _, err := store.MarkNotificationsRead(ctx, s.opt.Store.Q(), c.user().ID, c.user().TenantID, stored, now); err != nil {
			s.fail(c.w, err)
			return
		}
	}
	der, err := s.derivedItems(c)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if len(keys) > 0 {
		if err := s.seeProblems(c, der.items, keys, now); err != nil {
			s.fail(c.w, err)
			return
		}
	}
	s.answerUnread(c, der.items)
}

// seeProblems records that the caller read the derived items with these keys as
// they are now (a key that is not one of the caller's has nothing to record),
// and marks them read in items.
func (s *server) seeProblems(c *call, items []notificationResp, keys []string, now time.Time) error {
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	for i, d := range items {
		if !want[d.ID] || d.Read {
			continue
		}
		sig, _ := d.Payload.(map[string]any)["sig"].(string)
		if err := store.SetSeen(c.r.Context(), s.opt.Store.Q(), c.user().ID, d.ID, sig, now); err != nil {
			return err
		}
		items[i].Read = true
	}
	return nil
}

func (s *server) markAllNotificationsRead(c *call) {
	var req struct {
		Kind string `json:"kind"`
	}
	if !s.readJSON(c.w, c.r, &req, true) {
		return
	}
	if req.Kind != "" && !store.KnownNotifyKind(req.Kind) {
		writeError(c.w, http.StatusBadRequest, "invalid", "That is not a kind of notification.")
		return
	}
	ctx, now := c.r.Context(), s.now()
	if req.Kind != store.NotifyProblems {
		if _, err := store.MarkAllNotificationsRead(ctx, s.opt.Store.Q(), c.user().ID, c.user().TenantID, req.Kind, now); err != nil {
			s.fail(c.w, err)
			return
		}
	}
	der, err := s.derivedItems(c)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if req.Kind == "" || req.Kind == store.NotifyProblems {
		keys := make([]string, 0, len(der.items))
		for _, d := range der.items {
			keys = append(keys, d.ID)
		}
		if err := s.seeProblems(c, der.items, keys, now); err != nil {
			s.fail(c.w, err)
			return
		}
		// What is no longer a problem needs no cursor. The spaces past the cap
		// keep theirs: they are problems still.
		if der.more == 0 {
			if err := store.ForgetSeen(ctx, s.opt.Store.Q(), c.user().ID, keys); err != nil {
				s.fail(c.w, err)
				return
			}
		}
	}
	s.answerUnread(c, der.items)
}

// answerUnread answers the badge after a mark, from the derived items the
// request built (marked as they are now).
func (s *server) answerUnread(c *call, items []notificationResp) {
	n, err := s.unreadCount(c, unreadOf(items))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Unread int `json:"unread"`
	}{n})
}

// atoiIn reads a whole number from lo to hi.
func atoiIn(v string, lo, hi int) (int, bool) {
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= lo && n <= hi
}
