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

type checkRequest struct {
	checkID    string
	check      config.CheckConfig
	repository checkedOutRepository
}

type checkReport struct {
	outcome    checkOutcome
	install    *installReport
	commit     string
	reached    bool
	checkImage string
}

func (jr *jobRunner) runCheckRequest(ctx context.Context, request checkRequest, results map[string]agentResult) (checkReport, error) {
	report := checkReport{commit: results[request.repository.ID].commit, checkImage: request.check.Image}
	outcome, err := jr.runInCheckWorkspace(ctx, results, func(workspace *checkWorkspace, location checkLocation) checkOutcome {
		report.reached = true
		install, refusal := jr.installCheckDependencies(ctx, request, workspace, location, report.commit)
		report.install = install
		if refusal != nil {
			return *refusal
		}
		return jr.executeConfiguredCheck(ctx, request.checkID, location.root, location.dirs[request.repository.ID], request.check)
	})
	report.outcome = outcome
	return report, err
}

func (jr *jobRunner) executeConfiguredCheck(ctx context.Context, checkID, root, dir string, check config.CheckConfig) checkOutcome {
	timeout := 10 * time.Minute
	if check.TimeoutSeconds > 0 {
		timeout = time.Duration(check.TimeoutSeconds) * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	out, capped, runErr := jr.runWorkspaceTask(checkCtx, workspaceTask{
		purpose: "check-" + checkID, ownershipPurpose: "check", root: root, dir: dir,
		command: check.Command, args: check.Args, configuredNetwork: true, outputLimit: 1 << 20, image: check.Image,
	})
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
