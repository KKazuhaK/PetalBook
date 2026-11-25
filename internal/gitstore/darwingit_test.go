package gitstore

import (
	"errors"
	"testing"
)

func TestResolveDarwinGit(t *testing.T) {
	const shim = "/usr/bin/git"
	const real = "/Library/Developer/CommandLineTools/usr/bin/git"
	ok := func(string) bool { return true }
	no := func(string) bool { return false }
	out := func(s string) func() (string, error) { return func() (string, error) { return s, nil } }
	fail := func() (string, error) { return "", errors.New("exit status 1") }
	never := func() (string, error) { panic("xcrun must not be called") }

	tests := []struct {
		name     string
		goos     string
		lookedUp string
		xcrun    func() (string, error)
		isExec   func(string) bool
		want     string
	}{
		{"linux passthrough", "linux", shim, never, ok, shim},
		{"windows passthrough", "windows", `C:\Git\git.exe`, never, ok, `C:\Git\git.exe`},
		{"homebrew passthrough", "darwin", "/opt/homebrew/bin/git", never, ok, "/opt/homebrew/bin/git"},
		{"shim and good xcrun", "darwin", shim, out(real), ok, real},
		{"trailing newline", "darwin", shim, out(real + "\n"), ok, real},
		{"xcrun error", "darwin", shim, fail, ok, shim},
		{"empty output", "darwin", shim, out(""), ok, shim},
		{"blank output", "darwin", shim, out(" \n"), ok, shim},
		{"relative path", "darwin", shim, out("usr/bin/git\n"), ok, shim},
		{"not executable", "darwin", shim, out(real), no, shim},
		{"xcrun points at the shim", "darwin", shim, out(shim), ok, shim},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveDarwinGit(tt.goos, tt.lookedUp, tt.xcrun, tt.isExec); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
