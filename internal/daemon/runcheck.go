package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

const runCheckTool = "platform_run_check"

func (jr *jobRunner) runnableChecks() []string {
	checks := make([]string, 0, len(jr.job.RequiredChecks))
	for _, id := range jr.job.RequiredChecks {
		check, ok := jr.d.cfg.Checks[id]
		if !ok {
			continue
		}
		if _, ok := jr.checkRepository(check.RepositoryID); !ok {
			continue
		}
		checks = append(checks, id)
	}
	return checks
}

func (jr *jobRunner) executeRunCheck(ctx context.Context, raw map[string]any) (any, error) {
	for field := range raw {
		if field != "check_id" && field != "idempotency_key" {
			return nil, fmt.Errorf("platform_run_check field %q is not allowed", field)
		}
	}
	if jr.job.Kind != v1.JobKindBuild {
		return nil, fmt.Errorf("platform_run_check is available to build jobs only")
	}
	checkID := str(raw, "check_id")
	if err := validation.ID("check_id", checkID); err != nil {
		return nil, err
	}
	if !slices.Contains(jr.job.RequiredChecks, checkID) {
		return nil, fmt.Errorf("check %q is not required by this job", checkID)
	}
	check, ok := jr.d.cfg.Checks[checkID]
	if !ok {
		return nil, fmt.Errorf("check %q is not registered on this runner", checkID)
	}
	key := str(raw, "idempotency_key")
	owner, found, err := jr.d.journal.ToolCallKeyOwner(ctx, jr.job.AttemptID, runCheckTool+":", key)
	if err != nil {
		return nil, fmt.Errorf("look up check idempotency binding: %w", err)
	}
	if found && owner != runCheckTool+":"+checkID {
		return nil, fmt.Errorf("idempotency key is already bound to a different check")
	}
	repository, ok := jr.checkRepository(check.RepositoryID)
	if !ok {
		return nil, fmt.Errorf("check %q repository is not part of the job", checkID)
	}
	jr.checkMu.Lock()
	defer jr.checkMu.Unlock()
	if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
		return nil, fmt.Errorf("lease validation before check execution: %w", err)
	}
	outcome := jr.executeConfiguredCheck(ctx, checkID, repository, check)
	evidenceCtx, evidenceCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer evidenceCancel()
	commitSHA, dirty, err := jr.repositoryEvidence(evidenceCtx, repository)
	if err != nil {
		return nil, err
	}
	if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
		return nil, fmt.Errorf("lease validation before check evidence: %w", err)
	}
	event := map[string]any{
		"suite":          checkID,
		"command":        append([]string{check.Command}, check.Args...),
		"result":         outcome.Result,
		"duration_ms":    outcome.Duration.Milliseconds(),
		"repository_id":  repository.ID,
		"commit_sha":     commitSHA,
		"worktree_dirty": dirty,
		"phase":          "iterative",
		"source":         "runner",
		"truncated":      outcome.Truncated,
	}
	if outcome.Result != "passed" {
		failures := []string{}
		if outcome.Output != "" {
			failures = append(failures, outcome.Output)
		}
		event["failures"] = failures
	}
	if outcome.Class != "" {
		event["error_classification"] = outcome.Class
	}
	if err := jr.sendJobEvent(ctx, "test_results", event); err != nil {
		return nil, err
	}
	result := map[string]any{
		"check_id":       checkID,
		"repository_id":  repository.ID,
		"result":         outcome.Result,
		"duration_ms":    outcome.Duration.Milliseconds(),
		"output":         outcome.Output,
		"truncated":      outcome.Truncated,
		"commit_sha":     commitSHA,
		"worktree_dirty": dirty,
	}
	if outcome.Class != "" {
		result["error_classification"] = outcome.Class
	}
	return result, nil
}

func (jr *jobRunner) repositoryEvidence(ctx context.Context, repository checkedOutRepository) (string, bool, error) {
	head, _, err := jr.runWorkspaceCommand(ctx, "git-head-runcheck-"+repository.ID, repository.WorkDir,
		"git", []string{"rev-parse", "--verify", "HEAD^{commit}"}, false, 1<<20)
	if err != nil {
		return "", false, fmt.Errorf("read repository HEAD: %w", err)
	}
	status, _, err := jr.runWorkspaceCommand(ctx, "git-status-runcheck-"+repository.ID, repository.WorkDir,
		"git", []string{"status", "--porcelain"}, false, 1<<20)
	if err != nil {
		return "", false, fmt.Errorf("read repository worktree state: %w", err)
	}
	return strings.TrimSpace(head), strings.TrimSpace(status) != "", nil
}
