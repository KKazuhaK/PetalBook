package space

import (
	"encoding/binary"
	"errors"
	"hash/maphash"
	"strconv"

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
// a filter, a value. What that costs is bounded for the document as a whole,
// when it is loaded (withinBudget): the nodes its aliases stand for beyond the
// ones written, how deep it goes with the aliases followed, and no alias inside
// its own anchor. A document over that is not readable, like one that does not
// parse; nothing is ever read from it, so no reader needs a budget of its own to
// stay fast, and no writer can be made to expand more than the budget.
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
// edit is made again on the document with every alias written out, which the
// budget keeps small. If that does not fit the file either, the write is
// refused and nothing is changed.
const (
	// maxAliasNodes is the most nodes the aliases of a settings file may stand
	// for, beyond the nodes written in it.
	maxAliasNodes = 100000
	// maxDocDepth is how deep a settings file may go, aliases followed.
	maxDocDepth = 100
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

// eachPair calls f for every text key of a mapping with what its value means,
// in the order "the last one counts": first what the merge keys bring in (what
// weighs least first), then the keys written in the mapping itself, in their
// order. at is the position of a key written in the mapping itself, and -1 for
// one that a merge key brought in.
func eachPair(m *yaml.Node, f func(key string, val *yaml.Node, at int)) {
	eachPairAt(deref(m), f, 0)
}

func eachPairAt(m *yaml.Node, f func(key string, val *yaml.Node, at int), depth int) {
	if m == nil || m.Kind != yaml.MappingNode || depth > maxDocDepth {
		return
	}
	if hasMerge(m) {
		merged := func(key string, val *yaml.Node, _ int) { f(key, val, -1) }
		for i := 0; i+1 < len(m.Content); i += 2 {
			if !isMerge(m.Content[i]) {
				continue
			}
			switch v := deref(m.Content[i+1]); {
			case v == nil:
			case v.Kind == yaml.MappingNode:
				eachPairAt(v, merged, depth+1)
			case v.Kind == yaml.SequenceNode:
				// The first of a list of mappings weighs most.
				for j := len(v.Content) - 1; j >= 0; j-- {
					eachPairAt(deref(v.Content[j]), merged, depth+1)
				}
			}
		}
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if key, ok := keyText(m.Content[i]); ok {
			f(key, deref(m.Content[i+1]), i)
		}
	}
}

// mapValue finds a key of a mapping by its text, whatever YAML made of it
// (a tag written 2026 is a number to YAML and the tag "2026" to us). It
// returns what the value means (an alias followed) and the index of the key in
// the mapping's content. A key that only a merge key brings in has the index
// -1; a key that is not there has no value and the index -1.
//
// A key written twice counts as its last writing, which is what a reader of
// the file as a map sees too.
func mapValue(m *yaml.Node, key string) (*yaml.Node, int) {
	m = deref(m)
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, -1
	}
	var v *yaml.Node
	at := -1
	merge := false
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if isMerge(k) {
			merge = true
		} else if t, ok := keyText(k); ok && t == key {
			v, at = deref(m.Content[i+1]), i
		}
	}
	if at < 0 && merge {
		eachPair(m, func(k string, val *yaml.Node, _ int) {
			if k == key {
				v = val
			}
		})
	}
	return v, at
}

// hasKey reports whether a mapping has a key, its own or merged in.
func hasKey(m *yaml.Node, key string) bool {
	v, at := mapValue(m, key)
	return v != nil || at >= 0
}

// withinBudget reports whether a settings document can be read: see the rule
// at the top. It looks at every written node once.
func withinBudget(doc *yaml.Node) bool {
	m := docMeasure{size: map[*yaml.Node]int{}, height: map[*yaml.Node]int{}}
	m.walk(doc, 0)
	return !m.bad
}

type docMeasure struct {
	// size and height of every anchored node seen so far, aliases followed;
	// the size is -1 while the node is being measured.
	size, height map[*yaml.Node]int
	extra        int
	bad          bool
}

