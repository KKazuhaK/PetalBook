//go:build unix

package space

import (
	"io/fs"
	"syscall"
)

// fileID is the device and inode of a file, which stay with it through a
// rename; ok is false where the system does not give them.
func fileID(fi fs.FileInfo) (dev, ino uint64, ok bool) {
	st, isStat := fi.Sys().(*syscall.Stat_t)
	if !isStat || st.Ino == 0 {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true
}
