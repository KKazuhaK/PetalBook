package space

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestSideFilesLeftByACrashAfterTheDatabaseWentAreRemovedAtTheNextOpen(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	openAt(t, root).Close()
	state := DefaultStateDir(root)
	old := filepath.Join(root, ".petalbook", "cache")
	os.MkdirAll(old, 0o755)
	// The copies are in the state directory; the main files are gone from the
	// old place, their side files are not.
	os.WriteFile(filepath.Join(state, "state.db"), []byte("drafts"), 0o644)
	os.WriteFile(filepath.Join(state, "index.db"), []byte("index"), 0o644)
	for _, n := range []string{"state.db-wal", "state.db-shm", "index.db-wal", "index.db-shm"} {
		os.WriteFile(filepath.Join(old, n), []byte("x"), 0o644)
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	openAt(t, root, func(o *Options) { o.Logger = log }).Close()
	if _, err := os.Lstat(old); err == nil {
		es, _ := os.ReadDir(old)
		t.Errorf("the old folder is still there: %v", es)
	}
	if buf.Len() > 0 {
		t.Errorf("a warning for files nobody uses:\n%s", buf.String())
	}
	if b, _ := os.ReadFile(filepath.Join(state, "state.db")); string(b) != "drafts" {
		t.Errorf("the copy changed: %q", b)
	}
}

func TestTrackedSideFilesOfAGoneDatabaseAreLeftAlone(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	openAt(t, root).Close()
	state := DefaultStateDir(root)
	old := filepath.Join(root, ".petalbook", "cache")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(state, "state.db"), []byte("drafts"), 0o644)
	os.WriteFile(filepath.Join(old, "state.db-wal"), []byte("mine"), 0o644)
	os.WriteFile(filepath.Join(old, "state.db-shm"), []byte("x"), 0o644)
	gitO(t, root, "add", "-f", ".petalbook/cache/state.db-wal")
	gitO(t, root, "commit", "-qm", "side file")
	openAt(t, root).Close()
	if b, err := os.ReadFile(filepath.Join(old, "state.db-wal")); err != nil || string(b) != "mine" {
		t.Errorf("the tracked file: %q %v", b, err)
	}
	if _, err := os.Lstat(filepath.Join(old, "state.db-shm")); err == nil {
		t.Error("the untracked side file was left")
	}
	if st := git(t, root, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("a change nobody made:\n%s", st)
	}
}
