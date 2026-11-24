package space

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/langtag"
)

// The daily text of a space (milestone 2.3): one page a day, picked from the
// pages that meet a condition the space settings name.
//
//	daily:
//	  folder: 文案            # or tag: …, or type: …; one of the three
//	  filter: [{key: status, op: in, values: [定稿]}]   # optional, as a listing's filter
//
// The settings live in space.yml like the bindings (see config.go), so they
// are read and written the same way: a file that cannot be used is restored
// from its history, anchors and aliases are kept, and a change is journaled
// and committed like any other.

const (
	// DailyBodyLimit is how many characters of a page the daily text carries.
	DailyBodyLimit = 2000
	// maxDailySkip is the largest skip a request may carry.
	maxDailySkip = 1000
	// dailyMemoMax is how many picks the space remembers of each kind: those
	// everybody sees (skip 0) and the others.
	dailyMemoMax = 512
)

// DailySource says where the daily text is picked from: exactly one of Folder
// (the folder and the folders below it), Tag and Type (the effective type),
// and the filters the pages must also meet.
type DailySource struct {
	Folder *string
	Tag    string
	Type   string
	Filter []ViewFilter
}

// MarshalJSON writes the keys that are set, and the filter always.
func (d DailySource) MarshalJSON() ([]byte, error) {
	out := map[string]any{"filter": d.filters()}
	switch {
	case d.Folder != nil:
		out["folder"] = *d.Folder
	case d.Tag != "":
		out["tag"] = d.Tag
	case d.Type != "":
		out["type"] = d.Type
	}
	return json.Marshal(out)
}

