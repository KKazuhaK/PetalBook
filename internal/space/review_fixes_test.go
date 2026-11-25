package space

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

func spaceYML(root string) string {
	b, _ := os.ReadFile(filepath.Join(root, ".petalbook", "space.yml"))
	return string(b)
}

func setSpaceYML(t *testing.T, root, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".petalbook"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".petalbook", "space.yml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func bindingMap(s *Space) map[string]string {
	m := map[string]string{}
	for _, b := range s.Bindings() {
		m[b.Folder] = b.Type
	}
	return m
}

// --- types: rev ---------------------------------------------------------------

func TestTypeChangesNeedTheRevTheyWereBasedOn(t *testing.T) {
	t.Parallel()
	s := open(t)
	root := s.Root()
	made, err := s.PutType(Author{}, "t", "", TypeInput{Title: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if made.Rev != page.Rev([]byte(readType(t, root, "t"))) {
		t.Errorf("rev %q is not the blob id of the file", made.Rev)
	}
	types, _, _ := s.Types()
	if len(types) != 1 || types[0].Rev != made.Rev {
		t.Errorf("listed rev: %+v", types)
	}
	conflict := func(what string, err error, wantCurrent bool) {
		t.Helper()
		var ce *TypeConflictError
		if !errors.As(err, &ce) {
			t.Errorf("%s: want a conflict, got %v", what, err)
		} else if (ce.Current != nil) != wantCurrent {
			t.Errorf("%s: current %+v", what, ce.Current)
		}
	}
	// Making one that is there, with no rev or a wrong one.
	_, err = s.PutType(Author{}, "t", "", TypeInput{Title: "two"})
	conflict("make over an existing type", err, true)
	_, err = s.PutType(Author{}, "t", "0000", TypeInput{Title: "two"})
	conflict("a rev that is not the file's", err, true)
	// Changing one that is not there.
	_, err = s.PutType(Author{}, "gone", made.Rev, TypeInput{})
	conflict("a type that is gone", err, false)
	if readType(t, root, "t") == "" || !strings.Contains(readType(t, root, "t"), "one") {
		t.Errorf("a refused change wrote: %s", readType(t, root, "t"))
	}
	next, err := s.PutType(Author{}, "t", made.Rev, TypeInput{Title: "two"})
	if err != nil || next.Rev == made.Rev {
		t.Fatalf("%+v %v", next, err)
	}
	// The one who still has the old rev is refused, and deleting is no different.
	_, err = s.PutType(Author{}, "t", made.Rev, TypeInput{Title: "three"})
	var ce *TypeConflictError
	if !errors.As(err, &ce) || ce.Current == nil || ce.Current.Title != "two" || ce.Current.Rev != next.Rev {
		t.Errorf("the conflict should carry the current type: %v", err)
	}
	conflict("delete from an old rev", s.DeleteType(Author{}, "t", made.Rev), true)
	conflict("delete with no rev", s.DeleteType(Author{}, "t", ""), true)
	if !exists(s, ".petalbook/types/t.json") {
		t.Fatal("a refused delete removed the file")
	}
	// A type file that cannot be used and has no earlier version is no type:
	// it is not listed, and deleting it (from no rev) keeps what it was.
	writeType(t, root, "bad", `{"properties": {`)
	types, _, _ = s.Types()
	for _, ti := range types {
		if ti.Name == "bad" {
			t.Errorf("listed: %+v", ti)
		}
	}
	conflict("delete from a rev it does not have", s.DeleteType(Author{}, "bad", made.Rev), false)
	if err := s.DeleteType(Author{}, "bad", ""); err != nil {
		t.Errorf("%v", err)
	}
	if kept := copiesOf(root, typeSettings("bad")); exists(s, ".petalbook/types/bad.json") || len(kept) != 1 || readDisk(t, s, kept[0]) != `{"properties": {` {
		t.Errorf("after the delete: %v", kept)
	}
	if err := s.DeleteType(Author{}, "t", next.Rev); err != nil {
		t.Errorf("%v", err)
	}
}

// --- types: keys and titles that are already there ----------------------------

func TestFieldsThatAreAlreadyThereKeepTheirKeysAndNonTextTitles(t *testing.T) {
	t.Parallel()
	s := open(t)
	root := s.Root()
	long := strings.Repeat("k", 70)
	writeType(t, root, "old", `{
  "title": 7,
  "properties": {
    "slug": {"type": "string", "title": 5},
    "a:b": {"type": "string", "title": ["x"]},
    "`+long+`": {"type": "number"}
  }
}
`)
	// What a listing says of them, and what a request that changes nothing says.
	types, _, _ := s.Types()
	if len(types) != 1 || len(types[0].Fields) != 3 || types[0].Title != "" || types[0].Fields[0].Title != "" {
		t.Fatalf("%+v", types)
	}
	in := TypeInput{Fields: []FieldInput{{Key: "slug"}, {Key: "a:b"}, {Key: long}}}
	if _, err := putCurrent(s, "old", in); err != nil {
		t.Fatalf("a field that is there is refused for its key: %v", err)
	}
	// (The first write sets the file in the server's layout.)
	before := readType(t, root, "old")
	if !strings.Contains(before, `"title": 7`) || !strings.Contains(before, `"title": 5`) {
		t.Fatalf("a title that is not text was dropped:\n%s", before)
	}
	if _, err := putCurrent(s, "old", in); err != nil {
		t.Fatal(err)
	}
	if got := readType(t, root, "old"); got != before {
		t.Errorf("the file changed:\n%s\nwas\n%s", got, before)
	}
	// A new field still has the rules.
	in.Fields = append(in.Fields, FieldInput{Key: "id", Kind: KindText})
	_, err := putCurrent(s, "old", in)
	wantInvalid(t, "a new built-in key", err, "")
	in.Fields[3] = FieldInput{Key: "x:y", Kind: KindText}
	_, err = putCurrent(s, "old", in)
	wantInvalid(t, "a new key with a colon", err, "colon")
	// A title that is text is changed as ever, and then the others stay.
	in.Fields = in.Fields[:3]
	in.Fields[0].Title = "Slug"
	if _, err := putCurrent(s, "old", in); err != nil {
		t.Fatal(err)
	}
	got := readType(t, root, "old")
	if !strings.Contains(got, `"title": "Slug"`) || !strings.Contains(got, `"x"`) || !strings.Contains(got, `"title": 7`) {
		t.Errorf("%s", got)
	}
}

// --- space.yml and tags.yml with nothing but comments --------------------------

func TestSettingsFilesThatAreOnlyCommentsKeepThemWhenWritten(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, content string }{
		{"comments", "# mine\n\n# second\n"},
		{"marker", "---\n# mine\n\n# second\n"},
		{"on the marker line", "--- # mine\n# second\n"},
		{"both sides of the marker", "# mine\n---\n# second\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := open(t)
			root := s.Root()
			if _, err := putCurrent(s, "t", TypeInput{}); err != nil {
				t.Fatal(err)
			}
			mustCreate(t, s, "F", meta("title", "A"), "x\n")
			setSpaceYML(t, root, c.content)
			if _, err := s.BindFolder(Author{}, "F", "t"); err != nil {
				t.Fatal(err)
			}
			got := spaceYML(root)
			if !strings.Contains(got, "# mine") || !strings.Contains(got, "# second") || !strings.Contains(got, "folders:") {
				t.Errorf("space.yml:\n%s", got)
			}
			if bindingMap(s)["F"] != "t" {
				t.Errorf("%v", s.Bindings())
			}
			// And they stay when the binding goes.
			if _, err := s.BindFolder(Author{}, "F", ""); err != nil {
				t.Fatal(err)
			}
			if got := spaceYML(root); !strings.Contains(got, "# mine") || !strings.Contains(got, "# second") {
				t.Errorf("space.yml after unbinding:\n%s", got)
			}
		})
	}
	t.Run("tags", func(t *testing.T) {
		s := open(t)
		file := filepath.Join(s.Root(), ".petalbook", "tags.yml")
		os.MkdirAll(filepath.Dir(file), 0o755)
		os.WriteFile(file, []byte("# colours of the tags\n"), 0o644)
		if err := s.SetTagColor(Author{}, "栀子", "rose"); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(file)
		if !strings.Contains(string(b), "# colours of the tags") || !strings.Contains(string(b), "栀子") {
			t.Errorf("tags.yml:\n%s", b)
		}
	})
}

