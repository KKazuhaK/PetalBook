package api

import (
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/space"
)

// inlineTypes are the files a browser may show in place: the content type is
// decided here, by extension, never by what the file claims or looks like.
// Everything else is downloaded.
var inlineTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".svg": "image/svg+xml", ".bmp": "image/bmp",
	".ico": "image/x-icon",
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".ogg": "audio/ogg", ".wav": "audio/wav",
	".mp4": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",
	".pdf": "application/pdf",
	".txt": "text/plain; charset=utf-8",
}

// fileCSP keeps a file that is opened as a document (an SVG, a PDF) from
// running scripts or loading anything, and gives it an origin of its own.
const fileCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

func (s *server) putAsset(c *call) {
	id, ok := s.pageID(c)
	if !ok {
		return
	}
	name := c.r.URL.Query().Get("name")
	if strings.TrimSpace(name) == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "give the file's name in the name parameter")
		return
	}
	max := s.opt.MaxUpload
	// The server's read timeout is sized for JSON; a file over a slow link
	// needs longer. A failure to extend it only means the default applies.
	// The write deadline runs from when the request began, so it moves as well.
	rc := http.NewResponseController(c.w)
	_ = rc.SetReadDeadline(time.Now().Add(uploadTimeout))
	_ = rc.SetWriteDeadline(time.Now().Add(uploadTimeout + time.Minute))
	data, err := io.ReadAll(http.MaxBytesReader(c.w, c.r.Body, max))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(c.w, http.StatusRequestEntityTooLarge, "invalid", "the file is larger than "+sizeText(max)+", the most this server accepts")
		} else {
			writeError(c.w, http.StatusBadRequest, "invalid", "could not read the file")
		}
		return
	}
	if len(data) == 0 {
		writeError(c.w, http.StatusBadRequest, "invalid", "the file is empty")
		return
	}
	if err := s.spaces.CheckRoom(c.r.Context(), c.sp, int64(len(data))); err != nil {
		s.fail(c.w, err)
		return
	}
	a, err := c.sp.Space.PutAsset(s.author(c), id, name, data)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	status := http.StatusOK
	if a.Created {
		status = http.StatusCreated
	}
	writeJSON(c.w, status, struct {
		Path string `json:"path"`
		Name string `json:"name"`
		Size int64  `json:"size"`
	}{"/" + a.Rel, a.Name, a.Size})
}

// uploadTimeout is how long the body of an upload may take to arrive.
const uploadTimeout = 10 * time.Minute

// filenameStar encodes a file name for `filename*=UTF-8”…` (RFC 5987): only
// attr-char stays as it is, everything else is percent-encoded byte by byte.
func filenameStar(name string) string {
	const hexdigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexdigits[c>>4])
		b.WriteByte(hexdigits[c&15])
	}
	return b.String()
}

func sizeText(n int64) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return strconv.FormatInt(n>>20, 10) + " MiB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

func (s *server) getFile(c *call) {
	rel := c.r.PathValue("path")
	f, st, etag, err := c.sp.Space.OpenFileTag(c.r.Context(), rel)
	if err != nil {
		if errors.Is(err, space.ErrNotFound) {
			writeError(c.w, http.StatusNotFound, "not_found", "no such file")
			return
		}
		s.fail(c.w, err)
		return
	}
	defer f.Close()
	h := c.w.Header()
	name := path.Base(rel)
	ct, inline := inlineTypes[strings.ToLower(path.Ext(name))]
	disposition := "inline"
	if !inline {
		ct, disposition = "application/octet-stream", "attachment"
	}
	h.Set("Content-Type", ct)
	// filename* carries any name safely (RFC 6266); no quoted form to escape.
	h.Set("Content-Disposition", disposition+"; filename*=UTF-8''"+filenameStar(name))
	// A browser's PDF viewer does not start inside a sandboxed document, and a
	// PDF cannot run script in the page's origin, so it goes without the policy.
	if ct != "application/pdf" {
		h.Set("Content-Security-Policy", fileCSP)
	}
	h.Set("Cache-Control", "private, no-cache")
	modTime := st.ModTime()
	if etag != "" {
		// A file of the attachment repository: its content id is the tag, and it
		// has no modification time (finding one would walk the history).
		h.Set("ETag", `"`+etag+`"`)
		modTime = time.Time{}
	}
	http.ServeContent(c.w, c.r, "", modTime, f)
}
