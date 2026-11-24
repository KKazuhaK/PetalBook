package space

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// AssetsDir is the folder at the root of a space that holds the files the
// server stores for pages, one directory per page id. It is never a folder of
// pages (see checkRel and ignoredDir).
const AssetsDir = "assets"

// maxAssetStem is how much of the name a file came with is kept.
const maxAssetStem = 80

// Asset is a stored file.
type Asset struct {
	// Rel is the path from the root of the space: assets/<page id>/<name>.
	Rel  string
	Name string
	Size int64
	// Created is false when the same bytes were already stored under the name.
	Created bool
}

// AssetName makes the name a file is stored under from the name it came with
// and its content: the last path segment, with what file systems refuse
// replaced, cut to a reasonable length; then the first 8 hexadecimal digits of
// the SHA-256 of the content; then the extension in lower case, kept only when
// it is 1 to 10 letters and digits.
func AssetName(given string, data []byte) string {
	base := strings.ToValidUTF8(given, "-")
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	stem, ext := base, ""
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		e := strings.ToLower(base[i+1:])
		ok := len(e) >= 1 && len(e) <= 10
		for _, c := range []byte(e) {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
				ok = false
			}
		}
		if ok {
			stem, ext = base[:i], "."+e
		}
	}
	stem = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"\|?*/`, r) {
			return '-'
		}
		return r
	}, stem)
	stem = strings.TrimLeft(stem, ". ")
	if utf8.RuneCountInString(stem) > maxAssetStem {
		stem = string([]rune(stem)[:maxAssetStem])
	}
	stem = strings.TrimRight(stem, ". ")
	if stem == "" {
		stem = "file"
	}
	sum := sha256.Sum256(data)
	return stem + "-" + hex.EncodeToString(sum[:4]) + ext
}

// PutAsset stores data as a file of page id, named after name (see AssetName),
// and returns where. The file is part of the space like a page is: journaled
// and committed in a local space; committed and pushed, or taken back, in one
// that follows a remote. Storing the same bytes under the same name again
// changes nothing. ErrNotFound when the page does not exist; ErrExists when
// another file already has the name.
func (s *Space) PutAsset(by Author, id, name string, data []byte) (Asset, error) {
	if !page.ValidID(id) {
		return Asset{}, ErrNotFound
	}
	if strings.TrimSpace(name) == "" {
		return Asset{}, invalidf("the file has no name")
	}
	if len(data) == 0 {
		return Asset{}, invalidf("the file is empty")
	}
	stored := AssetName(name, data)
	s.refresh(id)
	return mutate(s, by, func(tx *txn) (Asset, error) { return s.putAssetLocked(tx, id, stored, data) })
}

func (s *Space) putAssetLocked(tx *txn, id, name string, data []byte) (Asset, error) {
	if _, _, _, err := s.load(id, false); err != nil {
		return Asset{}, err
	}
	if !s.assetsDirIsOurs() {
		return Asset{}, invalidf("a folder whose name differs from %q only in case is in the way; attachments cannot be stored", AssetsDir)
	}
	if b := s.Assets(); b != nil && b.Direct() && tx.remote {
		a, _, err := s.putAssetRepo(tx, b, id, name, data)
		return a, err
	}
	dirRel := path.Join(AssetsDir, id)
	dir, err := s.assetDir(tx, dirRel)
	if err != nil {
		return Asset{}, err
	}
	rel := path.Join(dirRel, name)
	target := filepath.Join(dir, name)
	out := Asset{Rel: rel, Name: name, Size: int64(len(data))}

	same, err := sameAsset(target, data)
	if err != nil {
		return Asset{}, err
	}
	if !same {
		// Journal the name before taking it; recovery only believes this record
		// if the file has our content.
		if err := tx.record(jrec{Op: "w", Path: rel, Rev: page.Rev(data)}); err != nil {
			return Asset{}, err
		}
		tmp, err := newTempIn(dir, data, 0o644)
		if err != nil {
			return Asset{}, err
		}
		defer os.Remove(tmp)
		if err := s.ops.Link(tmp, target); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return Asset{}, ErrExists
			}
			return Asset{}, err
		}
		out.Created = true
	}
	// Also when the file was there already: one an earlier attempt left behind
	// uncommitted is committed now, and a committed one nets out to nothing.
	tx.touch(rel)
	if out.Created {
		// A page that points here was missing it until now.
		if err := s.idx.AssetAppeared(rel); err != nil {
			s.log.Warn("could not update the missing attachments", "error", err)
		}
	}
	return out, nil
}

