//go:build !unix

package space

import "io/fs"

// fileID is not known on this system: the checks that use it fall back to
// what the names say.
func fileID(fs.FileInfo) (dev, ino uint64, ok bool) { return 0, 0, false }
