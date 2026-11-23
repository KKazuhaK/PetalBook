package api

import (
	"encoding/json"
	"net/http"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/space"
)

type problemResp struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	ID      string `json:"id,omitempty"`
	Message string `json:"message"`
	// Rev is the current rev of the file the problem is in: the baseRev of a
	// fix (a file without an id has no page to read it from).
	Rev    string          `json:"rev,omitempty"`
	Detail json.RawMessage `json:"detail,omitempty"`
	Fix    *string         `json:"fix"`
}

// getChecks lists what is wrong in the space, from the index (docs/api.md,
// "Checks").
func (s *server) getChecks(c *call) {
	q := c.r.URL.Query()
	counts, items, next, err := c.sp.Space.Checks(q.Get("kind"), q.Get("cursor"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]problemResp, len(items))
	for i, it := range items {
		out[i] = problemResp{Kind: it.Kind, Path: it.Path, ID: it.ID, Message: it.Message, Rev: it.Rev, Detail: it.Detail}
		if it.Fix != "" {
			f := it.Fix
			out[i].Fix = &f
		}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Counts map[string]int `json:"counts"`
		Items  []problemResp  `json:"items"`
		Next   string         `json:"next"`
	}{counts, out, next})
}

// fixCheck makes one of the two fixes of the check page, as a save does.
func (s *server) fixCheck(c *call) {
	var req struct {
		Kind    string `json:"kind"`
		Path    string `json:"path"`
		BaseRev string `json:"baseRev"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	if req.Kind != index.KindNoID && req.Kind != index.KindNotInType {
		writeJSON(c.w, http.StatusBadRequest, errorBody{Error: "not_fixable", Message: "only problems of the kinds no_id and not_in_type can be fixed here"})
		return
	}
	if req.Path == "" || req.BaseRev == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "a fix needs path and baseRev, the rev of the page's file as it was listed")
		return
	}
	var p *space.Page
	var err error
	switch req.Kind {
	case index.KindNoID:
		p, err = c.sp.Space.FixNoID(s.author(c), req.Path, req.BaseRev)
	default:
		id, ok := c.sp.Space.PageIDByPath(req.Path)
		if !ok {
			err = space.ErrNotFound
			break
		}
		p, err = s.drafts.FixNotInType(c.r.Context(), c.sp.Space, c.sp.Info.ID, c.user().ID, id, req.BaseRev)
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toPage(p))
}
