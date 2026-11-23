package space

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// tagsFile holds what a space says about its tags (their colours). It is a
// file of the repository, so it travels with it.
const tagsFile = ".petalbook/tags.yml"

// TagColors are the colours a tag may have. How each looks is the client's
// business.
var TagColors = []string{"gray", "red", "orange", "amber", "green", "teal", "blue", "purple", "pink", "rose"}

// NormalTagColor returns a colour as it is stored: one of the names in
// TagColors, or a colour of the person's own written `#rrggbb` (six
// hexadecimal digits, in lower case whatever case they came in). ok is false
// for anything else.
func NormalTagColor(c string) (string, bool) {
	for _, k := range TagColors {
		if k == c {
			return c, true
		}
	}
	if len(c) != 7 || c[0] != '#' {
		return "", false
	}
	for i := 1; i < 7; i++ {
		d := c[i]
		if !(d >= '0' && d <= '9' || d >= 'a' && d <= 'f' || d >= 'A' && d <= 'F') {
			return "", false
		}
	}
	return strings.ToLower(c), true
}

// MaxTagBytes is the longest tag a colour can be given to.
const MaxTagBytes = 200

// maxTagsFile is the most of the file that is read: it is written by the
// server and by hand, and neither makes it large.
const maxTagsFile = 1 << 20

// errTagsFile says the file is there but cannot be used as it is.
var errTagsFile = errors.New("the tags file cannot be read")

// loadTags reads the file as a YAML document, keeping everything in it
// (comments, keys the server does not know, keys that are not text). It
// returns nil when there is no file or nothing in it, and errTagsFile for a
// file that is too large, is not a regular file, does not parse, is not a
// mapping at the top, or whose aliases stand for more than the server reads
// (see anchors.go).
func (s *Space) loadTags() (*yaml.Node, error) {
	return s.loadYAMLFile(tagsFile, maxTagsFile, errTagsFile)
}

// loadYAMLFile is loadTags for any of the space's YAML files: rel is the path
// in the space, max the most that is read, and bad what is returned for a file
// that cannot be used as it is.
func (s *Space) loadYAMLFile(rel string, max int64, bad error) (*yaml.Node, error) {
	return loadYAMLAt(s.root, rel, max, bad)
}

// emptyDoc is a document with an empty mapping, carrying head as its comment
// (a file with nothing but comments keeps them when something is written to it).
func emptyDoc(head string) *yaml.Node {
	return &yaml.Node{Kind: yaml.DocumentNode, HeadComment: head,
		Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
}

// commentLines is the comments of a file that has no content: its comment
// lines, with the blank lines between them, without those around them.
func commentLines(data []byte) string {
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "---") || strings.HasPrefix(t, "...") {
			// A comment on the marker's own line (`--- # note`) is a comment too.
			if rest := strings.TrimSpace(t[3:]); strings.HasPrefix(rest, "#") {
				t = rest
			}
		}
		if strings.HasPrefix(t, "#") {
			out = append(out, t)
		} else if t == "" && len(out) > 0 {
			out = append(out, "")
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// loadYAMLAt is loadYAMLFile for the space at root. A file with nothing but
// comments gives an empty document that has them as its head comment, so
// that writing to it does not lose them.
func loadYAMLAt(root, rel string, max int64, bad error) (*yaml.Node, error) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// A link in the way could lead out of the space.
	if !fi.Mode().IsRegular() || fi.Size() > max {
		return nil, bad
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, bad
	}
	return parseYAMLDoc(data, bad)
}

// parseYAMLDoc reads the text of a settings file by the rules of loadYAMLAt.
// It is also what a file about to be written is checked with: a document the
// server could not read again must not be written.
func parseYAMLDoc(data []byte, bad error) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	switch err := dec.Decode(&doc); {
	case errors.Is(err, io.EOF):
		if c := commentLines(data); c != "" {
			return emptyDoc(c), nil
		}
		return nil, nil
	case err != nil:
		return nil, bad
	}
	// One document only: a second would not be written back, and what is in
	// it is someone's.
	var more yaml.Node
	if err := dec.Decode(&more); !errors.Is(err, io.EOF) {
		return nil, bad
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return nil, nil
	}
	if r := doc.Content[0]; doc.Kind == yaml.DocumentNode && r.Kind == yaml.ScalarNode && r.Tag == "!!null" && r.Value == "" {
		// `---` with nothing after it: an empty document. The parser keeps only
		// some of its comments (not the ones before the marker, say), so the
		// comments are taken from the text, all of them in their order.
		return emptyDoc(commentLines(data)), nil
	}
	if doc.Kind != yaml.DocumentNode || doc.Content[0].Kind != yaml.MappingNode {
		return nil, bad
	}
	// What its aliases stand for is bounded here, once, for every reader and
	// writer of the document (see anchors.go).
	if !withinBudget(&doc) {
		return nil, bad
	}
	return &doc, nil
}

func textNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

// TagColors returns the colour of every tag that has one. Colours the server
// does not know (written by hand) are left out, and a file that cannot be
// read says nothing. It reads the file each time: it is small, and may have
// come in with a pull.
func (s *Space) TagColors() map[string]string {
	out := map[string]string{}
	doc, err := s.loadTags()
	if err != nil || doc == nil {
		return out
	}
	tags, _ := mapValue(doc.Content[0], "tags")
	if tags == nil || tags.Kind != yaml.MappingNode {
		return out
	}
	// A tag written twice counts as its last writing, as it does to a write.
	eachPair(tags, func(tag string, entry *yaml.Node, _ int) {
		delete(out, tag)
		if c, _ := mapValue(entry, "color"); c != nil && c.Kind == yaml.ScalarNode {
			// A value written by hand that is no colour leaves the tag without one.
			if v, ok := NormalTagColor(c.Value); ok {
				out[tag] = v
			}
		}
	})
	return out
}

// checkTagName refuses what cannot be the name of a tag.
func checkTagName(tag string) error {
	if strings.TrimSpace(tag) == "" || !utf8.ValidString(tag) || len(tag) > MaxTagBytes || strings.ContainsAny(tag, "\x00\n\r") {
		return invalidf("that is not a tag")
	}
	return nil
}

// tagsFileProblem is what is said of a tags file that cannot be used as it is.
func tagsFileProblem() error {
	return invalidf("%s is not something the server can read (it must be YAML with keys at the top, a regular file, under 1 MiB); it was left as it is and has to be fixed by hand", tagsFile)
}

// tagsNode returns the tags mapping of the file, made when create is set; nil
// when it is not there and not made. A `tags` that is not a mapping is the
// person's to fix.
func tagsNode(root *yaml.Node, create bool) (*yaml.Node, error) {
	tags, at := mapValue(root, "tags")
	switch {
	case tags != nil && tags.Kind == yaml.MappingNode:
		return own(root, at+1), nil
	case tags != nil && tags.Kind == yaml.ScalarNode && tags.Tag == "!!null":
		if create {
			tags = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			root.Content[at+1] = tags
			return tags, nil
		}
		return nil, nil
	case tags != nil:
		return nil, invalidf("`tags` in %s is not a list of tags with their settings; it was left as it is and has to be fixed by hand", tagsFile)
	case !create:
		return nil, nil
	}
	tags = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content, textNode("tags"), tags)
	return tags, nil
}

var tagsSettings = settingsFile{rel: tagsFile, max: maxTagsFile, bad: errTagsFile, problem: tagsFileProblem}

