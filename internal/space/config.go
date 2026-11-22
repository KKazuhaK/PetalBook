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
//	    views: [...]        (see views.go)
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
// file. Entries that are not a folder with a text type are not bindings. A
// folder written twice counts as its last entry (as mapValue does for any
// key), at the place of that entry.
func bindingsOf(doc *yaml.Node) []Binding {
	if doc == nil {
		return nil
	}
	folders, _ := mapValue(doc.Content[0], "folders")
	if folders == nil || folders.Kind != yaml.MappingNode {
		return nil
	}
	last := map[string]int{}
	for i := 0; i+1 < len(folders.Content); i += 2 {
		if k := folders.Content[i]; k.Kind == yaml.ScalarNode {
			last[bindingKey(k.Value)] = i
		}
	}
	var out []Binding
	for i := 0; i+1 < len(folders.Content); i += 2 {
		k := folders.Content[i]
		if k.Kind != yaml.ScalarNode || last[bindingKey(k.Value)] != i {
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
	views    map[string][]View // by folder, only those that have some
}

// config returns what is known of the settings file, read again when the file
// looks different. A file that cannot be read says nothing: a page then has no
// type but its own.
func (s *Space) config() configCache {
	abs := filepath.Join(s.root, filepath.FromSlash(spaceFile))
	fi, err := os.Lstat(abs)
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if err != nil || !fi.Mode().IsRegular() {
		s.cfg = configCache{}
		return s.cfg
	}
	if c := s.cfg; c.valid && c.size == fi.Size() && c.mtimeNs == fi.ModTime().UnixNano() {
		return c
	}
	doc, err := s.loadYAMLFile(spaceFile, maxSpaceFile, errSpaceFile)
	c := configCache{valid: true, size: fi.Size(), mtimeNs: fi.ModTime().UnixNano()}
	if err == nil {
		c.bindings = bindingsOf(doc)
		c.byFolder = make(map[string]string, len(c.bindings))
		for _, b := range c.bindings {
			c.byFolder[b.Folder] = b.Type
		}
		c.views = viewsOf(doc, "folders", bindingKey)
	}
	s.cfg = c
	return c
}

// readBindings returns the bindings of the space.
func (s *Space) readBindings() ([]Binding, map[string]string) {
	c := s.config()
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
	_, err := editSpaceYAML(s.root, s.name, s.tmpDir(), edit, func() error {
		if err := tx.record(jrec{Op: "t", Path: spaceFile}); err != nil {
			return err
		}
		tx.touch(spaceFile)
		return nil
	})
	s.cfgMu.Lock()
	s.cfg = configCache{}
	s.cfgMu.Unlock()
	return err
}

// editSpaceYAML is the editing of the settings file of the space at root,
// without a Space: before runs once, just ahead of the write, when there is
// something to write. It reports whether the file was written. Comments in the
// file stay, also when they are all there is.
func editSpaceYAML(root, name, tmpDir string, edit func(root *yaml.Node) (bool, error), before func() error) (bool, error) {
	doc, err := loadYAMLAt(root, spaceFile, maxSpaceFile, errSpaceFile)
	if errors.Is(err, errSpaceFile) {
		return false, spaceFileProblem()
	}
	if err != nil {
		return false, err
	}
	if doc == nil {
		doc = emptyDoc("")
	}
	top := doc.Content[0]
	fresh := len(top.Content) == 0
	changed, err := edit(top)
	if err != nil || !changed {
		return false, err
	}
	if fresh {
		// A file made here still says what the space is called.
		top.Content = append([]*yaml.Node{textNode("name"), textNode(name)}, top.Content...)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return false, err
	}
	if err := enc.Close(); err != nil {
		return false, err
	}
	if buf.Len() > maxSpaceFile {
		// A file this large would not be read again, and every setting in it
		// would be lost with it.
		return false, invalidf("the change would make space.yml larger than %d bytes, more than the server reads; nothing was changed", maxSpaceFile)
	}
	if before != nil {
		if err := before(); err != nil {
			return false, err
		}
	}
	abs := filepath.Join(root, filepath.FromSlash(spaceFile))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return false, err
	}
	return true, writeAtomic(tmpDir, abs, buf.Bytes())
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

// removeBinding takes the type off every entry written for folder (there is
// one unless the file was edited by hand into having two; the last would count,
// and an earlier one must not come back to life when it goes). An entry of any
// other shape holds no type to remove. It reports whether there was one.
func removeBinding(root, folders *yaml.Node, folder string) bool {
	removed := false
	for i := len(folders.Content) - 2; i >= 0; i -= 2 {
		if k := folders.Content[i]; k.Kind == yaml.ScalarNode && bindingKey(k.Value) == folder {
			if removeTypeKey(root, folders, i) {
				removed = true
			}
		}
	}
	return removed
}

// setBinding binds folder to typ, or unbinds it when typ is "".
func setBinding(root *yaml.Node, folder, typ string) (bool, error) {
	folders, err := foldersNode(root, typ != "")
	if err != nil || folders == nil {
		return false, err
	}
	i := entryAt(folders, folder)
	if typ == "" {
		return removeBinding(root, folders, folder), nil
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
// places under to. Nothing was at to before the move (a folder is never moved
// onto one), so every entry written for to or for a place under it is stale and
// goes, whether or not the folder has bindings of its own to bring: it must not
// give the moved folder a type it never had. An entry written twice arrives as
// its last one (the one that counted).
//
// With onlyIfBound the stale entries go only when there are entries to move:
// that is for the recovery of a move, where the old keys being still there is
// what says the edit was not made yet (a repeat must not drop what a first run
// moved to to).
func rebind(root *yaml.Node, from, to string, onlyIfBound bool) (bool, error) {
	folders, err := foldersNode(root, false)
	if err != nil || folders == nil {
		return false, err
	}
	n := len(folders.Content) / 2
	newKey := make([]string, n)  // "" for an entry that stays where it is
	arriving := map[string]int{} // new key -> position of the last entry that arrives under it
	for e := 0; e < n; e++ {
		k := folders.Content[2*e]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		switch key := bindingKey(k.Value); {
		case key == from:
			newKey[e] = to
		case strings.HasPrefix(key, from+"/"):
			newKey[e] = to + key[len(from):]
		default:
			continue
		}
		arriving[newKey[e]] = e
	}
	if onlyIfBound && len(arriving) == 0 {
		return false, nil
	}
	changed := len(arriving) > 0
	out := make([]*yaml.Node, 0, len(folders.Content))
	for e := 0; e < n; e++ {
		k, v := folders.Content[2*e], folders.Content[2*e+1]
		switch {
		case newKey[e] != "":
			if arriving[newKey[e]] != e {
				continue // an earlier writing of the same folder
			}
			*k = *textNode(newKey[e])
		case k.Kind == yaml.ScalarNode:
			if key := bindingKey(k.Value); key == to || strings.HasPrefix(key, to+"/") {
				changed = true
				continue // stale: nothing was there
			}
		}
		out = append(out, k, v)
	}
	folders.Content = out
	if len(folders.Content) == 0 {
		dropEmpty(root, folders, -1)
	}
	return changed, nil
}

// unbindWhere takes the type off every folder for which drop says it is to go
// (its folder is no more, or, for a type that was deleted, the type matches).
// The entry that counts for a folder is its last; all of a folder's entries go
// with it.
func unbindWhere(root *yaml.Node, drop func(folder, typ string) bool) (bool, error) {
	folders, err := foldersNode(root, false)
	if err != nil || folders == nil {
		return false, err
	}
	last := map[string]int{}
	for i := 0; i+1 < len(folders.Content); i += 2 {
		if k := folders.Content[i]; k.Kind == yaml.ScalarNode {
			last[bindingKey(k.Value)] = i
		}
	}
	var gone []string
	for key, i := range last {
		t, _ := mapValue(folders.Content[i+1], "type")
		if t != nil && t.Kind == yaml.ScalarNode && drop(key, strings.TrimSpace(t.Value)) {
			gone = append(gone, key)
		}
	}
	changed := false
	for _, key := range gone {
		if removeBinding(root, folders, key) {
			changed = true
		}
	}
	return changed, nil
}

// forgetFolders takes the settings the server keeps for a folder (its type and
// its views) off every folder for which gone says it is no more. A folder's
// entry is dropped when nothing else is left in it; what else a person wrote
// there stays, as it does for a binding. It reports whether anything changed.
func forgetFolders(root *yaml.Node, gone func(folder string) bool) (bool, error) {
	folders, err := foldersNode(root, false)
	if err != nil || folders == nil {
		return false, err
	}
	changed := false
	for i := len(folders.Content) - 2; i >= 0; i -= 2 {
		k, e := folders.Content[i], folders.Content[i+1]
		if k.Kind != yaml.ScalarNode || e.Kind != yaml.MappingNode || !gone(bindingKey(k.Value)) {
			continue
		}
		for j := len(e.Content) - 2; j >= 0; j -= 2 {
			if kk := e.Content[j]; kk.Kind == yaml.ScalarNode && (kk.Value == "type" || kk.Value == "views") {
				e.Content = append(e.Content[:j:j], e.Content[j+2:]...)
				changed = true
			}
		}
		dropEmpty(root, folders, i)
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

// recoverBindings finishes what a crash left of the bindings of a folder that
// was moved ("b": From to To) or deleted ("u": Path), after the files have
// been looked at. Both are safe to run again and again: a move's bindings are
// moved only if the folder is at its new place and no longer at the old one,
// and a deleted folder's are dropped only if the folder is not there.
func recoverBindings(root string, recs []jrec) (changed bool) {
	tmp := filepath.Join(root, ".petalbook", "tmp")
	gone := func(rel string) bool {
		_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		return errors.Is(err, fs.ErrNotExist)
	}
	for _, r := range recs {
		var edit func(*yaml.Node) (bool, error)
		switch r.Op {
		case "b":
			dir := filepath.Join(root, filepath.FromSlash(r.To))
			if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
				continue // the move did not happen, or not yet
			}
			if listed(filepath.Dir(filepath.Join(root, filepath.FromSlash(r.From))), filepath.Base(r.From)) {
				continue // the old folder is there: nothing was moved, or it was moved back
			}
			from, to := bindingKey(r.From), bindingKey(r.To)
			edit = func(top *yaml.Node) (bool, error) { return rebind(top, from, to, true) }
		case "u":
			canon := bindingKey(r.Path)
			edit = func(top *yaml.Node) (bool, error) {
				return forgetFolders(top, func(folder string) bool {
					return (folder == canon || strings.HasPrefix(folder, canon+"/")) && gone(folder)
				})
			}
		default:
			continue
		}
		// A file that cannot be read is left alone, as when the change was made.
		if ok, err := editSpaceYAML(root, "", tmp, edit, nil); err == nil && ok {
			changed = true
		}
	}
	return changed
}
