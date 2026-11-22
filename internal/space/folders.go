package space

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
)

// folderKey is a folder path as a file system that ignores case and Unicode
// form would see it.
func folderKey(rel string) string {
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		segs[i] = page.NameKey(s)
	}
	return strings.Join(segs, "/")
}

// MoveFolder moves a folder with everything in it to the path to, which is
// also how a folder is renamed. Missing parents of to are made. It returns the
// folder's new path as it is on disk. It is one change: one commit, and in a
// space that follows a remote one push, or nothing at all.
func (s *Space) MoveFolder(by Author, from, to string) (string, error) {
	if err := checkRel(from); err != nil {
		return "", err
	}
	if err := checkRel(to); err != nil {
		return "", err
	}
	if from == "" {
		return "", invalidf("the root cannot be moved")
	}
	if to == "" {
		return "", invalidf("a folder cannot become the root")
	}
	return mutate(s, by, func(tx *txn) (string, error) { return s.moveFolderLocked(tx, from, to) })
}

func (s *Space) moveFolderLocked(tx *txn, from, to string) (string, error) {
	canonFrom, existed, err := s.resolveFolder(from)
	if errors.Is(err, ErrExists) || (err == nil && !existed) {
		return "", ErrNoFolder
	}
	if err != nil {
		return "", err
	}
	absFrom, err := s.abs(canonFrom)
	if err != nil {
		return "", err
	}
	fromInfo, err := s.ops.Lstat(absFrom)
	if err != nil {
		return "", err
	}
	fromKey, toKey := folderKey(canonFrom), folderKey(to)
	switch {
	case to == canonFrom:
		return "", invalidf("the folder is already at %q", to)
	case strings.HasPrefix(toKey, fromKey+"/"):
		return "", invalidf("a folder cannot be moved into itself")
	}
	// The same question put to the file system, which may take two names for
	// one where no folding of letters here would: is some folder on the way to
	// the target the folder being moved?
	for anc := path.Dir(to); anc != "." && anc != "/" && anc != ""; anc = path.Dir(anc) {
		if fi, err := s.ops.Lstat(filepath.Join(s.root, filepath.FromSlash(anc))); err == nil && s.ops.SameFile(fromInfo, fi) {
			return "", invalidf("a folder cannot be moved into itself")
		}
	}
	parent, base := path.Dir(to), path.Base(to)
	if parent == "." {
		parent = ""
	}
	if err := notPageNames([]string{base}); err != nil {
		return "", err
	}
	// Refused before anything is made: a target that is already there, under
	// this name or another spelling of it (to the rest of the space, and to a
	// file system that ignores case, those are one folder). The one exception
	// is the folder itself, being respelled.
	if canonParent, parentExisted, err := s.resolveFolder(parent); err != nil {
		return "", err
	} else if parentExisted {
		absParent := filepath.Join(s.root, filepath.FromSlash(canonParent))
		// What the file system has under the target's name decides: the folder
		// itself (being respelled) is fine, anything else is in the way.
		switch fi, err := s.ops.Lstat(filepath.Join(absParent, base)); {
		case err == nil && !s.ops.SameFile(fromInfo, fi):
			return "", ErrExists
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return "", err
		}
		// And on a file system that tells the spellings apart, another
		// spelling of the name counts as the same folder to the rest of the
		// space (and to the next machine the repository is cloned on).
		entries, err := s.ops.ReadDir(absParent)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		for _, e := range entries {
			if page.NameKey(e.Name()) != page.NameKey(base) {
				continue
			}
			if fi, err := e.Info(); err == nil && s.ops.SameFile(fromInfo, fi) {
				continue
			}
			return "", ErrExists
		}
	}

	// Everything in the folder: files, and the folders that hold them.
	var files, subdirs []string // relative to the folder
	err = filepath.WalkDir(absFrom, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(absFrom, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			return invalidf("the folder holds a symbolic link (%s); it cannot be moved", path.Join(canonFrom, rel))
		case e.IsDir():
			subdirs = append(subdirs, rel)
		case e.Type().IsRegular():
			files = append(files, rel)
		default:
			return invalidf("the folder holds something that is not a file (%s); it cannot be moved", path.Join(canonFrom, rel))
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	canonParent, parentAbs, err := s.ensureFolder(tx, parent)
	if err != nil {
		return "", err
	}
	target := path.Join(canonParent, base)
	absTarget := filepath.Join(parentAbs, base)

	// Which of the files are the repository's business: the pages, the
	// placeholders of empty folders, whatever Git already has, and whatever the
	// server wrote and has not committed yet. A file that merely lies there
	// (an editor's leftovers) moves with the folder and is not committed.
	ours := map[string]bool{}
	for _, f := range files {
		if isPagePath(path.Join(canonFrom, f)) || path.Base(f) == ".gitkeep" {
			ours[f] = true
		}
	}
	tracked, err := s.cm.git.r().TrackedUnder(s.ctx, canonFrom)
	if err != nil {
		return "", err
	}
	for _, p := range append(tracked, s.cm.pendingUnder(canonFrom)...) {
		ours[strings.TrimPrefix(p, canonFrom+"/")] = true
	}
	var oldPaths, newPaths, oldPages, newPages []string
	for f := range ours {
		o, n := path.Join(canonFrom, f), path.Join(target, f)
		oldPaths, newPaths = append(oldPaths, o), append(newPaths, n)
		if isPagePath(o) {
			oldPages = append(oldPages, o)
		}
		if isPagePath(n) {
			newPages = append(newPages, n)
		}
	}
	sort.Strings(oldPaths)
	sort.Strings(newPaths)
	inHead := make(map[string]bool, len(tracked))
	for _, p := range tracked {
		inHead[p] = true
	}
	var untracked []string // of the old paths, the ones the last commit does not have
	for _, p := range oldPaths {
		if !inHead[p] {
			untracked = append(untracked, p)
		}
	}

	// Journal first: after a crash these paths are committed as they are then,
	// moved or not.
	recs := make([]jrec, 0, len(oldPaths)+len(newPaths))
	for _, p := range append(append([]string{}, oldPaths...), newPaths...) {
		recs = append(recs, jrec{Op: "t", Path: p})
	}
	// The bindings of the folder (and of the folders in it) follow it: that is
	// part of the move, so it is journaled with it and a crash between the
	// rename and the edit of space.yml is finished at recovery (see "b").
	bind := jrec{Op: "b", From: canonFrom, To: target}
	recs = append(recs, bind)
	if err := tx.record(recs...); err != nil {
		return "", err
	}
	tx.bind = append(tx.bind, bind)
	inside := make([]string, 0, len(ours))
	for f := range ours {
		inside = append(inside, f)
	}
	sort.Strings(inside)
	if err := s.rename(tx.by, absFrom, absTarget, inside); err != nil {
		tx.settleBindings() // nothing moved (or the rename is stuck: its own records say so)
		return "", err
	}
	// The folder is part of the change from here on. Undoing it renames the
	// folder back, which takes everything in it along: files Git does not
	// track, and empty folders, which Git cannot put back.
	sort.Strings(subdirs)
	tx.undo = append(tx.undo, func() []string {
		if err := s.rename(tx.by, absTarget, absFrom, inside); err != nil {
			s.log.Error("could not move a folder back while undoing a change", "from", target, "to", canonFrom, "error", err)
			// The files are at the new place and may be nowhere else: nothing
			// there is removed. The space refuses changes until someone has looked.
			tx.undoFailed = "the folder " + target + " could not be moved back to " + canonFrom + " while undoing a change: " + err.Error()
		}
		// The new names are gone with the rename, and the files Git never had
		// are back under their old names: neither is Git's to remove.
		return append(append([]string{}, newPaths...), untracked...)
	})
	tx.touch(append(append([]string{}, oldPaths...), newPaths...)...)

	if tx.remote {
		// If the change is undone, space.yml goes back before the folder does:
		// a crash between the two leaves the folder at the new place and the
		// bindings at the old, which the "b" record (still open) finishes
		// forwards at the next start. The other order would leave the folder at
		// the old place and the bindings at the new, which nothing repairs.
		tx.undo = append(tx.undo, func() []string {
			s.restoreSpaceFile(tx)
			return nil
		})
	}
	// What was bound to the folder, and to the folders in it, follows it.
	//
	// If that edit fails the folder must not stay at its new place with its
	// bindings at the old one while the request reports failure. In a space that
	// follows a remote the failed change is undone as a whole. In a local space
	// nothing undoes it, so the rename is taken back here: a rename moves
	// nothing but the name, so undoing it cannot lose a file, and the edit of
	// space.yml is atomic, so either it was made (and then it did not fail) or
	// it is as it was. (Finishing the edit instead would only repeat what just
	// failed.) Should the rename not go back either, the "b" record above has
	// the next start move the bindings to where the folder now is.
	if err := s.editBindings(tx, func(root *yaml.Node) (bool, error) { return rebind(root, canonFrom, target, false) }); err != nil {
		if !tx.remote {
			tx.runUndo()
			s.dropCreated(tx)
			if tx.undoFailed != "" {
				err = errors.Join(err, errors.New(tx.undoFailed))
			} else {
				tx.settleBindings() // the folder is where it was, with its bindings
			}
		}
		return "", err
	}
	tx.settleBindings()

	oldDirs, newDirs := []string{canonFrom}, []string{target}
	for _, d := range subdirs {
		oldDirs, newDirs = append(oldDirs, path.Join(canonFrom, d)), append(newDirs, path.Join(target, d))
	}
	if tx.remote {
		tx.dirs = append(append(tx.dirs, oldDirs...), newDirs...)
	}
	// The old names are forgotten outright rather than looked up: under a
	// change of case they still answer on a file system that ignores it.
	if err := s.idx.Forget(oldPages, visibleDirs(oldDirs)); err != nil {
		return "", err
	}
	if _, err := s.idx.Refresh(s.src, newPages, visibleDirs(newDirs)); err != nil {
		return "", err
	}
	if err := s.idx.RetryDuplicates(s.src); err != nil {
		s.log.Warn("could not re-check duplicate ids", "error", err)
	}
	return target, nil
}

// pagesUnder lists the ids of the pages in a folder and below it.
func (s *Space) pagesUnder(folder string) ([]string, error) {
	var ids []string
	q := index.Query{Folder: &folder, Recursive: true, Limit: 200, Sort: "created"}
	for {
		res, err := s.idx.List(q)
		if err != nil {
			return nil, err
		}
		for _, it := range res.Items {
			ids = append(ids, it.ID)
		}
		if res.Next == "" {
			return ids, nil
		}
		q.Cursor = res.Next
	}
}

// DeleteFolder removes a folder. Without recursive it must hold no pages, in
// it or below it (ErrNotEmpty); with it the pages go too. Files that are not
// pages stay, and then so do the folders that hold them. It is one change.
// The ids returned are the pages that were deleted and stay deleted: all of
// them on success; on a failure, the ones a local space had already deleted
// (it has no undo), and none in a space that follows a remote (it has).
func (s *Space) DeleteFolder(by Author, rel string, recursive bool) ([]string, error) {
	if err := checkRel(rel); err != nil {
		return nil, err
	}
	if rel == "" {
		return nil, invalidf("the root cannot be removed")
	}
	var deleted []string
	_, err := mutate(s, by, func(tx *txn) (struct{}, error) {
		deleted = deleted[:0] // the change may be made again on a newer remote
		canon, existed, err := s.resolveFolder(rel)
		if errors.Is(err, ErrExists) || (err == nil && !existed) {
			return struct{}{}, ErrNoFolder
		}
		if err != nil {
			return struct{}{}, err
		}
		abs, err := s.abs(canon)
		if err != nil {
			return struct{}{}, err
		}
		// Journaled before the first deletion: the bindings of what is gone go
		// too, at recovery if the crash comes before the edit of space.yml (see "u").
		startU := func() error {
			if len(tx.bind) > 0 {
				return nil
			}
			u := jrec{Op: "u", Path: canon}
			if err := tx.record(u); err != nil {
				return err
			}
			tx.bind = append(tx.bind, u)
			return nil
		}
		// Deleting a page can turn another file with the same id into the
		// page, so the folder is looked at again until nothing is left or a
		// round deletes nothing.
		for round := 0; ; round++ {
			ids, err := s.pagesUnder(canon)
			if err != nil {
				return struct{}{}, err
			}
			if len(ids) == 0 {
				break
			}
			if !recursive {
				return struct{}{}, ErrNotEmpty
			}
			if round > maxNameTries {
				return struct{}{}, errors.New("the folder keeps showing pages after they were deleted")
			}
			if err := startU(); err != nil {
				return struct{}{}, err
			}
			for _, id := range ids {
				if err := s.deleteLocked(tx, id); err != nil {
					return struct{}{}, err
				}
				deleted = append(deleted, id)
			}
		}
		if err := startU(); err != nil {
			return struct{}{}, err
		}
		// The folders, deepest first: their placeholders go, and each folder
		// that is empty then goes too.
		dirs := []string{canon}
		werr := filepath.WalkDir(abs, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() && p != abs && e.Type()&fs.ModeSymlink == 0 {
				r, _ := filepath.Rel(abs, p)
				dirs = append(dirs, path.Join(canon, filepath.ToSlash(r)))
			}
			return nil
		})
		if werr != nil {
			return struct{}{}, werr
		}
		sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
		// Git does not keep empty folders: an undo puts back the ones removed here.
		var removed []string
		tx.undo = append(tx.undo, func() []string {
			for i := len(removed) - 1; i >= 0; i-- {
				os.MkdirAll(filepath.Join(s.root, filepath.FromSlash(removed[i])), 0o755)
			}
			return nil
		})
		for _, d := range dirs {
			keep := path.Join(d, ".gitkeep")
			absKeep := filepath.Join(s.root, filepath.FromSlash(keep))
			if fi, err := os.Lstat(absKeep); err == nil && fi.Mode().IsRegular() {
				if err := tx.record(jrec{Op: "d", Path: keep}); err != nil {
					return struct{}{}, err
				}
				tx.touch(keep)
				if err := os.Remove(absKeep); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return struct{}{}, err
				}
			}
			// Fails, rightly, for a folder that still holds something.
			if os.Remove(filepath.Join(s.root, filepath.FromSlash(d))) == nil {
				removed = append(removed, d)
			}
		}
		// A folder that is gone has no type or views to keep. One that stays (it holds
		// files that are not pages) keeps its binding.
		if err := s.editBindings(tx, func(root *yaml.Node) (bool, error) {
			return forgetFolders(root, func(folder string) bool {
				return (folder == canon || strings.HasPrefix(folder, canon+"/")) && s.fileGone(folder)
			})
		}); err != nil {
			// Pages are deleted for good in a local space, so there is nothing
			// to take back; the error says so (see the ids returned), and the
			// "u" record has the next start drop the bindings of what is gone.
			return struct{}{}, err
		}
		tx.settleBindings()
		if tx.remote {
			tx.dirs = append(tx.dirs, dirs...)
		}
		if _, err := s.idx.Refresh(s.src, nil, visibleDirs(dirs)); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	if err != nil && s.IsRemote() {
		return nil, err
	}
	return append([]string(nil), deleted...), err
}

// checkBatch validates the ids of a batch: 1 to MaxBatch, each a page id, each once.
func checkBatch(ids []string) error {
	if len(ids) == 0 || len(ids) > MaxBatch {
		return invalidf("give 1 to %d page ids", MaxBatch)
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !page.ValidID(id) {
			return invalidf("%q is not a page id", id)
		}
		if seen[id] {
			return invalidf("page %s is listed twice", id)
		}
		seen[id] = true
	}
	return nil
}

// MaxBatch is the most pages one batch may name.
const MaxBatch = 500

// BatchMove moves pages into folder as one change. A page already there is
// left where it is. ErrNotFound when one of the ids is not a page; nothing is
// changed then.
func (s *Space) BatchMove(by Author, ids []string, folder string) error {
	if err := checkBatch(ids); err != nil {
		return err
	}
	if err := checkRel(folder); err != nil {
		return err
	}
	for _, id := range ids {
		s.refresh(id)
	}
	_, err := mutate(s, by, func(tx *txn) (struct{}, error) {
		for _, id := range ids {
			if _, _, _, err := s.load(id, false); err != nil {
				return struct{}{}, err
			}
		}
		// Something that is not a folder where the folder should be: said
		// before any page has moved.
		if _, _, err := s.resolveFolder(folder); errors.Is(err, ErrExists) {
			return struct{}{}, invalidf("%q is not a folder: a file has that name", folder)
		} else if err != nil {
			return struct{}{}, err
		}
		for _, id := range ids {
			if _, err := s.moveLocked(tx, id, folder); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

// BatchDelete deletes pages as one change. ErrNotFound when one of the ids is
// not a page; nothing is changed then. The ids returned are the pages that
// were deleted and stay deleted (see DeleteFolder).
func (s *Space) BatchDelete(by Author, ids []string) ([]string, error) {
	if err := checkBatch(ids); err != nil {
		return nil, err
	}
	for _, id := range ids {
		s.refresh(id)
	}
	var deleted []string
	_, err := mutate(s, by, func(tx *txn) (struct{}, error) {
		deleted = deleted[:0]
		for _, id := range ids {
			if _, _, _, err := s.load(id, false); err != nil {
				return struct{}{}, err
			}
		}
		for _, id := range ids {
			if err := s.deleteLocked(tx, id); err != nil {
				return struct{}{}, err
			}
			deleted = append(deleted, id)
		}
		return struct{}{}, nil
	})
	if err != nil && s.IsRemote() {
		return nil, err
	}
	return append([]string(nil), deleted...), err
}
