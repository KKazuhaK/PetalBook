package space

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
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
	maxViewBytes      = 16 << 10
	maxViewsBytes     = 128 << 10
	maxViewExtraKeys  = 16
	maxViewExtraBytes = 4 << 10

	// maxNodeBudget is the most YAML nodes read for the values of one view,
	// aliases followed: a file written with nested aliases can stand for far
	// more than its size.
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
		return invalidf("a view must be an object")
	}
	*v = View{}
	text := func(key string, into *string) error {
		r, ok := raw[key]
		if !ok || string(r) == "null" {
			return nil
		}
		if err := json.Unmarshal(r, into); err != nil {
			return invalidf("the %s of a view must be text", key)
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
			return invalidf("the filter of a view must be a list of {\"key\", \"op\", \"values\"}")
		}
		for i := range v.Filter {
			for j := range v.Filter[i].Values {
				v.Filter[i].Values[j] = plain(v.Filter[i].Values[j])
			}
		}
	}
	if r, ok := raw["hidden"]; ok && string(r) != "null" {
		if err := json.Unmarshal(r, &v.Hidden); err != nil {
			return invalidf("the hidden columns of a view must be a list of keys")
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
			return invalidf("the key %q of a view is not valid JSON", k)
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
	w := nodeWalk{left: maxNodeBudget}
	v := w.value(n, 0)
	if w.over {
		return nil
	}
	return v
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
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k := n.Content[i]; k.Kind == yaml.ScalarNode {
				out[k.Value] = w.value(n.Content[i+1], depth+1)
				if w.over {
					return nil
				}
			}
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

// viewOf reads a view from its mapping node.
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
			if c.Kind != yaml.MappingNode {
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
			if t, ok := scalarText(c); ok {
				v.Hidden = append(v.Hidden, t)
			}
		}
	}
	last := map[string]int{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := n.Content[i]; k.Kind == yaml.ScalarNode {
			last[k.Value] = i
		}
	}
	for k, i := range last {
		if viewKnown[k] {
			continue
		}
		val := w.value(n.Content[i+1], 0)
		if w.over {
			break // what is too large to read is left out (and left in the file)
		}
		if v.Extra == nil {
			v.Extra = map[string]any{}
		}
		v.Extra[k] = val
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

// viewNodes are the mapping nodes of an entry's `views`, the views the server
// reads. Anything else in the list (written by hand) is not a view.
func viewNodes(entry *yaml.Node) []*yaml.Node {
	seq, _ := mapValue(entry, "views")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	var out []*yaml.Node
	for _, c := range seq.Content {
		if c.Kind == yaml.MappingNode {
			out = append(out, c)
		}
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
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		key := norm(k.Value)
		delete(out, key)
		if vs := viewsIn(m.Content[i+1]); len(vs) > 0 {
			out[key] = vs
		}
	}
	return out
}

// tagCache is the views of tags.yml for as long as the file looks the same.
type tagCache struct {
	valid   bool
	size    int64
	mtimeNs int64
	views   map[string][]View
}

func (s *Space) tagViews() map[string][]View {
	abs := filepath.Join(s.root, filepath.FromSlash(tagsFile))
	fi, err := os.Lstat(abs)
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if err != nil || !fi.Mode().IsRegular() {
		s.tagCfg = tagCache{}
		return nil
	}
	if c := s.tagCfg; c.valid && c.size == fi.Size() && c.mtimeNs == fi.ModTime().UnixNano() {
		return c.views
	}
	c := tagCache{valid: true, size: fi.Size(), mtimeNs: fi.ModTime().UnixNano()}
	if doc, err := s.loadTags(); err == nil {
		c.views = viewsOf(doc, "tags", func(k string) string { return k })
	}
	s.tagCfg = c
	return c.views
}

// Views returns the views of a folder or a tag, in order, the first being the
// default. A file that cannot be read has none to show. The folder is not
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
		return invalidf("give a folder or a tag, not both")
	case scope.Folder != nil:
		return checkRel(*scope.Folder)
	case scope.Tag != "":
		return checkTagName(scope.Tag)
	}
	return invalidf("give the folder or the tag the views belong to")
}

// --- checking views ----------------------------------------------------------------

