package space

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Someone has a referring page open while the page it links to is renamed: their
// save, based on the rev before the service rewrote the link, is accepted with
// the same rewrite made to their text; the old path is not written back.
func TestSaveBasedBeforeALinkRewriteIsRebased(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	tg := mustCreate(t, s, "Tang", meta("title", "暮雪"), "x\n")
	ref := mustCreate(t, s, "Notes", meta("title", "引用"), "见[暮雪](../Tang/暮雪.md)。\n")
	// The editor has ref open at ref.Rev; meanwhile the target is renamed.
	mustSave(t, s, tg, "夜雪", "x\n")
	if got := readDisk(t, s, ref.Path); !strings.Contains(got, "../Tang/夜雪.md") {
		t.Fatalf("not rewritten:\n%s", got)
	}
	p, err := s.Save(Author{}, ref.ID, ref.Rev, meta("title", "引用"), "见[暮雪](../Tang/暮雪.md)。\n\n我新写的一段。\n")
	if err != nil {
		t.Fatalf("save after the rewrite: %v", err)
	}
	got := readDisk(t, s, p.Path)
	if !strings.Contains(got, "见[暮雪](../Tang/夜雪.md \""+tg.ID+"\")。\n\n我新写的一段。\n") || strings.Contains(got, "暮雪.md") {
		t.Fatalf("%q", got)
	}
	// A save based on a rev that a person changed is still a conflict.
	if _, err := s.Save(Author{}, ref.ID, ref.Rev, meta("title", "引用"), "again\n"); !errors.As(err, new(*ConflictError)) {
		t.Fatalf("want a conflict, got %v", err)
	}

	// The moved page itself, open in an editor, after a move: its relative
	// links are written from where it is now. The steps survive a restart.
	o := mustCreate(t, s, "O", meta("title", "外"), "x\n")
	m := mustCreate(t, s, "A", meta("title", "移"), "[o](../O/外.md)\n")
	m2, err := s.Move(Author{}, m.ID, "B/C")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openAt(t, root)
	if !s.Rebasable(m.ID, m.Rev, m2.Rev) {
		t.Fatal("the rewrite was forgotten at the restart")
	}
	p, err = s.Save(Author{}, m.ID, m.Rev, meta("title", "移"), "[o](../O/外.md) 加一句\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := readDisk(t, s, p.Path); !strings.Contains(got, "[o](../../O/外.md \""+o.ID+"\") 加一句") {
		t.Fatalf("%q", got)
	}
}

// A save keeps no stale path for a link whose id is that of a page somewhere
// else: the path follows the id, nothing else changes.
func TestSaveFixesThePathOfALinkByItsID(t *testing.T) {
	t.Parallel()
	s := open(t)
	tg := mustCreate(t, s, "N", meta("title", "目标"), "x\n")
	b := mustCreate(t, s, "", meta("title", "乙"), "x\n")
	b2 := mustSave(t, s, b, "乙", "[t](旧/目标.md#s \""+tg.ID+"\") [z](x.md \"01JD3K8Q7Z\")\n")
	if got := readDisk(t, s, b2.Path); !strings.Contains(got, "[t](N/目标.md#s \""+tg.ID+"\") [z](x.md \"01JD3K8Q7Z\")\n") {
		t.Fatalf("%q", got)
	}
}

// A link that carries an id two files claim is not rewritten by a rename, as
// the repair after an outside rename leaves it.
func TestRenameLeavesLinksToDuplicatedIDs(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	tg := mustCreate(t, s, "", meta("title", "目标"), "x\n")
	ref := mustCreate(t, s, "", meta("title", "引"), "[t](目标.md \""+tg.ID+"\")\n")
	s.Close()
	b, _ := os.ReadFile(filepath.Join(root, "目标.md"))
	os.WriteFile(filepath.Join(root, "目标 copy.md"), b, 0o644)
	s = openAt(t, root)
	cur, err := s.Get(tg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(Author{}, tg.ID, cur.Rev, meta("title", "新名"), "x\n"); err != nil {
		t.Fatal(err)
	}
	if got := readDisk(t, s, ref.Path); !strings.Contains(got, "[t](目标.md \""+tg.ID+"\")") {
		t.Fatalf("%q", got)
	}
}
