//go:build !windows

package repo

import (
	"os"
	"os/exec"
	"syscall"
)

func configureScanProcess(cmd *exec.Cmd) func() {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil || cmd.Process.Pid <= 1 {
			return os.ErrProcessDone
		}
		// Setpgid creates a group owned by this invocation, with the child's PID
		// as its identifier. The group can outlive a reaped leader, so looking up
		// the leader's current group would miss surviving transport descendants.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	return func() { _ = cmd.Cancel() }
}
