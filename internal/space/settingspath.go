package space

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
)

// Everything the server keeps under .petalbook (the settings files, the types,
// its temporary files, by default its state) is reached through .petalbook and
// .petalbook/types, and those are folders of the repository: a pull, or a
// person, can put a link or a file in their place. Nothing is ever read or
// written through one, wherever it leads:
//
//   - Reading. A settings file or type behind something that is not a folder
//     cannot be used, like a file that is not YAML: it is read as restored from
//     the history (restore.go), which knows the version where the folder was
//     replaced for one where the file was not there. The types listed are those
//     the folder had.
//   - Writing. A change that writes there first moves what is in the place of
//     the folder aside, inside the space, as `<name>.unreadable-<UTC>`, commits
//     that on its own, makes the folder again and puts back into it what it
//     had (the settings as they are read), so that the change is made on all of
//     it and nothing is lost (clearWay).
//   - What is not in the repository's history (temporary files, the state of a
//     space kept under .petalbook/cache) is never written behind one either:
//     at the start, and after a pull that brought one, the folder is put right
//     as above, and until then temporary files go into the Git directory.

// wayBlocked is the first folder on the way to rel (a slash path of the space;
// rel itself is not looked at) that is there and is not a folder: a link, a
// file. "" when every folder on the way is one, or is not there.
func wayBlocked(root, rel string) (string, error) {
	parts := strings.Split(rel, "/")
	p := root
	for i := 0; i < len(parts)-1; i++ {
		p = filepath.Join(p, parts[i])
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if !fi.IsDir() {
			return strings.Join(parts[:i+1], "/"), nil
		}
	}
	return "", nil
}

// lstatIn is os.Lstat of rel in root, never through a link or a file on the
// way: when one is in the way, it is what is looked at, and blocked names it.
func lstatIn(root, rel string) (fi fs.FileInfo, blocked string, err error) {
	if blocked, err = wayBlocked(root, rel); err != nil {
		return nil, "", err
	}
	if blocked != "" {
		fi, err = os.Lstat(filepath.Join(root, filepath.FromSlash(blocked)))
		return fi, blocked, err
	}
	fi, err = os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return fi, "", err
}

// isRealDir reports whether rel in root is a folder reached through folders.
func isRealDir(root, rel string) bool {
	fi, blocked, err := lstatIn(root, rel)
	return err == nil && blocked == "" && fi.IsDir()
}

// tmpDirAt is where files of the space at root are written before they are
// renamed into place: .petalbook/tmp, on the same file system as the space,
// unless something that is not a folder is on that way; then a folder in the
// Git directory, which no pull ever writes to.
func tmpDirAt(root string) string {
	rel := ".petalbook/tmp"
	if b, err := wayBlocked(root, rel+"/x"); err == nil && b == "" {
		return filepath.Join(root, filepath.FromSlash(rel))
	}
	if fi, err := os.Lstat(filepath.Join(root, ".git")); err == nil && fi.IsDir() {
		return filepath.Join(root, ".git", "petalbook-tmp")
	}
	return filepath.Join(os.TempDir(), "petalbook-tmp")
}

// ownDirs are the folders of .petalbook that the server itself writes in
// without a settings file to restore through: the folder, and its temporary
// and (by default) state folders.
var ownDirs = []string{".petalbook", ".petalbook/tmp", ".petalbook/cache"}

// firstBlockedOwn is the first of ownDirs that is there and is not a folder.
func firstBlockedOwn(root string) string {
	for _, d := range ownDirs {
		if fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(d))); err == nil && !fi.IsDir() {
			return d
		} else if err != nil {
			return "" // not there: neither is anything under it
		}
	}
	return ""
}

// asideName is a free name for a copy of rel in its own folder.
func asideName(root, rel string) string {
	name := rel + unreadableMark + time.Now().UTC().Format("20060102T150405Z")
	out := name
	for n := 2; ; n++ {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(out))); errors.Is(err, fs.ErrNotExist) {
			return out
		}
		out = fmt.Sprintf("%s-%d", name, n)
	}
}

