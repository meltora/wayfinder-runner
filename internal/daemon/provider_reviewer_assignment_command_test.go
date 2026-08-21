package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type fakeProviderReviewerAssigner struct {
	outcome v1.ProviderReviewerAssignmentResultOutcome
	calls   int
	delay   time.Duration
}

func (assigner *fakeProviderReviewerAssigner) ReadyProviderReviewerAssignmentInstallations() []v1.ProviderReviewerAssignmentInstallationCapability {
	return []v1.ProviderReviewerAssignmentInstallationCapability{{
		InstallationID: "installation-1", Provider: "github", ProviderInstanceKey: "github.com",
	}}
}

func (assigner *fakeProviderReviewerAssigner) AssignProviderReviewers(
	_ context.Context,
	_ v1.ProviderReviewerAssignmentOperation,
) v1.ProviderReviewerAssignmentResultOutcome {
	assigner.calls++
	if assigner.delay != 0 {
		time.Sleep(assigner.delay)
	}
	return assigner.outcome
}

func validProviderReviewerAssignmentCommand() v1.CommandPayload {
	proofExpiry := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Millisecond)
	verificationExpiry := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Millisecond)
	recipients := []v1.ProviderReviewerAssignmentRecipient{{
		ReviewerIdentityID: "reviewer-identity-1", UserID: "user-1", OrganizationMembershipID: "membership-1",
		IdentityStateRevision: 2, IdentityRevision: 3, ProviderAccountProofID: "provider-account-proof-1",
		ProviderAccountProofRevision: 4, ProviderAccountProofDigest: strings.Repeat("b", 64),
		ProviderAccountProofExpiresAt: proofExpiry, IdentityVerificationDigest: strings.Repeat("c", 64),
		IdentityVerificationExpiresAt: verificationExpiry, ProviderAccountID: "42",
		ProviderUsername: "octocat", AssignmentPrincipal: "octocat",
	}}
	return v1.CommandPayload{
		Command: v1.CmdProviderReviewerAssignment,
		ProviderReviewerAssignment: &v1.ProviderReviewerAssignmentOperation{
			ProviderReviewerAssignmentOperationAuthority: v1.ProviderReviewerAssignmentOperationAuthority{
				SchemaVersion:                         1,
				ProviderReviewerAssignmentOperationID: "provider-reviewer-assignment-operation-" + strings.Repeat("a", 64),
				OrganizationID:                        "organization-1", AssignmentPlanID: "assignment-plan-1",
				AssignmentPlanDigest: strings.Repeat("1", 64), SourcePublicationObservationID: "observation-1",
				DeliveryID: "delivery-1", RepositoryID: "repository-1", ProviderRepositoryID: "101",
				ExternalRequestID: "17", HeadSHA: strings.Repeat("2", 40), InstallationID: "installation-1",
				InstallationStateRevision: 5, InstallationAuthorityRevision: 6,
				InstallationAuthorityDigest: strings.Repeat("3", 64), Provider: "github",
				ProviderInstanceKey: "github.com", IdempotencyKey: "provider-reviewer-assignment-" + strings.Repeat("4", 64),
				RecipientSetDigest: v1.ProviderReviewerAssignmentRecipientSetDigest(recipients),
				OperationDigest:    strings.Repeat("5", 64), CommandDigest: strings.Repeat("6", 64),
				LeaseID: "assignment-lease-1", FencingToken: 7,
				LeaseExpiresAt: time.Now().Add(time.Minute).UTC().Truncate(time.Millisecond),
			},
			Recipients: recipients,
		},
	}
}

func TestProviderReviewerAssignmentRemainsDisabledWithoutExactInstallationCapability(t *testing.T) {
	jr, _ := testJobRunner(t)
	err := jr.d.executeCommand(context.Background(), v1.Envelope{}, validProviderReviewerAssignmentCommand())
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonUnsupportedCommand {
		t.Fatalf("reason = %q, want unsupported command (err=%v)", reason, err)
	}
}

func TestProviderReviewerAssignmentRejectsCrossCapabilityPayload(t *testing.T) {
	command := validProviderReviewerAssignmentCommand()
	command.SourcePublication = &v1.SourcePublicationOperation{}
	jr, _ := testJobRunner(t)
	err := jr.d.executeCommand(context.Background(), v1.Envelope{}, command)
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonInvalidCommand {
		t.Fatalf("reason = %q, want invalid command (err=%v)", reason, err)
	}
}

