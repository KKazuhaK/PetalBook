package space

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setPrefs(t *testing.T, s *Space, kv map[string]any) {
	t.Helper()
	err := s.WritePrefs(Author{Name: "Ann", Email: "ann@example.test"}, func(p map[string]any) bool {
		for k, v := range kv {
			if v == nil {
				delete(p, k)
			} else {
				p[k] = v
			}
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPrefsAreReadBackAsTheyWereWritten(t *testing.T) {
	t.Parallel()
	s := localSpace(t)
	if p, err := s.ReadPrefs(); err != nil || len(p) != 0 {
		t.Fatalf("a new space has preferences: %v %v", p, err)
	}
	setPrefs(t, s, map[string]any{
		"pb.table.users": map[string]any{"hidden": []any{"email"}, "widths": map[string]any{"name": 0.3}, "pageSize": float64(100)},
		"onboarding":     map[string]any{"state": "done"},
	})
	p, err := s.ReadPrefs()
	if err != nil {
		t.Fatal(err)
	}
	tbl := p["pb.table.users"].(map[string]any)
	if tbl["pageSize"] != 100 && tbl["pageSize"] != float64(100) || tbl["hidden"].([]any)[0] != "email" || tbl["widths"].(map[string]any)["name"] != 0.3 {
		t.Fatalf("%v", p)
	}
	if p["onboarding"].(map[string]any)["state"] != "done" {
		t.Fatalf("%v", p)
	}
	data, _ := os.ReadFile(filepath.Join(s.root, PrefsFile))
	if !strings.HasPrefix(string(data), "# Preferences of the owner of this space") {
		t.Fatalf("no header comment:\n%s", data)
	}
	// Writing what is there changes nothing (no commit).
	err = s.WritePrefs(Author{}, func(map[string]any) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	// Taking everything away takes the file away.
	setPrefs(t, s, map[string]any{"pb.table.users": nil, "onboarding": nil})
	if _, err := os.Stat(filepath.Join(s.root, PrefsFile)); !os.IsNotExist(err) {
		t.Fatalf("the file stays: %v", err)
	}
}

func TestPrefsAreInQuietCommitsThatNoPageHistoryShows(t *testing.T) {
	t.Parallel()
	s := localSpace(t)
	ctx := context.Background()
	p := mustCreate(t, s, "Tang", meta("title", "暮雪"), "v1\n")
	flushed(t, s)
	stamp := s.SettingsStamp()
	head := s.Head()
	// Ten writes in a burst are one commit: the committer commits after a quiet period.
	for i := 0; i < 10; i++ {
		setPrefs(t, s, map[string]any{"k": float64(i)})
	}
	flushed(t, s)
	log := git(t, s.root, "log", "--format=%s", "--", PrefsFile)
	if lines := strings.Split(log, "\n"); len(lines) != 1 || lines[0] != "Update prefs.yml" {
		t.Fatalf("the commits of the file: %q", log)
	}
	// The page's history is its own commits only.
	items, _, err := s.History(ctx, p.ID, 30, "")
	if err != nil || len(items) != 1 {
		t.Fatalf("history: %v %v", items, err)
	}
	// Nothing for the offline sync: the index did not change, nor the settings stamp.
	if s.Head() != head {
		t.Errorf("the change cursor moved: %s -> %s", head, s.Head())
	}
	if s.SettingsStamp() != stamp {
		t.Errorf("the settings stamp moved")
	}
	if ch, err := s.Changes(head, 50); err != nil || len(ch.Items) != 0 {
		t.Errorf("changes: %v %v", ch, err)
	}
	// It is no page, no attachment and no settings file to read.
	if _, _, err := s.OpenFile(PrefsFile); err == nil {
		t.Error("the preferences can be opened as a file")
	}
	if _, _, err := s.OpenFile(".petalbook/prefs.yml"); err == nil {
		t.Error("the preferences can be opened as a file")
	}
}

func TestPrefsNeverFollowALinkOutOfTheSpace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := openAt(t, root)
	outside := filepath.Join(t.TempDir(), "secret.yml")
	if err := os.WriteFile(outside, []byte("stolen: yes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".petalbook"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, PrefsFile)); err != nil {
		t.Skip("no symbolic links here")
	}
	if p, err := s.ReadPrefs(); err != nil || len(p) != 0 {
		t.Fatalf("read through a link: %v %v", p, err)
	}
	setPrefs(t, s, map[string]any{"k": "v"})
	if b, _ := os.ReadFile(outside); string(b) != "stolen: yes\n" {
		t.Fatalf("the file outside was written: %q", b)
	}
}

func TestPrefsThatCannotBeReadAreEmptyAndAreReplaced(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := openAt(t, root)
	if err := os.MkdirAll(filepath.Join(root, ".petalbook"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, PrefsFile), []byte("{{{ not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, err := s.ReadPrefs(); err != nil || len(p) != 0 {
		t.Fatalf("%v %v", p, err)
	}
	setPrefs(t, s, map[string]any{"k": "v"})
	if p, _ := s.ReadPrefs(); p["k"] != "v" {
		t.Fatalf("%v", p)
	}
	// A file too large is refused, and the one there stays.
	big := strings.Repeat("x", MaxPrefsBytes)
	err := s.WritePrefs(Author{}, func(p map[string]any) bool { p["big"] = big; return true })
	if err == nil {
		t.Fatal("a file over the limit was written")
	}
	if p, _ := s.ReadPrefs(); p["k"] != "v" || p["big"] != nil {
		t.Fatalf("%v", p)
	}
}
