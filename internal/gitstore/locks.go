package gitstore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StaleLockAge is how old a lock file of git has to be before
// RemoveStaleLocks takes it for one a crash left. Git holds its locks for the
// length of one command, which is far shorter.
const StaleLockAge = 60 * time.Second

// writeCap bounds a command that cancelling does not stop (lockingWrites):
// one that hangs still ends.
const writeCap = 2 * time.Minute

// StaleLock is a lock file RemoveStaleLocks removed.
type StaleLock struct {
	Path string        // relative to the Git directory, slash-separated
	Age  time.Duration // since it was last written
}

// RemoveStaleLocks removes the lock files that git left in the Git directory
// gitDir and that are older than olderThan: the ones a killed git (a crash, a
// power loss, an out-of-memory kill) never got to remove, and that otherwise
// make every later command that takes the same lock fail. They are the
// <name>.lock files at the top of gitDir (index, HEAD, ORIG_HEAD, config,
// packed-refs, shallow...) and those under refs/ and logs/. Objects and other
// checkouts (worktrees/) are not looked at.
//
// Only a caller that knows no other process uses the repository may call it:
// a lock of a git that is running is never that old, but a lock is a lock.
func RemoveStaleLocks(gitDir string, olderThan time.Duration, now time.Time) ([]StaleLock, error) {
	var removed []StaleLock
	var errs []error
	take := func(p string, fi fs.FileInfo) {
		if !fi.Mode().IsRegular() || !strings.HasSuffix(fi.Name(), ".lock") {
			return
		}
		age := now.Sub(fi.ModTime())
		if age < olderThan {
			return
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			return
		}
		rel, _ := filepath.Rel(gitDir, p)
		removed = append(removed, StaleLock{Path: filepath.ToSlash(rel), Age: age})
	}
	top, err := os.ReadDir(gitDir)
	if err != nil {
		return nil, err
	}
	for _, e := range top {
		if fi, err := e.Info(); err == nil {
			take(filepath.Join(gitDir, e.Name()), fi)
		}
	}
	for _, sub := range []string{"refs", "logs"} {
		err := filepath.WalkDir(filepath.Join(gitDir, sub), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				errs = append(errs, err)
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 || d.IsDir() {
				return nil
			}
			if fi, err := d.Info(); err == nil {
				take(p, fi)
			}
			return nil
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	return removed, errors.Join(errs...)
}
