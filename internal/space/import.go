package space

import (
	"path"
	"strings"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// ImportAsset is a file an imported page refers to. Token stands for its
// address in the page's body.
type ImportAsset struct {
	Token string
	Name  string
	Data  []byte
}

// ImportPage is a page to create from outside (see internal/importer).
type ImportPage struct {
	// Folder is relative to the folder of the import.
	Folder string
	// Meta is the header the page came with, without an id; a `created` in it
	// is kept.
	Meta   page.Meta
	Body   string
	Assets []ImportAsset
}

// Import creates pages and their attachments in folder as one change: one
// commit, and in a space that follows a remote one push, or nothing at all.
// Nothing that exists is replaced. It returns how many pages and files it
// made.
func (s *Space) Import(folder string, items []ImportPage) (pages, files int, err error) {
	if err := checkRel(folder); err != nil {
		return 0, 0, err
	}
	// Everything that can be refused is refused before anything is made.
	exists := map[string]bool{}
	for _, it := range items {
		dir := path.Join(folder, it.Folder)
		if err := checkRel(dir); err != nil {
			return 0, 0, err
		}
		if !exists[dir] {
			// A folder that is already there is used whatever its name.
			if ok := s.src.DirExists(dir); !ok && dir != "" {
				if err := ValidNewFolder(dir); err != nil {
					return 0, 0, err
				}
			}
			exists[dir] = true
		}
		if err := checkBody(it.Body); err != nil {
			return 0, 0, err
		}
	}
	type made struct{ pages, files int }
	m, err := mutate(s, func(tx *txn) (made, error) {
		var m made
		for _, it := range items {
			in := CreateInput{Folder: path.Join(folder, it.Folder)}
			var meta page.Meta
			for _, f := range it.Meta {
				switch f.Key {
				case "id":
				case "created":
					in.created = strings.TrimSpace(page.Meta{f}.String("created"))
				default:
					meta = append(meta, f)
				}
			}
			// The stored name of a file depends on its content alone, so the
			// addresses can be written before the files are.
			names := make([]string, len(it.Assets))
			for i, a := range it.Assets {
				names[i] = AssetName(a.Name, a.Data)
			}
			body := it.Body
			assets := it.Assets
			in.bodyFor = func(id string) string {
				out := body
				for i, a := range assets {
					out = strings.ReplaceAll(out, a.Token, "/"+path.Join(AssetsDir, id, names[i]))
				}
				return out
			}
			p, err := s.createLocked(tx, in, newMeta(meta))
			if err != nil {
				return m, err
			}
			m.pages++
			for i, a := range it.Assets {
				stored, err := s.putAssetLocked(tx, p.ID, names[i], a.Data)
				if err != nil {
					return m, err
				}
				if stored.Created {
					m.files++
				}
			}
		}
		return m, nil
	})
	return m.pages, m.files, err
}
