//go:build unix

package space

import (
	"os"
	"syscall"
)

// sameDevice reports whether a and b are on one file system (true when that
// cannot be told).
func sameDevice(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return true
	}
	fb, err := os.Stat(b)
	if err != nil {
		return true
	}
	sa, ok1 := fa.Sys().(*syscall.Stat_t)
	sb, ok2 := fb.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return true
	}
	return sa.Dev == sb.Dev
}
