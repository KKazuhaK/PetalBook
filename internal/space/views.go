package space

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
)

// A view is a saved way of looking at the pages of a folder or of a tag: a
// layout with a sort, filters, a field to group a board by, and the columns of
// a table that are hidden. A folder's views are written under its entry in
// space.yml, next to its type; a tag's under its entry in tags.yml, next to its
// colour:
//
//	folders:
//	  文案:
//	    type: wenan
//	    views:
//	      - id: k3f9
//	        name: 按状态
//	        layout: board
//	        group: status
//	        sort: -prop:written
//	        filter: [{key: series, op: in, values: [冬季]}]
//
// The first view is the default one. A key of a view that the server does not
// know stays where it is, and so do comments: a view that is saved again is
// changed in its own YAML, not written anew.
const (
	// MaxViews is the most views a folder or a tag has.
	MaxViews = 20

	maxViewName   = 200
	maxViewHidden = 200
	maxViewKey    = 256
	maxViewExtra  = 64
	viewIDLen     = 6

	// What the keys the server does not know may take, so that a write cannot
	// make settings too large to be read again: a view is at most maxViewBytes
	// as JSON, the views a request changes at most maxViewsBytes together, and
	// a view has at most maxViewExtraKeys such keys, each at most
	// maxViewExtraBytes as JSON.
	maxViewBytes  = 16 << 10
	maxViewsBytes = 128 << 10
	// MaxViewsBody is the most a request that replaces the views may be: every
	// view at its largest, and room for the rest of the request.
	MaxViewsBody      = MaxViews*maxViewBytes + 512<<10
	maxViewExtraKeys  = 16
	maxViewExtraBytes = 4 << 10

	// maxNodeBudget is the most YAML nodes read for one value of a view the
	// server does not know, aliases followed; a larger one is left out. (What
	// the aliases of the whole file may stand for is bounded when it is
	// loaded: see anchors.go.)
	maxNodeBudget = 10000
	maxNodeDepth  = 32
)

// ViewLayouts are the layouts a view may have.
var ViewLayouts = []string{"list", "cards", "table", "board", "read"}

// ViewFilter is one condition of a view, written as in a listing's `filter`.
type ViewFilter struct {
	Key    string `json:"key"`
	Op     string `json:"op"`
	Values []any  `json:"values"`
}

// View is a saved view.
type View struct {
	ID     string
	Name   string
	Layout string
	Sort   string
	Group  string
	Filter []ViewFilter
	Hidden []string
	// Extra are the keys the server does not know, as the file has them.
	Extra map[string]any
}

// ViewList is the views of a folder or a tag with the rev they are changed
// from: a digest of the views as the server reads them, so that any change to
// the file, by the server or by hand, is a new rev.
type ViewList struct {
	Views []View
	Rev   string
}

// ViewScope says whose views: a folder's (Folder set, "" for the root) or a
// tag's.
type ViewScope struct {
	Folder *string
	Tag    string
}

// ViewsConflictError means the views are not as the request thought: another
// change came first. Current is how they are now.
type ViewsConflictError struct{ Current ViewList }

func (e *ViewsConflictError) Error() string { return "the views were changed by someone else" }

var viewKnown = map[string]bool{"id": true, "name": true, "layout": true, "sort": true, "group": true, "filter": true, "hidden": true}

// MarshalJSON writes a view with every key it knows (empty ones as "" or []),
// and the keys it does not know alongside.
func (v View) MarshalJSON() ([]byte, error) {
	o := make(map[string]any, len(v.Extra)+7)
	for k, e := range v.Extra {
		o[k] = e
	}
	filter, hidden := v.Filter, v.Hidden
	if filter == nil {
		filter = []ViewFilter{}
	}
	for i := range filter {
		if filter[i].Values == nil {
			filter[i].Values = []any{}
		}
	}
	if hidden == nil {
		hidden = []string{}
	}
	o["id"], o["name"], o["layout"], o["sort"], o["group"], o["filter"], o["hidden"] = v.ID, v.Name, v.Layout, v.Sort, v.Group, filter, hidden
	return json.Marshal(o)
}

