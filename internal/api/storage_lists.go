package api

// The lists and the summary of the storage administration (milestone 3.5, part 4;
// docs/api.md, "Storage management"). Every number is a stored value: the usage
// rows, the columns of the spaces, the in-memory attachment cache. Nothing here
// walks a disk or a repository, and every list is one query a page over an index.

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// intParam reads a whole number of at least min from the query; "" is missing.
func intParam(c *call, name string, min, max int) (n int, present, ok bool) {
	v := c.r.URL.Query().Get(name)
	if v == "" {
		return 0, false, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		writeError(c.w, http.StatusBadRequest, "invalid", name+" is a number from "+strconv.Itoa(min)+" to "+strconv.Itoa(max))
		return 0, true, false
	}
	return n, true, true
}

// flagParam reads a filter that is "1" to ask for it ("0" or nothing: not asked).
func flagParam(c *call, name string) (on, ok bool) {
	switch c.r.URL.Query().Get(name) {
	case "", "0":
		return false, true
	case "1":
		return true, true
	}
	writeError(c.w, http.StatusBadRequest, "invalid", name+" is 1 or 0")
	return false, false
}

// oneOf reads a filter that is one of the listed values ("" for none).
func oneOf(c *call, name string, values []string) (string, bool) {
	v := c.r.URL.Query().Get(name)
	if v == "" || slices.Contains(values, v) {
		return v, true
	}
	writeError(c.w, http.StatusBadRequest, "invalid", name+" is one of "+joinOr(values))
	return "", false
}

func joinOr(l []string) string {
	out := ""
	for i, v := range l {
		switch {
		case i == 0:
		case i == len(l)-1:
			out += " or "
		default:
			out += ", "
		}
		out += v
	}
	return out
}

func (s *server) badList(c *call, err error) bool {
	switch {
	case errors.Is(err, store.ErrBadCursor):
		writeError(c.w, http.StatusBadRequest, "invalid", "that cursor is not one this list gave, or it was made for another sort")
	case errors.Is(err, store.ErrBadFilter):
		writeError(c.w, http.StatusBadRequest, "invalid", "a sort, order or filter value this list does not have")
	default:
		return false
	}
	return true
}

// adminListSpaces is the 1.7 list with 3.1's storage block, sorted and filtered
// (docs/api.md, "GET /api/admin/spaces").
func (s *server) adminListSpaces(c *call) {
	q := c.r.URL.Query()
	limit, _, ok := intParam(c, "limit", 1, maxAdminLimit)
	if !ok {
		return
	}
	if limit == 0 {
		limit = defaultAdminLimit
	}
	f := store.SpaceFilter{Query: q.Get("q"), After: q.Get("cursor"), Limit: limit, Owner: q.Get("owner"), Order: q.Get("order")}
	if f.Sort, ok = oneOf(c, "sort", store.SpaceSorts); !ok {
		return
	}
	if f.Order != "" && f.Order != "asc" && f.Order != "desc" {
		writeError(c.w, http.StatusBadRequest, "invalid", "order is asc or desc")
		return
	}
	if f.Cache, ok = oneOf(c, "cache", store.SpaceCaches); !ok {
		return
	}
	if f.Remote, ok = oneOf(c, "remote", store.SpaceRemotes); !ok {
		return
	}
	if f.Deadline, ok = oneOf(c, "deadline", store.SpaceDeadline); !ok {
		return
	}
	if f.Pinned, ok = flagParam(c, "pinned"); !ok {
		return
	}
	if f.Exempt, ok = flagParam(c, "exempt"); !ok {
		return
	}
	overdue, ok := flagParam(c, "overdue") // 3.1's way to ask for deadline=overdue
	if !ok {
		return
	}
	if overdue && f.Deadline == "" {
		f.Deadline = "overdue"
	}
	if days, present, ok := intParam(c, "idleDays", 0, 36500); !ok {
		return
	} else if present {
		f.IdleBefore = s.now().Add(-time.Duration(days) * 24 * time.Hour)
	}
	if f.Deadline != "" {
		p := s.spaces.RemotePolicy()
		f.Policy = &p
	}
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	rows, next, err := store.ListAllSpaces(c.r.Context(), s.opt.Store.Q(), sc, f)
	if s.badList(c, err) {
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []adminSpaceResp `json:"items"`
		Next  string           `json:"next"`
	}{s.adminSpaceItems(c, rows), next})
}

