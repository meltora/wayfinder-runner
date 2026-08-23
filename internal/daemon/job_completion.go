package daemon

import (
	"context"
	"os"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/collector"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/validation"
)

func (jr *jobRunner) completeJob(outcome jobOutcome, capacityEvent *v1.ProviderCapacityExhaustedEvent) {
	jr.procMu.Lock()
	jr.completionStarted = true
	jr.procMu.Unlock()
	jr.awaitControlResult()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	outcome, capacityEvent = jr.enforceCompletionAuthority(outcome, capacityEvent)
	jr.persistFinalSessionCheckpoint(capacityEvent)
	outcome, capacityEvent = jr.cleanupCompletionIsolation(ctx, outcome, capacityEvent)
	if jr.recordCompletionEvidence(ctx, outcome, capacityEvent) {
		jr.releaseCompletedLocalLease(ctx)
	}
	jr.retireCompletedJob()
}

func (jr *jobRunner) enforceCompletionAuthority(
	outcome jobOutcome,
	capacityEvent *v1.ProviderCapacityExhaustedEvent,
) (jobOutcome, *v1.ProviderCapacityExhaustedEvent) {
	jr.procMu.Lock()
	leaseLost := jr.leaseLost
	jr.procMu.Unlock()
	if !leaseLost {
		leaseLost = jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken) != nil
	}
	if !leaseLost {
		return outcome, capacityEvent
	}
	return jobOutcome{
		State: v1.JobFinishedQuarantined, FailureCode: v1.JobFailureLeaseLost,
		FailureMessage: "authoritative lease was lost",
	}, nil
}

func (jr *jobRunner) persistFinalSessionCheckpoint(capacityEvent *v1.ProviderCapacityExhaustedEvent) {
	sessionID := jr.currentSessionID()
	if sessionID == "" || capacityEvent != nil {
		return
	}
	checkpoint := collector.NewCheckpoint(jr.job, "finished", "", "")
	checkpoint.ProviderSessionID = sessionID
	if err := jr.d.collectors.SaveCheckpoint(checkpoint); err != nil {
		jr.logger.Warn("save final checkpoint", "err", err)
	}
}

func (jr *jobRunner) cleanupCompletionIsolation(
	ctx context.Context,
	outcome jobOutcome,
	capacityEvent *v1.ProviderCapacityExhaustedEvent,
) (jobOutcome, *v1.ProviderCapacityExhaustedEvent) {
	if jr.d.cfg.Isolation != "docker" {
		return outcome, capacityEvent
	}
	if err := isolation.RemoveRunnerContainers(ctx, jr.d.runnerID); err != nil {
		jr.logger.Error("remove job container", "err", err)
		return jobOutcome{
			State: v1.JobFinishedQuarantined, FailureCode: v1.JobFailureCleanup,
			FailureMessage: "job container cleanup failed",
		}, nil
	}
	return outcome, capacityEvent
}

func (jr *jobRunner) recordCompletionEvidence(
	ctx context.Context,
	outcome jobOutcome,
	capacityEvent *v1.ProviderCapacityExhaustedEvent,
) bool {
	var evidence any
	var err error
	if capacityEvent != nil {
		evidence = *capacityEvent
		err = validation.ProviderCapacityExhaustedEvent(*capacityEvent)
	} else {
		terminal := jr.jobFinishedEvent(outcome)
		evidence = terminal
		err = validation.JobFinishedEvent(terminal)
	}
	if err == nil {
		err = jr.d.sendEvent(ctx, evidence)
	}
	if err != nil {
		jr.d.recoveredLease.Store(true)
		jr.logger.Error("record terminal evidence; lease retained for reconciliation", "err", err)
		return false
	}
	return true
}

func (jr *jobRunner) releaseCompletedLocalLease(ctx context.Context) {
	if err := jr.d.journal.ClearLease(ctx, jr.job.AttemptID); err != nil {
		jr.d.recoveredLease.Store(true)
		jr.logger.Error("clear durable lease; lease retained for reconciliation", "err", err)
		return
	}
	jr.d.leases.Release(jr.job.AttemptID)
	jr.d.recoveredLease.Store(false)
}

func (jr *jobRunner) retireCompletedJob() {
	jr.d.jobMu.Lock()
	jr.d.activeJob = nil
	jr.d.signalUpdateLocked()
	jr.d.jobMu.Unlock()
	if err := safeio.MakeTreeRemovable(jr.workDir); err != nil {
		jr.logger.Warn("prepare workdir cleanup", "err", err)
	}
	if err := os.RemoveAll(jr.workDir); err != nil {
		jr.logger.Warn("workdir cleanup", "err", err)
	}
}
