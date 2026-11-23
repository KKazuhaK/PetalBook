package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/drafts"
	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/space"
)

// maxQueryTags is the most different tags a listing or a count may ask for:
// each is a condition in the query.
const maxQueryTags = 10

// queryTags reads the repeated tag parameter without its duplicates, and
// reports false when more than maxQueryTags different tags are given.
func queryTags(v url.Values) ([]string, bool) {
	var out []string
	seen := map[string]bool{}
	for _, t := range v["tag"] {
		if seen[t] {
			continue
		}
		if seen[t] = true; len(out) == maxQueryTags {
			return nil, false
		}
		out = append(out, t)
	}
	return out, true
}

func (s *server) listPages(c *call) {
	v := c.r.URL.Query()
	tags, ok := queryTags(v)
	if !ok {
		writeError(c.w, http.StatusBadRequest, "invalid", fmt.Sprintf("at most %d different tags", maxQueryTags))
		return
	}
	q := index.Query{Tags: tags, Text: v.Get("q"), Sort: v.Get("sort"), Cursor: v.Get("cursor")}
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
	switch in := v.Get("in"); in {
	case "":
	case "title":
		q.TitleOnly = true
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "in must be title")
		return
	}
	// A day in UTC: "from" is its first moment, "to" its last.
	day := func(name string) (time.Time, bool, bool) {
		raw := v.Get(name)
		if raw == "" {
			return time.Time{}, false, true
		}
		t, err := time.Parse("2006-01-02", raw)
		// Years the index can compare: file times are kept in nanoseconds
		// since 1970, which run out in 2262. A day outside is refused rather
		// than turned into a bound that means something else.
		if err != nil || t.Year() < 1970 || t.Year() > 2261 {
			writeError(c.w, http.StatusBadRequest, "invalid", name+" must be a day like 2026-11-01, from 1970 to 2261")
			return time.Time{}, false, false
		}
		return t, true, true
	}
	if t, set, ok := day("updatedFrom"); !ok {
		return
	} else if set {
		q.UpdatedFrom = t.UnixNano()
	}
	if t, set, ok := day("updatedTo"); !ok {
		return
	} else if set {
		q.UpdatedTo = t.AddDate(0, 0, 1).UnixNano()
	}
	if t, set, ok := day("createdFrom"); !ok {
		return
	} else if set {
		q.CreatedFrom = t.Format("2006-01-02")
	}
	if t, set, ok := day("createdTo"); !ok {
		return
	} else if set {
		q.CreatedTo = t.Format("2006-01-02")
	}
	switch v.Get("props") {
	case "", "0", "false":
	case "1", "true":
		q.Props = true
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "props must be 1 or 0")
		return
	}
	body := false
	switch v.Get("body") {
	case "", "0", "false":
	case "1", "true":
		body = true
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "body must be 1 or 0")
		return
	}
	if raw := v.Get("filter"); raw != "" {
		fs, err := parseFilters(raw)
		if err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		q.Filters = fs
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
	if body {
		// Every body is a file to read: only a few at a time.
		if q.Limit == 0 {
			q.Limit = space.MaxBodies
		}
		if q.Limit > space.MaxBodies {
			writeError(c.w, http.StatusBadRequest, "invalid", "with body=1 the limit is at most "+strconv.Itoa(space.MaxBodies))
			return
		}
	}
	res, err := c.sp.Space.List(q)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	items := make([]summaryResp, len(res.Items))
	types := c.sp.Space.TypeResolver()
	for i, it := range res.Items {
		items[i] = toSummary(it, types)
	}
	if body {
		bodies, err := c.sp.Space.Bodies(res.Items)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		for i := range items {
			items[i].Body = &bodies[i]
		}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []summaryResp `json:"items"`
		Next  string        `json:"next"`
	}{items, res.Next})
}

// maxFilterParam is the most a filter parameter may be.
const maxFilterParam = 128 << 10

