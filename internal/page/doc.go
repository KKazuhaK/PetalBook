// Package page is the page file format: a Markdown body with an optional YAML
// header between `---` lines. It never loses content: a file with a missing or
// broken header is still a readable page, with the problem reported.
package page

import (
	"bytes"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"petalbook.app/server/internal/icons"
)

// bomText is the UTF-8 byte order mark some editors put at the start of a file.
const bomText = "\xef\xbb\xbf"

// Problem describes something wrong with a page file that does not stop it
// from being read.
type Problem struct {
	Code    string // no_header, unterminated_header, bad_yaml, header_not_mapping, no_id, bad_id
	Message string
}

func (p Problem) Error() string { return p.Message }

// Doc is a parsed page file. Writing it back with Bytes reproduces the input
// exactly (apart from CRLF line ends, which become LF) unless the header or
// body was changed through SetMeta or SetBody.
type Doc struct {
	Body string

	bom     bool
	present bool   // a valid header is part of the file
	open    string // exact text of the opening fence line, with its newline
	text    string // exact header text between the fences
	close   string // exact closing fence line, with its newline if it had one
	root    *yaml.Node
	perr    *Problem // why the header is not usable; the whole file is then Body
}

// Parse reads a page file. It always succeeds.
func Parse(data []byte) *Doc {
	d := &Doc{}
	s := string(data)
	if strings.HasPrefix(s, bomText) {
		d.bom = true
		s = s[len(bomText):]
	}
	if strings.Contains(s, "\r\n") {
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	d.Body = s

	first, afterFirst := cutLine(s)
	if !isFenceLine(first, false) {
		d.perr = &Problem{"no_header", "the page has no header"}
		return d
	}
	// Find the closing fence.
	off := len(s) - len(afterFirst)
	rest := afterFirst
	hdrEnd, closeEnd := -1, -1
	for len(rest) > 0 {
		line, next := cutLine(rest)
		lineStart := len(s) - len(rest)
		if isFenceLine(line, true) {
			hdrEnd, closeEnd = lineStart, len(s)-len(next)
			break
		}
		rest = next
	}
	if hdrEnd < 0 {
		d.perr = &Problem{"unterminated_header", "the header is opened with --- but never closed"}
		return d
	}
	text := s[off:hdrEnd]
	root, perr := parseHeader(text)
	if perr != nil {
		d.perr = perr
		return d
	}
	d.present = true
	d.open = s[:off]
	d.text = text
	d.close = s[hdrEnd:closeEnd]
	d.root = root
	d.Body = s[closeEnd:]
	return d
}

// cutLine splits off the first line; line has no newline, rest follows it.
func cutLine(s string) (line, rest string) {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func isFenceLine(line string, closing bool) bool {
	line = strings.TrimRight(line, " \t")
	return line == "---" || (closing && line == "...")
}

func parseHeader(text string) (*yaml.Node, *Problem) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, &Problem{"bad_yaml", "the header is not valid YAML: " + strings.TrimPrefix(err.Error(), "yaml: ")}
	}
	if doc.Kind == 0 || (doc.Kind == yaml.DocumentNode && len(doc.Content) == 0) {
		// Empty or comments only.
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
	}
	root := &doc
	if root.Kind == yaml.DocumentNode {
		root = root.Content[0]
	}
	if root.Kind == yaml.AliasNode {
		root = root.Alias
	}
	if root.Kind == yaml.ScalarNode && root.ShortTag() == "!!null" {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, &Problem{"header_not_mapping", "the header is not a set of key: value fields"}
	}
	return root, nil
}

// HasHeader reports whether the file has a usable header.
func (d *Doc) HasHeader() bool { return d.present }

// Problems lists what is wrong with the file, if anything.
func (d *Doc) Problems() []Problem {
	if d.perr != nil {
		return []Problem{*d.perr}
	}
	switch id := d.Meta().ID(); {
	case id == "":
		return []Problem{{"no_id", "the header has no id"}}
	case !ValidID(id):
		return []Problem{{"bad_id", fmt.Sprintf("%q is not a valid page id", id)}}
	}
	return nil
}

// Meta returns the header fields in file order. It is empty for a file
// without a usable header.
func (d *Doc) Meta() Meta {
	if d.root == nil {
		return Meta{}
	}
	return metaFromNode(d.root)
}

// InexactKeys lists the top-level keys whose value Meta gives as something
// other than what the file says, so that writing the Meta value back (through
// JSON, a browser, and the encoder) would change the file. A scalar is exact
// when it is text with the !!str tag (a calendar date written as one counts
// too: it is written back the same), a bool written true or false, or a number
// whose text is the one the encoder writes and, for an integer, is within
// 2^53. Everything else is not: other spellings of a number (0x10, 1.0, +5,
// 1e3, .5), integers past int64 or 2^53, other tags, other bools. One inexact
// element makes a list's key inexact. It is nil when there is none.
func (d *Doc) InexactKeys() map[string]bool {
	if d.root == nil {
		return nil
	}
	var out map[string]bool
	seen := map[string]bool{}
	for i := 0; i+1 < len(d.root.Content); i += 2 {
		key := d.root.Content[i].Value
		if seen[key] {
			continue
		}
		seen[key] = true
		if !exactValue(d.root.Content[i+1], 0) {
			if out == nil {
				out = map[string]bool{}
			}
			out[key] = true
		}
	}
	return out
}