// UnmarshalJSON reads a view as a request gives it. A key of the wrong kind is
// refused; a key that is not known is kept in Extra.
func (v *View) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return invalidf("A view must be an object.")
	}
	*v = View{}
	text := func(key string, into *string) error {
		r, ok := raw[key]
		if !ok || string(r) == "null" {
			return nil
		}
		if err := json.Unmarshal(r, into); err != nil {
			return invalidf("The %s of a view must be text.", key)
		}
		return nil
	}
	for _, f := range []struct {
		key  string
		into *string
	}{{"id", &v.ID}, {"name", &v.Name}, {"layout", &v.Layout}, {"sort", &v.Sort}, {"group", &v.Group}} {
		if err := text(f.key, f.into); err != nil {
			return err
		}
	}
	if r, ok := raw["filter"]; ok && string(r) != "null" {
		dec := json.NewDecoder(bytes.NewReader(r))
		dec.UseNumber()
		dec.DisallowUnknownFields()
		if err := dec.Decode(&v.Filter); err != nil {
			return invalidf("The filter of a view must be a list of {\"key\", \"op\", \"values\"}.")
		}
		for i := range v.Filter {
			for j := range v.Filter[i].Values {
				v.Filter[i].Values[j] = plain(v.Filter[i].Values[j])
			}
		}
	}
	if r, ok := raw["hidden"]; ok && string(r) != "null" {
		if err := json.Unmarshal(r, &v.Hidden); err != nil {
			return invalidf("The hidden columns of a view must be a list of keys.")
		}
	}
	for k, r := range raw {
		if viewKnown[k] {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(r))
		dec.UseNumber()
		var val any
		if err := dec.Decode(&val); err != nil {
			return invalidf("The key %q of a view is not valid JSON.", k)
		}
		if v.Extra == nil {
			v.Extra = map[string]any{}
		}
		v.Extra[k] = plain(val)
	}
	return nil
}

// plain turns the numbers a JSON decoder produced as json.Number, and ints, into int64 or
// float64, so that a value is the same whether it came from JSON or YAML.
func plain(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	case int:
		return int64(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = plain(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = plain(e)
		}
		return out
	}
	return v
}

func newViewList(vs []View) ViewList {
	if vs == nil {
		vs = []View{}
	}
	b, _ := json.Marshal(vs)
	return ViewList{Views: vs, Rev: page.Rev(b)}
}

// --- reading views out of a YAML document --------------------------------------------

// nodeValue is a YAML value as plain Go: text, int64, float64, bool, nil,
// []any, map[string]any. A date written bare is text, as it is in a page. A
// value of more than maxNodeBudget nodes (aliases counted as often as they are
// used) or deeper than maxNodeDepth is nil.
func nodeValue(n *yaml.Node) any {
	v, _ := nodeValueWithin(n)
	return v
}

// nodeValueWithin is nodeValue; ok is false for a value that is too large.
func nodeValueWithin(n *yaml.Node) (v any, ok bool) {
	w := nodeWalk{left: maxNodeBudget}
	v = w.value(n, 0)
	if w.over {
		return nil, false
	}
	return v, true
}

// nodeWalk is the budget of a reading of YAML values.
type nodeWalk struct {
	left int
	over bool
}

func (w *nodeWalk) value(n *yaml.Node, depth int) any {
	if w.left--; w.left < 0 || depth > maxNodeDepth {
		w.over = true
		return nil
	}
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode {
		if n.Alias == nil {
			return nil
		}
		return w.value(n.Alias, depth+1)
	}
	switch n.Kind {
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!null":
			return nil
		case "!!bool":
			var b bool
			if n.Decode(&b) == nil {
				return b
			}
		case "!!int":
			var i int64
			if n.Decode(&i) == nil {
				return i
			}
		case "!!float":
			var f float64
			if n.Decode(&f) == nil {
				return f
			}
		}
		return n.Value
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			out = append(out, w.value(c, depth+1))
			if w.over {
				return nil
			}
		}
		return out
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		eachPair(n, func(key string, val *yaml.Node, _ int) {
			if !w.over {
				out[key] = w.value(val, depth+1)
			}
		})
		if w.over {
			return nil
		}
		return out
	}
	return nil
}

// valueNode is a plain Go value as a YAML node.
func valueNode(v any) *yaml.Node {
	switch x := plain(v).(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	case string:
		return textNode(x)
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(x)}
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(x, 10)}
	case float64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(x, 'g', -1, 64)}
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, e := range x {
			n.Content = append(n.Content, valueNode(e))
		}
		return n
	case map[string]any:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			n.Content = append(n.Content, textNode(k), valueNode(x[k]))
		}
		return n
	}
	return textNode("")
}

func sameValue(a, b any) bool {
	x, _ := json.Marshal(plain(a))
	y, _ := json.Marshal(plain(b))
	return bytes.Equal(x, y)
}

func scalarText(n *yaml.Node) (string, bool) {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return "", false
	}
	return n.Value, true
}

