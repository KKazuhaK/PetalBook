package space

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// otherFileSystem is a folder for a Git directory on another file system than
// the temporary folder of the tests, where the machine has one (/dev/shm);
// else a folder of its own, and a file written in the work tree has to be
// renamed there just the same.
func otherFileSystem(t *testing.T) string {
	t.Helper()
	if d, err := os.MkdirTemp("/dev/shm", "petalbook-gitdir-"); err == nil {
		t.Cleanup(func() { os.RemoveAll(d) })
		return d
	}
	return t.TempDir()
}

// tempsUnder lists the files under root (not .git) that are named as the
// server names a file it is still writing (isTempName).
func tempsUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if isTempName(d.Name()) {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

// everyKindOfWrite makes, in s, each kind of change that writes a file of the
// space: a page made, saved and moved, a type, a folder bound to it, a tag
// colour, an attachment, a folder made and moved. Each must work, and none may
// leave a file of its own behind or in a commit.
func everyKindOfWrite(t *testing.T, s *Space, n string) {
	t.Helper()
	p := mustCreate(t, s, "", meta("title", "Written"+n), "x\n")
	p, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "Written"+n), "y\n")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "Written again"+n), "z\n"); err != nil {
		t.Fatalf("save with a new title: %v", err)
	}
	if _, err := s.PutType(Author{}, "note"+n, "", TypeInput{Title: "Note"}); err != nil {
		t.Fatalf("type: %v", err)
	}
	if _, err := s.CreateFolder(Author{}, "Box"+n); err != nil {
		t.Fatalf("folder: %v", err)
	}
	if _, err := s.BindFolder(Author{}, "Box"+n, "note"+n); err != nil {
		t.Fatalf("binding: %v", err)
	}
	if err := s.SetTagColor(Author{}, "t", "red"); err != nil {
		t.Fatalf("tag colour: %v", err)
	}
	if _, err := s.PutAsset(Author{}, p.ID, "a.txt", []byte("attached\n")); err != nil {
		t.Fatalf("attachment: %v", err)
	}
	if _, err := s.Move(Author{}, p.ID, "Box"+n); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := s.MoveFolder(Author{}, "Box"+n, "Crate"+n); err != nil {
		t.Fatalf("folder move: %v", err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if left := tempsUnder(t, s.Root()); len(left) > 0 {
		t.Errorf("files of the server left behind: %v", left)
	}
	if files := git(t, s.Root(), "log", "--all", "--name-only", "--format="); strings.Contains(files, tempMark[1:]) {
		t.Errorf("a file of the server was committed:\n%s", files)
	}
}

// A pull can bring a link, or a file, named as the folder an earlier build
// wrote its files in before they were renamed into place (.petalbook-tmp);
// with the Git directory on another file system every save then failed
// (EXDEV) for good. Files are written in their own folder now: whatever is
// at that name is the repository's, never followed, moved or removed.
func TestAPulledPetalbookTmpLinkOrFileStopsNoSave(t *testing.T) {
	t.Parallel()
	needGit(t)
	for _, kind := range []string{"link", "file"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "space")
			os.MkdirAll(root, 0o755)
			gd := filepath.Join(otherFileSystem(t), "repo.git")
			git(t, t.TempDir(), "init", "-q", "--separate-git-dir", gd, root)
			out := outsideDir(t)
			before := treeOf(t, out)
			at := filepath.Join(root, ".petalbook-tmp")
			if kind == "link" {
				if err := os.Symlink(out, at); err != nil {
					t.Fatal(err)
				}
			} else {
				os.WriteFile(at, []byte("theirs\n"), 0o644)
			}
			gitO(t, root, "add", ".petalbook-tmp")
			gitO(t, root, "commit", "-qm", "pulled")
			for i := range 2 { // and again after a restart
				s := openAt(t, root)
				everyKindOfWrite(t, s, fmt.Sprint(i))
				s.Close()
			}
			untouched(t, out, before)
			if kind == "link" {
				if to, err := os.Readlink(at); err != nil || to != out {
					t.Errorf("the link: %q %v", to, err)
				}
			} else if b, _ := os.ReadFile(at); string(b) != "theirs\n" {
				t.Errorf("the file: %q", b)
			}
			if st := git(t, root, "status", "--porcelain", "--untracked-files=all"); st != "" {
				t.Errorf("the work tree is not clean:\n%s", st)
			}
			if _, err := os.Lstat(filepath.Join(gd, "petalbook-tmp")); err == nil {
				t.Error("a folder of temporary files was made in the Git directory")
			}
		})
	}
}

