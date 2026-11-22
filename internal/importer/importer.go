// Package importer turns Markdown files from outside into pages to create:
// one Markdown file, or a zip of files and folders. It reads and decides; it
// writes nothing. What it returns is handed to the space, which creates the
// pages and their attachments as one change.
//
// The rules are those of docs/api.md, "Import".
package importer

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// MaxEntries is the most entries a zip may have.
const MaxEntries = 20000

// MaxSkipped is how many skipped entries a report names.
const MaxSkipped = 200

// Error is a problem with what was sent, to be reported to the sender.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func errf(format string, a ...any) error { return &Error{fmt.Sprintf(format, a...)} }

// Asset is a file a page refers to. In the page's body its address is
// Token; whoever creates the page puts the stored address there.
type Asset struct {
	Token string
	Name  string
	Data  []byte
}

// Page is a page to create.
type Page struct {
	// Folder is relative to the folder the import goes into; "" is that folder.
	Folder string
	// Meta is the header, without an id.
	Meta   page.Meta
	Body   string
	Assets []Asset
	// Source is the path the page came from, for messages.
	Source string
}

// Skipped names something that was not imported, and why.
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Plan is what an import will do.
type Plan struct {
	Pages   []Page
	Skipped []Skipped
	// SkippedMore counts skipped entries beyond MaxSkipped.
	SkippedMore int
}

func (p *Plan) skip(path, reason string) {
	if len(p.Skipped) >= MaxSkipped {
		p.SkippedMore++
		return
	}
	p.Skipped = append(p.Skipped, Skipped{path, reason})
}

// Options bound an import.
type Options struct {
	// MaxPage is the largest Markdown file that becomes a page, in bytes: what
	// a page may be when it is saved. A larger one is left out.
	MaxPage int64
	// MaxAsset is the largest attachment taken along, in bytes.
	MaxAsset int64
	// MaxTotal is the most the files read from a zip may come to, in bytes.
	MaxTotal int64
	// ValidFolder reports whether a folder path can exist in a space.
	ValidFolder func(rel string) error
}

// IsZip reports whether data starts like a zip file (also an empty one).
func IsZip(data []byte) bool {
	return bytes.HasPrefix(data, []byte("PK\x03\x04")) || bytes.HasPrefix(data, []byte("PK\x05\x06"))
}

// IsMarkdownName reports whether a file name is that of a Markdown file.
func IsMarkdownName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

