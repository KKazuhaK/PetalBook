package space

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// spaceFile is the settings file of a space. Besides its name it says which
// folders are bound to a page type:
//
//	folders:
//	  Tang:
//	    type: 文案
//
// A folder takes the type of the nearest folder above it that has one.
const spaceFile = ".petalbook/space.yml"

// maxSpaceFile is the most of the file that is read.
const maxSpaceFile = 1 << 20

// errSpaceFile says the file is there but cannot be used as it is.
var errSpaceFile = errors.New("the space settings file cannot be read")

func spaceFileProblem() error {
	return invalidf("%s is not something the server can read (it must be YAML with keys at the top, a regular file, under 1 MiB); it was left as it is and has to be fixed by hand", spaceFile)
}

// Binding says a folder is bound to a page type.
type Binding struct {
	Folder string `json:"folder"`
	Type   string `json:"type"`
}

// bindingKey is a folder path as a key of the folders mapping is compared:
// slashes around it do not count.
func bindingKey(folder string) string { return strings.Trim(folder, "/") }

// bindingsOf reads the bindings of a settings document, in the order of the
// file. Entries that are not a folder with a text type are not bindings.
func bindingsOf(doc *yaml.Node) []Binding {
	if doc == nil {
		return nil
	}
	folders, _ := mapValue(doc.Content[0], "folders")
	if folders == nil || folders.Kind != yaml.MappingNode {
		return nil
	}
	var out []Binding
	for i := 0; i+1 < len(folders.Content); i += 2 {
		k := folders.Content[i]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		if t, _ := mapValue(folders.Content[i+1], "type"); t != nil && t.Kind == yaml.ScalarNode && t.Tag != "!!null" && strings.TrimSpace(t.Value) != "" {
			out = append(out, Binding{bindingKey(k.Value), strings.TrimSpace(t.Value)})
		}
	}
	return out
}

// configCache remembers the bindings for as long as the file looks the same,
// so a listing does not parse it again for every request.
type configCache struct {
	valid    bool
	size     int64
	mtimeNs  int64
	bindings []Binding
	byFolder map[string]string
}

// readBindings returns the bindings of the space. A file that cannot be read
// says nothing: a page then has no type but its own.
func (s *Space) readBindings() ([]Binding, map[string]string) {
	abs := filepath.Join(s.root, filepath.FromSlash(spaceFile))
	fi, err := os.Lstat(abs)
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if err != nil || !fi.Mode().IsRegular() {
		s.cfg = configCache{}
		return nil, nil
	}
	if c := &s.cfg; c.valid && c.size == fi.Size() && c.mtimeNs == fi.ModTime().UnixNano() {
		return c.bindings, c.byFolder
	}
	doc, err := s.loadYAMLFile(spaceFile, maxSpaceFile, errSpaceFile)
	c := configCache{valid: true, size: fi.Size(), mtimeNs: fi.ModTime().UnixNano()}
	if err == nil {
		c.bindings = bindingsOf(doc)
		c.byFolder = make(map[string]string, len(c.bindings))
		for _, b := range c.bindings {
			c.byFolder[b.Folder] = b.Type
		}
	}
	s.cfg = c
	return c.bindings, c.byFolder
}

// Bindings lists the folders bound to a type, in the order of the file.
func (s *Space) Bindings() []Binding {
	b, _ := s.readBindings()
	return append([]Binding(nil), b...)
}

// TypeResolver answers which type a page has.
type TypeResolver struct{ byFolder map[string]string }

// TypeResolver reads the bindings once for a whole listing.
func (s *Space) TypeResolver() TypeResolver {
	_, m := s.readBindings()
	return TypeResolver{m}
}

