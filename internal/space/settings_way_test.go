package space

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// --- nothing is read or written through a link or a file in the place of a folder ---

// outsideDir is a folder outside the space with what a link could lead to: a
// space.yml, a tags.yml and a folder of types of its own.
func outsideDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range map[string]string{
		"space.yml":           "name: Outside\nfolders:\n  Elsewhere: {type: outsider}\n",
		"tags.yml":            "tags:\n  outside: {color: red}\n",
		"types/outsider.json": `{"title":"Outsider","properties":{}}` + "\n",
		"outsider.json":       `{"title":"Outsider","properties":{}}` + "\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// treeOf is every path under dir with what it holds.
func treeOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			to, _ := os.Readlink(p)
			out[rel] = "link:" + to
		case d.IsDir():
			out[rel] = "dir"
		default:
			b, _ := os.ReadFile(p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

func untouched(t *testing.T, dir string, before map[string]string) {
	t.Helper()
	if after := treeOf(t, dir); !reflect.DeepEqual(before, after) {
		t.Errorf("the folder outside the space was changed:\nbefore %v\nafter  %v", before, after)
	}
}

func typeNamesOf(t *testing.T, s *Space) []string {
	t.Helper()
	types, _, err := s.Types()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ti := range types {
		out = append(out, ti.Name)
	}
	sort.Strings(out)
	return out
}

func isLink(root, rel string) bool {
	fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && fi.Mode()&fs.ModeSymlink != 0
}

// copiesAt are the copies of rel kept next to it.
func copiesAt(root, rel string) []string {
	dir, base := filepath.Split(filepath.Join(root, filepath.FromSlash(rel)))
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), base+unreadableMark) {
			out = append(out, e.Name())
		}
	}
	return out
}

func cleanTree(t *testing.T, s *Space) {
	t.Helper()
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := git(t, s.Root(), "status", "--porcelain"); st != "" {
		t.Errorf("not all committed:\n%s", st)
	}
}

func TestALinkedPetalbookAtTheStartIsPutRightAndNothingOutsideIsTouched(t *testing.T) {
	s := settled(t)
	root := s.Root()
	if err := s.SetTagColor(Author{}, "x", "blue"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	crash(s)
	out := outsideDir(t)
	before := treeOf(t, out)
	os.RemoveAll(filepath.Join(root, ".petalbook"))
	if err := os.Symlink(out, filepath.Join(root, ".petalbook")); err != nil {
		t.Fatal(err)
	}
	s = openAt(t, root)
	untouched(t, out, before)
	if isLink(root, ".petalbook") || !isRealDir(root, ".petalbook") {
		t.Fatal(".petalbook is not a folder again")
	}
	if c := copiesAt(root, ".petalbook"); len(c) != 1 || !isLink(root, c[0]) {
		t.Fatalf("what was in the place of the folder: %v", c)
	}
	// What the folder had is back: the bindings, the views, the types, the tags.
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A": "wenan"}) {
		t.Errorf("the bindings are %v", m)
	}
	if got := viewNames(t, s, folderScope("A/Sub")); got != "one" {
		t.Errorf("the views are %q", got)
	}
	if got := typeNamesOf(t, s); !reflect.DeepEqual(got, []string{"wenan"}) {
		t.Errorf("the types are %v", got)
	}
	if c := s.TagColors(); !reflect.DeepEqual(c, map[string]string{"x": "blue"}) {
		t.Errorf("the tags are %v", c)
	}
	// The state of the space is inside it.
	if rel, err := filepath.Rel(root, s.StateDir()); err != nil || !strings.HasPrefix(filepath.ToSlash(rel), ".petalbook/") {
		t.Errorf("the state directory is %s", s.StateDir())
	}
	if n := noticeOf(s, ".petalbook", false); n == nil || !strings.HasPrefix(n.Copy, ".petalbook"+unreadableMark) {
		t.Errorf("the notice: %+v", s.SettingsNotices())
	}
	mustCreate(t, s, "B", meta("title", "after"), "x\n")
	cleanTree(t, s)
	tree := git(t, root, "ls-tree", "-r", "--name-only", "HEAD")
	for _, want := range []string{".petalbook/space.yml", ".petalbook/tags.yml", ".petalbook/types/wenan.json"} {
		if !strings.Contains(tree, want+"\n") && !strings.HasSuffix(tree, want) {
			t.Errorf("%s is not committed:\n%s", want, tree)
		}
	}
	untouched(t, out, before)
}

