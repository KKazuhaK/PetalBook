package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"petalbook.app/server/internal/index"
	"petalbook.app/server/internal/space"
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

// viewList is the views of a request. It reads the list entry by entry and
// stops at the first one beyond the most a folder or tag may have, before it
// builds the rest: a body of empty objects would otherwise be turned into
// millions of views before any limit is looked at.
type viewList []space.View

func (l *viewList) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil // null: as if views was left out
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return errors.New("views must be a list")
	}
	out := viewList{}
	for dec.More() {
		if len(out) == space.MaxViews {
			return fmt.Errorf("a folder or a tag has at most %d views", space.MaxViews)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		var v space.View
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		out = append(out, v)
	}
	*l = out
	return nil
}

// putViews replaces the views of a folder or a tag.
func (s *server) putViews(c *call) {
	var req struct {
		Folder  *string   `json:"folder"`
		Tag     string    `json:"tag"`
		BaseRev string    `json:"baseRev"`
		Views   *viewList `json:"views"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, space.MaxViewsBody) {
		return
	}
	if req.BaseRev == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "A change needs baseRev, the rev of the views it started from.")
		return
	}
	if req.Views == nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "A change needs views, the whole list the folder or tag is to have.")
		return
	}
	l, err := c.sp.Space.PutViews(s.author(c), space.ViewScope{Folder: req.Folder, Tag: req.Tag}, req.BaseRev, []space.View(*req.Views))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toViews(l))
}

// getGroups counts the pages of a folder or a tag by the values of a property.
func (s *server) getGroups(c *call) {
	v := c.r.URL.Query()
	tags, ok := queryTags(v)
	if !ok {
		writeError(c.w, http.StatusBadRequest, "invalid", fmt.Sprintf("At most %d different tags.", maxQueryTags))
		return
	}
	q := index.GroupQuery{Tags: tags, Key: v.Get("key")}
	if q.Key == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "Key is the property to count by.")
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
		writeError(c.w, http.StatusBadRequest, "invalid", "Recursive must be true or false.")
		return
	}
	if q.Folder == nil && len(q.Tags) == 0 {
		writeError(c.w, http.StatusBadRequest, "invalid", "Give a folder or a tag to count the pages of.")
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
	return errorBody{Error: "conflict", Message: "The views changed since you opened them; here is how they are now.", Current: toViews(e.Current)}
}
