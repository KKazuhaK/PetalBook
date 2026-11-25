package space

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
)

// RenameTypeField gives a field of a type another key: in the type's file and,
// in the same change, in the header of every page that has the type and a
// value under the old key (the type of a page is its header's, else that of its
// folder). The pages keep the place of the field in their header, and
// everything else in the file. It returns the type as it is now and the number
// of pages rewritten.
//
// A page that holds the old key with no value (null, blank text, an empty
// list) is not one that uses the field, and is left as it is.
func (s *Space) RenameTypeField(by Author, typeName, baseRev, from, to string) (TypeInfo, int, error) {
	if err := ValidTypeName(typeName); err != nil {
		return TypeInfo{}, 0, err
	}
	if from == to {
		return TypeInfo{}, 0, invalidf("The field already has that key.")
	}
	if err := validFieldKey(to); err != nil {
		return TypeInfo{}, 0, err
	}
	type result struct {
		info  TypeInfo
		pages int
	}
	res, err := mutate(s, by, func(tx *txn) (result, error) {
		data, fromRestored, err := s.typeNow(typeName)
		switch {
		case errors.Is(err, errTypeFile):
			return result{}, typeFileProblem(typeName)
		case err != nil:
			return result{}, err
		case data == nil:
			return result{}, ErrNotFound
		}
		if baseRev != page.Rev(data) {
			return result{}, &TypeConflictError{typeNow(typeName, data, nil)}
		}
		_, top, perr := parseType(typeName, data)
		if perr != nil {
			return result{}, typeFileProblem(typeName)
		}
		raw, _ := lookup(top, "properties")
		var props []kv
		if raw != nil {
			props, _ = decodeObject(raw)
		}
		if _, i := lookup(props, from); i < 0 {
			return result{}, ErrNotFound
		}
		if _, i := lookup(props, to); i >= 0 {
			return result{}, invalidf("The type already has a field %q.", to)
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
			return result{}, err
		}
		rel := typesDir + "/" + typeName + ".json"
		if err := by.checkFileSize(rel, int64(len(out)), int64(len(data))); err != nil {
			return result{}, err
		}

		// The pages that have the field with a value, whose type is this one.
		resolver := s.TypeResolver()
		var edits []fileEdit
		var after string
		for {
			r, err := s.idx.List(index.Query{Filters: []index.Filter{{Key: from, Op: "notEmpty"}}, Limit: 200, Cursor: after})
			if err != nil {
				return result{}, err
			}
			for _, it := range r.Items {
				if resolver.For(it.Folder, it.Type) != typeName {
					continue
				}
				pdata, _, err := s.readFile(it.Path)
				if err != nil {
					return result{}, err
				}
				doc := page.Parse(pdata)
				meta := doc.Meta().Clone()
				if doc.Meta().ID() != it.ID {
					continue // the index is behind: that file is not this page
				}
				i := meta.Index(from)
				if i < 0 || meta.Index(to) >= 0 {
					continue
				}
				meta[i].Key = to
				if err := doc.SetMeta(meta); err != nil {
					return result{}, invalidf("%v.", err)
				}
				if nd := doc.Bytes(); !bytes.Equal(nd, pdata) {
					edits = append(edits, fileEdit{rel: it.Path, data: nd})
				}
			}
			if r.Next == "" {
				break
			}
			after = r.Next
		}

		moved, err := s.makeTypeDir(tx, rel)
		if err != nil {
			return result{}, err
		}
		done := func() {
			if s.heal != nil && fromRestored != nil {
				s.heal.repaired(typeSettings(typeName), &repair{fromRestored, nil}, moved)
			}
		}
		if moved == "" {
			if done, err = s.asideType(tx, typeName, fromRestored, nil); err != nil {
				return result{}, err
			}
		}
		if err := tx.record(jrec{Op: "t", Path: rel}); err != nil {
			return result{}, err
		}
		tx.touch(rel)
		s.typesDirty.Store(true)
		if err := writeIn(s.root, rel, out); err != nil {
			return result{}, err
		}
		s.heal.wroteGood(typeSettings(typeName), out)
		done()
		paths, err := s.writeEdits(tx, edits)
		if err != nil {
			return result{}, err
		}
		if len(paths) > 0 {
			if _, err := s.idx.Refresh(s.src, paths, nil); err != nil {
				return result{}, err
			}
		}
		t, _, err := parseType(typeName, out)
		t.Rev = page.Rev(out)
		return result{t, len(edits)}, err
	})
	return res.info, res.pages, err
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
