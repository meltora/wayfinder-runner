package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/example/app-runner/internal/plancommand"
)

const planCommandGuardDeadline = 2 * time.Second

func denyPlanCommand(stderr io.Writer, reason string) int {
	fmt.Fprintln(stderr, "planning command guard denied this command: "+reason)
	return 2
}

func runPlanCommandGuard(args []string, stdin io.Reader, _ io.Writer, stderr io.Writer) int {
	roots, err := parsePlanCommandGuardRoots(args)
	if err != nil {
		return denyPlanCommand(stderr, err.Error())
	}
	guard, err := plancommand.New(roots)
	if err != nil {
		return denyPlanCommand(stderr, err.Error())
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, plancommand.MaxHookInputBytes+1))
	if err != nil {
		return denyPlanCommand(stderr, "hook input could not be read")
	}
	if err := guard.CheckHookInput(raw); err != nil {
		return denyPlanCommand(stderr, err.Error())
	}
	return 0
}

type planCommandGuardResult struct {
	status int
	stderr []byte
}

func runPlanCommandGuardWithin(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, deadline time.Duration) int {
	done := make(chan planCommandGuardResult, 1)
	go func() {
		var reason bytes.Buffer
		status := runPlanCommandGuard(args, stdin, io.Discard, &reason)
		done <- planCommandGuardResult{status: status, stderr: reason.Bytes()}
	}()
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case result := <-done:
		_, _ = stderr.Write(result.stderr)
		return result.status
	case <-timer.C:
		return denyPlanCommand(stderr, fmt.Sprintf("guard did not decide within %s", deadline))
	}
}

func parsePlanCommandGuardRoots(args []string) ([]string, error) {
	var roots []string
	for i := 0; i < len(args); i++ {
		if args[i] != "--root" {
			return nil, fmt.Errorf("unexpected argument %q", args[i])
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf("--root requires a directory")
		}
		roots = append(roots, args[i+1])
		i++
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("at least one --root is required")
	}
	return roots, nil
}

func cmdPlanCommandGuard(args []string) int {
	return runPlanCommandGuardWithin(args, os.Stdin, os.Stdout, os.Stderr, planCommandGuardDeadline)
}
