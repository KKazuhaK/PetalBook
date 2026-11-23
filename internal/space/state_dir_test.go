package space

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- the server's own state is not in the work tree ----------------------------------

func TestAPullThatPutsALinkForPetalbookLeavesTheIndexWhole(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	s := f.s
	if _, err := putCurrent(s, "wenan", allKinds()); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "A", meta("title", "one", "tags", []any{"red"}), "x\n")
	mustCreate(t, s, "B", meta("title", "two", "tags", []any{"blue"}), "x\n")
	if rel, _ := filepath.Rel(f.root, s.StateDir()); filepath.ToSlash(rel) != ".git/petalbook-state" {
		t.Fatalf("the state directory is %s", s.StateDir())
	}
	out := outsideDir(t)
	before := treeOf(t, out)
	o := f.theirs()
	os.RemoveAll(filepath.Join(o.dir, ".petalbook"))
	if err := os.Symlink(out, filepath.Join(o.dir, ".petalbook")); err != nil {
		t.Fatal(err)
	}
	o.write("C/three.md", pageText("THEMS00003", "three", "z\n"))
	o.push("a link in the place of .petalbook, and a page")
	if err := s.Pull(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	untouched(t, out, before)
	tree, err := s.Tree()
	if err != nil {
		t.Fatal("the folders:", err)
	}
	if len(tree) < 3 {
		t.Errorf("the folders are %v", tree)
	}
	tags, err := s.Tags()
	if err != nil || len(tags) != 2 {
		t.Fatalf("the tags: %v %v", tags, err)
	}
	if _, err := s.Get("THEMS00003"); err != nil {
		t.Errorf("the page of the pull: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.StateDir(), "index.db")); err != nil {
		t.Error(err)
	}
	// A pull leaves the link as it came: no commit of the server's own.
	if git(t, f.root, "rev-parse", "HEAD") != f.tip() || !isLink(f.root, ".petalbook") {
		t.Error("the pull was followed by a commit of the server's own")
	}
	if st := git(t, f.root, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Errorf("the clone has changes:\n%s", st)
	}
	consistent(t, s)
}

func TestTheStateOfAnEarlierVersionIsMovedIntoTheGitDirectoryOnce(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "Kept"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	// What an earlier version left: its state under .petalbook/cache, with a
	// state database and a change that was written and not committed yet.
	old := filepath.Join(root, ".petalbook", "cache")
	if err := os.Rename(DefaultStateDir(root), old); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(old, "state.db"), []byte("drafts"), 0o644)
	os.WriteFile(filepath.Join(root, "Late.md"), []byte(pageText("LATEPAGE01", "Late", "l\n")), 0o644)
	os.WriteFile(filepath.Join(old, "pending.journal"), []byte(`{"op":"t","p":"Late.md"}`+"\n"), 0o644)

	s = openAt(t, root)
	defer s.Close()
	if _, err := os.Lstat(old); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf(".petalbook/cache is still there: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(DefaultStateDir(root), "state.db")); string(b) != "drafts" {
		t.Errorf("the state database: %q", b)
	}
	if _, err := s.Get(p.ID); err != nil {
		t.Error(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if files := git(t, root, "ls-files"); !strings.Contains(files, "Late.md") {
		t.Errorf("the journal was not carried over:\n%s", files)
	}
	if st := git(t, root, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Errorf("not all committed:\n%s", st)
	}
}

func TestALinkWhereTheOldStateWasIsNeitherFollowedNorMoved(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	mustCreate(t, s, "", meta("title", "Kept"), "x\n")
	s.Close()
	os.RemoveAll(DefaultStateDir(root))
	out := outsideDir(t)
	before := treeOf(t, out)
	if err := os.Symlink(out, filepath.Join(root, ".petalbook", "cache")); err != nil {
		t.Fatal(err)
	}
	s = openAt(t, root)
	defer s.Close()
	untouched(t, out, before)
	if !isLink(root, ".petalbook/cache") {
		t.Error("the link was moved")
	}
	if _, err := os.Stat(filepath.Join(DefaultStateDir(root), "index.db")); err != nil {
		t.Error(err)
	}
}

// --- a repair is never left behind as someone's change ---------------------------------

func TestARepairWhosePushIsRejectedIsMadeAgainOnTheNewTip(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"the remote moved", "the network is down"} {
		t.Run(how, func(t *testing.T) {
			f := newRemoteSpace(t)
			s := f.s
			mustCreate(t, s, "A", meta("title", "A"), "x\n")
			if _, err := putCurrent(s, "wenan", allKinds()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.BindFolder(Author{}, "A", "wenan"); err != nil {
				t.Fatal(err)
			}
			mustCreate(t, s, "B", meta("title", "B"), "x\n")
			out := outsideDir(t)
			before := treeOf(t, out)
			o := f.theirs()
			os.RemoveAll(filepath.Join(o.dir, ".petalbook"))
			if err := os.Symlink(out, filepath.Join(o.dir, ".petalbook")); err != nil {
				t.Fatal(err)
			}
			o.push("a link in the place of .petalbook")
			if err := s.Pull(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			f.net.beforePush = func() {
				once.Do(func() {
					if how == "the network is down" {
						f.net.setDown(true)
						return
					}
					o.write("theirs.md", pageText("THEMS00009", "Theirs", "t\n"))
					o.push("theirs")
				})
			}
			_, err := s.BindFolder(Author{}, "B", "wenan")
			if how == "the network is down" {
				if err == nil {
					t.Fatal("the change went through with the network down")
				}
				// Nothing of the repair is left in the clone: it is as the remote.
				if git(t, f.root, "rev-parse", "HEAD") != f.tip() || !isLink(f.root, ".petalbook") {
					t.Fatal("the repair was left behind")
				}
				if st := git(t, f.root, "status", "--porcelain", "--untracked-files=all"); st != "" {
					t.Fatalf("the clone has changes:\n%s", st)
				}
				f.net.setDown(false)
				if _, err = s.BindFolder(Author{}, "B", "wenan"); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			untouched(t, out, before)
			if err := s.Pull(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if st, _ := s.Remote(); st.Kind == KindRescued || st.State != RemoteOK {
				t.Errorf("the status: %+v", st)
			}
			if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A": "wenan", "B": "wenan"}) {
				t.Errorf("the bindings are %v", m)
			}
			if git(t, f.root, "rev-parse", "HEAD") != f.tip() {
				t.Error("not at the tip")
			}
			if n := strings.Count(git(t, f.remote, "ls-tree", "--name-only", "main"), ".petalbook"+unreadableMark); n != 1 {
				t.Errorf("%d copies of the link on the remote", n)
			}
			if refs := git(t, f.root, "for-each-ref", "--format=%(refname)"); strings.Contains(refs, "unpushed") {
				t.Errorf("something was kept as never pushed:\n%s", refs)
			}
			if st := git(t, f.root, "status", "--porcelain", "--untracked-files=all"); st != "" {
				t.Errorf("the clone has changes:\n%s", st)
			}
			consistent(t, s)
		})
	}
}

// --- a link put on the way while a file is read --------------------------------------

func TestALinkSwappedInWhileTheSettingsAreReadIsNeverFollowed(t *testing.T) {
	t.Parallel()
	s := settled(t)
	root := s.Root()
	if err := s.SetTagColor(Author{}, "x", "blue"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	out := outsideDir(t)
	before := treeOf(t, out)
	for _, which := range []string{".petalbook", typesDir} {
		target := out
		if which == typesDir {
			target = filepath.Join(out, "types")
		}
		at := filepath.Join(root, filepath.FromSlash(which))
		aside := at + "-real"
		var stop atomic.Bool
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				// The folder goes, a link to outside the space takes its place,
				// and the folder comes back: over and over.
				if os.Rename(at, aside) != nil {
					continue
				}
				os.Symlink(target, at)
				time.Sleep(50 * time.Microsecond)
				os.Remove(at)
				os.Rename(aside, at)
			}
		}()
		deadline := time.Now().Add(time.Second)
		for n := 0; time.Now().Before(deadline) || n < 50; n++ {
			if _, ok := bindingMap(s)["Elsewhere"]; ok {
				t.Fatalf("%s: a binding was read from outside the space", which)
			}
			if names, _ := s.typeNames(); strings.Contains(fmt.Sprint(names), "outsider") {
				t.Fatalf("%s: a type was listed from outside the space", which)
			}
			if _, ok := s.typeInfoCached("outsider"); ok {
				t.Fatalf("%s: a type was read from outside the space", which)
			}
			if _, ok := s.TagColors()["outside"]; ok {
				t.Fatalf("%s: a tag was read from outside the space", which)
			}
		}
		stop.Store(true)
		wg.Wait()
		if isLink(root, which) {
			os.Remove(at)
		}
		if _, err := os.Lstat(aside); err == nil {
			os.Rename(aside, at)
		}
		untouched(t, out, before)
	}
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A": "wenan"}) {
		t.Errorf("the bindings are %v", m)
	}
}

// --- a link spelled otherwise, on a file system that ignores case ------------------------

func TestALinkSpelledPetalBookIsNeverFollowed(t *testing.T) {
	t.Parallel()
	s := settled(t)
	root := s.Root()
	crash(s)
	out := outsideDir(t)
	before := treeOf(t, out)
	os.RemoveAll(filepath.Join(root, ".petalbook"))
	if err := os.Symlink(out, filepath.Join(root, ".PetalBook")); err != nil {
		t.Fatal(err)
	}
	_, err := os.Lstat(filepath.Join(root, ".petalbook"))
	folds := err == nil
	t.Logf("the file system folds case: %v", folds)
	s = openAt(t, root)
	untouched(t, out, before)
	if _, ok := bindingMap(s)["Elsewhere"]; ok {
		t.Fatal("a binding was read from outside the space")
	}
	for _, n := range typeNamesOf(t, s) {
		if n == "outsider" {
			t.Fatal("a type was read from outside the space")
		}
	}
	if folds {
		// .PetalBook is .petalbook here: it is put right like it, with what the
		// folder had.
		if !isRealDir(root, ".petalbook") {
			t.Fatal(".petalbook is not a folder again")
		}
		if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A": "wenan"}) {
			t.Errorf("the bindings are %v", m)
		}
	}
	if !folds {
		return
	}
	if _, err := s.BindFolder(Author{}, "B", "wenan"); err != nil {
		t.Fatal(err)
	}
	untouched(t, out, before)
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"A": "wenan", "B": "wenan"}) {
		t.Errorf("the bindings are %v", m)
	}
}

