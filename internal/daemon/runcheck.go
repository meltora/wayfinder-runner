package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/validation"
)

const runCheckTool = "platform_run_check"

const (
	runCheckPerCheckExecBudget   = 10
	runCheckPerAttemptExecBudget = 50
)

func (jr *jobRunner) runnableChecks() []string {
	if jr.job.Kind != v1.JobKindBuild {
		return nil
	}
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

// runCheckEventID derives a stable, non-secret identity for the iterative
// evidence of one (attempt, check, idempotency key) triple. The stable ID lets
// the journal close the duplicate window between event publication and
// tool-result commit.
func runCheckEventID(attemptID, checkID, key string) string {
	sum := sha256.Sum256([]byte(attemptID + "\x00" + checkID + "\x00" + key))
	return "runcheck-" + hex.EncodeToString(sum[:16])
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
	repository, ok := jr.checkRepository(check.RepositoryID)
	if !ok {
		return nil, fmt.Errorf("check %q repository is not part of the job", checkID)
	}
	key := str(raw, "idempotency_key")
	if err := validation.ID("idempotency_key", key); err != nil {
		return nil, err
	}
	if _, err := jr.d.journal.ClaimCheckExecution(ctx, jr.job.AttemptID, key, checkID,
		runCheckPerCheckExecBudget, runCheckPerAttemptExecBudget); err != nil {
		if errors.Is(err, journal.ErrCheckClaimConflict) {
			return nil, fmt.Errorf("idempotency key is already bound to a different check")
		}
		if errors.Is(err, journal.ErrCheckBudgetExhausted) {
			return nil, errors.New("on-demand check execution budget is exhausted")
		}
		return nil, fmt.Errorf("claim check execution: %w", err)
	}
	jr.checkMu.Lock()
	defer jr.checkMu.Unlock()
	if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
		return nil, fmt.Errorf("lease validation before check execution: %w", err)
	}
	eventID := runCheckEventID(jr.job.AttemptID, checkID, key)
	if exists, err := jr.d.journal.HasRecord(ctx, eventID); err != nil {
		return nil, fmt.Errorf("inspect check evidence journal: %w", err)
	} else if exists {
		return jr.recoverRunCheckResult(ctx, checkID, key)
	}
	runCtx, stopLeaseWatch := jr.leaseMonitoredContext(ctx)
	defer stopLeaseWatch()
	results, err := jr.checkResults(runCtx, nil, "check run")
	if err != nil {
		return nil, err
	}
	report, err := jr.runCheckRequest(runCtx, checkRequest{checkID: checkID, check: check, repository: repository}, results)
	if err != nil {
		return nil, errors.New(checkWorkspaceMessage(err))
	}
	outcome := report.outcome
	if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
		return nil, fmt.Errorf("lease validation after check execution: %w", err)
	}
	evidenceCtx, evidenceCancel := context.WithTimeout(jr.ctx, 30*time.Second)
	defer evidenceCancel()
	commitSHA, dirty := results[repository.ID].commit, false
	if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
		return nil, fmt.Errorf("lease validation before check evidence: %w", err)
	}
	event := map[string]any{
		"suite":          checkID,
		"check_id":       checkID,
		"result":         outcome.Result,
		"duration_ms":    outcome.Duration.Milliseconds(),
		"repository_id":  repository.ID,
		"commit_sha":     commitSHA,
		"worktree_dirty": dirty,
		"phase":          "iterative",
		"source":         "runner",
	}
	if outcome.Result != "passed" {
		failures := []string{}
		if outcome.Output != "" {
			failures = append(failures, outcome.Output)
		}
		event["failures"] = failures
		event["truncated"] = outcome.Truncated
	}
	if outcome.Class != "" {
		event["error_classification"] = outcome.Class
	}
	event = manifestBoundEvent(jr.job, "test_results", event, time.Now())
	if _, err := jr.d.sendEventWithStableID(evidenceCtx, eventID, event); err != nil {
		return nil, err
	}
	result := map[string]any{
		"check_id":       checkID,
		"repository_id":  repository.ID,
		"result":         outcome.Result,
		"duration_ms":    outcome.Duration.Milliseconds(),
		"commit_sha":     commitSHA,
		"worktree_dirty": dirty,
	}
	if outcome.Result != "passed" {
		result["output"] = outcome.Output
		result["truncated"] = outcome.Truncated
	}
	if outcome.Class != "" {
		result["error_classification"] = outcome.Class
	}
	return result, nil
}

// leaseMonitoredContext derives an execution context that is cancelled when
// the caller context ends, when the job ends, or when the active lease or
// fencing token is lost, so an on-demand check never outlives its authority.
func (jr *jobRunner) leaseMonitoredContext(ctx context.Context) (context.Context, context.CancelFunc) {
	runCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-jr.ctx.Done():
				cancel()
				return
			case <-ticker.C:
				if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
					jr.logger.Warn("lease lost during on-demand check; killing execution", "err", err)
					cancel()
					return
				}
			}
		}
	}()
	return runCtx, cancel
}
