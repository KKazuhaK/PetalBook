//go:build !(linux || darwin || freebsd)

package spaces

// diskFree is not known on this platform.
func diskFree(dir string) (uint64, bool) { return 0, false }
