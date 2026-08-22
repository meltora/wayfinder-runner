package daemon

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type fakeProviderReviewerAssignmentReconciler struct {
	outcome v1.ProviderReviewerAssignmentReconciliationResultOutcome
	calls   atomic.Int32
	delay   time.Duration
}

func (f *fakeProviderReviewerAssignmentReconciler) ReadyProviderReviewerAssignmentReconciliationInstallations() []v1.ProviderReviewerAssignmentReconciliationInstallationCapability {
	return []v1.ProviderReviewerAssignmentReconciliationInstallationCapability{{InstallationID: "installation-1", Provider: "github", ProviderInstanceKey: "github.com"}}
}
func (f *fakeProviderReviewerAssignmentReconciler) ReconcileProviderReviewerAssignment(context.Context, v1.ProviderReviewerAssignmentReconciliationOperation) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
	f.calls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.outcome
}

func validProviderReviewerAssignmentReconciliationCommand() v1.CommandPayload {
	reviewers := []v1.ProviderReviewerAssignmentReconciliationIntendedReviewer{{ReviewerIdentityID: "identity-1", ProviderAccountID: "42"}}
	return v1.CommandPayload{Command: v1.CmdProviderReviewerAssignmentReconciliation, ProviderReviewerAssignmentReconciliation: &v1.ProviderReviewerAssignmentReconciliationOperation{ProviderReviewerAssignmentReconciliationOperationAuthority: v1.ProviderReviewerAssignmentReconciliationOperationAuthority{SchemaVersion: 1, ProviderReviewerAssignmentReconciliationOperationID: "provider-reviewer-assignment-reconciliation-operation-" + strings.Repeat("1", 64), OrganizationID: "organization-1", ProjectID: "project-1", WorkflowRunID: "run-1", AssignmentPlanID: "plan-1", AssignmentPlanDigest: strings.Repeat("2", 64), SourcePublicationObservationID: "observation-1", DeliveryID: "delivery-1", AssignmentOperationID: "provider-reviewer-assignment-operation-" + strings.Repeat("3", 64), AssignmentOperationDigest: strings.Repeat("4", 64), AssignmentCommandDigest: strings.Repeat("5", 64), AssignmentObservationID: "assignment-observation-1", AssignmentResultDeliveryID: "result-delivery-1", AssignmentResultPayloadChecksum: strings.Repeat("6", 64), RepositoryID: "repository-1", ProviderRepositoryID: "123", ExternalRequestID: "7", ExpectedHeadSHA: "0123456789abcdef0123456789abcdef01234567", InstallationID: "installation-1", InstallationStateRevision: 1, InstallationAuthorityRevision: 1, InstallationAuthorityDigest: strings.Repeat("7", 64), Provider: "github", ProviderInstanceKey: "github.com", IdempotencyKey: "provider-reviewer-assignment-reconciliation-" + strings.Repeat("8", 64), AssignmentRecipientSetDigest: strings.Repeat("9", 64), IntendedReviewerSetDigest: v1.ProviderReviewerAssignmentReconciliationIntendedReviewerSetDigest(reviewers), OperationDigest: strings.Repeat("a", 64), CommandDigest: strings.Repeat("b", 64), LeaseID: "lease-1", FencingToken: 1, LeaseExpiresAt: time.Now().Add(time.Minute).UTC().Truncate(time.Millisecond)}, IntendedReviewers: reviewers}}
}

func successfulReconciliationOutcome() v1.ProviderReviewerAssignmentReconciliationResultOutcome {
	return v1.ProviderReviewerAssignmentReconciliationResultOutcome{Kind: v1.ProviderReviewerAssignmentReconciliationSucceeded, Conclusion: v1.ProviderReviewerAssignmentReconciliationSatisfied, RequestState: v1.ProviderReviewerAssignmentReconciliationRequestOpen, ObservedHeadSHA: "0123456789abcdef0123456789abcdef01234567", IntendedReviewers: []v1.ProviderReviewerAssignmentReconciliationObservedReviewer{{ReviewerIdentityID: "identity-1", ProviderAccountID: "42", Presence: v1.ProviderReviewerAssignmentReconciliationReviewed}}}
}

func TestProviderReviewerAssignmentReconciliationRequiresSeparateCapabilityAndRejectsSmuggling(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validProviderReviewerAssignmentReconciliationCommand()
	err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: "provider-reviewer-assignment-reconciliation-command-" + strings.Repeat("c", 64)}, command)
	if commandRejectionReason(err) != v1.CommandAckReasonUnsupportedCommand {
		t.Fatalf("err=%v", err)
	}
	command.ProviderReviewerAssignment = &v1.ProviderReviewerAssignmentOperation{}
	jr.d.providerReviewerAssignmentReconciler = &fakeProviderReviewerAssignmentReconciler{}
	err = jr.d.executeCommand(context.Background(), v1.Envelope{ID: "provider-reviewer-assignment-reconciliation-command-" + strings.Repeat("c", 64)}, command)
	if commandRejectionReason(err) != v1.CommandAckReasonInvalidCommand {
		t.Fatalf("smuggling err=%v", err)
	}
}