// treeFile is a file that was in a folder, and what it is to hold again.
type treeFile struct {
	rel  string
	data []byte
	from *restored // set for a settings file: what it was restored from
}

// settingsFileAt is the settings file or type at rel, if it is one.
func settingsFileAt(rel string) (settingsFile, bool) {
	switch rel {
	case spaceFile:
		return spaceSettings, true
	case tagsFile:
		return tagsSettings, true
	}
	if dir, name := path.Split(rel); dir == typesDir+"/" && strings.HasSuffix(name, ".json") {
		if n := strings.TrimSuffix(name, ".json"); ValidTypeName(n) == nil {
			return typeSettings(n), true
		}
	}
	return settingsFile{}, false
}

// listedIn is the files the folder dir had in the history before something
// else took its place: in HEAD when it is still a folder there (the folder was
// replaced in the working tree only), else in the commit before the one that
// replaced it. source is that commit; "" when the history has no such folder.
func (h *healer) listedIn(ctx context.Context, dir string) (source string, files []string, err error) {
	if h.repo == nil {
		return "", nil, nil
	}
	repo := h.repo()
	head := repo.Head(ctx)
	if head == "" {
		return "", nil, nil
	}
	isBlob := func(commit, rel string) (bool, error) {
		_, _, err := repo.ReadBlob(ctx, commit, rel)
		if errors.Is(err, gitstore.ErrNoSuchFile) {
			return false, nil
		}
		return err == nil, err
	}
	under := func(c string) bool { return strings.HasPrefix(c, dir+"/") }
	if blob, err := isBlob(head, dir); err != nil {
		return "", nil, err
	} else if !blob {
		files, err := repo.TrackedUnder(ctx, dir)
		return head, files, err
	}
	versions, _, err := repo.FileHistory(ctx, gitstore.HistoryQuery{Start: "HEAD", Path: dir, Limit: maxRestoreLook})
	if err != nil {
		return "", nil, err
	}
	for _, v := range versions {
		blob, err := isBlob(v.Commit, dir)
		if err != nil {
			return "", nil, err
		}
		if !blob {
			return "", nil, nil // a folder here, and not one in a commit after it: nothing to go by
		}
		parent := repo.CommitOf(ctx, v.Commit+"^")
		if parent == "" {
			return "", nil, nil
		}
		changes, err := repo.DiffNames(ctx, parent, v.Commit)
		if err != nil {
			return "", nil, err
		}
		for _, c := range changes {
			if c.Status == 'D' && under(c.New) {
				files = append(files, c.New)
			}
		}
		if len(files) > 0 {
			sort.Strings(files)
			return parent, files, nil
		}
	}
	return "", nil, nil
}

// refill is what the folder dir (something else is in its place now) is to
// hold again when it is made anew: each file it had, a settings file or type
// as it is read now (restored, see restore.go), anything else as it was.
func (h *healer) refill(dir string) ([]treeFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	source, files, err := h.listedIn(ctx, dir)
	if err != nil || source == "" {
		return nil, err
	}
	var out []treeFile
	for _, rel := range files {
		if f, ok := settingsFileAt(rel); ok {
			r, err := h.restore(f)
			if err != nil {
				return nil, err
			}
			if r.text != nil {
				out = append(out, treeFile{rel: rel, data: r.text, from: r})
			}
			continue
		}
		data, _, err := h.repo().ReadBlob(ctx, source, rel)
		if errors.Is(err, gitstore.ErrNoSuchFile) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, treeFile{rel: rel, data: data})
	}
	return out, nil
}

