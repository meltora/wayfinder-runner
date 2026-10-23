package daemon

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type fakePreviewLifecycleController struct {
	profiles []v1.PreviewLifecycleProfileCapability
	outcome  v1.PreviewLifecycleResultOutcome
	mu       sync.Mutex
	calls    int
}

func (controller *fakePreviewLifecycleController) ReadyProfiles() []v1.PreviewLifecycleProfileCapability {
	return append([]v1.PreviewLifecycleProfileCapability(nil), controller.profiles...)
}

func (controller *fakePreviewLifecycleController) Apply(_ context.Context, _ v1.PreviewLifecycleOperation) v1.PreviewLifecycleResultOutcome {
	controller.mu.Lock()
	controller.calls++
	controller.mu.Unlock()
	return controller.outcome
}

func (controller *fakePreviewLifecycleController) callCount() int {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.calls
}

type blockingPreviewLifecycleController struct {
	*fakePreviewLifecycleController
	started chan struct{}
	release chan struct{}
}

func (controller *blockingPreviewLifecycleController) Apply(ctx context.Context, operation v1.PreviewLifecycleOperation) v1.PreviewLifecycleResultOutcome {
	controller.mu.Lock()
	controller.calls++
	controller.mu.Unlock()
	select {
	case controller.started <- struct{}{}:
	default:
	}
	select {
	case <-controller.release:
		return controller.outcome
	case <-ctx.Done():
		return v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecycleUncertain, Code: v1.PreviewLifecycleControllerTimeout}
	}
}

func validPreviewLifecycleCommand(action v1.PreviewLifecycleAction) v1.CommandPayload {
	now := time.Now().UTC()
	operation := v1.PreviewLifecycleOperation{
		PreviewLifecycleOperationAuthority: v1.PreviewLifecycleOperationAuthority{
			SchemaVersion:            v1.PreviewLifecycleSchemaVersion,
			PreviewLifecycleEffectID: "preview-lifecycle-effect-1", PreviewLifecycleEffectRevision: 1,
			PreviewLifecycleOperationID: "preview-lifecycle-operation-" + strings.Repeat("a", 64),
			OrganizationID:              "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1",
			PreviewEvidenceID: "preview-evidence-1", PreviewObservationID: "preview-observation-1",
			PreviewObservationDigest: strings.Repeat("b", 64), ExecutionManifestID: "manifest-1",
			ExecutionManifestDigest: strings.Repeat("c", 64), Action: action,
			IdempotencyKey: "preview-lifecycle-" + strings.Repeat("d", 64), LeaseID: "lease-1", FencingToken: 4,
			LeaseExpiresAt: now.Add(time.Minute),
		},
		Blueprint:         v1.PreviewObservationBlueprint{BlueprintID: "node-web", Version: "1", Digest: strings.Repeat("e", 64)},
		PreviewURL:        "https://branch.preview.example.test/",
		CommitSet:         []v1.PreviewObservationCommit{{RepositoryID: "web", CommitSHA: strings.Repeat("f", 40)}},
		OriginalExpiresAt: now.Add(time.Hour), CurrentEffectiveExpiresAt: now.Add(time.Hour),
	}
	if action == v1.PreviewLifecycleExtend {
		desired := now.Add(2 * time.Hour)
		operation.DesiredExpiresAt = &desired
	}
	operation.CommitSetDigest = v1.PreviewLifecycleCommitSetDigest(operation.CommitSet)
	operation.ResourceBindingDigest = v1.PreviewLifecycleResourceBindingDigest(operation)
	operation.OperationDigest = v1.PreviewLifecycleOperationDigest(operation)
	operation.CommandDigest = v1.PreviewLifecycleCommandDigest(operation)
	return v1.CommandPayload{Command: v1.CmdPreviewLifecycle, PreviewLifecycle: &operation}
}

func successfulPreviewLifecycleOutcome(operation v1.PreviewLifecycleOperation) v1.PreviewLifecycleResultOutcome {
	state := &v1.PreviewLifecycleResourceState{Kind: v1.PreviewLifecycleResourceDestroyed, Disposition: v1.PreviewLifecycleDestroyed}
	if operation.Action == v1.PreviewLifecycleExtend {
		blueprint := operation.Blueprint
		state = &v1.PreviewLifecycleResourceState{
			Kind: v1.PreviewLifecycleResourceActive, PreviewURL: operation.PreviewURL, Blueprint: &blueprint,
			CommitSet: append([]v1.PreviewObservationCommit(nil), operation.CommitSet...), ExpiresAt: operation.DesiredExpiresAt,
		}
	}
	return v1.PreviewLifecycleResultOutcome{
		Kind: v1.PreviewLifecycleSucceeded, ControllerReceiptDigest: strings.Repeat("1", 64),
		ResourceBindingDigest: operation.ResourceBindingDigest, ResourceState: state,
	}
}

func previewLifecycleCommandID() string {
	return "preview-lifecycle-command-" + strings.Repeat("2", 64)
}

