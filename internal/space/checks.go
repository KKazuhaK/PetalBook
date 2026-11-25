package space

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
)

// The check page (docs/api.md, "Checks"). What is wrong with a page is
// recorded by the index when the page is indexed (index/checks.go); this file
// tells the index what the types say and what files exist, and makes the two
// fixes.

// FixError is a refusal with a code of its own: not_fixable.
type FixError struct{ Code, Msg string }

func (e *FixError) Error() string { return e.Msg }

func notFixable(kind string) error {
	return &FixError{"not_fixable", "problems of the kind " + quote(kind) + " cannot be fixed here; open the page and fix it by hand"}
}

// Checks lists the problems of the space (see index.Checks).
func (s *Space) Checks(kind, cursor string) (map[string]int, []index.Check, string, error) {
	return s.idx.Checks(kind, cursor)
}

// CheckCounts is how many problems of each kind the space has (see index.CheckCounts).
func (s *Space) CheckCounts() (map[string]int, error) { return s.idx.CheckCounts() }

// PageIDByPath is the id of the page whose file is at rel, from the index.
func (s *Space) PageIDByPath(rel string) (string, bool) { return s.idx.IDByPath(rel) }

// watchChecks tells the index how to find out whether an attachment exists and
// what the types say, before it looks at any file.
func (s *Space) watchChecks() {
	s.idx.SetAssetExists(s.assetExists)
	if r, err := s.typeRules(); err == nil {
		s.idx.UseTypes(r)
	}
}

// syncTypes makes the type problems of the pages those of the types and
// bindings as they are now. It does nothing when they are what the problems on
// record were made with. A failure is logged: the check page is only as good as
// the last time it worked.
func (s *Space) syncTypes() {
	r, err := s.typeRules()
	if err == nil {
		err = s.idx.ApplyTypes(r)
	}
	if err != nil {
		s.log.Warn("could not check the pages against their types", "error", err)
	}
}

// typeRules is what the types of the space say, for the index. A type whose
// file cannot be used gives no rule (its pages are not judged by it).
func (s *Space) typeRules() (*index.TypeRules, error) {
	types, bindings, err := s.Types()
	if err != nil {
		return nil, err
	}
	rules := map[string]*index.TypeRule{}
	h := sha1.New()
	for _, t := range types {
		h.Write([]byte("t\x00" + t.Name + "\x00" + t.Rev + "\x00" + t.Error + "\n"))
		if t.Error == "" {
			rules[t.Name] = ruleOf(t)
		}
	}
	bs := make([]string, len(bindings))
	for i, b := range bindings {
		bs[i] = b.Folder + "\x00" + b.Type
	}
	sort.Strings(bs)
	h.Write([]byte("b\x00" + strings.Join(bs, "\n")))
	res := s.TypeResolver()
	return &index.TypeRules{
		Fingerprint: hex.EncodeToString(h.Sum(nil)),
		For: func(folder, headerType string) *index.TypeRule {
			if t := res.For(folder, headerType); t != "" {
				return rules[t]
			}
			return nil
		},
	}, nil
}

func ruleOf(t TypeInfo) *index.TypeRule {
	r := &index.TypeRule{Fields: make(map[string]index.FieldRule, len(t.Fields))}
	for _, f := range t.Fields {
		r.Fields[f.Key] = index.FieldRule{Kind: f.Kind, Options: f.Options}
	}
	return r
}

// FixNotInType removes from the page the header fields its type does not have,
// and only those: the rest of the header and the body stay as the file has
// them. baseRev is the rev the person saw, as for a save. The fields are those
// of the page as it is now, so a type that has gained a field since takes
// nothing out; a page that has none to remove is returned as it is.
func (s *Space) FixNotInType(by Author, id, baseRev string) (*Page, error) {
	return s.patchProps(by, id, baseRev, nil, nil, func(rel string, doc *page.Doc) []string {
		t := s.TypeResolver().For(folderOf(rel), doc.Meta().Type())
		if t == "" {
			return nil
		}
		info, ok := s.typeInfoCached(t)
		if !ok {
			return nil
		}
		return index.StrayFields(doc, ruleOf(info))
	})
}

// FixNoID gives the page file at rel a new id, written at the start of its
// header; nothing else of the file changes. baseRev is the rev of the file the
// person saw (a conflict carries the file as it is). A file that already has a
// page's id is returned as it is. A file whose header cannot be read is not
// fixable this way.
func (s *Space) FixNoID(by Author, rel, baseRev string) (*Page, error) {
	if err := checkRel(rel); err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(rel), ".md") {
		return nil, ErrNotFound
	}
	return mutate(s, by, func(tx *txn) (*Page, error) {
		abs, err := s.abs(rel)
		if err != nil {
			return nil, err
		}
		data, mtime, err := s.readFile(rel)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if baseRev != page.Rev(data) {
			return nil, &ConflictError{Current: s.pageFrom(rel, data, mtime)}
		}
		doc := page.Parse(data)
		ps := doc.Problems()
		if len(ps) == 0 {
			// It has an id: the problem is gone (or is another one).
			if s.idx.HasID(doc.Meta().ID()) {
				return s.pageFrom(rel, data, mtime), nil
			}
			return nil, notFixable(index.KindDuplicateID)
		}
		if ps[0].Code != "no_id" && ps[0].Code != "bad_id" {
			return nil, notFixable(index.KindBadHeader)
		}
		var id string
		for range 50 {
			id = page.NewID(s.now())
			if !s.idx.HasID(id) {
				break
			}
		}
		if s.idx.HasID(id) {
			return nil, unavailable(nil, "could not make an id that is not in use; try again")
		}
		meta := page.Meta{{Key: "id", Value: id}}
		for _, f := range doc.Meta() {
			if f.Key != "id" {
				meta = append(meta, f)
			}
		}
		if err := doc.SetMeta(meta); err != nil {
			return nil, invalidf("%v.", err)
		}
		newData := doc.Bytes()
		if bytes.Equal(newData, data) {
			return s.pageFrom(rel, data, mtime), nil
		}
		if err := tx.record(jrec{Op: "w", Path: rel, Rev: page.Rev(newData)}); err != nil {
			return nil, err
		}
		if err := writeAtomic(abs, newData); err != nil {
			return nil, err
		}
		return s.afterWrite(tx, rel, newData)
	})
}
