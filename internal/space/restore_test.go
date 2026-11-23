package space

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// --- a legitimate file is never taken for one that cannot be used -----------------

func TestAnEntrySharedByTenThousandFoldersCostsWhatIsWritten(t *testing.T) {
	const n = 10000
	shared := "shared: &tmpl\n  type: wenan\n  views:\n" +
		"    - {id: aaa111, name: one, layout: board, filter: [{key: series, op: in, values: [a, b]}]}\n" +
		"    - {id: aaa222, name: two, layout: table}\n"
	for _, c := range []struct{ name, entry string }{
		{"by alias", "*tmpl"},
		{"through a merge key", "{<<: *tmpl, x: 1}"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := open(t)
			if _, err := putCurrent(s, "wenan", allKinds()); err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{"f00001", "f00002", "f00003"} {
				mustCreate(t, s, f, meta("title", f), "x\n")
			}
			var b strings.Builder
			b.WriteString(shared + "folders:\n")
			for i := 0; i < n; i++ {
				fmt.Fprintf(&b, "  f%05d: %s\n", i, c.entry)
			}
			writeSpaceYML(t, s, b.String())
			timed(t, "reading the bindings and views of 10000 folders", func() {
				if got := len(s.Bindings()); got != n {
					t.Errorf("%d bindings", got)
				}
				if got := viewNames(t, s, folderScope("f09999")); got != "one,two" {
					t.Errorf("views: %q", got)
				}
			})
			if notes := s.SettingsNotices(); len(notes) != 0 {
				t.Fatalf("the file was taken for one that cannot be used: %+v", notes)
			}
			// Writes too: one folder, one that is moved, one that is deleted,
			// and all of them at once.
			timed(t, "unbinding one of them", func() {
				if _, err := s.BindFolder(Author{}, "f00001", ""); err != nil {
					t.Error(err)
				}
			})
			timed(t, "moving one of them", func() {
				if _, err := s.MoveFolder(Author{}, "f00002", "moved"); err != nil {
					t.Error(err)
				}
			})
			timed(t, "deleting one of them", func() {
				if _, err := s.DeleteFolder(Author{}, "f00003", true); err != nil {
					t.Error(err)
				}
			})
			m := bindingMap(s)
			if len(m) != n-2 || m["f00001"] != "" || m["f00002"] != "" || m["moved"] != "wenan" || m["f00003"] != "" || m["f00004"] != "wenan" {
				t.Errorf("%d bindings; moved: %q", len(m), m["moved"])
			}
			if got := viewNames(t, s, folderScope("f00001")); got != "one,two" {
				t.Errorf("the views of the folder that was unbound: %q", got)
			}
			if got := viewNames(t, s, folderScope("f00003")); got != "" {
				t.Errorf("the views of the folder that was deleted: %q", got)
			}
			// The type goes from all of them, and what they shared stays shared:
			// by alias, the first of them carries it under an anchor of its own
			// and the others refer to that; through a merge key, each folder
			// has the views written out for itself, which here is more than
			// the file holds.
			before := len(spaceYML(s.Root()))
			if c.entry == "*tmpl" {
				timed(t, "deleting the type of all of them", func() {
					if err := deleteCurrent(s, "wenan"); err != nil {
						t.Error(err)
					}
				})
				got := spaceYML(s.Root())
				if len(s.Bindings()) != 0 || viewNames(t, s, folderScope("f09999")) != "one,two" || viewNames(t, s, folderScope("f00004")) != "one,two" {
					t.Errorf("%d bindings are left; views %q", len(s.Bindings()), viewNames(t, s, folderScope("f09999")))
				}
				if len(got) > before+before/2 || !strings.Contains(got, "*tmpl-no-type") {
					t.Errorf("the file went from %d to %d bytes", before, len(got))
				}
				if !reflect.DeepEqual(meaningOf(t, got)["shared"], meaningOf(t, shared)["shared"]) {
					t.Error("what the entry was shared from changed")
				}
				return
			}
			timed(t, "deleting the type of all of them", func() {
				wantInvalid(t, "deleting the type", deleteCurrent(s, "wenan"), "larger than")
			})
			// With an entry that is the type and nothing else, it fits.
			writeSpaceYML(t, s, strings.Replace(spaceYML(s.Root()), shared, "shared: &tmpl\n  type: wenan\n", 1))
			timed(t, "deleting the type of all of them, with nothing else shared", func() {
				if err := deleteCurrent(s, "wenan"); err != nil {
					t.Error(err)
				}
			})
			if len(s.Bindings()) != 0 {
				t.Errorf("%d bindings are left", len(s.Bindings()))
			}
		})
	}
}

