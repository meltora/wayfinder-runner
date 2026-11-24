package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/collector"
	"github.com/example/app-runner/internal/validation"
)

const maxCheckpointPatchBytes = 64 << 20

var errControlAttemptCompleted = errors.New("attempt completion already started")

func validateJobControlCommandBoundary(command v1.CommandPayload, now time.Time) error {
	hasControlAuthority := command.ControlID != "" || command.Action != "" || command.RequestedAt != "" ||
		command.ExpiresAt != "" || command.LeaseID != "" || command.ManifestID != "" ||
		command.ManifestDigest != "" || command.AgentRunAuthority != nil || command.CommandDigest != ""
	if command.Command != v1.CmdControlJob {
		if hasControlAuthority {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return nil
	}
	if command.Job != nil || command.SourcePublication != nil || command.ProviderReviewerAssignment != nil ||
		command.ProviderReviewerAssignmentReconciliation != nil || command.ProviderReviewObservation != nil ||
		command.ProviderReviewFeedbackContent != nil || command.ReviewFeedbackContinuation != nil ||
		command.ProviderPullRequest != nil ||
		command.PreviewObservation != nil || command.PreviewLifecycle != nil || command.PreviewDeploy != nil ||
		command.ReviewerIdentityVerification != nil || !command.LeaseExpiresAt.IsZero() ||
		command.QuestionID != "" || command.Decision != "" || command.DecidedBy != "" ||
		command.UpdateID != "" || command.DrainDeadline != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	if err := validation.JobControlOperation(command.JobControlOperation(), now); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	return nil
}

func (d *Daemon) executeJobControl(ctx context.Context, commandMessageID string, operation v1.JobControlOperation) error {
	d.jobMu.Lock()
	jr := d.activeJob
	d.jobMu.Unlock()
	if jr == nil {
		return rejectCommand(v1.CommandAckReasonLeaseRejected, nil)
	}
	if !jobControlMatches(jr.job, operation) {
		return d.publishJobControlResult(ctx, jr.job, operation, v1.JobControlRejected, v1.JobControlReasonStaleAuthority)
	}
	if err := d.leases.Validate(operation.AttemptID, operation.FencingToken); err != nil {
		return d.publishJobControlResult(ctx, jr.job, operation, v1.JobControlRejected, v1.JobControlReasonStaleAuthority)
	}

	jr.controlMu.Lock()
	defer jr.controlMu.Unlock()
	jr.procMu.Lock()
	if jr.completionStarted {
		jr.procMu.Unlock()
		return d.publishJobControlResult(ctx, jr.job, operation, v1.JobControlRejected, v1.JobControlReasonTerminalAttempt)
	}
	previous := jr.controlAction
	if previous != "" {
		jr.procMu.Unlock()
		if previous == operation.Action {
			reason := v1.JobControlReasonAlreadyCancelled
			if previous == v1.JobControlPause {
				reason = v1.JobControlReasonAlreadyPaused
			}
			return d.publishJobControlResult(ctx, jr.job, operation, v1.JobControlAlreadyApplied, reason)
		}
		return d.publishJobControlResult(ctx, jr.job, operation, v1.JobControlRejected, v1.JobControlReasonTerminalAttempt)
	}
	if jr.controlResultDone == nil {
		jr.controlResultDone = make(chan struct{})
	}
	jr.procMu.Unlock()
	defer jr.signalControlResultReady()

	if operation.Action == v1.JobControlPause {
		if err := jr.persistControlCheckpoint(ctx, commandMessageID); err != nil {
			jr.logger.Warn("pause checkpoint unavailable", "control_id", operation.ControlID, "err", err)
			return d.publishJobControlResult(ctx, jr.job, operation, v1.JobControlRejected, v1.JobControlReasonCheckpointUnavailable)
		}
	}
	// Revalidate immediately before the only supervisor call. Checkpoint and
	// durable event work may have consumed the short-lived control authority.
	expiresAt, _ := time.Parse(time.RFC3339Nano, operation.ExpiresAt)
	if !expiresAt.After(time.Now()) || d.leases.Validate(operation.AttemptID, operation.FencingToken) != nil {
		return d.publishJobControlResult(ctx, jr.job, operation, v1.JobControlRejected, v1.JobControlReasonStaleAuthority)
	}
	if err := jr.stopForControl(operation.Action); err != nil {
		if errors.Is(err, errControlAttemptCompleted) {
			return d.publishJobControlResult(ctx, jr.job, operation, v1.JobControlRejected, v1.JobControlReasonTerminalAttempt)
		}
		jr.logger.Error("supervised process-tree stop failed", "control_id", operation.ControlID, "err", err)
		return d.publishJobControlResult(ctx, jr.job, operation, v1.JobControlRejected, v1.JobControlReasonRunnerInternalError)
	}
	return d.publishJobControlResult(ctx, jr.job, operation, v1.JobControlApplied, "")
}

func jobControlMatches(job v1.JobSpec, operation v1.JobControlOperation) bool {
	return job.AgentRunAuthority != nil && *job.AgentRunAuthority == operation.AgentRunAuthority &&
		job.JobID == operation.JobID && job.AttemptID == operation.AttemptID &&
		job.LeaseID == operation.LeaseID && job.ManifestID == operation.ManifestID &&
		job.ManifestDigest == operation.ManifestDigest && job.FencingToken == operation.FencingToken
}

func (jr *jobRunner) stopForControl(action v1.JobControlAction) error {
	jr.procMu.Lock()
	if jr.completionStarted {
		jr.procMu.Unlock()
		return errControlAttemptCompleted
	}
	proc := jr.proc
	if proc != nil {
		if err := proc.Kill(); err != nil {
			jr.procMu.Unlock()
			return err
		}
	}
	jr.controlAction = action
	jr.cancelled = true
	jr.procMu.Unlock()
	jr.cancel()
	return nil
}

func (jr *jobRunner) signalControlResultReady() {
	jr.procMu.Lock()
	done := jr.controlResultDone
	if done != nil {
		close(done)
	}
	jr.controlResultDone = nil
	jr.procMu.Unlock()
}

func (jr *jobRunner) awaitControlResult() {
	jr.procMu.Lock()
	done := jr.controlResultDone
	jr.procMu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		jr.logger.Error("timed out awaiting durable control result")
	}
}

