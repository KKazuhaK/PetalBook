package space

import (
	"strings"
	"testing"
)

func TestChangedViaTrailerAndSeparateCommits(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openTwoAuthors(t, root)
	s.Flush()
	viaTok := Author{Name: ann.Name, Email: ann.Email, Via: ViaToken}
	a, err := s.Create(ann, CreateInput{Meta: meta("title", "Typed"), Body: "a\n"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(viaTok, CreateInput{Meta: meta("title", "Scripted"), Body: "b\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// Same person, different means: two commits, only the second has the trailer.
	subjects, err := gitTry(root, "log", "--format=@%B", "-n", "2")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(subjects, "@")[1:]
	if len(parts) != 2 || !strings.Contains(parts[0], "Add Scripted") || !strings.Contains(parts[0], "\n\nChanged-via: API token") ||
		strings.Contains(parts[1], "Changed-via") || !strings.Contains(parts[1], "Add Typed") {
		t.Fatalf("%q", parts)
	}
	// The history says how each version was made.
	hb, _, err := s.History(t.Context(), b.ID, 10, "")
	if err != nil || len(hb) != 1 || hb[0].Via != "token" {
		t.Fatalf("%+v %v", hb, err)
	}
	ha, _, err := s.History(t.Context(), a.ID, 10, "")
	if err != nil || len(ha) != 1 || ha[0].Via != "" {
		t.Fatalf("%+v %v", ha, err)
	}
}

func TestViaSurvivesTheJournal(t *testing.T) {
	t.Parallel()
	r := jrec{Op: "x", Tx: 1, AuthorName: "A", AuthorEmail: "a@b", Via: ViaToken}
	if got := r.author(); got != (Author{Name: "A", Email: "a@b", Via: ViaToken}) {
		t.Fatalf("%+v", got)
	}
}
