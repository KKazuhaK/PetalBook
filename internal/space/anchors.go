package space

import (
	"encoding/binary"
	"errors"
	"hash/maphash"
	"io/fs"
	"strconv"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
)

// The settings files (space.yml, tags.yml) are YAML that people write by hand
// and that comes in with a pull, so they may hold anchors (&name), aliases
// (*name) and merge keys (<<). There is one rule for them, and everything that
// reads or writes these files goes by it:
//
// Reading. An alias means exactly what its anchor means, and a mapping with a
// merge key has the keys of what it merges, wherever the server looks: the
// `folders` and `tags` lists, an entry, its type or colour, its views, a view,
// a filter, a value. What an anchor means is worked out once however many
// aliases refer to it (reading), so reading costs what is written in the file,
// not what its aliases stand for: an entry that ten thousand folders share by
// alias is as cheap as one that is written once. The only limits are ones a
// file a person keeps cannot reach (usableDoc): how deep it goes with the
// aliases followed, no alias inside its own anchor, and how many keys its
// merge keys bring in, each mapping that has one counted by itself. A document
// over them is not usable, like one that does not parse (see restore.go for
// what happens then).
//
// Writing. A write changes what it targets and nothing else:
//
//   - Before a node is changed in place it is made the writer's own (own): an
//     alias, or a node that carries an anchor, is replaced in its place by a copy
//     without anchors, and the merge keys of a mapping are written out as the
//     keys they bring in. The copy keeps the aliases inside it as aliases, so
//     it is never larger than what is written in the file.
//   - A value is replaced or removed by taking its node out of the tree, never
//     by writing over it.
//   - When the edit is done, an anchor whose node left the tree while aliases to
//     it remain moves to the first of them (settle); the others then point
//     there. Nothing is copied for that.
//
// Before the text is written it is checked: every alias still in the document
// stands for what it stood for before the edit, and the text reads back as the
// document that was meant. If not (an anchor name used twice can do that), the
// edit is made again on the document with every alias written out. If that
// does not fit the file, the write is refused and nothing is changed; no more
// is written out to find that than a file can hold (textOf).
const (
	// maxDocDepth is how deep a settings file may go, aliases followed.
	maxDocDepth = 100
	// maxMergedKeys is the most keys the merge keys of a settings file may
	// bring in, the keys each mapping with a merge key gets added up: ten
	// thousand folders that each merge a template of four hundred keys.
	maxMergedKeys = 4 << 20
)

// deref is what a node means when it is an alias: the node of its anchor.
func deref(n *yaml.Node) *yaml.Node {
	if n != nil && n.Kind == yaml.AliasNode {
		return n.Alias
	}
	return n
}

// isMerge reports whether a key of a mapping is a merge key (`<<`, not quoted).
func isMerge(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && k.Tag == "!!merge"
}

func hasMerge(m *yaml.Node) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if isMerge(m.Content[i]) {
			return true
		}
	}
	return false
}

// keyText is the text of a key of a mapping; ok is false for a key that is not
// text (a list, say) and for a merge key.
func keyText(k *yaml.Node) (string, bool) {
	if k == nil || isMerge(k) {
		return "", false
	}
	if k = deref(k); k == nil || k.Kind != yaml.ScalarNode {
		return "", false
	}
	return k.Value, true
}

// pair is a key of a mapping with what its value means.
type pair struct {
	key string
	val *yaml.Node
}

// merges works out what merge keys bring in, each mapping once however often
// it is merged. work counts the keys looked at; with a limit, it stops there.
type merges struct {
	brought map[*yaml.Node][]pair
	whole   map[*yaml.Node][]pair
	work    int
	limit   int
}

func (g *merges) over() bool { return g.limit > 0 && g.work > g.limit }

// keysInto adds pairs to a list that has each key once, at the place it was
// first seen, with the value that came last.
func keysInto(out []pair, at map[string]int, ps ...pair) []pair {
	for _, p := range ps {
		if i, ok := at[p.key]; ok {
			out[i].val = p.val
		} else {
			at[p.key] = len(out)
			out = append(out, p)
		}
	}
	return out
}

