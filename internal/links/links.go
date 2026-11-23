// Package links finds the links from one page to another in a page file, and
// rewrites their paths. A link is a Markdown link whose target is a page of the
// space: a relative path ending in .md (with an optional #anchor), and, in the
// title position, the id of the target: [暮雪](../Tang/暮雪.md "01JD3K8Q7Z").
// Links to the web, images, attachments and anything inside code are not links
// of the space.
//
// The parser is goldmark with the rules the editor's converter uses (GFM tables
// and the CJK-friendly emphasis), so a file reads the same on both sides; the
// shared cases are in testdata/links.json.
package links

import (
	"bytes"
	"html"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	cjkfriendly "github.com/tats-u/goldmark-cjk-friendly/v2"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// MaxPerPage is the most links of one page that are kept.
const MaxPerPage = 2000

// Link is one link to a page, as a page file has it.
type Link struct {
	// Text is the visible text of the link, without formatting.
	Text string
	// Raw is the path as written, with escapes and percent-encoding undone,
	// without the anchor and the leading slash.
	Raw string
	// Rooted is true when the path was written with a leading slash (from the
	// root of the space).
	Rooted bool
	// Path is the page file the link names, relative to the space root.
	Path string
	// ID is the title of the link when it is exactly a page id, else "".
	ID string
	// Anchor is what follows the #, as written, without the #.
	Anchor string
	// Ord is the place of the link among the links of the file, from 0.
	Ord int

	// The positions below are byte offsets in the file. They are -1 when the
	// syntax of the link could not be located: such a link is found but never
	// rewritten. A reference-style link ([text][ref]) has its destination and
	// title in the definition ([ref]: b.md "id"), and Def is set: every link
	// that uses the definition has the same positions, so their edits are one.
	DestStart, DestEnd   int  // the destination, with the <> when it has them
	Angle                bool // the destination is written <like this>
	TitleStart, TitleEnd int  // the title with its quotes; -1 when there is none
	TailPos              int  // the closing parenthesis (the end of the destination of a definition)
	Def                  bool // the destination is in a link reference definition
	// BlockStart and BlockEnd delimit the paragraph (or heading, or line of a
	// table) the link is in; -1 when unknown.
	BlockStart, BlockEnd int
}

// md parses what the editor's converter parses. Linkify is not among the
// extensions: it only makes links of bare web addresses, which are never links
// to pages, and it cost about a fifth of the time of a parse.
var md = goldmark.New(goldmark.WithExtensions(
	extension.Table, extension.TaskList, extension.Footnote,
	cjkfriendly.CJKFriendlyEmphasisAndStrikethrough,
))

// hasScheme reports whether dest starts with a URL scheme ("https:", "mailto:").
func hasScheme(dest string) bool {
	for i := 0; i < len(dest); i++ {
		c := dest[i]
		switch {
		case isAlpha(c):
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '.' || c == '-'):
		case i > 0 && c == ':':
			return true
		default:
			return false
		}
	}
	return false
}

// hasMD is a cheap test for a body that can hold a link to a page at all.
func hasMD(b string) bool {
	for i := 0; i+3 <= len(b); i++ {
		if b[i] == '.' && (b[i+1] == 'm' || b[i+1] == 'M') && (b[i+2] == 'd' || b[i+2] == 'D') {
			return true
		}
	}
	return false
}