func TestALinkedPetalbookWhileRunningIsReadFromTheHistoryAndPutRightByAWrite(t *testing.T) {
	needGit(t)
	state := t.TempDir()
	s := openAt(t, t.TempDir(), func(o *Options) { o.StateDir = state })
	root := s.Root()
	if _, err := putCurrent(s, "wenan", allKinds()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"A", "B", "C"} {
		mustCreate(t, s, f, meta("title", f), "x\n")
	}
	if _, err := s.BindFolder(Author{}, "A", "wenan"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	out := outsideDir(t)
	before := treeOf(t, out)
	os.RemoveAll(filepath.Join(root, ".petalbook"))
	if err := os.Symlink(out, filepath.Join(root, ".petalbook")); err != nil {
		t.Fatal(err)
	}
	// Reads go by the history, not by what the link leads to.
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A": "wenan"}) {
		t.Errorf("the bindings are %v", m)
	}
	if got := typeNamesOf(t, s); !reflect.DeepEqual(got, []string{"wenan"}) {
		t.Errorf("the types are %v", got)
	}
	if c := s.TagColors(); len(c) != 0 {
		t.Errorf("the tags are %v", c)
	}
	// A page is written without anything going through the link.
	mustCreate(t, s, "B", meta("title", "page"), "x\n")
	untouched(t, out, before)
	if n := noticeOf(s, spaceFile, true); n == nil || n.RestoredFrom == nil {
		t.Errorf("the notice: %+v", s.SettingsNotices())
	}
	// A write of the settings puts the folder right, with all it had.
	if _, err := s.BindFolder(Author{}, "C", "wenan"); err != nil {
		t.Fatal(err)
	}
	untouched(t, out, before)
	if !isRealDir(root, ".petalbook") || !isRealDir(root, typesDir) {
		t.Fatal(".petalbook is not a folder again")
	}
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A": "wenan", "C": "wenan"}) {
		t.Errorf("the bindings are %v\n%s", m, spaceYML(root))
	}
	if got := typeNamesOf(t, s); !reflect.DeepEqual(got, []string{"wenan"}) {
		t.Errorf("the types are %v", got)
	}
	if c := copiesAt(root, ".petalbook"); len(c) != 1 || !isLink(root, c[0]) {
		t.Errorf("what was in the place of the folder: %v", c)
	}
	cleanTree(t, s)
}

func TestALinkedTypesFolderIsNeverFollowed(t *testing.T) {
	s := settled(t)
	root := s.Root()
	out := outsideDir(t)
	types := filepath.Join(out, "types")
	before := treeOf(t, out)
	os.RemoveAll(filepath.Join(root, ".petalbook", "types"))
	if err := os.Symlink(types, filepath.Join(root, ".petalbook", "types")); err != nil {
		t.Fatal(err)
	}
	if got := typeNamesOf(t, s); !reflect.DeepEqual(got, []string{"wenan"}) {
		t.Errorf("the types are %v", got)
	}
	if _, ok := s.typeInfoCached("outsider"); ok {
		t.Error("a type outside the space was read")
	}
	if _, err := putCurrent(s, "fresh", TypeInput{Title: "Fresh"}); err != nil {
		t.Fatal(err)
	}
	untouched(t, out, before)
	if !isRealDir(root, typesDir) {
		t.Fatal("the folder of the types was not made again")
	}
	if got := typeNamesOf(t, s); !reflect.DeepEqual(got, []string{"fresh", "wenan"}) {
		t.Errorf("the types are %v", got)
	}
	if c := copiesAt(root, typesDir); len(c) != 1 || !isLink(root, ".petalbook/"+c[0]) {
		t.Errorf("what was in the place of the folder: %v", c)
	}
	cleanTree(t, s)

	// Deleting a type behind such a link deletes nothing outside either.
	os.RemoveAll(filepath.Join(root, ".petalbook", "types"))
	if err := os.Symlink(types, filepath.Join(root, ".petalbook", "types")); err != nil {
		t.Fatal(err)
	}
	if err := deleteCurrent(s, "fresh"); err != nil {
		t.Fatal(err)
	}
	untouched(t, out, before)
	if got := typeNamesOf(t, s); !reflect.DeepEqual(got, []string{"wenan"}) {
		t.Errorf("the types are %v", got)
	}
	cleanTree(t, s)
}