// from is the keys the merge keys of the mapping m bring in: what weighs
// least first, each key once with the value that counts.
func (g *merges) from(m *yaml.Node, depth int) []pair {
	if ps, ok := g.brought[m]; ok {
		return ps
	}
	if depth > maxDocDepth || g.over() {
		return nil
	}
	var out []pair
	at := map[string]int{}
	source := func(v *yaml.Node) {
		if v == nil || v.Kind != yaml.MappingNode || g.over() {
			return
		}
		ps := g.all(v, depth+1)
		g.work += len(ps) + 1
		out = keysInto(out, at, ps...)
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !isMerge(m.Content[i]) {
			continue
		}
		switch v := deref(m.Content[i+1]); {
		case v == nil:
		case v.Kind == yaml.MappingNode:
			source(v)
		case v.Kind == yaml.SequenceNode:
			// The first of a list of mappings weighs most.
			for j := len(v.Content) - 1; j >= 0; j-- {
				source(deref(v.Content[j]))
			}
		}
	}
	if g.brought == nil {
		g.brought = map[*yaml.Node][]pair{}
	}
	g.brought[m] = out
	return out
}

// all is every text key of the mapping m, merged ones first, each once.
func (g *merges) all(m *yaml.Node, depth int) []pair {
	if ps, ok := g.whole[m]; ok {
		return ps
	}
	var out []pair
	at := map[string]int{}
	if hasMerge(m) {
		out = keysInto(out, at, g.from(m, depth)...)
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if key, ok := keyText(m.Content[i]); ok {
			out = keysInto(out, at, pair{key, deref(m.Content[i+1])})
		}
	}
	if g.whole == nil {
		g.whole = map[*yaml.Node][]pair{}
	}
	g.whole[m] = out
	return out
}

// eachPair calls f for every text key of a mapping with what its value means,
// in the order "the last one counts": first what the merge keys bring in (each
// key once, what weighs least first), then the keys written in the mapping
// itself, in their order. at is the position of a key written in the mapping
// itself, and -1 for one that a merge key brought in.
func eachPair(m *yaml.Node, f func(key string, val *yaml.Node, at int)) {
	if m = deref(m); m == nil || m.Kind != yaml.MappingNode {
		return
	}
	if hasMerge(m) {
		var g merges
		for _, p := range g.from(m, 0) {
			f(p.key, p.val, -1)
		}
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if key, ok := keyText(m.Content[i]); ok {
			f(key, deref(m.Content[i+1]), i)
		}
	}
}

// reading remembers what was found in a document while it is read, so that
// what an anchor means is worked out once, not once for every alias of it. It
// is for one pass over a document; the zero value is ready. In a writer it
// remembers only what it found in nodes that carry an anchor, which a writer
// never changes in place (see the rule above).
type reading struct {
	vals  map[keyIn]found
	views map[*yaml.Node][]View
	ids   map[*yaml.Node][]string
}

type keyIn struct {
	m   *yaml.Node
	key string
}

type found struct {
	v  *yaml.Node
	at int
	ok bool
}

// find is value, and whether the mapping has the key at all.
func (r *reading) find(m *yaml.Node, key string, depth int) found {
	if m = deref(m); m == nil || m.Kind != yaml.MappingNode || depth > maxDocDepth {
		return found{at: -1}
	}
	if m.Anchor != "" {
		if x, ok := r.vals[keyIn{m, key}]; ok {
			return x
		}
	}
	x := found{at: -1}
	merge := false
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if isMerge(k) {
			merge = true
		} else if t, ok := keyText(k); ok && t == key {
			x = found{deref(m.Content[i+1]), i, true}
		}
	}
	if !x.ok && merge {
		// What weighs most first: the last merge key, and in a list the first.
	search:
		for i := len(m.Content) - 2; i >= 0; i -= 2 {
			if !isMerge(m.Content[i]) {
				continue
			}
			switch v := deref(m.Content[i+1]); {
			case v == nil:
			case v.Kind == yaml.MappingNode:
				if y := r.find(v, key, depth+1); y.ok {
					x = found{y.v, -1, true}
					break search
				}
			case v.Kind == yaml.SequenceNode:
				for _, c := range v.Content {
					if y := r.find(c, key, depth+1); y.ok {
						x = found{y.v, -1, true}
						break search
					}
				}
			}
		}
	}
	if m.Anchor != "" {
		if r.vals == nil {
			r.vals = map[keyIn]found{}
		}
		r.vals[keyIn{m, key}] = x
	}
	return x
}

