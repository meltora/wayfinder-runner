package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

type providerReviewerAssignmentReconciler interface {
	ReadyProviderReviewerAssignmentReconciliationInstallations() []v1.ProviderReviewerAssignmentReconciliationInstallationCapability
	ReconcileProviderReviewerAssignment(context.Context, v1.ProviderReviewerAssignmentReconciliationOperation, *v1.ProviderCredential) v1.ProviderReviewerAssignmentReconciliationResultOutcome
}

func validateProviderReviewerAssignmentReconciliationCommandBoundary(command v1.CommandPayload) error {
	if command.Command != v1.CmdProviderReviewerAssignmentReconciliation {
		if command.ProviderReviewerAssignmentReconciliation != nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return nil
	}
	if command.ProviderReviewerAssignmentReconciliation == nil || command.ProviderReviewFeedbackContent != nil || command.ProviderReviewObservation != nil || command.ProviderReviewerAssignment != nil || command.SourcePublication != nil || command.PreviewObservation != nil || command.ReviewerIdentityVerification != nil || command.Job != nil || command.JobID != "" || command.AttemptID != "" || command.FencingToken != 0 || !command.LeaseExpiresAt.IsZero() || command.Reason != "" || command.QuestionID != "" || command.Decision != "" || command.DecidedBy != "" || command.UpdateID != "" || command.DrainDeadline != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	if err := validation.ProviderReviewerAssignmentReconciliationOperation(*command.ProviderReviewerAssignmentReconciliation); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	return nil
}

func (d *Daemon) executeProviderReviewerAssignmentReconciliation(ctx context.Context, commandMessageID string, operation v1.ProviderReviewerAssignmentReconciliationOperation, providerCredential *v1.ProviderCredential) error {
	reconciler := d.sourcePublicationServices().providerReviewerAssignmentReconciler
	if reconciler == nil {
		return rejectCommand(v1.CommandAckReasonUnsupportedCommand, nil)
	}
	if err := validation.ProviderReviewerAssignmentReconciliationCommandMessageID(commandMessageID); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	if !operation.LeaseExpiresAt.After(time.Now()) {
		return rejectCommand(v1.CommandAckReasonLeaseRejected, nil)
	}
	reconcileCtx, cancel := context.WithDeadline(ctx, operation.LeaseExpiresAt)
	defer cancel()
	outcome := reconciler.ReconcileProviderReviewerAssignment(reconcileCtx, operation, providerCredential)
	observedAt := time.Now().UTC()
	if observedAt.After(operation.LeaseExpiresAt) {
		observedAt = operation.LeaseExpiresAt
		outcome = v1.ProviderReviewerAssignmentReconciliationResultOutcome{Kind: v1.ProviderReviewerAssignmentReconciliationRetryable, Code: v1.ProviderReviewerAssignmentReconciliationProviderUnavailable}
	}
	event := v1.ProviderReviewerAssignmentReconciliationResultEvent{Kind: v1.EventProviderReviewerAssignmentReconciliationResult, ProviderReviewerAssignmentReconciliationOperationAuthority: operation.ProviderReviewerAssignmentReconciliationOperationAuthority, CommandMessageID: commandMessageID, ObservedAt: observedAt, Outcome: outcome}
	if err := validation.ProviderReviewerAssignmentReconciliationResultEvent(event); err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	_, err := d.sendEventWithStableID(ctx, providerReviewerAssignmentReconciliationResultEventID(commandMessageID), event)
	return err
}

func providerReviewerAssignmentReconciliationResultEventID(commandMessageID string) string {
	digest := sha256.Sum256([]byte(commandMessageID))
	return "provider-reviewer-assignment-reconciliation-result-" + hex.EncodeToString(digest[:])
}

func (d *Daemon) completeProviderReviewerAssignmentReconciliationCommand(ctx context.Context, envelope v1.Envelope, command v1.CommandPayload, claimOwner string, effectMayHaveRun bool) {
	defer d.providerReviewerAssignmentReconciliationCommands.Done()
	defer d.providerReviewerAssignmentReconciliationCommandFinish(envelope.ID)
	if err := d.completeClaimedCommand(ctx, envelope, command, nil, claimOwner, effectMayHaveRun); err != nil {
		d.logger.Error("complete provider reviewer assignment reconciliation command", "id", envelope.ID, "err", err)
	}
}

func (d *Daemon) providerReviewerAssignmentReconciliationCommandIsActive(commandID string) bool {
	d.providerReviewerAssignmentReconciliationCommandMu.Lock()
	defer d.providerReviewerAssignmentReconciliationCommandMu.Unlock()
	_, active := d.providerReviewerAssignmentReconciliationCommandActive[commandID]
	return active
}
func (d *Daemon) providerReviewerAssignmentReconciliationCommandStart(commandID string) bool {
	d.providerReviewerAssignmentReconciliationCommandMu.Lock()
	defer d.providerReviewerAssignmentReconciliationCommandMu.Unlock()
	if d.providerReviewerAssignmentReconciliationCommandActive == nil {
		d.providerReviewerAssignmentReconciliationCommandActive = make(map[string]struct{})
	}
	if _, active := d.providerReviewerAssignmentReconciliationCommandActive[commandID]; active {
		return false
	}
	d.providerReviewerAssignmentReconciliationCommandActive[commandID] = struct{}{}
	return true
}
func (d *Daemon) providerReviewerAssignmentReconciliationCommandFinish(commandID string) {
	d.providerReviewerAssignmentReconciliationCommandMu.Lock()
	delete(d.providerReviewerAssignmentReconciliationCommandActive, commandID)
	d.providerReviewerAssignmentReconciliationCommandMu.Unlock()
}
