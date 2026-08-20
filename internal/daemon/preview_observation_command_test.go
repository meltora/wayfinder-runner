package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type fakePreviewObserver struct {
	outcome  v1.PreviewObservationResultOutcome
	profiles []v1.PreviewObservationProfileCapability
	calls    int
}

func (observer *fakePreviewObserver) ReadyProfiles() []v1.PreviewObservationProfileCapability {
	return append([]v1.PreviewObservationProfileCapability(nil), observer.profiles...)
}

func (observer *fakePreviewObserver) Observe(
	_ context.Context,
	_ v1.PreviewObservationOperation,
) v1.PreviewObservationResultOutcome {
	observer.calls++
	return observer.outcome
}

type blockingPreviewObserver struct {
	started chan struct{}
	release chan struct{}
	outcome v1.PreviewObservationResultOutcome
}

type deadlineIgnoringPreviewObserver struct {
	outcome v1.PreviewObservationResultOutcome
}

func (observer *deadlineIgnoringPreviewObserver) ReadyProfiles() []v1.PreviewObservationProfileCapability {
	return nil
}

func (observer *deadlineIgnoringPreviewObserver) Observe(
	ctx context.Context,
	_ v1.PreviewObservationOperation,
) v1.PreviewObservationResultOutcome {
	<-ctx.Done()
	return observer.outcome
}

func (observer *blockingPreviewObserver) ReadyProfiles() []v1.PreviewObservationProfileCapability {
	return []v1.PreviewObservationProfileCapability{{
		BlueprintID: "node-web-app", Version: "2026.08.20", Digest: strings.Repeat("a", 64),
		HealthCheckIDs: []string{"http-smoke"},
	}}
}

func (observer *blockingPreviewObserver) Observe(
	ctx context.Context,
	_ v1.PreviewObservationOperation,
) v1.PreviewObservationResultOutcome {
	close(observer.started)
	select {
	case <-observer.release:
		return observer.outcome
	case <-ctx.Done():
		return v1.PreviewObservationResultOutcome{
			Kind: v1.PreviewObservationRetryable, Code: v1.PreviewUnreachable,
		}
	}
}

func validPreviewObservationCommand() v1.CommandPayload {
	leaseExpiresAt := time.Now().Add(time.Minute).UTC()
	return v1.CommandPayload{
		Command: v1.CmdPreviewObservation,
		PreviewObservation: &v1.PreviewObservationOperation{
			PreviewObservationOperationAuthority: v1.PreviewObservationOperationAuthority{
				SchemaVersion:              v1.PreviewObservationOperationAuthorityVersion,
				PreviewObservationEffectID: "preview-effect-1", PreviewEvidenceID: "preview-evidence-1",
				ReviewReadyRequestID: "review-ready-1", ProjectID: "project-1", WorkflowRunID: "workflow-1",
				AttemptID: "attempt-1", ExecutionManifestID: "manifest-1",
				ExecutionManifestDigest: strings.Repeat("b", 64), LeaseID: "preview-lease-1",
				FencingToken: 13, LeaseExpiresAt: leaseExpiresAt,
			},
			Blueprint: v1.PreviewObservationBlueprint{
				BlueprintID: "node-web-app", Version: "2026.08.20", Digest: strings.Repeat("a", 64),
			},
			PreviewURL:       "https://branch.preview.example.com/review",
			PreviewExpiresAt: leaseExpiresAt.Add(time.Hour),
			CommitSet: []v1.PreviewObservationCommit{
				{RepositoryID: "web", CommitSHA: strings.Repeat("c", 40)},
			},
			RequiredHealthCheckIDs: []string{"http-smoke"},
		},
	}
}

func successfulPreviewOutcome(command v1.CommandPayload) v1.PreviewObservationResultOutcome {
	return v1.PreviewObservationResultOutcome{
		Kind: v1.PreviewObservationObserved,
		Observation: &v1.PreviewObservation{
			SchemaVersion: v1.PreviewObservationSchemaVersion,
			PreviewURL:    command.PreviewObservation.PreviewURL,
			CommitSet:     append([]v1.PreviewObservationCommit(nil), command.PreviewObservation.CommitSet...),
			Checks: []v1.PreviewObservationCheck{
				{CheckID: "http-smoke", Status: v1.PreviewObservationCheckPassed, HTTPStatus: 200, DurationMS: 42},
			},
		},
	}
}

func TestPreviewObservationCommandRemainsDisabledUntilUsableProfileExists(t *testing.T) {
	jr, _ := testJobRunner(t)
	err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: "preview-command-1"}, validPreviewObservationCommand())
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonUnsupportedCommand {
		t.Fatalf("reason = %q, want %q (err=%v)", reason, v1.CommandAckReasonUnsupportedCommand, err)
	}
}

