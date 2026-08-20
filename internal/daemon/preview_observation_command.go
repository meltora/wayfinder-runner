package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

type previewObserver interface {
	ReadyProfiles() []v1.PreviewObservationProfileCapability
	Observe(context.Context, v1.PreviewObservationOperation) v1.PreviewObservationResultOutcome
}

func validatePreviewObservationCommandBoundary(command v1.CommandPayload) error {
	if command.Command != v1.CmdPreviewObservation {
		if command.PreviewObservation != nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return nil
	}
	if command.PreviewObservation == nil || command.SourcePublication != nil || command.Job != nil ||
		command.JobID != "" || command.AttemptID != "" || command.FencingToken != 0 ||
		!command.LeaseExpiresAt.IsZero() || command.Reason != "" || command.QuestionID != "" ||
		command.Decision != "" || command.DecidedBy != "" || command.UpdateID != "" ||
		command.DrainDeadline != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	if err := validation.PreviewObservationOperation(*command.PreviewObservation); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	return nil
}

func (d *Daemon) executePreviewObservation(
	ctx context.Context,
	commandMessageID string,
	operation v1.PreviewObservationOperation,
) error {
	if d.previewObserver == nil {
		return rejectCommand(v1.CommandAckReasonUnsupportedCommand, nil)
	}
	if commandMessageID == "" {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	if !operation.LeaseExpiresAt.After(time.Now()) {
		return rejectCommand(v1.CommandAckReasonLeaseRejected, nil)
	}
	observationCtx, cancel := context.WithDeadline(ctx, operation.LeaseExpiresAt)
	defer cancel()
	outcome := d.previewObserver.Observe(observationCtx, operation)
	observedAt := time.Now().UTC()
	if observedAt.After(operation.LeaseExpiresAt) {
		observedAt = operation.LeaseExpiresAt
		outcome = v1.PreviewObservationResultOutcome{
			Kind: v1.PreviewObservationRetryable,
			Code: v1.PreviewObserverUnavailable,
		}
	}
	event := v1.PreviewObservationResultEvent{
		Kind:                                 v1.EventPreviewObservationResult,
		PreviewObservationOperationAuthority: operation.PreviewObservationOperationAuthority,
		CommandMessageID:                     commandMessageID,
		ObservedAt:                           observedAt,
		Outcome:                              outcome,
	}
	if err := validation.PreviewObservationResultEvent(event); err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	_, err := d.sendEventWithStableID(ctx, previewObservationResultEventID(commandMessageID), event)
	return err
}

func previewObservationResultEventID(commandMessageID string) string {
	digest := sha256.Sum256([]byte(commandMessageID))
	return "preview-observation-result-" + hex.EncodeToString(digest[:])
}

func (d *Daemon) completePreviewObservationCommand(
	ctx context.Context,
	envelope v1.Envelope,
	command v1.CommandPayload,
	claimOwner string,
	effectMayHaveRun bool,
) {
	defer d.previewObservationCommands.Done()
	defer d.previewObservationCommandFinish(envelope.ID)
	if err := d.completeClaimedCommand(
		ctx,
		envelope,
		command,
		nil,
		claimOwner,
		effectMayHaveRun,
	); err != nil {
		d.logger.Error("complete preview observation command", "id", envelope.ID, "err", err)
	}
}

func (d *Daemon) previewObservationCommandIsActive(commandID string) bool {
	d.previewObservationCommandMu.Lock()
	defer d.previewObservationCommandMu.Unlock()
	_, active := d.previewObservationCommandActive[commandID]
	return active
}

func (d *Daemon) previewObservationCommandStart(commandID string) bool {
	d.previewObservationCommandMu.Lock()
	defer d.previewObservationCommandMu.Unlock()
	if d.previewObservationCommandActive == nil {
		d.previewObservationCommandActive = make(map[string]struct{})
	}
	if _, active := d.previewObservationCommandActive[commandID]; active {
		return false
	}
	d.previewObservationCommandActive[commandID] = struct{}{}
	return true
}

func (d *Daemon) previewObservationCommandFinish(commandID string) {
	d.previewObservationCommandMu.Lock()
	delete(d.previewObservationCommandActive, commandID)
	d.previewObservationCommandMu.Unlock()
}