// parseFilters reads the filter parameter: a JSON array of
// {"key", "op", "values"}. The values keep their JSON types (a number is a
// number, not text).
func parseFilters(raw string) ([]index.Filter, error) {
	const bad = "filter must be a JSON array of {\"key\", \"op\", \"values\"}"
	// Room for a board's "other values" column on a select field with as many
	// options as one may have (500 of 200 bytes), all excluded.
	if len(raw) > maxFilterParam {
		return nil, errors.New("filter is too long")
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	var in []struct {
		Key    string `json:"key"`
		Op     string `json:"op"`
		Values []any  `json:"values"`
	}
	if err := dec.Decode(&in); err != nil {
		return nil, errors.New(bad)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New(bad)
	}
	out := make([]index.Filter, len(in))
	for i, f := range in {
		out[i] = index.Filter{Key: f.Key, Op: f.Op, Values: f.Values}
	}
	return out, nil
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
	p, err := c.sp.Space.Create(s.author(c), space.CreateInput{Folder: req.Folder, Meta: req.Meta, Body: req.Body})
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

// patchProps changes properties of a page's header and nothing else.
func (s *server) patchProps(c *call) {
	id, ok := s.pageID(c)
	if !ok {
		return
	}
	var req struct {
		BaseRev string    `json:"baseRev"`
		Set     page.Meta `json:"set"`
		Unset   []string  `json:"unset"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	if req.BaseRev == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "a change needs baseRev, the rev of the page it started from")
		return
	}
	if len(req.Set) == 0 && len(req.Unset) == 0 {
		writeError(c.w, http.StatusBadRequest, "invalid", "a change needs set or unset")
		return
	}
	p, err := s.drafts.PatchProps(c.r.Context(), c.sp.Space, c.sp.Info.ID, c.user().ID, id, req.BaseRev, req.Set, req.Unset)
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
	p, err := c.sp.Space.Move(s.author(c), id, req.Folder)
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
	if err := s.drafts.DeletePageFile(c.r.Context(), c.sp.Space, c.sp.Info.ID, c.user().ID, id); err != nil {
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
	path, err := c.sp.Space.CreateFolder(s.author(c), req.Path)
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
	type tagResp struct {
		index.TagCount
		Color string `json:"color,omitempty"`
	}
	colors := c.sp.Space.TagColors()
	out := make([]tagResp, len(tags))
	for i, t := range tags {
		out[i] = tagResp{t, colors[t.Tag]}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Tags []tagResp `json:"tags"`
	}{out})
}

// putTag sets or removes the colour of a tag.
func (s *server) putTag(c *call) {
	tag := c.r.PathValue("tag")
	// Absent and null both take the colour away; anything else must be text.
	var req struct {
		Color json.RawMessage `json:"color"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	color := ""
	if raw := strings.TrimSpace(string(req.Color)); raw != "" && raw != "null" {
		if err := json.Unmarshal(req.Color, &color); err != nil || color == "" {
			writeError(c.w, http.StatusBadRequest, "invalid", "color must be the name of a colour, a colour written #rrggbb, or null")
			return
		}
		// Answered and stored in one form: lower case.
		if norm, ok := space.NormalTagColor(color); ok {
			color = norm
		}
	}
	if err := c.sp.Space.SetTagColor(s.author(c), tag, color); err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Tag   string `json:"tag"`
		Color string `json:"color,omitempty"`
	}{tag, color})
}

// batchPages moves or deletes several pages as one change.
func (s *server) batchPages(c *call) {
	var req struct {
		Op  string   `json:"op"`
		IDs []string `json:"ids"`
		// A pointer: a move must say where to, and "" (the root) is a place.
		Folder *string `json:"folder"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	var err error
	switch req.Op {
	case "move":
		if req.Folder == nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "a move needs the folder to move to (\"\" is the top level)")
			return
		}
		err = c.sp.Space.BatchMove(s.author(c), req.IDs, *req.Folder)
	case "delete":
		var gone []string
		gone, err = c.sp.Space.BatchDelete(s.author(c), req.IDs)
		// Also when it failed part way: what is deleted is deleted.
		s.dropDrafts(c, gone)
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "op must be move or delete")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Pages int `json:"pages"`
	}{len(req.IDs)})
}

// dropDrafts removes every user's drafts of pages that were just deleted. The
// pages are gone: a failure here is logged, not reported as theirs.
func (s *server) dropDrafts(c *call, ids []string) {
	if len(ids) == 0 {
		return
	}
	// Not the request's own context: the pages are deleted whether or not the
	// client is still there, and their drafts must not be left behind. Each
	// page has its own time, so one slow answer does not cost the rest theirs.
	for _, id := range ids {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.r.Context()), 5*time.Second)
		err := s.drafts.DeletePage(ctx, c.sp.Info.ID, id)
		cancel()
		if err != nil {
			s.log.Warn("could not discard the drafts of a deleted page", "space", c.sp.Info.ID, "page", id, "error", err)
		}
	}
}

func (s *server) moveFolder(c *call) {
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	p, err := c.sp.Space.MoveFolder(s.author(c), req.From, req.To)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Path string `json:"path"`
	}{p})
}

func (s *server) deleteFolder(c *call) {
	v := c.r.URL.Query()
	recursive := false
	switch v.Get("recursive") {
	case "", "false":
	case "true":
		recursive = true
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "recursive must be true or false")
		return
	}
	ids, err := c.sp.Space.DeleteFolder(s.author(c), v.Get("path"), recursive)
	// Also when it failed part way: what is deleted is deleted.
	s.dropDrafts(c, ids)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
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
