package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

type sourcePublicationPublisher interface {
	ReadyProviders() []string
	Publish(context.Context, v1.SourcePublicationOperation) v1.SourcePublicationResultOutcome
}

// validateSourcePublicationCommandBoundary prevents a command from smuggling
// source-control authority across another capability boundary. A valid source
// publication command is accepted only when the runner has a local provider
// adapter and advertises the matching capability.
func validateSourcePublicationCommandBoundary(command v1.CommandPayload) error {
	if command.Command != v1.CmdSourcePublication {
		if command.SourcePublication != nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return nil
	}

	if command.SourcePublication == nil || command.PreviewObservation != nil || command.Job != nil || command.JobID != "" ||
		command.AttemptID != "" || command.FencingToken != 0 || !command.LeaseExpiresAt.IsZero() ||
		command.Reason != "" || command.QuestionID != "" || command.Decision != "" ||
		command.DecidedBy != "" || command.UpdateID != "" || command.DrainDeadline != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}

	if err := validation.SourcePublicationOperation(*command.SourcePublication); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	return nil
}

func (d *Daemon) executeSourcePublication(
	ctx context.Context,
	commandMessageID string,
	operation v1.SourcePublicationOperation,
) error {
	if d.sourcePublicationPublisher == nil {
		return rejectCommand(v1.CommandAckReasonUnsupportedCommand, nil)
	}
	if commandMessageID == "" {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	if !operation.LeaseExpiresAt.After(time.Now()) {
		return rejectCommand(v1.CommandAckReasonLeaseRejected, nil)
	}
	publicationCtx, cancel := context.WithDeadline(ctx, operation.LeaseExpiresAt)
	defer cancel()
	outcome := d.sourcePublicationPublisher.Publish(publicationCtx, operation)
	event := v1.SourcePublicationResultEvent{
		Kind:                                v1.EventSourcePublicationResult,
		SourcePublicationOperationAuthority: operation.SourcePublicationOperationAuthority,
		CommandMessageID:                    commandMessageID,
		ObservedAt:                          time.Now().UTC(),
		Outcome:                             outcome,
	}
	if err := validation.SourcePublicationResultEvent(event); err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	_, err := d.sendEventWithStableID(ctx, sourcePublicationResultEventID(commandMessageID), event)
	return err
}

func sourcePublicationResultEventID(commandMessageID string) string {
	digest := sha256.Sum256([]byte(commandMessageID))
	return "source-publication-result-" + hex.EncodeToString(digest[:])
}