func TestProviderReviewerAssignmentEmitsStableNormalizedResult(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validProviderReviewerAssignmentCommand()
	assigner := &fakeProviderReviewerAssigner{outcome: v1.ProviderReviewerAssignmentResultOutcome{
		Kind: v1.ProviderReviewerAssignmentSucceeded,
		RequestedReviewers: []v1.ProviderReviewerAssignmentRequestedReviewer{{
			ReviewerIdentityID: "reviewer-identity-1", ProviderAccountID: "42", AssignmentPrincipal: "octocat",
		}},
	}}
	jr.d.providerReviewerAssigner = assigner
	var events []v1.ProviderReviewerAssignmentResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event, ok := payload.(v1.ProviderReviewerAssignmentResultEvent)
		if !ok {
			t.Fatalf("event type = %T", payload)
		}
		events = append(events, event)
		return nil
	}
	envelope := v1.Envelope{ID: "provider-reviewer-assignment-command-" + strings.Repeat("f", 64)}
	if err := jr.d.executeCommand(context.Background(), envelope, command); err != nil {
		t.Fatal(err)
	}
	if err := jr.d.executeCommand(context.Background(), envelope, command); err != nil {
		t.Fatal(err)
	}
	if assigner.calls != 2 || len(events) != 1 {
		t.Fatalf("assigner calls = %d, events = %d", assigner.calls, len(events))
	}
	if events[0].CommandMessageID != envelope.ID || events[0].Outcome.Kind != v1.ProviderReviewerAssignmentSucceeded ||
		events[0].ProviderReviewerAssignmentOperationID != command.ProviderReviewerAssignment.ProviderReviewerAssignmentOperationID {
		t.Fatalf("event = %+v", events[0])
	}
}

func TestProviderReviewerAssignmentRejectsInvalidCommandMessageIDBeforeProviderCall(t *testing.T) {
	jr, _ := testJobRunner(t)
	assigner := &fakeProviderReviewerAssigner{}
	jr.d.providerReviewerAssigner = assigner
	err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: "loose-command-id"}, validProviderReviewerAssignmentCommand())
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonInvalidCommand || assigner.calls != 0 {
		t.Fatalf("reason = %q calls = %d err=%v", reason, assigner.calls, err)
	}
}

func TestProviderReviewerAssignmentNeverRetriesMutationThatCrossesLeaseDeadline(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validProviderReviewerAssignmentCommand()
	command.ProviderReviewerAssignment.LeaseExpiresAt = time.Now().Add(20 * time.Millisecond)
	assigner := &fakeProviderReviewerAssigner{
		delay: 40 * time.Millisecond,
		outcome: v1.ProviderReviewerAssignmentResultOutcome{
			Kind: v1.ProviderReviewerAssignmentSucceeded,
			RequestedReviewers: []v1.ProviderReviewerAssignmentRequestedReviewer{{
				ReviewerIdentityID: "reviewer-identity-1", ProviderAccountID: "42", AssignmentPrincipal: "octocat",
			}},
		},
	}
	jr.d.providerReviewerAssigner = assigner
	var event v1.ProviderReviewerAssignmentResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event = payload.(v1.ProviderReviewerAssignmentResultEvent)
		return nil
	}
	err := jr.d.executeCommand(context.Background(), v1.Envelope{
		ID: "provider-reviewer-assignment-command-" + strings.Repeat("9", 64),
	}, command)
	if err != nil {
		t.Fatal(err)
	}
	if event.Outcome.Kind != v1.ProviderReviewerAssignmentUncertain ||
		event.Outcome.Code != v1.ProviderReviewerAssignmentDeliveryUncertain ||
		!event.ObservedAt.Equal(command.ProviderReviewerAssignment.LeaseExpiresAt) {
		t.Fatalf("lease-crossing mutation became retryable: %#v", event.Outcome)
	}
}

func TestProviderReviewerAssignmentCompletedCommandRedeliveryNeverReexecutesProvider(t *testing.T) {
	jr, _ := testJobRunner(t)
	assigner := &fakeProviderReviewerAssigner{outcome: v1.ProviderReviewerAssignmentResultOutcome{
		Kind: v1.ProviderReviewerAssignmentSucceeded,
		RequestedReviewers: []v1.ProviderReviewerAssignmentRequestedReviewer{{
			ReviewerIdentityID: "reviewer-identity-1", ProviderAccountID: "42", AssignmentPrincipal: "octocat",
		}},
	}}
	jr.d.providerReviewerAssigner = assigner
	events := 0
	jr.d.eventSink = func(context.Context, any) error {
		events++
		return nil
	}
	envelope := v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeCommand,
		ID:     "provider-reviewer-assignment-command-" + strings.Repeat("d", 64),
		SentAt: time.Now(), Payload: validProviderReviewerAssignmentCommand(),
	}
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	jr.d.providerReviewerAssignmentCommands.Wait()
	envelope.SentAt = time.Now()
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	jr.d.providerReviewerAssignmentCommands.Wait()
	if assigner.calls != 1 || events != 1 {
		t.Fatalf("durable redelivery reexecuted provider: calls=%d events=%d", assigner.calls, events)
	}
}
