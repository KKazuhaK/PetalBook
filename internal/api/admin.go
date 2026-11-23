package api

import (
	"errors"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// The administration of the whole server (administrators only, accounts mode).
// These routes name a space in the path but never open it for its content: they
// work on the database rows and on the manager, so they work on a disabled
// space, and a list of every space does not open a single one.

const (
	defaultAdminLimit = 50
	maxAdminLimit     = 200
)

type adminOwner struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

type adminSpaceResp struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Personal bool        `json:"personal"`
	Inbox    string      `json:"inbox"`
	Created  string      `json:"created"`
	Disabled bool        `json:"disabled"`
	Owner    *adminOwner `json:"owner"`
	Members  int         `json:"members"`
	// SizeBytes and SizeAt are the last stored measurement, null before the first.
	SizeBytes *int64  `json:"sizeBytes"`
	SizeAt    *string `json:"sizeAt"`
	// Sync is "local", or the state of the remote the space follows, as far as
	// the server knows without opening the space.
	Sync string `json:"sync"`
	// Storage is what the space's copy on the server is (milestone 3.1).
	Storage *adminStorageResp `json:"storage"`
}

func (s *server) adminListSpaces(c *call) {
	q := c.r.URL.Query()
	limit := defaultAdminLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxAdminLimit {
			writeError(c.w, http.StatusBadRequest, "invalid", "limit is a number from 1 to "+strconv.Itoa(maxAdminLimit))
			return
		}
		limit = n
	}
	f := store.SpaceFilter{Query: q.Get("q"), After: q.Get("cursor"), Limit: limit, Cache: q.Get("cache")}
	switch q.Get("overdue") {
	case "", "0":
	case "1":
		p := s.spaces.RemotePolicy()
		f.Overdue = &p
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "overdue is 1 or 0")
		return
	}
	rows, next, err := store.ListAllSpaces(c.r.Context(), s.opt.Store.Q(), f)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	items := s.adminSpaceItems(c, rows)
	writeJSON(c.w, http.StatusOK, struct {
		Items []adminSpaceResp `json:"items"`
		Next  string           `json:"next"`
	}{items, next})
}

// adminSpaceItems is the answer's form of rows read by the store.
func (s *server) adminSpaceItems(c *call, rows []store.AdminSpace) []adminSpaceResp {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	syncs := s.spaces.SyncOf(c.r.Context(), ids)
	items := make([]adminSpaceResp, len(rows))
	for i, r := range rows {
		it := adminSpaceResp{ID: r.ID, Name: r.Name, Personal: r.Personal, Inbox: r.Inbox, Created: rfc3339(r.Created), Disabled: r.Disabled,
			Members: r.Members, SizeBytes: r.SizeBytes, Sync: syncs[i], Storage: s.adminStorageOf(c.r.Context(), r)}
		if r.Owner != nil {
			it.Owner = &adminOwner{r.Owner.ID, r.Owner.Username, r.Owner.Name}
		}
		if r.SizeAt != nil {
			at := rfc3339(*r.SizeAt)
			it.SizeAt = &at
		}
		items[i] = it
	}
	return items
}

// adminGetSpace is one item of the list, found by its id.
func (s *server) adminGetSpace(c *call) {
	row, err := store.GetAdminSpace(c.r.Context(), s.opt.Store.Q(), c.r.PathValue("space"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "no such space")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, s.adminSpaceItems(c, []store.AdminSpace{row})[0])
}

func (s *server) adminResizeSpace(c *call) {
	n, at, err := s.spaces.Resize(c.r.Context(), c.r.PathValue("space"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		SizeBytes int64  `json:"sizeBytes"`
		SizeAt    string `json:"sizeAt"`
	}{n, rfc3339(at)})
}

func (s *server) adminTransferSpace(c *call) {
	var req struct {
		UserID string `json:"userId"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if err := s.spaces.Transfer(c.r.Context(), c.r.PathValue("space"), req.UserID); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) adminDisableSpace(c *call) {
	s.adminSetDisabled(c, true)
}

func (s *server) adminEnableSpace(c *call) {
	s.adminSetDisabled(c, false)
}

func (s *server) adminSetDisabled(c *call, disabled bool) {
	if err := s.spaces.SetDisabled(c.r.Context(), c.r.PathValue("space"), disabled); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) adminDeleteSpace(c *call) {
	d, err := s.spaces.DeleteSpace(c.r.Context(), c.r.PathValue("space"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Trashed      string   `json:"trashed"`
		CacheTrashed string   `json:"cacheTrashed"`
		Warnings     []string `json:"warnings"`
	}{d.Trashed, d.CacheTrashed, append([]string{}, d.Warnings...)})
}

func (s *server) adminStatus(c *call) {
	ctx := c.r.Context()
	nSpaces, err := store.CountSpaces(ctx, s.opt.Store.Q())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	nUsers, err := store.CountUsers(ctx, s.opt.Store.Q())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := struct {
		Version       string `json:"version"`
		StartedAt     string `json:"startedAt"`
		UptimeSeconds int64  `json:"uptimeSeconds"`
		Database      string `json:"database"`
		Spaces        int    `json:"spaces"`
		Users         int    `json:"users"`
		OpenSpaces    int    `json:"openSpaces"`
		// DiskFreeBytes is the room left on the disk of the data directory;
		// absent where the platform cannot say.
		DiskFreeBytes *uint64 `json:"diskFreeBytes,omitempty"`
		GoVersion     string  `json:"goVersion"`
		Storage       any     `json:"storage"`
	}{s.opt.Version, rfc3339(s.started), int64(time.Since(s.started) / time.Second), string(s.opt.Store.Dialect()),
		nSpaces, nUsers, s.spaces.OpenCount(), nil, runtime.Version(), nil}
	var serr error
	if out.Storage, serr = s.storageStatus(ctx); serr != nil {
		s.fail(c.w, serr)
		return
	}
	if free, ok := diskFree(s.spaces.DataDir()); ok {
		out.DiskFreeBytes = &free
	}
	writeJSON(c.w, http.StatusOK, out)
}
