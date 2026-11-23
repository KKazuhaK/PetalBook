package api

import (
	"net/http"
)

type backlinkResp struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Title    string `json:"title"`
	Untitled bool   `json:"untitled"`
	Excerpt  string `json:"excerpt"`
}

// getBacklinks lists the pages that link to a page.
func (s *server) getBacklinks(c *call) {
	id, ok := s.pageID(c)
	if !ok {
		return
	}
	items, next, total, err := c.sp.Space.Backlinks(id, c.r.URL.Query().Get("cursor"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]backlinkResp, len(items))
	for i, b := range items {
		out[i] = backlinkResp{ID: b.ID, Path: b.Path, Title: b.Title, Untitled: b.Untitled, Excerpt: b.Excerpt}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []backlinkResp `json:"items"`
		Next  string         `json:"next"`
		Total int            `json:"total"`
	}{out, next, total})
}

type linkTargetResp struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Title    string `json:"title"`
	Untitled bool   `json:"untitled"`
}

type linkResp struct {
	Text   string          `json:"text"`
	Path   string          `json:"path"`
	ID     string          `json:"id,omitempty"`
	Anchor string          `json:"anchor,omitempty"`
	Broken bool            `json:"broken"`
	Target *linkTargetResp `json:"target,omitempty"`
}

// getLinks lists the links of a page, each with the page it leads to now.
func (s *server) getLinks(c *call) {
	id, ok := s.pageID(c)
	if !ok {
		return
	}
	ls, err := c.sp.Space.Links(id)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]linkResp, len(ls))
	for i, l := range ls {
		out[i] = linkResp{Text: l.Text, Path: l.Path, ID: l.ID, Anchor: l.Anchor, Broken: l.Target == nil}
		if t := l.Target; t != nil {
			out[i].Target = &linkTargetResp{ID: t.ID, Path: t.Path, Title: t.Title, Untitled: t.Untitled}
		}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []linkResp `json:"items"`
	}{out})
}
