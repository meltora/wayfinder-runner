//go:build windows

package repo

import "os/exec"

func configureScanProcess(cmd *exec.Cmd) func() { return func() {} }
