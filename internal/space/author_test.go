package space

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultCommitIdleIsAFewSeconds(t *testing.T) {
	// A draft is applied after its own idle time (see the drafts package); the
	// file it writes must reach Git within seconds, not after another long wait.
	if DefaultCommitIdle > 10*time.Second || DefaultCommitIdle <= 0 {
		t.Fatalf("commit idle default is %v", DefaultCommitIdle)
	}
	if DefaultCommitMax < time.Minute {
		t.Fatalf("commit max default is %v", DefaultCommitMax)
	}
}

func TestAuthorAndCommitterAreSeparate(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root, func(o *Options) {
		o.CommitIdle, o.CommitMax = time.Hour, time.Hour
		o.Committer = "Service <svc@example.com>"
		o.Author = "Ann Lee <ann@example.com>"
	})
	mustCreate(t, s, "", meta("title", "one"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	who := func() string {
		out, err := gitTry(root, "log", "-1", "--format=%an <%ae>|%cn <%ce>")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := who(); got != "Ann Lee <ann@example.com>|Service <svc@example.com>" {
		t.Fatalf("%q", got)
	}
	// Changing the author applies to later commits only.
	s.SetAuthor("Bo Chen", "bo@example.com")
	mustCreate(t, s, "", meta("title", "two"), "y\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := who(); got != "Bo Chen <bo@example.com>|Service <svc@example.com>" {
		t.Fatalf("%q", got)
	}
	log, _ := gitTry(root, "log", "--format=%an")
	if !strings.Contains(log, "Ann Lee") {
		t.Fatal("history was rewritten")
	}
}

func TestSingleUserAuthorStaysTheCommitter(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root, func(o *Options) {
		o.CommitIdle, o.CommitMax = time.Hour, time.Hour
		o.Committer = "Test <t@example.com>"
	})
	mustCreate(t, s, "", meta("title", "one"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, _ := gitTry(root, "log", "-1", "--format=%an <%ae>|%cn <%ce>"); got != "Test <t@example.com>|Test <t@example.com>" {
		t.Fatalf("%q", got)
	}
	if _, err := Open(Options{Root: t.TempDir(), Author: "no email"}); err == nil {
		t.Fatal("a malformed author was accepted")
	}
}

func TestCheckDraftValidatesWithoutWriting(t *testing.T) {
	needGit(t)
	s := openAt(t, t.TempDir())
	p := mustCreate(t, s, "", meta("title", "T"), "x\n")
	before := p.Rev
	cur, err := s.CheckDraft(p.ID, meta("title", "T2"), "new body\n")
	if err != nil || cur.Rev != before || cur.Body != "x\n" {
		t.Fatalf("%v %v", cur, err)
	}
	if _, err := s.CheckDraft(p.ID, meta("id", "0000000000"), "x"); err == nil {
		t.Fatal("a draft with another id was accepted")
	} else if _, ok := err.(*InvalidError); !ok {
		t.Fatalf("%T", err)
	}
	if _, err := s.CheckDraft(p.ID, meta(), "bad \xff"); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if _, err := s.CheckDraft("0000000000", meta(), "x"); err != ErrNotFound {
		t.Fatalf("%v", err)
	}
	if got, _ := s.Get(p.ID); got.Rev != before {
		t.Fatal("CheckDraft wrote")
	}
}

func TestChangesAfterCloseAreRefusedBeforeAnythingIsWritten(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root, func(o *Options) { o.CommitIdle, o.CommitMax = time.Hour, time.Hour })
	p := mustCreate(t, s, "", meta("title", "T"), "x\n")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := gitTry(root, "status", "--porcelain")
	if _, err := s.Create(CreateInput{Meta: meta("title", "Late")}); err != ErrClosed {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Save(p.ID, p.Rev, meta("title", "T"), "changed\n"); err != ErrClosed {
		t.Fatalf("save: %v", err)
	}
	if _, err := s.Move(p.ID, "Sub"); err != ErrClosed {
		t.Fatalf("move: %v", err)
	}
	if err := s.Delete(p.ID); err != ErrClosed {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.CreateFolder("Folder"); err != ErrClosed {
		t.Fatalf("folder: %v", err)
	}
	if _, err := s.Reindex(); err != ErrClosed {
		t.Fatalf("reindex: %v", err)
	}
	if after, _ := gitTry(root, "status", "--porcelain"); after != before {
		t.Fatalf("a refused change touched the files:\n%s\n--\n%s", before, after)
	}
	if _, err := os.Stat(filepath.Join(root, "Late.md")); err == nil {
		t.Fatal("a file was written after close")
	}
}
