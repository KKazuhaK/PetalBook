package space

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// A settings file that cannot be used is mostly not wrong all over: thirty
// folders were bound and one line has a typo, or one entry has an alias that
// goes round in a circle. What can still be read of it is not given up for
// the version restored from the history (restore.go); it is put onto it, entry
// by entry, the file as it is winning for each entry it has:
//
//   - A file that is YAML, but cannot be used (an entry over a limit, an alias
//     inside its own anchor, `folders` that is not a list of entries, a second
//     document), is read entry by entry: each top-level key, and each entry of
//     the list of entries, that can be used by itself (with its aliases written
//     out) is taken.
//   - A file that is not YAML is cut into its top-level keys, and the list of
//     entries into its entries, by their indentation; each piece that reads by
//     itself is taken as above.
//
// What cannot be read even so is in the file, and in the copy kept of it when
// the next change writes the settings; the notice of the file lists it
// (SettingsNotice.Lost). A type file is one JSON document: it is the version
// restored, with the notice.

// maxSalvage is the most of an unusable settings file that is read to take
// entries from: a file somewhat larger than the server reads may still give
// what fits.
func maxSalvage(f settingsFile) int {
	if f.good != nil {
		return f.max
	}
	return 4 * f.max
}

// piece is one top-level key, or one entry of the list of entries, read from a
// file that cannot be used: its value with no anchors or aliases left in it.
type piece struct {
	key   string
	entry bool
	val   *yaml.Node
}

// salvageInto puts what can be read of the file f as it is onto r, the
// settings restored for it, and notes in r what could not be.
func (h *healer) salvageInto(f settingsFile, r *restored) {
	if f.good != nil || f.section == "" {
		return
	}
	fi, blocked, err := lstatIn(h.root, f.rel)
	if err != nil || blocked != "" || !fi.Mode().IsRegular() {
		return // nothing to read: a link, a folder
	}
	file, err := os.Open(filepath.Join(h.root, filepath.FromSlash(f.rel)))
	if err != nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maxSalvage(f))+1))
	file.Close()
	if err != nil {
		return
	}
	if len(data) > maxSalvage(f) {
		r.lost = []string{"*"}
		return
	}
	pieces, lost := salvagePieces(f, data)
	if len(pieces) == 0 {
		r.lost = lost
		return
	}
	text, dropped, ok := mergePieces(f, r.text, pieces)
	if !ok {
		for _, p := range pieces {
			lost = append(lost, p.key)
		}
		r.lost = lost
		return
	}
	r.text, r.lost = text, append(lost, dropped...)
}

// salvagePieces reads the pieces of data that can be used one by one, in the
// order of the file, and names those that cannot.
func salvagePieces(f settingsFile, data []byte) (pieces []piece, lost []string) {
	docs, err := decodeAll(data)
	if err == nil {
		for _, d := range docs {
			ps, ls := piecesOf(f, d)
			pieces, lost = append(pieces, ps...), append(lost, ls...)
		}
		return pieces, lost
	}
	// Not YAML: by the lines.
	for _, b := range topBlocks(data) {
		if docs, err := decodeAll([]byte(b.text)); err == nil {
			for _, d := range docs {
				ps, ls := piecesOf(f, d)
				pieces, lost = append(pieces, ps...), append(lost, ls...)
			}
			continue
		}
		if b.key != f.section || !b.bare {
			lost = append(lost, b.key)
			continue
		}
		for _, e := range entryBlocks(b.body) {
			docs, err := decodeAll([]byte(f.section + ":\n" + e.text))
			if err != nil {
				lost = append(lost, e.key)
				continue
			}
			for _, d := range docs {
				ps, ls := piecesOf(f, d)
				pieces, lost = append(pieces, ps...), append(lost, ls...)
			}
		}
	}
	return pieces, lost
}

// decodeAll reads every document of data; an error when it is not YAML.
func decodeAll(data []byte) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var out []*yaml.Node
	for {
		var d yaml.Node
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
}

