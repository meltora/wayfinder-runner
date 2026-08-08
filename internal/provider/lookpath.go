package provider

import "os/exec"

func exec_LookPath(name string) (string, error) { return exec.LookPath(name) }
