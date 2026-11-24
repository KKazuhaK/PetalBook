package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/settings"
	"github.com/KKazuhaK/PetalBook/internal/space"
)

// The offline sync endpoints (milestone 4.1, docs/api.md "Offline sync"). They
// read the index's change feed and change nothing.

const (
	// maxSyncPages is how many pages one request of sync/pages may name.
	maxSyncPages = 100
	// maxHeadSpaces is how many spaces one request of heads may name.
	maxHeadSpaces = 100
)

// syncPagesBytes is how much of the pages one answer of sync/pages holds
// (tests make it smaller).
var syncPagesBytes = 8 << 20

// offlineOff answers 403 offline_off, and reports true, when the caller's
// tenant does not allow offline copies (the setting offline.copies, as set by
// the site and the tenant).
func (s *server) offlineOff(c *call) bool {
	e, err := s.settings.Get(c.r.Context(), c.user().TenantID, settings.OfflineCopies)
	if err != nil {
		s.fail(c.w, err)
		return true
	}
	if on, _ := e.Value.(bool); on {
		return false
	}
	writeError(c.w, http.StatusForbidden, "offline_off", "offline copies are turned off for this tenant")
	return true
}

// syncLimit reads the limit of a request: def when there is none, else 1 to max.
func syncLimit(r *http.Request, def, max int) (int, string) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return def, ""
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > max {
		return 0, "limit must be a number from 1 to " + strconv.Itoa(max)
	}
	return n, ""
}

// failFeed answers the refusals of the feed: a cursor that is not one of ours,
// and one that is too old.
func (s *server) failFeed(c *call, err error) {
	switch {
	case errors.Is(err, index.ErrExpired):
		writeError(c.w, http.StatusGone, "changes_expired", "the change cursor is too old; read the catalogue again")
	case errors.Is(err, index.ErrBadFeedCursor), errors.Is(err, index.ErrBadCursor):
		writeError(c.w, http.StatusBadRequest, "invalid", "not a cursor this server made, or one made for other parameters")
	default:
		s.fail(c.w, err)
	}
}

func (s *server) syncCatalog(c *call) {
	if s.offlineOff(c) {
		return
	}
	q := c.r.URL.Query()
	limit, bad := syncLimit(c.r, index.DefaultCatalog, index.MaxCatalog)
	if bad != "" {
		writeError(c.w, http.StatusBadRequest, "invalid", bad)
		return
	}
	folders := q["folder"]
	if len(folders) > index.MaxFolders {
		writeError(c.w, http.StatusBadRequest, "invalid", "at most "+strconv.Itoa(index.MaxFolders)+" folders")
		return
	}
	b, err := c.sp.Space.Catalog(folders, q.Get("cursor"), limit)
	if err != nil {
		s.failFeed(c, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items    []index.CatalogEntry `json:"items"`
		Next     string               `json:"next"`
		Changes  string               `json:"changes,omitempty"`
		Tree     string               `json:"tree"`
		Settings string               `json:"settings"`
	}{b.Items, b.Next, b.Changes, b.Tree, c.sp.Space.SettingsStamp()})
}

func (s *server) syncChanges(c *call) {
	if s.offlineOff(c) {
		return
	}
	since := c.r.URL.Query().Get("since")
	if since == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "since is required: a change cursor")
		return
	}
	limit, bad := syncLimit(c.r, index.DefaultChanges, index.MaxChanges)
	if bad != "" {
		writeError(c.w, http.StatusBadRequest, "invalid", bad)
		return
	}
	cl, err := c.sp.Space.Changes(since, limit)
	if err != nil {
		s.failFeed(c, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items    []index.CatalogEntry `json:"items"`
		Next     string               `json:"next"`
		More     bool                 `json:"more"`
		Tree     string               `json:"tree"`
		Settings string               `json:"settings"`
	}{cl.Items, cl.Next, cl.More, cl.Tree, c.sp.Space.SettingsStamp()})
}

func (s *server) syncPages(c *call) {
	if s.offlineOff(c) {
		return
	}
	raw := c.r.URL.Query().Get("ids")
	if raw == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "ids is required: 1 to "+strconv.Itoa(maxSyncPages)+" page ids, separated by commas")
		return
	}
	ids := strings.Split(raw, ",")
	if len(ids) > maxSyncPages {
		writeError(c.w, http.StatusBadRequest, "invalid", "at most "+strconv.Itoa(maxSyncPages)+" pages at a time")
		return
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !page.ValidID(id) {
			writeError(c.w, http.StatusBadRequest, "invalid", "not a page id: "+id)
			return
		}
		if seen[id] {
			writeError(c.w, http.StatusBadRequest, "invalid", "a page id given twice: "+id)
			return
		}
		seen[id] = true
	}
	var pages []pageWithDraft
	var missing, rest []string
	revs := map[string]string{}
	size := 0
	for i, id := range ids {
		p, err := c.sp.Space.Get(id)
		if errors.Is(err, space.ErrNotFound) {
			missing = append(missing, id)
			continue
		}
		if err != nil {
			s.fail(c.w, err)
			return
		}
		// The first page always fits, whatever its size.
		if n := len(p.Header) + len(p.Body); len(pages) > 0 && size+n > syncPagesBytes {
			rest = append(rest, ids[i:]...)
			break
		} else {
			size += n
		}
		pages = append(pages, pageWithDraft{pageResp: toPage(p)})
		revs[id] = p.Rev
	}
	ds, err := s.drafts.GetMany(c.r.Context(), c.sp.Space, c.sp.Info.ID, c.user().ID, revs)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	for i := range pages {
		if d := ds[pages[i].ID]; d != nil {
			pages[i].Draft = toDraftResp(d)
		}
	}
	if pages == nil {
		pages = []pageWithDraft{}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Pages   []pageWithDraft `json:"pages"`
		Missing []string        `json:"missing,omitempty"`
		Rest    []string        `json:"rest,omitempty"`
	}{pages, missing, rest})
}

func (s *server) syncHeads(c *call) {
	if s.offlineOff(c) {
		return
	}
	ids := c.r.URL.Query()["space"]
	if len(ids) == 0 || len(ids) > maxHeadSpaces {
		writeError(c.w, http.StatusBadRequest, "invalid", "give 1 to "+strconv.Itoa(maxHeadSpaces)+" space parameters")
		return
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			writeError(c.w, http.StatusBadRequest, "invalid", "a space given twice: "+id)
			return
		}
		seen[id] = true
	}
	hs, err := s.spaces.Heads(c.r.Context(), c.user().ID, ids)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	heads := make(map[string]string, len(ids))
	var missing []string
	for _, id := range ids {
		h, ok := hs[id]
		if ok {
			if g := c.p.Grant; g != nil {
				// A token reaches only the spaces it names, never more than its owner has.
				ok = g.Names(id) && (h.Perms & g.Perms()).Has(perm.Read)
			} else {
				ok = h.Perms.Has(perm.Read)
			}
		}
		if !ok || h.Disabled {
			missing = append(missing, id)
			continue
		}
		heads[id] = h.Head
	}
	writeJSON(c.w, http.StatusOK, struct {
		Heads   map[string]string `json:"heads"`
		Missing []string          `json:"missing,omitempty"`
	}{heads, missing})
}
