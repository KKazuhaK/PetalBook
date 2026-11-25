package space

import (
	"crypto/sha1"
	"encoding/hex"
	"io/fs"
	"sort"
	"strconv"

	"petalbook.app/server/internal/index"
)

// The change feed of the space (milestone 4.1): where it is, what changed
// after a cursor, and the catalogue of the pages. All of it is the index's
// (see index/feed.go); no file of a page is read.

// Head is the change cursor the space is at now. It is a read of memory.
func (s *Space) Head() string { return s.idx.Head() }

// TreeStamp changes whenever a folder is made, moved or removed, or the number
// of pages in a folder changes.
func (s *Space) TreeStamp() string { return s.idx.TreeStamp() }

// Catalog is a batch of the catalogue of the pages in folders (all when none).
func (s *Space) Catalog(folders []string, cursor string, limit int) (index.Batch, error) {
	return s.idx.Catalog(folders, cursor, limit)
}

// Changes is what changed after the change cursor since.
func (s *Space) Changes(since string, limit int) (index.ChangeList, error) {
	return s.idx.Changes(since, limit)
}

// SettingsStamp changes whenever a file the space reads as settings changes:
// space.yml, tags.yml, the types and the templates. It looks at the names,
// sizes and times of the files of those three folders (one listing each, as
// many entries as the space has settings: not as many as it has pages).
//
// An evicted space has no files: it answers the stamp it had when it was last
// closed with them ("" if it was evicted before this was kept).
func (s *Space) SettingsStamp() string {
	if s.evicted {
		return s.settingsNote
	}
	h := sha1.New()
	for _, dir := range []string{".petalbook", typesDir, templatesDir} {
		h.Write([]byte(dir + "\x00"))
		ents, err := readDirIn(s.root, dir)
		if err != nil {
			continue
		}
		type stamp struct {
			name       string
			size, time int64
		}
		var l []stamp
		for _, e := range ents {
			if e.IsDir() || dir+"/"+e.Name() == PrefsFile {
				// The person's own preferences are no setting of the space: writing them must not
				// make every device read the settings again.
				continue
			}
			fi, err := e.Info()
			if err != nil || fi.Mode()&fs.ModeType != 0 && fi.Mode()&fs.ModeSymlink == 0 {
				continue
			}
			l = append(l, stamp{e.Name(), fi.Size(), fi.ModTime().UnixNano()})
		}
		sort.Slice(l, func(i, j int) bool { return l[i].name < l[j].name })
		for _, f := range l {
			h.Write([]byte(f.name + "\x00" + strconv.FormatInt(f.size, 10) + "\x00" + strconv.FormatInt(f.time, 10) + "\x00"))
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:6])
}