func TestALinkFromAPullIsPutRightAndPushed(t *testing.T) {
	for _, which := range []string{".petalbook", typesDir} {
		t.Run(which, func(t *testing.T) {
			f := newRemoteSpace(t)
			s := f.s
			if _, err := putCurrent(s, "wenan", allKinds()); err != nil {
				t.Fatal(err)
			}
			for _, d := range []string{"A", "B"} {
				mustCreate(t, s, d, meta("title", d), "x\n")
			}
			if _, err := s.BindFolder(Author{}, "A", "wenan"); err != nil {
				t.Fatal(err)
			}
			out := outsideDir(t)
			target := out
			if which == typesDir {
				target = filepath.Join(out, "types")
			}
			before := treeOf(t, out)
			o := f.theirs()
			os.RemoveAll(filepath.Join(o.dir, filepath.FromSlash(which)))
			if err := os.Symlink(target, filepath.Join(o.dir, filepath.FromSlash(which))); err != nil {
				t.Fatal(err)
			}
			o.push("a link in the place of " + which)
			if err := s.Pull(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			untouched(t, out, before)
			if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A": "wenan"}) {
				t.Errorf("the bindings are %v", m)
			}
			if got := typeNamesOf(t, s); !reflect.DeepEqual(got, []string{"wenan"}) {
				t.Errorf("the types are %v", got)
			}
			if _, err := s.BindFolder(Author{}, "B", "wenan"); err != nil {
				t.Fatal(err)
			}
			if _, err := putCurrent(s, "fresh", TypeInput{Title: "Fresh"}); err != nil {
				t.Fatal(err)
			}
			untouched(t, out, before)
			if !isRealDir(f.root, which) {
				t.Fatalf("%s is not a folder again", which)
			}
			tree := git(t, f.remote, "ls-tree", "-r", "--name-only", "main")
			for _, want := range []string{".petalbook/space.yml", ".petalbook/types/wenan.json", ".petalbook/types/fresh.json", which + unreadableMark} {
				if !strings.Contains(tree, want) {
					t.Errorf("the remote has no %s:\n%s", want, tree)
				}
			}
			if got := git(t, f.remote, "show", "main:.petalbook/space.yml"); !strings.Contains(got, "A:") || !strings.Contains(got, "B:") {
				t.Errorf("the remote has\n%s", got)
			}
			if st := git(t, f.root, "status", "--porcelain"); st != "" {
				t.Errorf("the clone has changes:\n%s", st)
			}
			consistent(t, s)
		})
	}
}

// --- the server's own last write is not compared by the clock ----------------------

// pushAt pushes what is in their clone as a commit made at date.
func (o *theirs) pushAt(msg, date string) {
	o.t.Helper()
	gitO(o.t, o.dir, "add", "-A")
	cmd := exec.Command("git", "-C", o.dir, "-c", "user.name=Other", "-c", "user.email=other@example.test", "-c", "commit.gpgsign=false", "commit", "-qm", msg)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_COMMITTER_DATE="+date, "GIT_AUTHOR_DATE="+date)
	if out, err := cmd.CombinedOutput(); err != nil {
		o.t.Fatalf("commit: %v\n%s", err, out)
	}
	gitO(o.t, o.dir, "push", "-q", "--force", "origin", "main")
}

