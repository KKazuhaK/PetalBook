package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// --profile cn on the first start seeds the site values; later starts ignore
// the same profile and refuse a different one.
func TestServeProfileSeedsOnTheFirstStartOnly(t *testing.T) {
	needGit(t)
	data := filepath.Join(tempDir(t), "data")
	c := serveConfig{data: data, profile: "cn", committer: "Svc <svc@example.test>", commitIdle: time.Hour, commitMax: time.Hour}
	srv := start(t, c)
	if !strings.Contains(srv.log.String(), "seeded") {
		t.Errorf("no word of the seeding in the log:\n%s", srv.log)
	}
	if err := srv.stop(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(data, "petalbook.db"))
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := settings.New(st).Site(ctx)
	st.Close()
	if err != nil || m[settings.FeatureMCP].Value != false {
		t.Fatal(err, m[settings.FeatureMCP].Value)
	}
	// The same profile again starts, and says it is ignored.
	srv = start(t, c)
	if !strings.Contains(srv.log.String(), "--profile is ignored") {
		t.Errorf("no word of the ignored flag:\n%s", srv.log)
	}
	if err := srv.stop(); err != nil {
		t.Fatal(err)
	}
	// Another one does not.
	c.profile = "default"
	c.listen, c.logger = "127.0.0.1:0", nil
	if err := runServe(ctx, c); err == nil || !strings.Contains(err.Error(), "set up with the cn profile") {
		t.Fatalf("%v", err)
	}
	// An unknown one is refused before anything starts.
	if _, err := (serveConfig{data: data, profile: "mars"}).check(); err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("%v", err)
	}
	if _, err := (serveConfig{space: data, profile: "cn"}).check(); err == nil || !strings.Contains(err.Error(), "accounts mode") {
		t.Fatalf("%v", err)
	}
}
