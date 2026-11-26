package api

import (
	"runtime/debug"
	"time"
)

// buildInfo says which build the server is, for the administrators of the whole server (GET /api/admin/status). The
// commit and its time are what the Go toolchain stamped into the binary; a build outside a Git checkout has none.
type buildInfo struct {
	// Commit is the first 7 characters of the commit the binary was built from; empty when unknown.
	Commit string `json:"commit"`
	// Time is RFC 3339, UTC; empty when unknown. TimeKind says what it is: "build" when the build injected its own
	// time, else "commit", the time of the commit.
	Time     string `json:"time"`
	TimeKind string `json:"timeKind"`
	// Modified is whether the working tree had changes the commit does not have.
	Modified bool `json:"modified"`
}

func readBuild(injected string) buildInfo {
	var b buildInfo
	if info, ok := debug.ReadBuildInfo(); ok {
		var commitTime string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				b.Commit = s.Value
				if len(b.Commit) > 7 {
					b.Commit = b.Commit[:7]
				}
			case "vcs.time":
				commitTime = s.Value
			case "vcs.modified":
				b.Modified = s.Value == "true"
			}
		}
		b.Time, b.TimeKind = commitTime, "commit"
	}
	if t, err := time.Parse(time.RFC3339, injected); err == nil {
		b.Time, b.TimeKind = t.UTC().Format(time.RFC3339), "build"
	}
	if b.Time == "" {
		b.TimeKind = ""
	}
	return b
}