// value is mapValue with what was found before remembered.
func (r *reading) value(m *yaml.Node, key string) (*yaml.Node, int) {
	x := r.find(m, key, 0)
	return x.v, x.at
}

func (r *reading) has(m *yaml.Node, key string) bool { return r.find(m, key, 0).ok }

// mapValue finds a key of a mapping by its text, whatever YAML made of it
// (a tag written 2026 is a number to YAML and the tag "2026" to us). It
// returns what the value means (an alias followed) and the index of the key in
// the mapping's content. A key that only a merge key brings in has the index
// -1; a key that is not there has no value and the index -1.
//
// A key written twice counts as its last writing, which is what a reader of
// the file as a map sees too.
func mapValue(m *yaml.Node, key string) (*yaml.Node, int) {
	var r reading
	return r.value(m, key)
}

// hasKey reports whether a mapping has a key, its own or merged in.
func hasKey(m *yaml.Node, key string) bool {
	var r reading
	return r.has(m, key)
}

// usableDoc reports whether a settings document can be read: see the rule at
// the top. It looks at every written node once, and at no more keys of merge
// keys than the limit.
func usableDoc(doc *yaml.Node) bool {
	m := docMeasure{height: map[*yaml.Node]int{}, open: map[*yaml.Node]bool{}}
	m.walk(doc, 0)
	if m.bad {
		return false
	}
	g := merges{limit: maxMergedKeys}
	var each func(n *yaml.Node) bool
	each = func(n *yaml.Node) bool {
		if n.Kind == yaml.MappingNode && hasMerge(n) {
			if g.from(n, 0); g.over() {
				return false
			}
		}
		for _, c := range n.Content {
			if !each(c) {
				return false
			}
		}
		return true
	}
	return each(doc)
}

type docMeasure struct {
	// height of every anchored node seen so far, aliases followed; open while
	// the node is being measured.
	height map[*yaml.Node]int
	open   map[*yaml.Node]bool
	bad    bool
}

func (m *docMeasure) walk(n *yaml.Node, depth int) (height int) {
	if m.bad || n == nil || depth > maxDocDepth {
		m.bad = true
		return 0
	}
	if n.Kind == yaml.AliasNode {
		// An anchor that is not known yet, or that the alias is inside of.
		h, ok := m.height[n.Alias]
		if n.Alias == nil || !ok || m.open[n.Alias] || depth+h > maxDocDepth {
			m.bad = true
			return 0
		}
		return h
	}
	if n.Anchor != "" {
		m.open[n], m.height[n] = true, 0
	}
	for _, c := range n.Content {
		ch := m.walk(c, depth+1)
		if m.bad {
			return 0
		}
		if ch+1 > height {
			height = ch + 1
		}
	}
	if n.Anchor != "" {
		m.open[n], m.height[n] = false, height
	}
	return height
}

// hasAlias reports whether a document has an alias anywhere.
func hasAlias(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode {
		return true
	}
	for _, c := range n.Content {
		if hasAlias(c) {
			return true
		}
	}
	return false
}

// cloneNode is a copy of a node and of what is written under it, without the
// anchors. An alias in it stays an alias of the same anchor, so the copy is as
// large as what is written, not as what it stands for.
func cloneNode(n *yaml.Node) *yaml.Node {
	c := *n
	c.Anchor = ""
	if len(n.Content) > 0 {
		c.Content = make([]*yaml.Node, len(n.Content))
		for i, k := range n.Content {
			c.Content[i] = cloneNode(k)
		}
	}
	return &c
}

// owned is a node a writer may change in place without changing what anything
// else in the document means: n itself when nothing else can refer to it, else
// a copy of what it means. The merge keys of a mapping are written out.
func owned(n *yaml.Node) *yaml.Node {
	switch {
	case n.Kind == yaml.AliasNode:
		if n.Alias == nil {
			return n
		}
		c := cloneNode(n.Alias)
		if n.HeadComment != "" || n.LineComment != "" || n.FootComment != "" {
			c.HeadComment, c.LineComment, c.FootComment = n.HeadComment, n.LineComment, n.FootComment
		}
		n = c
	case n.Anchor != "":
		// The node with the anchor leaves the tree as it is; if aliases to it
		// remain, settle gives it to the first of them.
		n = cloneNode(n)
	}
	if n.Kind == yaml.MappingNode {
		flattenMerges(n)
	}
	return n
}