// viewOf reads a view from its mapping node. An alias anywhere in it is read
// as its anchor, and a merge key as the keys it brings in.
func viewOf(n *yaml.Node) View {
	w := &nodeWalk{left: maxNodeBudget}
	var v View
	v.ID, _ = scalarText(first(mapValue(n, "id")))
	v.Name, _ = scalarText(first(mapValue(n, "name")))
	v.Layout, _ = scalarText(first(mapValue(n, "layout")))
	v.Sort, _ = scalarText(first(mapValue(n, "sort")))
	v.Group, _ = scalarText(first(mapValue(n, "group")))
	if f, _ := mapValue(n, "filter"); f != nil && f.Kind == yaml.SequenceNode {
		for _, c := range f.Content {
			if c = deref(c); c == nil || c.Kind != yaml.MappingNode {
				continue
			}
			vf := ViewFilter{Values: []any{}}
			vf.Key, _ = scalarText(first(mapValue(c, "key")))
			vf.Op, _ = scalarText(first(mapValue(c, "op")))
			if vals, _ := mapValue(c, "values"); vals != nil && vals.Kind == yaml.SequenceNode {
				for _, e := range vals.Content {
					vf.Values = append(vf.Values, w.value(e, 0))
				}
			}
			v.Filter = append(v.Filter, vf)
		}
	}
	if h, _ := mapValue(n, "hidden"); h != nil && h.Kind == yaml.SequenceNode {
		for _, c := range h.Content {
			if t, ok := scalarText(deref(c)); ok {
				v.Hidden = append(v.Hidden, t)
			}
		}
	}
	last := map[string]*yaml.Node{}
	eachPair(n, func(key string, val *yaml.Node, _ int) {
		if !viewKnown[key] {
			last[key] = val
		}
	})
	for k, val := range last {
		// Each value by itself, so that which ones are read does not depend
		// on the order they are looked at in.
		x, ok := nodeValueWithin(val)
		if !ok {
			continue // what is too large to read is left out (and left in the file)
		}
		if v.Extra == nil {
			v.Extra = map[string]any{}
		}
		v.Extra[k] = x
	}
	return v
}

func validLayout(l string) bool {
	for _, x := range ViewLayouts {
		if x == l {
			return true
		}
	}
	return false
}

// pseudoID is the id of the view at position i of an entry whose written id is
// not usable. No real id starts with @.
func pseudoID(i int) string { return "@" + strconv.Itoa(i) }

// readable makes what was read from a file that may have been written by hand
// something a client can work with: every view has an id, valid and used once
// (a missing, malformed or repeated one is the pseudo id of the view's position,
// which PutViews takes for that view and replaces with a new id), and a layout.
func readable(vs []View) []View {
	seen := map[string]bool{}
	for i := range vs {
		if id := vs[i].ID; !validViewID(id) || seen[id] {
			vs[i].ID = pseudoID(i)
		} else {
			seen[id] = true
		}
		if !validLayout(vs[i].Layout) {
			vs[i].Layout = "list"
		}
	}
	return vs
}

func first(n *yaml.Node, _ int) *yaml.Node { return n }

// viewSlots finds the views of an entry: the list under its `views` as it
// reads (nil when there is none), and the positions in it of the views the
// server reads. Those are the first MaxViews mappings of the list, aliases of
// one included; anything else in it (written by hand) is not a view.
func viewSlots(entry *yaml.Node) (seq *yaml.Node, at []int) {
	seq, _ = mapValue(entry, "views")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil, nil
	}
	for i, c := range seq.Content {
		if c = deref(c); c != nil && c.Kind == yaml.MappingNode {
			if at = append(at, i); len(at) == MaxViews {
				break
			}
		}
	}
	return seq, at
}

// viewNodes are the views of an entry as they read: see viewSlots.
func viewNodes(entry *yaml.Node) []*yaml.Node {
	seq, at := viewSlots(entry)
	out := make([]*yaml.Node, len(at))
	for i, j := range at {
		out[i] = deref(seq.Content[j])
	}
	return out
}

func viewsIn(entry *yaml.Node) []View {
	var out []View
	for _, n := range viewNodes(entry) {
		out = append(out, viewOf(n))
	}
	return readable(out)
}

// viewsIn is viewsIn, read once for a list of views however many entries have
// it (by alias, or through a merge key). The views are not the caller's to
// change.
func (r *reading) viewsIn(entry *yaml.Node) []View {
	seq, _ := r.value(entry, "views")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	if vs, ok := r.views[seq]; ok {
		return vs
	}
	vs := viewsIn(entry)
	if r.views == nil {
		r.views = map[*yaml.Node][]View{}
	}
	r.views[seq] = vs
	return vs
}

