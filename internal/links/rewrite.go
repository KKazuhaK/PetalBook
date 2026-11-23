package links

import (
	"path"
	"sort"
	"strings"
)

// Edit replaces the bytes [Start, End) of a file with Text.
type Edit struct {
	Start, End int
	Text       string
}

// Apply returns data with the edits made. The edits must not overlap, except
// that the same edit may come more than once (the links that share a reference
// definition each ask for it); they need not be in order. Everything outside
// them is kept byte for byte.
func Apply(data []byte, edits []Edit) []byte {
	if len(edits) == 0 {
		return data
	}
	es := append([]Edit(nil), edits...)
	sort.SliceStable(es, func(i, j int) bool { return es[i].Start < es[j].Start })
	out := make([]byte, 0, len(data)+64)
	at := 0
	for i, e := range es {
		if i > 0 && e == es[i-1] {
			continue
		}
		out = append(out, data[at:e.Start]...)
		out = append(out, e.Text...)
		at = e.End
	}
	return append(out, data[at:]...)
}

// Rel is the path of the file to, as a link in the file from names it: relative
// to the folder of from, with .. where it has to go up. Both are paths in the
// space.
func Rel(from, to string) string {
	var a []string
	if d := path.Dir(from); d != "." {
		a = strings.Split(d, "/")
	}
	b := strings.Split(to, "/")
	i := 0
	for i < len(a) && i < len(b)-1 && a[i] == b[i] {
		i++
	}
	parts := make([]string, 0, len(a)-i+len(b)-i)
	for range a[i:] {
		parts = append(parts, "..")
	}
	parts = append(parts, b[i:]...)
	return strings.Join(parts, "/")
}

// Target is the page file a link names when it stands in the file at from and
// is written as raw (see Link.Raw).
func Target(from, raw string, rooted bool) string {
	if rooted {
		return path.Clean(raw)
	}
	return path.Join(path.Dir(from), raw)
}

// encode writes a path as the destination of a link. A destination in angle
// brackets keeps spaces and the like as they are; one without has them
// percent-encoded. Characters that mean something in a destination (the
// percent sign, # and ?) are always encoded.
func encode(p string, angle, nonASCII bool) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '%' || c == '#' || c == '?' || c == '\\' || c == '<' || c == '>' || c < 0x20 || c == 0x7f:
			b.WriteString(pct(c))
		case !angle && (c == ' ' || c == '(' || c == ')' || c == '[' || c == ']' || c == '"' || c == '`'):
			b.WriteString(pct(c))
		case c >= 0x80 && nonASCII:
			b.WriteString(pct(c))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func pct(c byte) string {
	const hex = "0123456789ABCDEF"
	return "%" + string(hex[c>>4]) + string(hex[c&15])
}

// usesEncodedUTF8 reports whether a destination as written has percent-encoded
// bytes of non-ASCII characters, which a rewrite then writes the same way.
func usesEncodedUTF8(rawDest string) bool {
	for i := 0; i+2 < len(rawDest); i++ {
		if rawDest[i] == '%' {
			c := rawDest[i+1]
			if c >= '8' && c <= '9' || c >= 'A' && c <= 'F' || c >= 'a' && c <= 'f' {
				return true
			}
		}
	}
	return false
}

// DestEdit returns the edit that makes the link name the page file newTarget
// (a path in the space) from the file at from. The anchor, the query and the
// way the destination is written (angle brackets, ./, encoded or plain) are
// kept. ok is false for a link whose destination could not be located.
func (l Link) DestEdit(data []byte, from, newTarget string) (Edit, bool) {
	if l.DestStart < 0 {
		return Edit{}, false
	}
	region := string(data[l.DestStart:l.DestEnd])
	inner := region
	if l.Angle {
		inner = region[1 : len(region)-1]
	}
	suffix := ""
	if i := strings.IndexAny(inner, "#?"); i >= 0 {
		suffix = inner[i:]
	}
	rel := Rel(from, newTarget)
	if l.Rooted {
		rel = "/" + newTarget
	} else if strings.HasPrefix(inner, "./") && !strings.HasPrefix(rel, "../") {
		rel = "./" + rel
	}
	dest := encode(rel, l.Angle, usesEncodedUTF8(inner)) + suffix
	if l.Angle {
		dest = "<" + dest + ">"
	}
	return Edit{l.DestStart, l.DestEnd, dest}, true
}

// IDEdit returns the edit that puts id in the title position of a link that has
// no title (for a reference-style link, of its definition); ok is false when
// the link has a title (it is left as it is) or could not be located.
func (l Link) IDEdit(id string) (Edit, bool) {
	if l.DestStart < 0 || l.TitleStart >= 0 || l.TailPos < 0 {
		return Edit{}, false
	}
	return Edit{l.DestEnd, l.DestEnd, ` "` + id + `"`}, true
}
