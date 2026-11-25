package space

// The attachment repository (milestone 3.1, docs/api.md "Attachment
// repository") from the space's side: where the files of assets/ come from and
// go to when the space keeps them in a second repository. The repository
// itself is internal/assetrepo; the space only knows this interface, which the
// spaces manager gives it.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"petalbook.app/server/internal/gitstore"
)

// AssetInfo is what is known of a file of the attachment repository: the id of
// its contents (Git's) and its size, -1 when that is not known.
type AssetInfo struct {
	ID   string
	Size int64
}

// AssetFile is a file to put in the attachment repository.
type AssetFile struct {
	Rel  string // below assets/: <page id>/<name>
	Data []byte
}

// AssetBackend is a space's attachment repository.
type AssetBackend interface {
	// Lookup finds a file in the table of the repository's current files, in
	// memory. rel is below assets/.
	Lookup(rel string) (AssetInfo, bool)
	// Open opens the cached contents, fetching them first when they are not
	// there; *NotCachedError when they cannot be had now.
	Open(ctx context.Context, rel string) (*os.File, AssetInfo, error)
	// Direct reports whether new uploads go to the repository (they do not
	// while the attachments of the space are being copied into it).
	Direct() bool
	// Put commits and pushes the files in one commit before returning;
	// *UnavailableError when the repository cannot be reached, ErrExists when a
	// path has other contents already.
	Put(ctx context.Context, files []AssetFile, author, committer gitstore.Identity) error
}

// SetAssets gives the space its attachment repository (nil takes it away).
// The files already in assets/ stay where they are and are read from there
// until a move takes them out.
func (s *Space) SetAssets(b AssetBackend) {
	if b == nil {
		s.assets.Store(nil)
		return
	}
	s.assets.Store(&b)
	// What was missing for want of the table may be there now.
}

// Assets is the attachment repository, nil when the space has none.
func (s *Space) Assets() AssetBackend {
	if p := s.assets.Load(); p != nil {
		return *p
	}
	return nil
}

// assetExists is how checks find out that an attachment exists: the table of
// the repository first, then the files of the space.
func (s *Space) assetExists(rel string) bool {
	if b := s.Assets(); b != nil {
		if below, ok := strings.CutPrefix(rel, AssetsDir+"/"); ok {
			if _, ok := b.Lookup(below); ok {
				return true
			}
		}
	}
	fi, err := os.Lstat(filepath.Join(s.root, filepath.FromSlash(rel)))
	return err == nil && fi.Mode().IsRegular()
}

// AssetsAppeared tells the index that these files (below assets/) are in the
// repository now, so the checks that said they were missing are dropped.
func (s *Space) AssetsAppeared(rels []string) {
	for _, r := range rels {
		if err := s.idx.AssetAppeared(path.Join(AssetsDir, r)); err != nil {
			s.log.Warn("could not update the missing attachments", "error", err)
			return
		}
	}
}

// pendingAsset is an upload that goes to the attachment repository when the
// change that made it is about to be committed.
type pendingAsset struct {
	rel  string
	data []byte
}

// pushAssets puts the attachments a change made into the attachment
// repository, before the change's own commit is made: a page committed after
// it never refers to an attachment that is not on its remote.
func (s *Space) pushAssets(ctx context.Context, tx *txn) error {
	if len(tx.assets) == 0 {
		return nil
	}
	b := s.Assets()
	if b == nil {
		return errors.New("the attachment repository is gone")
	}
	files := make([]AssetFile, len(tx.assets))
	rels := make([]string, len(tx.assets))
	for i, a := range tx.assets {
		files[i] = AssetFile{Rel: a.rel, Data: a.data}
		rels[i] = a.rel
	}
	author, committer := s.cm.git.identities(tx.by)
	if err := b.Put(ctx, files, author, committer); err != nil {
		var ue *UnavailableError
		if !errors.As(err, &ue) && gitstore.KindOf(err) != gitstore.KindOther {
			err = &UnavailableError{Msg: "The attachment repository: " + gitstore.Scrub(err.Error()) + ".", Err: err}
		}
		return err
	}
	tx.assets = nil
	s.AssetsAppeared(rels)
	return nil
}

