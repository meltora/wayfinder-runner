//go:build !windows

package daemon

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcessTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return killOwnedProcessGroup(cmd.Process)
	}
}

func killOwnedProcessGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	if p.Pid <= 1 {
		return p.Kill()
	}
	pgid, err := syscall.Getpgid(p.Pid)
	if err != nil {
		return nil
	}
	if pgid != p.Pid {
		return p.Kill()
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
