package space

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
)

// Versions before 2.2 kept the state of a space in .petalbook/cache, a folder
// of the work tree. It is moved into the Git directory once (moveOldState).
// Only the server's own files are taken, by their known names, and only those
// the repository does not have: a file of that folder that was committed (a
// person forced it in, or a pull brought it) is the repository's, and is
// neither moved, which would be a deletion nobody made, nor used, which would
// replay another machine's journal. What else is there is left where it is.

// oldStateDir is where versions before 2.2 kept the state by default.
const oldStateDir = ".petalbook/cache"

// oldStateFiles are the files the server kept in oldStateDir.
var oldStateFiles = []string{
	"pending.journal",
	"settings-restored.json",
	"state.db", "state.db-wal", "state.db-shm", "state.db-journal",
	"index.db", "index.db-wal", "index.db-shm", "index.db-journal",
}

// EarlierStatePattern matches, in a state directory, the state databases of
// an earlier version that were found when the state directory was already in
// use (see moveOldState): their drafts are to be imported by the caller that
// opens the state database (ImportEarlierDrafts in the command).
const EarlierStatePattern = "state-earlier-*.db"

// moveOldState takes the state an earlier version kept in .petalbook/cache
// into dir. When dir is not there yet the files are copied into a new folder
// that is then renamed to dir, and only then removed from the old place, so a
// crash leaves them whole in one place or both. When dir is there already
// (a downgrade was followed by an upgrade), what the old place holds is
// newer than nothing and must not be lost either:
//
//   - its journal of changes not committed yet is appended to the current
//     one, and so replayed (its records are paths: replaying one twice is
//     harmless);
//   - its state database (drafts, in single-user mode) and its notices are
//     kept beside the current ones as state-earlier-<UTC>.db and
//     settings-restored-earlier-<UTC>.json; the drafts are imported by
//     whoever opens the state database;
//   - its index is dropped: it is made again from the files.
//
// Only a folder reached through folders is looked at; a link there is left
// alone (and never followed).
func moveOldState(root, dir string, g *gitRepo, log *slog.Logger) error {
	if !isRealDir(root, oldStateDir) {
		return nil
	}
	old, blocked, err := openDirIn(root, oldStateDir)
	if err != nil || blocked != "" || old == nil {
		return nil
	}
	defer old.Close()
	var present []string
	for _, n := range oldStateFiles {
		if fi, err := old.Lstat(n); err == nil && fi.Mode().IsRegular() {
			present = append(present, n)
		}
	}
	if len(present) == 0 {
		return nil
	}
	tracked, err := g.trackedLower()
	if err != nil {
		return fmt.Errorf("could not tell which files of %s the repository has: %w", oldStateDir, err)
	}
	var names []string
	for _, n := range present {
		if tracked[strings.ToLower(oldStateDir+"/"+n)] {
			log.Warn("a file of the repository is where an earlier version kept its state; it is left as it is and not used", "path", oldStateDir+"/"+n)
			continue
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		return nil
	}
	if _, err = os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		err = moveFresh(old, names, dir, log)
	} else if err == nil {
		err = mergeOld(old, names, dir, time.Now().UTC(), log)
	}
	if err == nil {
		// The folder goes when nothing else is in it.
		if p, b, perr := openDirIn(root, ".petalbook"); perr == nil && b == "" && p != nil {
			p.Remove("cache")
			p.Close()
		}
	}
	return err
}

// moveFresh copies names from old into a new folder renamed to dir, then
// removes them from old.
func moveFresh(old *os.Root, names []string, dir string, log *slog.Logger) error {
	stage := dir + ".new"
	os.RemoveAll(stage) // copies a crash left: the originals are still in old
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	for _, n := range names {
		if err := copyOut(old, n, filepath.Join(stage, n)); err != nil {
			os.RemoveAll(stage)
			return fmt.Errorf("the state of the space could not be moved from %s to %s: %w", oldStateDir, dir, err)
		}
	}
	if err := os.Rename(stage, dir); err != nil {
		os.RemoveAll(stage)
		return fmt.Errorf("the state of the space could not be moved from %s to %s: %w", oldStateDir, dir, err)
	}
	syncDir(filepath.Dir(dir))
	for _, n := range names {
		old.Remove(n)
	}
	log.Info("moved the state of the space into the Git directory", "from", oldStateDir, "to", dir, "files", len(names))
	return nil
}

