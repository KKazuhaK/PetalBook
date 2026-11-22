//go:build !(linux || darwin || freebsd)

package api

// diskFree: this platform has no statfs; the status leaves the field out.
func diskFree(string) (uint64, bool) { return 0, false }
