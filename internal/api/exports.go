package api

import (
	"errors"
	"net/http"
	"os"
	"strconv"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

// Exports (docs/api.md, "Exports"): a zip of what some spaces hold, made in the
// background by a job of kind "export" (internal/jobs), kept for exports.days,
// audited when it is made and when it is downloaded.

// exportSourceResp is one space an export holds: `space` for a live space and
// `item` for one that was in the recycle bin, `name` as it was, `spaceId` the
// space either way, and `open` whether it exists now and the caller may open it
// (so that the page links to it only then).
type exportSourceResp struct {
	Space   string `json:"space,omitempty"`
	Item    string `json:"item,omitempty"`
	Name    string `json:"name,omitempty"`
	SpaceID string `json:"spaceId"`
	Open    bool   `json:"open"`
}

type exportResp struct {
	ID       string             `json:"id"`
	Tenant   string             `json:"tenant"`
	MadeBy   recycleUserRef     `json:"madeBy"`
	Spaces   []exportSourceResp `json:"spaces"`
	History  bool               `json:"history"`
	State    string             `json:"state"`
	Error    string             `json:"error,omitempty"`
	Warnings []string           `json:"warnings"`
	Bytes    int64              `json:"bytes"`
	Created  string             `json:"created"`
	Expires  string             `json:"expires"`
	// Total, Done and BytesDone are the job's progress: the spaces made into the
	// zip so far, and the bytes written.
	Total     int64 `json:"total"`
	Done      int64 `json:"done"`
	BytesDone int64 `json:"bytesDone"`
}

// The states an export shows: its job's, as they have been since before it was
// one (a job that is done is ready; one that ended any other way is failed).
const (
	exportQueued  = "queued"
	exportRunning = "running"
	exportReady   = "ready"
	exportFailed  = "failed"
)

func exportStateOf(j *store.Job) string {
	switch j.State {
	case store.JobQueued:
		return exportQueued
	case store.JobRunning:
		return exportRunning
	case store.JobDone:
		return exportReady
	}
	return exportFailed
}

// exportsOf is the answer's form of exports: one query for all the spaces they
// hold says which can be opened by the caller.
func (s *server) exportsOf(c *call, rows []*store.Job) ([]exportResp, error) {
	var ids []string
	for _, j := range rows {
		for _, src := range store.ExportParamsOf(j).Spaces {
			ids = append(ids, sourceSpace(src))
		}
	}
	open, err := store.OpenableSpaces(c.r.Context(), s.opt.Store.Q(), c.adminReach(), ids)
	if err != nil {
		return nil, err
	}
	out := make([]exportResp, len(rows))
	for i, j := range rows {
		out[i] = exportOf(j, open)
	}
	return out, nil
}

// sourceSpace is the space an export source holds.
func sourceSpace(src store.ExportSource) string {
	if src.SpaceID != "" {
		return src.SpaceID
	}
	return src.Space
}

func exportOf(j *store.Job, open map[string]bool) exportResp {
	params := store.ExportParamsOf(j)
	out := exportResp{ID: j.ID, Tenant: j.TenantID, MadeBy: recycleUserRef{j.MadeBy, j.MadeByName}, Spaces: make([]exportSourceResp, len(params.Spaces)), History: params.History,
		State: exportStateOf(j), Created: rfc3339(j.Created), Expires: rfc3339(j.Expires), Warnings: []string{}, Total: j.Total, Done: j.Done, BytesDone: j.BytesDone}
	for i, src := range params.Spaces {
		id := sourceSpace(src)
		out.Spaces[i] = exportSourceResp{Space: src.Space, Item: src.Item, Name: src.Name, SpaceID: id, Open: open[id]}
	}
	switch out.State {
	case exportFailed:
		out.Error = j.Error
		if out.Error == "" {
			out.Error = "The server stopped before the export was made."
		}
	case exportReady:
		out.Bytes = j.BytesDone
		// What a finished export could not include is listed, not hidden.
		for _, l := range splitLines(store.ExportResultOf(j).Warnings) {
			out.Warnings = append(out.Warnings, l)
		}
	}
	return out
}

func (s *server) adminCreateExport(c *call) {
	var req struct {
		Spaces  []string `json:"spaces"`
		Items   []string `json:"items"`
		User    *string  `json:"user"`
		History bool     `json:"history"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	if req.User != nil && *req.User != "" && !s.exportUserSources(c, &req.Spaces, &req.Items, *req.User) {
		return
	}
	if n := len(req.Spaces) + len(req.Items); n == 0 || n > maxConfirmIDs {
		writeError(c.w, http.StatusBadRequest, "invalid", "Name from 1 to "+strconv.Itoa(maxConfirmIDs)+" spaces or recycle items to export.")
		return
	}
	if req.History && !c.siteWide(authz.TenantDataExport) {
		writeError(c.w, http.StatusForbidden, "forbidden", "Only an administrator of the whole server may export the history.")
		return
	}
	sc := c.adminReach()
	var sources []store.ExportSource
	tenants := map[string]bool{}
	personal := false
	for _, id := range req.Spaces {
		sp, ok := s.adminSpaceFor(c, id)
		if !ok {
			return
		}
		sources = append(sources, store.ExportSource{Space: sp.ID, SpaceID: sp.ID, Name: sp.Name})
		tenants[sp.TenantID] = true
		personal = personal || sp.Personal
	}
	items, err := store.GetRecycleItems(c.r.Context(), s.opt.Store.Q(), sc, req.Items)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	for _, id := range req.Items {
		it, ok := items[id]
		if !ok || it.Kind != store.RecycleSpace || (it.Personal && !c.can(authz.SiteSpacesPersonalContent)) {
			writeError(c.w, http.StatusNotFound, "not_found", "No such item in the recycle bin.")
			return
		}
		sources = append(sources, store.ExportSource{Item: it.ID, SpaceID: it.SpaceID, Name: it.Name})
		tenants[it.TenantID] = true
		personal = personal || it.Personal
	}
	tenant := c.tenant()
	if len(tenants) == 1 {
		for t := range tenants {
			tenant = t
		}
	}
	e, err := s.spaces.StartExport(c.r.Context(), spaces.ExportRequest{TenantID: tenant, By: c.user().ID, ByName: c.user().Username, Capacity: c.scope.capacity, Sources: sources, History: req.History,
		SiteOnly: exportSiteOnly(c.siteWide(authz.TenantDataExport), req.History, personal, len(tenants))})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	ids := make([]string, 0, len(sources))
	for _, src := range sources {
		if src.Space != "" {
			ids = append(ids, src.Space)
		} else {
			ids = append(ids, src.Item)
		}
	}
	s.auditRow(c, "export_created", tenant, map[string]any{"export": e.ID, "spaces": ids, "history": req.History})
	writeJSON(c.w, http.StatusAccepted, struct {
		Job jobResp `json:"job"`
	}{jobOf(e)})
}

// exportSiteOnly says whether an export is the site's: one made by who exports
// across the site (siteWide: a site administrator), and in any case one that
// holds a history bundle, a personal space or the spaces of more than one
// tenant. A tenant administrator never sees such an
// export, whatever tenant its row names (docs/api.md, "Exports": what a tenant
// administrator made is seen by that tenant's administrators and the site's).
func exportSiteOnly(siteWide, history, personal bool, tenants int) bool {
	return siteWide || history || personal || tenants > 1
}

func (s *server) adminListExports(c *call) {
	q := c.r.URL.Query()
	limit := defaultAdminLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxAdminLimit {
			writeError(c.w, http.StatusBadRequest, "invalid", "Limit is a number from 1 to "+strconv.Itoa(maxAdminLimit)+".")
			return
		}
		limit = n
	}
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	rows, next, err := store.ListJobs(c.r.Context(), s.opt.Store.Q(), sc, store.JobFilter{Kinds: []string{store.JobExport}, After: q.Get("cursor"), Limit: limit})
	if errors.Is(err, store.ErrBadCursor) {
		writeError(c.w, http.StatusBadRequest, "invalid", "That cursor is not one this list gave.")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	items, err := s.exportsOf(c, rows)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []exportResp `json:"items"`
		Next  string       `json:"next"`
	}{items, next})
}

func (s *server) exportFound(c *call) (*store.Job, bool) {
	e, err := store.GetJob(c.r.Context(), s.opt.Store.Q(), c.reach(), c.r.PathValue("id"))
	if err == nil && e.Kind != store.JobExport {
		err = store.ErrNotFound
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such export.")
		return nil, false
	}
	if err != nil {
		s.fail(c.w, err)
		return nil, false
	}
	return e, true
}

func (s *server) adminDeleteExport(c *call) {
	e, ok := s.exportFound(c)
	if !ok {
		return
	}
	if !store.JobEnded(e.State) {
		writeError(c.w, http.StatusConflict, "busy", "The export is being made.")
		return
	}
	s.spaces.DeleteExportFile(e)
	if err := store.DeleteJob(c.r.Context(), s.opt.Store.Q(), e.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(c.w, err)
		return
	}
	s.auditRow(c, "export_deleted", e.TenantID, map[string]any{"export": e.ID})
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) adminDownloadExport(c *call) {
	e, ok := s.exportFound(c)
	if !ok {
		return
	}
	if e.State != store.JobDone || s.spaces.ExportPath(e) == "" {
		writeError(c.w, http.StatusConflict, "busy", "The export is not ready.")
		return
	}
	f, err := os.Open(s.spaces.ExportPath(e))
	if err != nil {
		writeError(c.w, http.StatusNotFound, "not_found", "The file of the export is gone.")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.auditRow(c, "export_downloaded", e.TenantID, map[string]any{"export": e.ID})
	h := c.w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="petalbook-export-`+e.ID+`.zip"`)
	h.Set("Cache-Control", "private, no-store")
	http.ServeContent(c.w, c.r, "", st.ModTime(), f)
}

// exportUserSources adds what an export of a person holds: their personal space
// and the spaces nobody else is in (rules 1 and 2 of deleting them), or, for a
// person who is in the recycle bin, the items recycled with them. A personal
// space is left out for a tenant administrator, who never sees one. It answers
// and reports false for a person who is not in the caller's scope.
func (s *server) exportUserSources(c *call, spaceIDs, itemIDs *[]string, user string) bool {
	ctx := c.r.Context()
	q := s.opt.Store.Q()
	sc := c.adminReach()
	st, err := store.GetUserState(ctx, q, user)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !sc.All() && sc.Tenant() != st.Tenant) {
		writeError(c.w, http.StatusNotFound, "user_not_found", "There is no such user.")
		return false
	}
	if err != nil {
		s.fail(c.w, err)
		return false
	}
	if st.Deleted {
		children, err := store.UserSpaceItems(ctx, q, st.Item)
		if err != nil {
			s.fail(c.w, err)
			return false
		}
		for _, ch := range children {
			if ch.State == store.RecycleReady && (!ch.Personal || c.can(authz.SiteSpacesPersonalContent)) {
				*itemIDs = append(*itemIDs, ch.ID)
			}
		}
		return true
	}
	plan, err := store.PlanUserDeletion(ctx, q, sc, user, "", nil)
	if err != nil {
		s.fail(c.w, err)
		return false
	}
	for _, r := range plan.Recycle {
		if !r.Personal || c.can(authz.SiteSpacesPersonalContent) {
			*spaceIDs = append(*spaceIDs, r.ID)
		}
	}
	return true
}
