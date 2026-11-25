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
	"github.com/KKazuhaK/PetalBook/internal/page"
)

// .petalbook holds the settings files and the types, and nothing else: it is
// a folder of the repository, and a pull, or a person, can put a link or a
// file in its place, or in the place of .petalbook/types. The server's own
// state (index, journal, notices) is kept in the Git directory, where no pull
// writes (see DefaultStateDir); a file of the space is written next to where
// it goes, under a new hidden name (newTempIn, writeIn). Nothing is ever read
// or written through a link or a file in the place of a folder, wherever it
// leads, and every read and write there is made in the folder as it was
// opened (inroot.go), so a link put there meanwhile is never followed either:
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

// firstBlockedOwn is .petalbook when it is there and is not a folder: the
// folder a local space writes in by itself when it starts (space.yml).
func firstBlockedOwn(root string) string {
	if fi, err := os.Lstat(filepath.Join(root, ".petalbook")); err == nil && !fi.IsDir() {
		return ".petalbook"
	}
	return ""
}

// asideName is a free name for a copy of rel in its own folder.
func asideName(root, rel string) string {
	name := rel + unreadableMark + time.Now().UTC().Format("20060102T150405Z")
	out := name
	for n := 2; ; n++ {
		if _, _, err := lstatIn(root, out); errors.Is(err, fs.ErrNotExist) {
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
	if fi, _, err := lstatIn(h.root, blocked); err == nil {
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
	// record journals rel before it is written (with data), removed
	// (deleted), or made otherwise (data nil: a thing moved aside), and
	// returns what to call once it is done.
	record func(rel string, deleted bool, data []byte) (func(), error)
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
	var aside []byte // what was in the way, when it is a file
	if fi, _, err := lstatIn(h.root, blocked); err == nil && fi.Mode().IsRegular() {
		aside, _ = os.ReadFile(filepath.Join(h.root, filepath.FromSlash(blocked)))
	}
	afterCopy, err := w.record(copy, false, aside)
	if err != nil {
		return "", err
	}
	afterGone, err := w.record(blocked, true, nil)
	if err != nil {
		return "", err
	}
	if err := renameIn(h.root, blocked, copy); err != nil {
		return "", err
	}
	afterCopy()
	afterGone()
	if err := w.commitNow([]string{copy, blocked}); err != nil {
		return copy, err
	}
	d, err := mkdirIn(h.root, blocked)
	if err != nil {
		return copy, err
	}
	d.Close()
	// The state of the space, when it is kept there (given so), goes on there.
	h.mu.Lock()
	state := h.stateDir
	h.mu.Unlock()
	abs := filepath.Join(h.root, filepath.FromSlash(blocked))
	if rel, err := filepath.Rel(h.root, state); state != "" && err == nil && filepath.IsLocal(rel) {
		if in, err := filepath.Rel(abs, state); err == nil && filepath.IsLocal(in) {
			d, err := mkdirIn(h.root, filepath.ToSlash(rel))
			if err != nil {
				return copy, err
			}
			d.Close()
		}
	}
	for _, f := range fill {
		if f.rel == skip {
			continue
		}
		if b, err := wayBlocked(h.root, f.rel); err != nil || b != "" {
			continue // a folder further down that was not one either: left out
		}
		after, err := w.record(f.rel, false, f.data)
		if err != nil {
			return copy, err
		}
		if err := writeIn(h.root, f.rel, f.data); err != nil {
			return copy, err
		}
		after()
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
	// The repair is pushed: the change now starts from its commit, also for
	// the next start if a crash cuts the change short.
	if err := s.cm.Record(tx.by, jrec{Op: "rp", Rev: tx.parent}); err != nil {
		return "", err
	}
	return copy, nil
}

// isAside reports whether rel is a name clearWay or keep puts a thing aside
// under: a new file, never another's.
func isAside(rel string) bool { return strings.Contains(path.Base(rel), unreadableMark) }

// localWay is how a change of a local space takes in what clearWay does.
func (s *Space) localWay(tx *txn) wayWriter {
	return wayWriter{
		record: func(rel string, deleted bool, data []byte) (func(), error) {
			tx.touch(rel)
			if !deleted && data == nil {
				// A thing moved aside that is no file: committed as it is.
				return func() {}, nil
			}
			w, err := tx.journalFiles(fileWrite{rel: rel, data: data, del: deleted, create: !deleted && data != nil && isAside(rel)})
			if err != nil {
				return nil, err
			}
			return w.applied, nil
		},
		commitNow: func([]string) error {
			if err := s.stepped("way cleared"); err != nil {
				return err
			}
			return s.cm.FlushChange()
		},
	}
}

// errRemoteMoved: the remote moved on while a change was being made; it is
// made again on the new tip.
var errRemoteMoved = errors.New("the remote moved on; the change is made again")

// repairWayLocked puts right the way to rel in a space that follows a remote,
// as a change by nobody in particular: what is in the place of a folder is
// moved aside (one commit) and the folder made again with what it had (a
// second one), and both are pushed before the change that needs it goes on.
// A repair is the server's own business, never anybody's change: when its
// push does not go through, the commits are dropped and the clone is put back
// as it was, so that nothing of it is ever left unpushed for a later sync to
// call a change of someone's. errRemoteMoved when the remote moved on: the
// change is then made again, from the new tip (which may not need the repair
// any more); a network failure fails the change, as any change fails then.
func (s *Space) repairWayLocked(ctx context.Context, r *remote, rel string) (string, error) {
	if len(r.pending) > 0 {
		return "", errPushPending // pushed on its own, once theirs are out
	}
	parent := r.repo.Head(ctx)
	author, committer := s.cm.git.identities(Author{})
	var paths, all []string
	var txs []uint64
	defer func() {
		// The repair is committed and pushed, or taken back, here and now:
		// nothing of it is left for the committer.
		for _, id := range txs {
			s.cm.forget(id)
		}
	}()
	w := wayWriter{
		record: func(p string, deleted bool, data []byte) (func(), error) {
			// Journaled so that a crash before the push has the next start
			// put the path back (rollbackRemote).
			f := txFile{path: p, old: revOf(filepath.Join(s.root, filepath.FromSlash(p)))}
			if !deleted && data != nil {
				f.rev, f.data = page.Rev(data), data
			}
			id, err := s.cm.Prepare(Author{}, []txFile{f})
			if err != nil {
				return nil, err
			}
			txs = append(txs, id)
			paths = append(paths, p)
			all = append(all, p)
			return func() {}, nil
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
		s.takeBackWayLocked(ctx, parent, blocked, copy, all)
		return "", err
	}
	s.heal.committed(paths)
	if perr := r.repo.Push(ctx, r.branch); perr != nil {
		landed, lerr := s.landed(ctx, r)
		if lerr == nil && landed {
			s.pushedLocked(ctx, r)
			return copy, nil
		}
		moved := lerr == nil && r.repo.RemoteTip(ctx, r.branch) != parent
		s.takeBackWayLocked(ctx, parent, blocked, copy, all)
		switch {
		case lerr != nil:
			return "", s.netFailure(r, lerr)
		case moved:
			r.noteFetched() // landed has just looked at the remote
			return "", errRemoteMoved
		}
		return "", unavailable(perr, "%s", gitstore.Scrub(perr.Error()))
	}
	s.pushedLocked(ctx, r)
	return copy, nil
}

// takeBackWayLocked undoes a repair of the way that was not pushed: the
// clone goes back to parent, the folder made again is taken away with what
// was put into it, and what was moved aside goes back to its place, so that
// the clone is as parent says and the next change repairs again.
func (s *Space) takeBackWayLocked(ctx context.Context, parent, blocked, copy string, paths []string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	defer s.heal.committed(paths)
	r := s.rem.Load()
	if r == nil {
		return
	}
	var errs []error
	if r.repo.Head(ctx) != parent {
		errs = append(errs, r.repo.Discard(ctx, parent, nil))
	}
	if copy != "" {
		// What was put into the folder made again, the deepest first, then the
		// folders themselves when nothing else is in them.
		var dirs []string
		for _, p := range paths {
			if p == copy || p == blocked || !strings.HasPrefix(p, blocked+"/") {
				continue
			}
			if err := removeIn(s.root, p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			for d := path.Dir(p); d != blocked && strings.HasPrefix(d, blocked+"/"); d = path.Dir(d) {
				dirs = append(dirs, d)
			}
		}
		sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
		for _, d := range append(dirs, blocked) {
			if fi, _, err := lstatIn(s.root, d); err == nil && fi.IsDir() {
				removeIn(s.root, d) // fails, rightly, when something else is in it
			}
		}
		if _, _, err := lstatIn(s.root, blocked); errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, renameIn(s.root, copy, blocked))
		} else {
			errs = append(errs, fmt.Errorf("%s could not be taken away again", blocked))
		}
		s.heal.forgetRepairs(copy)
	}
	if err := errors.Join(errs...); err != nil {
		s.log.Error("a repair of a folder of the settings that was not pushed could not be undone completely", "path", blocked, "copy", copy, "error", err)
	}
}