// viewIDs is the ids written in the views of an entry, read once for a list.
func (r *reading) viewIDs(entry *yaml.Node) []string {
	seq, _ := r.value(entry, "views")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	if ids, ok := r.ids[seq]; ok {
		return ids
	}
	var ids []string
	for _, n := range viewNodes(entry) {
		if id, ok := scalarText(first(mapValue(n, "id"))); ok {
			ids = append(ids, id)
		}
	}
	if r.ids == nil {
		r.ids = map[*yaml.Node][]string{}
	}
	r.ids[seq] = ids
	return ids
}

// viewsOf reads the views of every entry of the mapping `section` of a
// settings document (folders in space.yml, tags in tags.yml), by the key norm
// makes of the entry's. An entry written twice counts as its last.
func viewsOf(doc *yaml.Node, section string, norm func(string) string) map[string][]View {
	if doc == nil {
		return nil
	}
	m, _ := mapValue(doc.Content[0], section)
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	out := map[string][]View{}
	var r reading
	eachPair(m, func(k string, entry *yaml.Node, _ int) {
		key := norm(k)
		delete(out, key)
		if vs := r.viewsIn(entry); len(vs) > 0 {
			out[key] = vs
		}
	})
	return out
}

// tagCache is the views of tags.yml for as long as the file looks the same.
type tagCache struct {
	valid   bool
	mode    fs.FileMode
	size    int64
	mtimeNs int64
	views   map[string][]View
	blocked string
}

func (s *Space) tagViews() map[string][]View {
	fi, blocked, err := lstatIn(s.root, tagsFile)
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.heal.fine(tagsSettings)
		}
		s.tagCfg = tagCache{}
		return nil
	}
	if c := s.tagCfg; c.valid && c.blocked == blocked && c.mode == fi.Mode() && c.size == fi.Size() && c.mtimeNs == fi.ModTime().UnixNano() {
		return c.views
	}
	c := tagCache{valid: true, mode: fi.Mode(), size: fi.Size(), mtimeNs: fi.ModTime().UnixNano(), blocked: blocked}
	afterLook(s.root, tagsFile)
	doc, saw, err := s.loadSettingsSeen(tagsSettings)
	if err == nil {
		c.views = viewsOf(doc, "tags", func(k string) string { return k })
		s.heal.setOdd(tagsSettings, oddEntries(doc, tagsSettings))
	}
	if saw.is(fi, blocked) { // as in config
		s.tagCfg = c
	} else {
		s.tagCfg = tagCache{}
	}
	return c.views
}

// Views returns the views of a folder or a tag, in order, the first being the
// default. A file that cannot be used shows those restored for it. The folder is not
// looked for on disk: one that is not there has no views.
func (s *Space) Views(scope ViewScope) (ViewList, error) {
	if err := checkScope(scope); err != nil {
		return ViewList{}, err
	}
	if scope.Folder != nil {
		return newViewList(append([]View(nil), s.config().views[bindingKey(*scope.Folder)]...)), nil
	}
	return newViewList(append([]View(nil), s.tagViews()[scope.Tag]...)), nil
}

func checkScope(scope ViewScope) error {
	switch {
	case scope.Folder != nil && scope.Tag != "":
		return invalidf("Give a folder or a tag, not both.")
	case scope.Folder != nil:
		return checkRel(*scope.Folder)
	case scope.Tag != "":
		return checkTagName(scope.Tag)
	}
	return invalidf("Give the folder or the tag the views belong to.")
}

// --- checking views ----------------------------------------------------------------

func checkViewText(what, s string, max int) error {
	if !utf8.ValidString(s) || len(s) > max || strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return invalidf("%s is not valid: text of at most %d bytes on one line.", what, max)
	}
	return nil
}