func (jr *jobRunner) persistControlCheckpoint(ctx context.Context, commandMessageID string) error {
	recovery, err := jr.controlRecoveryCheckpoint(ctx, commandMessageID)
	if err != nil {
		return err
	}
	checkpoint := collector.NewCheckpoint(jr.job, "paused", "checkpointed", "await explicit human continuation")
	checkpoint.ProviderSessionID = jr.currentSessionID()
	checkpoint.TrustedRecovery = recovery
	if err := jr.d.collectors.SaveCheckpoint(checkpoint); err != nil {
		return err
	}
	fields := map[string]any{
		"plan_step": checkpoint.PlanStep, "branch_state": checkpoint.BranchState,
		"next_action": checkpoint.NextAction, "provider_session_id": checkpoint.ProviderSessionID,
		"created_at": checkpoint.CreatedAt.UTC().Format(time.RFC3339Nano), "recovery_checkpoint": recovery,
	}
	_, err = jr.d.sendEventWithStableID(ctx, jobControlCheckpointEventID(commandMessageID),
		manifestBoundEvent(jr.job, "checkpoint", fields, checkpoint.CreatedAt))
	return err
}

func (jr *jobRunner) controlRecoveryCheckpoint(ctx context.Context, commandMessageID string) (*v1.TrustedRecoveryCheckpoint, error) {
	if len(jr.repositories) == 0 || len(jr.repositories) > 16 {
		return nil, fmt.Errorf("active repository set is unavailable")
	}
	repositories := append([]checkedOutRepository(nil), jr.repositories...)
	sort.Slice(repositories, func(i, j int) bool { return repositories[i].ID < repositories[j].ID })
	result := &v1.TrustedRecoveryCheckpoint{
		SchemaVersion: 1, CheckpointID: checkpointID(commandMessageID), Collector: "runner",
		SafetyClassification: v1.RecoverySafetyDisplayOnly, SideEffectClassification: v1.RecoverySideEffectsUnknown,
		TestClassification: v1.RecoveryTestsUnknown,
		Repositories:       make([]v1.RecoveryCheckpointRepository, 0, len(repositories)),
	}
	for index, repository := range repositories {
		if index > 0 && repositories[index-1].ID == repository.ID {
			return nil, fmt.Errorf("duplicate repository identity")
		}
		snapshot, err := jr.snapshotRepository(ctx, repository)
		if err != nil {
			return nil, fmt.Errorf("snapshot repository %s: %w", repository.ID, err)
		}
		result.Repositories = append(result.Repositories, snapshot)
	}
	result.RepositorySetDigest = v1.RecoveryRepositorySetDigest(result.Repositories)
	result.DigestSHA256 = v1.TrustedRecoveryCheckpointDigest(*result)
	return result, nil
}