// Extract returns the links of the page file data, which lies at src (relative
// to the space root). Relative paths are resolved from src.
func Extract(src string, data []byte) []Link {
	doc := page.Parse(data)
	body := doc.Body
	if !hasMD(body) {
		return nil
	}
	// Offsets are worked out in the text the parser saw (no byte order mark,
	// line ends as LF) and mapped back to the file.
	bom := 0
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		bom = 3
	}
	var crs []int // places, in the normalised text, of the line feeds that had a CR before
	norm := len(data) - bom
	if bytes.Contains(data, []byte("\r\n")) {
		n := 0
		for i := bom; i < len(data); i++ {
			switch {
			case data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n':
				crs = append(crs, n)
			default:
				n++
			}
		}
		norm = n
	}
	bodyStart := norm - len(body)
	raw := func(o int) int {
		if o < 0 {
			return -1
		}
		o += bodyStart
		// number of recorded line feeds before o
		lo, hi := 0, len(crs)
		for lo < hi {
			m := (lo + hi) / 2
			if crs[m] < o {
				lo = m + 1
			} else {
				hi = m
			}
		}
		return o + lo + bom
	}

	b := []byte(body)
	root := md.Parser().Parse(text.NewReader(b))
	var out []Link
	var refs []*ast.Link                             // the node of each link of out that uses a definition, else nil
	var defs map[string]*ast.LinkReferenceDefinition // by label; the first one counts
	ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.LinkReferenceDefinition:
			if defs == nil {
				defs = map[string]*ast.LinkReferenceDefinition{}
			}
			if k := util.ToLinkReference(v.Label); defs[k] == nil {
				defs[k] = v
			}
			return ast.WalkSkipChildren, nil
		case *ast.Image:
			return ast.WalkSkipChildren, nil
		case *ast.Link:
			if len(out) >= MaxPerPage {
				return ast.WalkStop, nil
			}
			if l, ok := fromNode(src, b, v); ok {
				l.Ord = len(out)
				out = append(out, l)
				if v.Reference != nil {
					refs = append(refs, v)
				} else {
					refs = append(refs, nil)
				}
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	for i := range out {
		l := &out[i]
		if n := refs[i]; n != nil {
			if d := defs[util.ToLinkReference(n.Reference.Value)]; d != nil {
				locateDef(l, b, d)
			}
		}
		l.DestStart, l.DestEnd = raw(l.DestStart), raw(l.DestEnd)
		l.TitleStart, l.TitleEnd = raw(l.TitleStart), raw(l.TitleEnd)
		l.TailPos = raw(l.TailPos)
		l.BlockStart, l.BlockEnd = raw(l.BlockStart), raw(l.BlockEnd)
	}
	return out
}

// Resolve names the page file a link destination points at, from the page at
// src: ok is false for what is not a link to a page of the space. raw is the
// path part, decoded; anchor is the text after the #.
func Resolve(src, dest string) (target, raw string, rooted bool, anchor string, ok bool) {
	if strings.Contains(dest, "\\") || strings.Contains(dest, "&") {
		dest = unescape(dest)
	}
	if i := strings.IndexByte(dest, '#'); i >= 0 {
		dest, anchor = dest[:i], dest[i+1:]
	}
	if i := strings.IndexByte(dest, '?'); i >= 0 {
		dest = dest[:i]
	}
	if dest == "" || hasScheme(dest) || strings.HasPrefix(dest, "//") {
		return "", "", false, "", false
	}
	if d, err := url.PathUnescape(dest); err == nil {
		dest = d
	}
	if !utf8.ValidString(dest) || !strings.EqualFold(path.Ext(dest), ".md") {
		return "", "", false, "", false
	}
	if strings.HasPrefix(dest, "/") {
		rooted = true
		dest = strings.TrimLeft(dest, "/")
		raw = dest
		target = path.Clean(dest)
	} else {
		raw = dest
		target = path.Join(path.Dir(src), dest)
	}
	if target == "." || target == ".." || strings.HasPrefix(target, "../") || strings.HasPrefix(target, "/") || target == "" {
		return "", "", false, "", false
	}
	return target, raw, rooted, anchor, true
}

