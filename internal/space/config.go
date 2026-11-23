package space

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
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
	return unavailable(nil, "%s cannot be used as it is, and there is nothing here to restore it with", spaceFile)
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
// key), at the place of that entry. A type written as an alias is the type of
// its anchor, as everywhere in the file (see anchors.go).
func bindingsOf(doc *yaml.Node) []Binding {
	if doc == nil {
		return nil
	}
	folders, _ := mapValue(doc.Content[0], "folders")
	if folders == nil || folders.Kind != yaml.MappingNode {
		return nil
	}
	type entry struct {
		key string
		val *yaml.Node
	}
	var entries []entry
	last := map[string]int{}
	eachPair(folders, func(k string, v *yaml.Node, _ int) {
		key := bindingKey(k)
		last[key] = len(entries)
		entries = append(entries, entry{key, v})
	})
	var out []Binding
	var r reading
	for i, e := range entries {
		if last[e.key] != i {
			continue
		}
		if t := r.typeOf(e.val); t != "" {
			out = append(out, Binding{e.key, t})
		}
	}
	return out
}

// typeOf is the type an entry of the folders mapping binds its folder to, or "".
func typeOf(entry *yaml.Node) string {
	var r reading
	return r.typeOf(entry)
}

func (r *reading) typeOf(entry *yaml.Node) string {
	t, _ := r.value(entry, "type")
	if t == nil || t.Kind != yaml.ScalarNode || t.Tag == "!!null" {
		return ""
	}
	return strings.TrimSpace(t.Value)
}

// configCache remembers the bindings for as long as the file looks the same,
// so a listing does not parse it again for every request.
type configCache struct {
	valid    bool
	mode     fs.FileMode
	size     int64
	mtimeNs  int64
	bindings []Binding
	byFolder map[string]string
	views    map[string][]View // by folder, only those that have some
	daily    *DailyConfig      // where the daily text comes from, when the file says
	blocked  string            // what is in the way of the file, when something is
}

