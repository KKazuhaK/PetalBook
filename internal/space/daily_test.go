package space

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/index"
)

func dailyFolder(s *Space, folder string, filter ...ViewFilter) *DailySource {
	return &DailySource{Folder: &folder, Filter: filter}
}

func mustDaily(t *testing.T, s *Space, spaceID, date string, skip int) *DailyPick {
	t.Helper()
	p, none, err := s.DailyText(spaceID, date, skip)
	if err != nil || p == nil {
		t.Fatalf("daily %s/%d: %v %v", date, skip, none, err)
	}
	return p
}

func TestDailyIsTheSameForADayAndMovesWithTheDayAndSkip(t *testing.T) {
	s := open(t)
	ids := map[string]bool{}
	for i := 0; i < 7; i++ {
		ids[mustCreate(t, s, "文案", meta("title", fmt.Sprint("p", i)), fmt.Sprintf("《p%d》｜x\n", i)).ID] = true
	}
	if _, err := s.SetDaily(Author{}, dailyFolder(s, "文案")); err != nil {
		t.Fatal(err)
	}
	first := mustDaily(t, s, "SPACE1", "2026-11-23", 0)
	for i := 0; i < 5; i++ {
		if got := mustDaily(t, s, "SPACE1", "2026-11-23", 0); got.Page.ID != first.Page.ID || got.Count != 7 || got.Truncated {
			t.Fatalf("%+v", got)
		}
	}
	// The same day in a space of another id may be another page, and over a
	// month the days and the spaces spread over the pages.
	days, other := map[string]bool{}, 0
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for d := 0; d < 30; d++ {
		date := t0.AddDate(0, 0, d).Format("2006-01-02")
		a := mustDaily(t, s, "SPACE1", date, 0)
		if !ids[a.Page.ID] {
			t.Fatalf("%s is not one of the pages", a.Page.ID)
		}
		days[a.Page.ID] = true
		if b := mustDaily(t, s, "SPACE2", date, 0); b.Page.ID != a.Page.ID {
			other++
		}
	}
	if len(days) < 5 || other < 10 {
		t.Errorf("30 days showed %d of 7 pages; the other space differed on %d days", len(days), other)
	}
	// skip goes along the pages: each is another page until it comes round.
	seen := map[string]bool{}
	for k := 0; k < 7; k++ {
		seen[mustDaily(t, s, "SPACE1", "2026-11-23", k).Page.ID] = true
	}
	if len(seen) != 7 {
		t.Errorf("skip 0..6 showed %d pages", len(seen))
	}
	if mustDaily(t, s, "SPACE1", "2026-11-23", 7).Page.ID != first.Page.ID {
		t.Error("skip 7 is not the day's page")
	}
	// What a request may not ask.
	for _, c := range []struct {
		date string
		skip int
	}{{"23 Nov", 0}, {"2026-02-30", 0}, {"2026-11-23", -1}, {"2026-11-23", 1 << 21}} {
		if _, _, err := s.DailyText("S", c.date, c.skip); err == nil {
			t.Errorf("%v is accepted", c)
		}
	}
}

func TestDailyKeepsTheDaysPickWhilePagesComeAndGo(t *testing.T) {
	s := open(t)
	for i := 0; i < 5; i++ {
		mustCreate(t, s, "f", meta("title", fmt.Sprint("p", i)), "x\n")
	}
	if _, err := s.SetDaily(Author{}, dailyFolder(s, "f")); err != nil {
		t.Fatal(err)
	}
	was := mustDaily(t, s, "S", "2026-11-23", 0)
	for i := 0; i < 20; i++ {
		mustCreate(t, s, "f", meta("title", fmt.Sprint("n", i)), "x\n")
	}
	if got := mustDaily(t, s, "S", "2026-11-23", 0); got.Page.ID != was.Page.ID {
		t.Fatal("the pick changed when pages were added")
	}
	// Another source is another memory.
	if _, err := s.SetDaily(Author{}, dailyFolder(s, "f", ViewFilter{Key: "k", Op: "notEmpty"})); err != nil {
		t.Fatal(err)
	}
	if _, none, err := s.DailyText("S", "2026-11-23", 0); err != nil || none != DailyNoMatch {
		t.Fatalf("%v %v", none, err)
	}
	if _, err := s.SetDaily(Author{}, dailyFolder(s, "f")); err != nil {
		t.Fatal(err)
	}
	if got := mustDaily(t, s, "S", "2026-11-23", 0); got.Page.ID != was.Page.ID {
		t.Fatal("the pick was forgotten with the source")
	}
	// The page is gone: another is picked, and that one stays.
	if err := s.Delete(Author{}, was.Page.ID); err != nil {
		t.Fatal(err)
	}
	after := mustDaily(t, s, "S", "2026-11-23", 0)
	if after.Page.ID == was.Page.ID || after.Count != 24 {
		t.Fatalf("%+v", after)
	}
	if again := mustDaily(t, s, "S", "2026-11-23", 0); again.Page.ID != after.Page.ID {
		t.Fatal("the new pick did not stay")
	}
	// A page removed outside, before the index knows: still a page is answered.
	p := mustDaily(t, s, "S", "2026-11-24", 0)
	if err := os.Remove(filepath.Join(s.Root(), filepath.FromSlash(p.Page.Path))); err != nil {
		t.Fatal(err)
	}
	if got := mustDaily(t, s, "S", "2026-11-24", 0); got.Page.ID == p.Page.ID {
		t.Fatal("a page that is not there was answered")
	}
}