func validViewID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// sameView reports whether two views are the same as JSON.
func sameView(a, b View) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// checkViews validates what a request gives as the views of a folder or tag.
// stored are the views the entry has now, by id: one that the request gives
// back as it is stored passes without a look (it may have been written by hand,
// and a client that saves another view must not be refused for it).
func checkViews(vs []View, stored map[string]View) error {
	if len(vs) > MaxViews {
		return invalidf("A folder or a tag has at most %d views.", MaxViews)
	}
	ids := map[string]bool{}
	total := 0
	for i, v := range vs {
		at := "view " + strconv.Itoa(i+1)
		old, isStored := stored[v.ID]
		if v.ID != "" {
			if !validViewID(v.ID) && !isStored {
				return invalidf("%s: that is not a view id.", at)
			}
			if ids[v.ID] {
				return invalidf("%s: the id %q is used twice.", at, v.ID)
			}
			ids[v.ID] = true
		}
		if isStored && sameView(old, v) {
			continue
		}
		if strings.TrimSpace(v.Name) == "" {
			return invalidf("%s needs a name.", at)
		}
		if err := checkViewText(at+": the name", v.Name, maxViewName); err != nil {
			return err
		}
		if !validLayout(v.Layout) {
			return invalidf("%s: the layout must be one of %s.", at, strings.Join(ViewLayouts, ", "))
		}
		if err := checkViewText(at+": the sort", v.Sort, maxViewKey+8); err != nil {
			return err
		}
		if err := index.ValidSort(v.Sort); err != nil {
			return invalidf("%s: %s.", at, strings.TrimSuffix(strings.TrimPrefix(err.Error(), index.ErrInvalidQuery.Error()+": "), "."))
		}
		if err := checkViewText(at+": the group", v.Group, maxViewKey); err != nil {
			return err
		}
		fs := make([]index.Filter, len(v.Filter))
		for j, f := range v.Filter {
			fs[j] = index.Filter{Key: f.Key, Op: f.Op, Values: f.Values}
		}
		if err := index.CheckFilters(fs); err != nil {
			return invalidf("%s: %s.", at, strings.TrimSuffix(strings.TrimPrefix(err.Error(), index.ErrInvalidQuery.Error()+": "), "."))
		}
		if len(v.Hidden) > maxViewHidden {
			return invalidf("%s: at most %d hidden columns.", at, maxViewHidden)
		}
		seen := map[string]bool{}
		for _, h := range v.Hidden {
			if h == "" || seen[h] {
				return invalidf("%s: the hidden columns must be keys, each once.", at)
			}
			seen[h] = true
			if err := checkViewText(at+": a hidden column", h, maxViewKey); err != nil {
				return err
			}
		}
		if len(v.Extra) > maxViewExtraKeys {
			return invalidf("%s: at most %d keys the server does not know.", at, maxViewExtraKeys)
		}
		for k, e := range v.Extra {
			if k == "" || len(k) > maxViewExtra || !utf8.ValidString(k) {
				return invalidf("%s: a key of a view is 1 to %d bytes.", at, maxViewExtra)
			}
			if b, err := json.Marshal(e); err != nil || len(b) > maxViewExtraBytes {
				return invalidf("%s: the value of %q is larger than %d bytes.", at, k, maxViewExtraBytes)
			}
		}
		b, err := json.Marshal(v)
		if err != nil || len(b) > maxViewBytes {
			return invalidf("%s is larger than %d bytes.", at, maxViewBytes)
		}
		if total += len(b); total > maxViewsBytes {
			return invalidf("The views changed are larger than %d bytes together.", maxViewsBytes)
		}
	}
	return nil
}

// normalViews is a copy of views with every value in its plain form (see plain),
// so that what is saved is what is read back whatever the caller's number types.
func normalViews(vs []View) []View {
	out := make([]View, len(vs))
	for i, v := range vs {
		if len(v.Filter) > 0 {
			fs := make([]ViewFilter, len(v.Filter))
			for j, f := range v.Filter {
				fs[j] = ViewFilter{Key: f.Key, Op: f.Op, Values: make([]any, len(f.Values))}
				for k, e := range f.Values {
					fs[j].Values[k] = plain(e)
				}
			}
			v.Filter = fs
		}
		if len(v.Extra) > 0 {
			extra := make(map[string]any, len(v.Extra))
			for k, e := range v.Extra {
				extra[k] = plain(e)
			}
			v.Extra = extra
		}
		out[i] = v
	}
	return out
}

// --- changing views ------------------------------------------------------------------

// newViewID makes an id that is not in taken: viewIDLen letters and digits.
func newViewID(taken map[string]bool) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	for try := 0; try < 100; try++ {
		b := make([]byte, viewIDLen)
		for i := range b {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return "", err
			}
			b[i] = alphabet[n.Int64()]
		}
		if id := string(b); !taken[id] {
			taken[id] = true
			return id, nil
		}
	}
	return "", errors.New("no free view id")
}

// idsOf adds the ids of the views in the mapping `section` of doc to into,
// leaving out the entry that skip says is the one being changed.
func idsOf(doc *yaml.Node, section string, skip func(key string) bool, into map[string]bool) {
	if doc == nil {
		return
	}
	m, _ := mapValue(doc.Content[0], section)
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	var r reading
	eachPair(m, func(k string, entry *yaml.Node, _ int) {
		if skip(k) {
			return
		}
		for _, id := range r.viewIDs(entry) {
			into[id] = true
		}
	})
}