// --- bindings: duplicates -----------------------------------------------------

func TestMovingAFolderOntoAStaleBindingReplacesIt(t *testing.T) {
	t.Parallel()
	s := open(t)
	root := s.Root()
	mustCreate(t, s, "A/x", meta("title", "P"), "x\n")
	setSpaceYML(t, root, "name: n\nfolders:\n  B:\n    type: stale\n    views: [old]\n  A:\n    type: a\n  B/x:\n    type: stale-x\n  A/x:\n    type: ax\n  B/other:\n    type: stale-other\n")
	if _, err := s.MoveFolder(Author{}, "A", "B"); err != nil {
		t.Fatal(err)
	}
	got := spaceYML(root)
	for _, key := range []string{"B:", "B/x:", "A:", "A/x:"} {
		if n := strings.Count("\n"+got, "\n  "+key); n > 1 || (n == 0 && strings.HasPrefix(key, "B")) {
			t.Errorf("%s is written %d times:\n%s", key, n, got)
		}
	}
	want := map[string]string{"B": "a", "B/x": "ax"} // nothing was at B: B/other was stale too
	if m := bindingMap(s); !reflect.DeepEqual(m, want) {
		t.Errorf("%v, want %v\n%s", m, want, got)
	}
}

func TestReadersOfBindingsAgreeOnAFolderWrittenTwice(t *testing.T) {
	t.Parallel()
	s := open(t)
	root := s.Root()
	mustCreate(t, s, "A", meta("title", "P"), "x\n")
	if _, err := putCurrent(s, "t", TypeInput{}); err != nil {
		t.Fatal(err)
	}
	setSpaceYML(t, root, "name: n\nfolders:\n  A:\n    type: first\n  B:\n    type: b\n  A:\n    type: t\n")
	if bs := s.Bindings(); !reflect.DeepEqual(bs, []Binding{{"B", "b"}, {"A", "t"}}) {
		t.Errorf("%+v", bs)
	}
	if got := s.TypeResolver().For("A", ""); got != "t" {
		t.Errorf("resolver: %q", got)
	}
	// Unbinding takes every entry, so the first does not come back.
	if _, err := s.BindFolder(Author{}, "A", ""); err != nil {
		t.Fatal(err)
	}
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"B": "b"}) || strings.Contains(spaceYML(root), "first") {
		t.Errorf("%v\n%s", m, spaceYML(root))
	}
	// And so does deleting the type they name.
	setSpaceYML(t, root, "name: n\nfolders:\n  A:\n    type: t\n  A:\n    type: t\n")
	if err := deleteCurrent(s, "t"); err != nil {
		t.Fatal(err)
	}
	if len(s.Bindings()) != 0 || strings.Contains(spaceYML(root), "type:") {
		t.Errorf("%v\n%s", s.Bindings(), spaceYML(root))
	}
}

