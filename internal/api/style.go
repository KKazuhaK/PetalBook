package api

import (
	"net/http"

	"github.com/KKazuhaK/PetalBook/internal/space"
)

// getAppearance answers the icons and colours of the folders and the presets
// of the sources a space chose (null: it chose none, and the client's defaults
// apply).
func (s *server) getAppearance(c *call) {
	styles := c.sp.Space.Styles()
	presets, set := c.sp.Space.SourcePresets()
	var sp []string
	if set {
		sp = presets
	}
	writeJSON(c.w, http.StatusOK, struct {
		Folders       []space.FolderStyle `json:"folders"`
		SourcePresets []string            `json:"sourcePresets"`
		IconColors    []string            `json:"iconColors"`
	}{styles, sp, space.IconColors})
}

func (s *server) putFolderStyle(c *call) {
	var req struct {
		Folder string `json:"folder"`
		Icon   string `json:"icon"`
		Color  string `json:"color"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	folder, err := c.sp.Space.SetFolderStyle(s.author(c), req.Folder, req.Icon, req.Color)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, space.FolderStyle{Folder: folder, Icon: req.Icon, Color: req.Color})
}

// getSources answers the sources the pages use, most used first.
func (s *server) getSources(c *call) {
	list, err := c.sp.Space.UsedSources()
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Sources any `json:"sources"`
	}{list})
}

func (s *server) putSourcePresets(c *call) {
	var req struct {
		Presets *[]string `json:"presets"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	var p []string
	if req.Presets != nil {
		p = append([]string{}, *req.Presets...)
	}
	if err := c.sp.Space.SetSourcePresets(s.author(c), p); err != nil {
		s.fail(c.w, err)
		return
	}
	// What is stored: trimmed, without blanks and repeats; null when the space has chosen none.
	var stored []string
	if cur, set := c.sp.Space.SourcePresets(); set {
		stored = cur
	}
	writeJSON(c.w, http.StatusOK, struct {
		Presets []string `json:"presets"`
	}{stored})
}

func (s *server) renameTypeField(c *call) {
	var req struct {
		BaseRev string `json:"baseRev"`
		From    string `json:"from"`
		To      string `json:"to"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	r, err := c.sp.Space.RenameTypeField(s.author(c), c.r.PathValue("name"), req.BaseRev, req.From, req.To)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	skipped := r.Skipped
	if skipped == nil {
		skipped = []space.SkippedPage{}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Type    typeResp            `json:"type"`
		Pages   int                 `json:"pages"`
		Skipped []space.SkippedPage `json:"skipped"`
	}{toType(r.Type), r.Pages, skipped})
}
