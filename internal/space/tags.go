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
// file that is too large, is not a regular file, does not parse, or is not a
// mapping at the top.
func (s *Space) loadTags() (*yaml.Node, error) {
	abs := filepath.Join(s.root, filepath.FromSlash(tagsFile))
	fi, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// A link in the way could lead out of the space.
	if !fi.Mode().IsRegular() || fi.Size() > maxTagsFile {
		return nil, errTagsFile
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxTagsFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxTagsFile {
		return nil, errTagsFile
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	switch err := dec.Decode(&doc); {
	case errors.Is(err, io.EOF):
		return nil, nil
	case err != nil:
		return nil, errTagsFile
	}
	// One document only: a second would not be written back, and what is in
	// it is someone's.
	var more yaml.Node
	if err := dec.Decode(&more); !errors.Is(err, io.EOF) {
		return nil, errTagsFile
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return nil, nil
	}
	if doc.Kind != yaml.DocumentNode || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errTagsFile
	}
	return &doc, nil
}

// mapValue finds a key of a mapping by its text, whatever YAML made of it
// (a tag written 2026 is a number to YAML and the tag "2026" to us). It
// returns the value and the index of the key in the mapping's content, or
// nil and -1.
//
// A key written twice counts as its last writing, which is what a reader of
// the file as a map sees too.
func mapValue(m *yaml.Node, key string) (*yaml.Node, int) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, -1
	}
	var v *yaml.Node
	at := -1
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i]; k.Kind == yaml.ScalarNode && k.Value == key {
			v, at = m.Content[i+1], i
		}
	}
	return v, at
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
	for i := 0; i+1 < len(tags.Content); i += 2 {
		k := tags.Content[i]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		if c, _ := mapValue(tags.Content[i+1], "color"); c != nil && c.Kind == yaml.ScalarNode {
			// A value written by hand that is no colour leaves the tag without one.
			if v, ok := NormalTagColor(c.Value); ok {
				out[k.Value] = v
			}
		}
	}
	return out
}

// SetTagColor gives a tag a colour (see NormalTagColor; it is stored in its
// normal form), or takes it away when color is "". The
// tag need not be in use. Everything else in the file stays as it is. A file
// that cannot be read is not replaced: the answer says it must be fixed by
// hand. The change is committed like any other.
func (s *Space) SetTagColor(by Author, tag, color string) error {
	switch {
	case strings.TrimSpace(tag) == "" || !utf8.ValidString(tag) || len(tag) > MaxTagBytes || strings.ContainsAny(tag, "\x00\n\r"):
		return invalidf("that is not a tag")
	case color != "":
		c, ok := NormalTagColor(color)
		if !ok {
			return invalidf("the colour must be one of %s, or a colour written #rrggbb", strings.Join(TagColors, ", "))
		}
		color = c
	}
	_, err := mutate(s, by, func(tx *txn) (struct{}, error) {
		doc, err := s.loadTags()
		if errors.Is(err, errTagsFile) {
			return struct{}{}, invalidf("%s is not something the server can read (it must be YAML with keys at the top, a regular file, under 1 MiB); it was left as it is and has to be fixed by hand", tagsFile)
		}
		if err != nil {
			return struct{}{}, err
		}
		if doc == nil {
			doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
		}
		root := doc.Content[0]
		tags, tagsAt := mapValue(root, "tags")
		if tags != nil && tags.Kind != yaml.MappingNode {
			return struct{}{}, invalidf("`tags` in %s is not a list of tags with their settings; it was left as it is and has to be fixed by hand", tagsFile)
		}
		entry, entryAt := mapValue(tags, tag)
		if entry != nil && entry.Kind != yaml.MappingNode {
			// `栀子: rose` or an empty value written by hand: the entry becomes
			// a mapping only when a colour is set.
			if entry.Kind == yaml.ScalarNode && (entry.Tag == "!!null" || entry.Value == "") {
				entry = nil
			} else {
				return struct{}{}, invalidf("the entry of %q in %s is not a list of settings; it was left as it is and has to be fixed by hand", tag, tagsFile)
			}
		}
		if color == "" {
			cur, colorAt := mapValue(entry, "color")
			if cur == nil {
				return struct{}{}, nil // nothing to take away
			}
			entry.Content = append(entry.Content[:colorAt:colorAt], entry.Content[colorAt+2:]...)
			if len(entry.Content) == 0 {
				tags.Content = append(tags.Content[:entryAt:entryAt], tags.Content[entryAt+2:]...)
			}
			if len(tags.Content) == 0 {
				root.Content = append(root.Content[:tagsAt:tagsAt], root.Content[tagsAt+2:]...)
			}
		} else {
			if tags == nil {
				tags = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				root.Content = append(root.Content, textNode("tags"), tags)
			}
			switch cur, _ := mapValue(entry, "color"); {
			case cur != nil && cur.Kind == yaml.ScalarNode && cur.Value == color:
				return struct{}{}, nil
			case cur != nil:
				*cur = *textNode(color)
			case entry != nil:
				entry.Content = append(entry.Content, textNode("color"), textNode(color))
			case entryAt >= 0:
				// The key is there with nothing under it.
				tags.Content[entryAt+1] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{textNode("color"), textNode(color)}}
			default:
				tags.Content = append(tags.Content, textNode(tag),
					&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{textNode("color"), textNode(color)}})
			}
		}
		abs := filepath.Join(s.root, filepath.FromSlash(tagsFile))
		// With nothing left to say (and no comment worth keeping) the file goes.
		empty := len(root.Content) == 0 && doc.HeadComment == "" && doc.FootComment == "" && root.HeadComment == "" && root.FootComment == ""
		var data []byte
		if !empty {
			if data, err = yaml.Marshal(doc); err != nil {
				return struct{}{}, err
			}
		}
		if err := tx.record(jrec{Op: "t", Path: tagsFile}); err != nil {
			return struct{}{}, err
		}
		tx.touch(tagsFile)
		if empty {
			if err := os.Remove(abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return struct{}{}, err
			}
			return struct{}{}, nil
		}
		return struct{}{}, writeAtomic(s.tmpDir(), abs, data)
	})
	return err
}