// PutViews replaces the views of a folder or a tag. baseRev is the rev of the
// views the request was made from (see ViewList); when they are not as that
// says, nothing is written and the answer is a *ViewsConflictError. A view
// with an id must be one of the views the folder or tag has; one without gets
// a new id, unique in the space. The views are written into the YAML of the
// entry in place: a view that is kept keeps its comments and the keys the
// request does not mention, a view that is gone is removed.
//
// The change is journaled and committed like any other. A folder that is not
// there is ErrNoFolder.
func (s *Space) PutViews(by Author, scope ViewScope, baseRev string, views []View) (ViewList, error) {
	if err := checkScope(scope); err != nil {
		return ViewList{}, err
	}
	if len(views) > MaxViews {
		return ViewList{}, invalidf("A folder or a tag has at most %d views.", MaxViews)
	}
	views = normalViews(views)
	return mutate(s, by, func(tx *txn) (ViewList, error) {
		var out ViewList
		if scope.Folder != nil {
			canon, existed, err := s.resolveFolder(*scope.Folder)
			if errors.Is(err, ErrExists) || (err == nil && !existed) {
				return out, ErrNoFolder
			}
			if err != nil {
				return out, err
			}
			taken := map[string]bool{}
			if tdoc, err := s.loadTags(); err == nil {
				idsOf(tdoc, "tags", func(string) bool { return false }, taken)
			}
			err = s.editSpaceFile(tx, func(root *yaml.Node) (bool, error) {
				idsOf(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}, "folders", func(k string) bool { return bindingKey(k) == canon }, taken)
				changed, res, err := putViewsInto(root, "folders", foldersNode, canon, bindingKey, baseRev, views, taken)
				out = res
				return changed, err
			})
			return out, err
		}
		taken := map[string]bool{}
		if sdoc, err := s.loadSettings(spaceSettings); err == nil {
			idsOf(sdoc, "folders", func(string) bool { return false }, taken)
		}
		err := s.editTagsFile(tx, func(root *yaml.Node) (bool, error) {
			idsOf(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}, "tags", func(k string) bool { return k == scope.Tag }, taken)
			changed, res, err := putViewsInto(root, "tags", tagsNode, scope.Tag, func(k string) string { return k }, baseRev, views, taken)
			out = res
			return changed, err
		})
		return out, err
	})
}

