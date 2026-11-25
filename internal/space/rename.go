package space

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
)

// FieldRename is what RenameTypeField did: the type as it is now, the number
// of pages rewritten, and the pages of the type it left as they were.
type FieldRename struct {
	Type    TypeInfo
	Pages   int
	Skipped []SkippedPage
}

// SkippedPage is a page of the type that a field rename did not rewrite, and
// why: "has_key" when its header already has a value under the new key (both
// are kept, as they are), "unreadable" when its header cannot be read.
type SkippedPage struct {
	ID     string `json:"id,omitempty"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// headerProblems are the problems of a file whose header cannot be read.
var headerProblems = map[string]bool{"unterminated_header": true, "bad_yaml": true, "header_not_mapping": true, "no_id": true, "bad_id": true}

// RenameTypeField gives a field of a type another key: in the type's file and,
// in the same change, in the header of every page that has the type and a
// value under the old key (the type of a page is its header's, else that of its
// folder), and in the saved views of the folders of the type (sort, group,
// filters, hidden columns, the fields of a card). The pages keep the place of
// the field in their header, and everything else in the file.
//
// The change is journaled whole before anything is written (an "f" record),
// and the type's file is written first: a crash before it leaves everything as
// it was, and one after it has the next start finish the pages and the views
// (see recoverFieldRename).
//
// A page that holds the old key with no value (null, blank text, an empty
// list) is not one that uses the field, and is left as it is. One that already
// has the new key, or whose header cannot be read, is left as it is and
// reported in Skipped.
func (s *Space) RenameTypeField(by Author, typeName, baseRev, from, to string) (FieldRename, error) {
	if err := ValidTypeName(typeName); err != nil {
		return FieldRename{}, err
	}
	if from == to {
		return FieldRename{}, invalidf("The field already has that key.")
	}
	if err := validFieldKey(to); err != nil {
		return FieldRename{}, err
	}
	return mutate(s, by, func(tx *txn) (FieldRename, error) {
		data, fromRestored, err := s.typeNow(typeName)
		switch {
		case errors.Is(err, errTypeFile):
			return FieldRename{}, typeFileProblem(typeName)
		case err != nil:
			return FieldRename{}, err
		case data == nil:
			return FieldRename{}, ErrNotFound
		}
		if baseRev != page.Rev(data) {
			return FieldRename{}, &TypeConflictError{typeNow(typeName, data, nil)}
		}
		_, top, perr := parseType(typeName, data)
		if perr != nil {
			return FieldRename{}, typeFileProblem(typeName)
		}
		raw, _ := lookup(top, "properties")
		var props []kv
		if raw != nil {
			props, _ = decodeObject(raw)
		}
		if _, i := lookup(props, from); i < 0 {
			return FieldRename{}, ErrNotFound
		}
		if _, i := lookup(props, to); i >= 0 {
			return FieldRename{}, invalidf("The type already has a field %q.", to)
		}
		for i := range props {
			if props[i].key == from {
				props[i].key = to
			}
		}
		top = set(top, "properties", encodeObject(props))
		if req, _ := lookup(top, "required"); req != nil {
			if names, ok := stringList(req); ok {
				for i := range names {
					if names[i] == from {
						names[i] = to
					}
				}
				top = set(top, "required", jsonText(names))
			}
		}
		out, err := indentJSON(top)
		if err != nil {
			return FieldRename{}, err
		}
		rel := typesDir + "/" + typeName + ".json"
		if err := by.checkFileSize(rel, int64(len(out)), int64(len(data))); err != nil {
			return FieldRename{}, err
		}

		// Everything the change writes is planned before anything is.
		resolver := s.TypeResolver()
		var res FieldRename
		var edits []fileEdit
		var after string
		for {
			r, err := s.idx.List(index.Query{Filters: []index.Filter{{Key: from, Op: "notEmpty"}}, Limit: 200, Cursor: after})
			if err != nil {
				return FieldRename{}, err
			}
			for _, it := range r.Items {
				if resolver.For(it.Folder, it.Type) != typeName {
					continue
				}
				pdata, _, err := s.readFile(it.Path)
				if err != nil {
					return FieldRename{}, err
				}
				doc := page.Parse(pdata)
				if len(doc.Problems()) > 0 {
					res.Skipped = append(res.Skipped, SkippedPage{ID: it.ID, Path: it.Path, Reason: "unreadable"})
					continue
				}
				if doc.Meta().ID() != it.ID {
					continue // the index is behind: that file is not this page
				}
				nd, ok, err := renameKey(doc, from, to)
				switch {
				case err != nil:
					return FieldRename{}, invalidf("The header of %s cannot be written with the new key: %v.", it.Path, err)
				case !ok:
					if doc.Meta().Index(from) >= 0 {
						res.Skipped = append(res.Skipped, SkippedPage{ID: it.ID, Path: it.Path, Reason: "has_key"})
					}
				case !bytes.Equal(nd, pdata):
					edits = append(edits, fileEdit{rel: it.Path, old: page.Rev(pdata), data: nd})
				}
			}
			if r.Next == "" {
				break
			}
			after = r.Next
		}
		// The files of the type's folders that are no pages because their
		// header cannot be read: whether they have the field is not known.
		probs, err := s.idx.Problems()
		if err != nil {
			return FieldRename{}, err
		}
		for _, p := range probs {
			if headerProblems[p.Code] && resolver.For(folderOf(p.Path), "") == typeName {
				res.Skipped = append(res.Skipped, SkippedPage{Path: p.Path, Reason: "unreadable"})
			}
		}
		_, views, err := planSpaceYAML(s.root, s.name, func(root *yaml.Node) (bool, error) {
			return renameFieldInViews(root, typeName, from, to)
		})
		if err != nil {
			return FieldRename{}, err
		}

		pages := make([]string, len(edits))
		recs := []jrec{{Op: "t", Path: rel}}
		for i, e := range edits {
			pages[i] = e.rel
			recs = append(recs, jrec{Op: "t", Path: e.rel})
		}
		if views {
			recs = append(recs, jrec{Op: "t", Path: spaceFile})
		}
		f := jrec{Op: "f", Path: rel, From: from, To: to, Rev: page.Rev(out), Pages: pages}
		if err := tx.record(append(recs, f)...); err != nil {
			return FieldRename{}, err
		}
		tx.bind = append(tx.bind, f)
		// Until the type's file is written nothing is: a failure there leaves
		// nothing to finish.
		notMade := func(err error) (FieldRename, error) {
			tx.settleBindings()
			return FieldRename{}, err
		}

		moved, err := s.makeTypeDir(tx, rel)
		if err != nil {
			return notMade(err)
		}
		done := func() {
			if s.heal != nil && fromRestored != nil {
				s.heal.repaired(typeSettings(typeName), &repair{fromRestored, nil}, moved)
			}
		}
		if moved == "" {
			if done, err = s.asideType(tx, typeName, fromRestored, nil); err != nil {
				return notMade(err)
			}
		}
		tx.touch(rel)
		s.typesDirty.Store(true)
		if err := writeIn(s.root, rel, out); err != nil {
			return notMade(err)
		}
		s.heal.wroteGood(typeSettings(typeName), out)
		done()
		// From here on the change is made: what is left of it is finished at
		// the next start should this stop part way (the "f" record stays open).
		paths, err := s.writeEdits(tx, edits)
		if err != nil {
			return FieldRename{}, err
		}
		if views {
			if err := s.editSpaceFile(tx, func(root *yaml.Node) (bool, error) {
				return renameFieldInViews(root, typeName, from, to)
			}); err != nil {
				return FieldRename{}, err
			}
		}
		tx.settleBindings()
		if len(paths) > 0 {
			if _, err := s.idx.Refresh(s.src, paths, nil); err != nil {
				return FieldRename{}, err
			}
		}
		sort.Slice(res.Skipped, func(i, j int) bool { return res.Skipped[i].Path < res.Skipped[j].Path })
		t, _, err := parseType(typeName, out)
		t.Rev = page.Rev(out)
		res.Type, res.Pages = t, len(edits)
		return res, err
	})
}

// setHeader writes a header into a page (a variable for the tests).
var setHeader = (*page.Doc).SetMeta

// renameKey gives the field from of a page's header the key to, in its place.
// ok is false when the header has no value under from, or has to already.
func renameKey(doc *page.Doc, from, to string) (data []byte, ok bool, err error) {
	meta := doc.Meta().Clone()
	i := meta.Index(from)
	if i < 0 || meta.Index(to) >= 0 {
		return nil, false, nil
	}
	meta[i].Key = to
	if err := setHeader(doc, meta); err != nil {
		return nil, false, err
	}
	return doc.Bytes(), true, nil
}

// renameFieldInViews gives the key from the name to in the saved views of the
// folders whose pages have the type typeName (by the bindings of the same
// settings): their sort (`prop:from`), the field a board is grouped by, the
// keys of their filters, their hidden columns and the fields of their cards.
func renameFieldInViews(root *yaml.Node, typeName, from, to string) (bool, error) {
	folders, err := foldersNode(root, false)
	if err != nil || folders == nil {
		return false, err
	}
	byFolder := map[string]string{}
	for _, b := range bindingsOf(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}) {
		byFolder[b.Folder] = b.Type
	}
	resolver := TypeResolver{byFolder}
	changed := false
	for i := 0; i+1 < len(folders.Content); i += 2 {
		key, ok := folderAt(folders, i)
		if !ok || resolver.For(key, "") != typeName {
			continue
		}
		seq, at := viewSlots(deref(folders.Content[i+1]))
		need := false
		for _, j := range at {
			if _, ch := renamedView(viewOf(deref(seq.Content[j])), from, to); ch {
				need = true
			}
		}
		if !need {
			continue
		}
		entry := own(folders, i+1)
		_, vi := mapValue(entry, "views")
		if vi < 0 {
			continue
		}
		list := own(entry, vi+1)
		for _, j := range at {
			n := own(list, j)
			if v, ch := renamedView(viewOf(n), from, to); ch {
				applyView(n, v)
				changed = true
			}
		}
	}
	return changed, nil
}

// renamedView is v with the key from named to wherever it names a field.
func renamedView(v View, from, to string) (View, bool) {
	changed := false
	swap := func(s string) string {
		if s == from {
			changed = true
			return to
		}
		return s
	}
	if rest, desc := strings.CutPrefix(v.Sort, "-"); rest == "prop:"+from {
		v.Sort = "prop:" + to
		if desc {
			v.Sort = "-" + v.Sort
		}
		changed = true
	}
	v.Group = swap(v.Group)
	if len(v.Filter) > 0 {
		fs := make([]ViewFilter, len(v.Filter))
		for i, f := range v.Filter {
			f.Key = swap(f.Key)
			fs[i] = f
		}
		v.Filter = fs
	}
	if len(v.Hidden) > 0 {
		hs := make([]string, len(v.Hidden))
		for i, h := range v.Hidden {
			hs[i] = swap(h)
		}
		v.Hidden = hs
	}
	if cf, ok := v.Extra["cardFields"].([]any); ok {
		out := make([]any, len(cf))
		for i, x := range cf {
			if s, ok := x.(string); ok {
				out[i] = swap(s)
			} else {
				out[i] = x
			}
		}
		extra := make(map[string]any, len(v.Extra))
		for k, x := range v.Extra {
			extra[k] = x
		}
		extra["cardFields"] = out
		v.Extra = extra
	}
	return v, changed
}

// recoverFieldRename finishes a field rename that a crash stopped part way:
// when the type's file has the content the rename wrote, the pages it named
// that still have the old key (and not the new) get the new one, and so do
// the views of the folders of the type. When it has not, nothing was written
// (the type's file is written first) and nothing is done. It returns the files
// it wrote, and the type's file and the pages, for the commit.
func recoverFieldRename(h *healer, root string, r jrec) []string {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(r.Path)))
	if err != nil || page.Rev(data) != r.Rev {
		return nil
	}
	typeName := strings.TrimSuffix(path.Base(r.Path), ".json")
	out := []string{r.Path}
	for _, p := range r.Pages {
		if !fs.ValidPath(p) || strings.HasPrefix(p, ".git/") || strings.HasPrefix(p, ".petalbook/") {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(p))
		pdata, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		doc := page.Parse(pdata)
		if len(doc.Problems()) > 0 {
			continue
		}
		nd, ok, err := renameKey(doc, r.From, r.To)
		if err != nil || !ok {
			continue
		}
		if err := writeIn(root, p, nd); err != nil {
			slog.Warn("recovery: a page of a field rename could not be written", "path", p, "error", err)
			continue
		}
		out = append(out, p)
	}
	written, err := editSpaceYAML(h, root, "", func(top *yaml.Node) (bool, error) {
		return renameFieldInViews(top, typeName, r.From, r.To)
	}, nil, nil)
	if err != nil {
		slog.Warn("recovery: the views of a field rename could not be written", "type", typeName, "error", err)
	}
	return append(out, written...)
}

// indentJSON writes a type's top-level object as the files of types are written.
func indentJSON(top []kv) ([]byte, error) {
	var out bytes.Buffer
	if err := json.Indent(&out, encodeObject(top), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}
