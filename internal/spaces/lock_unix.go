//go:build linux || darwin || freebsd

package spaces

import (
	"errors"
	"os"
	"syscall"
)

// lockDataDir takes the exclusive lock of the data directory, without
// waiting: ErrDataDirInUse when another server holds it. The lock goes with
// the process, so a crash never leaves it held.
func lockDataDir(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrDataDirInUse
		}
		return nil, err
	}
	return f, nil
}

// unlockDataDir releases the lock and closes the file. The lock belongs to the
// open file description, so closing alone would leave it held while a child
// process forked meanwhile still has a copy of the descriptor.
func unlockDataDir(f *os.File) error {
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return f.Close()
}