// own makes the node at position i of parent the writer's own (see owned) and
// returns it.
func own(parent *yaml.Node, i int) *yaml.Node {
	n := owned(parent.Content[i])
	parent.Content[i] = n
	return n
}

// flattenMerges writes the keys the merge keys of a mapping bring in into the
// mapping itself, where the first merge key was, and takes the merge keys out.
// What the mapping means stays the same.
//
// The comments of a merge key stay where it was: the comment above it goes
// above the first key written in its place, the one on its line onto the line
// of that key, the one below it below the last of them. A merge key in whose
// place nothing is written (all it brings in is written in the mapping too)
// leaves its comments above the key after it, or below the last key.
func flattenMerges(m *yaml.Node) {
	if !hasMerge(m) {
		return
	}
	var order []string
	vals := map[string]*yaml.Node{}
	eachPair(m, func(key string, val *yaml.Node, at int) {
		if at >= 0 {
			delete(vals, key) // written in the mapping itself
			return
		}
		if _, seen := vals[key]; !seen {
			order = append(order, key)
		}
		vals[key] = val
	})
	out := make([]*yaml.Node, 0, len(m.Content)+2*len(vals))
	placed := false
	pending := "" // comments of merge keys that no key was written for yet
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if !isMerge(k) {
			if pending != "" {
				nk := *k
				nk.HeadComment = joinComments(pending, k.HeadComment)
				k, pending = &nk, ""
			}
			out = append(out, k, v)
			continue
		}
		head := joinComments(k.HeadComment, v.HeadComment)
		line := joinComments(k.LineComment, v.LineComment)
		foot := joinComments(v.FootComment, k.FootComment)
		first := len(out)
		if !placed {
			placed = true
			for _, key := range order {
				if val := vals[key]; val != nil {
					out = append(out, textNode(key), cloneNode(val))
					delete(vals, key)
				}
			}
		}
		if len(out) == first {
			pending = joinComments(pending, head, line, foot)
			continue
		}
		fk, fv := out[first], out[first+1]
		fk.HeadComment = joinComments(pending, head)
		pending = ""
		if line != "" {
			// The line of a key whose value is written under it is the key's.
			if (fv.Kind == yaml.MappingNode || fv.Kind == yaml.SequenceNode) && fv.Style&yaml.FlowStyle == 0 && len(fv.Content) > 0 {
				fk.LineComment = line
			} else {
				fv.LineComment = line
			}
		}
		out[len(out)-2].FootComment = foot
	}
	if pending != "" && len(out) > 0 {
		lk := *out[len(out)-2]
		lk.FootComment = joinComments(lk.FootComment, pending)
		out[len(out)-2] = &lk
	}
	m.Content = out
}

// joinComments is comments one after the other, each on lines of its own.
func joinComments(parts ...string) string {
	out := ""
	for _, p := range parts {
		switch {
		case p == "":
		case out == "":
			out = p
		default:
			out += "\n" + p
		}
	}
	return out
}

// settle makes a document that an edit took nodes out of whole again: an alias
// whose anchor is no longer in the tree gets the anchor's node in its place,
// the first such alias in the order of the document, and the later ones then
// refer to it there. It looks at every node once and copies nothing.
func settle(doc *yaml.Node) {
	live := map[*yaml.Node]bool{}
	var mark func(n *yaml.Node)
	mark = func(n *yaml.Node) {
		live[n] = true
		for _, c := range n.Content {
			mark(c)
		}
	}
	mark(doc)
	// adopt brings a node that was outside the tree into it. What is under it
	// and already in the tree (an anchor that moved to an earlier alias) is
	// referred to instead of being there twice.
	var adopt func(n *yaml.Node)
	adopt = func(n *yaml.Node) {
		live[n] = true
		for i, c := range n.Content {
			switch {
			case !live[c]:
				adopt(c)
			case c.Kind == yaml.AliasNode:
				cp := *c
				n.Content[i] = &cp
			case c.Anchor != "":
				n.Content[i] = &yaml.Node{Kind: yaml.AliasNode, Value: c.Anchor, Alias: c}
			default:
				n.Content[i] = cloneNode(c)
			}
		}
	}
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		for i, c := range n.Content {
			if c.Kind == yaml.AliasNode && c.Alias != nil && !live[c.Alias] {
				x := c.Alias
				if x.HeadComment == "" && x.LineComment == "" && x.FootComment == "" {
					x.HeadComment, x.LineComment, x.FootComment = c.HeadComment, c.LineComment, c.FootComment
				}
				adopt(x)
				n.Content[i] = x
				c = x
			}
			walk(c)
		}
	}
	walk(doc)
}

