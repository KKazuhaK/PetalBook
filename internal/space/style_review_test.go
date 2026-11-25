package space

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
)

// --- what an icon and a colour may be ------------------------------------------

func TestAFolderIconMustBeALucideNameOrOneEmoji(t *testing.T) {
	t.Parallel()
	s := open(t)
	mustCreate(t, s, "A", meta("title", "A"), "x\n")
	for _, icon := range []string{"constructor", "__proto__", "toString", "not-an-icon", "\U0001F4DA\U0001F4DA", "<b>", "ab"} {
		if _, err := s.SetFolderStyle(Author{}, "A", icon, ""); err == nil {
			t.Errorf("icon %q was taken", icon)
		}
	}
	for _, icon := range []string{"book-open", "\U0001F4DA", "\U0001F469\u200d\U0001F4BB"} {
		if _, err := s.SetFolderStyle(Author{}, "A", icon, "teal"); err != nil {
			t.Errorf("icon %q: %v", icon, err)
		}
	}
	if _, err := s.SetFolderStyle(Author{}, "A", "star", "constructor"); err == nil {
		t.Error("a colour that is not one of the eight")
	}
}

func TestAPageIconIsCheckedWhenSavedAndIgnoredWhenReadFromAHandEditedFile(t *testing.T) {
	t.Parallel()
	s := open(t)
	for _, m := range []page.Meta{meta("title", "A", "icon", "constructor"), meta("title", "A", "color", "mauve"), meta("title", "A", "icon", 5)} {
		if _, err := s.Create(Author{}, CreateInput{Meta: m, Body: "x\n"}); err == nil {
			t.Errorf("created with %+v", m)
		}
	}
	p := mustCreate(t, s, "", meta("title", "B", "icon", "flower", "color", "pink"), "x\n")
	if _, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "B", "icon", "__proto__"), "x\n"); err == nil {
		t.Error("saved with the icon __proto__")
	}
	// A file written by hand: the listing has no icon and no colour, and a save
	// that leaves them as they are is not refused.
	raw := readDisk(t, s, p.Path)
	raw = strings.Replace(raw, "icon: flower", "icon: constructor", 1)
	raw = strings.Replace(raw, "color: pink", "color: mauve", 1)
	if err := os.WriteFile(filepath.Join(s.Root(), p.Path), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	r, err := s.List(index.Query{})
	if err != nil || len(r.Items) != 1 || r.Items[0].Icon != "" || r.Items[0].Color != "" {
		t.Fatalf("%+v %v", r.Items, err)
	}
	got, err := s.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Meta.Icon() != "" || got.Meta.Color() != "" {
		t.Errorf("read as %q %q", got.Meta.Icon(), got.Meta.Color())
	}
	if _, err := s.Save(Author{}, p.ID, got.Rev, got.Meta, "y\n"); err != nil {
		t.Errorf("a save that leaves the hand-written icon as it is: %v", err)
	}
}

func TestHandWrittenFolderAndTypeIconsThatAreNoneAreIgnored(t *testing.T) {
	t.Parallel()
	s := open(t)
	mustCreate(t, s, "A", meta("title", "A"), "x\n")
	mustCreate(t, s, "B", meta("title", "B"), "x\n")
	yml := "folders:\n  A:\n    icon: constructor\n    color: __proto__\n  B:\n    icon: book-open\n    color: teal\n"
	if err := os.MkdirAll(filepath.Join(s.Root(), ".petalbook"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root(), ".petalbook/space.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	st := s.Styles()
	if len(st) != 1 || st[0].Folder != "B" {
		t.Fatalf("%+v", st)
	}
	writeType(t, s.Root(), "poem", `{"title":"Poem","x-icon":"constructor","x-color":"toString"}`)
	ts, _, err := s.Types()
	if err != nil {
		t.Fatal(err)
	}
	for _, ti := range ts {
		if ti.Name == "poem" && (ti.Icon != "" || ti.Color != "") {
			t.Errorf("%+v", ti)
		}
	}
	bad := "constructor"
	if _, err := s.PutType(Author{}, "verse", "", TypeInput{Title: "Verse", Icon: &bad}); err == nil {
		t.Error("a type with the icon constructor")
	}
}

// --- an empty list of presets is not "the defaults" -------------------------------

func TestEmptySourcePresetsStayEmpty(t *testing.T) {
	t.Parallel()
	s := open(t)
	if err := s.SetSourcePresets(Author{}, []string{}); err != nil {
		t.Fatal(err)
	}
	got, set := s.SourcePresets()
	if !set || got == nil || len(got) != 0 {
		t.Fatalf("%#v %v", got, set)
	}
	if err := s.SetSourcePresets(Author{}, nil); err != nil {
		t.Fatal(err)
	}
	if got, set := s.SourcePresets(); set || got != nil {
		t.Fatalf("unset: %#v %v", got, set)
	}
}
