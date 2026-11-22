//go:build !unix

package gitstore

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {}