// What a crash left of a write is removed when the space opens, in a folder
// of pages, in the attachments and in the settings; a file of the repository
// is never removed, whatever its name, and none is left changed.
func TestLeftHalfWrittenFilesGoButNoFileOfTheRepository(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "Notes", meta("title", "Kept"), "x\n")
	if _, err := s.PutAsset(Author{}, p.ID, "a.txt", []byte("a\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	theirs := []string{"Notes/.petalbook-w-0123456789abcdef", ".petalbook/.space.yml.petalbook-w-00000000000000aa", "assets/" + p.ID + "/.petalbook-w-00000000000000bb"}
	left := []string{"Notes/.petalbook-w-1111111111111111", "assets/" + p.ID + "/.petalbook-w-2222222222222222",
		".petalbook/.space.yml.petalbook-w-3333333333333333", "Deep/er/.petalbook-w-4444444444444444"}
	for _, rel := range append(append([]string{}, theirs...), left...) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("half\n"), 0o644)
	}
	gitO(t, root, "add", "-f", "--", theirs[0], theirs[1], theirs[2])
	gitO(t, root, "commit", "-qm", "files of theirs")

	s = openAt(t, root)
	defer s.Close()
	for _, rel := range theirs {
		if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))); err != nil || string(b) != "half\n" {
			t.Errorf("%s of the repository: %q %v", rel, b, err)
		}
	}
	for _, rel := range left {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s was left", rel)
		}
	}
	if st := git(t, root, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Errorf("the work tree is not clean:\n%s", st)
	}
	// Not pages, not listed.
	if got, _ := s.Get(p.ID); got == nil || got.Path != "Notes/Kept.md" {
		t.Errorf("the page: %+v", got)
	}
	if ps, _ := s.Problems(); len(ps) != 0 {
		t.Errorf("problems: %+v", ps)
	}
}