func (m *docMeasure) walk(n *yaml.Node, depth int) (size, height int) {
	if m.bad || n == nil || depth > maxDocDepth {
		m.bad = true
		return 0, 0
	}
	if n.Kind == yaml.AliasNode {
		// An anchor that is not known yet, or that the alias is inside of.
		s, ok := m.size[n.Alias]
		if n.Alias == nil || !ok || s < 0 || depth+m.height[n.Alias] > maxDocDepth {
			m.bad = true
			return 0, 0
		}
		if m.extra += s - 1; m.extra > maxAliasNodes {
			m.bad = true
		}
		return s, m.height[n.Alias]
	}
	if n.Anchor != "" {
		m.size[n] = -1
	}
	size = 1
	for _, c := range n.Content {
		cs, ch := m.walk(c, depth+1)
		if m.bad {
			return 0, 0
		}
		size += cs
		if ch+1 > height {
			height = ch + 1
		}
	}
	if n.Anchor != "" {
		m.size[n], m.height[n] = size, height
	}
	return size, height
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
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !isMerge(m.Content[i]) {
			out = append(out, m.Content[i], m.Content[i+1])
			continue
		}
		if placed {
			continue
		}
		placed = true
		for _, key := range order {
			if v := vals[key]; v != nil {
				out = append(out, textNode(key), cloneNode(v))
				delete(vals, key)
			}
		}
	}
	m.Content = out
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
// stands for and takes the anchors away. The budget of a document that was
// loaded bounds what that makes.
func inlineAliases(n *yaml.Node) {
	n.Anchor = ""
	for i, c := range n.Content {
		if c.Kind == yaml.AliasNode && c.Alias != nil {
			n.Content[i] = expanded(c.Alias, 0)
		} else {
			inlineAliases(c)
		}
	}
}

func expanded(n *yaml.Node, depth int) *yaml.Node {
	if n = deref(n); n == nil || depth > maxDocDepth {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	}
	c := *n
	c.Anchor = ""
	if len(n.Content) > 0 {
		c.Content = make([]*yaml.Node, len(n.Content))
		for i, k := range n.Content {
			c.Content[i] = expanded(k, depth+1)
		}
	}
	return &c
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
		was, known := before[t]
		if !known || was != now[t] {
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
	max     int
	bad     error        // what loading returns for a file that cannot be used
	problem func() error // what is said of such a file
}

// settingsFullError is the refusal of a change that would make a settings file
// too large to be read. It is an *InvalidError too.
type settingsFullError struct{ *InvalidError }

func (e *settingsFullError) Unwrap() error { return e.InvalidError }

// unreadableEditError is the refusal of a change that would leave a settings
// file in a form that cannot be read again. It is an *InvalidError too.
type unreadableEditError struct{ *InvalidError }

func (e *unreadableEditError) Unwrap() error { return e.InvalidError }

// planSettingsEdit reads a settings file, lets edit change the document and
// returns the text to write in its place. Nothing is written here. changed is
// false when edit says there is nothing to write. render makes the text of the
// document; text of no bytes stands for a file that is to go.
//
// edit gets the top mapping, its own to change, and must go by the rule at the
// top of this file. It may run twice (the second time on the document with its
// aliases written out), so what it leaves outside the document must be right
// when the last run is the one that counts.
func planSettingsEdit(root string, f settingsFile, edit func(top *yaml.Node) (bool, error), render func(doc *yaml.Node) ([]byte, error)) (data []byte, changed bool, err error) {
	for attempt := 0; ; attempt++ {
		doc, err := loadYAMLAt(root, f.rel, int64(f.max), f.bad)
		if errors.Is(err, f.bad) {
			return nil, false, f.problem()
		}
		if err != nil {
			return nil, false, err
		}
		if doc == nil {
			doc = emptyDoc("")
		}
		aliased := hasAlias(doc)
		if aliased && attempt > 0 {
			inlineAliases(doc)
			aliased = false
		}
		var before map[*yaml.Node]uint64
		if aliased {
			before, _, _ = meanings(doc)
		}
		changed, err := edit(own(doc, 0))
		if err != nil || !changed {
			return nil, false, err
		}
		if aliased {
			settle(doc)
		}
		data, err := render(doc)
		if err != nil {
			return nil, false, err
		}
		if len(data) > f.max {
			// A file this large would not be read again, and every setting in it
			// would be lost with it.
			return nil, false, &settingsFullError{&InvalidError{"the change would make " + f.rel + " larger than " + strconv.Itoa(f.max) + " bytes, more than the server reads; nothing was changed"}}
		}
		ok := true
		if len(data) > 0 {
			back, perr := parseYAMLDoc(data, f.bad)
			ok = perr == nil && (!aliased || (back != nil && aliasesMeanTheSame(doc, before) && sameMeaning(doc.Content[0], back.Content[0])))
		}
		if ok {
			return data, true, nil
		}
		if !aliased {
			return nil, false, &unreadableEditError{&InvalidError{"the change cannot be written to " + f.rel + " in a form the server can read again without changing what other entries of the file mean (its anchors and aliases stand in the way); nothing was changed; write the aliases (*name) of the entry out by hand and try again"}}
		}
	}
}
