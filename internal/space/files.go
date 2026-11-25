package space

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/KKazuhaK/PetalBook/internal/page"
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
func (s *Space) createNamed(by Author, folder, dir, stem string, data []byte) (string, error) {
	taken, err := s.takenKeys(dir)
	if err != nil {
		return "", err
	}
	tmp, err := newTempIn(dir, data, 0o644)
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
		// Journal the name, with the content, before taking it. In a local
		// space it is a transaction, applied once the name is ours and given
		// up when another file holds it, so the next start commits our
		// content there even if the file was changed outside meanwhile, and
		// never someone else's. A space that follows a remote journals a "w"
		// record, which recovery only believes if the file has our content.
		rel := path.Join(folder, name)
		tx, err := s.cm.Prepare(by, []txFile{{path: rel, rev: rev, data: data}})
		if err != nil {
			return "", err
		}
		err = s.ops.Link(tmp, filepath.Join(dir, name))
		if err == nil {
			s.cm.Applied(tx)
			return name, nil
		}
		s.cm.Abort(tx)
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
//
// With reserve, the new name is only taken, as a second name of the file, and
// src stays: the caller journals the move as a transaction and removes src
// itself (reserved is then true). A change of spelling is made at once, and
// reserved is false.
func (s *Space) moveNamed(by Author, src, dir, stem string, intent func(name string) error, reserve bool) (name string, reserved bool, err error) {
	if intent == nil {
		intent = func(string) error { return nil }
	}
	taken, err := s.takenKeys(dir)
	if err != nil {
		return "", false, err
	}
	srcInfo, err := s.ops.Lstat(src)
	if err != nil {
		return "", false, err
	}
	ownName := filepath.Base(src)
	sameDir := filepath.Clean(filepath.Dir(src)) == filepath.Clean(dir)
	for n := 1; n <= maxNameTries; n++ {
		name := page.NumberedStem(stem, n) + ".md"
		dst := filepath.Join(dir, name)
		if sameDir && page.NameKey(name) == page.NameKey(ownName) {
			if name == ownName {
				return name, false, nil
			}
			// Another spelling of our own name. On a file system that folds
			// case this is the same file; on one that does not, it may be a
			// different page's file, which must not be replaced.
			if err := intent(name); err != nil {
				return "", false, err
			}
			fi, err := s.ops.Lstat(dst)
			switch {
			case err == nil && s.ops.SameFile(srcInfo, fi):
			case err != nil && errors.Is(err, fs.ErrNotExist):
			case err != nil:
				return "", false, err
			default:
				continue
			}
			if err := s.rename(by, src, dst, nil); err != nil {
				return "", false, err
			}
			return name, false, nil
		}
		if taken[page.NameKey(name)] {
			continue
		}
		if err := intent(name); err != nil {
			return "", false, err
		}
		err := s.ops.Link(src, dst)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		// The target is now a second name for the file (a copy on file systems
		// without hard links); check it is there before the old name goes.
		if fi, err := s.ops.Lstat(dst); err != nil || fi.Size() != srcInfo.Size() {
			return "", false, fmt.Errorf("%w: %s was taken while moving", ErrExists, name)
		}
		if reserve {
			if err := s.stepped("name reserved"); err != nil {
				s.ops.Remove(dst)
				return "", false, err
			}
			return name, true, nil
		}
		if err := s.ops.Remove(src); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", false, err
		}
		return name, false, nil
	}
	return "", false, noFreeName(stem)
}