func TestPreviewLifecycleCommandRequiresLocalExactCapability(t *testing.T) {
	jr, _ := testJobRunner(t)
	err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewLifecycleCommandID()}, validPreviewLifecycleCommand(v1.PreviewLifecycleDestroy))
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonUnsupportedCommand {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
}

func TestPreviewLifecycleCommandRejectsMalformedAndCrossCapabilityPayload(t *testing.T) {
	tests := map[string]func(v1.CommandPayload) v1.CommandPayload{
		"missing": func(command v1.CommandPayload) v1.CommandPayload { command.PreviewLifecycle = nil; return command },
		"digest": func(command v1.CommandPayload) v1.CommandPayload {
			command.PreviewLifecycle.CommandDigest = strings.Repeat("9", 64)
			return command
		},
		"job smuggling": func(command v1.CommandPayload) v1.CommandPayload { command.JobID = "job-1"; return command },
		"other capability": func(command v1.CommandPayload) v1.CommandPayload {
			command.PreviewObservation = &v1.PreviewObservationOperation{}
			return command
		},
		"under resume": func(command v1.CommandPayload) v1.CommandPayload {
			command.Command = v1.CmdResumeRunner
			return command
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			jr.d.previewLifecycleController = &fakePreviewLifecycleController{}
			err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewLifecycleCommandID()}, mutate(validPreviewLifecycleCommand(v1.PreviewLifecycleDestroy)))
			if reason := commandRejectionReason(err); reason != v1.CommandAckReasonInvalidCommand {
				t.Fatalf("reason=%q err=%v", reason, err)
			}
		})
	}
}

func TestPreviewLifecyclePublishesOneDurableNormalizedResult(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewLifecycleCommand(v1.PreviewLifecycleExtend)
	controller := &fakePreviewLifecycleController{outcome: successfulPreviewLifecycleOutcome(*command.PreviewLifecycle)}
	jr.d.previewLifecycleController = controller
	var events []v1.PreviewLifecycleResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event, ok := payload.(v1.PreviewLifecycleResultEvent)
		if !ok {
			t.Fatalf("event type=%T", payload)
		}
		events = append(events, event)
		return nil
	}
	envelope := v1.Envelope{ID: previewLifecycleCommandID()}
	if err := jr.d.executeCommand(context.Background(), envelope, command); err != nil {
		t.Fatal(err)
	}
	if err := jr.d.executeCommand(context.Background(), envelope, command); err != nil {
		t.Fatal(err)
	}
	if controller.callCount() != 2 || len(events) != 1 || events[0].Outcome.Kind != v1.PreviewLifecycleSucceeded ||
		events[0].PreviewLifecycleOperationID != command.PreviewLifecycle.PreviewLifecycleOperationID {
		t.Fatalf("calls=%d events=%+v", controller.callCount(), events)
	}
}

func TestPreviewLifecycleRejectsExpiredAuthorityBeforeControllerCall(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewLifecycleCommand(v1.PreviewLifecycleDestroy)
	command.PreviewLifecycle.LeaseExpiresAt = time.Now().Add(-time.Second)
	command.PreviewLifecycle.CommandDigest = v1.PreviewLifecycleCommandDigest(*command.PreviewLifecycle)
	controller := &fakePreviewLifecycleController{}
	jr.d.previewLifecycleController = controller
	err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewLifecycleCommandID()}, command)
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonLeaseRejected || controller.callCount() != 0 {
		t.Fatalf("reason=%q calls=%d err=%v", reason, controller.callCount(), err)
	}
}

func TestPreviewLifecycleNeverRevivesExpiredPreview(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewLifecycleCommand(v1.PreviewLifecycleExtend)
	command.PreviewLifecycle.CurrentEffectiveExpiresAt = time.Now().Add(-time.Second)
	command.PreviewLifecycle.OriginalExpiresAt = command.PreviewLifecycle.CurrentEffectiveExpiresAt.Add(-time.Hour)
	desired := time.Now().Add(time.Hour)
	command.PreviewLifecycle.DesiredExpiresAt = &desired
	command.PreviewLifecycle.ResourceBindingDigest = v1.PreviewLifecycleResourceBindingDigest(*command.PreviewLifecycle)
	command.PreviewLifecycle.OperationDigest = v1.PreviewLifecycleOperationDigest(*command.PreviewLifecycle)
	command.PreviewLifecycle.CommandDigest = v1.PreviewLifecycleCommandDigest(*command.PreviewLifecycle)
	controller := &fakePreviewLifecycleController{}
	jr.d.previewLifecycleController = controller
	err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewLifecycleCommandID()}, command)
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonLeaseRejected || controller.callCount() != 0 {
		t.Fatalf("reason=%q calls=%d err=%v", reason, controller.callCount(), err)
	}
}

