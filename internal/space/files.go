package space

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/KazuhaHub/PetalBook/internal/page"
)

// maxNameTries bounds every search for a free file name or id. A loop that
// cannot make progress must fail, not hold the space lock forever.
const maxNameTries = 1000

// fileOps is the part of the file system the naming logic decides on. It is a
// seam so tests can model file systems the machine they run on does not have
// (case-sensitive on macOS, say).
type fileOps struct {
	Lstat    func(string) (fs.FileInfo, error)
	Rename   func(from, to string) error
	Link     func(from, to string) error // fails with fs.ErrExist if to exists
	Remove   func(string) error
	ReadDir  func(string) ([]fs.DirEntry, error)
	SameFile func(a, b fs.FileInfo) bool
}

func osOps() fileOps {
	return fileOps{
		Lstat: os.Lstat, Rename: os.Rename, Link: linkNoClobber, Remove: os.Remove,
		ReadDir: os.ReadDir, SameFile: os.SameFile,
	}
}

// tmpDir is where files are written before being renamed into place. It is
// inside the space (same file system, so rename is atomic) and git-ignored.
func (s *Space) tmpDir() string { return filepath.Join(s.root, ".petalbook", "tmp") }

// takenKeys lists the names in dir by NameKey (NFC, case-folded). It is a
// hint only to skip names quickly: the file system has the last word, through
// the exclusive link in createNamed and moveNamed. Cost: O(files in that folder).
func (s *Space) takenKeys(dir string) (map[string]bool, error) {
	entries, err := s.ops.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	taken := make(map[string]bool, len(entries))
	for _, e := range entries {
		taken[page.NameKey(e.Name())] = true
	}
	return taken, nil
}

func noFreeName(stem string) error {
	return fmt.Errorf("%w: no free file name for %q after %d tries", ErrExists, stem, maxNameTries)
}