// mergeOld brings what old holds into the state directory dir that is in use.
func mergeOld(old *os.Root, names []string, dir string, now time.Time, log *slog.Logger) error {
	stamp := now.Format("20060102T150405Z")
	has := map[string]bool{}
	for _, n := range names {
		has[n] = true
	}
	if has["pending.journal"] {
		data, err := readOut(old, "pending.journal")
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(data)) > 0 {
			if err := appendJournal(filepath.Join(dir, "pending.journal"), data); err != nil {
				return fmt.Errorf("the journal an earlier version left in %s could not be taken over: %w", oldStateDir, err)
			}
			log.Warn("an earlier version left changes it had not committed in its own state folder: they are committed now", "from", oldStateDir+"/pending.journal")
		}
		old.Remove("pending.journal")
	}
	aside := func(base, ext string, members ...string) error {
		var moved []string
		for _, m := range members {
			if !has[m] {
				continue
			}
			to := filepath.Join(dir, base+"-earlier-"+stamp+ext+strings.TrimPrefix(m, base+ext))
			if err := copyOut(old, m, to); err != nil {
				return fmt.Errorf("%s/%s could not be kept in %s: %w", oldStateDir, m, dir, err)
			}
			moved = append(moved, m)
		}
		if len(moved) > 0 {
			syncDir(dir)
			for _, m := range moved {
				old.Remove(m)
			}
			log.Warn("an earlier version left its own state where it kept it before; it is kept beside the current one", "from", oldStateDir, "files", moved,
				"as", filepath.Join(dir, base+"-earlier-"+stamp+ext))
		}
		return nil
	}
	if has["state.db"] {
		if err := aside("state", ".db", "state.db", "state.db-wal", "state.db-shm", "state.db-journal"); err != nil {
			return err
		}
	}
	if err := aside("settings-restored", ".json", "settings-restored.json"); err != nil {
		return err
	}
	for _, n := range []string{"index.db", "index.db-wal", "index.db-shm", "index.db-journal"} {
		if has[n] {
			old.Remove(n) // made again from the files
		}
	}
	return nil
}

// readOut reads the regular file name of old, never through a link.
func readOut(old *os.Root, name string) ([]byte, error) {
	fi, err := old.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a file", name)
	}
	f, err := old.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !os.SameFile(fi, st) {
		return nil, fmt.Errorf("%s was replaced while it was read", name)
	}
	return io.ReadAll(f)
}

// copyOut copies the regular file name of old to the new file to (written
// whole and synced before it gets its name), never through a link.
func copyOut(old *os.Root, name, to string) error {
	fi, err := old.Lstat(name)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a file", name)
	}
	src, err := old.Open(name)
	if err != nil {
		return err
	}
	defer src.Close()
	if st, err := src.Stat(); err != nil || !os.SameFile(fi, st) {
		return fmt.Errorf("%s was replaced while it was copied", name)
	}
	tmp := to + ".copying"
	dst, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	if err == nil {
		err = dst.Sync()
	}
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, to)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// appendJournal adds the records in data to the journal file at path, on a
// line of their own, and syncs it.
func appendJournal(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, fi.Size()-1); err == nil && last[0] != '\n' {
			data = append([]byte{'\n'}, data...)
		}
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// trackedLower is every path the repository has, in the index or in HEAD,
// lower-cased (a file system that folds case takes one for the other).
func (g *gitRepo) trackedLower() (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	run := hookRunner{g}
	out := map[string]bool{}
	add := func(s string) {
		for _, p := range strings.Split(s, "\x00") {
			if p != "" {
				out[strings.ToLower(p)] = true
			}
		}
	}
	s, errOut, err := run.Run(ctx, gitstore.Cmd{Dir: g.dir, Args: []string{"ls-files", "-z"}})
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, gitstore.Scrub(errOut))
	}
	add(s)
	if g.r().Head(ctx) != "" {
		s, errOut, err = run.Run(ctx, gitstore.Cmd{Dir: g.dir, Args: []string{"ls-tree", "-r", "-z", "--name-only", "HEAD"}})
		if err != nil {
			return nil, fmt.Errorf("%w: %s", err, gitstore.Scrub(errOut))
		}
		add(s)
	}
	return out, nil
}

// EarlierStateDBs are the state databases of an earlier version kept in the
// state directory (EarlierStatePattern), oldest first.
func (s *Space) EarlierStateDBs() []string {
	m, _ := filepath.Glob(filepath.Join(s.stateDir, EarlierStatePattern))
	return m
}