// putAssetRepo is putAssetLocked for a space whose uploads go to the
// attachment repository: nothing is written in the work tree.
func (s *Space) putAssetRepo(tx *txn, b AssetBackend, id, name string, data []byte) (Asset, bool, error) {
	rel := path.Join(id, name)
	full := path.Join(AssetsDir, rel)
	out := Asset{Rel: full, Name: name, Size: int64(len(data))}
	if info, ok := b.Lookup(rel); ok {
		if info.ID != gitstore.BlobID(data) {
			return Asset{}, true, ErrExists
		}
		return out, true, nil
	}
	// A file of the same name in the space's own assets/ (from before the
	// attachments moved, or while they are being moved).
	if fi, err := os.Lstat(filepath.Join(s.root, filepath.FromSlash(full))); err == nil {
		if same, err := sameAsset(filepath.Join(s.root, filepath.FromSlash(full)), data); err != nil {
			return Asset{}, true, err
		} else if same && fi.Mode().IsRegular() {
			return out, true, nil
		}
	}
	for _, p := range tx.assets {
		if p.rel == rel {
			return out, true, nil
		}
	}
	tx.assets = append(tx.assets, pendingAsset{rel: rel, data: data})
	out.Created = true
	return out, true, nil
}

// repoAsset finds the file at rel (a path of the space) in the attachment
// repository: below is its path there, and ok is false when the file is not
// read from the repository (no repository, a path outside assets/, a folder in
// the way, or not in the table). direct is false while the attachments are
// being copied, when the space's own file is read first.
func (s *Space) repoAsset(rel string) (b AssetBackend, below string, info AssetInfo, direct, ok bool) {
	b = s.Assets()
	if b == nil || rel == "" || !utf8.ValidString(rel) || len(rel) > 4096 {
		return nil, "", AssetInfo{}, false, false
	}
	below, found := strings.CutPrefix(rel, AssetsDir+"/")
	if !found || below == "" || !s.assetsDirIsOurs() {
		return nil, "", AssetInfo{}, false, false
	}
	for _, seg := range strings.Split(below, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") || strings.ContainsRune(seg, 0) || strings.ContainsRune(seg, filepath.Separator) {
			return nil, "", AssetInfo{}, false, false
		}
	}
	info, found = b.Lookup(below)
	return b, below, info, b.Direct(), found
}

// AssetTag is the entity tag OpenFileTag would give the file at rel, from the
// table in memory: no network and no cache. "" when the file is not read from
// the attachment repository (or, while a move copies, may be read from the
// space's own folder).
func (s *Space) AssetTag(rel string) string {
	_, _, info, direct, ok := s.repoAsset(rel)
	if !ok || !direct {
		return ""
	}
	return info.ID
}

// OpenFileTag is OpenFile with a context and the entity tag of a file that
// comes from the attachment repository (its content id), "" for the files of
// the work tree. For a file that is in the repository but not in the cache,
// the contents are fetched first: *NotCachedError when that cannot be done now.
func (s *Space) OpenFileTag(ctx context.Context, rel string) (*os.File, fs.FileInfo, string, error) {
	b, below, _, direct, ok := s.repoAsset(rel)
	if !ok {
		f, st, err := s.OpenFile(rel)
		return f, st, "", err
	}
	if !direct {
		// While the attachments are being copied, the space's own files
		// are the ones to read: no network.
		if f, st, err := s.OpenFile(rel); err == nil {
			return f, st, "", nil
		}
	}
	f, info, err := b.Open(ctx, below)
	if err != nil {
		return nil, nil, "", err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, "", err
	}
	return f, st, info.ID, nil
}