func TestAGoodCommitFromASlowClockIsNotLostToTheServersOwnWrite(t *testing.T) {
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
	good := spaceYML(f.root) + "  B:\n    type: wenan\n"
	o.write(".petalbook/space.yml", good)
	o.pushAt("bind B", "2001-01-01T00:00:00Z")
	o.write(".petalbook/space.yml", notYAML)
	o.pushAt("break the settings", "2001-01-02T00:00:00Z")
	if err := s.Pull(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A": "wenan", "B": "wenan"}) {
		t.Fatalf("the bindings are %v", m)
	}
	if _, err := s.BindFolder(Author{}, "C", "wenan"); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.remote, "show", "main:.petalbook/space.yml"); !strings.Contains(got, "B:") || !strings.Contains(got, "C:") {
		t.Errorf("the remote has\n%s", got)
	}
}

func TestAfterAHistoryRewriteTheNewestCommitOfTheNewHistoryIsRestored(t *testing.T) {
	f := newRemoteSpace(t)
	s := f.s
	if _, err := putCurrent(s, "wenan", allKinds()); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"A", "B"} {
		mustCreate(t, s, d, meta("title", d), "x\n")
	}
	base := git(t, f.root, "rev-parse", "HEAD")
	if _, err := s.BindFolder(Author{}, "A", "wenan"); err != nil {
		t.Fatal(err)
	}
	// The history is rewritten from before A was bound, with B bound instead,
	// at an old date, and then the file is broken.
	o := f.theirs()
	gitO(t, o.dir, "reset", "-q", "--hard", base)
	o.write(".petalbook/space.yml", spaceYML(f.root)[:strings.Index(spaceYML(f.root), "folders:")]+"folders:\n  B:\n    type: wenan\n")
	o.pushAt("bind B", "2001-01-01T00:00:00Z")
	o.write(".petalbook/space.yml", notYAML)
	o.pushAt("break the settings", "2001-01-02T00:00:00Z")
	tip := git(t, f.remote, "rev-parse", "main")
	s.Pull(context.Background(), true)
	if err := s.Accept(context.Background(), tip); err != nil {
		t.Fatal(err)
	}
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"B": "wenan"}) {
		t.Errorf("the bindings are %v", m)
	}
}

func TestALocalCommitDropsTheServersOwnWrite(t *testing.T) {
	s := settled(t)
	if _, err := s.BindFolder(Author{}, "B", "wenan"); err != nil {
		t.Fatal(err)
	}
	s.heal.mu.Lock()
	_, pending := s.heal.wrote[spaceFile]
	s.heal.mu.Unlock()
	if !pending {
		t.Fatal("the write is not remembered before its commit")
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s.heal.mu.Lock()
	_, pending = s.heal.wrote[spaceFile]
	s.heal.mu.Unlock()
	if pending {
		t.Error("the write is still remembered after its commit")
	}
}

// --- what can be read of an unusable file is kept ----------------------------------

// thirty is A (as settled has it) and thirty new bindings, as lines of space.yml.
func thirty() (lines string, want map[string]string) {
	want = map[string]string{"A": "wenan"}
	var b strings.Builder
	b.WriteString("folders:\n  A:\n    type: wenan\n")
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "  F%02d:\n    type: wenan\n", i)
		want[fmt.Sprintf("F%02d", i)] = "wenan"
	}
	return b.String(), want
}