// createNamed writes data to a new file in dir named after stem ("stem.md",
// then "stem 2.md"...). A name is claimed by creating it exclusively, so a file
// the listing did not show (another name for the same file on a file system
// that ignores case or Unicode form) is never replaced, and the search ends
// after maxNameTries.
func (s *Space) createNamed(folder, dir, stem string, data []byte) (string, error) {
	taken, err := s.takenKeys(dir)
	if err != nil {
		return "", err
	}
	tmp, err := newTemp(s.tmpDir(), data, 0o644)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	rev := page.Rev(data)
	for n := 1; n <= maxNameTries; n++ {
		name := page.NumberedStem(stem, n) + ".md"
		if taken[page.NameKey(name)] {
			continue
		}
		// Journal the name before taking it; recovery only believes this
		// record if the file has our content.
		if err := s.cm.Record(jrec{Op: "w", Path: path.Join(folder, name), Rev: rev}); err != nil {
			return "", err
		}
		err := s.ops.Link(tmp, filepath.Join(dir, name))
		if err == nil {
			return name, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", noFreeName(stem)
}

// moveNamed moves the file src into dir under a free name derived from stem
// and returns the name. It never replaces an existing different file. When dir
// is src's own folder the file may be renamed to another spelling of its own
// name (a case change), which is allowed only if the target really is the same
// file or does not exist.
func (s *Space) moveNamed(src, dir, stem string, intent func(name string) error) (string, error) {
	if intent == nil {
		intent = func(string) error { return nil }
	}
	taken, err := s.takenKeys(dir)
	if err != nil {
		return "", err
	}
	srcInfo, err := s.ops.Lstat(src)
	if err != nil {
		return "", err
	}
	ownName := filepath.Base(src)
	sameDir := filepath.Clean(filepath.Dir(src)) == filepath.Clean(dir)
	for n := 1; n <= maxNameTries; n++ {
		name := page.NumberedStem(stem, n) + ".md"
		dst := filepath.Join(dir, name)
		if sameDir && page.NameKey(name) == page.NameKey(ownName) {
			if name == ownName {
				return name, nil
			}
			// Another spelling of our own name. On a file system that folds
			// case this is the same file; on one that does not, it may be a
			// different page's file, which must not be replaced.
			if err := intent(name); err != nil {
				return "", err
			}
			fi, err := s.ops.Lstat(dst)
			switch {
			case err == nil && s.ops.SameFile(srcInfo, fi):
			case err != nil && errors.Is(err, fs.ErrNotExist):
			case err != nil:
				return "", err
			default:
				continue
			}
			if err := s.ops.Rename(src, dst); err != nil {
				return "", err
			}
			return name, nil
		}
		if taken[page.NameKey(name)] {
			continue
		}
		if err := intent(name); err != nil {
			return "", err
		}
		err := s.ops.Link(src, dst)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		// The target is now a second name for the file (a copy on file systems
		// without hard links); check it is there before the old name goes.
		if fi, err := s.ops.Lstat(dst); err != nil || fi.Size() != srcInfo.Size() {
			return "", fmt.Errorf("%w: %s was taken while moving", ErrExists, name)
		}
		if err := s.ops.Remove(src); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		return name, nil
	}
	return "", noFreeName(stem)
}

// moveIntent returns the journal call for moving the page at rel into folder:
// it records from -> to before the link, so a crash between link and remove
// can be finished at the next start.
func (s *Space) moveIntent(rel, folder string) func(name string) error {
	return func(name string) error {
		return s.cm.Record(jrec{Op: "m", From: rel, To: path.Join(folder, name)})
	}
}

// resolveFolder maps a folder path to the names that exist on disk: a folder
// that differs only in case or Unicode form from an existing one is that
// folder, so the path, the tree and the index agree. existed reports whether
// the whole path was already there.
func (s *Space) resolveFolder(rel string) (canon string, existed bool, err error) {
	if rel == "" {
		return "", true, nil
	}
	cur := s.root
	existed = true
	var out []string
	for _, seg := range strings.Split(rel, "/") {
		name := seg
		if existed {
			entries, rerr := s.ops.ReadDir(cur)
			switch {
			case rerr != nil && errors.Is(rerr, fs.ErrNotExist):
				existed = false
			case rerr != nil:
				return "", false, rerr
			default:
				found := ""
				for _, e := range entries { // an exact match wins
					if e.Name() == seg {
						found = e.Name()
					}
				}
				for _, e := range entries {
					if found == "" && page.NameKey(e.Name()) == page.NameKey(seg) {
						found = e.Name()
					}
				}
				if found == "" {
					existed = false
					break
				}
				for _, e := range entries {
					if e.Name() == found {
						if e.Type()&fs.ModeSymlink != 0 {
							return "", false, invalidf("path %q goes through a symbolic link", rel)
						}
						if !e.IsDir() {
							return "", false, fmt.Errorf("%w: %q is a file", ErrExists, found)
						}
					}
				}
				name = found
			}
		}
		out = append(out, name)
		cur = filepath.Join(cur, name)
	}
	return strings.Join(out, "/"), existed, nil
}

// ensureFolder creates a folder path (and records it) and returns it as it is
// spelled on disk, with its file system path.
func (s *Space) ensureFolder(rel string) (canon, abs string, err error) {
	if err := checkRel(rel); err != nil {
		return "", "", err
	}
	canon, _, err = s.resolveFolder(rel)
	if err != nil {
		return "", "", err
	}
	if abs, err = s.abs(canon); err != nil {
		return "", "", err
	}
	if canon == "" {
		return "", abs, nil
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", "", err
	}
	return canon, abs, s.idx.AddFolder(canon)
}

func newTemp(tmpDir string, data []byte, mode os.FileMode) (string, error) {
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(tmpDir, "w-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	fail := func(err error) (string, error) {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// writeAtomic replaces target with data so a reader or a crash sees either the
// old file or the new one, never half of it.
func writeAtomic(tmpDir, target string, data []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(target); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := newTemp(tmpDir, data, mode)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// linkNoClobber makes target a second name for src and fails with fs.ErrExist
// if target exists. It falls back to a checked copy where the file system has
// no hard links.
func linkNoClobber(src, target string) error {
	err := os.Link(src, target)
	if err == nil || errors.Is(err, fs.ErrExist) {
		return err
	}
	if _, serr := os.Lstat(target); serr == nil {
		return fs.ErrExist
	}
	data, rerr := os.ReadFile(src)
	if rerr != nil {
		return err
	}
	f, oerr := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if oerr != nil {
		return oerr
	}
	if _, werr := f.Write(data); werr != nil {
		f.Close()
		os.Remove(target)
		return werr
	}
	return f.Close()
}
