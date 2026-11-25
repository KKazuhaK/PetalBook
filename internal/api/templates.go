package api

import (
	"errors"
	"net/http"

	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/space"
)

type templateResp struct {
	Name string    `json:"name"`
	Path string    `json:"path"`
	Rev  string    `json:"rev"`
	Meta page.Meta `json:"meta"`
	Body *string   `json:"body,omitempty"`
}

func toTemplate(t space.Template, withBody bool) templateResp {
	m := t.Meta
	if m == nil {
		m = page.Meta{}
	}
	out := templateResp{Name: t.Name, Path: t.Path, Rev: t.Rev, Meta: m}
	if withBody {
		out.Body = &t.Body
	}
	return out
}

type templateDefaultsResp struct {
	Folders   map[string]string `json:"folders"`
	QuickNote string            `json:"quickNote"`
}

func toTemplateDefaults(d space.TemplateDefaults) templateDefaultsResp {
	f := d.Folders
	if f == nil {
		f = map[string]string{}
	}
	return templateDefaultsResp{Folders: f, QuickNote: d.QuickNote}
}

func (s *server) getTemplates(c *call) {
	list, defaults, err := c.sp.Space.Templates()
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]templateResp, len(list))
	for i, t := range list {
		out[i] = toTemplate(t, false)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Templates []templateResp       `json:"templates"`
		Defaults  templateDefaultsResp `json:"defaults"`
	}{out, toTemplateDefaults(defaults)})
}

func (s *server) getTemplate(c *call) {
	t, err := c.sp.Space.Template(c.r.PathValue("name"))
	if errors.Is(err, space.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such template.")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toTemplate(*t, true))
}

func (s *server) putTemplate(c *call) {
	var req struct {
		BaseRev string     `json:"baseRev"`
		Meta    *page.Meta `json:"meta"`
		Body    *string    `json:"body"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	var meta page.Meta
	if req.Meta != nil {
		meta = *req.Meta
	}
	body := ""
	if req.Body != nil {
		body = *req.Body
	}
	t, err := c.sp.Space.PutTemplate(s.author(c), c.r.PathValue("name"), req.BaseRev, meta, body)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toTemplate(*t, true))
}

func (s *server) deleteTemplate(c *call) {
	err := c.sp.Space.DeleteTemplate(s.author(c), c.r.PathValue("name"), c.r.URL.Query().Get("baseRev"))
	if errors.Is(err, space.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such template.")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// putTemplateDefaults sets the default template of a folder, or the quick-note one.
func (s *server) putTemplateDefaults(c *call) {
	var req struct {
		Folder    *string `json:"folder"`
		Template  *string `json:"template"`
		QuickNote *string `json:"quickNote"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	var (
		d   space.TemplateDefaults
		err error
	)
	switch {
	case req.Folder != nil && req.QuickNote == nil && req.Template != nil:
		d, err = c.sp.Space.SetFolderTemplate(s.author(c), *req.Folder, *req.Template)
	case req.QuickNote != nil && req.Folder == nil && req.Template == nil:
		d, err = c.sp.Space.SetQuickNoteTemplate(s.author(c), *req.QuickNote)
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", `Send {"folder": …, "template": …} or {"quickNote": …}.`)
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toTemplateDefaults(d))
}