// rename renames from to to, also when to is another spelling of from's own
// name on a file system that takes the two for one entry. macOS respells the
// entry on a plain rename; Linux (a case-folding ext4, f2fs or tmpfs
// directory) takes it for a rename of a file onto itself, reports success
// and changes nothing. Going through a third name respells it on both.
//
// inside lists the files of a folder being renamed (relative to it); nil for
// a file. The third name is journaled before anything moves: if the process
// dies between the two steps, the next start finishes the rename
// (recoverPending), and if it cannot, the files are committed under the third
// name rather than as deleted. That name is next to
// the target and keeps a file's extension, so a page left there is still a
// page in its folder.
func (s *Space) rename(by Author, from, to string, inside []string) error {
	fi, err := s.ops.Lstat(from)
	if err != nil {
		return err
	}
	ti, err := s.ops.Lstat(to)
	if err != nil || filepath.Base(from) == filepath.Base(to) || !s.ops.SameFile(fi, ti) {
		return s.ops.Rename(from, to)
	}
	base := filepath.Base(to)
	ext := ""
	if !fi.IsDir() {
		ext = filepath.Ext(base)
	}
	stem := strings.TrimSuffix(base, ext)
	// A name of its own each time: the undo of a rename goes through a third
	// name as well, and if both were "(renaming 1)" a crash during the undo
	// would be finished by the record of the rename it was undoing.
	mid := ""
	for n := 1; n <= maxNameTries && mid == ""; n++ {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		c := filepath.Join(filepath.Dir(to), fmt.Sprintf("%s (renaming %x)%s", stem, b, ext))
		if _, err := s.ops.Lstat(c); errors.Is(err, fs.ErrNotExist) {
			mid = c
		}
	}
	if mid == "" {
		return fmt.Errorf("%w: no free name to rename %s through", ErrExists, base)
	}
	midPaths, err := s.journalRespell(by, from, mid, to, inside)
	if err != nil {
		return err
	}
	if err := s.ops.Rename(from, mid); err != nil {
		return err
	}
	if err := s.ops.Rename(mid, to); err != nil {
		if back := s.ops.Rename(mid, from); back != nil {
			// Stuck under the third name. In local mode that is committed
			// there (the next commit's journal rewrite would otherwise drop
			// the records, and only the old name would go, as a deletion); a
			// space that follows a remote undoes the change from Git instead.
			if s.cm != nil && s.rem.Load() == nil {
				s.cm.Touch(by, midPaths...)
			}
			return errors.Join(err, fmt.Errorf("%s is left as %s: %w", filepath.Base(from), filepath.Base(mid), back))
		}
		return err
	}
	return nil
}

// journalRespell records a rename through a third name (see rename) and
// returns the paths under it and under the old name. A Space without a committer (tests of naming
// alone) journals nothing.
func (s *Space) journalRespell(by Author, from, mid, to string, inside []string) ([]string, error) {
	if s.cm == nil {
		return nil, nil
	}
	rel := func(p string) (string, error) {
		r, err := filepath.Rel(s.root, p)
		if err != nil || !fs.ValidPath(filepath.ToSlash(r)) {
			return "", fmt.Errorf("%s is not inside the space", p)
		}
		return filepath.ToSlash(r), nil
	}
	f, err := rel(from)
	if err != nil {
		return nil, err
	}
	m, err := rel(mid)
	if err != nil {
		return nil, err
	}
	t, err := rel(to)
	if err != nil {
		return nil, err
	}
	paths, olds := []string{m}, []string{f}
	if inside != nil {
		paths, olds = paths[:0], olds[:0]
		for _, p := range inside {
			paths, olds = append(paths, path.Join(m, p)), append(olds, path.Join(f, p))
		}
	}
	recs := []jrec{{Op: "r", From: f, To: t, Path: m, Dir: inside != nil}}
	for _, p := range paths {
		recs = append(recs, jrec{Op: "t", Path: p})
	}
	// Journaled only, not handed to the committer: once the rename is done
	// the third name is gone, and a space that follows a remote commits its
	// changes itself (txn), not in the background.
	return append(paths, olds...), s.cm.Record(by, recs...)
}