// --- a file that cannot be used ---------------------------------------------------

// settled is a space with settings in its history: A is bound and A/Sub has a
// view, B, C and D have nothing.
func settled(t *testing.T) *Space {
	t.Helper()
	needGit(t)
	s := openAt(t, t.TempDir())
	if _, err := putCurrent(s, "wenan", allKinds()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"A", "A/Sub", "B", "C", "D"} {
		mustCreate(t, s, f, meta("title", f), "x\n")
	}
	if _, err := s.BindFolder(Author{}, "A", "wenan"); err != nil {
		t.Fatal(err)
	}
	mustPutViews(t, s, folderScope("A/Sub"), View{Name: "one", Layout: "list"})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	return s
}

// restart is the server stopping without a word and starting again.
func restart(t *testing.T, s *Space) *Space {
	t.Helper()
	root := s.Root()
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	crash(s)
	return openAt(t, root)
}

func noticeOf(s *Space, file string, unreadable bool) *SettingsNotice {
	for _, n := range s.SettingsNotices() {
		if n.File == file && n.Unreadable == unreadable {
			return &n
		}
	}
	return nil
}

const notYAML = "folders: [unclosed\n"

func TestAFileThatIsNotYAMLAfterARestartIsRestoredFromTheHistory(t *testing.T) {
	s := settled(t)
	root := s.Root()
	good := git(t, root, "rev-parse", "HEAD")
	crash(s)
	setSpaceYML(t, root, notYAML)
	s = openAt(t, root)

	// Reads go on with the settings of the newest version that can be used.
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A": "wenan"}) {
		t.Fatalf("the bindings are %v", m)
	}
	if got := viewNames(t, s, folderScope("A/Sub")); got != "one" {
		t.Errorf("the views are %q", got)
	}
	n := noticeOf(s, spaceFile, true)
	if n == nil || n.RestoredFrom == nil || n.RestoredFrom.Commit != good || n.Since == nil || n.Since.Time.IsZero() || n.Copy != "" {
		t.Fatalf("the notice: %+v", s.SettingsNotices())
	}
	// A read writes nothing.
	if spaceYML(root) != notYAML || len(copiesOf(root, spaceSettings)) != 0 {
		t.Fatal("reading wrote to the space")
	}
	// A folder without settings is moved and deleted, and nothing needs writing.
	if _, err := s.MoveFolder(Author{}, "B", "B2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteFolder(Author{}, "C", true); err != nil {
		t.Fatal(err)
	}
	if spaceYML(root) != notYAML {
		t.Error("a change that had nothing to write wrote the file")
	}
	// A folder with settings is moved, and they follow it: the file is put
	// right in the same change, and what it was is kept in the same commit.
	if _, err := s.MoveFolder(Author{}, "A", "Moved"); err != nil {
		t.Fatal(err)
	}
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"Moved": "wenan"}) {
		t.Errorf("after the move: %v", m)
	}
	if got := viewNames(t, s, folderScope("Moved/Sub")); got != "one" {
		t.Errorf("the views after the move: %q", got)
	}
	if _, err := parseYAMLDoc([]byte(spaceYML(root)), errSpaceFile); err != nil {
		t.Errorf("space.yml is still not usable:\n%s", spaceYML(root))
	}
	kept := copiesOf(root, spaceSettings)
	if len(kept) != 1 || readDisk(t, s, kept[0]) != notYAML || !strings.HasPrefix(kept[0], ".petalbook/space.yml.unreadable-") || !strings.HasSuffix(kept[0], "Z") {
		t.Fatalf("what the file was: %v", kept)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if files := git(t, root, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(files, kept[0]) || !strings.Contains(files, ".petalbook/space.yml\n") && !strings.HasSuffix(files, ".petalbook/space.yml") {
		t.Errorf("the copy and the file are not in one commit:\n%s", files)
	}
	n = noticeOf(s, spaceFile, false)
	if n == nil || n.Copy != kept[0] || n.RestoredFrom == nil || n.RestoredFrom.Commit != good || n.RepairedAt == nil || noticeOf(s, spaceFile, true) != nil {
		t.Fatalf("the notice after the repair: %+v", s.SettingsNotices())
	}
	// A folder with settings is deleted, and they go.
	if _, err := s.DeleteFolder(Author{}, "Moved", true); err != nil {
		t.Fatal(err)
	}
	if len(s.Bindings()) != 0 || strings.Contains(spaceYML(root), "Moved") {
		t.Errorf("after the delete:\n%s", spaceYML(root))
	}

	// The notice is still there after another restart, and goes with the copy.
	s = restart(t, s)
	if n = noticeOf(s, spaceFile, false); n == nil || n.Copy != kept[0] || n.RestoredFrom == nil || n.RestoredFrom.Commit != good {
		t.Fatalf("the notice after a restart: %+v", s.SettingsNotices())
	}
	os.Remove(filepath.Join(root, filepath.FromSlash(kept[0])))
	if got := s.SettingsNotices(); len(got) != 0 {
		t.Errorf("the notice outlived the copy: %+v", got)
	}
}

func TestTheSameUnusableFileIsKeptOnce(t *testing.T) {
	s := settled(t)
	root := s.Root()
	for round := 0; round < 3; round++ {
		// It comes back each time (a pull brings it, say).
		setSpaceYML(t, root, notYAML)
		if m := bindingMap(s); m["A"] != "wenan" || len(m) != 1+round {
			t.Fatalf("round %d: the bindings are %v", round, m)
		}
		f := []string{"B", "C", "D"}[round]
		if _, err := s.BindFolder(Author{}, f, "wenan"); err != nil {
			t.Fatal(err)
		}
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
		if kept := copiesOf(root, spaceSettings); len(kept) != 1 {
			t.Fatalf("round %d: %d copies: %v", round, len(kept), kept)
		}
	}
	if m := bindingMap(s); len(m) != 4 {
		t.Errorf("the bindings are %v", m)
	}
	// Another content is another copy.
	setSpaceYML(t, root, "- a list\n")
	if _, err := s.BindFolder(Author{}, "B", ""); err != nil {
		t.Fatal(err)
	}
	if kept := copiesOf(root, spaceSettings); len(kept) != 2 {
		t.Errorf("%d copies: %v", len(kept), kept)
	}
	if got := s.SettingsNotices(); len(got) != 2 {
		t.Errorf("the notices: %+v", got)
	}
	if st := git(t, root, "status", "--porcelain"); s.Flush() != nil || strings.Contains(git(t, root, "status", "--porcelain"), "unreadable") {
		t.Errorf("a copy is not committed:\n%s", st)
	}
}

func TestASpaceWhoseHistoryHasNoUsableSettingsStartsEmpty(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	setSpaceYML(t, root, notYAML)
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "add", "-A")
	git(t, root, "-c", "user.name=x", "-c", "user.email=x@example.test", "commit", "-qm", "broken from the start")
	s := openAt(t, root)
	if _, err := putCurrent(s, "wenan", allKinds()); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "A", meta("title", "a"), "x\n")
	if len(s.Bindings()) != 0 {
		t.Fatalf("%v", s.Bindings())
	}
	n := noticeOf(s, spaceFile, true)
	if n == nil || n.RestoredFrom != nil || n.Since == nil || n.Since.Commit == "" {
		t.Fatalf("the notice: %+v", s.SettingsNotices())
	}
	if _, err := s.MoveFolder(Author{}, "A", "A2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindFolder(Author{}, "A2", "wenan"); err != nil {
		t.Fatal(err)
	}
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A2": "wenan"}) {
		t.Errorf("%v\n%s", m, spaceYML(root))
	}
	kept := copiesOf(root, spaceSettings)
	if len(kept) != 1 || readDisk(t, s, kept[0]) != notYAML {
		t.Fatalf("what the file was: %v", kept)
	}
	if n = noticeOf(s, spaceFile, false); n == nil || n.RestoredFrom != nil || n.Copy != kept[0] {
		t.Errorf("the notice after the repair: %+v", s.SettingsNotices())
	}
}