// typeNamesFromHistory are the types the folder of the types had before
// something else took its place (or the folder above it was replaced).
func (h *healer) typeNamesFromHistory(blocked string) ([]string, error) {
	if h == nil {
		return nil, nil
	}
	key := blocked + "@" + h.historyMark()
	if fi, err := os.Lstat(filepath.Join(h.root, filepath.FromSlash(blocked))); err == nil {
		key += fmt.Sprintf(":%v:%d:%d", fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
	}
	h.mu.Lock()
	if h.names != nil && h.names.key == key {
		names := h.names.list
		h.mu.Unlock()
		return names, nil
	}
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, files, err := h.listedIn(ctx, blocked)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, rel := range files {
		if dir, name := path.Split(rel); dir == typesDir+"/" && strings.HasSuffix(name, ".json") && !strings.HasPrefix(name, ".") {
			names = append(names, strings.TrimSuffix(name, ".json"))
		}
	}
	sort.Strings(names)
	h.mu.Lock()
	h.names = &namesCache{key, names}
	h.mu.Unlock()
	return names, nil
}

type namesCache struct {
	key  string
	list []string
}

// wayWriter is what clearWay needs of a change: to journal and take in paths
// as part of it, and to commit what it did so far on its own.
type wayWriter struct {
	record func(rel string, deleted bool) error
	// commitNow commits these paths (a copy, and the name it was moved from)
	// before the folder is made anew: Git cannot record in one commit that a
	// name stopped being a link and became a folder of files.
	commitNow func(paths []string) error
}

// clearWay is for a change about to write rel under .petalbook: whatever on
// the way there is not a folder is moved aside inside the space (and that is
// committed), the folder is made again, and what it had is put back into it
// (refill). It returns the copy that was made, "" when the way was clear.
// skip is a file the change writes itself, not put back here.
func (h *healer) clearWay(rel, skip string, w wayWriter) (copy string, err error) {
	blocked, err := wayBlocked(h.root, rel)
	if err != nil || blocked == "" {
		return "", err
	}
	// What the folder is to hold is read before anything moves.
	fill, err := h.refill(blocked)
	if err != nil {
		return "", err
	}
	copy = asideName(h.root, blocked)
	if err := w.record(copy, false); err != nil {
		return "", err
	}
	if err := w.record(blocked, true); err != nil {
		return "", err
	}
	if err := os.Rename(filepath.Join(h.root, filepath.FromSlash(blocked)), filepath.Join(h.root, filepath.FromSlash(copy))); err != nil {
		return "", err
	}
	if err := w.commitNow([]string{copy, blocked}); err != nil {
		return copy, err
	}
	abs := filepath.Join(h.root, filepath.FromSlash(blocked))
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return copy, err
	}
	// The state of the space, when it is kept there, goes on there.
	h.mu.Lock()
	state := h.stateDir
	h.mu.Unlock()
	if rel, err := filepath.Rel(abs, state); state != "" && err == nil && filepath.IsLocal(rel) {
		if err := os.MkdirAll(state, 0o755); err != nil {
			return copy, err
		}
	}
	tmp := tmpDirAt(h.root)
	for _, f := range fill {
		if f.rel == skip {
			continue
		}
		if b, err := wayBlocked(h.root, f.rel); err != nil || b != "" {
			continue // a folder further down that was not one either: left out
		}
		if err := w.record(f.rel, false); err != nil {
			return copy, err
		}
		target := filepath.Join(h.root, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return copy, err
		}
		if err := writeAtomic(tmp, target, f.data); err != nil {
			return copy, err
		}
	}
	now := time.Now().UTC()
	h.mu.Lock()
	h.found, h.bySrc, h.names = nil, nil, nil
	h.mu.Unlock()
	var made []SettingsNotice
	made = append(made, SettingsNotice{File: blocked, Entry: "not a folder", Copy: copy, RepairedAt: &now})
	for _, f := range fill {
		if f.from != nil && (f.from.from != nil || len(f.from.lost) > 0) {
			made = append(made, SettingsNotice{File: f.rel, Since: f.from.since, RestoredFrom: f.from.from, Lost: f.from.lost, Copy: copy, RepairedAt: &now})
		}
	}
	h.noteRepairs(made, copy)
	if h.log != nil {
		h.log.Warn("something that is not a folder was where a folder of the settings belongs; it is kept aside and the folder was made again", "path", blocked, "copy", copy, "files", len(fill))
	}
	return copy, nil
}