// Single plans the import of one Markdown file. maxPage is the largest a
// page may be, in bytes (0: no limit).
func Single(name string, data []byte, maxPage int64) (*Plan, error) {
	base := path.Base(strings.ReplaceAll(name, `\`, "/"))
	if strings.TrimSpace(name) == "" || base == "." || base == "/" {
		return nil, errf("give the file's name in the name parameter")
	}
	if !IsMarkdownName(base) {
		return nil, errf("%q is not a Markdown file (.md or .markdown), nor a zip", base)
	}
	if maxPage > 0 && int64(len(data)) > maxPage {
		return nil, errf("%q is larger than %d bytes, the most a page may be", base, maxPage)
	}
	if !utf8.Valid(data) {
		return nil, errf("%q is not UTF-8 text", base)
	}
	plan := &Plan{}
	plan.Pages = append(plan.Pages, pageOf(base, "", data))
	return plan, nil
}

// file is one entry of a zip that may be imported.
type file struct {
	path string // cleaned, slash-separated
	zf   *zip.File
}

// Zip plans the import of a zip of Markdown files and what they refer to.
func Zip(data []byte, opt Options) (*Plan, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errf("the file is not a zip that can be read: %v", err)
	}
	if len(zr.File) > MaxEntries {
		return nil, errf("the zip has %d entries; at most %d can be imported at once", len(zr.File), MaxEntries)
	}
	plan := &Plan{}
	files := map[string]*file{}
	var pages []*file
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		p, ok := cleanPath(zf.Name)
		if !ok {
			continue // hidden, or not a path inside the zip
		}
		if !zf.Mode().IsRegular() {
			plan.skip(p, "not a regular file")
			continue
		}
		if _, dup := files[p]; dup {
			plan.skip(p, "the zip has this path twice; the first one was used")
			continue
		}
		f := &file{path: p, zf: zf}
		files[p] = f
		if IsMarkdownName(p) {
			pages = append(pages, f)
		}
	}
	if len(pages) == 0 {
		return nil, errf("the zip has no Markdown file (.md or .markdown)")
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].path < pages[j].path })

	// What has been read so far counts against the limit, compressed or not:
	// a small zip can unpack to something enormous.
	var total int64
	read := func(f *file, max int64) ([]byte, error) {
		if opt.MaxTotal > 0 && total+int64(f.zf.UncompressedSize64) > opt.MaxTotal {
			return nil, errf("the zip unpacks to more than %d bytes", opt.MaxTotal)
		}
		rc, err := f.zf.Open()
		if err != nil {
			return nil, errf("%s cannot be read from the zip: %v", f.path, err)
		}
		defer rc.Close()
		limit := max
		if opt.MaxTotal > 0 && (limit <= 0 || opt.MaxTotal-total < limit) {
			limit = opt.MaxTotal - total
		}
		var r io.Reader = rc
		if limit > 0 {
			r = io.LimitReader(rc, limit+1)
		}
		b, err := io.ReadAll(r)
		if err != nil {
			return nil, errf("%s cannot be read from the zip: %v", f.path, err)
		}
		total += int64(len(b))
		if limit > 0 && int64(len(b)) > limit {
			if max > 0 && limit == max {
				return nil, errTooLarge
			}
			return nil, errf("the zip unpacks to more than %d bytes", opt.MaxTotal)
		}
		return b, nil
	}

	referred := map[string]bool{}
	for _, f := range pages {
		folder := path.Dir(f.path)
		if folder == "." {
			folder = ""
		}
		if folder != "" && opt.ValidFolder != nil {
			if err := opt.ValidFolder(folder); err != nil {
				plan.skip(f.path, "its folder cannot be a folder of a space: "+err.Error())
				continue
			}
		}
		if opt.MaxPage > 0 && int64(f.zf.UncompressedSize64) > opt.MaxPage {
			plan.skip(f.path, fmt.Sprintf("larger than %d bytes, the most a page may be", opt.MaxPage))
			continue
		}
		data, err := read(f, opt.MaxPage)
		if errors.Is(err, errTooLarge) {
			plan.skip(f.path, fmt.Sprintf("larger than %d bytes, the most a page may be", opt.MaxPage))
			continue
		}
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(data) {
			plan.skip(f.path, "not UTF-8 text")
			continue
		}
		pg := pageOf(path.Base(f.path), folder, data)
		pg.Source = f.path

		// Files the page refers to come along as its attachments.
		tokens := map[string]string{} // path in the zip -> token
		pg.Body = rewriteLinks(pg.Body, func(dest string) (string, bool) {
			target, ok := resolve(folder, dest)
			if !ok {
				return "", false
			}
			tf := files[target]
			if tf == nil || IsMarkdownName(target) {
				return "", false
			}
			if tok, seen := tokens[target]; seen {
				return tok, true
			}
			if opt.MaxAsset > 0 && int64(tf.zf.UncompressedSize64) > opt.MaxAsset {
				plan.skip(target, fmt.Sprintf("larger than %d bytes, the most an attachment may be; %s still refers to it as written", opt.MaxAsset, f.path))
				referred[target] = true
				return "", false
			}
			b, rerr := read(tf, opt.MaxAsset)
			if rerr != nil {
				if errors.Is(rerr, errTooLarge) {
					plan.skip(target, fmt.Sprintf("larger than %d bytes, the most an attachment may be", opt.MaxAsset))
					referred[target] = true
				} else if err == nil {
					err = rerr
				}
				return "", false
			}
			if len(b) == 0 {
				plan.skip(target, "an empty file cannot be an attachment")
				referred[target] = true
				return "", false
			}
			tok := fmt.Sprintf("%d", len(pg.Assets))
			pg.Assets = append(pg.Assets, Asset{Token: tok, Name: path.Base(target), Data: b})
			tokens[target] = tok
			referred[target] = true
			return tok, true
		})
		if err != nil {
			return nil, err
		}
		plan.Pages = append(plan.Pages, pg)
	}

	var rest []string
	for p := range files {
		if !IsMarkdownName(p) && !referred[p] {
			rest = append(rest, p)
		}
	}
	sort.Strings(rest)
	for _, p := range rest {
		plan.skip(p, "not a Markdown file and no page refers to it")
	}
	if len(plan.Pages) == 0 {
		return nil, errf("none of the Markdown files in the zip could be imported")
	}
	return plan, nil
}

var errTooLarge = errors.New("too large")

// cleanPath normalises the name of a zip entry and refuses what is not a
// path inside the zip, or is hidden: a segment starting with a dot, or
// __MACOSX (what macOS adds to the zips it makes).
func cleanPath(name string) (string, bool) {
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return "", false
	}
	name = strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(name, "/") {
		return "", false
	}
	var segs []string
	for _, s := range strings.Split(name, "/") {
		switch {
		case s == "" || s == ".":
			continue
		case s == "..":
			return "", false
		case strings.HasPrefix(s, ".") || s == "__MACOSX":
			return "", false
		}
		segs = append(segs, s)
	}
	if len(segs) == 0 {
		return "", false
	}
	return strings.Join(segs, "/"), true
}

var schemeRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// resolve maps an address written in a page of folder to a path in the zip.
// Addresses that are not relative paths (a scheme, an absolute path, a
// fragment) and ones that lead out of the zip are not files of it.
func resolve(folder, dest string) (string, bool) {
	if dest == "" || schemeRE.MatchString(dest) || strings.HasPrefix(dest, "/") || strings.HasPrefix(dest, "#") {
		return "", false
	}
	if i := strings.IndexAny(dest, "?#"); i >= 0 {
		dest = dest[:i]
	}
	if dec, err := url.PathUnescape(dest); err == nil {
		dest = dec
	}
	dest = strings.ReplaceAll(dest, `\`, "/")
	p := path.Join(folder, dest)
	if p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
		return "", false
	}
	return p, true
}

// pageOf reads one Markdown file into a page: its header (without the id),
// its title, its body.
func pageOf(base, folder string, data []byte) Page {
	doc := page.Parse(data)
	body := doc.Body
	var meta page.Meta
	if doc.HasHeader() {
		for _, f := range doc.Meta() {
			switch f.Key {
			case "id":
				// Every imported page gets an id of its own.
			case "created":
				if d, ok := dateOf(f.Value); ok {
					meta = append(meta, page.Field{Key: "created", Value: d})
				}
			default:
				meta = append(meta, f)
			}
		}
	}
	if meta.Title() == "" {
		title, rest, ok := leadingHeading(body)
		if ok {
			body = rest
		} else {
			title = strings.TrimSpace(strings.TrimSuffix(base, path.Ext(base)))
		}
		if title != "" {
			// A title the header had, but empty or not text, gives way.
			out := page.Meta{{Key: "title", Value: title}}
			for _, f := range meta {
				if f.Key != "title" {
					out = append(out, f)
				}
			}
			meta = out
		}
	}
	return Page{Folder: folder, Meta: meta, Body: body, Source: base}
}

// dateOf accepts a creation date the way a page header has it: a day, or a
// time whose day is taken.
func dateOf(v any) (string, bool) {
	var s string
	switch t := v.(type) {
	case string:
		s = strings.TrimSpace(t)
	case time.Time:
		return t.Format("2006-01-02"), true
	default:
		return "", false
	}
	if len(s) >= 10 {
		if _, err := time.Parse("2006-01-02", s[:10]); err == nil && (len(s) == 10 || s[10] == 'T' || s[10] == ' ') {
			return s[:10], true
		}
	}
	return "", false
}

var atxH1 = regexp.MustCompile(`^#[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$`)

// leadingHeading takes a `# heading` off the top of a body: the first line
// that is not blank must be it. The blank lines after it go as well.
func leadingHeading(body string) (title, rest string, ok bool) {
	s := body
	for {
		line, next, found := strings.Cut(s, "\n")
		if strings.TrimSpace(line) == "" && found {
			s = next
			continue
		}
		m := atxH1.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil || strings.TrimSpace(m[1]) == "" {
			return "", body, false
		}
		rest = next
		for {
			l, n, f := strings.Cut(rest, "\n")
			if strings.TrimSpace(l) != "" || !f {
				if strings.TrimSpace(l) == "" {
					rest = ""
				}
				break
			}
			rest = n
		}
		return strings.TrimSpace(m[1]), rest, true
	}
}

// linkRE finds inline links and images: [text](dest) and ![alt](dest), with
// an optional title. The address is bare (possibly with one level of
// parentheses, as in "image (1).png" percent-encoded or not) or in <>.
var linkRE = regexp.MustCompile(`(!?\[(?:[^\]\\\n]|\\.)*\]\()[ \t]*(<[^>\n]*>|(?:[^\s()<>]|\([^\s()]*\))+)((?:[ \t]+(?:"[^"\n]*"|'[^'\n]*'))?[ \t]*\))`)

var fenceRE = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")

// rewriteLinks calls replace for the address of every inline link and image
// outside fenced code, and writes what it returns in its place (in <>, so
// any character can be in it). Text in code spans is left alone.
func rewriteLinks(body string, replace func(dest string) (string, bool)) string {
	var out strings.Builder
	fence := ""
	for len(body) > 0 {
		line, rest, found := strings.Cut(body, "\n")
		body = rest
		if m := fenceRE.FindStringSubmatch(line); m != nil {
			switch {
			case fence == "":
				fence = m[1]
			case m[1][0] == fence[0] && len(m[1]) >= len(fence) && strings.TrimSpace(line[strings.Index(line, m[1])+len(m[1]):]) == "":
				fence = ""
			}
			out.WriteString(line)
		} else if fence != "" || !strings.Contains(line, "](") {
			out.WriteString(line)
		} else {
			out.WriteString(rewriteLine(line, replace))
		}
		if found {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// maxLinkLine is the longest line whose links are looked at. Text has no
// lines like that; a file that does is left as written rather than scanned.
const maxLinkLine = 64 << 10

func rewriteLine(line string, replace func(dest string) (string, bool)) string {
	if len(line) > maxLinkLine {
		return line
	}
	spans := codeSpans(line)
	// Matches and spans both come in order of position, so one pass over the
	// spans serves all the matches.
	next := 0
	inCode := func(i int) bool {
		for next < len(spans) && spans[next][1] <= i {
			next++
		}
		return next < len(spans) && i >= spans[next][0]
	}
	var out strings.Builder
	last := 0
	for _, m := range linkRE.FindAllStringSubmatchIndex(line, -1) {
		if inCode(m[0]) {
			continue
		}
		dest := line[m[4]:m[5]]
		if strings.HasPrefix(dest, "<") {
			dest = dest[1 : len(dest)-1]
		}
		to, ok := replace(dest)
		if !ok {
			continue
		}
		out.WriteString(line[last:m[4]])
		out.WriteString("<" + to + ">")
		last = m[5]
	}
	out.WriteString(line[last:])
	return out.String()
}

// codeSpans finds the `code` spans of a line: a run of backticks up to the
// next run of the same length. One pass over the runs, whatever the line.
func codeSpans(line string) [][2]int {
	type run struct{ start, end int }
	var runs []run
	for i := 0; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] == '`' {
			j++
		}
		runs = append(runs, run{i, j})
		i = j
	}
	// For each run, the next one of the same length.
	next := make([]int, len(runs))
	last := map[int]int{}
	for i := len(runs) - 1; i >= 0; i-- {
		n := runs[i].end - runs[i].start
		if j, ok := last[n]; ok {
			next[i] = j
		} else {
			next[i] = -1
		}
		last[n] = i
	}
	var spans [][2]int
	for i := 0; i < len(runs); {
		if j := next[i]; j >= 0 {
			spans = append(spans, [2]int{runs[i].start, runs[j].end})
			i = j + 1
		} else {
			i++
		}
	}
	return spans
}
