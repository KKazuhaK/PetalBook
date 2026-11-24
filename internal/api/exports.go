package api

import (
	"errors"
	"net/http"
	"os"
	"strconv"

	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Exports (docs/api.md, "Exports"): a zip of what some spaces hold, made in the
// background, kept for exports.days, audited when it is made and when it is
// downloaded.

type exportResp struct {
	ID       string               `json:"id"`
	Tenant   string               `json:"tenant"`
	MadeBy   recycleUserRef       `json:"madeBy"`
	Spaces   []store.ExportSource `json:"spaces"`
	History  bool                 `json:"history"`
	State    string               `json:"state"`
	Error    string               `json:"error,omitempty"`
	Warnings []string             `json:"warnings"`
	Bytes    int64                `json:"bytes"`
	Created  string               `json:"created"`
	Expires  string               `json:"expires"`
}

func exportOf(e store.Export) exportResp {
	out := exportResp{ID: e.ID, Tenant: e.TenantID, MadeBy: recycleUserRef{e.MadeBy, e.MadeByName}, Spaces: e.Spaces, History: e.History, State: e.State,
		Bytes: e.Bytes, Created: rfc3339(e.Created), Expires: rfc3339(e.Expires), Warnings: []string{}}
	if out.Spaces == nil {
		out.Spaces = []store.ExportSource{}
	}
	switch e.State {
	case store.ExportFailed:
		out.Error = e.Error
	case store.ExportReady:
		// What a finished export could not include is listed, not hidden.
		for _, l := range splitLines(e.Error) {
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
		writeError(c.w, http.StatusBadRequest, "invalid", "name from 1 to "+strconv.Itoa(maxConfirmIDs)+" spaces or recycle items to export")
		return
	}
	if req.History && !c.isSiteAdmin() {
		writeError(c.w, http.StatusForbidden, "forbidden", "only an administrator of the whole server may export the history")
		return
	}
	sc := store.ScopeOf(c.user())
	var sources []store.ExportSource
	tenants := map[string]bool{}
	for _, id := range req.Spaces {
		sp, ok := s.adminSpaceFor(c, id)
		if !ok {
			return
		}
		sources = append(sources, store.ExportSource{Space: sp.ID, Name: sp.Name})
		tenants[sp.TenantID] = true
	}
	items, err := store.GetRecycleItems(c.r.Context(), s.opt.Store.Q(), sc, req.Items)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	for _, id := range req.Items {
		it, ok := items[id]
		if !ok || it.Kind != store.RecycleSpace || (it.Personal && !c.isSiteAdmin()) {
			writeError(c.w, http.StatusNotFound, "not_found", "no such item in the recycle bin")
			return
		}
		sources = append(sources, store.ExportSource{Item: it.ID, Name: it.Name})
		tenants[it.TenantID] = true
	}
	tenant := c.user().TenantID
	if len(tenants) == 1 {
		for t := range tenants {
			tenant = t
		}
	}
	e, err := s.spaces.StartExport(c.r.Context(), spaces.ExportRequest{TenantID: tenant, By: c.user().ID, ByName: c.user().Username, Sources: sources, History: req.History})
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
	writeJSON(c.w, http.StatusAccepted, exportOf(*e))
}

func (s *server) adminListExports(c *call) {
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
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	rows, next, err := store.ListExports(c.r.Context(), s.opt.Store.Q(), sc, q.Get("cursor"), limit)
	if errors.Is(err, store.ErrBadCursor) {
		writeError(c.w, http.StatusBadRequest, "invalid", "that cursor is not one this list gave")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	items := make([]exportResp, len(rows))
	for i, e := range rows {
		items[i] = exportOf(e)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []exportResp `json:"items"`
		Next  string       `json:"next"`
	}{items, next})
}

func (s *server) exportFound(c *call) (*store.Export, bool) {
	e, err := store.GetExport(c.r.Context(), s.opt.Store.Q(), store.ScopeOf(c.user()), c.r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "no such export")
		return nil, false
	}
	if err != nil {
		s.fail(c.w, err)
		return nil, false
	}
	return e, true
}

func (s *server) adminGetExport(c *call) {
	if e, ok := s.exportFound(c); ok {
		writeJSON(c.w, http.StatusOK, exportOf(*e))
	}
}

func (s *server) adminDeleteExport(c *call) {
	e, ok := s.exportFound(c)
	if !ok {
		return
	}
	if e.State == store.ExportRunning || e.State == store.ExportQueued {
		writeError(c.w, http.StatusConflict, "busy", "the export is being made")
		return
	}
	s.spaces.DeleteExportFile(e)
	if err := store.DeleteExport(c.r.Context(), s.opt.Store.Q(), e.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
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
	if e.State != store.ExportReady || s.spaces.ExportPath(e) == "" {
		writeError(c.w, http.StatusConflict, "busy", "the export is not ready")
		return
	}
	f, err := os.Open(s.spaces.ExportPath(e))
	if err != nil {
		writeError(c.w, http.StatusNotFound, "not_found", "the file of the export is gone")
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
	sc := store.ScopeOf(c.user())
	st, err := store.GetUserState(ctx, q, user)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !sc.All() && sc.Tenant() != st.Tenant) {
		writeError(c.w, http.StatusNotFound, "user_not_found", "there is no such user")
		return false
	}
	if err != nil {
		s.fail(c.w, err)
		return false
	}
	if st.Deleted {
		children, err := store.UserSpaceItems(ctx, q, user, st.Item)
		if err != nil {
			s.fail(c.w, err)
			return false
		}
		for _, ch := range children {
			if ch.State == store.RecycleReady && (!ch.Personal || c.isSiteAdmin()) {
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
		if !r.Personal || c.isSiteAdmin() {
			*spaceIDs = append(*spaceIDs, r.ID)
		}
	}
	return true
}