func checkViewText(what, s string, max int) error {
	if !utf8.ValidString(s) || len(s) > max || strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return invalidf("%s is not valid: text of at most %d bytes on one line", what, max)
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
		return invalidf("a folder or a tag has at most %d views", MaxViews)
	}
	ids := map[string]bool{}
	total := 0
	for i, v := range vs {
		at := "view " + strconv.Itoa(i+1)
		old, isStored := stored[v.ID]
		if v.ID != "" {
			if !validViewID(v.ID) && !isStored {
				return invalidf("%s: that is not a view id", at)
			}
			if ids[v.ID] {
				return invalidf("%s: the id %q is used twice", at, v.ID)
			}
			ids[v.ID] = true
		}
		if isStored && sameView(old, v) {
			continue
		}
		if strings.TrimSpace(v.Name) == "" {
			return invalidf("%s needs a name", at)
		}
		if err := checkViewText(at+": the name", v.Name, maxViewName); err != nil {
			return err
		}
		if !validLayout(v.Layout) {
			return invalidf("%s: the layout must be one of %s", at, strings.Join(ViewLayouts, ", "))
		}
		if err := checkViewText(at+": the sort", v.Sort, maxViewKey+8); err != nil {
			return err
		}
		if err := index.ValidSort(v.Sort); err != nil {
			return invalidf("%s: %s", at, strings.TrimPrefix(err.Error(), index.ErrInvalidQuery.Error()+": "))
		}
		if err := checkViewText(at+": the group", v.Group, maxViewKey); err != nil {
			return err
		}
		fs := make([]index.Filter, len(v.Filter))
		for j, f := range v.Filter {
			fs[j] = index.Filter{Key: f.Key, Op: f.Op, Values: f.Values}
		}
		if err := index.CheckFilters(fs); err != nil {
			return invalidf("%s: %s", at, strings.TrimPrefix(err.Error(), index.ErrInvalidQuery.Error()+": "))
		}
		if len(v.Hidden) > maxViewHidden {
			return invalidf("%s: at most %d hidden columns", at, maxViewHidden)
		}
		seen := map[string]bool{}
		for _, h := range v.Hidden {
			if h == "" || seen[h] {
				return invalidf("%s: the hidden columns must be keys, each once", at)
			}
			seen[h] = true
			if err := checkViewText(at+": a hidden column", h, maxViewKey); err != nil {
				return err
			}
		}
		if len(v.Extra) > maxViewExtraKeys {
			return invalidf("%s: at most %d keys the server does not know", at, maxViewExtraKeys)
		}
		for k, e := range v.Extra {
			if k == "" || len(k) > maxViewExtra || !utf8.ValidString(k) {
				return invalidf("%s: a key of a view is 1 to %d bytes", at, maxViewExtra)
			}
			if b, err := json.Marshal(e); err != nil || len(b) > maxViewExtraBytes {
				return invalidf("%s: the value of %q is larger than %d bytes", at, k, maxViewExtraBytes)
			}
		}
		b, err := json.Marshal(v)
		if err != nil || len(b) > maxViewBytes {
			return invalidf("%s is larger than %d bytes", at, maxViewBytes)
		}
		if total += len(b); total > maxViewsBytes {
			return invalidf("the views changed are larger than %d bytes together", maxViewsBytes)
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
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i]; k.Kind != yaml.ScalarNode || skip(k.Value) {
			continue
		}
		for _, n := range viewNodes(m.Content[i+1]) {
			if id, ok := scalarText(first(mapValue(n, "id"))); ok {
				into[id] = true
			}
		}
	}
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
// there is ErrNoFolder; settings that cannot be read are not replaced.
func (s *Space) PutViews(by Author, scope ViewScope, baseRev string, views []View) (ViewList, error) {
	if err := checkScope(scope); err != nil {
		return ViewList{}, err
	}
	if len(views) > MaxViews {
		return ViewList{}, invalidf("a folder or a tag has at most %d views", MaxViews)
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
		if sdoc, err := s.loadYAMLFile(spaceFile, maxSpaceFile, errSpaceFile); err == nil {
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
	var entry *yaml.Node
	at := -1
	if cont != nil {
		for i := 0; i+1 < len(cont.Content); i += 2 {
			if k := cont.Content[i]; k.Kind == yaml.ScalarNode && norm(k.Value) == key {
				at = i
			}
		}
		if at >= 0 {
			entry = cont.Content[at+1]
		}
	}
	if entry != nil && entry.Kind != yaml.MappingNode {
		if !(entry.Kind == yaml.ScalarNode && (entry.Tag == "!!null" || entry.Value == "")) {
			return false, ViewList{}, invalidf("the entry of %q in the settings is not a list of settings; it was left as it is and has to be fixed by hand", key)
		}
		entry = nil
	}
	var curNodes []*yaml.Node
	if entry != nil {
		curNodes = viewNodes(entry)
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
	matched := make([]*yaml.Node, len(views))
	for i, v := range views {
		if v.ID == "" {
			id, err := newViewID(taken)
			if err != nil {
				return false, ViewList{}, err
			}
			v.ID = id
		} else {
			j, ok := byID[v.ID]
			if !ok {
				return false, ViewList{}, invalidf("view %d: there is no view with the id %q here; leave the id out to make a new view", i+1, v.ID)
			}
			matched[i] = curNodes[j]
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
		for i := len(cont.Content) - 2; i >= 0; i -= 2 {
			k := cont.Content[i]
			if k.Kind != yaml.ScalarNode || norm(k.Value) != key {
				continue
			}
			if e := cont.Content[i+1]; e.Kind == yaml.MappingNode {
				removeViewNodes(e)
			}
			if section == "folders" {
				dropEmpty(top, cont, i)
			} else {
				dropEmptyTag(top, cont, i)
			}
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
			cont.Content[at+1] = entry
		} else {
			cont.Content = append(cont.Content, textNode(key), entry)
		}
	}
	seq, _ := mapValue(entry, "views")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		if v, vat := mapValue(entry, "views"); v != nil {
			entry.Content[vat+1] = seq
		} else {
			entry.Content = append(entry.Content, textNode("views"), seq)
		}
	}
	// What is in the list that is not a view (written by hand) stays, after the views.
	var rest []*yaml.Node
	for _, c := range seq.Content {
		if c.Kind != yaml.MappingNode {
			rest = append(rest, c)
		}
	}
	nodes := make([]*yaml.Node, 0, len(result)+len(rest))
	for i, v := range result {
		n := matched[i]
		if n == nil {
			n = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		}
		applyView(n, v)
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

// removeViewNodes takes the views off an entry, all writings of the key.
func removeViewNodes(entry *yaml.Node) {
	for j := len(entry.Content) - 2; j >= 0; j -= 2 {
		if k := entry.Content[j]; k.Kind == yaml.ScalarNode && k.Value == "views" {
			entry.Content = append(entry.Content[:j:j], entry.Content[j+2:]...)
		}
	}
}

// setKey sets a key of a mapping to a node: the last writing of the key is
// changed, a missing key added at the end. A scalar that is already the same
// text is left alone (its comments and quoting stay).
func setKey(m *yaml.Node, key string, val *yaml.Node) {
	cur, at := mapValue(m, key)
	switch {
	case cur == nil:
		m.Content = append(m.Content, textNode(key), val)
	case cur.Kind == yaml.ScalarNode && val.Kind == yaml.ScalarNode && cur.Tag == val.Tag && cur.Value == val.Value:
	default:
		val.HeadComment, val.LineComment, val.FootComment = cur.HeadComment, cur.LineComment, cur.FootComment
		m.Content[at+1] = val
	}
}

func removeKey(m *yaml.Node, key string) {
	for j := len(m.Content) - 2; j >= 0; j -= 2 {
		if k := m.Content[j]; k.Kind == yaml.ScalarNode && k.Value == key {
			m.Content = append(m.Content[:j:j], m.Content[j+2:]...)
		}
	}
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

// applyView writes v into the mapping node n of a view: keys are set in
// place, and a new node gets them in the order id, name, layout, group, sort,
// filter, hidden, the rest.
func applyView(n *yaml.Node, v View) {
	setKey(n, "id", textNode(v.ID))
	setKey(n, "name", textNode(v.Name))
	setKey(n, "layout", textNode(v.Layout))
	for _, f := range []struct{ key, val string }{{"group", v.Group}, {"sort", v.Sort}} {
		if f.val == "" {
			removeKey(n, f.key)
		} else {
			setKey(n, f.key, textNode(f.val))
		}
	}
	old := viewOf(n)
	if len(v.Filter) == 0 {
		removeKey(n, "filter")
	} else if !sameValue(filterPlain(old.Filter), filterPlain(v.Filter)) {
		setKey(n, "filter", filterNode(v.Filter))
	}
	if len(v.Hidden) == 0 {
		removeKey(n, "hidden")
	} else if !reflect.DeepEqual(old.Hidden, v.Hidden) {
		setKey(n, "hidden", hiddenNode(v.Hidden))
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
