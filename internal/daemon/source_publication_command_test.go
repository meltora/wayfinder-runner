package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validSourcePublicationCommand() v1.CommandPayload {
	return v1.CommandPayload{
		Command: v1.CmdSourcePublication,
		SourcePublication: &v1.SourcePublicationOperation{
			SourcePublicationOperationAuthority: v1.SourcePublicationOperationAuthority{
				SchemaVersion:             v1.SourcePublicationOperationAuthorityVersion,
				SourcePublicationEffectID: "publication-effect-1",
				ReviewReadyRequestID:      "review-ready-1",
				ProjectID:                 "project-1",
				WorkflowRunID:             "workflow-1",
				AttemptID:                 "attempt-1",
				ExecutionManifestID:       "manifest-1",
				ExecutionManifestDigest:   strings.Repeat("a", 64),
				LeaseID:                   "publication-lease-1",
				FencingToken:              12,
				LeaseExpiresAt:            time.Date(2026, 8, 20, 16, 30, 0, 0, time.UTC),
			},
			Publication: v1.SourcePublicationCommand{
				SchemaVersion:        1,
				Provider:             "github",
				InstallationID:       "installation-1",
				RepositoryID:         "repository-1",
				RepositoryExternalID: "octo/example",
				BaseBranch:           "main",
				HeadBranch:           "job/workflow-1/attempt-1",
				HeadSHA:              strings.Repeat("b", 40),
				IdempotencyKey:       "source-publish-" + strings.Repeat("c", 64),
				Title:                "Implement ABC-123",
			},
		},
	}
}

func TestSourcePublicationCommandRemainsDisabledUntilCapabilityExists(t *testing.T) {
	jr, _ := testJobRunner(t)
	err := jr.d.executeCommand(context.Background(), v1.Envelope{}, validSourcePublicationCommand())
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonUnsupportedCommand {
		t.Fatalf("reason = %q, want %q (err=%v)", reason, v1.CommandAckReasonUnsupportedCommand, err)
	}
}

func TestSourcePublicationCommandRejectsMalformedOrCrossCapabilityPayload(t *testing.T) {
	tests := map[string]func(v1.CommandPayload) v1.CommandPayload{
		"missing operation": func(command v1.CommandPayload) v1.CommandPayload {
			command.SourcePublication = nil
			return command
		},
		"invalid operation": func(command v1.CommandPayload) v1.CommandPayload {
			command.SourcePublication.FencingToken = 0
			return command
		},
		"job field smuggling": func(command v1.CommandPayload) v1.CommandPayload {
			command.JobID = "smuggled-job"
			return command
		},
		"source payload under another command": func(command v1.CommandPayload) v1.CommandPayload {
			command.Command = v1.CmdResumeRunner
			return command
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			command := mutate(validSourcePublicationCommand())
			err := jr.d.executeCommand(context.Background(), v1.Envelope{}, command)
			if reason := commandRejectionReason(err); reason != v1.CommandAckReasonInvalidCommand {
				t.Fatalf("reason = %q, want %q (err=%v)", reason, v1.CommandAckReasonInvalidCommand, err)
			}
		})
	}
}