func TestProviderReviewerAssignmentReconciliationEmitsNormalizedStableResult(t *testing.T) {
	jr, _ := testJobRunner(t)
	fake := &fakeProviderReviewerAssignmentReconciler{outcome: successfulReconciliationOutcome()}
	jr.d.providerReviewerAssignmentReconciler = fake
	var event v1.ProviderReviewerAssignmentReconciliationResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event = payload.(v1.ProviderReviewerAssignmentReconciliationResultEvent)
		return nil
	}
	command := validProviderReviewerAssignmentReconciliationCommand()
	id := "provider-reviewer-assignment-reconciliation-command-" + strings.Repeat("d", 64)
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: id}, command); err != nil {
		t.Fatal(err)
	}
	if fake.calls.Load() != 1 || event.CommandMessageID != id || event.Outcome.Conclusion != v1.ProviderReviewerAssignmentReconciliationSatisfied {
		t.Fatalf("calls=%d event=%#v", fake.calls.Load(), event)
	}
}

func TestProviderReviewerAssignmentReconciliationLeaseCrossingIsRetryableRead(t *testing.T) {
	jr, _ := testJobRunner(t)
	fake := &fakeProviderReviewerAssignmentReconciler{outcome: successfulReconciliationOutcome(), delay: 40 * time.Millisecond}
	jr.d.providerReviewerAssignmentReconciler = fake
	command := validProviderReviewerAssignmentReconciliationCommand()
	command.ProviderReviewerAssignmentReconciliation.LeaseExpiresAt = time.Now().Add(20 * time.Millisecond).UTC()
	var event v1.ProviderReviewerAssignmentReconciliationResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event = payload.(v1.ProviderReviewerAssignmentReconciliationResultEvent)
		return nil
	}
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: "provider-reviewer-assignment-reconciliation-command-" + strings.Repeat("e", 64)}, command); err != nil {
		t.Fatal(err)
	}
	if event.Outcome.Kind != v1.ProviderReviewerAssignmentReconciliationRetryable || event.Outcome.Code != v1.ProviderReviewerAssignmentReconciliationProviderUnavailable || !event.ObservedAt.Equal(command.ProviderReviewerAssignmentReconciliation.LeaseExpiresAt) {
		t.Fatalf("event=%#v", event)
	}
}

func TestProviderReviewerAssignmentReconciliationDurableDuplicateNeverReexecutesCompletedRead(t *testing.T) {
	jr, _ := testJobRunner(t)
	fake := &fakeProviderReviewerAssignmentReconciler{outcome: successfulReconciliationOutcome()}
	jr.d.providerReviewerAssignmentReconciler = fake
	events := atomic.Int32{}
	jr.d.eventSink = func(context.Context, any) error { events.Add(1); return nil }
	envelope := v1.Envelope{Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: "provider-reviewer-assignment-reconciliation-command-" + strings.Repeat("f", 64), SentAt: time.Now(), Payload: validProviderReviewerAssignmentReconciliationCommand()}
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	jr.d.providerReviewerAssignmentReconciliationCommands.Wait()
	envelope.SentAt = time.Now()
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	jr.d.providerReviewerAssignmentReconciliationCommands.Wait()
	if fake.calls.Load() != 1 || events.Load() != 1 {
		t.Fatalf("calls=%d events=%d", fake.calls.Load(), events.Load())
	}
}

func TestProviderReviewerAssignmentReconciliationActiveLaneSerializesDuplicateCommand(t *testing.T) {
	jr, _ := testJobRunner(t)
	const id = "duplicate-command"
	if !jr.d.providerReviewerAssignmentReconciliationCommandStart(id) {
		t.Fatal("first claim rejected")
	}
	if jr.d.providerReviewerAssignmentReconciliationCommandStart(id) {
		t.Fatal("duplicate active claim accepted")
	}
	if !jr.d.providerReviewerAssignmentReconciliationCommandIsActive(id) {
		t.Fatal("active command not visible")
	}
	jr.d.providerReviewerAssignmentReconciliationCommandFinish(id)
	if jr.d.providerReviewerAssignmentReconciliationCommandIsActive(id) {
		t.Fatal("finished command remained active")
	}
}