// --- a folder move and its bindings are one change -----------------------------

func journalFile(root string) string {
	return filepath.Join(DefaultStateDir(root), "pending.journal")
}

func TestFolderMoveIsTakenBackWhenItsBindingsCannotBeWritten(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("a file mode does not stop root")
	}
	s := open(t)
	root := s.Root()
	putCurrent(s, "t", TypeInput{})
	p := mustCreate(t, s, "A", meta("title", "P"), "x\n")
	if _, err := s.BindFolder(Author{}, "A", "t"); err != nil {
		t.Fatal(err)
	}
	// space.yml can be read but nothing can be put in its place.
	dir := filepath.Join(root, ".petalbook")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if f, err := os.Create(filepath.Join(dir, "probe")); err == nil {
		f.Close()
		os.Remove(f.Name())
		t.Skip("the file system ignores the mode")
	}
	_, err := s.MoveFolder(Author{}, "A", "B")
	if err == nil {
		t.Fatal("the move was reported as done")
	}
	if !exists(s, "A/"+filepath.Base(p.Path)) || exists(s, "B") {
		t.Errorf("the folder moved although the request failed: A=%v B=%v", exists(s, "A"), exists(s, "B"))
	}
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A": "t"}) {
		t.Errorf("%v", m)
	}
	if got, err := s.Get(p.ID); err != nil || got.Folder != "A" || got.EffectiveType != "t" {
		t.Errorf("%+v %v", got, err)
	}
	os.Chmod(dir, 0o755)
	if _, err := s.MoveFolder(Author{}, "A", "B"); err != nil {
		t.Fatal(err)
	}
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"B": "t"}) {
		t.Errorf("%v", m)
	}
	_ = bytes.Equal
}

// --- a move onto stale bindings ------------------------------------------------

