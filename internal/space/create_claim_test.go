package space

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/index"
)

// A create that has to pass over many names the file system holds journals a
// fixed number of records, not some for every name it tried: each record is
// a synced append, and a folder full of names the listing does not show would
// otherwise cost a thousand of them under the space lock.
func TestCreatePastManyTakenNamesJournalsAFixedNumberOfRecords(t *testing.T) {
	t.Parallel()
	s := open(t)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	const refused = 50
	tries := 0
	ops := s.ops
	real := ops.Link
	ops.Link = func(from, to string) error {
		if tries++; tries <= refused {
			return fs.ErrExist
		}
		return real(from, to)
	}
	ops.ReadDir = func(string) ([]fs.DirEntry, error) { return nil, nil }
	s.ops = ops
	before := s.cm.j.lastSeq()
	p, err := s.Create(Author{}, CreateInput{Meta: meta("title", "Busy"), Body: "x\n"})
	if err != nil {
		t.Fatal(err)
	}
	s.ops = osOps()
	if p.Path != "Busy 51.md" {
		t.Fatalf("created at %q", p.Path)
	}
	if n := s.cm.j.lastSeq() - before; n > 4 {
		t.Fatalf("one create journaled %d records after %d refused names", n, refused)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, _ := gitTry(s.Root(), "ls-files"); !strings.Contains(got, "Busy 51.md") {
		t.Fatalf("the page was not committed:\n%s", got)
	}
}

// crashAtCreateStep runs a create that dies (as kill -9 would) at the named
// step, after the name was linked, and returns the space's root. The
// temporary file of the create is put back as a crash leaves it: the panic
// that stands for the crash runs the deferred clean-up a dead process never
// would.
func crashAtCreateStep(t *testing.T, step string) string {
	t.Helper()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	keep := t.TempDir()
	type crashed struct{}
	var temps []string
	s.step = func(name string) error {
		if name != step {
			return nil
		}
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if isTempName(e.Name()) {
				if err := os.Link(filepath.Join(root, e.Name()), filepath.Join(keep, e.Name())); err != nil {
					t.Error(err)
				}
				temps = append(temps, e.Name())
			}
		}
		panic(crashed{})
	}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("the create never reached %q", step)
			} else if _, ok := r.(crashed); !ok {
				panic(r)
			}
		}()
		s.Create(Author{}, CreateInput{Meta: meta("title", "Claimed"), Body: "x\n"})
	}()
	crash(s)
	if len(temps) == 0 {
		t.Fatal("test setup: no temporary file at the crash")
	}
	for _, n := range temps {
		os.Remove(filepath.Join(root, n))
		if err := os.Link(filepath.Join(keep, n), filepath.Join(root, n)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "Claimed.md")); err != nil {
		t.Fatalf("test setup: the name was not taken: %v", err)
	}
	return root
}

// The name is taken before the create is journaled: a crash in between
// leaves a file nobody was told of, and the next start takes it back.
func TestCrashAfterACreateTookItsNameTakesTheNameBack(t *testing.T) {
	t.Parallel()
	root := crashAtCreateStep(t, "name claimed")
	s := openAt(t, root)
	if exists(s, "Claimed.md") {
		t.Fatal("the name taken before the crash is still there")
	}
	r, _ := s.List(index.Query{Limit: 50})
	if len(r.Items) != 0 {
		t.Fatalf("%+v", r.Items)
	}
	if probs, _ := s.Problems(); len(probs) != 0 {
		t.Fatalf("%+v", probs)
	}
	if left := tempsIn(root, ""); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// Once journaled, the create is kept and committed, even though the crash
// came before it was told it was made.
func TestCrashAfterACreateWasJournaledKeepsAndCommitsIt(t *testing.T) {
	t.Parallel()
	root := crashAtCreateStep(t, "create journaled")
	s := openAt(t, root)
	if !exists(s, "Claimed.md") {
		t.Fatal("the journaled create was taken back")
	}
	r, _ := s.List(index.Query{Limit: 50})
	if len(r.Items) != 1 || r.Items[0].Path != "Claimed.md" {
		t.Fatalf("%+v", r.Items)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, _ := gitTry(root, "ls-files"); !strings.Contains(got, "Claimed.md") {
		t.Fatalf("the page was not committed:\n%s", got)
	}
}
