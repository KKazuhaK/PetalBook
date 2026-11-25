package space

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A move whose rewrite of the links to it is half written when a commit
// comes (the idle timer fires in the middle): the commit takes none of it,
// and the move with all its rewrites is one commit.
func TestAMoveIsOneCommitEvenWhenACommitComesInTheMiddle(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	target := mustCreate(t, s, "", meta("title", "Target"), "t\n")
	var from []*Page
	for _, n := range []string{"One", "Two", "Three"} {
		from = append(from, mustCreate(t, s, "Notes", meta("title", n), "see [Target](../Target.md)\n"))
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	before := len(commits(t, root))
	written := 0
	s.step = func(step string) error {
		if step == "edit written" {
			if written++; written == 1 {
				if err := s.cm.commit(); err != nil {
					t.Error(err)
				}
			}
		}
		return nil
	}
	if _, err := s.Move(Author{}, target.ID, "Archive"); err != nil {
		t.Fatal(err)
	}
	s.step = nil
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if c := commits(t, root); len(c) != before+1 {
		t.Fatalf("the move made %d commits: %q", len(c)-before, c[:len(c)-before])
	}
	for _, p := range from {
		if got := git(t, root, "show", "HEAD:"+p.Path); !strings.Contains(got, "../Archive/Target.md") {
			t.Errorf("%s is not committed rewritten:\n%s", p.Path, got)
		}
	}
}

// A save, then an edit of the same file outside PetalBook before the commit:
// Git records what the server wrote, and the edit stays in the work tree, not
// committed.
func TestAnOutsideEditBeforeTheCommitStaysUncommitted(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "Kept"), "v1\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "Kept"), "v2\n"); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(root, filepath.FromSlash(p.Path))
	server, _ := os.ReadFile(abs)
	outside := strings.Replace(string(server), "v2", "v3 from outside", 1)
	os.WriteFile(abs, []byte(outside), 0o644)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	checkServerCommitted(t, root, p.Path, string(server), outside)
}

func checkServerCommitted(t *testing.T, root, rel, server, outside string) {
	t.Helper()
	if got := git(t, root, "show", "HEAD:"+rel); strings.TrimSpace(got) != strings.TrimSpace(server) {
		t.Errorf("committed:\n%s\nwant what the server wrote:\n%s", got, server)
	}
	if got, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))); string(got) != outside {
		t.Errorf("the work tree lost the edit made outside:\n%s", got)
	}
	if git(t, root, "diff", "--name-only", "--", rel) != rel || git(t, root, "diff", "--cached", "--name-only", "--", rel) != "" {
		t.Errorf("the edit made outside should be an uncommitted change:\n%s", git(t, root, "status", "--short"))
	}
}

// A page the server made, or a save it wrote, changed outside PetalBook
// before the next start committed it: Git records what the server wrote, and
// the outside edit stays in the work tree, not committed.
func TestAChangeEditedOutsideBeforeRecoveryIsCommittedAsTheServerWroteIt(t *testing.T) {
	needGit(t)
	for _, c := range []string{"created", "saved"} {
		t.Run(c, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			s := openAt(t, root)
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			p := mustCreate(t, s, "", meta("title", "Made"), "v1\n")
			if c == "saved" {
				if err := s.Flush(); err != nil {
					t.Fatal(err)
				}
				var err error
				if p, err = s.Save(Author{}, p.ID, p.Rev, meta("title", "Made"), "v2\n"); err != nil {
					t.Fatal(err)
				}
			}
			crash(s)
			abs := filepath.Join(root, filepath.FromSlash(p.Path))
			server, _ := os.ReadFile(abs)
			outside := string(server) + "added outside\n"
			os.WriteFile(abs, []byte(outside), 0o644)
			s2 := openAt(t, root)
			if err := s2.Flush(); err != nil {
				t.Fatal(err)
			}
			checkServerCommitted(t, root, p.Path, string(server), outside)
		})
	}
}