func TestEveryKindOfUnusableFileIsRestored(t *testing.T) {
	for _, c := range []struct {
		name string
		make func(t *testing.T, file string)
	}{
		{"folders is a list", func(t *testing.T, file string) { os.WriteFile(file, []byte("folders: [a, b]\n"), 0o644) }},
		{"an alias inside its own anchor", func(t *testing.T, file string) { os.WriteFile(file, []byte("a: &a [*a]\nfolders: {}\n"), 0o644) }},
		{"two documents", func(t *testing.T, file string) { os.WriteFile(file, []byte("name: x\n---\nname: y\n"), 0o644) }},
		{"larger than the server reads", func(t *testing.T, file string) {
			os.WriteFile(file, []byte("name: x\n# "+strings.Repeat("x", maxSpaceFile)+"\n"), 0o644)
		}},
		{"a folder", func(t *testing.T, file string) {
			os.Remove(file)
			os.Mkdir(file, 0o755)
			os.WriteFile(filepath.Join(file, "inside.txt"), []byte("x\n"), 0o644)
		}},
		{"a link", func(t *testing.T, file string) {
			os.Remove(file)
			if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere.yml"), file); err != nil {
				t.Skip(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := settled(t)
			root := s.Root()
			file := filepath.Join(root, ".petalbook", "space.yml")
			c.make(t, file)
			was, _ := os.Lstat(file)
			var m map[string]string
			bounded(t, "reading", func() { m = bindingMap(s) })
			if !reflect.DeepEqual(m, map[string]string{"A": "wenan"}) {
				t.Fatalf("the bindings are %v", m)
			}
			if noticeOf(s, spaceFile, true) == nil {
				t.Errorf("no notice: %+v", s.SettingsNotices())
			}
			if _, err := s.MoveFolder(Author{}, "A", "A2"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DeleteFolder(Author{}, "A2/Sub", true); err != nil {
				t.Fatal(err)
			}
			if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A2": "wenan"}) || strings.Contains(spaceYML(root), "Sub") {
				t.Errorf("after the move and the delete: %v\n%s", m, spaceYML(root))
			}
			kept := copiesOf(root, spaceSettings)
			if len(kept) != 1 {
				t.Fatalf("what the file was: %v", kept)
			}
			if fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(kept[0]))); err != nil || fi.Mode().Type() != was.Mode().Type() || fi.Size() != was.Size() {
				t.Errorf("the copy is not the file that was there: %v", err)
			}
			if err := s.Flush(); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestATagsFileThatCannotBeUsedIsRestoredToo(t *testing.T) {
	needGit(t)
	s := openAt(t, t.TempDir())
	root := s.Root()
	if err := s.SetTagColor(Author{}, "a", "red"); err != nil {
		t.Fatal(err)
	}
	mustPutViews(t, s, tagScope("a"), View{Name: "one", Layout: "list"})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s = restart(t, s)
	setTagsYML(t, root, "tags: {a: [unclosed\n")
	if c := s.TagColors(); c["a"] != "red" || viewNames(t, s, tagScope("a")) != "one" {
		t.Fatalf("colours %v, views %q", c, viewNames(t, s, tagScope("a")))
	}
	if n := noticeOf(s, tagsFile, true); n == nil || n.RestoredFrom == nil {
		t.Errorf("the notice: %+v", s.SettingsNotices())
	}
	if err := s.SetTagColor(Author{}, "b", "blue"); err != nil {
		t.Fatal(err)
	}
	if c := s.TagColors(); c["a"] != "red" || c["b"] != "blue" || viewNames(t, s, tagScope("a")) != "one" {
		t.Errorf("after the write: %v\n%s", c, tagsYML(root))
	}
	kept := copiesOf(root, tagsSettings)
	if len(kept) != 1 || !strings.HasPrefix(kept[0], ".petalbook/tags.yml.unreadable-") {
		t.Fatalf("what the file was: %v", kept)
	}
	if n := noticeOf(s, tagsFile, false); n == nil || n.Copy != kept[0] {
		t.Errorf("the notice after the repair: %+v", s.SettingsNotices())
	}
}

// --- recovery after a crash -------------------------------------------------------

func TestRecoveryRestoresAFileThatCannotBeUsed(t *testing.T) {
	s := settled(t)
	root := s.Root()
	crash(s)
	// A was moved and A/Sub... D deleted on disk when the server stopped; the
	// edits of space.yml were not made, and the file cannot be used.
	if err := os.Rename(filepath.Join(root, "A"), filepath.Join(root, "A2")); err != nil {
		t.Fatal(err)
	}
	setSpaceYML(t, root, notYAML)
	recs := `{"op":"b","f":"A","to":"A2"}` + "\n" + `{"op":"u","p":"D"}` + "\n"
	if err := os.WriteFile(journalFile(root), []byte(recs), 0o644); err != nil {
		t.Fatal(err)
	}
	var s2 *Space
	bounded(t, "opening the space", func() { s2 = openAt(t, root) })
	if m := bindingMap(s2); !reflect.DeepEqual(m, map[string]string{"A2": "wenan"}) {
		t.Fatalf("after recovery: %v\n%s", m, spaceYML(root))
	}
	if got := viewNames(t, s2, folderScope("A2/Sub")); got != "one" {
		t.Errorf("the views after recovery: %q", got)
	}
	kept := copiesOf(root, spaceSettings)
	if len(kept) != 1 || readDisk(t, s2, kept[0]) != notYAML {
		t.Fatalf("what the file was: %v", kept)
	}
	if n := noticeOf(s2, spaceFile, false); n == nil || n.Copy != kept[0] || n.RestoredFrom == nil {
		t.Errorf("the notice: %+v", s2.SettingsNotices())
	}
	// Both files are committed with what recovery found.
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := git(t, root, "status", "--porcelain"); strings.Contains(st, "space.yml") {
		t.Errorf("not committed:\n%s", st)
	}
	// And the space works.
	if _, err := s2.MoveFolder(Author{}, "A2", "A3"); err != nil {
		t.Error(err)
	}
}

// --- a space that follows a remote ------------------------------------------------

func TestAnUnusableFileThatAPullKeepsBringingIsPutRightAndPushed(t *testing.T) {
	f := newRemoteSpace(t)
	s := f.s
	if _, err := putCurrent(s, "wenan", allKinds()); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"A", "B", "C"} {
		mustCreate(t, s, d, meta("title", d), "x\n")
	}
	if _, err := s.BindFolder(Author{}, "A", "wenan"); err != nil {
		t.Fatal(err)
	}
	o := f.theirs()
	for round, folder := range []string{"B", "C"} {
		gitO(t, o.dir, "pull", "-q", "origin", "main")
		o.write(".petalbook/space.yml", notYAML)
		o.push("break the settings")
		if err := s.Pull(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		if spaceYML(f.root) != notYAML {
			t.Fatal("the pull did not bring the file")
		}
		before := len(f.remoteLog())
		if m := bindingMap(s); m["A"] != "wenan" || len(m) != 1+round {
			t.Fatalf("round %d: the bindings are %v", round, m)
		}
		// Reading pushed nothing.
		if len(f.remoteLog()) != before {
			t.Fatal("a read made a commit")
		}
		if _, err := s.BindFolder(Author{}, folder, "wenan"); err != nil {
			t.Fatal(err)
		}
		if m := bindingMap(s); m[folder] != "wenan" || len(m) != 2+round {
			t.Fatalf("round %d: after the write: %v", round, m)
		}
		// One commit, pushed, with the file put right and what it was.
		if len(f.remoteLog()) != before+1 {
			t.Errorf("round %d: %d commits were pushed", round, len(f.remoteLog())-before)
		}
		tree := git(t, f.remote, "ls-tree", "-r", "--name-only", "main")
		if n := strings.Count(tree, "space.yml.unreadable-"); n != 1 {
			t.Errorf("round %d: %d copies in the remote:\n%s", round, n, tree)
		}
		if got := git(t, f.remote, "show", "main:.petalbook/space.yml"); !strings.Contains(got, folder+":") {
			t.Errorf("round %d: the remote has\n%s", round, got)
		}
	}
	consistent(t, s)
}

// --- one entry that cannot be used ------------------------------------------------

func entriesOfNotices(s *Space, file string, unreadable bool) []string {
	var out []string
	for _, n := range s.SettingsNotices() {
		if n.File == file && n.Entry != "" && n.Unreadable == unreadable {
			out = append(out, n.Entry)
		}
	}
	return out
}

func TestAnEntryThatIsNotSettingsReadsAsNoneAndIsWrittenOver(t *testing.T) {
	s := settled(t)
	root := s.Root()
	src := "folders:\n  A: yes\n  B: {type: [x], views: 7, note: mine}\n  C: {type: wenan}\n"
	writeSpaceYML(t, s, src)
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"C": "wenan"}) || viewNames(t, s, folderScope("B")) != "" {
		t.Fatalf("%v", m)
	}
	if got := entriesOfNotices(s, spaceFile, true); !reflect.DeepEqual(got, []string{"A", "B: views", "B: type"}) {
		t.Errorf("the notices: %v", got)
	}
	// Nothing about them stops anything.
	if _, err := s.MoveFolder(Author{}, "D", "D2"); err != nil {
		t.Error(err)
	}
	if _, err := s.BindFolder(Author{}, "A", "wenan"); err != nil {
		t.Fatal(err)
	}
	mustPutViews(t, s, folderScope("B"), View{Name: "v", Layout: "list"})
	if _, err := s.BindFolder(Author{}, "B", "wenan"); err != nil {
		t.Fatal(err)
	}
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A": "wenan", "B": "wenan", "C": "wenan"}) || viewNames(t, s, folderScope("B")) != "v" {
		t.Errorf("%v\n%s", m, spaceYML(root))
	}
	if !strings.Contains(spaceYML(root), "note: mine") {
		t.Errorf("the rest of the entry is gone:\n%s", spaceYML(root))
	}
	// What was written over is kept, the first copy being the file as it was.
	kept := copiesOf(root, spaceSettings)
	if len(kept) != 3 || readDisk(t, s, kept[0]) != src {
		t.Fatalf("the copies: %v", kept)
	}
	if got := entriesOfNotices(s, spaceFile, false); !reflect.DeepEqual(got, []string{"A", "B: views", "B: type"}) || len(entriesOfNotices(s, spaceFile, true)) != 0 {
		t.Errorf("the notices after the writes: %v %+v", got, s.SettingsNotices())
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	// The same for a tag.
	setTagsYML(t, root, "tags:\n  a: rose\n  b: {color: [1], views: no}\n")
	if c := s.TagColors(); len(c) != 0 {
		t.Errorf("%v", c)
	}
	if got := entriesOfNotices(s, tagsFile, true); !reflect.DeepEqual(got, []string{"a", "b: views", "b: color"}) {
		t.Errorf("the notices: %v", got)
	}
	for _, tag := range []string{"a", "b"} {
		if err := s.SetTagColor(Author{}, tag, "red"); err != nil {
			t.Fatal(err)
		}
	}
	mustPutViews(t, s, tagScope("b"), View{Name: "w", Layout: "list"})
	if c := s.TagColors(); c["a"] != "red" || c["b"] != "red" || viewNames(t, s, tagScope("b")) != "w" {
		t.Errorf("%v\n%s", c, tagsYML(root))
	}
	if got := entriesOfNotices(s, tagsFile, false); !reflect.DeepEqual(got, []string{"a", "b: color", "b: views"}) {
		t.Errorf("the notices after the writes: %v", got)
	}
}

// --- type files -------------------------------------------------------------------

func TestATypeFileThatCannotBeUsedIsRestoredFromTheHistory(t *testing.T) {
	s := settled(t)
	root := s.Root()
	made, err := putCurrent(s, "task", TypeInput{Title: "Task", Fields: []FieldInput{{Key: "status", Kind: KindSelect, Options: []string{"todo", "done"}}}})
	if err != nil {
		t.Fatal(err)
	}
	s = restart(t, s)
	good := git(t, root, "rev-parse", "HEAD")
	writeType(t, root, "task", "{not json")

	typeOf := func() *TypeInfo {
		types, _, err := s.Types()
		if err != nil {
			t.Fatal(err)
		}
		for i := range types {
			if types[i].Name == "task" {
				return &types[i]
			}
		}
		return nil
	}
	rel := ".petalbook/types/task.json"
	if ti := typeOf(); ti == nil || ti.Error != "" || ti.Title != "Task" || len(ti.Fields) != 1 || ti.Rev != made.Rev {
		t.Fatalf("the type is read as %+v", ti)
	}
	if n := noticeOf(s, rel, true); n == nil || n.RestoredFrom == nil || n.RestoredFrom.Commit != good {
		t.Fatalf("the notice: %+v", s.SettingsNotices())
	}
	if _, err := s.BindFolder(Author{}, "B", "task"); err != nil {
		t.Errorf("binding to it: %v", err)
	}
	// A change from the rev that is read writes the file, and keeps what it was.
	next, err := s.PutType(Author{}, "task", made.Rev, TypeInput{Title: "Task", Fields: []FieldInput{{Key: "status", Kind: KindSelect, Options: []string{"todo", "done"}}, {Key: "due", Kind: KindDate}}})
	if err != nil || len(next.Fields) != 2 {
		t.Fatalf("%+v %v", next, err)
	}
	if ti := typeOf(); ti == nil || len(ti.Fields) != 2 || ti.Fields[0].Kind != KindSelect {
		t.Errorf("after the change: %+v", ti)
	}
	kept := copiesOf(root, typeSettings("task"))
	if len(kept) != 1 || readDisk(t, s, kept[0]) != "{not json" || !strings.HasPrefix(kept[0], rel+".unreadable-") {
		t.Fatalf("what the file was: %v", kept)
	}
	if n := noticeOf(s, rel, false); n == nil || n.Copy != kept[0] || noticeOf(s, rel, true) != nil {
		t.Errorf("the notice after the repair: %+v", s.SettingsNotices())
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := git(t, root, "status", "--porcelain"); strings.Contains(st, "types") {
		t.Errorf("not committed:\n%s", st)
	}
	// Deleting a type whose file cannot be used keeps what the file was too.
	writeType(t, root, "task", "[]")
	if err := deleteCurrent(s, "task"); err != nil {
		t.Fatal(err)
	}
	if typeOf() != nil || len(copiesOf(root, typeSettings("task"))) != 2 || len(s.Bindings()) != 1 {
		t.Errorf("after the delete: %v %v", copiesOf(root, typeSettings("task")), s.Bindings())
	}

	// A field whose schema is not one is written anew when it is given a kind.
	writeType(t, root, "odd", `{"properties":{"a":true}}`)
	if _, err := putCurrent(s, "odd", TypeInput{Fields: []FieldInput{{Key: "a", Kind: KindNumber}}}); err != nil {
		t.Fatal(err)
	}
	if kept := copiesOf(root, typeSettings("odd")); len(kept) != 1 || readDisk(t, s, kept[0]) != `{"properties":{"a":true}}` {
		t.Errorf("what the file was: %v", kept)
	}

	// Something that is not a folder where the types belong holds no types,
	// and is put aside when one is made.
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(root, ".petalbook", "types"))
	if err := os.WriteFile(filepath.Join(root, ".petalbook", "types"), []byte("not a folder\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if types, _, err := s.Types(); err != nil || len(types) != 0 {
		t.Errorf("%v %v", types, err)
	}
	if _, err := putCurrent(s, "fresh", TypeInput{Title: "Fresh"}); err != nil {
		t.Fatal(err)
	}
	if types, _, err := s.Types(); err != nil || len(types) != 1 {
		t.Errorf("%v %v", types, err)
	}
	if kept := copiesIn(root, ".petalbook"); len(kept) == 0 || readDisk(t, s, kept[len(kept)-1].rel) != "not a folder\n" {
		t.Errorf("what was in the place of the folder: %v", kept)
	}
	if err := s.Flush(); err != nil {
		t.Error(err)
	}
}
