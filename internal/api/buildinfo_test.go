package api

import "testing"

func TestReadBuild(t *testing.T) {
	t.Parallel()
	// A time injected at build time wins over the commit's, and says so.
	b := readBuild("2026-11-25T14:48:00+08:00")
	if b.Time != "2026-11-25T06:48:00Z" || b.TimeKind != "build" {
		t.Fatalf("%+v", b)
	}
	// Garbage is not a time: what the toolchain stamped stays (nothing, in a test binary).
	b = readBuild("yesterday")
	if b.TimeKind != "" && b.TimeKind != "commit" {
		t.Fatalf("%+v", b)
	}
	if len(b.Commit) > 7 {
		t.Fatalf("commit not shortened: %+v", b)
	}
}
