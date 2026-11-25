package space

import (
	"errors"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// IconColors are the colours an icon may have: eight, each with a light and a
// dark shade the client has tuned. Empty is the colour of the text.
var IconColors = []string{"red", "orange", "amber", "green", "teal", "blue", "purple", "pink"}

// MaxIconBytes is the longest icon: a Lucide name (`book-open`) or an emoji,
// which may be several code points long.
const MaxIconBytes = 64

// MaxSourcePresets is the most presets a space may list, and MaxSourceBytes the
// longest one.
const (
	MaxSourcePresets = 100
	MaxSourceBytes   = 200
)

// FolderStyle is the icon and colour of a folder, kept in space.yml:
//
//	folders:
//	  Tang:
//	    icon: book-open
//	    color: teal
//
// The entry of a folder is moved with the folder (see rebind), so the style of
// a folder follows it in the same commit as the move.
type FolderStyle struct {
	Folder string `json:"folder"`
	Icon   string `json:"icon,omitempty"`
	Color  string `json:"color,omitempty"`
}

// CheckIcon refuses what cannot be an icon: blank, too long, with a control
// character. Which names exist is the client's business (the icon set is its own).
func CheckIcon(icon string) error {
	if icon == "" {
		return nil
	}
	if len(icon) > MaxIconBytes || !utf8.ValidString(icon) || strings.IndexFunc(icon, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 || strings.TrimSpace(icon) != icon {
		return invalidf("An icon is a name or an emoji of at most %d bytes.", MaxIconBytes)
	}
	return nil
}

// CheckIconColor refuses a colour that is not one of IconColors ("" is none).
func CheckIconColor(color string) error {
	if color == "" {
		return nil
	}
	for _, c := range IconColors {
		if c == color {
			return nil
		}
	}
	return invalidf("The colour must be one of %s.", strings.Join(IconColors, ", "))
}

// stylesOf reads the icon and colour of the folders of a settings document, by
// folder. A value that is not text, an unknown colour and an icon that is not
// one are left out, as the other settings are read.
func stylesOf(doc *yaml.Node) map[string]FolderStyle {
	if doc == nil || len(doc.Content) == 0 {
		return nil
	}
	folders, _ := mapValue(doc.Content[0], "folders")
	if folders == nil || folders.Kind != yaml.MappingNode {
		return nil
	}
	out := map[string]FolderStyle{}
	var r reading
	eachPair(folders, func(k string, v *yaml.Node, _ int) {
		key := bindingKey(k)
		delete(out, key) // the last writing counts
		st := FolderStyle{Folder: key}
		if n, _ := r.value(v, "icon"); n != nil && n.Kind == yaml.ScalarNode && n.Tag != "!!null" && CheckIcon(n.Value) == nil {
			st.Icon = n.Value
		}
		if n, _ := r.value(v, "color"); n != nil && n.Kind == yaml.ScalarNode && CheckIconColor(n.Value) == nil {
			st.Color = n.Value
		}
		if st.Icon != "" || st.Color != "" {
			out[key] = st
		}
	})
	return out
}

// Styles lists the folders that have an icon or a colour, in folder order.
func (s *Space) Styles() []FolderStyle {
	m := s.config().styles
	out := make([]FolderStyle, 0, len(m))
	for _, st := range m {
		out = append(out, st)
	}
	sortStyles(out)
	return out
}

func sortStyles(l []FolderStyle) {
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && l[j].Folder < l[j-1].Folder; j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}

// setFolderStyle gives a folder an icon and a colour, or takes the ones that
// are "" away. Other keys of the entry (type, views) stay as they are.
func setFolderStyle(root *yaml.Node, folder, icon, color string) (bool, error) {
	set := icon != "" || color != ""
	folders, err := foldersNode(root, set)
	if err != nil || folders == nil {
		return false, err
	}
	i := entryAt(folders, folder)
	if i < 0 {
		if !set {
			return false, nil
		}
		e := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setStyleKeys(e, icon, color)
		folders.Content = append(folders.Content, textNode(folder), e)
		return true, nil
	}
	e := deref(folders.Content[i+1])
	if e == nil || e.Kind != yaml.MappingNode {
		if !set {
			return false, nil
		}
		if e != nil && !(e.Kind == yaml.ScalarNode && (e.Tag == "!!null" || e.Value == "")) {
			noteReplaced(root, folder)
		}
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setStyleKeys(n, icon, color)
		folders.Content[i+1] = n
		return true, nil
	}
	if sameStyle(e, icon, color) {
		return false, nil
	}
	setStyleKeys(own(folders, i+1), icon, color)
	if emptied(folders.Content[i+1]) {
		dropEmpty(root, folders, i)
	}
	return true, nil
}

func sameStyle(e *yaml.Node, icon, color string) bool {
	var r reading
	cur := func(key string) string {
		if n, _ := r.value(e, key); n != nil && n.Kind == yaml.ScalarNode && n.Tag != "!!null" {
			return n.Value
		}
		return ""
	}
	return cur("icon") == icon && cur("color") == color
}

func setStyleKeys(e *yaml.Node, icon, color string) {
	for _, kv := range [][2]string{{"icon", icon}, {"color", color}} {
		if kv[1] == "" {
			removeKey(e, kv[0])
		} else {
			setKey(e, kv[0], textNode(kv[1]))
		}
	}
}

// SetFolderStyle gives a folder an icon and a colour (see FolderStyle), or
// takes them away with "". The folder must be there. It returns the folder's
// path as it is on disk. It is one change, like binding a folder to a type.
func (s *Space) SetFolderStyle(by Author, folder, icon, color string) (string, error) {
	if err := checkRel(folder); err != nil {
		return "", err
	}
	if folder == "" {
		return "", invalidf("The root has no icon.")
	}
	if err := CheckIcon(icon); err != nil {
		return "", err
	}
	if err := CheckIconColor(color); err != nil {
		return "", err
	}
	return mutate(s, by, func(tx *txn) (string, error) {
		canon, existed, err := s.resolveFolder(folder)
		if errors.Is(err, ErrExists) || (err == nil && !existed) {
			return "", ErrNoFolder
		}
		if err != nil {
			return "", err
		}
		err = s.editSpaceFile(tx, func(root *yaml.Node) (bool, error) { return setFolderStyle(root, bindingKey(canon), icon, color) })
		return canon, err
	})
}

// --- source presets ---------------------------------------------------------

// sourcesOf reads the presets of the sources of a settings document: the list
// under `sources`. ok is false when the file does not say (the client then
// offers its own defaults); a file that says `sources: []` has none.
func sourcesOf(doc *yaml.Node) (presets []string, ok bool) {
	if doc == nil || len(doc.Content) == 0 {
		return nil, false
	}
	n, _ := mapValue(doc.Content[0], "sources")
	switch {
	case n == nil:
		return nil, false
	case n.Kind == yaml.ScalarNode && n.Tag == "!!null":
		return nil, false
	case n.Kind == yaml.ScalarNode:
		// One preset written by hand as text.
		if t := strings.TrimSpace(n.Value); t != "" {
			return []string{t}, true
		}
		return []string{}, true
	case n.Kind != yaml.SequenceNode:
		return nil, false
	}
	presets = []string{}
	seen := map[string]bool{}
	for _, e := range n.Content {
		e = deref(e)
		if e == nil || e.Kind != yaml.ScalarNode {
			continue
		}
		if t := strings.TrimSpace(e.Value); t != "" && !seen[t] {
			seen[t] = true
			presets = append(presets, t)
		}
	}
	return presets, true
}

// SourcePresets returns the presets of the sources the space chose, and
// whether it chose any (a space that did not has the client's defaults).
func (s *Space) SourcePresets() ([]string, bool) {
	c := s.config()
	return append([]string(nil), c.presets...), c.presetsSet
}

// CleanSourcePresets trims, drops blank ones and repeats, and checks the rest.
func CleanSourcePresets(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		if len(p) > MaxSourceBytes || !utf8.ValidString(p) || strings.ContainsAny(p, "\x00\n\r") {
			return nil, invalidf("A source is one line of text of at most %d bytes.", MaxSourceBytes)
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) > MaxSourcePresets {
		return nil, invalidf("At most %d sources.", MaxSourcePresets)
	}
	return out, nil
}

// SetSourcePresets sets the presets of the sources of the space, or, with nil,
// takes the choice away so the defaults apply again.
func (s *Space) SetSourcePresets(by Author, presets []string) error {
	var clean []string
	if presets != nil {
		var err error
		if clean, err = CleanSourcePresets(presets); err != nil {
			return err
		}
	}
	_, err := mutate(s, by, func(tx *txn) (struct{}, error) {
		return struct{}{}, s.editSpaceFile(tx, func(root *yaml.Node) (bool, error) {
			cur, ok := sourcesOf(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}})
			if presets == nil {
				if _, at := mapValue(root, "sources"); at < 0 {
					return false, nil
				}
				removeKey(root, "sources")
				return true, nil
			}
			if ok && equalStrings(cur, clean) {
				return false, nil
			}
			seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			if len(clean) <= 12 {
				seq.Style = yaml.FlowStyle
			}
			for _, p := range clean {
				seq.Content = append(seq.Content, textNode(p))
			}
			setKey(root, "sources", seq)
			return true, nil
		})
	})
	return err
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