// piecesOf is the pieces of one document.
func piecesOf(f settingsFile, d *yaml.Node) (pieces []piece, lost []string) {
	if d.Kind != yaml.DocumentNode || len(d.Content) == 0 {
		return nil, nil
	}
	top := d.Content[0]
	if top.Kind == yaml.ScalarNode && top.Tag == "!!null" {
		return nil, nil
	}
	if top.Kind != yaml.MappingNode {
		return nil, []string{"*"}
	}
	take := func(key string, entry bool, v *yaml.Node) {
		if !selfContained(v) {
			lost = append(lost, key)
			return
		}
		left := f.max
		val := expanded(v, 0, &left)
		if left < 0 || !standalone(f, key, entry, val) {
			lost = append(lost, key)
			return
		}
		pieces = append(pieces, piece{key, entry, val})
	}
	eachPair(top, func(key string, v *yaml.Node, _ int) {
		if key != f.section {
			take(key, false, v)
			return
		}
		switch {
		case v == nil || (v.Kind == yaml.ScalarNode && v.Tag == "!!null"):
		case v.Kind != yaml.MappingNode:
			lost = append(lost, key)
		default:
			eachPair(v, func(k string, e *yaml.Node, _ int) { take(k, true, e) })
		}
	})
	return pieces, lost
}

// selfContained reports whether every alias under n refers to an anchor it is
// not inside of, and n is not deeper than a settings file may be with its
// aliases followed: what writing the aliases out ends with. Each anchor is
// looked at once, however many aliases refer to it.
func selfContained(n *yaml.Node) bool {
	height := map[*yaml.Node]int{}
	open := map[*yaml.Node]bool{}
	bad := false
	var walk func(n *yaml.Node, depth int) int
	walk = func(n *yaml.Node, depth int) int {
		if bad || n == nil || depth > maxDocDepth {
			bad = true
			return 0
		}
		if n.Kind == yaml.AliasNode {
			t := n.Alias
			if t == nil || open[t] {
				bad = true
				return 0
			}
			h, ok := height[t]
			if !ok {
				h = walk(t, depth+1)
			}
			if depth+h > maxDocDepth {
				bad = true
			}
			return h
		}
		open[n] = true
		h := 0
		for _, c := range n.Content {
			if ch := walk(c, depth+1) + 1; ch > h {
				h = ch
			}
		}
		delete(open, n)
		if n.Anchor != "" {
			height[n] = h
		}
		return h
	}
	walk(n, 0)
	return !bad
}

// standalone reports whether a piece makes a file that can be used by itself.
func standalone(f settingsFile, key string, entry bool, val *yaml.Node) bool {
	top := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if entry {
		sec := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{textNode(key), val}}
		top.Content = []*yaml.Node{textNode(f.section), sec}
	} else {
		top.Content = []*yaml.Node{textNode(key), val}
	}
	out, err := renderSettings(f, &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{top}})
	return err == nil && len(out) <= f.max && f.usable(out)
}