// assetsDirIsOurs reports whether the name "assets" at the root is free or is
// spelled exactly so. On a file system that ignores case a folder of pages
// named "Assets" would otherwise receive the attachments, and be both.
// The answer is kept until forgetAssetsDir.
func (s *Space) assetsDirIsOurs() bool {
	seen := s.assetsOurs.Load()
	switch seen % 4 {
	case 1:
		return true
	case 2:
		return false
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return false // not kept: the next call looks again
	}
	ours := true
	for _, e := range entries {
		if strings.EqualFold(e.Name(), AssetsDir) {
			ours = e.Name() == AssetsDir
			break
		}
	}
	v := int64(2)
	if ours {
		v = 1
	}
	s.assetsOurs.CompareAndSwap(seen, seen+v)
	return ours
}

// forgetAssetsDir makes the next assetsDirIsOurs look at the root again.
func (s *Space) forgetAssetsDir() {
	for {
		old := s.assetsOurs.Load()
		if s.assetsOurs.CompareAndSwap(old, (old/4+1)*4) {
			return
		}
	}
}

// sameAsset reports whether target already holds exactly data. Anything else
// that has the name (other bytes, a link, a directory) is ErrExists.
func sameAsset(target string, data []byte) (bool, error) {
	fi, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() || fi.Size() != int64(len(data)) {
		return false, ErrExists
	}
	have, err := os.ReadFile(target)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(have, data) {
		return false, ErrExists
	}
	return true, nil
}

// assetDir makes sure the directory rel (assets/<page id>) exists and returns
// its file system path. A segment that is a symbolic link, or anything else
// than a directory, is refused: a link could lead out of the space. The
// directories it makes are noted in the change, so undoing it removes them.
func (s *Space) assetDir(tx *txn, rel string) (string, error) {
	p := s.root
	cur := ""
	for _, seg := range strings.Split(rel, "/") {
		p = filepath.Join(p, seg)
		cur = path.Join(cur, seg)
		fi, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(p, 0o755); err != nil {
				return "", err
			}
			tx.created = append(tx.created, cur)
		case err != nil:
			return "", err
		case !fi.IsDir(): // Lstat: a link to a directory is not one
			return "", invalidf("%q is not a directory; attachments cannot be stored", cur)
		}
	}
	return p, nil
}

// OpenFile opens a file of the space for reading: an attachment, or any other
// file that is not a page. rel is slash-separated from the root. The answer
// for everything that may not be read is ErrNotFound: a path that does not
// exist or is not a regular file, a segment that starts with a dot (.git,
// .petalbook), a path through a symbolic link, and pages (.md outside assets/), which are
// read as pages. The caller closes the file. No lock is held: a file is replaced
// whole or not at all, so a reader sees one version of it.
func (s *Space) OpenFile(rel string) (*os.File, fs.FileInfo, error) {
	if rel == "" || !utf8.ValidString(rel) || len(rel) > 4096 || strings.ContainsRune(rel, 0) {
		return nil, nil, ErrNotFound
	}
	// A page is read as a page. Under assets/ nothing is one, so a Markdown
	// file stored as an attachment can be fetched like any other.
	if strings.EqualFold(path.Ext(rel), ".md") && !(strings.HasPrefix(rel, AssetsDir+"/") && s.assetsDirIsOurs()) {
		return nil, nil, ErrNotFound
	}
	p := s.root
	var last fs.FileInfo
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") || strings.ContainsRune(seg, filepath.Separator) {
			return nil, nil, ErrNotFound
		}
		p = filepath.Join(p, seg)
		fi, err := os.Lstat(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) || isNotDir(err) {
				return nil, nil, ErrNotFound
			}
			return nil, nil, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, nil, ErrNotFound
		}
		last = fi
	}
	if !last.Mode().IsRegular() {
		return nil, nil, ErrNotFound
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	// The file opened must be the one that was looked at: a name swapped for a
	// link in between is not followed.
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || !os.SameFile(last, st) {
		f.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, ErrNotFound
	}
	return f, st, nil
}

// isNotDir reports the error of looking below something that is a file.
func isNotDir(err error) bool { return errors.Is(err, syscall.ENOTDIR) }
