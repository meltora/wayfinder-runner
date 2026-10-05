package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type fakeSourcePublicationPublisher struct {
	outcome    v1.SourcePublicationResultOutcome
	providers  []string
	calls      int
	credential *v1.ProviderCredential
}

type blockingSourcePublicationPublisher struct {
	started chan struct{}
	release chan struct{}
	outcome v1.SourcePublicationResultOutcome
}

func (publisher *blockingSourcePublicationPublisher) ReadyProviders() []string {
	return []string{"github"}
}

func (publisher *blockingSourcePublicationPublisher) Publish(
	ctx context.Context,
	_ v1.SourcePublicationOperation,
	_ *v1.ProviderCredential,
) v1.SourcePublicationResultOutcome {
	close(publisher.started)
	select {
	case <-publisher.release:
		return publisher.outcome
	case <-ctx.Done():
		return v1.SourcePublicationResultOutcome{
			Kind: v1.SourcePublicationRetryable, Code: v1.SourceControlProviderUnavailable,
		}
	}
}

func (publisher *fakeSourcePublicationPublisher) ReadyProviders() []string {
	return append([]string(nil), publisher.providers...)
}

func (publisher *fakeSourcePublicationPublisher) Publish(
	_ context.Context,
	_ v1.SourcePublicationOperation,
	credential *v1.ProviderCredential,
) v1.SourcePublicationResultOutcome {
	publisher.calls++
	publisher.credential = credential
	return publisher.outcome
}

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

func TestSourcePublicationCommandPublishesOneDurableNormalizedResult(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validSourcePublicationCommand()
	command.SourcePublication.LeaseExpiresAt = time.Now().Add(time.Minute)
	publisher := &fakeSourcePublicationPublisher{
		providers: []string{"github"},
		outcome: v1.SourcePublicationResultOutcome{
			Kind: v1.SourcePublicationSucceeded,
			Result: &v1.SourcePublicationResult{
				SchemaVersion: v1.SourcePublicationSchemaVersion, Provider: "github", InstallationID: "installation-1",
				RepositoryID: "repository-1", RepositoryExternalID: "octo/example", BaseBranch: "main",
				HeadBranch: "job/workflow-1/attempt-1", HeadSHA: strings.Repeat("b", 40),
				IdempotencyKey: "source-publish-" + strings.Repeat("c", 64), ExternalRequestID: "42",
				URL: "https://github.example/octo/example/pull/42", State: "open",
			},
		},
	}
	jr.d.sourcePublicationPublisher = publisher
	var events []v1.SourcePublicationResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event, ok := payload.(v1.SourcePublicationResultEvent)
		if !ok {
			t.Fatalf("event type = %T", payload)
		}
		events = append(events, event)
		return nil
	}
	envelope := v1.Envelope{ID: "source-publication-command-1"}
	if err := jr.d.executeCommand(context.Background(), envelope, command); err != nil {
		t.Fatal(err)
	}
	if err := jr.d.executeCommand(context.Background(), envelope, command); err != nil {
		t.Fatal(err)
	}
	if publisher.calls != 2 {
		t.Fatalf("publisher calls = %d", publisher.calls)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	event := events[0]
	if event.CommandMessageID != envelope.ID || event.SourcePublicationEffectID != command.SourcePublication.SourcePublicationEffectID ||
		event.Outcome.Kind != v1.SourcePublicationSucceeded || event.ObservedAt.IsZero() {
		t.Fatalf("event = %+v", event)
	}
}

func TestSourcePublicationCommandRejectsExpiredAuthorityBeforeProviderCall(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validSourcePublicationCommand()
	command.SourcePublication.LeaseExpiresAt = time.Now().Add(-time.Second)
	publisher := &fakeSourcePublicationPublisher{providers: []string{"github"}}
	jr.d.sourcePublicationPublisher = publisher
	err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: "source-publication-command-1"}, command)
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonLeaseRejected {
		t.Fatalf("reason = %q, want %q (err=%v)", reason, v1.CommandAckReasonLeaseRejected, err)
	}
	if publisher.calls != 0 {
		t.Fatalf("expired authority reached provider %d times", publisher.calls)
	}
}

func TestSourcePublicationProviderCallDoesNotBlockCommandReadLoop(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validSourcePublicationCommand()
	command.SourcePublication.LeaseExpiresAt = time.Now().Add(time.Minute)
	publisher := &blockingSourcePublicationPublisher{
		started: make(chan struct{}),
		release: make(chan struct{}),
		outcome: v1.SourcePublicationResultOutcome{
			Kind: v1.SourcePublicationPermanent, Code: v1.SourceControlPermissionDenied,
		},
	}
	jr.d.sourcePublicationPublisher = publisher
	var events int
	jr.d.eventSink = func(context.Context, any) error {
		events++
		return nil
	}
	envelope := v1.Envelope{
		Version: v1.ProtocolVersion,
		Type:    v1.TypeCommand,
		ID:      "source-publication-command-async",
		SentAt:  time.Now(),
		Payload: command,
	}
	returned := make(chan error, 1)
	go func() { returned <- jr.d.OnCommand(context.Background(), envelope) }()
	select {
	case <-publisher.started:
	case <-time.After(time.Second):
		t.Fatal("provider call did not start")
	}
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("provider call blocked the command read loop")
	}
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatalf("duplicate in-flight command: %v", err)
	}
	if events != 0 {
		t.Fatal("result event emitted before the provider completed")
	}
	close(publisher.release)
	jr.d.sourcePublicationCommands.Wait()
	if events != 1 {
		t.Fatalf("events = %d", events)
	}
}