// inlineAliases writes every alias of a document out as a copy of what it
// stands for and takes the anchors away. left is how many bytes of text the
// copies may still come to (see textOf); below zero it has given up, and the
// document is not to be used.
func inlineAliases(n *yaml.Node, left *int) {
	n.Anchor = ""
	for i, c := range n.Content {
		if *left < 0 {
			return
		}
		if c.Kind == yaml.AliasNode && c.Alias != nil {
			n.Content[i] = expanded(c.Alias, 0, left)
		} else {
			inlineAliases(c, left)
		}
	}
}

func expanded(n *yaml.Node, depth int, left *int) *yaml.Node {
	if n = deref(n); n == nil || depth > maxDocDepth || *left < 0 {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	}
	*left -= 1 + len(n.Value) // as textOf counts
	c := *n
	c.Anchor = ""
	if len(n.Content) > 0 {
		c.Content = make([]*yaml.Node, len(n.Content))
		for i, k := range n.Content {
			c.Content[i] = expanded(k, depth+1, left)
		}
	}
	return &c
}

// textOf is the least text n can be written as, in bytes: its scalars and the
// names of its aliases, and for every node something to tell it from the next. What
// is written is never shorter, so an edit whose copies come to more than a
// file may hold cannot fit, and need not be made to find that out.
func textOf(n *yaml.Node) int {
	switch n.Kind {
	case yaml.ScalarNode:
		return len(n.Value) + 1
	case yaml.AliasNode:
		return len(n.Value) + 2
	}
	c := 1
	for _, k := range n.Content {
		c += textOf(k)
	}
	return c
}

// stripper takes keys off many entries of a mapping in one pass, for a writer:
// each entry is made the writer's own first (see owned). Entries that are
// aliases of one anchor stay one entry written once: the first of them gets
// what the anchor is without the keys, under an anchor of its own, and the
// others are aliases of that. So taking the type off ten thousand folders that
// share an entry leaves a file as small as it was, and what else refers to the
// old anchor keeps what it had. An entry that would be left empty costs
// nothing. The text of what is copied is added up (textOf), and when that is
// more than the file may hold the edit is given up before the next copy.
//
// These are the only aliases the server writes.
type stripper struct {
	file  settingsFile
	top   *yaml.Node // the top of the document, for the anchor names in use
	keys  []string
	r     reading
	bare  map[*yaml.Node]*yaml.Node // by anchor: what it is without the keys, where it was first written
	names map[string]bool
	made  int
}

// has reports whether the value of an entry is a mapping with one of the keys.
func (s *stripper) has(v *yaml.Node) bool {
	e := deref(v)
	if e == nil || e.Kind != yaml.MappingNode {
		return false
	}
	for _, k := range s.keys {
		if s.r.has(e, k) {
			return true
		}
	}
	return false
}

// name is an anchor name that is not in use, made from the one an entry was
// an alias of.
func (s *stripper) name(of string) string {
	if s.names == nil {
		s.names = map[string]bool{}
		var walk func(n *yaml.Node)
		walk = func(n *yaml.Node) {
			if n.Anchor != "" {
				s.names[n.Anchor] = true
			}
			for _, c := range n.Content {
				walk(c)
			}
		}
		if s.top != nil {
			walk(s.top)
		}
	}
	base := of + "-no-" + strings.Join(s.keys, "-")
	name := base
	for n := 2; s.names[name]; n++ {
		name = base + "-" + strconv.Itoa(n)
	}
	s.names[name] = true
	return name
}