// moveIntent returns the journal call for moving the page at rel into folder:
// it records the name about to be taken ("mr"), so that a crash before the
// move's transaction has the next start take it back.
func (s *Space) moveIntent(tx *txn, rel, folder string) func(name string) error {
	return func(name string) error {
		to := path.Join(folder, name)
		// The name is taken as a second name of the page; the move and its
		// links are one transaction after that. Without it, the next start
		// takes the name back ("mr").
		return tx.record(jrec{Op: "mr", From: rel, To: to})
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
					// Not in the listing under any spelling this code can fold,
					// yet the file system may still take the name for one of the
					// entries (ß and ss, the Greek sigmas, are one name to some).
					// Ask it: the entry that is the same file is the folder.
					if fi, lerr := s.ops.Lstat(filepath.Join(cur, seg)); lerr == nil {
						for _, e := range entries {
							if ei, ierr := e.Info(); ierr == nil && s.ops.SameFile(fi, ei) {
								found = e.Name()
								break
							}
						}
					}
				}
				if found == "" {
					existed = false
					break
				}
				for _, e := range entries {
					if e.Name() == found {
						if e.Type()&fs.ModeSymlink != 0 {
							return "", false, invalidf("Path %q goes through a symbolic link.", rel)
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
func (s *Space) ensureFolder(tx *txn, rel string) (canon, abs string, err error) {
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
	// The directories this call makes are noted, so a change that is undone
	// can remove them again.
	var missing []string
	for d := canon; d != ""; d = path.Dir(d) {
		if d == "." {
			break
		}
		if _, err := os.Lstat(filepath.Join(s.root, filepath.FromSlash(d))); err == nil {
			break
		}
		missing = append([]string{d}, missing...)
	}
	if err := notPageNames(missing); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", "", err
	}
	tx.created = append(tx.created, missing...)
	return canon, abs, s.idx.AddFolder(canon)
}

// notPageNames refuses to make a folder whose name ends in .md: that is how
// page files are named, and a folder standing where a page file was cannot be
// told apart from it by Git while the page's removal is not yet committed.
// Folders of that name that already exist are used as they are.
func notPageNames(dirs []string) error {
	for _, d := range dirs {
		if strings.EqualFold(path.Ext(d), ".md") {
			return invalidf("A folder name cannot end in .md (%q).", path.Base(d))
		}
	}
	return nil
}

// ValidNewFolder reports whether rel is a folder path the server would make
// (ValidFolder, and no segment named like a page file). "" is the root.
func ValidNewFolder(rel string) error {
	if err := checkRel(rel); err != nil {
		return err
	}
	if rel == "" {
		return nil
	}
	return notPageNames(strings.Split(rel, "/"))
}

// tempName is a new name for a file written in a folder of the space before
// it is renamed or linked into place there: hidden (a dot name is no page,
// and is not listed), unpredictable, and marked as the server's (tempMark), so
// one a crash left is known at the next start (removeLeftTemps). Git never
// sees one: they are excluded in info/exclude, and the committer names its
// paths one by one.
func tempName() string {
	var rnd [8]byte
	rand.Read(rnd[:])
	return tempMark + hex.EncodeToString(rnd[:])
}

// isTempName reports whether name is one tempName or writeIn gives.
func isTempName(name string) bool {
	return strings.HasPrefix(name, ".") && strings.Contains(name, tempMark)
}

// newTempIn writes data to a new file in dir, the folder the file is to be
// renamed or linked into, so that it is always on one file system with it (a
// folder elsewhere may not be, whatever device numbers say). The file is made
// exclusively under a new name, so nothing that is there (a link a pull
// brought, a file) is ever written through or replaced; it is removed again
// when anything fails.
func newTempIn(dir string, data []byte, mode os.FileMode) (string, error) {
	var f *os.File
	var err error
	for range 8 {
		f, err = os.OpenFile(filepath.Join(dir, tempName()), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, fs.ErrExist) {
			break
		}
	}
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
// old file or the new one, never half of it. The new file is written in the
// folder of target (newTempIn).
func writeAtomic(target string, data []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Lstat(target); err == nil && st.Mode().IsRegular() {
		mode = st.Mode().Perm()
	}
	tmp, err := newTempIn(filepath.Dir(target), data, mode)
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
