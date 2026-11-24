//go:build !(linux || darwin || freebsd)

package spaces

import "os"

// lockDataDir only makes the lock file on this platform: a second server is
// not detected.
func lockDataDir(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
}
