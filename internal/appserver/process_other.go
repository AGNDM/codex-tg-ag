//go:build !(linux || darwin || freebsd || netbsd || openbsd)

package appserver

import "os/exec"

func configureCommand(*exec.Cmd) {}

func killCommand(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
