package daemon

import (
	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

// validateSourcePublicationCommandBoundary prevents a command from smuggling
// source-control authority across another capability boundary. A valid source
// publication command is still unsupported until the runner has a local
// provider adapter and advertises the matching capability.
func validateSourcePublicationCommandBoundary(command v1.CommandPayload) error {
	if command.Command != v1.CmdSourcePublication {
		if command.SourcePublication != nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return nil
	}

	if command.SourcePublication == nil || command.Job != nil || command.JobID != "" ||
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