// putViewsInto makes the views of the entry key of the mapping section of the
// settings document top be views. taken holds the ids in use elsewhere in the
// space; it gains the ids this makes.
func putViewsInto(top *yaml.Node, section string, section2 func(*yaml.Node, bool) (*yaml.Node, error), key string,
	norm func(string) string, baseRev string, views []View, taken map[string]bool) (bool, ViewList, error) {
	cont, err := section2(top, false)
	if err != nil {
		return false, ViewList{}, err
	}
	// The entry is read as it means here (an alias followed, merge keys
	// counted) and made the writer's own only where it is changed, below.
	isEntry := func(i int) bool {
		k, ok := keyText(cont.Content[i])
		return ok && norm(k) == key
	}
	var entry *yaml.Node
	at := -1
	if cont != nil {
		for i := 0; i+1 < len(cont.Content); i += 2 {
			if isEntry(i) {
				at = i
			}
		}
		if at >= 0 {
			entry = deref(cont.Content[at+1])
		}
	}
	oddEntry := false
	if entry != nil && entry.Kind != yaml.MappingNode {
		// Not settings (`Tang: yes`): it reads as none, and a write that puts
		// views there writes over it; the file as it was is kept.
		oddEntry = unusable(entry, yaml.MappingNode)
		entry = nil
	}
	var slots []int // where the views are in the list of the entry
	if entry != nil {
		_, slots = viewSlots(entry)
	}
	var cur []View
	if entry != nil {
		cur = viewsIn(entry) // as the client read them: the same ids, pseudo ones included
	}
	byID := map[string]int{}
	stored := make(map[string]View, len(cur))
	for i, c := range cur {
		byID[c.ID] = i
		stored[c.ID] = c
	}
	curList := newViewList(cur)
	if baseRev != curList.Rev {
		return false, ViewList{}, &ViewsConflictError{curList}
	}
	if err := checkViews(views, stored); err != nil {
		return false, ViewList{}, err
	}

	// The views as they are to be: ids given, keys the request does not mention kept.
	for _, v := range views {
		if v.ID != "" && !strings.HasPrefix(v.ID, "@") {
			taken[v.ID] = true
		}
	}
	result := make([]View, len(views))
	matched := make([]int, len(views)) // the position in slots of the view it was, or -1
	// unchanged[i] says the request gives view i back exactly as it was read:
	// its YAML node is then left as it is (it may hold what the reader could
	// not interpret, written by hand).
	unchanged := make([]bool, len(views))
	for i, v := range views {
		if v.ID == "" {
			id, err := newViewID(taken)
			if err != nil {
				return false, ViewList{}, err
			}
			v.ID = id
			matched[i] = -1
		} else {
			j, ok := byID[v.ID]
			if !ok {
				return false, ViewList{}, invalidf("View %d: there is no view with the id %q here; leave the id out to make a new view.", i+1, v.ID)
			}
			matched[i] = j
			unchanged[i] = !strings.HasPrefix(v.ID, "@") && sameView(stored[v.ID], v)
			if strings.HasPrefix(v.ID, "@") {
				// A view whose written id was not usable gets a new one.
				id, err := newViewID(taken)
				if err != nil {
					return false, ViewList{}, err
				}
				v.ID = id
			}
			if len(cur[j].Extra) > 0 {
				extra := map[string]any{}
				for k, e := range cur[j].Extra {
					extra[k] = e
				}
				for k, e := range v.Extra {
					extra[k] = e
				}
				v.Extra = extra
			}
		}
		result[i] = v
	}
	res := newViewList(result)
	if res.Rev == curList.Rev {
		return false, res, nil
	}

	// Write them: an entry and the lists above it are made when there are none.
	if len(result) == 0 {
		if entry == nil || cont == nil {
			return false, res, nil
		}
		// Every writing of the entry loses its views, or the last one that is
		// left would be the entry and bring its views back.
		// One pass, whatever the number of entries: what stays is collected,
		// not what goes cut out one at a time.
		file := spaceSettings
		if section != "folders" {
			file = tagsSettings
		}
		st := stripper{file: file, top: top, keys: []string{"views"}}
		kept := make([]*yaml.Node, 0, len(cont.Content))
		for i := 0; i+1 < len(cont.Content); i += 2 {
			if isEntry(i) {
				if st.has(cont.Content[i+1]) {
					if err := st.strip(cont, i+1); err != nil {
						return false, ViewList{}, err
					}
				}
				if emptied(cont.Content[i+1]) {
					continue
				}
			}
			kept = append(kept, cont.Content[i], cont.Content[i+1])
		}
		cont.Content = kept
		if section == "folders" {
			dropEmpty(top, cont, -1)
		} else {
			dropEmptyTag(top, cont, -1)
		}
		return true, res, nil
	}
	if cont == nil {
		if cont, err = section2(top, true); err != nil {
			return false, ViewList{}, err
		}
	}
	if entry == nil {
		entry = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if at >= 0 {
			if oddEntry {
				noteReplaced(top, key)
			}
			cont.Content[at+1] = entry
		} else {
			cont.Content = append(cont.Content, textNode(key), entry)
			at = len(cont.Content) - 2
		}
	}
	// From here the entry, its list and every view that changes are the
	// writer's own: a copy where they were an alias, carried an anchor or had
	// keys from a merge key. A copy has the views at the same positions.
	entry = own(cont, at+1)
	var seq *yaml.Node
	if cur, vat := mapValue(entry, "views"); cur != nil && cur.Kind == yaml.SequenceNode {
		seq = own(entry, vat+1)
	} else {
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		if unusable(cur, yaml.SequenceNode) {
			noteReplaced(top, key+": views")
		}
		if vat >= 0 {
			entry.Content[vat+1] = seq
		} else {
			entry.Content = append(entry.Content, textNode("views"), seq)
		}
		slots = nil
	}
	// What is in the list that is not a view (written by hand, or beyond the
	// views the server reads) stays, after the views.
	isView := make(map[int]bool, len(slots))
	for _, j := range slots {
		isView[j] = true
	}
	var rest []*yaml.Node
	for j, c := range seq.Content {
		if !isView[j] {
			rest = append(rest, c)
		}
	}
	nodes := make([]*yaml.Node, 0, len(result)+len(rest))
	for i, v := range result {
		var n *yaml.Node
		switch {
		case matched[i] < 0:
			n = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			applyView(n, v)
		case unchanged[i]:
			n = seq.Content[slots[matched[i]]] // as it is written, an alias too
		default:
			n = owned(seq.Content[slots[matched[i]]])
			applyView(n, v)
		}
		nodes = append(nodes, n)
	}
	seq.Content = append(nodes, rest...)
	return true, res, nil
}

// dropEmptyTag is dropEmpty for the tags mapping.
func dropEmptyTag(root, tags *yaml.Node, i int) {
	if i >= 0 {
		if e := tags.Content[i+1]; e.Kind == yaml.MappingNode && len(e.Content) == 0 {
			tags.Content = append(tags.Content[:i:i], tags.Content[i+2:]...)
		}
	}
	if len(tags.Content) == 0 {
		if _, at := mapValue(root, "tags"); at >= 0 {
			root.Content = append(root.Content[:at:at], root.Content[at+2:]...)
		}
	}
}

