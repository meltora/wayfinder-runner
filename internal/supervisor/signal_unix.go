//go:build !windows

package supervisor

import (
	"os/exec"
	"syscall"
)

func sigInterrupt() syscall.Signal { return syscall.SIGINT }
func sigKill() syscall.Signal      { return syscall.SIGKILL }

func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalProcessGroup(pid int, sig syscall.Signal) error {
	return syscall.Kill(-pid, sig)
}