func TestPreviewObservationCommandRejectsMalformedOrCrossCapabilityPayload(t *testing.T) {
	tests := map[string]func(v1.CommandPayload) v1.CommandPayload{
		"missing operation": func(command v1.CommandPayload) v1.CommandPayload {
			command.PreviewObservation = nil
			return command
		},
		"invalid operation": func(command v1.CommandPayload) v1.CommandPayload {
			command.PreviewObservation.FencingToken = 0
			return command
		},
		"job field smuggling": func(command v1.CommandPayload) v1.CommandPayload {
			command.JobID = "smuggled-job"
			return command
		},
		"preview payload under another command": func(command v1.CommandPayload) v1.CommandPayload {
			command.Command = v1.CmdResumeRunner
			return command
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			jr.d.previewObserver = &fakePreviewObserver{}
			err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: "preview-command-1"}, mutate(validPreviewObservationCommand()))
			if reason := commandRejectionReason(err); reason != v1.CommandAckReasonInvalidCommand {
				t.Fatalf("reason = %q, want %q (err=%v)", reason, v1.CommandAckReasonInvalidCommand, err)
			}
		})
	}
}

func TestPreviewObservationCommandPublishesOneDurableNormalizedResult(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewObservationCommand()
	observer := &fakePreviewObserver{outcome: successfulPreviewOutcome(command)}
	jr.d.previewObserver = observer
	var events []v1.PreviewObservationResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event, ok := payload.(v1.PreviewObservationResultEvent)
		if !ok {
			t.Fatalf("event type = %T", payload)
		}
		events = append(events, event)
		return nil
	}
	envelope := v1.Envelope{ID: "preview-command-1"}
	if err := jr.d.executeCommand(context.Background(), envelope, command); err != nil {
		t.Fatal(err)
	}
	if err := jr.d.executeCommand(context.Background(), envelope, command); err != nil {
		t.Fatal(err)
	}
	if observer.calls != 2 || len(events) != 1 {
		t.Fatalf("observer calls=%d events=%d", observer.calls, len(events))
	}
	if events[0].CommandMessageID != envelope.ID || events[0].PreviewObservationEffectID != command.PreviewObservation.PreviewObservationEffectID ||
		events[0].Outcome.Kind != v1.PreviewObservationObserved || events[0].ObservedAt.IsZero() {
		t.Fatalf("event = %+v", events[0])
	}
}

func TestPreviewObservationRejectsExpiredAuthorityBeforeNetworkCall(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewObservationCommand()
	command.PreviewObservation.LeaseExpiresAt = time.Now().Add(-time.Second)
	observer := &fakePreviewObserver{}
	jr.d.previewObserver = observer
	err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: "preview-command-1"}, command)
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonLeaseRejected || observer.calls != 0 {
		t.Fatalf("reason=%q calls=%d err=%v", reason, observer.calls, err)
	}
}

func TestPreviewObservationDoesNotBlockCommandReadLoop(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewObservationCommand()
	observer := &blockingPreviewObserver{
		started: make(chan struct{}), release: make(chan struct{}),
		outcome: successfulPreviewOutcome(command),
	}
	jr.d.previewObserver = observer
	var events int
	jr.d.eventSink = func(context.Context, any) error { events++; return nil }
	envelope := commandEnvelope("preview-command-async", command)
	returned := make(chan error, 1)
	go func() { returned <- jr.d.OnCommand(context.Background(), envelope) }()
	select {
	case <-observer.started:
	case <-time.After(time.Second):
		t.Fatal("observer did not start")
	}
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("observer blocked the command read loop")
	}
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatalf("duplicate in-flight command: %v", err)
	}
	if events != 0 {
		t.Fatal("result emitted before observation completed")
	}
	close(observer.release)
	jr.d.previewObservationCommands.Wait()
	if events != 1 {
		t.Fatalf("events = %d", events)
	}
}

func TestPreviewObservationDiscardsLateSuccessAfterLeaseDeadline(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewObservationCommand()
	command.PreviewObservation.LeaseExpiresAt = time.Now().Add(25 * time.Millisecond).UTC()
	observer := &deadlineIgnoringPreviewObserver{
		outcome: successfulPreviewOutcome(command),
	}
	jr.d.previewObserver = observer
	var events []v1.PreviewObservationResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event, ok := payload.(v1.PreviewObservationResultEvent)
		if !ok {
			t.Fatalf("event type = %T", payload)
		}
		events = append(events, event)
		return nil
	}
	if err := jr.d.executePreviewObservation(context.Background(), "preview-command-deadline", *command.PreviewObservation); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	if events[0].Outcome.Kind != v1.PreviewObservationRetryable ||
		events[0].Outcome.Code != v1.PreviewObserverUnavailable ||
		events[0].Outcome.Observation != nil ||
		events[0].ObservedAt.After(command.PreviewObservation.LeaseExpiresAt) {
		t.Fatalf("event = %+v", events[0])
	}
}