func TestThirtyNewBindingsAndOneMistakeAreKept(t *testing.T) {
	lines, want := thirty()
	deep := strings.Repeat("[", maxDocDepth+2) + strings.Repeat("]", maxDocDepth+2)
	for _, c := range []struct {
		name  string
		text  string
		extra map[string]string
		lost  []string
	}{
		{"a typo", lines + "  F31: {type: wenan\n  F32:\n    type: wenan\n", map[string]string{"F32": "wenan"}, []string{"F31"}},
		{"an alias inside its own anchor", lines + "  F31: &c {type: wenan, x: [*c]}\n", nil, []string{"F31"}},
		{"an alias before its anchor", lines + "  F31: *later\n  F32: &later {type: wenan}\n", map[string]string{"F32": "wenan"}, []string{"F31"}},
		{"over the depth", lines + "  F31: {type: wenan, x: " + deep + "}\n", nil, []string{"F31"}},
		{"a second document", lines + "---\nfolders:\n  F31: {type: wenan}\n", map[string]string{"F31": "wenan"}, nil},
		{"larger than the server reads", lines + "  F31: {type: wenan, x: '" + strings.Repeat("y", maxSpaceFile) + "'}\n", nil, []string{"F31"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := settled(t)
			root := s.Root()
			setSpaceYML(t, root, "name: Mine\n"+c.text)
			all := map[string]string{}
			for k, v := range want {
				all[k] = v
			}
			for k, v := range c.extra {
				all[k] = v
			}
			if m := bindingMap(s); !reflect.DeepEqual(m, all) {
				t.Fatalf("the bindings are %v", m)
			}
			if got := viewNames(t, s, folderScope("A/Sub")); got != "one" {
				// The file does not have A/Sub: it stays as restored.
				t.Errorf("the views of A/Sub are %q", got)
			}
			n := noticeOf(s, spaceFile, true)
			if n == nil || !reflect.DeepEqual(n.Lost, c.lost) {
				t.Fatalf("the notice: %+v", s.SettingsNotices())
			}
			// The next write keeps all of it, and the file as it was.
			if _, err := s.BindFolder(Author{}, "D", "wenan"); err != nil {
				t.Fatal(err)
			}
			all["D"] = "wenan"
			if m := bindingMap(s); !reflect.DeepEqual(m, all) {
				t.Errorf("after the write: %v", m)
			}
			if !strings.Contains(spaceYML(root), "name: Mine") {
				t.Errorf("the name is gone:\n%.300s", spaceYML(root))
			}
			kept := copiesOf(root, spaceSettings)
			if len(kept) != 1 || readDisk(t, s, kept[0]) != "name: Mine\n"+c.text {
				t.Fatalf("the copy: %v", kept)
			}
			if n := noticeOf(s, spaceFile, false); n == nil || n.Copy != kept[0] || !reflect.DeepEqual(n.Lost, c.lost) {
				t.Errorf("the notice after the write: %+v", s.SettingsNotices())
			}
			cleanTree(t, s)
		})
	}
}

func TestAFoldersThatIsNotAListKeepsTheRestOfTheFile(t *testing.T) {
	s := settled(t)
	root := s.Root()
	setSpaceYML(t, root, "name: Mine\nfolders: [a, b]\n")
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A": "wenan"}) {
		t.Fatalf("the bindings are %v", m)
	}
	if n := noticeOf(s, spaceFile, true); n == nil || !reflect.DeepEqual(n.Lost, []string{"folders"}) {
		t.Fatalf("the notice: %+v", s.SettingsNotices())
	}
	if _, err := s.BindFolder(Author{}, "B", "wenan"); err != nil {
		t.Fatal(err)
	}
	if y := spaceYML(root); !strings.Contains(y, "name: Mine") || !strings.Contains(y, "A:") || !strings.Contains(y, "B:") {
		t.Errorf("space.yml:\n%s", y)
	}
}

