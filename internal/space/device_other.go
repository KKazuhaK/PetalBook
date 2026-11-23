//go:build !unix

package space

import (
	"path/filepath"
	"strings"
)

// sameDevice reports whether a and b are on one volume, as far as their
// names tell.
func sameDevice(a, b string) bool {
	return strings.EqualFold(filepath.VolumeName(a), filepath.VolumeName(b))
}