func TestMovingAFolderDropsEveryStaleBindingAtAndUnderTheTarget(t *testing.T) {
	t.Parallel()
	s := open(t)
	root := s.Root()
	putCurrent(s, "t", TypeInput{})
	mustCreate(t, s, "Plain/sub", meta("title", "P"), "x\n")
	mustCreate(t, s, "Bound", meta("title", "Q"), "x\n")
	setSpaceYML(t, root, "name: n\nfolders:\n  Gone:\n    type: t\n  Gone/deep:\n    type: t\n  Elsewhere:\n    type: t\n  Bound:\n    type: t\n  Other:\n    type: t\n  Other/deep:\n    type: t\n")
	// An unbound folder moved onto a place with bindings left from a folder that was there once.
	if _, err := s.MoveFolder(Author{}, "Plain", "Gone"); err != nil {
		t.Fatal(err)
	}
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"Elsewhere": "t", "Bound": "t", "Other": "t", "Other/deep": "t"}) {
		t.Errorf("%v\n%s", m, spaceYML(root))
	}
	if got, _ := s.TypeResolver(), 0; got.For("Gone/sub", "") != "" {
		t.Errorf("the moved folder has a type it never had: %q", got.For("Gone/sub", ""))
	}
	// A bound one brings its own, and nothing else is kept at the target.
	if _, err := s.MoveFolder(Author{}, "Bound", "Other"); err != nil {
		t.Fatal(err)
	}
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"Elsewhere": "t", "Other": "t"}) {
		t.Errorf("%v\n%s", m, spaceYML(root))
	}
}

// --- the committer and the records of bindings -----------------------------------

// --- an undone move in a space that follows a remote -----------------------------

func TestUndoOfAFolderMoveIsConsistentAtEveryCrashPoint(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		after bool // the crash comes after the folder was renamed back
	}{{"before the folder goes back", false}, {"after the folder went back", true}} {
		t.Run(c.name, func(t *testing.T) {
			f := newRemoteSpace(t)
			putCurrent(f.s, "t", TypeInput{})
			mustCreate(t, f.s, "Tang/Sub", meta("title", "P"), "x\n")
			if _, err := f.s.BindFolder(Author{}, "Tang", "t"); err != nil {
				t.Fatal(err)
			}
			f.net.beforePush = func() { f.net.setDown(true) } // the push fails, and the move is undone
			type crashed struct{}
			real := f.s.ops.Rename
			back := filepath.Join(f.root, "Tang")
			f.s.ops.Rename = func(from, to string) error {
				if to != back {
					return real(from, to)
				}
				if c.after {
					if err := real(from, to); err != nil {
						return err
					}
				}
				panic(crashed{})
			}
			func() {
				defer func() {
					if r := recover(); r == nil {
						t.Fatal("the undo did not reach the folder")
					} else if _, ok := r.(crashed); !ok {
						panic(r)
					}
				}()
				f.s.MoveFolder(Author{}, "Tang", "唐")
			}()
			crash(f.s)
			s2 := openAt(t, f.root)
			at := ""
			for _, d := range []string{"Tang", "唐"} {
				if exists(s2, d+"/Sub") {
					at = d
				}
			}
			if at == "" || exists(s2, "Tang") == exists(s2, "唐") {
				t.Fatalf("the folder is in no place or two")
			}
			if m := bindingMap(s2); !reflect.DeepEqual(m, map[string]string{at: "t"}) {
				t.Errorf("the folder is at %s, the bindings are %v\n%s", at, m, spaceYML(f.root))
			}
		})
	}
}

// --- a move taken back leaves nothing it made -------------------------------------

func TestFolderMoveTakenBackLeavesNoFoldersItMade(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("a file mode does not stop root")
	}
	s := open(t)
	root := s.Root()
	putCurrent(s, "t", TypeInput{})
	mustCreate(t, s, "A", meta("title", "P"), "x\n")
	mustCreate(t, s, "Keep/Inner", meta("title", "Q"), "x\n")
	if _, err := s.BindFolder(Author{}, "A", "t"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".petalbook")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if f, err := os.Create(filepath.Join(dir, "probe")); err == nil {
		f.Close()
		os.Remove(f.Name())
		t.Skip("the file system ignores the mode")
	}
	// The target's parents: one is new (New/Deeper), one exists (Keep).
	for _, to := range []string{"New/Deeper/B", "Keep/Made/B"} {
		if _, err := s.MoveFolder(Author{}, "A", to); err == nil {
			t.Fatalf("%s: the move was reported as done", to)
		}
	}
	if exists(s, "New") || exists(s, "Keep/Made") || !exists(s, "Keep/Inner") || !exists(s, "A") {
		t.Errorf("folders left: New=%v Made=%v", exists(s, "New"), exists(s, "Keep/Made"))
	}
	tree, err := s.Tree()
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range tree {
		paths = append(paths, f.Path)
	}
	for _, p := range paths {
		if strings.HasPrefix(p, "New") || strings.HasPrefix(p, "Keep/Made") {
			t.Errorf("the index still lists %q: %v", p, paths)
		}
	}
	_ = fmt.Sprint
}
