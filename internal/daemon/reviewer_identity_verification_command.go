package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

type reviewerIdentityVerifier interface {
	ReadyReviewerIdentityInstallations() []v1.ReviewerIdentityVerificationInstallationCapability
	VerifyReviewerIdentity(context.Context, v1.ReviewerIdentityVerificationOperation) v1.ReviewerIdentityVerificationResultOutcome
}

func validateReviewerIdentityVerificationCommandBoundary(command v1.CommandPayload) error {
	if command.Command != v1.CmdReviewerIdentityVerification {
		if command.ReviewerIdentityVerification != nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return nil
	}
	if command.ReviewerIdentityVerification == nil || command.SourcePublication != nil ||
		command.PreviewObservation != nil || command.Job != nil || command.JobID != "" ||
		command.AttemptID != "" || command.FencingToken != 0 || !command.LeaseExpiresAt.IsZero() ||
		command.Reason != "" || command.QuestionID != "" || command.Decision != "" ||
		command.DecidedBy != "" || command.UpdateID != "" || command.DrainDeadline != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	if err := validation.ReviewerIdentityVerificationOperation(*command.ReviewerIdentityVerification); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	return nil
}

func (d *Daemon) executeReviewerIdentityVerification(
	ctx context.Context,
	commandMessageID string,
	operation v1.ReviewerIdentityVerificationOperation,
) error {
	if d.reviewerIdentityVerifier == nil {
		return rejectCommand(v1.CommandAckReasonUnsupportedCommand, nil)
	}
	if err := validation.ReviewerIdentityVerificationCommandMessageID(commandMessageID); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	if !operation.LeaseExpiresAt.After(time.Now()) {
		return rejectCommand(v1.CommandAckReasonLeaseRejected, nil)
	}
	verificationCtx, cancel := context.WithDeadline(ctx, operation.LeaseExpiresAt)
	defer cancel()
	outcome := d.reviewerIdentityVerifier.VerifyReviewerIdentity(verificationCtx, operation)
	observedAt := time.Now().UTC()
	if observedAt.After(operation.LeaseExpiresAt) {
		observedAt = operation.LeaseExpiresAt
		outcome = v1.ReviewerIdentityVerificationResultOutcome{
			Kind: v1.ReviewerIdentityVerificationRetryable,
			Code: v1.ReviewerIdentityProviderUnavailable,
		}
	}
	event := v1.ReviewerIdentityVerificationResultEvent{
		Kind: v1.EventReviewerIdentityVerificationResult,
		ReviewerIdentityVerificationResultAuthority: v1.ReviewerIdentityVerificationResultAuthorityFromOperation(operation),
		CommandMessageID: commandMessageID,
		ObservedAt:       observedAt,
		Outcome:          outcome,
	}
	if err := validation.ReviewerIdentityVerificationResultEvent(event); err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	_, err := d.sendEventWithStableID(ctx, reviewerIdentityVerificationResultEventID(commandMessageID), event)
	return err
}

func reviewerIdentityVerificationResultEventID(commandMessageID string) string {
	digest := sha256.Sum256([]byte(commandMessageID))
	return "reviewer-identity-verification-result-" + hex.EncodeToString(digest[:])
}

func (d *Daemon) completeReviewerIdentityVerificationCommand(
	ctx context.Context,
	envelope v1.Envelope,
	command v1.CommandPayload,
	claimOwner string,
	effectMayHaveRun bool,
) {
	defer d.reviewerIdentityVerificationCommands.Done()
	defer d.reviewerIdentityVerificationCommandFinish(envelope.ID)
	if err := d.completeClaimedCommand(ctx, envelope, command, nil, claimOwner, effectMayHaveRun); err != nil {
		d.logger.Error("complete reviewer identity verification command", "id", envelope.ID, "err", err)
	}
}

func (d *Daemon) reviewerIdentityVerificationCommandIsActive(commandID string) bool {
	d.reviewerIdentityVerificationCommandMu.Lock()
	defer d.reviewerIdentityVerificationCommandMu.Unlock()
	_, active := d.reviewerIdentityVerificationCommandActive[commandID]
	return active
}

func (d *Daemon) reviewerIdentityVerificationCommandStart(commandID string) bool {
	d.reviewerIdentityVerificationCommandMu.Lock()
	defer d.reviewerIdentityVerificationCommandMu.Unlock()
	if d.reviewerIdentityVerificationCommandActive == nil {
		d.reviewerIdentityVerificationCommandActive = make(map[string]struct{})
	}
	if _, active := d.reviewerIdentityVerificationCommandActive[commandID]; active {
		return false
	}
	d.reviewerIdentityVerificationCommandActive[commandID] = struct{}{}
	return true
}

func (d *Daemon) reviewerIdentityVerificationCommandFinish(commandID string) {
	d.reviewerIdentityVerificationCommandMu.Lock()
	delete(d.reviewerIdentityVerificationCommandActive, commandID)
	d.reviewerIdentityVerificationCommandMu.Unlock()
}
