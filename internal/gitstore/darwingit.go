package gitstore

import (
	"os"
	"path/filepath"
	"strings"
)

// macOS ships /usr/bin/git as a forwarder that asks xcrun which git to run,
// which costs about 13 ms on every call. The git it forwards to is the one
// `xcrun --find git` prints; starting that directly saves the detour.

const darwinShim = "/usr/bin/git"

// resolveDarwinGit is the decision: on darwin, when the git found is the
// /usr/bin shim, the absolute, executable path xcrun reports replaces it. Any
// failure keeps what was found. xcrun is only called when it can matter.
func resolveDarwinGit(goos, lookedUp string, xcrun func() (string, error), isExec func(string) bool) string {
	if goos != "darwin" || lookedUp != darwinShim {
		return lookedUp
	}
	p, err := xcrun()
	if err != nil {
		return lookedUp
	}
	p = strings.TrimSpace(p)
	if p == "" || !filepath.IsAbs(p) || !isExec(p) {
		return lookedUp
	}
	return p
}

func isExecutableFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// xcrunEnv is the environment xcrun runs with: the same filtered one git gets,
// without GIT_* or anything else. DEVELOPER_DIR and SDKROOT are deliberately
// not passed: xcrun would let them choose the git, which the environment may
// influence through PATH already but not further than that.
func xcrunEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); passEnv[k] && !strings.HasPrefix(k, "GIT_") {
			env = append(env, kv)
		}
	}
	return env
}
