package space

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
)

// MaxBodies is the most pages a listing may be asked to give the body of: each
// is a file read.
const MaxBodies = 20

// typeCacheEntry is a parsed type file for as long as the file looks the same.
type typeCacheEntry struct {
	size    int64
	mtimeNs int64
	info    TypeInfo
}

// typeInfoCached returns the type name as its file says, ok false when there is
// no usable file. It parses again only when the file changed.
func (s *Space) typeInfoCached(name string) (TypeInfo, bool) {
	if ValidTypeName(name) != nil {
		return TypeInfo{}, false
	}
	abs := filepath.Join(s.root, filepath.FromSlash(typesDir), name+".json")
	fi, err := os.Lstat(abs)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > MaxTypeFile {
		return TypeInfo{}, false
	}
	s.cfgMu.Lock()
	e, ok := s.typeCache[name]
	s.cfgMu.Unlock()
	if ok && e.size == fi.Size() && e.mtimeNs == fi.ModTime().UnixNano() {
		return e.info, true
	}
	data, err := s.readTypeFile(name)
	if err != nil || data == nil {
		return TypeInfo{}, false
	}
	info, _, err := parseType(name, data)
	if err != nil {
		return TypeInfo{}, false
	}
	s.cfgMu.Lock()
	if s.typeCache == nil || len(s.typeCache) >= MaxTypes {
		s.typeCache = map[string]typeCacheEntry{}
	}
	s.typeCache[name] = typeCacheEntry{fi.Size(), fi.ModTime().UnixNano(), info}
	s.cfgMu.Unlock()
	return info, true
}

// optionsOf are the options of the field key of the type a folder has, when
// that is a select or a multiselect with options: the order a sort by that
// field follows. nil otherwise.
func (s *Space) optionsOf(folder, key string) []string {
	typ := s.TypeResolver().For(folder, "")
	if typ == "" {
		return nil
	}
	info, ok := s.typeInfoCached(typ)
	if !ok {
		return nil
	}
	for _, f := range info.Fields {
		if f.Key == key && (f.Kind == KindSelect || f.Kind == KindMultiSelect) {
			return uniqueOptions(f.Options)
		}
	}
	return nil
}

// propSortKey is the property a sort name sorts by ("prop:status",
// "-prop:status").
func propSortKey(sort string) (string, bool) {
	rest := strings.TrimPrefix(sort, "-")
	if !strings.HasPrefix(rest, "prop:") {
		return "", false
	}
	return strings.TrimPrefix(rest, "prop:"), true
}

// List queries the index. See index.Query. A sort by a property in a listing of
// a folder follows the order of the options when the field is a select (or a
// multiselect) in the type the folder has; the listing is then read from the
// index in groups, one per option, and never sorted as a whole.
func (s *Space) List(q index.Query) (index.Result, error) {
	if key, ok := propSortKey(q.Sort); ok && q.Folder != nil && q.PropOptions == nil {
		q.PropOptions = s.optionsOf(*q.Folder, key)
	}
	return s.idx.List(q)
}

// Groups counts the pages of a scope by the values of a property, from the
// index.
func (s *Space) Groups(q index.GroupQuery) (index.GroupResult, error) { return s.idx.Groups(q) }

// Bodies reads the Markdown body of each of the pages listed (at most
// MaxBodies): one file each. A page whose file is not that page any more (the
// index is behind) or cannot be read has an empty body.
func (s *Space) Bodies(items []index.Summary) ([]string, error) {
	if len(items) > MaxBodies {
		return nil, invalidf("the bodies of at most %d pages can be read at once", MaxBodies)
	}
	out := make([]string, len(items))
	for i, it := range items {
		data, _, err := s.readFile(it.Path)
		if err != nil {
			continue
		}
		doc := page.Parse(data)
		if doc.Meta().ID() == it.ID {
			out[i] = doc.Body
		}
	}
	return out, nil
}