func (jr *jobRunner) snapshotRepository(ctx context.Context, repository checkedOutRepository) (v1.RecoveryCheckpointRepository, error) {
	result, err := jr.resolveAgentResult(ctx, repository, "pause checkpoint")
	if err != nil {
		return v1.RecoveryCheckpointRepository{}, err
	}
	if validation.GitCommitSHA("commit_sha", result.commit) != nil {
		return v1.RecoveryCheckpointRepository{}, fmt.Errorf("resolve exact commit")
	}
	if validation.GitRef("branch", repository.Branch) != nil {
		return v1.RecoveryCheckpointRepository{}, fmt.Errorf("resolve exact branch")
	}
	return v1.RecoveryCheckpointRepository{
		RepositoryID: repository.ID, Branch: repository.Branch, CommitSHA: result.commit,
	}, nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	raw, err := gitOutputBytes(ctx, dir, args...)
	return strings.TrimSpace(string(raw)), err
}

func gitOutputBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return gitOutputBytesLimited(ctx, dir, maxCheckpointPatchBytes, args...)
}

func gitOutputBytesLimited(ctx context.Context, dir string, limit int64, args ...string) ([]byte, error) {
	// #nosec G204 -- executable and argument vocabulary are runner-owned; no shell is used.
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Stderr = io.Discard
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, limit+1))
	if readErr != nil || int64(len(output)) > limit {
		_ = command.Process.Kill()
		_ = command.Wait()
		if readErr != nil {
			return nil, readErr
		}
		return nil, fmt.Errorf("git output exceeds %d-byte checkpoint limit", limit)
	}
	if err := command.Wait(); err != nil {
		return nil, err
	}
	return output, nil
}

func (d *Daemon) publishJobControlResult(ctx context.Context, job v1.JobSpec, operation v1.JobControlOperation, outcome v1.JobControlOutcome, reason v1.JobControlReasonCode) error {
	event := v1.JobControlResultEvent{
		Kind: "job_control_result", ManifestKind: job.Kind, LeaseID: job.LeaseID,
		JobID: job.JobID, AttemptID: job.AttemptID, ManifestID: job.ManifestID,
		ManifestDigest: job.ManifestDigest, FencingToken: job.FencingToken, ObservedAt: time.Now().UTC(),
		AgentRunAuthority: operation.AgentRunAuthority, ControlID: operation.ControlID,
		Action: operation.Action, Outcome: outcome, ReasonCode: reason, CommandDigest: operation.CommandDigest,
	}
	if err := validation.JobControlResultEvent(event); err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	_, err := d.sendEventWithStableID(ctx, jobControlResultEventID(operation.ControlID), event)
	return err
}

func checkpointID(commandMessageID string) string {
	digest := sha256.Sum256([]byte("checkpoint\x00" + commandMessageID))
	return "checkpoint-" + hex.EncodeToString(digest[:])
}

func jobControlCheckpointEventID(commandMessageID string) string {
	digest := sha256.Sum256([]byte(commandMessageID))
	return "job-control-checkpoint-" + hex.EncodeToString(digest[:])
}

func jobControlResultEventID(controlID string) string {
	digest := sha256.Sum256([]byte(controlID))
	return "job-control-result-" + hex.EncodeToString(digest[:])
}