func exactValue(n *yaml.Node, depth int) bool {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	switch n.Kind {
	case yaml.SequenceNode:
		if depth > 32 {
			return false
		}
		for _, c := range n.Content {
			if !exactValue(c, depth+1) {
				return false
			}
		}
		return true
	case yaml.ScalarNode:
		return exactScalar(n)
	}
	return true // an object: not a value a cell holds at all
}

// maxExactInt is the largest integer a JSON number carries in a browser exactly.
const maxExactInt = 1 << 53

func exactScalar(n *yaml.Node) bool {
	switch n.ShortTag() {
	case "!!null", "!!str":
		return true
	case "!!timestamp":
		return isCalendarDate(n.Value)
	case "!!bool":
		return n.Value == "true" || n.Value == "false"
	case "!!int":
		var i int64
		if n.Decode(&i) != nil || strconv.FormatInt(i, 10) != n.Value {
			return false
		}
		return i >= -maxExactInt && i <= maxExactInt
	case "!!float":
		var f float64
		if n.Decode(&f) != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return false
		}
		return strconv.FormatFloat(f, 'g', -1, 64) == n.Value
	}
	return false
}

// HeaderText returns the header exactly as it is written in the file: the
// lines between the two fences. It is "" for a file without a usable header.
func (d *Doc) HeaderText() string {
	if !d.present {
		return ""
	}
	return d.text
}

// Bytes returns the file content.
func (d *Doc) Bytes() []byte {
	var b bytes.Buffer
	if d.bom {
		b.WriteString(bomText)
	}
	if d.present {
		b.WriteString(d.open)
		b.WriteString(d.text)
		b.WriteString(d.close)
	}
	b.WriteString(d.Body)
	return b.Bytes()
}

// SetBody replaces the Markdown after the header. Line ends become LF.
func (d *Doc) SetBody(body string) {
	d.Body = strings.ReplaceAll(body, "\r\n", "\n")
}

// SetMeta replaces the header. Fields whose value is unchanged keep their
// original YAML (quoting, comments), so saving a page does not reformat what
// the author did not touch; when nothing at all changed the header text is
// kept byte for byte.
//
// An encoding failure is returned and leaves the document as it was.
func (d *Doc) SetMeta(m Meta) error {
	if d.present && MetaEqual(d.Meta(), m) {
		return nil
	}
	if len(m) == 0 && !d.present {
		return nil
	}
	var old map[string][2]*yaml.Node // key -> key node, value node
	if d.root != nil {
		old = map[string][2]*yaml.Node{}
		for i := 0; i+1 < len(d.root.Content); i += 2 {
			k := d.root.Content[i]
			if _, dup := old[k.Value]; !dup {
				old[k.Value] = [2]*yaml.Node{k, d.root.Content[i+1]}
			}
		}
	}
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, f := range m {
		if kv, ok := old[f.Key]; ok && valuesEqual(nodeValue(f.Key, kv[1], 0), f.Value) {
			root.Content = append(root.Content, kv[0], kv[1])
			continue
		}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: f.Key},
			valueNode(f.Key, f.Value))
	}
	text, err := encodeHeader(root)
	if err != nil {
		return fmt.Errorf("cannot write the header: %w", err)
	}
	d.present = true
	d.open, d.close = "---\n", "---\n"
	d.text = text
	d.root = root
	d.perr = nil
	return nil
}

// encodeHeader is a variable so a test can make encoding fail.
var encodeHeader = encodeMapping

func encodeMapping(root *yaml.Node) (string, error) {
	if len(root.Content) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, nil
}

// textKeys are header fields whose scalar value is always read as text, so a
// title like `1984` or an id of only digits is not turned into a number.
var textKeys = map[string]bool{"id": true, "title": true, "subtitle": true, "source": true, "type": true, "created": true, "icon": true, "color": true}

func metaFromNode(root *yaml.Node) Meta {
	m := Meta{}
	seen := map[string]bool{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if seen[key] {
			continue
		}
		seen[key] = true
		m = append(m, Field{key, nodeValue(key, root.Content[i+1], 0)})
	}
	return m
}

