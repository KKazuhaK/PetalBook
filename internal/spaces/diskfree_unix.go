//go:build linux || darwin || freebsd

package spaces

import "syscall"

// diskFree is the room a non-privileged process can still use on the disk of dir.
func diskFree(dir string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true
}