// A file the server is writing is never taken by a commit, also by a change
// whose commit names a whole folder (a folder moved): git is told to leave
// those names out.
func TestAFileBeingWrittenIsNeverCommitted(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	defer s.Close()
	mustCreate(t, s, "Box", meta("title", "In the box"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// As if a write were under way while the folder is moved.
	os.WriteFile(filepath.Join(root, "Box", ".petalbook-w-5555555555555555"), []byte("half"), 0o600)
	if _, err := s.MoveFolder(Author{}, "Box", "Crate"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if files := git(t, root, "log", "--all", "--name-only", "--format="); strings.Contains(files, tempMark[1:]) {
		t.Errorf("a file being written was committed:\n%s", files)
	}
	if st := git(t, root, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Errorf("git lists it:\n%s", st)
	}
}

// In a space that follows a remote, files are written the same way: a pulled
// .petalbook-tmp link stops nothing, and nothing of the server's is pushed.
func TestARemoteSpaceWritesBesideAPulledPetalbookTmpLink(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	mustCreate(t, f.s, "", meta("title", "Mine"), "x\n")
	out := outsideDir(t)
	before := treeOf(t, out)
	o := f.theirs()
	if err := os.Symlink(out, filepath.Join(o.dir, ".petalbook-tmp")); err != nil {
		t.Fatal(err)
	}
	o.push("a link")
	if err := f.s.Pull(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	everyKindOfWrite(t, f.s, "1")
	f.reopen()
	everyKindOfWrite(t, f.s, "2")
	untouched(t, out, before)
	if files := git(t, f.remote, "log", "--all", "--name-only", "--format="); strings.Contains(files, tempMark[1:]) {
		t.Errorf("a file of the server was pushed:\n%s", files)
	}
	if st := git(t, f.root, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Errorf("the clone is not clean:\n%s", st)
	}
}

// --- the state databases of an earlier version, kept beside the current one -----------------

// earlierBeside makes a space whose state directory is in use, with an
// earlier version's state in .petalbook/cache: a database and its log.
func earlierBeside(t *testing.T) (root, state, old string) {
	t.Helper()
	needGit(t)
	root = t.TempDir()
	s := openAt(t, root)
	mustCreate(t, s, "", meta("title", "Kept"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	state = DefaultStateDir(root)
	old = filepath.Join(root, ".petalbook", "cache")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, "state.db"), []byte("earlier"), 0o644)
	os.WriteFile(filepath.Join(old, "state.db-wal"), []byte("earlier-wal"), 0o644)
	return root, state, old
}

// A crash while the family is copied beside the current state leaves no
// database without its log: the database is copied last, and what a crash
// left before it is removed at the next start (the originals are still where
// they were), which keeps one whole copy, so the import never takes an older
// text for the newer one.
func TestACrashBetweenTheCopiesOfADatabaseAndItsLogLosesNothing(t *testing.T) {
	t.Parallel()
	root, state, old := earlierBeside(t)
	// The copy of the database fails after its log was copied: as a crash
	// there, the database is not taken.
	main := filepath.Join(old, "state.db")
	os.Rename(main, main+".away")
	o, err := os.OpenRoot(old)
	if err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.NewTextHandler(discard{}, nil))
	err = mergeOld(o, []string{"state.db", "state.db-wal"}, state, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), quiet)
	o.Close()
	if err == nil {
		t.Fatal("the copy did not fail")
	}
	if m, _ := filepath.Glob(filepath.Join(state, EarlierStatePattern)); len(m) != 0 {
		t.Fatalf("a database was kept without its log: %v", m)
	}
	if b, _ := os.ReadFile(filepath.Join(old, "state.db-wal")); string(b) != "earlier-wal" {
		t.Fatalf("the original log: %q", b)
	}
	os.Rename(main+".away", main)

	s := openAt(t, root)
	defer s.Close()
	dbs := s.EarlierStateDBs()
	if len(dbs) != 1 {
		t.Fatalf("earlier state databases: %v", dbs)
	}
	if b, _ := os.ReadFile(dbs[0]); string(b) != "earlier" {
		t.Errorf("the database: %q", b)
	}
	if b, _ := os.ReadFile(dbs[0] + "-wal"); string(b) != "earlier-wal" {
		t.Errorf("its log: %q", b)
	}
	if m, _ := filepath.Glob(filepath.Join(state, "state-earlier-*")); len(m) != 2 {
		t.Errorf("what is kept: %v", m)
	}
	if _, err := os.Lstat(old); err == nil {
		t.Error(".petalbook/cache is still there")
	}
}

// A crash while an imported database is renamed (the database first) leaves
// its log under the old name: it follows the database when the space opens.
func TestALogLeftByAnImportFollowsItsDatabase(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	firstCommit(t, root)
	state := DefaultStateDir(root)
	os.WriteFile(filepath.Join(state, "state-imported-20260101T000000Z.db"), []byte("db"), 0o644)
	os.WriteFile(filepath.Join(state, "state-earlier-20260101T000000Z.db-wal"), []byte("wal"), 0o644)
	s := openAt(t, root)
	defer s.Close()
	if b, _ := os.ReadFile(filepath.Join(state, "state-imported-20260101T000000Z.db-wal")); string(b) != "wal" {
		t.Errorf("the log did not follow its database: %q", b)
	}
	if dbs := s.EarlierStateDBs(); len(dbs) != 0 {
		t.Errorf("earlier state databases: %v", dbs)
	}
}

// When the repository has the database an earlier version kept, its log
// beside it is not the server's to move: replayed into another database it
// would be another one's text. The whole family stays as it is.
func TestATrackedDatabaseKeepsItsUntrackedLog(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	firstCommit(t, root)
	old := filepath.Join(root, ".petalbook", "cache")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, "state.db"), []byte("theirs"), 0o644)
	gitO(t, root, "add", "-f", ".petalbook/cache/state.db")
	gitO(t, root, "commit", "-qm", "a database")
	os.WriteFile(filepath.Join(old, "state.db-wal"), []byte("a log"), 0o644)
	os.WriteFile(filepath.Join(old, "index.db-wal"), []byte("a log of nothing"), 0o644)
	os.RemoveAll(DefaultStateDir(root))
	s := openAt(t, root)
	defer s.Close()
	for _, n := range []string{"state.db", "state.db-wal", "index.db-wal"} {
		if _, err := os.Lstat(filepath.Join(s.StateDir(), n)); err == nil && n != "index.db-wal" {
			t.Errorf("%s was moved into the state", n)
		}
	}
	for n, want := range map[string]string{"state.db": "theirs", "state.db-wal": "a log", "index.db-wal": "a log of nothing"} {
		if b, err := os.ReadFile(filepath.Join(old, n)); err != nil || string(b) != want {
			t.Errorf("%s: %q %v", n, b, err)
		}
	}
	if st := git(t, root, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("a change nobody made:\n%s", st)
	}
}