func nodeValue(key string, n *yaml.Node, depth int) any {
	if depth > 32 {
		return nil
	}
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	switch n.Kind {
	case yaml.MappingNode:
		m := Meta{}
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if seen[k] {
				continue
			}
			seen[k] = true
			m = append(m, Field{k, nodeValue("", n.Content[i+1], depth+1)})
		}
		return m
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			// Tag names are text even when they look like numbers.
			k := ""
			if key == "tags" {
				k = "id"
			}
			out = append(out, nodeValue(k, c, depth+1))
		}
		return out
	case yaml.ScalarNode:
		tag := n.ShortTag()
		if tag == "!!null" {
			return nil
		}
		if textKeys[key] {
			return n.Value
		}
		switch tag {
		case "!!int":
			var i int64
			if n.Decode(&i) == nil {
				return i
			}
			var f float64
			if n.Decode(&f) == nil {
				return f
			}
		case "!!float":
			var f float64
			if n.Decode(&f) == nil {
				return f
			}
		case "!!bool":
			var b bool
			if n.Decode(&b) == nil {
				return b
			}
		}
		return n.Value
	}
	return nil
}

var dateOnly = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// dateLike is text that a YAML 1.1 reader (PyYAML, say) takes for a date or a
// time stamp when it is written without quotes, and then fails on if it is not
// a real one.
var dateLike = regexp.MustCompile(`^\d{4}-\d{1,2}-\d{1,2}(?:(?:[Tt]|[ \t]+)\d{1,2}:\d{2}:\d{2}(?:\.\d*)?(?:[ \t]*(?:Z|[-+]\d{1,2}(?::\d{2})?))?)?$`)

// isCalendarDate reports whether s is a real date written YYYY-MM-DD.
func isCalendarDate(s string) bool {
	if !dateOnly.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func valueNode(key string, v any) *yaml.Node {
	switch x := v.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	case string:
		// `created` is a date in the file, not a quoted string.
		// A date in any other property of the page is written the same way, so
		// a date field reads as a date in other tools too.
		// Only a real date in the exact form YYYY-MM-DD; text that merely looks
		// like one is a string. (A node tagged !!timestamp whose text is no
		// time stamp is written with the tag spelled out, `!!timestamp 2026-13-45`,
		// which other readers cannot take.)
		if isCalendarDate(x) && (key == "created" || (key != "" && !textKeys[key] && key != "tags")) {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!timestamp", Value: x}
		}
		n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: x}
		if dateLike.MatchString(x) {
			// Quoted, so that no reader takes it for a date it is not.
			n.Style = yaml.DoubleQuotedStyle
		}
		return n
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(x)}
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(x, 10)}
	case float64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(x, 'g', -1, 64)}
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		allScalar, width := true, 0
		for _, e := range x {
			// An element is written by the rule of its list's key, so a real date in a
			// list of dates is a date, as it is alone (and tags stay text).
			c := valueNode(key, e)
			if c.Kind != yaml.ScalarNode {
				allScalar = false
			}
			width += len(c.Value) + 2
			n.Content = append(n.Content, c)
		}
		// Short lists of plain values stay on one line, like `tags: [a, b]`.
		if allScalar && width <= 72 {
			n.Style = yaml.FlowStyle
		}
		return n
	case Meta:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, f := range x {
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: f.Key}, valueNode("", f.Value))
		}
		return n
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprint(v)}
}

// ID returns the page id from the header, or "".
func (m Meta) ID() string { return strings.TrimSpace(m.String("id")) }

// Title returns the title, or "" when the page has none. An empty or blank
// title counts as no title.
func (m Meta) Title() string { return strings.TrimSpace(m.String("title")) }

// Subtitle returns the subtitle, or "".
func (m Meta) Subtitle() string { return strings.TrimSpace(m.String("subtitle")) }

// Sources returns where the text comes from (a person, a book, an address) in
// file order, without repeats. Like tags, a single text value counts as one
// source: people write a header by hand.
func (m Meta) Sources() []string { return m.listOf("source", true) }

// Type returns the page type, or "".
func (m Meta) Type() string { return strings.TrimSpace(m.String("type")) }

// Icon returns the icon of the page (a Lucide name or an emoji), or "". What
// is not one (a header written by hand) is no icon.
func (m Meta) Icon() string { return icons.Of(strings.TrimSpace(m.String("icon"))) }

// Color returns the colour of the page (one of icons.Colors), or "".
func (m Meta) Color() string { return icons.ColorOf(strings.TrimSpace(m.String("color"))) }

// Created returns the created date as written (YYYY-MM-DD), or "".
func (m Meta) Created() string { return strings.TrimSpace(m.String("created")) }

// Tags returns the tags in file order. A single text value counts as one tag.
func (m Meta) Tags() []string { return m.listOf("tags", false) }

func (m Meta) listOf(key string, unique bool) []string {
	v, _ := m.Get(key)
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" && !(unique && seen[s]) {
			seen[s] = true
			out = append(out, s)
		}
	}
	switch x := v.(type) {
	case string:
		add(x)
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				add(s)
			}
		}
	}
	return out
}