// clearWayIn is clearWay for a change of the space. In a space that follows
// a remote the folder is put right as a change of its own, committed and
// pushed before the change that needs it, which then starts from there.
func (s *Space) clearWayIn(tx *txn, rel, skip string) (string, error) {
	if s.heal == nil {
		return "", nil
	}
	if !tx.remote {
		return s.heal.clearWay(rel, skip, s.localWay(tx))
	}
	if b, err := wayBlocked(s.root, rel); err != nil || b == "" {
		return "", err
	}
	r := s.rem.Load()
	if r == nil {
		return "", errors.New("the space stopped following its remote")
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), time.Minute)
	defer cancel()
	copy, err := s.repairWayLocked(ctx, r, rel)
	if err != nil {
		return "", err
	}
	tx.parent = r.repo.Head(ctx)
	return copy, nil
}

// localWay is how a change of a local space takes in what clearWay does.
func (s *Space) localWay(tx *txn) wayWriter {
	return wayWriter{
		record: func(rel string, deleted bool) error {
			op := "t"
			if deleted {
				op = "d"
			}
			if err := tx.record(jrec{Op: op, Path: rel}); err != nil {
				return err
			}
			tx.touch(rel)
			return nil
		},
		commitNow: func([]string) error { return s.cm.Flush() },
	}
}

// repairWayLocked puts right the way to rel in a space that follows a remote,
// as a change by nobody in particular: what is in the place of a folder is
// moved aside (one commit) and the folder made again with what it had (a
// second one), and both are pushed. A push that does not go through now goes
// with the next sync: what is committed is never taken back into a link.
func (s *Space) repairWayLocked(ctx context.Context, r *remote, rel string) (string, error) {
	parent := r.repo.Head(ctx)
	author, committer := s.cm.git.identities(Author{})
	var paths []string
	w := wayWriter{
		record: func(p string, _ bool) error {
			if err := s.cm.Record(Author{}, jrec{Op: "t", Path: p}); err != nil {
				return err
			}
			paths = append(paths, p)
			return nil
		},
		commitNow: func(aside []string) error {
			if _, err := r.repo.CommitPaths(ctx, aside, commitMessage, author, committer); err != nil {
				return err
			}
			paths = nil
			return nil
		},
	}
	blocked, err := wayBlocked(s.root, rel)
	if err != nil || blocked == "" {
		return "", err
	}
	copy, err := s.heal.clearWay(rel, "", w)
	if err == nil && len(paths) > 0 {
		_, err = r.repo.CommitPaths(ctx, uniq(paths), commitMessage, author, committer)
	}
	if err != nil {
		if r.repo.Head(ctx) == parent && copy != "" {
			// Nothing committed: what was moved aside goes back, so that the
			// clone is as its last commit says.
			at := filepath.Join(s.root, filepath.FromSlash(blocked))
			if _, lerr := os.Lstat(at); errors.Is(lerr, fs.ErrNotExist) {
				os.Rename(filepath.Join(s.root, filepath.FromSlash(copy)), at)
			}
		} else if r.repo.Head(ctx) != parent {
			s.log.Error("a folder of the settings was moved aside, but what it had could not be committed in it again; the next change of the settings writes it", "path", blocked, "error", err)
		}
		return "", err
	}
	s.heal.committed(paths)
	if err := r.repo.Push(ctx, r.branch); err != nil {
		if landed, lerr := s.landed(ctx, r); lerr != nil || !landed {
			s.log.Warn("a folder of the settings was put right, but could not be pushed yet; it is pushed with the next sync", "error", err)
			return copy, nil
		}
	}
	s.pushedLocked(ctx, r)
	return copy, nil
}

// clearOwnDirsLocked puts right a folder of .petalbook that the server
// writes in by itself (ownDirs) that something else took the place of, after
// a pull. ok is false when one is still in the way after it.
func (s *Space) clearOwnDirsLocked(ctx context.Context, r *remote) bool {
	b := firstBlockedOwn(s.root)
	if b == "" {
		return true
	}
	if _, err := s.repairWayLocked(ctx, r, b+"/x"); err != nil {
		s.log.Error("could not put right a folder of the settings that something else took the place of; it is tried again after the next pull", "path", b, "error", err)
	}
	return firstBlockedOwn(s.root) == ""
}