// renderSettings writes a settings document as its writer does.
func renderSettings(f settingsFile, doc *yaml.Node) ([]byte, error) {
	if f.rel == tagsFile {
		return yaml.Marshal(doc)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mergePieces puts the pieces onto base (nil: no settings), each in the place
// of what base has under its key. Pieces that would make the file larger than
// the server reads are left out, from the last, and named in dropped.
func mergePieces(f settingsFile, base []byte, pieces []piece) (text []byte, dropped []string, ok bool) {
	for n := len(pieces); n > 0; n = n * 3 / 4 {
		out, err := mergeOnto(f, base, pieces[:n])
		if err != nil {
			return nil, nil, false
		}
		if len(out) <= f.max {
			if !f.usable(out) {
				return nil, nil, false
			}
			for _, p := range pieces[n:] {
				dropped = append(dropped, p.key)
			}
			return out, dropped, true
		}
	}
	return nil, nil, false
}

func mergeOnto(f settingsFile, base []byte, pieces []piece) ([]byte, error) {
	var doc *yaml.Node
	if base != nil {
		d, err := f.parse(base)
		if err != nil {
			return nil, err
		}
		doc = d
	}
	if doc == nil {
		doc = emptyDoc("")
	}
	aliased := hasAlias(doc)
	top := own(doc, 0)
	var sec *yaml.Node
	for _, p := range pieces {
		into := top
		if p.entry {
			if sec == nil {
				sec = sectionOf(top, f.section)
			}
			into = sec
		}
		put(into, p.key, p.val)
	}
	if aliased {
		settle(doc)
	}
	return renderSettings(f, doc)
}

// sectionOf is the list of entries of top, the writer's own, made when it is
// not there or is not a mapping.
func sectionOf(top *yaml.Node, section string) *yaml.Node {
	v, at := mapValue(top, section)
	switch {
	case v != nil && v.Kind == yaml.MappingNode && at >= 0:
		return own(top, at+1)
	case at >= 0:
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		top.Content[at+1] = m
		return m
	}
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	top.Content = append(top.Content, textNode(section), m)
	return m
}

// put sets key in the mapping m (the writer's own, its merge keys written out)
// to val: in the place of its last writing, the earlier ones taken out.
func put(m *yaml.Node, key string, val *yaml.Node) {
	last := -1
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k, ok := keyText(m.Content[i]); ok && k == key {
			last = i
		}
	}
	if last < 0 {
		m.Content = append(m.Content, textNode(key), val)
		return
	}
	m.Content[last+1] = val
	out := m.Content[:0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k, ok := keyText(m.Content[i]); ok && k == key && i != last {
			continue
		}
		out = append(out, m.Content[i], m.Content[i+1])
	}
	m.Content = out
}

// block is a piece of the text of a file: a top-level key and what is
// indented under it.
type block struct {
	key  string
	text string // the lines, the first one included
	body string // the lines under the first one
	bare bool   // the first line has nothing after the key but a comment
}

// topBlocks cuts text into its top-level keys. Lines at the left edge that
// are comments or document markers belong to no key.
func topBlocks(data []byte) []block {
	var out []block
	var cur *block
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	for _, line := range strings.SplitAfter(string(data), "\n") {
		t := strings.TrimRight(line, "\r\n")
		switch {
		case strings.TrimSpace(t) == "" || strings.HasPrefix(strings.TrimLeft(t, " \t"), "#") && cur != nil:
			if cur != nil {
				cur.text += line
				cur.body += line
			}
		case t[0] == ' ' || t[0] == '\t':
			if cur != nil {
				cur.text += line
				cur.body += line
			}
		case t[0] == '#' || strings.HasPrefix(t, "---") || strings.HasPrefix(t, "..."):
			flush()
		default:
			flush()
			key, rest := splitKey(t)
			cur = &block{key: key, text: line, bare: strings.TrimSpace(rest) == "" || strings.HasPrefix(strings.TrimSpace(rest), "#")}
		}
	}
	flush()
	return out
}

// entryBlocks cuts the lines under the key of the list of entries into its
// entries, by the indentation of the first of them.
func entryBlocks(body string) []block {
	var out []block
	var cur *block
	indent := -1
	for _, line := range strings.SplitAfter(body, "\n") {
		t := strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimLeft(t, " \t")
		n := len(t) - len(trimmed)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if cur != nil {
				cur.text += line
			}
			continue
		}
		if indent < 0 {
			indent = n
		}
		if n <= indent {
			if cur != nil {
				out = append(out, *cur)
			}
			key, _ := splitKey(trimmed)
			cur = &block{key: key, text: line}
			continue
		}
		if cur != nil {
			cur.text += line
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// splitKey is the key of a line `key: rest` as a person would name it, and
// the rest. A line without a colon is its own name.
func splitKey(line string) (key, rest string) {
	if i := strings.Index(line, ":"); i >= 0 {
		key, rest = line[:i], line[i+1:]
	} else {
		key = line
	}
	key = strings.TrimSpace(key)
	if len(key) >= 2 && (key[0] == '"' && key[len(key)-1] == '"' || key[0] == '\'' && key[len(key)-1] == '\'') {
		key = key[1 : len(key)-1]
	}
	return key, rest
}
