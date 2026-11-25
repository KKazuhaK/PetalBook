package space

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

var (
	ann = Author{Name: "Ann Lee", Email: "ann@example.test"}
	bo  = Author{Name: "Bo Chen", Email: "bo@example.test"}
)

// commitLog lists the commits newest first as "author|committer|subject|files".
func commitLog(t *testing.T, dir string) []string {
	t.Helper()
	out, err := gitTry(dir, "log", "--format=@%an|%cn|%s", "--name-only")
	if err != nil {
		t.Fatal(err)
	}
	var log []string
	for _, chunk := range strings.Split(out, "@")[1:] {
		head, files, _ := strings.Cut(strings.TrimSpace(chunk), "\n")
		log = append(log, head+"|"+strings.Join(strings.Fields(strings.ReplaceAll(files, "\n", " ")), ","))
	}
	return log
}

func openTwoAuthors(t *testing.T, root string) *Space {
	return openAt(t, root, func(o *Options) {
		o.Committer = "Service <svc@example.test>"
		o.Author = "Owner <owner@example.test>"
	})
}

func TestTwoAuthorsInOneWindowMakeTwoCommitsEachTheirOwn(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openTwoAuthors(t, root)
	s.Flush() // the start-up files
	a, err := s.Create(ann, CreateInput{Meta: meta("title", "Alpha"), Body: "a\n"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(bo, CreateInput{Meta: meta("title", "Beta"), Body: "b\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	log := commitLog(t, root)
	want := []string{
		"Bo Chen|Service|Add Beta|" + b.Path,
		"Ann Lee|Service|Add Alpha|" + a.Path,
	}
	if len(log) < 2 || log[0] != want[0] || log[1] != want[1] {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(log, "\n"), strings.Join(want, "\n"))
	}
}

func TestOneAuthorIsOneCommitAndOneGitRun(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openTwoAuthors(t, root)
	s.Flush()
	// The zero author and the owner spelled out are the same person.
	mustCreate(t, s, "", meta("title", "One"), "1\n")
	if _, err := s.Create(Author{Name: "Owner", Email: "owner@example.test"}, CreateInput{Meta: meta("title", "Two"), Body: "2\n"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	commitsMade := 0
	s.cm.git.before = func(args []string) {
		if args[0] == "commit-tree" || args[0] == "commit" {
			mu.Lock()
			commitsMade++
			mu.Unlock()
		}
	}
	before := len(commitLog(t, root))
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	log := commitLog(t, root)
	if len(log) != before+1 || !strings.HasPrefix(log[0], "Owner|Service|Add 2 pages|") {
		t.Fatalf("%q", log)
	}
	if commitsMade != 1 {
		t.Fatalf("%d commits made for one author", commitsMade)
	}
}

// A second person's change to a path commits the first person's work first, so
// the path's history shows both, each as its author.
func TestAPathChangedByTwoPeopleIsOneCommitEach(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openTwoAuthors(t, root)
	s.Flush()
	p, err := s.Create(ann, CreateInput{Meta: meta("title", "Shared"), Body: "ann\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(bo, p.ID, p.Rev, meta("title", "Shared"), "bo\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	log := commitLog(t, root)
	if len(log) < 2 || log[0] != "Bo Chen|Service|Edit Shared|"+p.Path || log[1] != "Ann Lee|Service|Add Shared|"+p.Path {
		t.Fatalf("%q", log)
	}
}

// When the commit of the earlier author fails, the next person's change still
// goes through (it is not refused for someone else's error), and a later
// commit makes both, each as its author.
func TestAFailedSwitchCommitDoesNotBlockTheNextChange(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openTwoAuthors(t, root)
	s.Flush()
	lock := filepath.Join(root, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := s.Create(ann, CreateInput{Meta: meta("title", "Alpha"), Body: "a\n"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(bo, CreateInput{Meta: meta("title", "Beta"), Body: "b\n"})
	if err != nil {
		t.Fatalf("a failed commit of the earlier author blocked the change: %v", err)
	}
	if s.cm.paths[a.Path] != ann || s.cm.paths[b.Path] != bo {
		t.Fatalf("pending paths lost their authors: %v", s.cm.paths)
	}
	os.Remove(lock)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	log := commitLog(t, root)
	if len(log) < 2 || log[0] != "Bo Chen|Service|Add Beta|"+b.Path || log[1] != "Ann Lee|Service|Add Alpha|"+a.Path {
		t.Fatalf("%q", log)
	}
}

func TestJournalAfterACommitKeepsTheAuthorsOfWhatIsStillPending(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openTwoAuthors(t, root)
	s.Flush()
	// A change that arrives while git runs is pending again, as theirs.
	var once sync.Once
	var late *Page
	s.cm.git.before = func(args []string) {
		if args[0] == "add" || args[0] == "hash-object" { // staging the commit
			once.Do(func() {
				var err error
				if late, err = s.Create(bo, CreateInput{Meta: meta("title", "Late"), Body: "l\n"}); err != nil {
					t.Error(err)
				}
			})
		}
	}
	mustCreate(t, s, "", meta("title", "Early"), "e\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// What is left is the page made while git ran: its transaction, by Bo.
	recs := journalRecords(t, root)
	if len(recs) != 3 || recs[0].Op != "x" || recs[0].author() != bo || recs[1].Op != "p" || recs[1].Path != late.Path || recs[2].Op != "a" {
		t.Fatalf("journal: %+v", recs)
	}
}

func TestCrashRecoveryCommitsEachPersonsChangesAsThem(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openTwoAuthors(t, root)
	s.Flush()
	// Commits fail while the lock is there, so several people's changes are
	// pending together when the process dies.
	lock := filepath.Join(root, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	a, _ := s.Create(ann, CreateInput{Meta: meta("title", "Alpha"), Body: "a\n"})
	b, _ := s.Create(bo, CreateInput{Meta: meta("title", "Beta"), Body: "b\n"})
	if err := s.Delete(ann, b.ID); err != nil { // Ann deletes what Bo made: hers now
		t.Fatal(err)
	}
	c, _ := s.Create(bo, CreateInput{Meta: meta("title", "Gamma"), Body: "c\n"})
	crash(s)
	os.Remove(lock)

	s2 := openTwoAuthors(t, root)
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	log := commitLog(t, root)
	if len(log) < 2 || log[0] != "Bo Chen|Service|Add Gamma|"+c.Path || log[1] != "Ann Lee|Service|Add Alpha|"+a.Path {
		t.Fatalf("%q", log)
	}
	for _, l := range log {
		if strings.Contains(l, b.Path) {
			t.Fatalf("a page created and deleted before any commit reached history: %q", l)
		}
	}
}

func TestOldJournalWithoutAuthorsRecoversAsTheDefaultAuthor(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openTwoAuthors(t, root)
	s.Flush()
	state := s.StateDir()
	s.Close()
	data := []byte(pageText("OLDJRNL001", "Old", "from an old journal\n"))
	if err := os.WriteFile(filepath.Join(root, "Old.md"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	line := `{"op":"w","p":"Old.md","rev":"` + page.Rev(data) + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(state, "pending.journal"), framed(line), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := openTwoAuthors(t, root)
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	if log := commitLog(t, root); log[0] != "Owner|Service|Add Old|Old.md" {
		t.Fatalf("%q", log)
	}
}

func TestRemoteChangesAreCommittedAsTheirAuthor(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	p, err := f.s.Create(bo, CreateInput{Meta: meta("title", "ByBo"), Body: "b\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Save(ann, p.ID, p.Rev, meta("title", "ByBo"), "ann edits\n"); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, f.s, "", meta("title", "ByDefault"), "d\n")
	got := git(f.t, f.remote, "log", "--format=%an|%cn|%s", "main")
	want := "Ann Lee|Service|Edit ByBo\nBo Chen|Service|Add ByBo"
	if !strings.Contains(got, want) || !strings.HasPrefix(got, "Ann|Service|Add ByDefault\n") {
		t.Fatalf("got\n%s", got)
	}
}

func TestRemoteRecoveryCommitsEachRecordedAuthor(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	write := func(by Author, title, id string) {
		data := []byte(pageText(id, title, "written\n"))
		if err := f.s.cm.Record(by, jrec{Op: "w", Path: title + ".md", Rev: page.Rev(data)}); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.root, title+".md"), data, 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	write(ann, "FromAnn", "ANNPAGE001")
	write(bo, "FromBo", "BOPAGE0001")
	crash(f.s)
	f.reopen()
	log := commitLog(t, f.root)
	if len(log) < 2 || !strings.HasPrefix(log[0], "Bo Chen|Service|Add FromBo|") || !strings.HasPrefix(log[1], "Ann Lee|Service|Add FromAnn|") {
		t.Fatalf("%q", log)
	}
}

func TestRecoveredAuthorOfAPathIsTheLastRecordOfIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	got := recoverPending(root, []jrec{
		{Op: "t", Path: "a.md", AuthorName: ann.Name, AuthorEmail: ann.Email},
		{Op: "t", Path: "a.md", AuthorName: bo.Name, AuthorEmail: bo.Email},
		{Op: "t", Path: "b.md"},
	})
	if got["a.md"] != bo || got["b.md"] != (Author{}) || len(got) != 2 {
		t.Fatalf("%v", got)
	}
}

func TestHalfFilledAuthorIsTheDefaultAuthor(t *testing.T) {
	t.Parallel()
	g := &gitRepo{name: "Service", email: "svc@example.test"}
	g.setAuthor("Owner", "owner@example.test")
	for _, by := range []Author{{}, {Name: "Only Name"}, {Email: "only@example.test"}} {
		if a, c := g.identities(by); a.Name != "Owner" || c.Name != "Service" {
			t.Fatalf("%+v -> %+v %+v", by, a, c)
		}
	}
	if a, _ := g.identities(ann); a.Name != ann.Name || a.Email != ann.Email {
		t.Fatalf("%+v", a)
	}
}

// Quiet-period commits group the same way as a flush.
func TestQuietPeriodCommitsPerAuthor(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root, func(o *Options) {
		o.Committer = "Service <svc@example.test>"
		o.CommitIdle, o.CommitMax = 300*time.Millisecond, time.Hour
	})
	s.Flush()
	mustCreate(t, s, "", meta("title", "Quiet"), "q\n")
	if _, err := s.Create(ann, CreateInput{Meta: meta("title", "Loud"), Body: "l\n"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "both commits", 5*time.Second, func() bool {
		log, _ := gitTry(root, "log", "--format=%an|%s")
		return strings.Contains(log, "Ann Lee|Add Loud") && strings.Contains(log, "Service|Add Quiet")
	})
}
