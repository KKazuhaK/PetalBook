package space

import (
	"bytes"
	"errors"
	"io"
	"sort"
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
	size  int // about how much of the file it takes, as textOf counts
}

// salvageInto puts what can be read of the file f as it is onto r, the
// settings restored for it, and notes in r what could not be.
func (h *healer) salvageInto(f settingsFile, r *restored) {
	if f.good != nil || f.section == "" {
		return
	}
	data, fi, blocked, err := readIn(h.root, f.rel, int64(maxSalvage(f)))
	if err != nil || blocked != "" || !fi.Mode().IsRegular() {
		return // nothing to read: a link, a folder
	}
	if data == nil || len(data) > maxSalvage(f) {
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
	// A byte order mark is not part of the first key.
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
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
		ps, ls := entryPieces(f, entryBlocks(b.body))
		pieces, lost = append(pieces, ps...), append(lost, ls...)
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

// entryPieces reads the entries cut from the text of the list of entries,
// each as if it were alone under the key of the list: what reads is taken
// (piecesOf), what does not is named. They are read many at once, and a run
// of them that does not read as exactly those entries, each by itself (one
// key on the first line of each, no alias anywhere, one document), is halved
// until it does or is one entry: the same as reading them one by one, at the
// cost of a few readings of the text instead of one per entry.
func entryPieces(f settingsFile, blocks []block) (pieces []piece, lost []string) {
	lines := make([]int, len(blocks)) // the line each starts on, counted from 0
	n := 0
	for i, b := range blocks {
		lines[i] = n
		n += strings.Count(b.text, "\n")
		if !strings.HasSuffix(b.text, "\n") {
			n++
		}
	}
	var read func(lo, hi int)
	read = func(lo, hi int) {
		var buf strings.Builder
		buf.WriteString(f.section + ":\n")
		for _, b := range blocks[lo:hi] {
			buf.WriteString(b.text)
			if !strings.HasSuffix(b.text, "\n") {
				buf.WriteString("\n")
			}
		}
		docs, err := decodeAll([]byte(buf.String()))
		if hi-lo == 1 {
			if err != nil {
				lost = append(lost, blocks[lo].key)
				return
			}
			for _, d := range docs {
				ps, ls := piecesOf(f, d)
				pieces, lost = append(pieces, ps...), append(lost, ls...)
			}
			return
		}
		if err == nil && len(docs) == 1 && eachOnItsOwn(docs[0], f.section, lines[lo:hi], lines[lo]) {
			ps, ls := piecesOf(f, docs[0])
			pieces, lost = append(pieces, ps...), append(lost, ls...)
			return
		}
		mid := (lo + hi) / 2
		read(lo, mid)
		read(mid, hi)
	}
	for lo := 0; lo < len(blocks); lo += entryBatch {
		read(lo, min(lo+entryBatch, len(blocks)))
	}
	return pieces, lost
}

// entryBatch is how many entries are read at once at most.
const entryBatch = 4096

// eachOnItsOwn reports whether d is a mapping of section to a mapping with one
// entry starting on each of the lines (counted from first, the line after
// the key of the section), and nothing in it is an alias.
func eachOnItsOwn(d *yaml.Node, section string, lines []int, first int) bool {
	if d.Kind != yaml.DocumentNode || len(d.Content) != 1 {
		return false
	}
	top := d.Content[0]
	if top.Kind != yaml.MappingNode || len(top.Content) != 2 || top.Content[0].Value != section {
		return false
	}
	m := top.Content[1]
	if m.Kind != yaml.MappingNode || len(m.Content) != 2*len(lines) {
		return false
	}
	for i, at := range lines {
		if m.Content[2*i].Line != at-first+2 {
			return false
		}
	}
	var alias func(n *yaml.Node) bool
	alias = func(n *yaml.Node) bool {
		if n.Kind == yaml.AliasNode {
			return true
		}
		for _, c := range n.Content {
			if alias(c) {
				return true
			}
		}
		return false
	}
	return !alias(m)
}

// piecesOf is the pieces of one document. Each must make a file that can be
// used by itself (standalone); they are checked many at once (usable).
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
	// In the order of the file: a piece to check, or a key that is lost.
	type item struct {
		p    piece
		lost bool
	}
	var items []item
	take := func(key string, entry bool, v *yaml.Node) {
		if !selfContained(v) {
			items = append(items, item{piece{key: key}, true})
			return
		}
		left := f.max
		val := expanded(v, 0, &left)
		if left < 0 {
			items = append(items, item{piece{key: key}, true})
			return
		}
		items = append(items, item{piece{key, entry, val, f.max - left}, false})
	}
	eachPair(top, func(key string, v *yaml.Node, _ int) {
		if key != f.section {
			take(key, false, v)
			return
		}
		switch {
		case v == nil || (v.Kind == yaml.ScalarNode && v.Tag == "!!null"):
		case v.Kind != yaml.MappingNode:
			items = append(items, item{piece{key: key}, true})
		default:
			eachPair(v, func(k string, e *yaml.Node, _ int) { take(k, true, e) })
		}
	})
	var cands []piece
	for _, it := range items {
		if !it.lost {
			cands = append(cands, it.p)
		}
	}
	ok := standaloneAll(f, cands)
	c := 0
	for _, it := range items {
		switch {
		case it.lost:
			lost = append(lost, it.p.key)
		case ok[c]:
			pieces = append(pieces, it.p)
			c++
		default:
			lost = append(lost, it.p.key)
			c++
		}
	}
	return pieces, lost
}

// standaloneAll reports for each piece whether it makes a file that can be
// used by itself. Many are put in one file and checked at once: what limits a
// file (its size, how deep it goes, what its merge keys bring in) is no less
// for a part of it, so when they all can be used together each one can by
// itself; when not, they are halved until each is looked at alone.
func standaloneAll(f settingsFile, ps []piece) []bool {
	ok := make([]bool, len(ps))
	var check func(lo, hi int)
	check = func(lo, hi int) {
		if hi-lo == 1 {
			ok[lo] = standalone(f, ps[lo].key, ps[lo].entry, ps[lo].val)
			return
		}
		size := 0
		for _, p := range ps[lo:hi] {
			size += p.size
		}
		if size <= f.max {
			top := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			var sec *yaml.Node
			for _, p := range ps[lo:hi] {
				if !p.entry {
					top.Content = append(top.Content, textNode(p.key), p.val)
					continue
				}
				if sec == nil {
					sec = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
					top.Content = append(top.Content, textNode(f.section), sec)
				}
				sec.Content = append(sec.Content, textNode(p.key), p.val)
			}
			out, err := renderSettings(f, &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{top}})
			if err == nil && len(out) <= f.max && f.usable(out) {
				for i := lo; i < hi; i++ {
					ok[i] = true
				}
				return
			}
		}
		mid := (lo + hi) / 2
		check(lo, mid)
		check(mid, hi)
	}
	if len(ps) > 0 {
		check(0, len(ps))
	}
	return ok
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
// the server reads are left out, from the last, and named in dropped. How
// many fit is first estimated from the sizes of the pieces, measured against
// what a part of them takes written out, so that the file is written out a few
// times, not once for each quarter less.
func mergePieces(f settingsFile, base []byte, pieces []piece) (text []byte, dropped []string, ok bool) {
	sum := make([]int, len(pieces)+1) // sum[i]: the sizes of the first i pieces
	for i, p := range pieces {
		sum[i+1] = sum[i] + p.size
	}
	// fit is how many of the first n pieces fit, at rate bytes written for
	// each byte of size.
	fit := func(n int, rate float64) int {
		room := float64(f.max-len(base)) / rate * 0.98
		return sort.Search(n, func(i int) bool { return float64(sum[i+1]) > room })
	}
	n := len(pieces)
	if sample := min(n, 256); sum[n] > f.max && sum[sample] > 0 {
		// The rate, from the first pieces written out on their own.
		if out, err := mergeOnto(f, nil, pieces[:sample]); err == nil {
			n = max(1, min(n, fit(n, float64(len(out))/float64(sum[sample]))))
		}
	}
	for n > 0 {
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
		next := n * 3 / 4
		if grown := len(out) - len(base); grown > 0 && sum[n] > 0 {
			if k := fit(n, float64(grown)/float64(sum[n])); k < next {
				next = k
			}
		}
		n = next
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
	var topPieces, secPieces []piece
	for _, p := range pieces {
		if p.entry {
			secPieces = append(secPieces, p)
		} else {
			topPieces = append(topPieces, p)
		}
	}
	putAll(top, topPieces)
	if len(secPieces) > 0 {
		// After the top-level keys: one of them may be the list of entries.
		putAll(sectionOf(top, f.section), secPieces)
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

// putAll sets each key of the pieces in the mapping m (the writer's own, its
// merge keys written out) to its value, the last piece of a key winning: in
// the place of the key's last writing in m, its earlier ones taken out; a key
// m does not have is added at the end, in the order of the pieces. One pass
// over m, however many pieces.
func putAll(m *yaml.Node, pieces []piece) {
	if len(pieces) == 0 {
		return
	}
	val := make(map[string]*yaml.Node, len(pieces))
	var order []string
	for _, p := range pieces {
		if _, ok := val[p.key]; !ok {
			order = append(order, p.key)
		}
		val[p.key] = p.val
	}
	last := map[string]int{}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k, ok := keyText(m.Content[i]); ok {
			if _, ok := val[k]; ok {
				last[k] = i
			}
		}
	}
	out := m.Content[:0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k, ok := keyText(m.Content[i]); ok {
			if v, ok := val[k]; ok {
				if last[k] != i {
					continue
				}
				out = append(out, m.Content[i], v)
				continue
			}
		}
		out = append(out, m.Content[i], m.Content[i+1])
	}
	for _, k := range order {
		if _, ok := last[k]; !ok {
			out = append(out, textNode(k), val[k])
		}
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

// lineSpan is one line of a text: where it starts and where the next starts.
type lineSpan struct{ start, end int }

// linesOf cuts text into its lines, the line ends kept with them.
func linesOf(text string) []lineSpan {
	var out []lineSpan
	for at := 0; at < len(text); {
		end := strings.IndexByte(text[at:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += at + 1
		}
		out = append(out, lineSpan{at, end})
		at = end
	}
	return out
}

// inScalar marks the lines that are inside a quoted value begun on a line
// before them: such a line is part of that value, wherever it starts, and
// never begins a key or an entry of its own (a value with a line `folders:`
// in it is not a second list of entries). A quote that is not closed before
// the end of the text closes nothing: it is a typo, and the lines after it are
// read as they stand. Each line is looked at a bounded number of times: a
// look for the end of a quote that does not find it goes to the end once for
// each kind of quote at most, since no line after it has that quote.
func inScalar(text string, lines []lineSpan) []bool {
	cont := make([]bool, len(lines))
	unclosed := [2]bool{} // a look for ' or " that found no end: none will
	for i := 0; i < len(lines); i++ {
		q := opensQuote(strings.TrimRight(text[lines[i].start:lines[i].end], "\r\n"))
		if q == 0 {
			continue
		}
		k := 0
		if q == '"' {
			k = 1
		}
		if unclosed[k] {
			continue
		}
		j := i + 1
		for ; j < len(lines); j++ {
			if closesQuote(strings.TrimRight(text[lines[j].start:lines[j].end], "\r\n"), q) >= 0 {
				break
			}
		}
		if j == len(lines) {
			unclosed[k] = true
			continue
		}
		for n := i + 1; n <= j; n++ {
			cont[n] = true
		}
		i = j
	}
	return cont
}

// opensQuote is the quote (' or ") a value on the line begins with and the
// line does not close: the value goes on on the next line. 0 when there is
// none. Only a value that starts with the quote is looked at, after a key
// (`key: "...`) or a dash (`- "...`): a quote inside a plain value is a letter.
func opensQuote(line string) byte {
	t := strings.TrimLeft(line, " \t")
	for strings.HasPrefix(t, "- ") || t == "-" {
		t = strings.TrimLeft(t[1:], " \t")
	}
	if t == "" || t[0] == '#' {
		return 0
	}
	// A quoted key first.
	if t[0] == '"' || t[0] == '\'' {
		end := closesQuote(t[1:], t[0])
		if end < 0 {
			return t[0] // `"key...` or `"value...` going on
		}
		t = t[1+end:]
		if !strings.HasPrefix(strings.TrimLeft(t, " \t"), ":") {
			return 0
		}
		t = strings.TrimLeft(t, " \t")[1:]
	} else {
		i := strings.Index(t, ": ")
		if i < 0 {
			i = strings.Index(t, ":\t")
		}
		if i < 0 {
			return 0
		}
		t = t[i+2:]
	}
	t = strings.TrimLeft(t, " \t")
	if t == "" || (t[0] != '"' && t[0] != '\'') {
		return 0
	}
	if closesQuote(t[1:], t[0]) < 0 {
		return t[0]
	}
	return 0
}

// closesQuote is the offset just past the quote q that ends a quoted value in
// s (s starts inside it), or -1. In "..." a backslash escapes the next
// letter; in '...' a quote is escaped by doubling it.
func closesQuote(s string, q byte) int {
	for i := 0; i < len(s); i++ {
		switch {
		case q == '"' && s[i] == '\\':
			i++
		case s[i] == q && q == '\'' && i+1 < len(s) && s[i+1] == '\'':
			i++
		case s[i] == q:
			return i + 1
		}
	}
	return -1
}

// topBlocks cuts text into its top-level keys. Lines at the left edge that
// are comments or document markers belong to no key; a line inside a quoted
// value begun before it belongs to the key of that value. Linear in the text:
// each block is a slice of it.
func topBlocks(data []byte) []block {
	text := string(data)
	lines := linesOf(text)
	cont := inScalar(text, lines)
	var out []block
	cur, first, start := -1, 0, 0 // the line the current block begins on, and where its body starts
	flush := func(end int) {
		if cur >= 0 {
			b := block{text: text[start:end], body: text[first:end]}
			t := strings.TrimRight(text[start:first], "\r\n")
			key, rest := splitKey(t)
			b.key = key
			b.bare = strings.TrimSpace(rest) == "" || strings.HasPrefix(strings.TrimSpace(rest), "#")
			out = append(out, b)
			cur = -1
		}
	}
	for i, l := range lines {
		t := strings.TrimRight(text[l.start:l.end], "\r\n")
		switch {
		case cont[i]:
			// Part of the value begun above: of the current block, or of none.
		case strings.TrimSpace(t) == "" || strings.HasPrefix(strings.TrimLeft(t, " \t"), "#") && cur >= 0:
		case t[0] == ' ' || t[0] == '\t':
		case t[0] == '#' || strings.HasPrefix(t, "---") || strings.HasPrefix(t, "..."):
			flush(l.start)
		default:
			flush(l.start)
			cur, start, first = i, l.start, l.end
		}
	}
	flush(len(text))
	return out
}

// entryBlocks cuts the lines under the key of the list of entries into its
// entries, by the indentation of the first of them; a line inside a quoted
// value begun before it stays with that value's entry.
func entryBlocks(body string) []block {
	lines := linesOf(body)
	cont := inScalar(body, lines)
	var out []block
	cur, start := -1, 0
	flush := func(end int) {
		if cur >= 0 {
			text := body[start:end]
			key, _ := splitKey(strings.TrimLeft(strings.TrimRight(body[lines[cur].start:lines[cur].end], "\r\n"), " \t"))
			out = append(out, block{key: key, text: text})
			cur = -1
		}
	}
	indent := -1
	for i, l := range lines {
		t := strings.TrimRight(body[l.start:l.end], "\r\n")
		trimmed := strings.TrimLeft(t, " \t")
		n := len(t) - len(trimmed)
		if cont[i] || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if cur < 0 && !cont[i] {
				start = l.end // nothing of an entry before the first one
			}
			continue
		}
		if indent < 0 {
			indent = n
		}
		if n <= indent {
			flush(l.start)
			cur, start = i, l.start
		}
	}
	flush(len(body))
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