// unescape undoes backslash escapes and character references the way
// CommonMark does, in one pass: a backslash before ASCII punctuation stands
// for that character, which is then taken literally (so `\&amp;` is "&amp;",
// not "&"); an entity or numeric reference with its semicolon ("&amp;",
// "&#38;", "&#x26;") is decoded; anything else, such as a "&" that starts no
// known reference, is kept as written.
func unescape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && isPunct(s[i+1]):
			i++
			b.WriteByte(s[i])
		case c == '&':
			if n := entityLen(s[i:]); n > 0 {
				if ref := s[i : i+n]; html.UnescapeString(ref) != ref {
					b.WriteString(html.UnescapeString(ref))
					i += n - 1
					continue
				}
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// entityLen is the length of the character reference s starts with, in the
// forms CommonMark knows (&name; &#digits; &#xhex;), or 0. Whether the name
// is an entity is for the caller to find out.
func entityLen(s string) int {
	if len(s) < 3 || s[0] != '&' {
		return 0
	}
	digits := func(from, most int, ok func(byte) bool) int {
		j := from
		for j < len(s) && j-from < most && ok(s[j]) {
			j++
		}
		if j == from {
			return -1
		}
		return j
	}
	var i int
	switch {
	case s[1] == '#' && (s[2] == 'x' || s[2] == 'X'):
		i = digits(3, 6, isHex)
	case s[1] == '#':
		i = digits(2, 7, func(c byte) bool { return c >= '0' && c <= '9' })
	case isAlpha(s[1]):
		i = digits(2, 31, func(c byte) bool { return isAlpha(c) || c >= '0' && c <= '9' })
	default:
		return 0
	}
	if i < 0 || i >= len(s) || s[i] != ';' {
		return 0
	}
	return i + 1
}

func isAlpha(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'F' || c >= 'a' && c <= 'f'
}

func isPunct(c byte) bool {
	return (c >= '!' && c <= '/') || (c >= ':' && c <= '@') || (c >= '[' && c <= '`') || (c >= '{' && c <= '~')
}

func fromNode(src string, b []byte, n *ast.Link) (Link, bool) {
	dest := string(n.Destination)
	target, raw, rooted, anchor, ok := Resolve(src, dest)
	if !ok {
		return Link{}, false
	}
	l := Link{
		Text: plain(n, b), Raw: raw, Rooted: rooted, Path: target, Anchor: anchor,
		DestStart: -1, DestEnd: -1, TitleStart: -1, TitleEnd: -1, TailPos: -1, BlockStart: -1, BlockEnd: -1,
	}
	if t := string(n.Title); page.ValidID(t) {
		l.ID = t
	}
	if n.Reference == nil {
		locate(&l, b, n)
	}
	l.BlockStart, l.BlockEnd = block(b, n)
	return l, true
}

// plain is the text a link shows: its descendants' text and code, no marks.
func plain(n ast.Node, b []byte) string {
	var sb strings.Builder
	ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := c.(type) {
		case *ast.Text:
			sb.WriteString(unescape(string(v.Segment.Value(b))))
			if v.SoftLineBreak() || v.HardLineBreak() {
				sb.WriteByte(' ')
			}
		case *ast.String:
			sb.Write(v.Value)
		case *ast.Image:
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	s := strings.Join(strings.Fields(sb.String()), " ")
	if utf8.RuneCountInString(s) > 200 {
		s = string([]rune(s)[:200])
	}
	return s
}

// block is the extent of the paragraph, heading or table row the link is in.
func block(b []byte, n ast.Node) (int, int) {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == ast.TypeBlock {
			if ls := p.Lines(); ls != nil && ls.Len() > 0 {
				return ls.At(0).Start, ls.At(ls.Len() - 1).Stop
			}
		}
	}
	// A table cell: the line it is on.
	at := firstStart(n)
	if at < 0 || at > len(b) {
		return -1, -1
	}
	s := bytes.LastIndexByte(b[:at], '\n') + 1
	e := bytes.IndexByte(b[at:], '\n')
	if e < 0 {
		e = len(b)
	} else {
		e += at
	}
	return s, e
}

func firstStart(n ast.Node) int {
	at := -1
	ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if t, ok := c.(*ast.Text); ok && entering {
			at = t.Segment.Start
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return at
}

// endOf is the offset just after the source text of an inline node, or -1.
func endOf(n ast.Node, b []byte) int {
	switch v := n.(type) {
	case *ast.Text:
		return v.Segment.Stop
	case *ast.RawHTML:
		if v.Segments.Len() == 0 {
			return -1
		}
		return v.Segments.At(v.Segments.Len() - 1).Stop
	case *ast.CodeSpan:
		lc := v.LastChild()
		t, ok := lc.(*ast.Text)
		if !ok {
			return -1
		}
		p := t.Segment.Stop
		for p < len(b) && (b[p] == '`' || b[p] == ' ' || b[p] == '\n') {
			p++
		}
		return p
	case *ast.AutoLink:
		// <https://example.com> in the text of a link: the label is a slice
		// of the source, so its place is known.
		lb := v.Label(b)
		if len(lb) == 0 {
			return -1
		}
		at := cap(b) - cap(lb)
		if at < 0 || at+len(lb) > len(b) || &b[at] != &lb[0] {
			return -1
		}
		p := at + len(lb)
		if p < len(b) && b[p] == '>' {
			p++
		}
		return p
	case *ast.String:
		return -1
	case *ast.Link, *ast.Image:
		var ref bool
		if l, ok := v.(*ast.Link); ok {
			ref = l.Reference != nil
		} else if im := v.(*ast.Image); im.Reference != nil {
			ref = true
		}
		if ref || n.LastChild() == nil {
			return -1
		}
		p := endOf(n.LastChild(), b)
		if p < 0 {
			return -1
		}
		_, _, _, _, tail, ok := tailAt(b, p)
		if !ok {
			return -1
		}
		return tail + 1
	default:
		lc := n.LastChild()
		if lc == nil {
			return -1
		}
		p := endOf(lc, b)
		if p < 0 {
			return -1
		}
		for p < len(b) && (b[p] == '*' || b[p] == '_' || b[p] == '~') {
			p++
		}
		return p
	}
}

// tailAt parses "](destination "title")" starting at or after p, where only
// white space and a hard break may stand before the "]". It returns the extents
// of the destination region, the title region (-1 when there is none) and the
// closing parenthesis.
func tailAt(b []byte, p int) (ds, de, ts, te, tail int, ok bool) {
	for p < len(b) && (b[p] == ' ' || b[p] == '\t' || b[p] == '\n' || b[p] == '\\') {
		p++
	}
	if p+1 >= len(b) || b[p] != ']' || b[p+1] != '(' {
		return 0, 0, 0, 0, 0, false
	}
	p += 2
	skip := func() {
		for p < len(b) && (b[p] == ' ' || b[p] == '\t' || b[p] == '\n') {
			p++
		}
	}
	skip()
	if p >= len(b) {
		return 0, 0, 0, 0, 0, false
	}
	ts, te = -1, -1
	if b[p] == ')' {
		return p, p, ts, te, p, true
	}
	ds = p
	if b[p] == '<' {
		i := p + 1
		for i < len(b) {
			if b[i] == '\\' && i+1 < len(b) && isPunct(b[i+1]) {
				i += 2
				continue
			}
			if b[i] == '>' {
				break
			}
			if b[i] == '\n' {
				return 0, 0, 0, 0, 0, false
			}
			i++
		}
		if i >= len(b) {
			return 0, 0, 0, 0, 0, false
		}
		p = i + 1
	} else {
		open := 0
		for p < len(b) {
			c := b[p]
			if c == '\\' && p+1 < len(b) && isPunct(b[p+1]) {
				p += 2
				continue
			}
			if c == '(' {
				open++
			} else if c == ')' {
				open--
				if open < 0 {
					break
				}
			} else if c == ' ' || c == '\t' || c == '\n' {
				break
			}
			p++
		}
	}
	de = p
	skip()
	if p < len(b) && b[p] != ')' {
		closer := b[p]
		switch closer {
		case '"', '\'':
		case '(':
			closer = ')'
		default:
			return 0, 0, 0, 0, 0, false
		}
		ts = p
		p++
		for p < len(b) && b[p] != closer {
			if b[p] == '\\' && p+1 < len(b) && isPunct(b[p+1]) {
				p++
			}
			p++
		}
		if p >= len(b) {
			return 0, 0, 0, 0, 0, false
		}
		p++
		te = p
		skip()
	}
	if p >= len(b) || b[p] != ')' {
		return 0, 0, 0, 0, 0, false
	}
	return ds, de, ts, te, p, true
}

// locate finds where the destination and title of an inline link are written,
// and leaves the offsets at -1 unless what is there is what the parser read.
func locate(l *Link, b []byte, n *ast.Link) {
	lc := n.LastChild()
	if lc == nil {
		return
	}
	p := endOf(lc, b)
	if p < 0 {
		return
	}
	ds, de, ts, te, tail, ok := tailAt(b, p)
	if !ok {
		return
	}
	rawDest := b[ds:de]
	angle := len(rawDest) >= 2 && rawDest[0] == '<'
	if angle {
		rawDest = rawDest[1 : len(rawDest)-1]
	}
	if !bytes.Equal(rawDest, n.Destination) {
		return
	}
	if ts >= 0 {
		if !bytes.Equal(b[ts+1:te-1], n.Title) {
			return
		}
	} else if len(n.Title) > 0 {
		return
	}
	l.DestStart, l.DestEnd, l.Angle = ds, de, angle
	l.TitleStart, l.TitleEnd, l.TailPos = ts, te, tail
}

// locateDef finds where the destination and title of the link reference
// definition d are written ([ref]: <dest> "title"), for a link that uses it,
// and leaves the offsets at -1 unless the destination there is what the
// parser read.
func locateDef(l *Link, b []byte, d *ast.LinkReferenceDefinition) {
	segs := d.Lines()
	if segs == nil || segs.Len() == 0 {
		return
	}
	p := segs.At(0).Start
	if p < 0 || p >= len(b) || b[p] != '[' {
		return
	}
	// The label, on one line.
	for p++; p < len(b) && b[p] != ']'; p++ {
		switch {
		case b[p] == '\\' && p+1 < len(b) && isPunct(b[p+1]):
			p++
		case b[p] == '[' || b[p] == '\n':
			return
		}
	}
	if p+1 >= len(b) || b[p+1] != ':' {
		return
	}
	p += 2
	// White space, with at most one line end: the next line starts where its
	// segment does (after the > of a block quote).
	gap := func() bool {
		for p < len(b) && (b[p] == ' ' || b[p] == '\t') {
			p++
		}
		if p < len(b) && b[p] == '\n' {
			next := -1
			for i := 0; i < segs.Len(); i++ {
				if s := segs.At(i).Start; s > p {
					next = s
					break
				}
			}
			if next < 0 {
				return false
			}
			p = next
			for p < len(b) && (b[p] == ' ' || b[p] == '\t') {
				p++
			}
		}
		return p < len(b)
	}
	if !gap() {
		return
	}
	ds := p
	if b[p] == '<' {
		for p++; p < len(b) && b[p] != '>'; p++ {
			if b[p] == '\\' && p+1 < len(b) && isPunct(b[p+1]) {
				p++
			} else if b[p] == '\n' {
				return
			}
		}
		if p >= len(b) {
			return
		}
		p++
	} else {
		open := 0
	scan:
		for ; p < len(b); p++ {
			switch c := b[p]; {
			case c == '\\' && p+1 < len(b) && isPunct(b[p+1]):
				p++
			case c == '(':
				open++
			case c == ')':
				if open--; open < 0 {
					break scan
				}
			case c == ' ' || c == '\t' || c == '\n':
				break scan
			}
		}
	}
	de := p
	rawDest := b[ds:de]
	angle := len(rawDest) >= 2 && rawDest[0] == '<'
	if angle {
		rawDest = rawDest[1 : len(rawDest)-1]
	}
	if !bytes.Equal(rawDest, d.Destination) {
		return
	}
	ts, te := -1, -1
	if len(d.Title) > 0 {
		if !gap() {
			return
		}
		closer := b[p]
		switch closer {
		case '"', '\'':
		case '(':
			closer = ')'
		default:
			return
		}
		ts = p
		for p++; p < len(b) && b[p] != closer; p++ {
			if b[p] == '\\' && p+1 < len(b) && isPunct(b[p+1]) {
				p++
			}
		}
		if p >= len(b) {
			return
		}
		te = p + 1
	}
	l.DestStart, l.DestEnd, l.Angle, l.Def = ds, de, angle, true
	l.TitleStart, l.TitleEnd, l.TailPos = ts, te, de
}