func TestDailyBodyIsCutAt2000Characters(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "f", meta("title", "long"), strings.Repeat("字", 2001))
	if _, err := s.SetDaily(Author{}, dailyFolder(s, "f")); err != nil {
		t.Fatal(err)
	}
	p := mustDaily(t, s, "S", "2026-11-23", 0)
	if !p.Truncated || len([]rune(p.Body)) != DailyBodyLimit {
		t.Fatalf("%v %d", p.Truncated, len([]rune(p.Body)))
	}
}

func TestDailyFollowsAFolderMoveAndItsRecovery(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	mustCreate(t, s, "A/sub", meta("title", "P"), "x\n")
	if _, err := s.SetDaily(Author{}, dailyFolder(s, "A/sub")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveFolder(Author{}, "A", "B"); err != nil {
		t.Fatal(err)
	}
	if d := s.Daily(); d == nil || *d.Folder != "B/sub" {
		t.Fatalf("%+v\n%s", d, spaceYML(root))
	}
	if got := mustDaily(t, s, "S", "2026-11-23", 0); got.Page.Folder != "B/sub" {
		t.Fatalf("%+v", got.Page)
	}
	// A folder with a similar name is not the folder.
	mustCreate(t, s, "Bx", meta("title", "Q"), "x\n")
	if _, err := s.MoveFolder(Author{}, "Bx", "By"); err != nil {
		t.Fatal(err)
	}
	if d := s.Daily(); *d.Folder != "B/sub" {
		t.Fatalf("%+v", d)
	}
	// A crash after the rename, before space.yml was written.
	s.Flush()
	crash(s)
	if err := os.Rename(filepath.Join(root, "B"), filepath.Join(root, "C")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalFile(root), []byte(`{"op":"b","f":"B","to":"C"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := openAt(t, root)
	if d := s2.Daily(); d == nil || *d.Folder != "C/sub" {
		t.Fatalf("after recovery: %+v\n%s", d, spaceYML(root))
	}
}

func TestDailyTypeCountsTheFoldersBoundToIt(t *testing.T) {
	s := open(t)
	if _, err := putCurrent(s, "t", TypeInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := putCurrent(s, "u", TypeInput{}); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "A/deep", meta("title", "in A"), "x\n")
	mustCreate(t, s, "A/other", meta("title", "in other"), "x\n")
	mustCreate(t, s, "A/other", meta("title", "own", "type", "t"), "x\n")
	mustCreate(t, s, "Z", meta("title", "own header", "type", "t"), "x\n")
	mustCreate(t, s, "Z", meta("title", "untyped"), "x\n")
	for f, typ := range map[string]string{"A": "t", "A/other": "u"} {
		if _, err := s.BindFolder(Author{}, f, typ); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetDaily(Author{}, &DailySource{Type: "t"}); err != nil {
		t.Fatal(err)
	}
	// "in A" (the folder's), "own" (the header's, though its folder is bound to u)
	// and "own header": not "in other" (its folder says u), not "untyped".
	titles := map[string]bool{}
	for k := 0; k < 3; k++ {
		p := mustDaily(t, s, "S", "2026-11-23", k)
		if p.Count != 3 {
			t.Fatalf("%d", p.Count)
		}
		titles[p.Page.Title] = true
	}
	if len(titles) != 3 || !titles["in A"] || !titles["own"] || !titles["own header"] {
		t.Fatalf("%v", titles)
	}
	// The root bound to a type makes everything unbound that type's.
	if _, err := s.BindFolder(Author{}, "", "u"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDaily(Author{}, &DailySource{Type: "u"}); err != nil {
		t.Fatal(err)
	}
	if p := mustDaily(t, s, "S", "2026-11-23", 0); p.Count != 2 {
		t.Fatalf("%d", p.Count) // "in other" and "untyped"
	}
}

func TestDailySourceInTheSettingsFile(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	mustCreate(t, s, "f", meta("title", "P", "tags", []any{"诗"}), "x\n")
	if got := s.Daily(); got != nil {
		t.Fatalf("%+v", got)
	}
	// A source made by hand, with an alias, a comment and a key of its own.
	hand := "name: x\nshared: &src\n  tag: 诗\ndaily: *src\nnotes: keep\n"
	if err := os.MkdirAll(filepath.Join(root, ".petalbook"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".petalbook", "space.yml"), []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := s.Daily(); d == nil || d.Tag != "诗" {
		t.Fatalf("an alias is read as what it stands for: %+v", d)
	}
	// Changing it does not change what the anchor means.
	if _, err := s.SetDaily(Author{}, dailyFolder(s, "f")); err != nil {
		t.Fatal(err)
	}
	y := spaceYML(root)
	if !strings.Contains(y, "shared:") || !strings.Contains(y, "tag: 诗") || !strings.Contains(y, "folder: f") || !strings.Contains(y, "notes: keep") {
		t.Fatalf("%s", y)
	}
	if d := s.Daily(); d == nil || d.Folder == nil || *d.Folder != "f" || d.Tag != "" {
		t.Fatalf("%+v\n%s", d, y)
	}
	// Written by hand with more than one: the first of folder, tag, type counts.
	os.WriteFile(filepath.Join(root, ".petalbook", "space.yml"), []byte("daily:\n  type: t\n  tag: 诗\n"), 0o644)
	if d := s.Daily(); d == nil || d.Tag != "诗" {
		t.Fatalf("%+v", d)
	}
	// A value that is not settings reads as none and is written over.
	os.WriteFile(filepath.Join(root, ".petalbook", "space.yml"), []byte("daily: yes\n"), 0o644)
	if d := s.Daily(); d != nil {
		t.Fatalf("%+v", d)
	}
	if _, err := s.SetDaily(Author{}, dailyFolder(s, "f")); err != nil {
		t.Fatal(err)
	}
	if d := s.Daily(); d == nil || *d.Folder != "f" {
		t.Fatalf("%+v", d)
	}
	// A file that cannot be used: the settings of the last version that could be.
	s.Flush()
	os.WriteFile(filepath.Join(root, ".petalbook", "space.yml"), []byte("daily: [unclosed\nfolders: {{\n"), 0o644)
	if d := s.Daily(); d == nil || *d.Folder != "f" {
		t.Fatalf("restored from the history: %+v", d)
	}
	if _, err := s.SetDaily(Author{}, nil); err != nil {
		t.Fatal(err)
	}
	if s.Daily() != nil || strings.Contains(spaceYML(root), "daily") {
		t.Fatalf("%s", spaceYML(root))
	}
	if m, _ := filepath.Glob(filepath.Join(root, ".petalbook", "space.yml.unreadable-*")); len(m) != 2 {
		// One of the `daily: yes` that was written over, one of the file that could not be used.
		t.Fatalf("the file as it was is kept: %v", m)
	}
}

func TestDailyIsRefusedWhenNotMade(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "f", meta("title", "P"), "x\n")
	tooMany := make([]ViewFilter, index.MaxViewFilters+1)
	for i := range tooMany {
		tooMany[i] = ViewFilter{Key: fmt.Sprint("k", i), Op: "notEmpty"}
	}
	f := "f"
	nowhere := "nowhere"
	for name, d := range map[string]DailySource{
		"none":         {},
		"two":          {Folder: &f, Tag: "t"},
		"no folder":    {Folder: &nowhere},
		"a bad tag":    {Tag: "a\nb"},
		"no type":      {Type: "ghost"},
		"a bad filter": {Folder: &f, Filter: []ViewFilter{{Key: "", Op: "in"}}},
		"too many":     {Folder: &f, Filter: tooMany},
	} {
		if _, err := s.SetDaily(Author{}, &d); err == nil {
			t.Errorf("%s is accepted", name)
		}
	}
	if s.Daily() != nil {
		t.Fatal("a refused source was written")
	}
	if p, none, err := s.DailyText("S", "2026-11-23", 0); err != nil || p != nil || none != DailyNoSource {
		t.Fatalf("%v %v %v", p, none, err)
	}
}
