package space

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/index"
)

func dailyFolder(s *Space, folder string, filter ...ViewFilter) *DailyConfig {
	return &DailyConfig{All: &DailySource{Folder: &folder, Filter: filter}}
}

func mustDaily(t *testing.T, s *Space, spaceID, date string, skip int) *DailyPick {
	t.Helper()
	p, none, err := s.DailyText(spaceID, date, skip, "")
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
		if _, _, err := s.DailyText("S", c.date, c.skip, ""); err == nil {
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
	if _, none, err := s.DailyText("S", "2026-11-23", 0, ""); err != nil || none != DailyNoMatch {
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

// Readers asking for many other texts, or for many other days, never push
// today's shared pick out of the memory; skip has a sane bound.
func TestDailyTodaysPickOutlastsOtherAsks(t *testing.T) {
	s := open(t)
	for i := 0; i < 5; i++ {
		mustCreate(t, s, "f", meta("title", fmt.Sprint("p", i)), "x\n")
	}
	if _, err := s.SetDaily(Author{}, dailyFolder(s, "f")); err != nil {
		t.Fatal(err)
	}
	today := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	s.dailyMu.Lock()
	s.daily.today = func() time.Time { return today }
	s.dailyMu.Unlock()
	was := mustDaily(t, s, "S", "2026-11-23", 0)
	for skip := 1; skip <= maxDailySkip; skip++ {
		mustDaily(t, s, "S", "2026-11-23", skip)
	}
	for d := 0; d < 3*dailyMemoMax; d++ {
		mustDaily(t, s, "S", today.AddDate(0, 0, -2-d).Format("2006-01-02"), 0)
	}
	s.dailyMu.Lock()
	kept := false
	for k, e := range s.daily.shared {
		if k.date == "2026-11-23" && k.skip == 0 && e.id == was.Page.ID {
			kept = true
		}
	}
	sizes := []int{len(s.daily.shared), len(s.daily.other)}
	s.dailyMu.Unlock()
	if !kept || sizes[0] > dailyMemoMax || sizes[1] > dailyMemoMax {
		t.Fatalf("today's pick kept: %v; sizes %v", kept, sizes)
	}
	if _, _, err := s.DailyText("S", "2026-11-23", maxDailySkip+1, ""); err == nil {
		t.Fatal("a skip beyond the bound is taken")
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
	if d := s.Daily(); d == nil || *d.All.Folder != "B/sub" {
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
	if d := s.Daily(); *d.All.Folder != "B/sub" {
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
	if d := s2.Daily(); d == nil || *d.All.Folder != "C/sub" {
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
	if _, err := s.SetDaily(Author{}, &DailyConfig{All: &DailySource{Type: "t"}}); err != nil {
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
	if _, err := s.SetDaily(Author{}, &DailyConfig{All: &DailySource{Type: "u"}}); err != nil {
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
	if d := s.Daily(); d == nil || d.All == nil || d.All.Tag != "诗" {
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
	if d := s.Daily(); d == nil || d.All == nil || d.All.Folder == nil || *d.All.Folder != "f" || d.All.Tag != "" {
		t.Fatalf("%+v\n%s", d, y)
	}
	// Written by hand with more than one: the first of folder, tag, type counts.
	os.WriteFile(filepath.Join(root, ".petalbook", "space.yml"), []byte("daily:\n  type: t\n  tag: 诗\n"), 0o644)
	if d := s.Daily(); d == nil || d.All == nil || d.All.Tag != "诗" {
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
	if d := s.Daily(); d == nil || *d.All.Folder != "f" {
		t.Fatalf("%+v", d)
	}
	// A file that cannot be used: the settings of the last version that could be.
	s.Flush()
	os.WriteFile(filepath.Join(root, ".petalbook", "space.yml"), []byte("daily: [unclosed\nfolders: {{\n"), 0o644)
	if d := s.Daily(); d == nil || *d.All.Folder != "f" {
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
		if _, err := s.SetDaily(Author{}, &DailyConfig{All: &d}); err == nil {
			t.Errorf("%s is accepted", name)
		}
	}
	if s.Daily() != nil {
		t.Fatal("a refused source was written")
	}
	if p, none, err := s.DailyText("S", "2026-11-23", 0, ""); err != nil || p != nil || none != DailyNoSource {
		t.Fatalf("%v %v %v", p, none, err)
	}
}

func TestDailyConfigByLanguage(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	mustCreate(t, s, "文案/a", meta("title", "中文一"), "中\n")
	mustCreate(t, s, "quotes", meta("title", "English one", "tags", []any{"q"}), "en\n")
	zh, en := "文案", "quotes"
	cfg := &DailyConfig{ByLang: map[string]DailySource{"zh": {Folder: &zh}, "EN": {Folder: &en}}}
	got, err := s.SetDaily(Author{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ByLang) != 2 || got.ByLang["en"].Folder == nil || got.All != nil {
		t.Fatalf("%+v", got)
	}
	y := spaceYML(root)
	if !strings.Contains(y, "daily:") || !strings.Contains(y, "zh-Hans:") || !strings.Contains(y, "en:") || !strings.Contains(y, "folder: 文案") {
		t.Fatalf("%s", y)
	}
	// Each language is picked from its own source; a region follows its language.
	for lang, title := range map[string]string{"zh": "中文一", "zh-CN": "中文一", "en": "English one", "en-GB": "English one"} {
		if p := mustDailyLang(t, s, lang); p.Page.Title != title {
			t.Errorf("%s: %s", lang, p.Page.Title)
		}
	}
	// A language nobody wrote a source for gets one of the sources, always the same.
	a, b := mustDailyLang(t, s, "fr"), mustDailyLang(t, s, "de")
	if a.Page.Title != b.Page.Title || a.Page.Title != "English one" {
		t.Errorf("%s %s", a.Page.Title, b.Page.Title)
	}
	if p := mustDailyLang(t, s, ""); p.Page.Title != "English one" {
		t.Errorf("no language: %s", p.Page.Title)
	}
	// A folder moved takes the sources of every language with it.
	if _, err := s.MoveFolder(Author{}, "文案", "诗"); err != nil {
		t.Fatal(err)
	}
	if d := s.Daily(); d == nil || *d.ByLang["zh-Hans"].Folder != "诗" || *d.ByLang["en"].Folder != "quotes" {
		t.Fatalf("%+v\n%s", d, spaceYML(root))
	}
	// Dropping one language keeps the other; the same everywhere replaces them all.
	if _, err := s.SetDaily(Author{}, &DailyConfig{ByLang: map[string]DailySource{"en": {Folder: &en}}}); err != nil {
		t.Fatal(err)
	}
	if d := s.Daily(); len(d.ByLang) != 1 || strings.Contains(spaceYML(root), "zh-Hans:") {
		t.Fatalf("%+v\n%s", d, spaceYML(root))
	}
	if _, err := s.SetDaily(Author{}, &DailyConfig{All: &DailySource{Tag: "q"}}); err != nil {
		t.Fatal(err)
	}
	if d := s.Daily(); d.All == nil || d.All.Tag != "q" || len(d.ByLang) != 0 || strings.Contains(spaceYML(root), "en:") {
		t.Fatalf("%+v\n%s", d, spaceYML(root))
	}
	// ... and back.
	if _, err := s.SetDaily(Author{}, &DailyConfig{ByLang: map[string]DailySource{"zh": {Tag: "q"}}}); err != nil {
		t.Fatal(err)
	}
	if d := s.Daily(); d.All != nil || d.ByLang["zh-Hans"].Tag != "q" || strings.Contains(spaceYML(root), "\n  tag: q") {
		t.Fatalf("%+v\n%s", d, spaceYML(root))
	}
	// "All languages" and some languages together: the first is the fallback.
	root2 := t.TempDir()
	s2 := openAt(t, root2)
	mustCreate(t, s2, "zh", meta("title", "中文一"), "中\n")
	mustCreate(t, s2, "any", meta("title", "For all"), "all\n")
	zh2, any2 := "zh", "any"
	if _, err := s2.SetDaily(Author{}, &DailyConfig{All: &DailySource{Folder: &any2}, ByLang: map[string]DailySource{"zh": {Folder: &zh2}}}); err != nil {
		t.Fatal(err)
	}
	y2 := spaceYML(root2)
	if !strings.Contains(y2, "folder: any") || !strings.Contains(y2, "zh-Hans:") {
		t.Fatalf("%s", y2)
	}
	d2 := s2.Daily()
	if d2.All == nil || *d2.All.Folder != "any" || *d2.ByLang["zh-Hans"].Folder != "zh" {
		t.Fatalf("%+v", d2)
	}
	for lang, title := range map[string]string{"zh": "中文一", "zh-TW": "中文一", "en": "For all", "": "For all"} {
		if p := mustDailyLang(t, s2, lang); p.Page.Title != title {
			t.Errorf("%q: %s", lang, p.Page.Title)
		}
	}
	// A move carries both.
	if _, err := s2.MoveFolder(Author{}, "zh", "chinese"); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.MoveFolder(Author{}, "any", "every"); err != nil {
		t.Fatal(err)
	}
	if d := s2.Daily(); *d.All.Folder != "every" || *d.ByLang["zh-Hans"].Folder != "chinese" {
		t.Fatalf("%+v\n%s", d, spaceYML(root2))
	}
	// Taking the fallback away leaves the languages, and the other way round.
	if _, err := s2.SetDaily(Author{}, &DailyConfig{ByLang: map[string]DailySource{"zh": {Tag: "t"}}}); err != nil {
		t.Fatal(err)
	}
	if d := s2.Daily(); d.All != nil || strings.Contains(spaceYML(root2), "folder: every") {
		t.Fatalf("%+v\n%s", d, spaceYML(root2))
	}
	for name, c := range map[string]DailyConfig{
		"a bad code": {ByLang: map[string]DailySource{"chinese!": {Tag: "q"}}},
		"a bad one":  {ByLang: map[string]DailySource{"en": {}}},
		"none":       {},
	} {
		if _, err := s.SetDaily(Author{}, &c); err == nil {
			t.Errorf("%s is accepted", name)
		}
	}
}

func mustDailyLang(t *testing.T, s *Space, lang string) *DailyPick {
	t.Helper()
	p, none, err := s.DailyText("S", "2026-11-23", 0, lang)
	if err != nil || p == nil {
		t.Fatalf("daily %q: %v %v", lang, none, err)
	}
	return p
}

func TestDailyConfigJSON(t *testing.T) {
	var c DailyConfig
	for in, ok := range map[string]bool{
		`{"folder":"a"}`:                         true,
		`{"tag":"a","filter":[]}`:                true,
		`{"zh":{"folder":"a"},"en":{"tag":"b"}}`: true,
		`{"zh-CN":{"type":"t"}}`:                 true,
		`{}`:                                     false,
		`{"zh":{"folder":"a"},"ZH":{"tag":"b"}}`: false,
		`{"folder":"a","tag":"b"}`:               false,
		`{"zh":{}}`:                              false,
		`{"toolongcode":{"tag":"b"}}`:            false,
		`{"zh":"folder"}`:                        false,
		`[]`:                                     false,
	} {
		c = DailyConfig{}
		if err := c.UnmarshalJSON([]byte(in)); (err == nil) != ok {
			t.Errorf("%s: %v", in, err)
		}
	}
}

func TestDailyLanguagesAreBCP47Tags(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	mustCreate(t, s, "hans", meta("title", "Simplified"), "x\n")
	mustCreate(t, s, "hant", meta("title", "Traditional"), "x\n")
	mustCreate(t, s, "en", meta("title", "English"), "x\n")
	mustCreate(t, s, "any", meta("title", "Any"), "x\n")
	// Sources saved before the rule: `zh` is read as zh-Hans, `pt-br` as pt-BR.
	os.WriteFile(filepath.Join(root, ".petalbook", "space.yml"), []byte("daily:\n  folder: any\n  zh:\n    folder: hans\n  zh-hant:\n    folder: hant\n  EN:\n    folder: en\n  pt-br:\n    folder: en\n"), 0o644)
	d := s.Daily()
	if d == nil || len(d.ByLang) != 4 || d.ByLang["zh-Hans"].Folder == nil || d.ByLang["zh-Hant"].Folder == nil || d.ByLang["en"].Folder == nil || d.ByLang["pt-BR"].Folder == nil {
		t.Fatalf("%+v", d)
	}
	// A reader's language is matched the BCP 47 way.
	for lang, title := range map[string]string{
		"zh-CN": "Simplified", "zh-SG": "Simplified", "zh": "Simplified", "zh-Hans-CN": "Simplified",
		"zh-TW": "Traditional", "zh-HK": "Traditional", "zh-Hant": "Traditional",
		"en-GB": "English", "en-US": "English", "fr": "Any", "": "Any",
	} {
		if p := mustDailyLang(t, s, lang); p.Page.Title != title {
			t.Errorf("%q: %s, want %s", lang, p.Page.Title, title)
		}
	}
	// The next save writes the tags back in canonical form.
	hans := "hans"
	cfg := *d
	cfg.ByLang = map[string]DailySource{"zh-Hans": {Folder: &hans}}
	if _, err := s.SetDaily(Author{}, &cfg); err != nil {
		t.Fatal(err)
	}
	y := spaceYML(root)
	if !strings.Contains(y, "zh-Hans:") || strings.Contains(y, "\n  zh:") || strings.Contains(y, "pt-br") || strings.Contains(y, "EN:") {
		t.Fatalf("%s", y)
	}
	// What is not a tag is refused with its code.
	for _, bad := range []string{"cn", "english", "zh_CN", "x-foo", "und"} {
		_, err := s.SetDaily(Author{}, &DailyConfig{ByLang: map[string]DailySource{bad: {Folder: &hans}}})
		var inv *InvalidError
		if !errors.As(err, &inv) || inv.Code != CodeBadLanguage {
			t.Errorf("%s: %v", bad, err)
		}
	}
	var c DailyConfig
	if err := json.Unmarshal([]byte(`{"zh_CN": {"folder": "x"}}`), &c); err == nil {
		t.Error("a bad tag is read from JSON")
	} else if inv := new(InvalidError); !errors.As(err, &inv) || inv.Code != CodeBadLanguage {
		t.Errorf("%v", err)
	}
	if err := json.Unmarshal([]byte(`{"zh": {"folder": "x"}, "zh-hans": {"folder": "y"}}`), &c); err == nil {
		t.Error("two spellings of one language")
	}
}

// A settings file with two spellings of one language (`zh:` and `zh-Hans:`)
// is written back with one key: the entry under the canonical key stays, the
// other spelling goes.
func TestDailyTwoSpellingsOfALanguageAreMergedOnSave(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	mustCreate(t, s, "hans", meta("title", "Simplified"), "x\n")
	mustCreate(t, s, "old", meta("title", "Old"), "x\n")
	mustCreate(t, s, "en", meta("title", "English"), "x\n")
	os.WriteFile(filepath.Join(root, ".petalbook", "space.yml"), []byte("daily:\n  zh:\n    folder: old\n  zh-Hans:\n    folder: hans\n"), 0o644)
	d := s.Daily()
	if d == nil || *d.ByLang["zh-Hans"].Folder != "hans" {
		t.Fatalf("%+v", d)
	}
	en := "en"
	cfg := *d
	cfg.ByLang = map[string]DailySource{"zh-Hans": d.ByLang["zh-Hans"], "en": {Folder: &en}}
	if _, err := s.SetDaily(Author{}, &cfg); err != nil {
		t.Fatal(err)
	}
	y := spaceYML(root)
	if strings.Count(y, "zh-Hans:") != 1 || strings.Contains(y, "\n  zh:") || strings.Contains(y, "folder: old") || !strings.Contains(y, "folder: hans") {
		t.Fatalf("%s", y)
	}
	if d := s.Daily(); len(d.ByLang) != 2 || *d.ByLang["zh-Hans"].Folder != "hans" {
		t.Fatalf("%+v", d)
	}
}

// Stored settings are read leniently: what can be is made canonical, what
// cannot is dropped and named, and the rest is kept.
func TestReadStoredDailyConfig(t *testing.T) {
	c, dropped := ReadStoredDailyConfig([]byte(`{"folder":"any","cn":{"folder":"x"},"zh":{"tag":"a"},"zh-Hans":{"tag":"b"},"pt-br":{"tag":"c"},"en":{"folder":"e","tag":"f"}}`))
	if c == nil || c.All == nil || *c.All.Folder != "any" || len(c.ByLang) != 2 || c.ByLang["zh-Hans"].Tag != "b" || c.ByLang["pt-BR"].Tag != "c" {
		t.Fatalf("%+v", c)
	}
	sort.Strings(dropped)
	if fmt.Sprint(dropped) != "[cn en zh]" {
		t.Fatalf("%v", dropped)
	}
	if c, dropped := ReadStoredDailyConfig([]byte(`{"cn":{"folder":"x"}}`)); c != nil || fmt.Sprint(dropped) != "[cn]" {
		t.Fatalf("%+v %v", c, dropped)
	}
}

// A pick says who wrote the page and where it is from, only when the page does.
func TestDailyPickCarriesTheAuthorAndSourceOfAPage(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "有", meta("title", "山花子", "author", "纳兰性德", "source", "纳兰词"), "人到情多情转薄\n")
	mustCreate(t, s, "没有", meta("title", "无名"), "只有正文\n")
	mustCreate(t, s, "中文", meta("title", "t", "作者", "苏轼", "出处", "东坡集"), "x\n")
	folder := func(name string) DailySource { f := name; return DailySource{Folder: &f} }
	p, _, err := s.DailyFrom("S", "2026-11-23", 0, folder("有"))
	if err != nil || p == nil || p.Author != "纳兰性德" || p.Page.Source != "纳兰词" {
		t.Fatalf("%+v %v", p, err)
	}
	p, _, err = s.DailyFrom("S", "2026-11-23", 0, folder("没有"))
	if err != nil || p == nil || p.Author != "" || p.Page.Source != "" {
		t.Fatalf("%+v %v", p, err)
	}
	p, _, err = s.DailyFrom("S", "2026-11-23", 0, folder("中文"))
	if err != nil || p == nil || p.Author != "苏轼" || p.Page.Source != "东坡集" {
		t.Fatalf("%+v %v", p, err)
	}
}
