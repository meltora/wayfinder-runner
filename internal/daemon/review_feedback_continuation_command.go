package daemon

import (
	"context"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

func validateReviewFeedbackContinuationCommandBoundary(command v1.CommandPayload) error {
	if command.Command != v1.CmdReviewFeedbackContinuation {
		if command.ReviewFeedbackContinuation != nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return nil
	}
	if command.Job == nil || command.ReviewFeedbackContinuation == nil ||
		command.SourcePublication != nil || command.ProviderReviewerAssignment != nil ||
		command.ProviderReviewerAssignmentReconciliation != nil || command.ProviderReviewObservation != nil ||
		command.ProviderReviewFeedbackContent != nil || command.PreviewObservation != nil ||
		command.ReviewerIdentityVerification != nil || command.JobID != "" || command.AttemptID != "" ||
		command.FencingToken != 0 || !command.LeaseExpiresAt.IsZero() || command.Reason != "" ||
		command.QuestionID != "" || command.Decision != "" || command.DecidedBy != "" ||
		command.UpdateID != "" || command.DrainDeadline != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	if err := validation.ReviewFeedbackContinuationCommand(*command.Job, *command.ReviewFeedbackContinuation); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	return nil
}

func (d *Daemon) startReviewFeedbackContinuation(
	ctx context.Context,
	job v1.JobSpec,
	authority v1.ReviewFeedbackContinuationAuthority,
	providerCredential *v1.ProviderCredential,
) error {
	if err := validation.ReviewFeedbackContinuationCommand(job, authority); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	if !authority.DeadlineAt.After(time.Now()) || !authority.LeaseExpiresAt.After(time.Now()) {
		return rejectCommand(v1.CommandAckReasonLeaseRejected, nil)
	}
	authorityCopy := authority
	return d.startJobWithReviewFeedbackContinuation(ctx, job, &authorityCopy, providerCredential)
}