// strip makes the value at position i of cont the writer's own, without the keys.
func (s *stripper) strip(cont *yaml.Node, i int) error {
	v := cont.Content[i]
	if v.Kind == yaml.AliasNode && v.Alias != nil {
		b, seen := s.bare[v.Alias]
		var n *yaml.Node
		switch {
		case !seen:
			b = owned(&yaml.Node{Kind: yaml.AliasNode, Value: v.Value, Alias: v.Alias})
			for _, k := range s.keys {
				removeKey(b, k)
			}
			if s.bare == nil {
				s.bare = map[*yaml.Node]*yaml.Node{}
			}
			s.bare[v.Alias] = b
			s.made += textOf(b)
			n = b
		case len(b.Content) == 0:
			n = &yaml.Node{Kind: b.Kind, Tag: b.Tag, Style: b.Style}
		default:
			if b.Anchor == "" {
				b.Anchor = s.name(v.Value)
			}
			n = &yaml.Node{Kind: yaml.AliasNode, Value: b.Anchor, Alias: b}
			s.made += len(b.Anchor) + 2
		}
		n.HeadComment, n.LineComment, n.FootComment = v.HeadComment, v.LineComment, v.FootComment
		cont.Content[i] = n
	} else {
		copied := v.Anchor != "" || (v.Kind == yaml.MappingNode && hasMerge(v))
		n := own(cont, i)
		for _, k := range s.keys {
			removeKey(n, k)
		}
		if copied {
			s.made += textOf(n)
		}
	}
	if s.made > s.file.max {
		return tooFull(s.file)
	}
	return nil
}

// replaced collects, for the document an edit is working on, the entries it
// found unusable and wrote over (an entry that is a line of text, `views` that
// is not a list, a `type` that is not text). Whoever writes the document keeps
// the file as it was beside it, so what was there is not lost (see
// restore.go). It is keyed by the top of the document.
var replaced sync.Map

// noteReplaced says an edit wrote over something unusable at what, in the
// document whose top is top.
func noteReplaced(top *yaml.Node, what string) {
	list, _ := replaced.Load(top)
	l, _ := list.([]string)
	replaced.Store(top, append(l, what))
}

func takeReplaced(top *yaml.Node) []string {
	list, ok := replaced.LoadAndDelete(top)
	if !ok {
		return nil
	}
	return list.([]string)
}

// unusable reports whether the value of an entry, or of one of its settings,
// is something where kind (a mapping, a list, a scalar) should be: there, not
// empty, and not that.
func unusable(v *yaml.Node, kind yaml.Kind) bool {
	if v == nil || v.Kind == kind {
		return false
	}
	return !(v.Kind == yaml.ScalarNode && (v.Tag == "!!null" || v.Value == ""))
}

// oddEntries lists the entries of the file's section that cannot be used as
// they are, and the settings of entries that cannot (`Tang: views`): they read
// as not there. At most 20 are listed.
func oddEntries(doc *yaml.Node, f settingsFile) []string {
	if doc == nil {
		return nil
	}
	m, _ := mapValue(doc.Content[0], f.section)
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	var r reading
	odd := map[string]bool{}
	var order []string
	eachPair(m, func(k string, e *yaml.Node, _ int) {
		say := func(what string, is bool) {
			switch {
			case is && !odd[what]:
				odd[what] = true
				order = append(order, what)
			case !is:
				delete(odd, what) // a later writing of the entry counts
			}
		}
		say(k, unusable(e, yaml.MappingNode))
		views, _ := r.value(e, "views")
		say(k+": views", unusable(views, yaml.SequenceNode))
		val, _ := r.value(e, f.setting)
		say(k+": "+f.setting, val != nil && val.Kind != yaml.ScalarNode)
	})
	var out []string
	for _, what := range order {
		if odd[what] && len(out) < 20 {
			out = append(out, what)
		}
	}
	return out
}

var meaningSeed = maphash.MakeSeed()

// meanings is what every anchored node of a document stands for, as a hash of
// its content with the aliases followed, and the anchors its aliases refer to.
// ok is false when an alias comes before its anchor.
func meanings(doc *yaml.Node) (of map[*yaml.Node]uint64, used []*yaml.Node, ok bool) {
	of = map[*yaml.Node]uint64{}
	ok = true
	var hash func(n *yaml.Node) uint64
	hash = func(n *yaml.Node) uint64 {
		if n.Kind == yaml.AliasNode {
			h, known := of[n.Alias]
			if !known {
				ok = false
			}
			used = append(used, n.Alias)
			return h
		}
		var h maphash.Hash
		h.SetSeed(meaningSeed)
		h.WriteByte(byte(n.Kind))
		if n.Kind == yaml.ScalarNode {
			h.WriteString(n.Tag)
			h.WriteByte(0)
			h.WriteString(n.Value)
		}
		var b [8]byte
		for _, c := range n.Content {
			binary.LittleEndian.PutUint64(b[:], hash(c))
			h.Write(b[:])
		}
		s := h.Sum64()
		if n.Anchor != "" {
			of[n] = s
		}
		return s
	}
	hash(doc)
	return of, used, ok
}

