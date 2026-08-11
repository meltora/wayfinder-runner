package daemon

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/hookspool"
)

const maxCheckOutputBytes = 4096

type checkOutcome struct {
	Result    string
	Class     string
	Output    string
	Truncated bool
	Duration  time.Duration
}

func (jr *jobRunner) executeConfiguredCheck(ctx context.Context, checkID string, repository checkedOutRepository, check config.CheckConfig) checkOutcome {
	timeout := 10 * time.Minute
	if check.TimeoutSeconds > 0 {
		timeout = time.Duration(check.TimeoutSeconds) * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	out, capped, runErr := jr.runWorkspaceCommand(checkCtx, "check-"+checkID, repository.WorkDir,
		check.Command, check.Args, true, 1<<20)
	outcome := checkOutcome{
		Result:    "passed",
		Output:    redactCheckOutput(truncate(out, maxCheckOutputBytes)),
		Truncated: capped || len(out) > maxCheckOutputBytes,
		Duration:  time.Since(start),
	}
	if runErr != nil {
		outcome.Class = classifyCheckError(checkCtx, runErr)
		if outcome.Class == "exit_nonzero" {
			outcome.Result = "failed"
		} else {
			outcome.Result = "error"
		}
	}
	return outcome
}

func classifyCheckError(ctx context.Context, err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return "cancelled"
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "exit_nonzero"
	}
	return "internal"
}

func redactCheckOutput(out string) string {
	if out == "" {
		return ""
	}
	redacted, _ := hookspool.Redact(map[string]any{"output": out})["output"].(string)
	return redacted
}