type personUserResp struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
}

type personResp struct {
	User          personUserResp `json:"user"`
	Tenant        string         `json:"tenant"`
	Created       string         `json:"created"`
	LastActive    *string        `json:"lastActive"`
	FirstWrite    *string        `json:"firstWrite"`
	Dormant       bool           `json:"dormant"`
	Connected     bool           `json:"connected"`
	Spaces        int64          `json:"spaces"`
	RemoteSpaces  int64          `json:"remoteSpaces"`
	EvictedSpaces int64          `json:"evictedSpaces"`
	PinnedSpaces  int64          `json:"pinnedSpaces"`
	GraceSpaces   int64          `json:"graceSpaces"`
	OverdueSpaces int64          `json:"overdueSpaces"`
	UsageBytes    int64          `json:"usageBytes"`
	HeldBytes     int64          `json:"heldBytes"`
	MeasuredAt    *string        `json:"measuredAt"`
}

func optTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	v := rfc3339(t)
	return &v
}

func personOf(e store.PersonUsage) personResp {
	return personResp{User: personUserResp{e.ID, e.Username, e.Name, e.Disabled}, Tenant: e.TenantID, Created: rfc3339(e.Created),
		LastActive: optTime(e.LastActive), FirstWrite: optTime(e.FirstWrite), Dormant: e.Dormant, Connected: e.Connected,
		Spaces: e.Spaces, RemoteSpaces: e.RemoteSpaces, EvictedSpaces: e.EvictedSpaces, PinnedSpaces: e.PinnedSpaces,
		GraceSpaces: e.GraceSpaces, OverdueSpaces: e.OverdueSpaces, UsageBytes: e.UsageBytes, HeldBytes: e.HeldBytes, MeasuredAt: optTime(e.Measured)}
}

// adminListPeople is the people and what they hold, a page at a time.
func (s *server) adminListPeople(c *call) {
	q := c.r.URL.Query()
	limit, _, ok := intParam(c, "limit", 1, maxAdminLimit)
	if !ok {
		return
	}
	if limit == 0 {
		limit = defaultAdminLimit
	}
	f := store.PeopleFilter{Query: q.Get("q"), After: q.Get("cursor"), Limit: limit, Order: q.Get("order")}
	if f.Sort, ok = oneOf(c, "sort", store.PersonSorts); !ok {
		return
	}
	if f.Order != "" && f.Order != "asc" && f.Order != "desc" {
		writeError(c.w, http.StatusBadRequest, "invalid", "order is asc or desc")
		return
	}
	if f.Dormant, ok = flagParam(c, "dormant"); !ok {
		return
	}
	if f.Remote, ok = oneOf(c, "remote", store.PersonRemotes); !ok {
		return
	}
	if f.Deadline, ok = oneOf(c, "deadline", store.SpaceDeadline); !ok {
		return
	}
	if days, present, ok := intParam(c, "inactiveDays", 0, 36500); !ok {
		return
	} else if present {
		f.InactiveBefore = s.now().Add(-time.Duration(days) * 24 * time.Hour)
	}
	switch q.Get("disabled") {
	case "":
	case "0", "1":
		v := q.Get("disabled") == "1"
		f.Disabled = &v
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "disabled is 1 or 0")
		return
	}
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	rows, next, err := store.ListPeople(c.r.Context(), s.opt.Store.Q(), s.opt.Store.Dialect(), sc, f)
	if s.badList(c, err) {
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	items := make([]personResp, len(rows))
	for i, e := range rows {
		items[i] = personOf(e)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []personResp `json:"items"`
		Next  string       `json:"next"`
	}{items, next})
}

