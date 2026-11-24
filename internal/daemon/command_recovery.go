package daemon

import (
	"context"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/lease"
	"github.com/example/app-runner/internal/validation"
)

// commandEffectAlreadyApplied recognizes the narrow crash window after a job
// lease was durably installed but before the command outcome was persisted.
// Other commands are deliberately re-executed through their idempotent paths.
func (d *Daemon) commandEffectAlreadyApplied(command v1.CommandPayload) bool {
	if command.Command == v1.CmdWorkIntakeProposal {
		// Claiming the durable command receipt is the pre-provider paid-call
		// fence. A processing receipt after restart therefore means the effect
		// may have run and must never be invoked again automatically.
		return true
	}
	if command.Command == v1.CmdControlJob {
		if command.ControlID == "" {
			return false
		}
		exists, err := d.journal.HasRecord(context.Background(), jobControlResultEventID(command.ControlID))
		return err == nil && exists
	}
	if command.Command != v1.CmdStartJob && command.Command != v1.CmdReviewComment &&
		command.Command != v1.CmdReviewFeedbackContinuation {
		return false
	}
	if command.Job == nil || validation.JobSpec(*command.Job) != nil {
		return false
	}
	if command.Command == v1.CmdReviewFeedbackContinuation &&
		validateReviewFeedbackContinuationCommandBoundary(command) != nil {
		return false
	}

	d.jobMu.Lock()
	active := d.activeJob != nil && d.activeJob.job.JobID == command.Job.JobID &&
		d.activeJob.job.AttemptID == command.Job.AttemptID
	d.jobMu.Unlock()
	if !active && !d.recoveredLease.Load() {
		return false
	}
	return leaseMatchesJobAuthority(d.leases.Current(), *command.Job)
}

func leaseMatchesJobAuthority(current *lease.Lease, job v1.JobSpec) bool {
	return current != nil &&
		current.LeaseID == job.LeaseID &&
		current.JobID == job.JobID &&
		current.AttemptID == job.AttemptID &&
		current.FencingToken == job.FencingToken &&
		current.Kind == job.Kind &&
		current.ManifestID == job.ManifestID &&
		current.ManifestDigest == job.ManifestDigest &&
		current.WorkflowRevision == job.WorkflowRevision &&
		timesEqual(current.ExpiresAt, job.LeaseExpiresAt)
}

func timesEqual(left, right time.Time) bool {
	return !left.IsZero() && left.Equal(right)
}

func (d *Daemon) recoverInterruptedPreviewDeploy(
	ctx context.Context,
	commandMessageID string,
	operation v1.PreviewDeployOperation,
	host journal.PreviewHost,
) error {
	if err := d.previewHostController.Recover(ctx, host); err != nil {
		d.logger.Warn("interrupted preview deploy teardown unproven", "preview_id", host.PreviewID, "err", err)
	}
	return d.reportUncertainPreviewDeploy(ctx, commandMessageID, operation)
}