// UnmarshalJSON reads a source; keys it does not know are refused.
func (d *DailySource) UnmarshalJSON(data []byte) error {
	var in struct {
		Folder *string      `json:"folder"`
		Tag    *string      `json:"tag"`
		Type   *string      `json:"type"`
		Filter []ViewFilter `json:"filter"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(&in); err != nil {
		return err
	}
	*d = DailySource{Folder: in.Folder, Filter: in.Filter}
	set := 0
	if in.Folder != nil {
		set++
	}
	if in.Tag != nil {
		set++
		d.Tag = *in.Tag
		if d.Tag == "" {
			return errors.New("tag is empty")
		}
	}
	if in.Type != nil {
		set++
		d.Type = *in.Type
		if d.Type == "" {
			return errors.New("type is empty")
		}
	}
	if set != 1 {
		return errors.New("give exactly one of folder, tag and type")
	}
	return nil
}

// DailyConfig is the daily source of a space: one for every language, one per
// language (`daily: {zh: {folder: 文案}, en: {tag: quotes}}`), or both (the one
// for every language is the fallback for the languages that have none).
type DailyConfig struct {
	// All is a source written without languages: it applies to every language
	// that has none of its own.
	All *DailySource
	// ByLang are the sources by language code, in lower case.
	ByLang map[string]DailySource
}

// maxDailyLangs is the most languages a space may give a source of its own.
const maxDailyLangs = 20

// ValidLang reports whether s can be a language: a well-formed BCP 47 tag
// (`zh-Hans`, `en`, `pt-BR`, in any case) whose language is in the IANA registry.
func ValidLang(s string) bool {
	_, ok := langtag.Canonical(s)
	return ok
}

// NormalLang is a language tag in canonical form (`ZH-cn` is `zh-CN`, `zh`
// is `zh-Hans`); text that is no tag is left in lower case.
func NormalLang(s string) string { return langtag.Read(s) }

// For is the source for a language: that of the language the reader's is
// matched to the BCP 47 way (`zh-Hans` for `zh-CN`, `en` for `en-GB`); else the
// one for every language ("all languages", the fallback entry); else, so that
// a language nobody wrote a source for still gets a text, the first of the
// sources by the order of their codes.
func (c *DailyConfig) For(lang string) *DailySource {
	if c == nil {
		return nil
	}
	keys := make([]string, 0, len(c.ByLang))
	for k := range c.ByLang {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if m, ok := langtag.Match(lang, keys); ok {
		if d, ok := c.ByLang[m]; ok {
			return &d
		}
	}
	// The source for all the other languages, if there is one; never the source of
	// another language: the text is in the reader's language or there is none.
	return c.All
}

// MarshalJSON writes the one source, or the sources by language.
func (c DailyConfig) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	for k, v := range c.ByLang {
		out[k] = v
	}
	if c.All != nil {
		out["filter"] = c.All.filters()
		switch {
		case c.All.Folder != nil:
			out["folder"] = *c.All.Folder
		case c.All.Tag != "":
			out["tag"] = c.All.Tag
		default:
			out["type"] = c.All.Type
		}
	}
	return json.Marshal(out)
}

// UnmarshalJSON reads the one source, the sources by language, or both.
func (c *DailyConfig) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if len(m) == 0 {
		return errors.New("give a source: folder, tag or type, or one for each language")
	}
	out := DailyConfig{}
	whole := map[string]json.RawMessage{}
	for k, raw := range m {
		if k == "folder" || k == "tag" || k == "type" || k == "filter" {
			whole[k] = raw
		}
	}
	if len(whole) > 0 {
		b, _ := json.Marshal(whole)
		var d DailySource
		if err := json.Unmarshal(b, &d); err != nil {
			return err
		}
		out.All = &d
	}
	if len(m)-len(whole) > maxDailyLangs {
		return fmt.Errorf("at most %d languages", maxDailyLangs)
	}
	for k, raw := range m {
		if _, ok := whole[k]; ok {
			continue
		}
		if !ValidLang(k) {
			return BadLanguage(k)
		}
		var d DailySource
		if err := json.Unmarshal(raw, &d); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		if out.ByLang == nil {
			out.ByLang = map[string]DailySource{}
		}
		if _, dup := out.ByLang[NormalLang(k)]; dup {
			return fmt.Errorf("%q is given twice", k)
		}
		out.ByLang[NormalLang(k)] = d
	}
	*c = out
	return nil
}

// key identifies the configuration, for what is remembered of a pick.
func (c DailyConfig) key() string {
	b, _ := json.Marshal(c)
	return string(b)
}

func (d DailySource) filters() []ViewFilter {
	if d.Filter == nil {
		return []ViewFilter{}
	}
	return d.Filter
}

// key identifies the source, so what is remembered of a pick is dropped when
// the settings change.
func (d DailySource) key() string {
	b, _ := json.Marshal(d)
	return string(b)
}

// dailyOf reads the daily configuration of a settings document: nil when
// there is none or what is there names no source.
func dailyOf(doc *yaml.Node) *DailyConfig {
	if doc == nil || len(doc.Content) == 0 {
		return nil
	}
	return dailyIn(doc.Content[0])
}

func isSourceKey(k string) bool { return k == "folder" || k == "tag" || k == "type" }

func hasSourceKey(m *yaml.Node) bool {
	for _, k := range []string{"folder", "tag", "type"} {
		if t, ok := scalarText(first(mapValue(m, k))); ok && (k == "folder" || strings.TrimSpace(t) != "") {
			return true
		}
	}
	return false
}

func dailyIn(top *yaml.Node) *DailyConfig {
	v, _ := mapValue(top, "daily")
	if v == nil || v.Kind != yaml.MappingNode {
		return nil
	}
	var out DailyConfig
	if hasSourceKey(v) {
		out.All = sourceIn(v)
	}
	eachPair(v, func(k string, val *yaml.Node, _ int) {
		if isSourceKey(k) || k == "filter" || !ValidLang(k) || val == nil || val.Kind != yaml.MappingNode || len(out.ByLang) >= maxDailyLangs {
			return
		}
		if d := sourceIn(val); d != nil {
			if out.ByLang == nil {
				out.ByLang = map[string]DailySource{}
			}
			// `zh` and `zh-Hans` are the same language: the one written in
			// canonical form wins, else the first.
			if _, dup := out.ByLang[NormalLang(k)]; dup && NormalLang(k) != k {
				return
			}
			out.ByLang[NormalLang(k)] = *d
		}
	})
	if out.All == nil && out.ByLang == nil {
		return nil
	}
	return &out
}

// sourceIn reads one source from a mapping. A mapping that names more than
// one reads as the first of folder, tag, type.
func sourceIn(v *yaml.Node) *DailySource {
	var d DailySource
	if t, ok := scalarText(first(mapValue(v, "folder"))); ok {
		t = bindingKey(t)
		d.Folder = &t
	} else if t, ok := scalarText(first(mapValue(v, "tag"))); ok && strings.TrimSpace(t) != "" {
		d.Tag = t
	} else if t, ok := scalarText(first(mapValue(v, "type"))); ok && strings.TrimSpace(t) != "" {
		d.Type = strings.TrimSpace(t)
	} else {
		return nil
	}
	w := &nodeWalk{left: maxNodeBudget}
	if f, _ := mapValue(v, "filter"); f != nil && f.Kind == yaml.SequenceNode {
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
			d.Filter = append(d.Filter, vf)
		}
	}
	return &d
}

// Daily returns the daily configuration, nil when the space has none.
func (s *Space) Daily() *DailyConfig {
	d := s.config().daily
	if d == nil {
		return nil
	}
	c := DailyConfig{}
	if d.All != nil {
		a := *d.All
		c.All = &a
	}
	if d.ByLang != nil {
		c.ByLang = make(map[string]DailySource, len(d.ByLang))
		for k, v := range d.ByLang {
			c.ByLang[k] = v
		}
	}
	return &c
}

// CheckDailySource validates a source a request gives, and returns it with the
// folder as it is spelled on disk.
func (s *Space) CheckDailySource(d DailySource) (DailySource, error) {
	n := 0
	if d.Folder != nil {
		n++
	}
	if d.Tag != "" {
		n++
	}
	if d.Type != "" {
		n++
	}
	if n != 1 {
		return d, invalidf("give exactly one of folder, tag and type")
	}
	switch {
	case d.Folder != nil:
		if err := checkRel(*d.Folder); err != nil {
			return d, err
		}
		canon, existed, err := s.resolveFolder(*d.Folder)
		if errors.Is(err, ErrExists) || (err == nil && !existed) {
			return d, ErrNoFolder
		}
		if err != nil {
			return d, err
		}
		d.Folder = &canon
	case d.Tag != "":
		if err := checkTagName(d.Tag); err != nil {
			return d, err
		}
	default:
		if _, ok := s.typeInfoCached(d.Type); !ok {
			return d, invalidf("there is no type %q", d.Type)
		}
	}
	if len(d.Filter) > index.MaxViewFilters {
		return d, invalidf("at most %d filters", index.MaxViewFilters)
	}
	fs := make([]index.Filter, len(d.Filter))
	for i, f := range d.Filter {
		fs[i] = index.Filter{Key: f.Key, Op: f.Op, Values: f.Values}
	}
	if err := index.CheckFilters(fs); err != nil {
		return d, invalidf("%s", strings.TrimPrefix(err.Error(), index.ErrInvalidQuery.Error()+": "))
	}
	for i := range d.Filter {
		if d.Filter[i].Values == nil {
			d.Filter[i].Values = []any{}
		}
	}
	return d, nil
}

func (s *Space) checkDaily(c DailyConfig) (DailyConfig, error) {
	var out DailyConfig
	if c.All == nil && len(c.ByLang) == 0 {
		return c, invalidf("give a source: folder, tag or type, or one for each language")
	}
	if c.All != nil {
		d, err := s.CheckDailySource(*c.All)
		if err != nil {
			return c, err
		}
		out.All = &d
	}
	if len(c.ByLang) > maxDailyLangs {
		return c, invalidf("at most %d languages", maxDailyLangs)
	}
	for lang, d := range c.ByLang {
		if !ValidLang(lang) {
			return c, BadLanguage(lang)
		}
		checked, err := s.CheckDailySource(d)
		if err != nil {
			return c, err
		}
		if out.ByLang == nil {
			out.ByLang = map[string]DailySource{}
		}
		out.ByLang[NormalLang(lang)] = checked
	}
	return out, nil
}

// SetDaily replaces the daily configuration; nil removes it. It returns the
// configuration as it is now. The change is one commit, like a change of views.
func (s *Space) SetDaily(by Author, d *DailyConfig) (*DailyConfig, error) {
	var want *DailyConfig
	if d != nil {
		c, err := s.checkDaily(*d)
		if err != nil {
			return nil, err
		}
		want = &c
	}
	return mutate(s, by, func(tx *txn) (*DailyConfig, error) {
		err := s.editSpaceFile(tx, func(root *yaml.Node) (bool, error) { return setDailyIn(root, want) })
		if err != nil {
			return nil, err
		}
		return s.Daily(), nil
	})
}

// writeSource writes a source into the mapping n, the writer's own. The key
// that names the source stays where it is, with its comments, and is renamed
// when the source changes from one kind to another.
func writeSource(n *yaml.Node, d DailySource) {
	want, text := "type", d.Type
	switch {
	case d.Folder != nil:
		want, text = "folder", *d.Folder
	case d.Tag != "":
		want, text = "tag", d.Tag
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k, ok := keyText(n.Content[i]); ok && isSourceKey(k) {
			if k != want {
				nk := textNode(want)
				nk.HeadComment, nk.LineComment, nk.FootComment = n.Content[i].HeadComment, n.Content[i].LineComment, n.Content[i].FootComment
				n.Content[i] = nk
			}
			break
		}
	}
	for _, k := range []string{"folder", "tag", "type"} {
		if k != want {
			removeKey(n, k)
		}
	}
	setKey(n, want, textNode(text))
	if len(d.Filter) > 0 {
		setKey(n, "filter", filterNode(d.Filter))
	} else {
		removeKey(n, "filter")
	}
}

// setDailyIn writes the daily configuration into the settings top, or removes
// it. What else the entry holds (comments, keys the server does not know)
// stays.
func setDailyIn(top *yaml.Node, d *DailyConfig) (bool, error) {
	cur, at := mapValue(top, "daily")
	if d == nil {
		if at < 0 && cur == nil {
			return false, nil
		}
		before := len(top.Content)
		removeKey(top, "daily")
		return len(top.Content) != before, nil
	}
	fresh := func() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"} }
	var n *yaml.Node
	switch {
	case at < 0:
		n = fresh()
		top.Content = append(top.Content, textNode("daily"), n)
	case cur.Kind == yaml.MappingNode:
		if old := dailyIn(top); old != nil && old.key() == d.key() {
			return false, nil
		}
		n = own(top, at+1)
	default:
		if !(cur.Kind == yaml.ScalarNode && (cur.Tag == "!!null" || cur.Value == "")) {
			noteReplaced(top, "daily")
		}
		n = fresh()
		top.Content[at+1] = n
	}
	if d.All != nil {
		writeSource(n, *d.All)
	} else {
		for _, k := range []string{"folder", "tag", "type", "filter"} {
			removeKey(n, k)
		}
	}
	have := map[string]bool{}
	for _, k := range langKeys(n) {
		if _, keep := d.ByLang[NormalLang(k)]; !keep {
			removeKey(n, k)
		}
	}
	langs := make([]string, 0, len(d.ByLang))
	for l := range d.ByLang {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	for _, l := range langs {
		// Two spellings of the language (`zh` beside `zh-Hans`) become one key:
		// the entry under the canonical key, else the first, stays (as it is
		// read, see dailyIn); the others go.
		keep, canon := -1, false
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k, ok := keyText(n.Content[i]); ok && ValidLang(k) && NormalLang(k) == l && (keep < 0 || k == l && !canon) {
				keep, canon = i, k == l
			}
		}
		if keep >= 0 {
			kept := n.Content[keep]
			out := n.Content[:0:0]
			for i := 0; i+1 < len(n.Content); i += 2 {
				if k, ok := keyText(n.Content[i]); ok && ValidLang(k) && NormalLang(k) == l && n.Content[i] != kept {
					continue
				}
				out = append(out, n.Content[i], n.Content[i+1])
			}
			if len(out) != len(n.Content) {
				n.Content = out
			}
		}
		found := false
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k, ok := keyText(n.Content[i]); ok && ValidLang(k) && NormalLang(k) == l {
				sub := own(n, i+1)
				if sub.Kind != yaml.MappingNode {
					noteReplaced(top, "daily: "+k)
					sub = fresh()
					n.Content[i+1] = sub
				}
				writeSource(sub, d.ByLang[l])
				if kn := n.Content[i]; kn.Kind == yaml.ScalarNode && kn.Value != l {
					kn.Value = l // a key saved before the rule is written back as the tag
				}
				found = true
			}
		}
		if !found && !have[l] {
			sub := fresh()
			writeSource(sub, d.ByLang[l])
			n.Content = append(n.Content, textNode(l), sub)
		}
	}
	return true, nil
}

// langKeys are the keys of a daily mapping that are sources by language.
func langKeys(n *yaml.Node) []string {
	var out []string
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, ok := keyText(n.Content[i])
		if ok && ValidLang(k) && !seen[k] {
			if v := deref(n.Content[i+1]); v != nil && v.Kind == yaml.MappingNode {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

// movedFolder is where a folder named by a source goes when `from` moves to `to`.
func movedFolder(src *yaml.Node, from, to string) (string, bool) {
	f, _ := mapValue(src, "folder")
	t, ok := scalarText(f)
	if !ok {
		return "", false
	}
	key := bindingKey(t)
	switch {
	case key == from:
		return to, true
	case strings.HasPrefix(key, from+"/"):
		return to + key[len(from):], true
	}
	return "", false
}

// rebindDaily makes the daily sources follow a folder that was moved from
// `from` to `to`: those that name the folder or one below it.
func rebindDaily(top *yaml.Node, from, to string) (bool, error) {
	cur, at := mapValue(top, "daily")
	if at < 0 || cur == nil || cur.Kind != yaml.MappingNode {
		return false, nil
	}
	anyMoves := false
	if hasSourceKey(cur) {
		_, anyMoves = movedFolder(cur, from, to)
	}
	eachPair(cur, func(k string, v *yaml.Node, _ int) {
		if !isSourceKey(k) && ValidLang(k) && v != nil && v.Kind == yaml.MappingNode {
			if _, ok := movedFolder(v, from, to); ok {
				anyMoves = true
			}
		}
	})
	if !anyMoves {
		return false, nil
	}
	n := own(top, at+1)
	if hasSourceKey(n) {
		if nk, ok := movedFolder(n, from, to); ok {
			setKey(n, "folder", textNode(nk))
		}
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k, ok := keyText(n.Content[i]); ok && !isSourceKey(k) && ValidLang(k) {
			if v := deref(n.Content[i+1]); v != nil && v.Kind == yaml.MappingNode {
				if nk, ok := movedFolder(v, from, to); ok {
					setKey(own(n, i+1), "folder", textNode(nk))
				}
			}
		}
	}
	return true, nil
}

// --- the pick --------------------------------------------------------------------------

// DailyPick is the daily text of a day: the page, its body cut to
// DailyBodyLimit characters, and how many pages the source holds.
type DailyPick struct {
	Page index.Summary
	Body string
	// Truncated says the body was cut.
	Truncated bool
	// Count is how many pages meet the source.
	Count int
	// Author is the page's `author` (or `作者`) property, when it has one.
	Author string
}

// DailyNone says why there is no daily text.
type DailyNone int

const (
	// DailyNoSource: the space has no source set.
	DailyNoSource DailyNone = iota + 1
	// DailyNoMatch: the source is set and no page meets it.
	DailyNoMatch
)

type dailyKey struct {
	space  string
	source string
	date   string
	skip   int
}

// dailyMemo is what a space remembers of its picks, so that pages added or
// removed during a day do not change the page everybody was shown. The picks
// everybody sees (skip 0) and the "another one"s (skip above 0) are kept
// apart, so that asking for many others never pushes out the shared one; when
// either is full, the half of it farthest from today goes (see evictFar).
type dailyMemo struct {
	shared map[dailyKey]dailyEntry
	other  map[dailyKey]dailyEntry
	// today is the day by which "far" is measured (tests set it).
	today func() time.Time
}

func (m *dailyMemo) of(k dailyKey) *map[dailyKey]dailyEntry {
	if k.skip == 0 {
		return &m.shared
	}
	return &m.other
}

func (m *dailyMemo) get(k dailyKey) (dailyEntry, bool) {
	e, ok := (*m.of(k))[k]
	return e, ok
}

func (m *dailyMemo) put(k dailyKey, e dailyEntry) {
	p := m.of(k)
	if *p == nil {
		*p = map[dailyKey]dailyEntry{}
	}
	if _, ok := (*p)[k]; !ok && len(*p) >= dailyMemoMax {
		today := time.Now()
		if m.today != nil {
			today = m.today()
		}
		evictFar(*p, today)
	}
	(*p)[k] = e
}

func (m *dailyMemo) forget(k dailyKey) { delete(*m.of(k), k) }

// evictFar removes the half of the picks whose days are farthest from today
// (at least one), keeping today's, yesterday's and tomorrow's while others
// are there to go. Cost: a sort of the memo, once every dailyMemoMax/2 picks.
func evictFar(picks map[dailyKey]dailyEntry, today time.Time) {
	t := today.UTC().Truncate(24 * time.Hour)
	dist := func(date string) time.Duration {
		d, err := time.Parse("2006-01-02", date)
		if err != nil {
			return 1 << 62
		}
		return max(d.Sub(t), t.Sub(d))
	}
	ds := make([]time.Duration, 0, len(picks))
	for k := range picks {
		ds = append(ds, dist(k.date))
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	cut := ds[len(ds)/2]
	if cut <= 24*time.Hour && ds[len(ds)-1] > 24*time.Hour {
		cut = 24 * time.Hour // the days around today stay while others can go
	}
	removed := false
	for k := range picks {
		if dist(k.date) > cut {
			delete(picks, k)
			removed = true
		}
	}
	if !removed { // all equally near: the half that comes first goes
		n := len(picks) / 2
		for k := range picks {
			if n == 0 {
				break
			}
			delete(picks, k)
			n--
		}
	}
}

type dailyEntry struct {
	id    string
	count int
}

// ValidDailyDate reports whether date is a day like 2026-11-23, in the years
// the rest of the server works with.
func ValidDailyDate(date string) bool {
	t, err := time.Parse("2006-01-02", date)
	return err == nil && t.Year() >= 1970 && t.Year() <= 2261
}

// ValidDailySkip reports whether skip is in the range a request may use.
func ValidDailySkip(skip int) bool { return skip >= 0 && skip <= maxDailySkip }

// dailyNumber is the number a day of a space starts at: the same for everybody
// who asks about that day, and unlike the one of the day before.
func dailyNumber(spaceID, date string) uint64 { return DailyNumber(spaceID, date) }

// DailyNumber is the number a day of a space (or of anything else that is
// picked from, named by key) starts at.
func DailyNumber(spaceID, date string) uint64 {
	sum := sha256.Sum256([]byte(spaceID + "\x00" + date))
	return binary.BigEndian.Uint64(sum[:8])
}

// Daily picks the daily text of a day. date is a day (ValidDailyDate), skip
// moves along the pages that meet the source (a "next one" that only the
// asker sees). The page is the n-th, by index order, of the pages that meet
// the source, n being a number made from the space and the day plus skip,
// modulo how many there are: one counting query and one read by position, both
// on the index, and the one file of the page for its body. A pick is
// remembered for the (source, date, skip) until the page is gone.
func (s *Space) DailyText(spaceID, date string, skip int, lang string) (*DailyPick, DailyNone, error) {
	if !ValidDailyDate(date) {
		return nil, 0, invalidf("date must be a day like 2026-11-23")
	}
	if !ValidDailySkip(skip) {
		return nil, 0, invalidf("skip must be a number from 0 to %d", maxDailySkip)
	}
	src := s.config().daily.For(lang)
	if src == nil {
		return nil, DailyNoSource, nil
	}
	return s.DailyFrom(spaceID, date, skip, *src)
}

// DailyFrom is DailyText for a source the caller has: the site's default
// source may name a folder, tag or type of this space.
func (s *Space) DailyFrom(spaceID, date string, skip int, source DailySource) (*DailyPick, DailyNone, error) {
	if !ValidDailyDate(date) {
		return nil, 0, invalidf("date must be a day like 2026-11-23")
	}
	if !ValidDailySkip(skip) {
		return nil, 0, invalidf("skip must be a number from 0 to %d", maxDailySkip)
	}
	src := &source
	q := index.PickQuery{Folder: src.Folder, Recursive: src.Folder != nil, Type: src.Type}
	if src.Tag != "" {
		q.Tags = []string{src.Tag}
	}
	for _, f := range src.Filter {
		q.Filters = append(q.Filters, index.Filter{Key: f.Key, Op: f.Op, Values: f.Values})
	}
	if src.Type != "" {
		q.TypeFolders = s.foldersOfType(src.Type)
	}
	k := dailyKey{spaceID, src.key(), date, skip}

	s.dailyMu.Lock()
	memo, remembered := s.daily.get(k)
	s.dailyMu.Unlock()
	var sum index.Summary
	count := 0
	if remembered {
		// Only the summary of the remembered page is read; a page that is gone
		// from the index is picked again.
		got, ok, err := s.idx.Summary(memo.id)
		if err != nil {
			return nil, 0, err
		}
		if ok {
			sum, count = got, memo.count
		}
		remembered = ok
	}
	if !remembered {
		base := dailyNumber(spaceID, date) + uint64(skip)
		var err error
		count, sum, err = s.idx.Pick(q, func(total int) int { return int(base % uint64(total)) })
		if errors.Is(err, index.ErrNoPick) || errors.Is(err, index.ErrInvalidQuery) {
			// A filter written by hand that the index refuses picks nothing.
			return nil, DailyNoMatch, nil
		}
		if err != nil {
			return nil, 0, err
		}
		s.dailyMu.Lock()
		s.daily.put(k, dailyEntry{sum.ID, count})
		s.dailyMu.Unlock()
	}
	p, err := s.Get(sum.ID)
	if errors.Is(err, ErrNotFound) {
		// Gone since the index was read: the pick is made again.
		s.dailyMu.Lock()
		s.daily.forget(k)
		s.dailyMu.Unlock()
		if !remembered {
			return nil, DailyNoMatch, nil
		}
		return s.DailyFrom(spaceID, date, skip, source)
	}
	if err != nil {
		return nil, 0, err
	}
	body, cut := cutText(p.Body, DailyBodyLimit)
	pick := &DailyPick{Page: sum, Body: body, Truncated: cut, Count: count}
	// What the page says about who wrote it and where it is from, only if it does.
	if withProps, ok, err := s.idx.SummaryWithProps(sum.ID); err == nil && ok {
		pick.Author = propText(withProps.Props, "author", "作者")
		if pick.Page.Source == "" {
			pick.Page.Source = propText(withProps.Props, "出处")
		}
	}
	return pick, 0, nil
}

// propText is the first of the properties (a JSON object) named that is a
// non-empty text.
func propText(props json.RawMessage, names ...string) string {
	var m map[string]any
	if len(props) == 0 || json.Unmarshal(props, &m) != nil {
		return ""
	}
	for _, n := range names {
		if v, ok := m[n].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// cutText keeps the first max characters of text.
func cutText(text string, max int) (string, bool) {
	if len(text) <= max { // bytes are never fewer than characters
		return text, false
	}
	n := 0
	for i := range text {
		if n == max {
			return text[:i], true
		}
		n++
	}
	return text, false
}

// foldersOfType are the folders whose pages, those with no `type` of their
// own in the header, have the type typ: those the bindings give it to.
func (s *Space) foldersOfType(typ string) []string {
	bound := false
	for _, b := range s.Bindings() {
		if b.Type == typ {
			bound = true
			break
		}
	}
	if !bound {
		return nil
	}
	tree, err := s.idx.Tree()
	if err != nil {
		return nil
	}
	r := s.TypeResolver()
	var out []string
	seen := false
	for _, f := range tree {
		if r.For(f.Path, "") == typ {
			out = append(out, f.Path)
		}
		seen = seen || f.Path == ""
	}
	if !seen && r.For("", "") == typ {
		out = append(out, "")
	}
	return out
}

// CheckDailyConfig validates a configuration a caller gives for this space.
func (s *Space) CheckDailyConfig(c DailyConfig) (DailyConfig, error) { return s.checkDaily(c) }
