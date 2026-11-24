package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

type previewLifecycleController interface {
	ReadyProfiles() []v1.PreviewLifecycleProfileCapability
	Apply(context.Context, v1.PreviewLifecycleOperation) v1.PreviewLifecycleResultOutcome
}

func validatePreviewLifecycleCommandBoundary(command v1.CommandPayload) error {
	if command.Command != v1.CmdPreviewLifecycle {
		if command.PreviewLifecycle != nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return nil
	}
	if command.PreviewLifecycle == nil || command.SourcePublication != nil || command.ProviderReviewerAssignment != nil ||
		command.ProviderReviewerAssignmentReconciliation != nil || command.ProviderReviewObservation != nil ||
		command.ProviderReviewFeedbackContent != nil || command.ReviewFeedbackContinuation != nil ||
		command.PreviewObservation != nil || command.PreviewDeploy != nil ||
		command.ReviewerIdentityVerification != nil || command.Job != nil ||
		command.JobID != "" || command.AttemptID != "" || command.FencingToken != 0 || !command.LeaseExpiresAt.IsZero() ||
		command.Reason != "" || command.QuestionID != "" || command.Decision != "" || command.DecidedBy != "" ||
		command.UpdateID != "" || command.DrainDeadline != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	validate := validation.PreviewLifecycleOperation
	if command.PreviewLifecycle.Hosted() {
		validate = validation.PreviewHostedLifecycleOperation
	}
	if err := validate(*command.PreviewLifecycle); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	return nil
}

func (d *Daemon) executePreviewLifecycle(
	ctx context.Context,
	commandMessageID string,
	operation v1.PreviewLifecycleOperation,
) error {
	if operation.Hosted() {
		return d.executeHostedPreviewLifecycle(ctx, commandMessageID, operation)
	}
	if d.previewLifecycleController == nil {
		return rejectCommand(v1.CommandAckReasonUnsupportedCommand, nil)
	}
	if err := validation.PreviewLifecycleCommandMessageID(commandMessageID); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	now := time.Now()
	if !operation.LeaseExpiresAt.After(now) ||
		(operation.Action == v1.PreviewLifecycleExtend && !operation.CurrentEffectiveExpiresAt.After(now)) {
		return rejectCommand(v1.CommandAckReasonLeaseRejected, nil)
	}
	lifecycleCtx, cancel := context.WithDeadline(ctx, operation.LeaseExpiresAt)
	outcome := d.previewLifecycleController.Apply(lifecycleCtx, operation)
	cancel()
	observedAt := time.Now().UTC().Truncate(time.Millisecond)
	if observedAt.After(operation.LeaseExpiresAt) {
		observedAt = operation.LeaseExpiresAt
		outcome = v1.PreviewLifecycleResultOutcome{
			Kind: v1.PreviewLifecycleUncertain, Code: v1.PreviewLifecycleControllerTimeout,
		}
	}
	event := v1.PreviewLifecycleResultEvent{
		Kind:                            v1.EventPreviewLifecycleResult,
		PreviewLifecycleResultAuthority: v1.PreviewLifecycleResultAuthorityFromOperation(operation),
		CommandMessageID:                commandMessageID,
		ObservedAt:                      observedAt,
		Outcome:                         outcome,
	}
	if err := validation.PreviewLifecycleResultEvent(event); err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	_, err := d.sendEventWithStableID(ctx, previewLifecycleResultEventID(commandMessageID), event)
	return err
}

func previewLifecycleResultEventID(commandMessageID string) string {
	digest := sha256.Sum256([]byte(commandMessageID))
	return "preview-lifecycle-result-" + hex.EncodeToString(digest[:])
}

func (d *Daemon) completePreviewLifecycleCommand(
	ctx context.Context,
	envelope v1.Envelope,
	command v1.CommandPayload,
	claimOwner string,
	effectMayHaveRun bool,
) {
	defer d.previewLifecycleCommands.Done()
	defer d.previewLifecycleCommandFinish(envelope.ID)
	if err := d.completeClaimedCommand(ctx, envelope, command, nil, claimOwner, effectMayHaveRun); err != nil {
		d.logger.Error("complete preview lifecycle command", "id", envelope.ID, "err", err)
	}
}

func (d *Daemon) previewLifecycleCommandIsActive(commandID string) bool {
	d.previewLifecycleCommandMu.Lock()
	defer d.previewLifecycleCommandMu.Unlock()
	_, active := d.previewLifecycleCommandActive[commandID]
	return active
}

func (d *Daemon) previewLifecycleCommandStart(commandID string) bool {
	d.previewLifecycleCommandMu.Lock()
	defer d.previewLifecycleCommandMu.Unlock()
	if d.previewLifecycleCommandActive == nil {
		d.previewLifecycleCommandActive = make(map[string]struct{})
	}
	if _, active := d.previewLifecycleCommandActive[commandID]; active {
		return false
	}
	d.previewLifecycleCommandActive[commandID] = struct{}{}
	return true
}

func (d *Daemon) previewLifecycleCommandFinish(commandID string) {
	d.previewLifecycleCommandMu.Lock()
	delete(d.previewLifecycleCommandActive, commandID)
	d.previewLifecycleCommandMu.Unlock()
}