// --- what can be read of an unusable file is read in time linear in it ----------------

// typoFile is a space.yml of about size bytes: one entry with a typo, the
// rest entries that can be used.
func typoFile(size int) []byte {
	var b strings.Builder
	b.WriteString("name: Mine\nfolders:\n  Typo: {type: wenan\n")
	for i := 0; b.Len() < size-64; i++ {
		fmt.Fprintf(&b, "  F%07d:\n    type: wenan\n", i)
	}
	return []byte(b.String())
}

// salvageTime is how long what can be read of data takes to cut and merge.
func salvageTime(t *testing.T, data []byte) time.Duration {
	t.Helper()
	start := time.Now()
	pieces, lost := salvagePieces(spaceSettings, data)
	if len(pieces) < len(data)/40 || !reflect.DeepEqual(lost, []string{"Typo"}) {
		t.Fatalf("%d pieces, lost %v", len(pieces), lost)
	}
	if _, _, ok := mergePieces(spaceSettings, nil, pieces); !ok {
		t.Fatal("not merged")
	}
	return time.Since(start)
}

func TestSalvagingALargeFileWithOneTypoIsLinear(t *testing.T) {
	s := settled(t)
	full := maxSalvage(spaceSettings)
	small, large := full/4, full
	if raceDetector {
		// Every reading of YAML costs several times as much: the same
		// proportion, on a quarter of the text.
		small, large = full/32, full/8
	}
	salvageTime(t, typoFile(small)) // warm
	// Four times the text takes about four times as long; quadratic, sixteen.
	// A machine that is busy with other work slows one of the two readings and
	// not the other, so the comparison is made again before it fails: code that
	// is quadratic fails every time.
	var ts, tl time.Duration
	for attempt := 1; ; attempt++ {
		ts = salvageTime(t, typoFile(small))
		tl = salvageTime(t, typoFile(large))
		if tl <= 8*ts+200*time.Millisecond {
			break
		}
		if attempt == 3 {
			t.Fatalf("%d bytes took %v, %d bytes %v, three times", small, ts, large, tl)
		}
		t.Logf("attempt %d: %d bytes took %v, %d bytes %v", attempt, small, ts, large, tl)
	}
	t.Logf("%d bytes: %v; %d bytes: %v", small, ts, large, tl)
	if raceDetector {
		return
	}
	// The largest file read, through the space: quadratic, this took minutes.
	data := typoFile(full)
	setSpaceYML(t, s.Root(), string(data))
	start := time.Now()
	m := bindingMap(s)
	took := time.Since(start)
	if m["F0000001"] != "wenan" || m["A"] != "wenan" {
		t.Errorf("%d bindings", len(m))
	}
	if took > 10*time.Second {
		t.Fatalf("reading %d bytes with a typo took %v", len(data), took)
	}
	t.Logf("%d bytes through the space: %v", len(data), took)
}

func TestSalvageNeverMakesUpAnEntry(t *testing.T) {
	t.Parallel()
	lines, want := thirty()
	for _, c := range []struct {
		name  string
		text  string
		extra map[string]string
	}{
		{"a byte order mark", "\ufeffname: Mine\n" + lines + "  F31: {type: wenan\n", nil},
		{"folders inside a quoted value", "name: Mine\n" + lines + "  N:\n    note: \"a line\nfolders:\n  Z:\n    type: wenan\"\n  F31: {type: wenan\n  F32:\n    type: wenan\n", map[string]string{"F32": "wenan"}},
		{"an entry inside a quoted value", "name: Mine\n" + lines + "  N:\n    note: 'it''s\n  Z:\n    type: wenan'\n  F31: {type: wenan\n", nil},
		{"a top-level value", "name: \"Mine\nfolders:\n  Z:\n    type: wenan\"\n" + lines + "  F31: {type: wenan\n", nil},
		{"a quote that is never closed", "name: Mine\n" + lines + "  N:\n    note: \"no end\n  F31: {type: wenan\n  F32:\n    type: wenan\n", map[string]string{"F32": "wenan"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := settled(t)
			setSpaceYML(t, s.Root(), c.text)
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
		})
	}
}