func TestPreviewLifecycleUsesDedicatedNonBlockingLaneAndDeduplicatesInflight(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewLifecycleCommand(v1.PreviewLifecycleDestroy)
	base := &fakePreviewLifecycleController{outcome: successfulPreviewLifecycleOutcome(*command.PreviewLifecycle)}
	controller := &blockingPreviewLifecycleController{fakePreviewLifecycleController: base, started: make(chan struct{}, 1), release: make(chan struct{})}
	jr.d.previewLifecycleController = controller
	jr.d.eventSink = func(context.Context, any) error { return nil }
	envelope := commandEnvelope(previewLifecycleCommandID(), command)
	returned := make(chan error, 1)
	go func() { returned <- jr.d.OnCommand(context.Background(), envelope) }()
	select {
	case <-controller.started:
	case <-time.After(time.Second):
		t.Fatal("controller did not start")
	}
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("controller blocked command loop")
	}
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if controller.callCount() != 1 {
		t.Fatalf("duplicate calls=%d", controller.callCount())
	}
	close(controller.release)
	jr.d.previewLifecycleCommands.Wait()
}

func TestPreviewLifecycleCrashRedeliveryReplaysSameIdempotentEffect(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.commandOwner = "preview-lifecycle-process-new"
	command := validPreviewLifecycleCommand(v1.PreviewLifecycleDestroy)
	controller := &fakePreviewLifecycleController{outcome: successfulPreviewLifecycleOutcome(*command.PreviewLifecycle)}
	d.previewLifecycleController = controller
	var events int
	d.eventSink = func(_ context.Context, payload any) error {
		if _, ok := payload.(v1.PreviewLifecycleResultEvent); !ok {
			t.Fatalf("event type=%T", payload)
		}
		events++
		return nil
	}
	envelope := commandEnvelope(previewLifecycleCommandID(), command)
	persistUnexecutedCommand(t, d, envelope, "preview-lifecycle-process-old")
	if err := d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	d.previewLifecycleCommands.Wait()
	if controller.callCount() != 1 || events != 1 {
		t.Fatalf("calls=%d events=%d", controller.callCount(), events)
	}
	receipt := commandReceipt(t, d, envelope)
	if receipt.AckStatus != string(v1.CommandAckAccepted) {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestPreviewLifecycleResultObservedAtHasControlPlaneMillisecondPrecision(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewLifecycleCommand(v1.PreviewLifecycleDestroy)
	jr.d.previewLifecycleController = &fakePreviewLifecycleController{outcome: successfulPreviewLifecycleOutcome(*command.PreviewLifecycle)}
	var event v1.PreviewLifecycleResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event = payload.(v1.PreviewLifecycleResultEvent)
		return nil
	}
	before := time.Now().UTC().Truncate(time.Millisecond)
	if err := jr.d.executePreviewLifecycle(context.Background(), previewLifecycleCommandID(), *command.PreviewLifecycle); err != nil {
		t.Fatal(err)
	}
	if event.ObservedAt.IsZero() || event.ObservedAt.Nanosecond()%int(time.Millisecond) != 0 ||
		event.ObservedAt.Before(before) || event.ObservedAt.Location() != time.UTC {
		t.Fatalf("observed_at=%s is not a UTC millisecond instant at or after %s", event.ObservedAt.Format(time.RFC3339Nano), before)
	}
}

func TestPreviewLifecycleLateSuccessBecomesUncertain(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewLifecycleCommand(v1.PreviewLifecycleDestroy)
	command.PreviewLifecycle.LeaseExpiresAt = time.Now().Add(20 * time.Millisecond).UTC()
	command.PreviewLifecycle.CommandDigest = v1.PreviewLifecycleCommandDigest(*command.PreviewLifecycle)
	controller := &blockingPreviewLifecycleController{
		fakePreviewLifecycleController: &fakePreviewLifecycleController{outcome: successfulPreviewLifecycleOutcome(*command.PreviewLifecycle)},
		started:                        make(chan struct{}, 1), release: make(chan struct{}),
	}
	jr.d.previewLifecycleController = controller
	var event v1.PreviewLifecycleResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event = payload.(v1.PreviewLifecycleResultEvent)
		return nil
	}
	go func() { <-controller.started; time.Sleep(30 * time.Millisecond); close(controller.release) }()
	if err := jr.d.executePreviewLifecycle(context.Background(), previewLifecycleCommandID(), *command.PreviewLifecycle); err != nil {
		t.Fatal(err)
	}
	if event.Outcome.Kind != v1.PreviewLifecycleUncertain || event.Outcome.Code != v1.PreviewLifecycleControllerTimeout {
		t.Fatalf("late result=%+v", event)
	}
}

func TestPausedRunnerRejectsAllPreviewLifecycleMutation(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.d.paused.Store(true)
	controller := &fakePreviewLifecycleController{}
	jr.d.previewLifecycleController = controller
	for _, action := range []v1.PreviewLifecycleAction{v1.PreviewLifecycleDestroy, v1.PreviewLifecycleExtend} {
		err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewLifecycleCommandID()}, validPreviewLifecycleCommand(action))
		if reason := commandRejectionReason(err); reason != v1.CommandAckReasonRunnerPaused {
			t.Fatalf("%s reason=%q err=%v", action, reason, err)
		}
	}
	if controller.callCount() != 0 {
		t.Fatal("paused runner called preview lifecycle controller")
	}
}