// aliasesMeanTheSame reports whether every alias of a document stands for what
// its anchor stood for when before was taken.
func aliasesMeanTheSame(doc *yaml.Node, before map[*yaml.Node]uint64) bool {
	now, used, ok := meanings(doc)
	if !ok {
		return false
	}
	for _, t := range used {
		// An anchor that was not there before is one the edit made (stripper):
		// whether its aliases read back as it is what sameMeaning looks at.
		if was, known := before[t]; known && was != now[t] {
			return false
		}
	}
	return true
}

// sameMeaning reports whether two documents say the same, aliases followed:
// the tree that was edited and the text that was made of it, read back.
func sameMeaning(a, b *yaml.Node) bool {
	return sameNodes(a, b, map[[2]*yaml.Node]bool{})
}

func sameNodes(a, b *yaml.Node, seen map[[2]*yaml.Node]bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Kind == yaml.AliasNode && b.Kind == yaml.AliasNode {
		pair := [2]*yaml.Node{a.Alias, b.Alias}
		if seen[pair] {
			return true
		}
		if !sameNodes(a.Alias, b.Alias, seen) {
			return false
		}
		seen[pair] = true
		return true
	}
	if a, b = deref(a), deref(b); a == nil || b == nil {
		return a == b
	}
	if a.Kind != b.Kind || len(a.Content) != len(b.Content) {
		return false
	}
	if a.Kind == yaml.ScalarNode && (a.Value != b.Value || (a.Tag != b.Tag && a.Tag != "" && b.Tag != "")) {
		return false
	}
	for i := range a.Content {
		if !sameNodes(a.Content[i], b.Content[i], seen) {
			return false
		}
	}
	return true
}

// settingsFile is one of the two settings files, as the editing of it needs it.
type settingsFile struct {
	rel     string
	section string            // the key of its list of entries: `folders`, `tags`
	setting string            // the setting of an entry besides its views: `type`, `color`
	good    func([]byte) bool // for a file that is not YAML settings (a type): whether its text can be used
	max     int
	bad     error        // what loading returns for a file that cannot be used
	problem func() error // what is said of such a file where nothing can restore it
}

// usable reports whether text can be the file.
func (f settingsFile) usable(data []byte) bool {
	if f.good != nil {
		return f.good(data)
	}
	_, err := f.parse(data)
	return err == nil
}

// repair is what a writer of a settings file has to do besides writing it,
// because of what the edit found: the file could not be used and from stands
// in for it, or entries of it could not and were written over.
type repair struct {
	from    *restored
	entries []string
}

// parse reads the text of the file: parseYAMLDoc, and its list of entries is a
// list of entries (or is not there). A file whose `folders` is a line of text
// can no more be used than one that is not YAML.
func (f settingsFile) parse(data []byte) (*yaml.Node, error) {
	doc, err := parseYAMLDoc(data, f.bad)
	if err != nil || doc == nil {
		return doc, err
	}
	if v, _ := mapValue(doc.Content[0], f.section); v != nil && v.Kind != yaml.MappingNode && !(v.Kind == yaml.ScalarNode && v.Tag == "!!null") {
		return nil, f.bad
	}
	return doc, nil
}

// loadSettingsAt reads a settings file of the space at root: nil when there is
// none or nothing in it, f.bad for one that cannot be used.
func loadSettingsAt(root string, f settingsFile) (*yaml.Node, error) {
	doc, _, err := loadSettingsSeen(root, f)
	return doc, err
}

// loadSettingsSeen is loadSettingsAt, with what the read found there.
func loadSettingsSeen(root string, f settingsFile) (*yaml.Node, seen, error) {
	data, saw, err := readSettingsSeen(root, f)
	if err != nil || data == nil {
		return nil, saw, err
	}
	doc, err := f.parse(data)
	return doc, saw, err
}

// readSettingsAt is the text of a settings file: nil when there is no file,
// f.bad for one that is not a regular file, is larger than the server reads,
// or is behind something on its way that is not a folder (see settingspath.go).
func readSettingsAt(root string, f settingsFile) ([]byte, error) {
	data, _, err := readSettingsSeen(root, f)
	return data, err
}