// For is the effective type of a page in folder whose header says
// headerType: the header's, else that of the nearest bound folder above, else
// "". The type is a name; whether such a type exists is not looked at.
func (r TypeResolver) For(folder, headerType string) string {
	if headerType != "" {
		return headerType
	}
	if len(r.byFolder) == 0 {
		return ""
	}
	for f := folder; ; {
		if t, ok := r.byFolder[f]; ok {
			return t
		}
		if f == "" {
			return ""
		}
		i := strings.LastIndexByte(f, '/')
		if i < 0 {
			f = ""
		} else {
			f = f[:i]
		}
	}
}

// editSpaceFile reads the settings, lets edit change the document, and
// writes it back if edit says it changed. It is for use inside a change (tx):
// the write is journaled and part of the commit like any other file. A file
// that cannot be read is not replaced.
func (s *Space) editSpaceFile(tx *txn, edit func(root *yaml.Node) (changed bool, err error)) error {
	doc, err := s.loadYAMLFile(spaceFile, maxSpaceFile, errSpaceFile)
	if errors.Is(err, errSpaceFile) {
		return spaceFileProblem()
	}
	if err != nil {
		return err
	}
	if doc == nil {
		doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	fresh := len(root.Content) == 0
	changed, err := edit(root)
	if err != nil || !changed {
		return err
	}
	if fresh {
		// A file made here still says what the space is called.
		root.Content = append([]*yaml.Node{textNode("name"), textNode(s.name)}, root.Content...)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := tx.record(jrec{Op: "t", Path: spaceFile}); err != nil {
		return err
	}
	tx.touch(spaceFile)
	abs := filepath.Join(s.root, filepath.FromSlash(spaceFile))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	err = writeAtomic(s.tmpDir(), abs, buf.Bytes())
	s.cfgMu.Lock()
	s.cfg = configCache{}
	s.cfgMu.Unlock()
	return err
}

// foldersNode returns the folders mapping of the settings, made when create is
// set. nil when it is not there and not made. A `folders` that is not a mapping
// is the person's to fix.
func foldersNode(root *yaml.Node, create bool) (*yaml.Node, error) {
	folders, _ := mapValue(root, "folders")
	switch {
	case folders != nil && folders.Kind == yaml.MappingNode:
		return folders, nil
	case folders != nil && folders.Kind == yaml.ScalarNode && folders.Tag == "!!null":
		// `folders:` with nothing under it.
		if create {
			*folders = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			return folders, nil
		}
		return nil, nil
	case folders != nil:
		return nil, invalidf("`folders` in %s is not a list of folders with their settings; it was left as it is and has to be fixed by hand", spaceFile)
	case !create:
		return nil, nil
	}
	folders = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content, textNode("folders"), folders)
	return folders, nil
}

// entryAt finds the entry of a folder in the folders mapping. It returns the
// position of its key, or -1.
func entryAt(folders *yaml.Node, folder string) int {
	at := -1
	for i := 0; i+1 < len(folders.Content); i += 2 {
		if k := folders.Content[i]; k.Kind == yaml.ScalarNode && bindingKey(k.Value) == folder {
			at = i
		}
	}
	return at
}

// dropEmpty removes the entry at position i of folders when nothing is left in
// it, and the folders mapping itself when it has no entries.
func dropEmpty(root, folders *yaml.Node, i int) {
	if i >= 0 {
		if e := folders.Content[i+1]; e.Kind == yaml.MappingNode && len(e.Content) == 0 {
			folders.Content = append(folders.Content[:i:i], folders.Content[i+2:]...)
		}
	}
	if len(folders.Content) == 0 {
		if _, at := mapValue(root, "folders"); at >= 0 {
			root.Content = append(root.Content[:at:at], root.Content[at+2:]...)
		}
	}
}

// removeTypeKey takes the `type` out of the entry at i, dropping what is left
// empty. It reports whether there was one.
func removeTypeKey(root, folders *yaml.Node, i int) bool {
	e := folders.Content[i+1]
	if e.Kind != yaml.MappingNode {
		return false
	}
	_, at := mapValue(e, "type")
	if at < 0 {
		return false
	}
	// A key written twice is all removed: the last was the one that counted.
	for j := len(e.Content) - 2; j >= 0; j -= 2 {
		if k := e.Content[j]; k.Kind == yaml.ScalarNode && k.Value == "type" {
			e.Content = append(e.Content[:j:j], e.Content[j+2:]...)
		}
	}
	dropEmpty(root, folders, i)
	return true
}

// setBinding binds folder to typ, or unbinds it when typ is "".
func setBinding(root *yaml.Node, folder, typ string) (bool, error) {
	folders, err := foldersNode(root, typ != "")
	if err != nil || folders == nil {
		return false, err
	}
	i := entryAt(folders, folder)
	if typ == "" {
		if i < 0 {
			return false, nil
		}
		// An entry of any other shape holds no type to remove.
		return removeTypeKey(root, folders, i), nil
	}
	if i < 0 {
		folders.Content = append(folders.Content, textNode(folder),
			&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{textNode("type"), textNode(typ)}})
		return true, nil
	}
	e := folders.Content[i+1]
	switch {
	case e.Kind == yaml.ScalarNode && (e.Tag == "!!null" || e.Value == ""):
		folders.Content[i+1] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{textNode("type"), textNode(typ)}}
		return true, nil
	case e.Kind != yaml.MappingNode:
		return false, invalidf("the entry of the folder %q in %s is not a list of settings; it was left as it is and has to be fixed by hand", folder, spaceFile)
	}
	if cur, _ := mapValue(e, "type"); cur != nil {
		if cur.Kind == yaml.ScalarNode && cur.Value == typ && cur.Tag == "!!str" {
			return false, nil
		}
		*cur = *textNode(typ)
		return true, nil
	}
	e.Content = append(e.Content, textNode("type"), textNode(typ))
	return true, nil
}

