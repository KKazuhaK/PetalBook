package api

import (
	"errors"
	"io"
	"net/http"
	"path"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/importer"
	"github.com/KKazuhaK/PetalBook/internal/space"
)

// importMarkdown brings one Markdown file or a zip of them into the space
// (docs/api.md, "Import"). Reading and deciding is the importer's; creating
// the pages is one change of the space.
func (s *server) importMarkdown(c *call) {
	folder := c.r.URL.Query().Get("folder")
	if err := space.ValidFolder(folder); err != nil {
		s.fail(c.w, err)
		return
	}
	if err := space.ValidNewFolder(folder); err != nil {
		s.fail(c.w, err)
		return
	}
	// One import at a time: each holds its file and what it unpacks in memory.
	select {
	case s.importing <- struct{}{}:
		defer func() { <-s.importing }()
	case <-c.r.Context().Done():
		return
	}
	max := s.opt.MaxImport
	rc := http.NewResponseController(c.w)
	_ = rc.SetReadDeadline(time.Now().Add(uploadTimeout))
	_ = rc.SetWriteDeadline(time.Now().Add(2 * uploadTimeout))
	data, err := io.ReadAll(http.MaxBytesReader(c.w, c.r.Body, max))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(c.w, http.StatusRequestEntityTooLarge, "invalid", "the file is larger than "+sizeText(max)+", the most this server imports at once")
		} else {
			writeError(c.w, http.StatusBadRequest, "invalid", "could not read the file")
		}
		return
	}
	if len(data) == 0 {
		writeError(c.w, http.StatusBadRequest, "invalid", "the file is empty")
		return
	}
	var plan *importer.Plan
	if importer.IsZip(data) {
		plan, err = importer.Zip(data, importer.Options{MaxPage: s.opt.MaxBody, MaxAsset: s.opt.MaxUpload, MaxTotal: 4 * max,
			ValidFolder: func(rel string) error { return space.ValidNewFolder(path.Join(folder, rel)) }})
	} else {
		plan, err = importer.Single(c.r.URL.Query().Get("name"), data, s.opt.MaxBody)
	}
	if err != nil {
		var ie *importer.Error
		if errors.As(err, &ie) {
			writeError(c.w, http.StatusBadRequest, "invalid", ie.Msg)
			return
		}
		s.fail(c.w, err)
		return
	}
	items := make([]space.ImportPage, len(plan.Pages))
	for i, p := range plan.Pages {
		it := space.ImportPage{Folder: p.Folder, Meta: p.Meta, Body: p.Body}
		for _, a := range p.Assets {
			it.Assets = append(it.Assets, space.ImportAsset{Token: a.Token, Name: a.Name, Data: a.Data})
		}
		items[i] = it
	}
	extra := int64(len(data))
	var planned int64
	for _, it := range items {
		planned += int64(len(it.Body))
		for _, a := range it.Assets {
			planned += int64(len(a.Data))
		}
	}
	if planned > extra {
		extra = planned
	}
	if err := s.spaces.CheckRoom(c.r.Context(), c.sp, extra); err != nil {
		s.fail(c.w, err)
		return
	}
	pages, files, err := c.sp.Space.Import(s.author(c), folder, items)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	skipped := plan.Skipped
	if skipped == nil {
		skipped = []importer.Skipped{}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Pages       int                `json:"pages"`
		Files       int                `json:"files"`
		Skipped     []importer.Skipped `json:"skipped"`
		SkippedMore int                `json:"skippedMore,omitempty"`
	}{pages, files, skipped, plan.SkippedMore})
}