// readSettingsSeen is readSettingsAt, with what the read found there.
func readSettingsSeen(root string, f settingsFile) ([]byte, seen, error) {
	data, fi, blocked, err := readIn(root, f.rel, int64(f.max))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, seen{}, nil
	}
	if errors.Is(err, errChanged) {
		return nil, seen{}, f.bad // replaced while it was read: not used as it is
	}
	saw := seen{fi, blocked}
	if err != nil {
		return nil, seen{}, err
	}
	// A link in the way could lead out of the space.
	if blocked != "" || !fi.Mode().IsRegular() || data == nil || len(data) > f.max {
		return nil, saw, f.bad
	}
	return data, saw, nil
}

// settingsFullError is the refusal of a change that would make a settings file
// too large to be read. It is an *InvalidError too.
type settingsFullError struct{ *InvalidError }

func (e *settingsFullError) Unwrap() error { return e.InvalidError }

// unreadableEditError is the refusal of a change that would leave a settings
// file in a form that cannot be read again. It is an *InvalidError too.
type unreadableEditError struct{ *InvalidError }

func (e *unreadableEditError) Unwrap() error { return e.InvalidError }

// tooFull is the refusal of a change that would make a settings file too large.
func tooFull(f settingsFile) error {
	return &settingsFullError{&InvalidError{Msg: "the change would make " + f.rel + " larger than " + strconv.Itoa(f.max) + " bytes, more than the server reads; nothing was changed"}}
}

// planSettingsEdit reads a settings file, lets edit change the document and
// returns the text to write in its place. Nothing is written here. changed is
// false when edit says there is nothing to write. render makes the text of the
// document; text of no bytes stands for a file that is to go.
//
// A file that cannot be used is not edited: the edit is made on the settings
// h restores for it (see restore.go), and from says so; whoever writes the
// text then puts the file as it is aside first. Without h such a file is the
// problem of f.
//
// edit gets the top mapping, its own to change, and must go by the rule at the
// top of this file. It may run twice (the second time on the document with its
// aliases written out), so what it leaves outside the document must be right
// when the last run is the one that counts.
func planSettingsEdit(root string, f settingsFile, h *healer, edit func(top *yaml.Node) (bool, error), render func(doc *yaml.Node) ([]byte, error)) (data []byte, changed bool, rep *repair, err error) {
	for attempt := 0; ; attempt++ {
		var from *restored
		doc, err := loadSettingsAt(root, f)
		if errors.Is(err, f.bad) {
			if h == nil {
				return nil, false, nil, f.problem()
			}
			if from, err = h.restore(f); err != nil {
				return nil, false, nil, err
			}
			doc, err = from.doc(f)
		}
		if err != nil {
			return nil, false, nil, err
		}
		if doc == nil {
			doc = emptyDoc("")
		}
		aliased := hasAlias(doc)
		if aliased && attempt > 0 {
			left := f.max
			if inlineAliases(doc, &left); left < 0 {
				return nil, false, nil, tooFull(f)
			}
			aliased = false
		}
		var before map[*yaml.Node]uint64
		if aliased {
			before, _, _ = meanings(doc)
		}
		top := own(doc, 0)
		changed, err := edit(top)
		entries := takeReplaced(top)
		if err != nil || !changed {
			return nil, false, nil, err
		}
		if aliased {
			settle(doc)
		}
		data, err := render(doc)
		if err != nil {
			return nil, false, nil, err
		}
		if len(data) > f.max {
			// A file this large would not be read again, and every setting in it
			// would be lost with it.
			return nil, false, nil, tooFull(f)
		}
		ok := true
		if len(data) > 0 {
			back, perr := f.parse(data)
			ok = perr == nil && (!aliased || (back != nil && aliasesMeanTheSame(doc, before) && sameMeaning(doc.Content[0], back.Content[0])))
		}
		if ok {
			if from != nil || len(entries) > 0 {
				rep = &repair{from, entries}
			}
			return data, true, rep, nil
		}
		if !aliased {
			return nil, false, nil, &unreadableEditError{&InvalidError{Msg: "the change cannot be written to " + f.rel + " in a form the server can read again without changing what other entries of the file mean (its anchors and aliases stand in the way); nothing was changed"}}
		}
	}
}
