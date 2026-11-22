package api

import (
	"net/http"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/space"
)

type viewsResp struct {
	Views []space.View `json:"views"`
	Rev   string       `json:"rev"`
}

func toViews(l space.ViewList) viewsResp { return viewsResp{Views: l.Views, Rev: l.Rev} }

// getViews lists the views of a folder or a tag.
func (s *server) getViews(c *call) {
	v := c.r.URL.Query()
	scope := space.ViewScope{Tag: v.Get("tag")}
	if v.Has("folder") {
		f := v.Get("folder")
		scope.Folder = &f
	}
	l, err := c.sp.Space.Views(scope)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toViews(l))
}

// putViews replaces the views of a folder or a tag.
func (s *server) putViews(c *call) {
	var req struct {
		Folder  *string       `json:"folder"`
		Tag     string        `json:"tag"`
		BaseRev string        `json:"baseRev"`
		Views   *[]space.View `json:"views"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	if req.BaseRev == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "a change needs baseRev, the rev of the views it started from")
		return
	}
	if req.Views == nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "a change needs views, the whole list the folder or tag is to have")
		return
	}
	l, err := c.sp.Space.PutViews(s.author(c), space.ViewScope{Folder: req.Folder, Tag: req.Tag}, req.BaseRev, *req.Views)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toViews(l))
}

// getGroups counts the pages of a folder or a tag by the values of a property.
func (s *server) getGroups(c *call) {
	v := c.r.URL.Query()
	q := index.GroupQuery{Tags: v["tag"], Key: v.Get("key")}
	if q.Key == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "key is the property to count by")
		return
	}
	if v.Has("folder") {
		f := v.Get("folder")
		if err := space.ValidFolder(f); err != nil {
			s.fail(c.w, err)
			return
		}
		q.Folder = &f
	}
	switch v.Get("recursive") {
	case "", "false":
	case "true":
		q.Recursive = true
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "recursive must be true or false")
		return
	}
	if q.Folder == nil && len(q.Tags) == 0 {
		writeError(c.w, http.StatusBadRequest, "invalid", "give a folder or a tag to count the pages of")
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
	res, err := c.sp.Space.Groups(q)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, res)
}

// viewsConflict is the body of the 409 of PUT views.
func viewsConflict(e *space.ViewsConflictError) errorBody {
	return errorBody{Error: "conflict", Message: "the views changed since you opened them; here is how they are now", Current: toViews(e.Current)}
}