// editTagsFile reads the tags file, lets edit change the document, and writes
// it back if edit says it changed (inside a change: the write is journaled and
// part of the commit). With nothing left to say, and no comment worth keeping,
// the file goes. A file that cannot be read is not replaced.
func (s *Space) editTagsFile(tx *txn, edit func(root *yaml.Node) (changed bool, err error)) error {
	data, changed, err := planSettingsEdit(s.root, tagsSettings, edit, func(doc *yaml.Node) ([]byte, error) {
		root := doc.Content[0]
		if len(root.Content) == 0 && doc.HeadComment == "" && doc.FootComment == "" && root.HeadComment == "" && root.FootComment == "" {
			return nil, nil
		}
		return yaml.Marshal(doc)
	})
	if err != nil || !changed {
		return err
	}
	abs := filepath.Join(s.root, filepath.FromSlash(tagsFile))
	if err := tx.record(jrec{Op: "t", Path: tagsFile}); err != nil {
		return err
	}
	tx.touch(tagsFile)
	// The cache is dropped after the write, as with space.yml: a reading in
	// between would otherwise keep the old content under the size and time of
	// the new file.
	defer func() {
		s.cfgMu.Lock()
		s.tagCfg = tagCache{}
		s.cfgMu.Unlock()
	}()
	if len(data) == 0 {
		if err := os.Remove(abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeAtomic(s.tmpDir(), abs, data)
}

// tagEntry finds the settings of a tag in the tags mapping: what the entry
// means (nil if there is none or it holds nothing; to change it, own it first)
// and the position of its key (-1 if none).
// An entry that is not a mapping and not empty is the person's to fix.
func tagEntry(tags *yaml.Node, tag string) (entry *yaml.Node, at int, err error) {
	entry, at = mapValue(tags, tag)
	if entry != nil && entry.Kind != yaml.MappingNode {
		// `栀子: rose` or an empty value written by hand: the entry becomes
		// a mapping only when something is set.
		if entry.Kind == yaml.ScalarNode && (entry.Tag == "!!null" || entry.Value == "") {
			return nil, at, nil
		}
		return nil, at, invalidf("the entry of %q in %s is not a list of settings; it was left as it is and has to be fixed by hand", tag, tagsFile)
	}
	return entry, at, nil
}

// SetTagColor gives a tag a colour (see NormalTagColor; it is stored in its
// normal form), or takes it away when color is "". The
// tag need not be in use. Everything else in the file stays as it is (the
// views of the tag too). A file that cannot be read is not replaced: the answer
// says it must be fixed by hand. The change is committed like any other.
func (s *Space) SetTagColor(by Author, tag, color string) error {
	if err := checkTagName(tag); err != nil {
		return err
	}
	if color != "" {
		c, ok := NormalTagColor(color)
		if !ok {
			return invalidf("the colour must be one of %s, or a colour written #rrggbb", strings.Join(TagColors, ", "))
		}
		color = c
	}
	_, err := mutate(s, by, func(tx *txn) (struct{}, error) {
		return struct{}{}, s.editTagsFile(tx, func(root *yaml.Node) (bool, error) {
			tags, err := tagsNode(root, false)
			if err != nil {
				return false, err
			}
			entry, entryAt, err := tagEntry(tags, tag)
			if err != nil {
				return false, err
			}
			if color == "" {
				if !hasKey(entry, "color") {
					return false, nil // nothing to take away
				}
				removeKey(own(tags, entryAt+1), "color")
				dropEmptyTag(root, tags, entryAt)
				return true, nil
			}
			if tags == nil {
				if tags, err = tagsNode(root, true); err != nil {
					return false, err
				}
			}
			switch cur, _ := mapValue(entry, "color"); {
			case cur != nil && cur.Kind == yaml.ScalarNode && cur.Value == color:
				return false, nil
			case entry != nil:
				setKey(own(tags, entryAt+1), "color", textNode(color))
			case entryAt >= 0:
				// The key is there with nothing under it.
				tags.Content[entryAt+1] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{textNode("color"), textNode(color)}}
			default:
				tags.Content = append(tags.Content, textNode(tag),
					&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{textNode("color"), textNode(color)}})
			}
			return true, nil
		})
	})
	return err
}