// config returns what is known of the settings file, read again when the file
// looks different. A file that cannot be used is read as the settings restored
// for it (see restore.go).
func (s *Space) config() configCache {
	fi, blocked, err := lstatIn(s.root, spaceFile)
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if errors.Is(err, fs.ErrNotExist) {
		s.heal.fine(spaceSettings)
		s.cfg = configCache{}
		return s.cfg
	}
	if err != nil {
		s.cfg = configCache{}
		return s.cfg
	}
	if c := s.cfg; c.valid && c.blocked == blocked && c.mode == fi.Mode() && c.size == fi.Size() && c.mtimeNs == fi.ModTime().UnixNano() {
		return c
	}
	doc, err := s.loadSettings(spaceSettings)
	c := configCache{valid: true, mode: fi.Mode(), size: fi.Size(), mtimeNs: fi.ModTime().UnixNano(), blocked: blocked}
	if err == nil {
		c.bindings = bindingsOf(doc)
		c.byFolder = make(map[string]string, len(c.bindings))
		for _, b := range c.bindings {
			c.byFolder[b.Folder] = b.Type
		}
		c.views = viewsOf(doc, "folders", bindingKey)
		c.daily = dailyOf(doc)
		s.heal.setOdd(spaceSettings, oddEntries(doc, spaceSettings))
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
// that cannot be used is put aside in the same change, and the settings
// restored for it are written with the edit (see restore.go).
func (s *Space) editSpaceFile(tx *txn, edit func(root *yaml.Node) (changed bool, err error)) error {
	_, err := editSpaceYAML(s.heal, s.root, s.name, edit, func(rel string) error {
		if err := tx.record(jrec{Op: "t", Path: rel}); err != nil {
			return err
		}
		tx.touch(rel)
		return nil
	}, func() (string, error) { return s.clearWayIn(tx, spaceFile, spaceFile) })
	s.cfgMu.Lock()
	s.cfg = configCache{}
	s.cfgMu.Unlock()
	return err
}

var spaceSettings = settingsFile{rel: spaceFile, section: "folders", setting: "type", max: maxSpaceFile, bad: errSpaceFile, problem: spaceFileProblem}

// planSpaceYAML is the text space.yml would have after edit, without writing
// it: see planSettingsEdit. Comments in the file stay, also when they are all
// there is.
func planSpaceYAML(root, name string, edit func(root *yaml.Node) (bool, error)) ([]byte, bool, error) {
	data, changed, _, err := planSpaceYAMLWith(nil, root, name, edit)
	return data, changed, err
}

func planSpaceYAMLWith(h *healer, root, name string, edit func(root *yaml.Node) (bool, error)) ([]byte, bool, *repair, error) {
	return planSettingsEdit(root, spaceSettings, h, func(top *yaml.Node) (bool, error) {
		fresh := len(top.Content) == 0
		changed, err := edit(top)
		if err != nil || !changed {
			return false, err
		}
		if fresh {
			// A file made here still says what the space is called.
			top.Content = append([]*yaml.Node{textNode("name"), textNode(name)}, top.Content...)
		}
		return true, nil
	}, func(doc *yaml.Node) ([]byte, error) {
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(doc); err != nil {
			return nil, err
		}
		if err := enc.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	})
}

// editSpaceYAML is the editing of the settings file of the space at root,
// without a Space: before runs just ahead of the write of each file that is
// written, when there is something to write. It returns the files written:
// space.yml, and the copy of a file that could not be used, put aside first.
// clear puts right a folder on the way that is not one (clearWay); without
// it, nothing is written through such a thing.
func editSpaceYAML(h *healer, root, name string, edit func(root *yaml.Node) (bool, error), before func(rel string) error, clear func() (string, error)) ([]string, error) {
	data, changed, rep, err := planSpaceYAMLWith(h, root, name, edit)
	if err != nil || !changed {
		return nil, err
	}
	if before == nil {
		before = func(string) error { return nil }
	}
	var written []string
	kept := ""
	blocked, err := wayBlocked(root, spaceFile)
	switch {
	case err != nil:
		return nil, err
	case blocked != "" && clear == nil:
		return nil, unavailable(nil, "%s is not a folder; it is put right by the next change", blocked)
	case blocked != "":
		if kept, err = clear(); err != nil {
			return nil, err
		}
		written = append(written, kept)
	case rep != nil && h != nil:
		if kept, err = h.keep(spaceSettings, before); err != nil {
			return nil, err
		}
		written = append(written, kept)
	}
	if err := before(spaceFile); err != nil {
		return written, err
	}
	if err := writeIn(root, spaceFile, data); err != nil {
		return written, err
	}
	h.wroteGood(spaceSettings, data)
	if rep != nil && h != nil {
		h.repaired(spaceSettings, rep, kept)
	}
	return append(written, spaceFile), nil
}

// foldersNode returns the folders mapping of the settings, the writer's own to
// change, made when create is set. nil when it is not there and not made. A
// `folders` that is not a mapping is the person's to fix.
func foldersNode(root *yaml.Node, create bool) (*yaml.Node, error) {
	folders, at := mapValue(root, "folders")
	switch {
	case folders != nil && folders.Kind == yaml.MappingNode:
		return own(root, at+1), nil
	case folders != nil && folders.Kind == yaml.ScalarNode && folders.Tag == "!!null":
		// `folders:` with nothing under it.
		if create {
			folders = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			root.Content[at+1] = folders
			return folders, nil
		}
		return nil, nil
	case folders != nil:
		// Not a list of folders: a file with that is not loaded (see
		// settingsFile.parse), so this is only for a document made otherwise.
		if !create {
			return nil, nil
		}
		noteReplaced(root, "folders")
		folders = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if at >= 0 {
			root.Content[at+1] = folders
			return folders, nil
		}
	case !create:
		return nil, nil
	}
	folders = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content, textNode("folders"), folders)
	return folders, nil
}

// folderAt is the folder the entry at position i of the folders mapping is
// written for; ok is false for a key that is not text.
func folderAt(folders *yaml.Node, i int) (string, bool) {
	k, ok := keyText(folders.Content[i])
	return bindingKey(k), ok
}

// entryAt finds the entry of a folder in the folders mapping. It returns the
// position of its key, or -1.
func entryAt(folders *yaml.Node, folder string) int {
	at := -1
	for i := 0; i+1 < len(folders.Content); i += 2 {
		if key, ok := folderAt(folders, i); ok && key == folder {
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

// untype takes the `type` off every entry of folders whose folder match names,
// dropping the entries left empty, in one pass over the mapping whatever the
// number of entries that go. It reports whether there was a type to take. An
// entry that is an alias, or that has its type from a merge key, gets its own
// copy first: what else refers to the anchor keeps its type. An entry of any
// other shape holds no type to remove, and stays as it is.
func untype(root, folders *yaml.Node, match func(folder string) bool) (bool, error) {
	removed := false
	st := stripper{file: spaceSettings, top: root, keys: []string{"type"}}
	kept := make([]*yaml.Node, 0, len(folders.Content))
	for i := 0; i+1 < len(folders.Content); i += 2 {
		if key, ok := folderAt(folders, i); ok && match(key) && st.has(folders.Content[i+1]) {
			// A key written twice is all removed: the last was the one that counted.
			if err := st.strip(folders, i+1); err != nil {
				return false, err
			}
			removed = true
			if emptied(folders.Content[i+1]) {
				continue
			}
		}
		kept = append(kept, folders.Content[i], folders.Content[i+1])
	}
	folders.Content = kept
	dropEmpty(root, folders, -1)
	return removed, nil
}

// removeBinding takes the type off every entry written for folder (there is
// one unless the file was edited by hand into having two; the last would count,
// and an earlier one must not come back to life when it goes). It reports
// whether there was one.
func removeBinding(root, folders *yaml.Node, folder string) (bool, error) {
	return untype(root, folders, func(key string) bool { return key == folder })
}

// setBinding binds folder to typ, or unbinds it when typ is "".
func setBinding(root *yaml.Node, folder, typ string) (bool, error) {
	folders, err := foldersNode(root, typ != "")
	if err != nil || folders == nil {
		return false, err
	}
	i := entryAt(folders, folder)
	if typ == "" {
		return removeBinding(root, folders, folder)
	}
	if i < 0 {
		folders.Content = append(folders.Content, textNode(folder),
			&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{textNode("type"), textNode(typ)}})
		return true, nil
	}
	e := deref(folders.Content[i+1])
	switch {
	case e == nil || e.Kind == yaml.ScalarNode && (e.Tag == "!!null" || e.Value == ""):
		folders.Content[i+1] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{textNode("type"), textNode(typ)}}
		return true, nil
	case e.Kind != yaml.MappingNode:
		// `Tang: yes`: not settings. It read as none, and is written over; the
		// file as it was is kept (noteReplaced).
		noteReplaced(root, folder)
		folders.Content[i+1] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{textNode("type"), textNode(typ)}}
		return true, nil
	}
	cur, _ := mapValue(e, "type")
	if cur != nil && cur.Kind == yaml.ScalarNode && cur.Value == typ && cur.Tag == "!!str" {
		return false, nil
	}
	if cur != nil && cur.Kind != yaml.ScalarNode {
		noteReplaced(root, folder+": type")
	}
	setKey(own(folders, i+1), "type", textNode(typ))
	return true, nil
}

// rebind moves the entries of folder, and of the folders below it, to the same
// places under to. Nothing was at to before the move (a folder is never moved
// onto one), so every entry written for to or for a place under it is stale and
// goes, whether or not the folder has bindings of its own to bring: it must not
// give the moved folder a type it never had. An entry written twice arrives as
// its last one (the one that counted).
//
// An entry keeps its value as it is written, an alias too: only its key is
// new. An anchor in an entry that goes moves to the first alias that is left
// (settle).
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
		key, ok := folderAt(folders, 2*e)
		if !ok {
			continue
		}
		switch {
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
		key, isText := folderAt(folders, 2*e)
		switch {
		case newKey[e] != "":
			if arriving[newKey[e]] != e {
				continue // an earlier writing of the same folder
			}
			nk := textNode(newKey[e])
			nk.HeadComment, nk.LineComment, nk.FootComment = k.HeadComment, k.LineComment, k.FootComment
			k = nk
		case isText:
			if key == to || strings.HasPrefix(key, to+"/") {
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
	var order []string
	for i := 0; i+1 < len(folders.Content); i += 2 {
		if key, ok := folderAt(folders, i); ok {
			if _, seen := last[key]; !seen {
				order = append(order, key)
			}
			last[key] = i
		}
	}
	gone := map[string]bool{}
	var r reading
	for _, key := range order {
		if t := r.typeOf(folders.Content[last[key]+1]); t != "" && drop(key, t) {
			gone[key] = true
		}
	}
	if len(gone) == 0 {
		return false, nil
	}
	return untype(root, folders, func(key string) bool { return gone[key] })
}

// forgetFolders takes the settings the server keeps for a folder (its type and
// its views) off every folder for which gone says it is no more. A folder's
// entry is dropped when nothing else is left in it; what else a person wrote
// there stays, as it does for a binding. An entry that is an alias, or has its
// settings from a merge key, gets its own copy first, so that what else refers
// to the anchor keeps them. It reports whether anything changed.
func forgetFolders(root *yaml.Node, gone func(folder string) bool) (bool, error) {
	folders, err := foldersNode(root, false)
	if err != nil || folders == nil {
		return false, err
	}
	// One pass, whatever the number of folders that go: what stays is
	// collected, not what goes cut out one at a time.
	changed := false
	st := stripper{file: spaceSettings, top: root, keys: []string{"type", "views"}}
	kept := make([]*yaml.Node, 0, len(folders.Content))
	for i := 0; i+1 < len(folders.Content); i += 2 {
		key, ok := folderAt(folders, i)
		if e := deref(folders.Content[i+1]); ok && e != nil && e.Kind == yaml.MappingNode && gone(key) {
			if st.has(folders.Content[i+1]) {
				if err := st.strip(folders, i+1); err != nil {
					return false, err
				}
				changed = true
			}
			if emptied(folders.Content[i+1]) {
				continue
			}
		}
		kept = append(kept, folders.Content[i], folders.Content[i+1])
	}
	folders.Content = kept
	dropEmpty(root, folders, -1)
	return changed, nil
}

// fileGone reports whether a path of the space is not there any more.
func (s *Space) fileGone(rel string) bool {
	_, err := os.Lstat(filepath.Join(s.root, filepath.FromSlash(rel)))
	return errors.Is(err, fs.ErrNotExist)
}

// settingsReason says why a change of a folder cannot take its settings in
// space.yml along, for an error that means so: the file would be too large, or
// could not be read again. ok is false for any other error.
func settingsReason(err error) (reason string, ok bool) {
	var full *settingsFullError
	if errors.As(err, &full) {
		return fmt.Sprintf("the file would become larger than %d bytes, more than the server reads; remove some saved views and try again", maxSpaceFile), true
	}
	var unreadable *unreadableEditError
	if errors.As(err, &unreadable) {
		return "the anchors and aliases around its entry stand in the way", true
	}
	return "", false
}

// bindingsWouldFail makes the edit of space.yml that a move or a delete of a
// folder will need, without writing it, and says why it cannot be made if so:
// asked before the folder is touched, so that such a change is refused with
// nothing done. A settings file that cannot be used is no reason: the edit is
// made on the settings restored for it.
func (s *Space) bindingsWouldFail(edit func(root *yaml.Node) (bool, error)) (reason string, fails bool) {
	_, _, _, err := planSpaceYAMLWith(s.heal, s.root, s.name, edit)
	return settingsReason(err)
}

// editBindings is editSpaceFile for a change whose business is something else
// (moving or deleting a folder).
//
// When the settings cannot follow the folder (the file would be too full, or
// its anchors stand in the way), refused is why, and the caller refuses the
// move or delete (and undoes what it can), or the folder's type and views
// would stay under a key that a new folder of that name then inherits.
func (s *Space) editBindings(tx *txn, edit func(root *yaml.Node) (bool, error)) (refused string, err error) {
	err = s.editSpaceFile(tx, edit)
	if reason, ok := settingsReason(err); ok {
		return reason, nil
	}
	var inv *InvalidError
	if errors.As(err, &inv) {
		s.log.Warn("the folder bindings in space.yml were not updated", "error", err)
		return "", nil
	}
	return "", err
}

// recoverBindings finishes what a crash left of the bindings of a folder that
// was moved ("b": From to To) or deleted ("u": Path), after the files have
// been looked at. Both are safe to run again and again: a move's bindings are
// moved only if the folder is at its new place and no longer at the old one,
// and a deleted folder's are dropped only if the folder is not there.
func recoverBindings(h *healer, root string, recs []jrec) (written []string) {
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
			edit = func(top *yaml.Node) (bool, error) {
				a, err := rebind(top, from, to, true)
				if err != nil {
					return false, err
				}
				b, err := rebindDaily(top, from, to)
				return a || b, err
			}
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
		// A file that cannot be used is put aside and the settings restored for
		// it are written with the edit, as when a change is made (see
		// restore.go). Each record is looked at once, and the edit costs no more
		// than the file is large, so a file cannot keep the space from opening.
		paths, err := editSpaceYAML(h, root, "", edit, nil, nil)
		written = append(written, paths...)
		if _, refused := settingsReason(err); !refused {
			if err != nil {
				slog.Warn("recovery: space.yml could not be written; the settings of a folder that was moved or deleted may still be under its old name",
					"folder", r.From+r.Path, "moved_to", r.To, "error", err)
			}
			continue
		}
		// The online path refuses such a change and moves the folder back; do
		// the same, or the folder's type and views stay under a key that a new
		// folder of that name inherits.
		if r.Op == "b" {
			from := filepath.Join(root, filepath.FromSlash(r.From))
			rerr := os.Rename(filepath.Join(root, filepath.FromSlash(r.To)), from)
			if rerr == nil {
				slog.Error("recovery: the settings of a moved folder do not fit in space.yml; the folder was moved back",
					"folder", r.From, "moved_to", r.To, "error", err)
				continue
			}
			err = errors.Join(err, rerr)
		}
		slog.Error("recovery: the settings of a folder in space.yml were not moved or removed",
			"folder", r.From+r.Path, "moved_to", r.To, "error", err)
	}
	return written
}
