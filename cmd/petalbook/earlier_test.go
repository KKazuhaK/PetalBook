package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	dst.Close()
}

// An earlier version ran after this one (a downgrade, then an upgrade): its
// drafts, in .petalbook/cache/state.db, are not lost. One the current
// database lacks is imported; one that differs from the current draft is
// kept apart, held, where petalbook draft list finds it.
func TestDraftsAnEarlierVersionLeftBesideTheCurrentStateAreNotLost(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	dir := filepath.Join(tempDir(t), "notes")
	cfg := serveConfig{space: dir, committer: "T <t@example.test>", commitIdle: time.Hour, commitMax: time.Hour, draftIdle: time.Hour, draftMax: 2 * time.Hour}
	srv := start(t, cfg)
	b := &browser{t: t, base: srv.base}
	_, a := b.do("POST", "/api/spaces/default/pages", map[string]any{"meta": map[string]any{"title": "A"}, "body": "one\n"})
	_, bp := b.do("POST", "/api/spaces/default/pages", map[string]any{"meta": map[string]any{"title": "B"}, "body": "one\n"})
	aID, bID := a["id"].(string), bp["id"].(string)
	if code, r := b.do("PUT", "/api/spaces/default/pages/"+aID+"/draft", map[string]any{"baseRev": a["rev"], "meta": map[string]any{"title": "A"}, "body": "current\n"}); code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	if err := srv.stop(); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, ".git", "petalbook-state")

	// What the earlier version had: the same database, then its own drafts.
	old := filepath.Join(dir, ".petalbook", "cache")
	os.MkdirAll(old, 0o755)
	copyFile(t, filepath.Join(state, "state.db"), filepath.Join(old, "state.db"))
	ost, err := store.Open(ctx, filepath.Join(old, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	cur, err := store.GetDraft(ctx, ost.Q(), "default", aID, "local")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := store.PutDraft(ctx, ost.Q(), store.Draft{SpaceID: "default", PageID: aID, UserID: "local", BaseRev: cur.BaseRev, Meta: cur.Meta, Body: "earlier\n", Updated: now}, cur.Version, time.Hour, 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutDraft(ctx, ost.Q(), store.Draft{SpaceID: "default", PageID: bID, UserID: "local", BaseRev: bp["rev"].(string), Meta: `{"title":"B"}`, Body: "only earlier\n", Updated: now}, "", time.Hour, 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	ost.Close()

	srv = start(t, cfg)
	b = &browser{t: t, base: srv.base}
	if _, g := b.do("GET", "/api/spaces/default/pages/"+bID, nil); g["draft"] == nil || g["draft"].(map[string]any)["body"] != "only earlier\n" {
		t.Errorf("the earlier draft of B was not imported: %v", g["draft"])
	}
	if _, g := b.do("GET", "/api/spaces/default/pages/"+aID, nil); g["draft"] == nil || g["draft"].(map[string]any)["body"] != "current\n" {
		t.Errorf("the current draft of A was replaced: %v", g["draft"])
	}
	if !strings.Contains(srv.log.String(), "kept apart") {
		t.Errorf("no warning:\n%s", srv.log)
	}
	if err := srv.stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(old, "state.db")); err == nil {
		t.Error("the earlier database is still in .petalbook/cache")
	}
	kept, _ := filepath.Glob(filepath.Join(state, "state-earlier-*.db"))
	if len(kept) != 1 {
		t.Fatalf("the earlier database kept: %v", kept)
	}
	out, err := runIn(t, "", "draft", "list", "--space", dir, "--database", kept[0])
	if err != nil || !strings.Contains(out, aID) || strings.Contains(out, bID) {
		t.Errorf("draft list of the earlier database: %v\n%s", err, out)
	}
	out, err = runIn(t, "", "draft", "export", "--space", dir, "--database", kept[0], "default", aID)
	if err != nil || !strings.Contains(out, "earlier\n") {
		t.Errorf("draft export of the earlier draft: %v\n%s", err, out)
	}

	// Another start leaves the earlier one where it is, and imports nothing
	// twice.
	srv = start(t, cfg)
	srv.stop()
	if k, _ := filepath.Glob(filepath.Join(state, "state-earlier-*.db")); len(k) != 1 {
		t.Errorf("after another start: %v", k)
	}
}
