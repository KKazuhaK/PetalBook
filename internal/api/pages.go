package api

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/KazuhaHub/PetalBook/internal/drafts"
	"github.com/KazuhaHub/PetalBook/internal/index"
	"github.com/KazuhaHub/PetalBook/internal/page"
	"github.com/KazuhaHub/PetalBook/internal/space"
)

func (s *server) listPages(c *call) {
	v := c.r.URL.Query()
	q := index.Query{Tags: v["tag"], Text: v.Get("q"), Sort: v.Get("sort"), Cursor: v.Get("cursor")}
	if v.Has("folder") {
		f := v.Get("folder")
		if err := space.ValidFolder(f); err != nil {
			s.fail(c.w, err)
			return
		}
		q.Folder = &f
	}
	switch rec := v.Get("recursive"); rec {
	case "", "false":
	case "true":
		q.Recursive = true
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "recursive must be true or false")
		return
	}
	if l := v.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "limit must be a number from 1 to 200")
			return
		}
		q.Limit = n
		if n == 0 { // 0 means "default" inside the index; over HTTP it is out of range
			q.Limit = -1
		}
	}
	res, err := c.sp.Space.List(q)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	items := make([]summaryResp, len(res.Items))
	for i, it := range res.Items {
		items[i] = toSummary(it)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []summaryResp `json:"items"`
		Next  string        `json:"next"`
	}{items, res.Next})
}

type draftResp struct {
	Version string    `json:"version"`
	BaseRev string    `json:"baseRev"`
	Meta    page.Meta `json:"meta"`
	Body    string    `json:"body"`
	Updated string    `json:"updated"`
	Stale   bool      `json:"stale"`
}

func toDraftResp(d *drafts.Draft) *draftResp {
	return &draftResp{Version: d.Version, BaseRev: d.BaseRev, Meta: d.Meta, Body: d.Body, Updated: rfc3339(d.Updated), Stale: d.Stale}
}

// pageWithDraft is a page plus the caller's draft of it, if there is one.
type pageWithDraft struct {
	pageResp
	Draft *draftResp `json:"draft,omitempty"`
}

func (s *server) getPage(c *call) {
	id, ok := s.pageID(c)
	if !ok {
		return
	}
	p, err := c.sp.Space.Get(id)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := pageWithDraft{pageResp: toPage(p)}
	d, err := s.drafts.Get(c.r.Context(), c.sp.Info.ID, id, c.user().ID, p.Rev)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if d != nil {
		out.Draft = toDraftResp(d)
	}
	writeJSON(c.w, http.StatusOK, out)
}

func (s *server) createPage(c *call) {
	var req struct {
		Folder string    `json:"folder"`
		Meta   page.Meta `json:"meta"`
		Body   string    `json:"body"`
	}
	if !s.readJSON(c.w, c.r, &req, true) {
		return
	}
	p, err := c.sp.Space.Create(space.CreateInput{Folder: req.Folder, Meta: req.Meta, Body: req.Body})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.Header().Set("Location", "/api/spaces/"+url.PathEscape(c.sp.Info.ID)+"/pages/"+url.PathEscape(p.ID))
	writeJSON(c.w, http.StatusCreated, toPage(p))
}

// saveRequest is the body of a save and of a draft.
type saveRequest struct {
	Prev    string     `json:"prev"` // PUT draft only
	BaseRev string     `json:"baseRev"`
	Meta    *page.Meta `json:"meta"`
	Body    *string    `json:"body"`
}

// readSave reads and checks the body of PUT page and PUT draft, which are
// the same shape with the same rules.
func (s *server) readSave(c *call) (saveRequest, bool) {
	var req saveRequest
	if !s.readJSON(c.w, c.r, &req, false) {
		return req, false
	}
	// A save replaces the whole page, so a request that leaves out the header
	// or the body would silently empty it. Refuse instead of guessing.
	if req.Meta == nil || req.Body == nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "a save needs both meta and body; send the page's current values for the part that did not change")
		return req, false
	}
	// A save without the rev it started from is a malformed request, not a
	// conflict: there is no other version to show the person.
	if req.BaseRev == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "a save needs baseRev, the rev of the page it started from")
		return req, false
	}
	return req, true
}

func (s *server) savePage(c *call) {
	id, ok := s.pageID(c)
	if !ok {
		return
	}
	req, ok := s.readSave(c)
	if !ok {
		return
	}
	// The file will hold what the person sent; the draft they had of it is spent.
	p, err := s.drafts.SaveFile(c.r.Context(), c.sp.Space, c.sp.Info.ID, c.user().ID, id, req.BaseRev, *req.Meta, *req.Body)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toPage(p))
}

func (s *server) movePage(c *call) {
	id, ok := s.pageID(c)
	if !ok {
		return
	}
	var req struct {
		Folder string `json:"folder"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	p, err := c.sp.Space.Move(id, req.Folder)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toPage(p))
}

func (s *server) deletePage(c *call) {
	id, ok := s.pageID(c)
	if !ok {
		return
	}
	if err := s.drafts.DeletePageFile(c.r.Context(), c.sp.Space, c.sp.Info.ID, id); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) getTree(c *call) {
	folders, err := c.sp.Space.Tree()
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Folders []index.FolderCount `json:"folders"`
	}{folders})
}

func (s *server) createFolder(c *call) {
	var req struct {
		Path string `json:"path"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	path, err := c.sp.Space.CreateFolder(req.Path)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusCreated, struct {
		Path string `json:"path"`
	}{path})
}

func (s *server) getTags(c *call) {
	tags, err := c.sp.Space.Tags()
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Tags []index.TagCount `json:"tags"`
	}{tags})
}

func (s *server) reindex(c *call) {
	st, err := c.sp.Space.Reindex()
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Pages int   `json:"pages"`
		Ms    int64 `json:"ms"`
	}{st.Pages, st.Elapsed.Milliseconds()})
}

// --- drafts ------------------------------------------------------------------

func (s *server) putDraft(c *call) {
	id, ok := s.pageID(c)
	if !ok {
		return
	}
	req, ok := s.readSave(c)
	if !ok {
		return
	}
	st, err := s.drafts.Put(c.r.Context(), c.sp.Space, c.sp.Info.ID, c.user().ID, id, req.Prev, req.BaseRev, *req.Meta, *req.Body)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Version string `json:"version"`
		BaseRev string `json:"baseRev"`
		Updated string `json:"updated"`
		Stale   bool   `json:"stale"`
	}{st.Version, st.BaseRev, rfc3339(st.Updated), st.Stale})
}

func (s *server) deleteDraft(c *call) {
	id, ok := s.pageID(c)
	if !ok {
		return
	}
	if err := s.drafts.Delete(c.r.Context(), c.sp.Space, c.sp.Info.ID, id, c.user().ID, c.r.URL.Query().Get("version"), c.r.URL.Query().Get("rev")); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) commitDraft(c *call) {
	id, ok := s.pageID(c)
	if !ok {
		return
	}
	var req struct {
		Version string `json:"version"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	if req.Version == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "a commit needs version, the version of the draft to commit")
		return
	}
	p, err := s.drafts.Commit(c.r.Context(), c.sp.Space, c.sp.Info.ID, c.user().ID, id, req.Version)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toPage(p))
}
