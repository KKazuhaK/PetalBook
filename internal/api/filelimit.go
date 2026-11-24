package api

import (
	"context"
	"net/http"

	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// The size of one file (files.maxBytes, docs/api.md "The size of one file").
// It is read from the settings cache, one map lookup, never per layer.

// fileLimitFor is the largest file a write by someone of the tenant may
// create or replace: the tenant's files.maxBytes, never above --max-upload.
func (s *server) fileLimitFor(ctx context.Context, tenant string) int64 {
	if s.single || s.settings == nil {
		return s.opt.MaxUpload
	}
	if n := s.settings.FileLimit(ctx, tenant); n > 0 {
		return n
	}
	return s.opt.MaxUpload
}

// bindDraftFileLimit gives the draft service the limit of a person, for the
// drafts its sweeper commits with no request to carry it: their tenant's.
func (s *server) bindDraftFileLimit() {
	if s.opt.Drafts == nil {
		return
	}
	s.opt.Drafts.SetFileLimit(func(ctx context.Context, userID string) int64 {
		tenant := ""
		if !s.single {
			if t, err := store.UserTenant(ctx, s.opt.Store.Q(), userID); err == nil {
				tenant = t
			}
		}
		return s.fileLimitFor(ctx, tenant)
	})
}

// fileLimit is fileLimitFor the caller's tenant.
func (s *server) fileLimit(c *call) int64 {
	return s.fileLimitFor(c.r.Context(), c.user().TenantID)
}

// tooLarge answers 413 file_too_large. size is 0 when the body was cut off at
// the limit and its size is not known.
func tooLarge(w http.ResponseWriter, limit, size int64, name string) {
	writeJSON(w, http.StatusRequestEntityTooLarge, fileTooLargeBody(limit, size, name))
}

func fileTooLargeBody(limit, size int64, name string) errorBody {
	b := errorBody{Error: "file_too_large", Name: name, Limit: &limit,
		Message: "the file is larger than " + sizeText(limit) + ", the most a file may be here"}
	if size > 0 {
		b.Size = &size
	}
	return b
}

// checkSize refuses a file of size bytes over the caller's limit, answering
// 413 file_too_large, and reports whether the file may go on.
func (s *server) checkSize(c *call, name string, size int64) bool {
	if limit := s.fileLimit(c); limit > 0 && size > limit {
		tooLarge(c.w, limit, size, name)
		return false
	}
	return true
}

// withFileLimit puts the caller's limit in the request's context, for the code
// that writes on the caller's behalf without being handed an author (drafts).
func (s *server) withFileLimit(c *call) {
	if c.p == nil {
		return
	}
	c.r = c.r.WithContext(space.ContextWithMaxFile(c.r.Context(), s.fileLimit(c)))
}

// pageFileSize is the size of the file of a page with this header and body.
func pageFileSize(meta page.Meta, body string) int64 {
	doc := page.Parse(nil)
	if err := doc.SetMeta(meta); err != nil {
		return int64(len(body))
	}
	doc.SetBody(body)
	return int64(len(doc.Bytes()))
}
