package space

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

func checkKinds(t *testing.T, s *Space) map[string]int {
	t.Helper()
	counts, _, _, err := s.Checks("", "")
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

func TestFixNoIDGivesTheHeaderAnIdAndNothingElseChanges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := openAt(t, root)
	files := map[string]string{
		"a.md": "---\n# a comment\ntitle: A\ntags: [x, y]\n---\nbody\r\nmore\n",
		"b.md": "---\nid: nope\ntitle: B\n---\nb\n",
		"c.md": "---\n---\nonly a header\n",
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(root, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	if c := checkKinds(t, s); c["no_id"] != 3 {
		t.Fatalf("counts %v", c)
	}
	for _, n := range []string{"a.md", "b.md", "c.md"} {
		data, _ := os.ReadFile(filepath.Join(root, n))
		p, err := s.FixNoID(Author{}, n, page.Rev(data))
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if !page.ValidID(p.ID) || p.Path != n {
			t.Fatalf("%s: %+v", n, p)
		}
		after, _ := os.ReadFile(filepath.Join(root, n))
		doc := page.Parse(after)
		if doc.Meta().ID() != p.ID || len(doc.Problems()) != 0 {
			t.Errorf("%s: %q", n, after)
		}
		// The body is as it was (line ends are the file's to keep as LF, as for any save).
		if before := page.Parse([]byte(files[n])); doc.Body != before.Body {
			t.Errorf("%s: body %q was %q", n, doc.Body, before.Body)
		}
		if n == "a.md" {
			if !strings.Contains(string(after), "# a comment\ntitle: A\ntags: [x, y]\n") {
				t.Errorf("the header's own text changed: %q", after)
			}
			if first := strings.SplitN(string(after), "\n", 3)[1]; first != "id: "+p.ID {
				t.Errorf("the id is not first: %q", first)
			}
		}
		// A second fix on the file as it was is stale; on the file as it is, nothing changes.
		if _, err := s.FixNoID(Author{}, n, page.Rev(data)); err == nil {
			t.Errorf("%s: a stale rev was accepted", n)
		} else {
			var ce *ConflictError
			if !errors.As(err, &ce) {
				t.Errorf("%s: %v", n, err)
			}
		}
		again, err := s.FixNoID(Author{}, n, page.Rev(after))
		if err != nil || again.ID != p.ID {
			t.Errorf("%s: fixing a page that has an id: %v %v", n, again, err)
		}
		if now, _ := os.ReadFile(filepath.Join(root, n)); string(now) != string(after) {
			t.Errorf("%s: written again", n)
		}
	}
	if c := checkKinds(t, s); c["no_id"] != 0 {
		t.Fatalf("counts after %v", c)
	}
	// A header that cannot be read, a name that is not a page, a path out of the space.
	os.WriteFile(filepath.Join(root, "bad.md"), []byte("no header at all\n"), 0o644)
	s.Reindex()
	var fe *FixError
	if _, err := s.FixNoID(Author{}, "bad.md", page.Rev([]byte("no header at all\n"))); !errors.As(err, &fe) || fe.Code != "not_fixable" {
		t.Errorf("a file without a header: %v", err)
	}
	if _, err := s.FixNoID(Author{}, "gone.md", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a file that is not there: %v", err)
	}
	if _, err := s.FixNoID(Author{}, "notes.txt", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("not a page: %v", err)
	}
	if _, err := s.FixNoID(Author{}, "../x.md", "x"); err == nil {
		t.Error("a path out of the space")
	}
}

func TestFixNotInTypeRemovesOnlyTheStrayFields(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeType(t, root, "task", `{"title":"Task","properties":{"status":{"type":"string","enum":["todo","done"]}}}`)
	s := openAt(t, root)
	if _, err := s.BindFolder(Author{}, "", "task"); err == nil {
		// the root cannot be bound; bind a folder instead
		t.Log("root bound")
	}
	if _, err := s.CreateFolder(Author{}, "W"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindFolder(Author{}, "W", "task"); err != nil {
		t.Fatal(err)
	}
	p := mustCreate(t, s, "W", meta("title", "T", "status", "todo", "stray", "x", "other", int64(3)), "body\n")
	if c := checkKinds(t, s); c["not_in_type"] != 1 {
		t.Fatalf("counts %v", c)
	}
	before, _ := os.ReadFile(filepath.Join(root, p.Path))
	got, err := s.FixNotInType(Author{}, p.ID, p.Rev)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, p.Path))
	want := strings.NewReplacer("stray: x\n", "", "other: 3\n", "").Replace(string(before))
	if string(after) != want || got.Rev == p.Rev {
		t.Errorf("file %q, want %q", after, want)
	}
	if c := checkKinds(t, s); c["not_in_type"] != 0 {
		t.Fatalf("counts after %v", c)
	}
	// Nothing left to remove: the page is returned as it is, nothing is written.
	again, err := s.FixNotInType(Author{}, p.ID, got.Rev)
	if err != nil || again.Rev != got.Rev {
		t.Errorf("a second fix: %v %v", again, err)
	}
	if _, err := s.FixNotInType(Author{}, p.ID, p.Rev); err == nil {
		t.Error("a stale rev was accepted")
	}
}

// TestTypeProblemsFollowTheTypesAcrossARestart: the types change while the
// server is not running (a pull, an editor); the next start finds out.
func TestTypeProblemsFollowTheTypesAcrossARestart(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	writeType(t, root, "task", `{"properties":{"a":{"type":"string"}}}`)
	s := openAt(t, root)
	mustCreate(t, s, "", meta("title", "One", "type", "task", "a", "x", "b", "y"), "x\n")
	if c := checkKinds(t, s); c["not_in_type"] != 1 {
		t.Fatalf("counts %v", c)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	writeType(t, root, "task", `{"properties":{"a":{"type":"string"},"b":{"type":"number"}}}`)
	s2 := openAt(t, root)
	c := checkKinds(t, s2)
	if c["not_in_type"] != 0 || c["type_mismatch"] != 1 {
		t.Fatalf("after the restart: %v", c)
	}
	// And through the API of the space: the type is edited, the problems follow at once.
	info, _, err := s2.Types()
	if err != nil || len(info) != 1 {
		t.Fatal(err, info)
	}
	if _, err := s2.PutType(Author{}, "task", info[0].Rev, TypeInput{Fields: []FieldInput{{Key: "a", Kind: KindText}, {Key: "b", Kind: KindText}}}); err != nil {
		t.Fatal(err)
	}
	if c := checkKinds(t, s2); c["type_mismatch"] != 0 || c["not_in_type"] != 0 {
		t.Fatalf("after PutType: %v", c)
	}
	if err := s2.DeleteType(Author{}, "task", ""); err != nil {
		var tce *TypeConflictError
		if !errors.As(err, &tce) {
			t.Fatal(err)
		}
		if err := s2.DeleteType(Author{}, "task", tce.Current.Rev); err != nil {
			t.Fatal(err)
		}
	}
	if c := checkKinds(t, s2); c["type_mismatch"] != 0 || c["not_in_type"] != 0 {
		t.Fatalf("after DeleteType: %v", c)
	}
}

func TestAnUploadedAttachmentClearsItsMissingProblems(t *testing.T) {
	t.Parallel()
	s := open(t)
	p := mustCreate(t, s, "", meta("title", "Pic"), "![x](/assets/XXXXXXXXXX/a-1234abcd.png)\n")
	_ = p
	counts := checkKinds(t, s)
	if counts["missing_asset"] != 1 {
		t.Fatalf("counts %v", counts)
	}
	data := []byte("not really a png")
	name := AssetName("a.png", data)
	body := fmt.Sprintf("![x](/assets/%s/%s)\n", p.ID, name)
	if _, err := s.Save(Author{}, p.ID, p.Rev, p.Meta, body); err != nil {
		t.Fatal(err)
	}
	if c := checkKinds(t, s); c["missing_asset"] != 1 {
		t.Fatalf("after pointing at a file that is not there: %v", c)
	}
	if _, err := s.PutAsset(Author{}, p.ID, "a.png", data); err != nil {
		t.Fatal(err)
	}
	if c := checkKinds(t, s); c["missing_asset"] != 0 {
		t.Fatalf("after the upload: %v", c)
	}
}