// adminGetPerson is one person's item with the spaces counted for them (at most
// 200, by the time they were last used). A tenant administrator's answer leaves
// the person's personal space out of both: the person's usage row is the one the
// quotas read, and is shown as it is.
func (s *server) adminGetPerson(c *call) {
	ctx := c.r.Context()
	e, err := store.GetPerson(ctx, s.opt.Store.Q(), s.opt.Store.Dialect(), store.AdminScopeOf(c.user()), c.r.PathValue("user"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "no such user")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	rows, _, err := store.ListAllSpaces(ctx, s.opt.Store.Q(), store.AdminScopeOf(c.user()), store.SpaceFilter{Owner: e.ID, Sort: "lastUsed", Limit: maxAdminLimit})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		personResp
		Items []adminSpaceResp `json:"items"`
	}{personOf(e), s.adminSpaceItems(c, rows)})
}

type storageSummaryResp struct {
	Tenant *string `json:"tenant"`
	Spaces struct {
		Local     int64 `json:"local"`
		Full      int64 `json:"full"`
		Partial   int64 `json:"partial"`
		Evicted   int64 `json:"evicted"`
		Archived  int64 `json:"archived"`
		Restoring *int  `json:"restoring,omitempty"` // the server's, so the site's only
	} `json:"spaces"`
	People          int64 `json:"people"`
	Dormant         int64 `json:"dormant"`
	HeldBytes       int64 `json:"heldBytes"`
	UsageBytes      int64 `json:"usageBytes"`
	AssetCacheBytes int64 `json:"assetCacheBytes"`
	Recycle         struct {
		Items int64 `json:"items"`
		Bytes int64 `json:"bytes"`
	} `json:"recycle"`
	Exports struct {
		Items int64 `json:"items"`
		Bytes int64 `json:"bytes"`
	} `json:"exports"`
	Remotes struct {
		InGrace int64 `json:"inGrace"`
		Overdue int64 `json:"overdue"`
	} `json:"remotes"`
	// RecycleDays is how long the recycle bin keeps an item (recycle.days now).
	RecycleDays   int     `json:"recycleDays"`
	DiskFreeBytes *uint64 `json:"diskFreeBytes,omitempty"`
	MeasuredAt    *string `json:"measuredAt"`
}

// storageSummary is the summary of the scope: stored values only (the usage rows,
// the recycle bin's and exports' rows, the attachment cache in memory, the free
// room of the disk for a site administrator).
func (s *server) storageSummary(c *call, sc store.Scope) (storageSummaryResp, error) {
	var r storageSummaryResp
	sum, err := store.GetStorageSummary(c.r.Context(), s.opt.Store.Q(), sc)
	if err != nil {
		return r, err
	}
	if !sc.All() {
		t := sc.Tenant()
		r.Tenant = &t
	}
	r.Spaces.Local, r.Spaces.Full, r.Spaces.Partial, r.Spaces.Evicted, r.Spaces.Archived = sum.Local, sum.Full, sum.Partial, sum.Evicted, sum.Archived
	if c.isSiteAdmin() {
		restoring, _ := s.spaces.RestoreCounts()
		r.Spaces.Restoring = &restoring
	}
	r.People, r.Dormant, r.HeldBytes, r.UsageBytes = sum.People, sum.Dormant, sum.HeldBytes, sum.UsageBytes
	if sc.All() {
		r.AssetCacheBytes, _ = s.spaces.AssetTotals()
	} else {
		if r.AssetCacheBytes, err = s.spaces.AssetBytesOfTenant(c.r.Context(), sc.Tenant()); err != nil {
			return r, err
		}
	}
	r.Recycle.Items, r.Recycle.Bytes, r.Exports.Items, r.Exports.Bytes = sum.RecycleItems, sum.RecycleBytes, sum.ExportItems, sum.ExportBytes
	r.Remotes.InGrace, r.Remotes.Overdue = sum.InGrace, sum.Overdue
	r.RecycleDays = s.recycleDays(c)
	r.MeasuredAt = optTime(sum.Measured)
	if c.isSiteAdmin() {
		if free, ok := diskFree(s.spaces.DataDir()); ok {
			r.DiskFreeBytes = &free
		}
	}
	return r, nil
}