// rebind moves the entries of folder, and of the folders below it, to the same
// places under to.
func rebind(root *yaml.Node, from, to string) (bool, error) {
	folders, err := foldersNode(root, false)
	if err != nil || folders == nil {
		return false, err
	}
	changed := false
	for i := 0; i+1 < len(folders.Content); i += 2 {
		k := folders.Content[i]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		key := bindingKey(k.Value)
		switch {
		case key == from:
			*k = *textNode(to)
			changed = true
		case strings.HasPrefix(key, from+"/"):
			*k = *textNode(to + key[len(from):])
			changed = true
		}
	}
	return changed, nil
}

// unbindWhere takes the type off every folder for which gone says its folder
// is no more (or, for a type that was deleted, for which the type matches).
func unbindWhere(root *yaml.Node, drop func(folder, typ string) bool) (bool, error) {
	folders, err := foldersNode(root, false)
	if err != nil || folders == nil {
		return false, err
	}
	changed := false
	for i := len(folders.Content) - 2; i >= 0; i -= 2 {
		k := folders.Content[i]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		t, _ := mapValue(folders.Content[i+1], "type")
		if t == nil || t.Kind != yaml.ScalarNode || !drop(bindingKey(k.Value), strings.TrimSpace(t.Value)) {
			continue
		}
		if removeTypeKey(root, folders, i) {
			changed = true
		}
	}
	return changed, nil
}

// fileGone reports whether a path of the space is not there any more.
func (s *Space) fileGone(rel string) bool {
	_, err := os.Lstat(filepath.Join(s.root, filepath.FromSlash(rel)))
	return errors.Is(err, fs.ErrNotExist)
}

// editBindings is editSpaceFile for a change whose business is something else
// (moving or deleting a folder): a settings file that cannot be read has no
// bindings to keep in step, so it does not stop the change, and is left as it
// is.
func (s *Space) editBindings(tx *txn, edit func(root *yaml.Node) (bool, error)) error {
	err := s.editSpaceFile(tx, edit)
	var inv *InvalidError
	if errors.As(err, &inv) {
		s.log.Warn("the folder bindings in space.yml were not updated", "error", err)
		return nil
	}
	return err
}
