//go:build unix

package spaces

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A copy across filesystems keeps each file's mode, whatever the umask, and a
// copy that cannot be renamed into place leaves no .partial behind.
func TestACopyAcrossFilesystemsKeepsModesAndLeavesNoPartial(t *testing.T) {
	// Serial: syscall.Umask changes the whole process.
	exdev := func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
	m := &Manager{opt: Options{Rename: exdev}}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(src, "sub", "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o666); err != nil { // beyond the usual umask
		t.Fatal(err)
	}
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	dst := filepath.Join(dir, "dst")
	if err := m.moveDir(src, dst); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dst, "sub", "f"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o666 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	if pathExists(src) || pathExists(src+movedSuffix) {
		t.Fatal("the source is still there")
	}

	// A destination in the way (not empty): the rename into place fails.
	if err := os.MkdirAll(filepath.Join(dir, "in-the-way", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.moveDir(dst, filepath.Join(dir, "in-the-way")); err == nil {
		t.Fatal("moved onto a directory in the way")
	}
	if pathExists(filepath.Join(dir, "in-the-way"+partialSuffix)) {
		t.Fatal("a .partial was left")
	}
	if !pathExists(filepath.Join(dst, "sub", "f")) {
		t.Fatal("the source was touched")
	}
}