func TestThirtyNewTagsAndOneTypoAreKept(t *testing.T) {
	s := settled(t)
	root := s.Root()
	if err := s.SetTagColor(Author{}, "old", "blue"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"old": "blue"}
	var b strings.Builder
	b.WriteString("tags:\n  old:\n    color: blue\n")
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "  t%02d:\n    color: red\n", i)
		want[fmt.Sprintf("t%02d", i)] = "red"
	}
	b.WriteString("  broken: {color: [red\n")
	setTagsYML(t, root, b.String())
	if c := s.TagColors(); !reflect.DeepEqual(c, want) {
		t.Fatalf("the colours are %v", c)
	}
	if n := noticeOf(s, tagsFile, true); n == nil || !reflect.DeepEqual(n.Lost, []string{"broken"}) {
		t.Fatalf("the notice: %+v", s.SettingsNotices())
	}
	if err := s.SetTagColor(Author{}, "new", "green"); err != nil {
		t.Fatal(err)
	}
	want["new"] = "green"
	if c := s.TagColors(); !reflect.DeepEqual(c, want) {
		t.Errorf("after the write: %v", c)
	}
	if kept := copiesOf(root, tagsSettings); len(kept) != 1 || readDisk(t, s, kept[0]) != b.String() {
		t.Errorf("the copy: %v", kept)
	}
}

// --- the history is not searched again for every commit ------------------------------

func TestAnUnusableFileIsNotSearchedAgainForEveryCommit(t *testing.T) {
	s := settled(t)
	root := s.Root()
	if err := s.SetTagColor(Author{}, "old", "blue"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	setTagsYML(t, root, "tags: [unclosed\n")
	if c := s.TagColors(); c["old"] != "blue" {
		t.Fatalf("%v", c)
	}
	searches := func() int {
		s.heal.mu.Lock()
		defer s.heal.mu.Unlock()
		return s.heal.searches
	}
	n := searches()
	if n == 0 {
		t.Fatal("the history was not searched")
	}
	for i := 0; i < 3; i++ {
		mustCreate(t, s, "B", meta("title", fmt.Sprint("p", i)), "x\n")
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
		if c := s.TagColors(); c["old"] != "blue" {
			t.Fatalf("%v", c)
		}
	}
	if got := searches(); got != n {
		t.Errorf("commits of other files searched the history again: %d searches, then %d", n, got)
	}
	// Another content is another search.
	setTagsYML(t, root, "tags: [unclosed, again\n")
	s.TagColors()
	if got := searches(); got != n+1 {
		t.Errorf("%d searches, then %d", n, got)
	}
}

func TestALinkThatCameWhileTheServerWasDownIsPutRightAtTheStart(t *testing.T) {
	f := newRemoteSpace(t)
	s := f.s
	if _, err := putCurrent(s, "wenan", allKinds()); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "A", meta("title", "A"), "x\n")
	mustCreate(t, s, "B", meta("title", "B"), "x\n")
	if _, err := s.BindFolder(Author{}, "A", "wenan"); err != nil {
		t.Fatal(err)
	}
	out := outsideDir(t)
	before := treeOf(t, out)
	o := f.theirs()
	os.RemoveAll(filepath.Join(o.dir, ".petalbook"))
	if err := os.Symlink(out, filepath.Join(o.dir, ".petalbook")); err != nil {
		t.Fatal(err)
	}
	o.push("a link in the place of .petalbook")
	f.s.Close()
	// The clone is moved on by hand while the server is down.
	gitO(t, f.root, "fetch", "-q", "origin")
	gitO(t, f.root, "merge", "-q", "--ff-only", "origin/main")
	f.reopen()
	s = f.s
	untouched(t, out, before)
	if !isRealDir(f.root, ".petalbook") {
		t.Fatal(".petalbook is not a folder again")
	}
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A": "wenan"}) {
		t.Errorf("the bindings are %v", m)
	}
	if _, err := s.BindFolder(Author{}, "B", "wenan"); err != nil {
		t.Fatal(err)
	}
	untouched(t, out, before)
	if got := git(t, f.remote, "show", "main:.petalbook/space.yml"); !strings.Contains(got, "A:") || !strings.Contains(got, "B:") {
		t.Errorf("the remote has\n%s", got)
	}
	if st := git(t, f.root, "status", "--porcelain"); st != "" {
		t.Errorf("the clone has changes:\n%s", st)
	}
}
