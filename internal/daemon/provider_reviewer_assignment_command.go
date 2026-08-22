package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

type providerReviewerAssigner interface {
	ReadyProviderReviewerAssignmentInstallations() []v1.ProviderReviewerAssignmentInstallationCapability
	AssignProviderReviewers(context.Context, v1.ProviderReviewerAssignmentOperation) v1.ProviderReviewerAssignmentResultOutcome
}

func validateProviderReviewerAssignmentCommandBoundary(command v1.CommandPayload) error {
	if command.Command != v1.CmdProviderReviewerAssignment {
		if command.ProviderReviewerAssignment != nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return nil
	}
	if command.ProviderReviewerAssignment == nil || command.ProviderReviewFeedbackContent != nil || command.ProviderReviewObservation != nil || command.ProviderReviewerAssignmentReconciliation != nil || command.SourcePublication != nil ||
		command.PreviewObservation != nil || command.ReviewerIdentityVerification != nil || command.Job != nil ||
		command.JobID != "" || command.AttemptID != "" || command.FencingToken != 0 ||
		!command.LeaseExpiresAt.IsZero() || command.Reason != "" || command.QuestionID != "" ||
		command.Decision != "" || command.DecidedBy != "" || command.UpdateID != "" ||
		command.DrainDeadline != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	if err := validation.ProviderReviewerAssignmentOperation(*command.ProviderReviewerAssignment); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	return nil
}

func (d *Daemon) executeProviderReviewerAssignment(
	ctx context.Context,
	commandMessageID string,
	operation v1.ProviderReviewerAssignmentOperation,
) error {
	if d.providerReviewerAssigner == nil {
		return rejectCommand(v1.CommandAckReasonUnsupportedCommand, nil)
	}
	if err := validation.ProviderReviewerAssignmentCommandMessageID(commandMessageID); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	if !operation.LeaseExpiresAt.After(time.Now()) {
		return rejectCommand(v1.CommandAckReasonLeaseRejected, nil)
	}
	assignCtx, cancel := context.WithDeadline(ctx, operation.LeaseExpiresAt)
	defer cancel()
	now := time.Now()
	outcome := v1.ProviderReviewerAssignmentResultOutcome{
		Kind: v1.ProviderReviewerAssignmentPermanent,
		Code: v1.ProviderReviewerAssignmentReviewerIdentityExpired,
	}
	if !providerReviewerAssignmentIdentityExpired(operation, now) {
		outcome = d.providerReviewerAssigner.AssignProviderReviewers(assignCtx, operation)
	}
	observedAt := time.Now().UTC()
	if observedAt.After(operation.LeaseExpiresAt) {
		observedAt = operation.LeaseExpiresAt
		if outcome.Kind == v1.ProviderReviewerAssignmentSucceeded ||
			outcome.Kind == v1.ProviderReviewerAssignmentUncertain {
			outcome = v1.ProviderReviewerAssignmentResultOutcome{
				Kind: v1.ProviderReviewerAssignmentUncertain,
				Code: v1.ProviderReviewerAssignmentDeliveryUncertain,
			}
		} else if providerReviewerAssignmentIdentityExpired(operation, time.Now()) {
			outcome = v1.ProviderReviewerAssignmentResultOutcome{
				Kind: v1.ProviderReviewerAssignmentPermanent,
				Code: v1.ProviderReviewerAssignmentReviewerIdentityExpired,
			}
		} else {
			outcome = v1.ProviderReviewerAssignmentResultOutcome{
				Kind: v1.ProviderReviewerAssignmentRetryable,
				Code: v1.ProviderReviewerAssignmentProviderUnavailable,
			}
		}
	}
	event := v1.ProviderReviewerAssignmentResultEvent{
		Kind: v1.EventProviderReviewerAssignmentResult,
		ProviderReviewerAssignmentOperationAuthority: operation.ProviderReviewerAssignmentOperationAuthority,
		CommandMessageID: commandMessageID,
		ObservedAt:       observedAt,
		Outcome:          outcome,
	}
	if err := validation.ProviderReviewerAssignmentResultEvent(event); err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	_, err := d.sendEventWithStableID(ctx, providerReviewerAssignmentResultEventID(commandMessageID), event)
	return err
}

func providerReviewerAssignmentResultEventID(commandMessageID string) string {
	digest := sha256.Sum256([]byte(commandMessageID))
	return "provider-reviewer-assignment-result-" + hex.EncodeToString(digest[:])
}

func (d *Daemon) completeProviderReviewerAssignmentCommand(
	ctx context.Context,
	envelope v1.Envelope,
	command v1.CommandPayload,
	claimOwner string,
	effectMayHaveRun bool,
) {
	defer d.providerReviewerAssignmentCommands.Done()
	defer d.providerReviewerAssignmentCommandFinish(envelope.ID)
	if err := d.completeClaimedCommand(ctx, envelope, command, nil, claimOwner, effectMayHaveRun); err != nil {
		d.logger.Error("complete provider reviewer assignment command", "id", envelope.ID, "err", err)
	}
}

func providerReviewerAssignmentIdentityExpired(
	operation v1.ProviderReviewerAssignmentOperation,
	now time.Time,
) bool {
	for _, recipient := range operation.Recipients {
		if !recipient.IdentityVerificationExpiresAt.After(now) ||
			!recipient.ProviderAccountProofExpiresAt.After(now) {
			return true
		}
	}
	return false
}

func (d *Daemon) providerReviewerAssignmentCommandIsActive(commandID string) bool {
	d.providerReviewerAssignmentCommandMu.Lock()
	defer d.providerReviewerAssignmentCommandMu.Unlock()
	_, active := d.providerReviewerAssignmentCommandActive[commandID]
	return active
}

func (d *Daemon) providerReviewerAssignmentCommandStart(commandID string) bool {
	d.providerReviewerAssignmentCommandMu.Lock()
	defer d.providerReviewerAssignmentCommandMu.Unlock()
	if d.providerReviewerAssignmentCommandActive == nil {
		d.providerReviewerAssignmentCommandActive = make(map[string]struct{})
	}
	if _, active := d.providerReviewerAssignmentCommandActive[commandID]; active {
		return false
	}
	d.providerReviewerAssignmentCommandActive[commandID] = struct{}{}
	return true
}

func (d *Daemon) providerReviewerAssignmentCommandFinish(commandID string) {
	d.providerReviewerAssignmentCommandMu.Lock()
	delete(d.providerReviewerAssignmentCommandActive, commandID)
	d.providerReviewerAssignmentCommandMu.Unlock()
}
