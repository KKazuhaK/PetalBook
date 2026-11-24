package spaces

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// The moves of the recycle bin. A directory is moved by a rename; when the two
// places are on different filesystems (a rename answers EXDEV) it is copied
// and then deleted, so that an item is never stuck because DATA/spaces and
// DATA/recycle are on different disks. The copy is written beside its
// destination as <dst>.partial and renamed into place only when it is whole and
// synced, so a destination that exists is always complete: a copy cut short is
// started again from nothing. The source is then renamed aside, within its own
// filesystem, to <src>.moved (its parent synced) and only then deleted, so that
// a delete cut short leaves a <src>.moved and never a partial <src>: when both
// the source and the destination exist, both are whole (the copy finished, the
// source was not touched), and a <src>.moved is never a whole copy of anything
// that is not also in place. What a move cut short leaves beside the two places
// (removeLeftovers) is deleted before the move is finished or undone.

// partialSuffix marks a copy that is not finished.
const partialSuffix = ".partial"

// movedSuffix marks a source whose copy is in place, being deleted.
const movedSuffix = ".moved"

func (m *Manager) rename(oldpath, newpath string) error {
	if m.opt.Rename != nil {
		return m.opt.Rename(oldpath, newpath)
	}
	return os.Rename(oldpath, newpath)
}

func (m *Manager) removeAll(path string) error {
	if m.opt.RemoveAll != nil {
		return m.opt.RemoveAll(path)
	}
	return os.RemoveAll(path)
}

// moveDir moves the directory src to dst (which must not exist): a rename, or a
// copy and a delete across filesystems.
func (m *Manager) moveDir(src, dst string) error {
	err := m.rename(src, dst)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return err
	}
	tmp := dst + partialSuffix
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := copyTree(src, tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil { // beside it: the same filesystem
		os.RemoveAll(tmp)
		return err
	}
	syncDir(filepath.Dir(dst))
	if err := m.recycleStep("recycle:copied"); err != nil {
		return err
	}
	// The source is set aside before it is deleted, so that a delete cut short
	// never leaves a partial tree where a whole one is expected.
	aside := src + movedSuffix
	if err := os.RemoveAll(aside); err != nil {
		return err
	}
	if err := os.Rename(src, aside); err != nil { // beside it: the same filesystem
		return err
	}
	syncDir(filepath.Dir(src))
	return m.removeAll(aside)
}

// removeLeftovers deletes what a move across filesystems cut short left beside
// the two places of a directory: a copy not finished (.partial) and a source
// set aside whose delete did not finish (.moved). None of them is ever the only
// whole copy. The caller holds the item, so no move of it is running.
func removeLeftovers(a, b string) error {
	for _, p := range []string{a + partialSuffix, a + movedSuffix, b + partialSuffix, b + movedSuffix} {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

// copyTree copies a directory tree: directories, regular files (synced) and
// symbolic links (as links, never followed), with their permissions.
func copyTree(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dst, st.Mode().Perm()|0o700); err != nil {
		return err
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range ents {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		info, err := os.Lstat(s)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(s)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, d); err != nil {
				return err
			}
		case info.IsDir():
			if err := copyTree(s, d); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := copyFile(s, d, info.Mode().Perm()); err != nil {
				return err
			}
		}
		// Anything else (a socket, a device) is not part of a space.
	}
	if err := os.Chmod(dst, st.Mode().Perm()); err != nil {
		return err
	}
	syncDir(dst)
	return nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	// The umask reduced the mode it was created with: it gets the source's.
	if err := out.Chmod(perm); err != nil {
		out.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
