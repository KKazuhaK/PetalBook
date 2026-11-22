package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/KKazuhaK/PetalBook/internal/space"
)

type typeFieldResp struct {
	Key     string          `json:"key"`
	Title   string          `json:"title"`
	Kind    string          `json:"kind"`
	Options []string        `json:"options,omitempty"`
	Schema  json.RawMessage `json:"schema"`
}

type typeResp struct {
	Name   string          `json:"name"`
	Title  string          `json:"title"`
	Fields []typeFieldResp `json:"fields"`
	Error  string          `json:"error,omitempty"`
	Rev    string          `json:"rev"`
}

func toType(t space.TypeInfo) typeResp {
	out := typeResp{Name: t.Name, Title: t.Title, Fields: make([]typeFieldResp, len(t.Fields)), Error: t.Error, Rev: t.Rev}
	for i, f := range t.Fields {
		out.Fields[i] = typeFieldResp{Key: f.Key, Title: f.Title, Kind: f.Kind, Options: f.Options, Schema: f.Schema}
	}
	return out
}

func (s *server) getTypes(c *call) {
	types, bindings, err := c.sp.Space.Types()
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]typeResp, len(types))
	for i, t := range types {
		out[i] = toType(t)
	}
	if bindings == nil {
		bindings = []space.Binding{}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Types    []typeResp      `json:"types"`
		Bindings []space.Binding `json:"bindings"`
	}{out, bindings})
}

// putType makes a type or changes one.
func (s *server) putType(c *call) {
	var req struct {
		BaseRev string `json:"baseRev"`
		Title   string `json:"title"`
		Fields  []struct {
			Key     string   `json:"key"`
			Title   string   `json:"title"`
			Kind    string   `json:"kind"`
			Options []string `json:"options"`
		} `json:"fields"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	in := space.TypeInput{Title: req.Title}
	for _, f := range req.Fields {
		in.Fields = append(in.Fields, space.FieldInput{Key: f.Key, Title: f.Title, Kind: f.Kind, Options: f.Options})
	}
	t, err := c.sp.Space.PutType(s.author(c), c.r.PathValue("name"), req.BaseRev, in)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toType(t))
}

// deleteType removes a type and the bindings to it.
func (s *server) deleteType(c *call) {
	if err := c.sp.Space.DeleteType(s.author(c), c.r.PathValue("name"), c.r.URL.Query().Get("baseRev")); err != nil {
		if errors.Is(err, space.ErrNotFound) {
			writeError(c.w, http.StatusNotFound, "not_found", "no such type")
			return
		}
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// putFolderType binds a folder to a type, or unbinds it ("type": "").
func (s *server) putFolderType(c *call) {
	var req struct {
		Folder string `json:"folder"`
		Type   string `json:"type"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	folder, err := c.sp.Space.BindFolder(s.author(c), req.Folder, req.Type)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Folder string `json:"folder"`
		Type   string `json:"type"`
	}{folder, req.Type})
}
