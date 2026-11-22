//go:build unix

package gitstore

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts git in its own process group and makes a cancelled
// context kill the whole group, so a hung ssh or remote helper under git dies
// with it and a hung remote cannot hold a space forever.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