// setKey sets a key of a mapping that is the writer's own (see own) to a node:
// the last writing of the key is changed, a missing key added at the end. A
// scalar that already means the same text is left alone (its comments and
// quoting stay, and an alias stays an alias). The value that was there leaves
// the tree as it is: it is not written over, so an anchor on it stays good
// for the aliases that remain.
func setKey(m *yaml.Node, key string, val *yaml.Node) {
	cur, at := mapValue(m, key)
	switch {
	case at < 0:
		m.Content = append(m.Content, textNode(key), val)
	case cur != nil && cur.Kind == yaml.ScalarNode && val.Kind == yaml.ScalarNode && cur.Tag == val.Tag && cur.Value == val.Value:
	default:
		was := m.Content[at+1]
		val.HeadComment, val.LineComment, val.FootComment = was.HeadComment, was.LineComment, was.FootComment
		m.Content[at+1] = val
	}
}

// removeKey takes every writing of a key out of a mapping that is the
// writer's own, in one pass over it.
func removeKey(m *yaml.Node, key string) {
	var out []*yaml.Node
	for j := 0; j+1 < len(m.Content); j += 2 {
		if k, ok := keyText(m.Content[j]); ok && k == key {
			if out == nil {
				out = append(make([]*yaml.Node, 0, len(m.Content)-2), m.Content[:j]...)
			}
			continue
		}
		if out != nil {
			out = append(out, m.Content[j], m.Content[j+1])
		}
	}
	if out != nil {
		m.Content = out
	}
}

// emptied reports whether the value of an entry is a mapping with nothing
// left in it, as written (an alias is not: what it stands for is not ours).
func emptied(v *yaml.Node) bool {
	return v.Kind == yaml.MappingNode && len(v.Content) == 0
}

func filterNode(fs []ViewFilter) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, f := range fs {
		vals := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		for _, v := range f.Values {
			vals.Content = append(vals.Content, valueNode(v))
		}
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle,
			Content: []*yaml.Node{textNode("key"), textNode(f.Key), textNode("op"), textNode(f.Op), textNode("values"), vals}})
	}
	return seq
}

func hiddenNode(hs []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, h := range hs {
		seq.Content = append(seq.Content, textNode(h))
	}
	return seq
}

// applyView writes v into the mapping node n of a view, which is the writer's
// own (see owned): keys are set in place, and a new node gets them in the
// order id, name, layout, group, sort, filter, hidden, the rest.
func applyView(n *yaml.Node, v View) {
	setKey(n, "id", textNode(v.ID))
	setKey(n, "name", textNode(v.Name))
	setKey(n, "layout", textNode(v.Layout))
	// A key is removed or rewritten only when the request changes it from what
	// was read: what the reader could not interpret (a scalar where a list
	// belongs) reads as empty, and must not be taken for a request to remove
	// it; and what is written as an alias and still means the same stays one.
	old := viewOf(n)
	for _, f := range []struct{ key, val, was string }{{"group", v.Group, old.Group}, {"sort", v.Sort, old.Sort}} {
		switch {
		case f.val == f.was:
		case f.val == "":
			removeKey(n, f.key)
		default:
			setKey(n, f.key, textNode(f.val))
		}
	}
	if !sameValue(filterPlain(old.Filter), filterPlain(v.Filter)) {
		if len(v.Filter) == 0 {
			removeKey(n, "filter")
		} else {
			setKey(n, "filter", filterNode(v.Filter))
		}
	}
	if !reflect.DeepEqual(old.Hidden, v.Hidden) && !(len(old.Hidden) == 0 && len(v.Hidden) == 0) {
		if len(v.Hidden) == 0 {
			removeKey(n, "hidden")
		} else {
			setKey(n, "hidden", hiddenNode(v.Hidden))
		}
	}
	keys := make([]string, 0, len(v.Extra))
	for k := range v.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if cur, _ := mapValue(n, k); cur != nil && sameValue(nodeValue(cur), v.Extra[k]) {
			continue
		}
		setKey(n, k, valueNode(v.Extra[k]))
	}
}

func filterPlain(fs []ViewFilter) any {
	out := make([]any, len(fs))
	for i, f := range fs {
		vals := f.Values
		if vals == nil {
			vals = []any{}
		}
		out[i] = map[string]any{"key": f.Key, "op": f.Op, "values": vals}
	}
	return out
}
