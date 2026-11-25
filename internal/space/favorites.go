package space

import (
	"path"
	"strings"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// What a person's favourites need of a space (the favourites themselves are in
// the state database, see store/favorites.go): whether a target is there, and
// what to call it. They are asked for the whole list at once: one query on the
// index for the pages, one for the folders, and the settings (read from the
// cache of their file) for the views; no page file is read.

// FavRef names what a favourite points at: kind page (target is the page id),
// folder (the path), or view (`folder:<path>#<view id>` or `tag:<tag>#<view id>`).
type FavRef struct{ Kind, Target string }

// FavLabel is what is known of a favourite's target now.
type FavLabel struct {
	// Name is what to show: a page's label, a folder's last name, a view's name.
	// For a target that is gone it is what can be told from the favourite alone
	// (the folder's name), "" for a page or a view.
	Name string
	// Missing says the page, folder or view is not there any more.
	Missing bool
	// Path is a page's path, and a folder's path.
	Path string
	// Of is the folder or the tag a view belongs to (a folder as `folder:<path>`
	// is not repeated: it is the path; a tag is the tag), with OfKind
	// "folder" or "tag".
	Of, OfKind string
}

// CanonicalFolder is the path of a folder as it is spelled on disk; ok is
// false when there is no such folder.
func (s *Space) CanonicalFolder(rel string) (canon string, ok bool, err error) {
	if err := checkRel(rel); err != nil {
		return "", false, err
	}
	canon, existed, err := s.resolveFolder(rel)
	if err != nil {
		return "", false, nil
	}
	return canon, existed, nil
}

// CheckFavorite validates a favourite a request gives and says it as it is
// stored: a folder spelled as on disk. A page, folder or view that is not there
// is ErrNotFound / ErrNoFolder; a target that cannot be one is invalid.
func (s *Space) CheckFavorite(ref FavRef) (FavRef, error) {
	switch ref.Kind {
	case "page":
		if !page.ValidID(ref.Target) {
			return ref, invalidf("That is not a page id.")
		}
		if _, ok, err := s.idx.Summary(ref.Target); err != nil {
			return ref, err
		} else if !ok {
			return ref, ErrNotFound
		}
		return ref, nil
	case "folder":
		if ref.Target == "" {
			return ref, invalidf("The root folder cannot be a favourite.")
		}
		canon, ok, err := s.CanonicalFolder(ref.Target)
		if err != nil {
			return ref, err
		}
		if !ok {
			return ref, ErrNoFolder
		}
		return FavRef{"folder", canon}, nil
	case "view":
		kind, scope, id, ok := splitViewTarget(ref.Target)
		if !ok {
			return ref, invalidf("A view is folder:<path>#<id> or tag:<tag>#<id>.")
		}
		if kind == "folder" {
			canon, existed, err := s.CanonicalFolder(scope)
			if err != nil {
				return ref, err
			}
			if !existed {
				return ref, ErrNoFolder
			}
			scope = canon
		} else if err := checkTagName(scope); err != nil {
			return ref, err
		}
		if _, ok := s.viewNamed(kind, scope, id); !ok {
			return ref, ErrNotFound
		}
		return FavRef{"view", kind + ":" + scope + "#" + id}, nil
	}
	return ref, invalidf("Kind must be page, folder or view.")
}

// splitViewTarget cuts `folder:<path>#<id>` or `tag:<tag>#<id>`.
func splitViewTarget(target string) (kind, scope, id string, ok bool) {
	for _, k := range []string{"folder", "tag"} {
		if rest, found := strings.CutPrefix(target, k+":"); found {
			i := strings.LastIndexByte(rest, '#')
			if i < 0 || i == len(rest)-1 {
				return "", "", "", false
			}
			return k, rest[:i], rest[i+1:], true
		}
	}
	return "", "", "", false
}

// viewNamed finds a view of a folder or a tag by its id.
func (s *Space) viewNamed(kind, scope, id string) (string, bool) {
	var vs []View
	if kind == "folder" {
		vs = s.config().views[bindingKey(scope)]
	} else {
		vs = s.tagViews()[scope]
	}
	for _, v := range vs {
		if v.ID == id {
			return v.Name, true
		}
	}
	return "", false
}

// FavoriteLabels says for each favourite what it points at now. The pages and
// the folders are looked up in the index all at once.
func (s *Space) FavoriteLabels(refs []FavRef) ([]FavLabel, error) {
	var ids, folders []string
	for _, r := range refs {
		switch r.Kind {
		case "page":
			ids = append(ids, r.Target)
		case "folder":
			folders = append(folders, r.Target)
		case "view":
			if kind, scope, _, ok := splitViewTarget(r.Target); ok && kind == "folder" {
				folders = append(folders, scope)
			}
		}
	}
	pages, err := s.idx.Summaries(ids)
	if err != nil {
		return nil, err
	}
	known, err := s.idx.FoldersKnown(folders)
	if err != nil {
		return nil, err
	}
	out := make([]FavLabel, len(refs))
	for i, r := range refs {
		switch r.Kind {
		case "page":
			if p, ok := pages[r.Target]; ok {
				out[i] = FavLabel{Name: p.Label, Path: p.Path}
			} else {
				out[i] = FavLabel{Missing: true}
			}
		case "folder":
			out[i] = FavLabel{Name: path.Base(r.Target), Path: r.Target, Missing: !known[r.Target]}
		case "view":
			kind, scope, id, ok := splitViewTarget(r.Target)
			if !ok {
				out[i] = FavLabel{Name: r.Target, Missing: true}
				continue
			}
			l := FavLabel{Of: scope, OfKind: kind}
			if kind == "folder" {
				l.Missing = !known[scope]
			}
			if name, found := s.viewNamed(kind, scope, id); found {
				l.Name = name
			} else {
				l.Missing = true
			}
			out[i] = l
		default:
			out[i] = FavLabel{Name: r.Target, Missing: true}
		}
	}
	return out, nil
}
