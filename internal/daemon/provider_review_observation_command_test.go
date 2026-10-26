package daemon

import (
	"context"
	v1 "github.com/example/app-runner/api/runner/v1"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeProviderReviewObserver struct {
	calls      atomic.Int32
	delay      time.Duration
	outcome    v1.ProviderReviewObservationResultOutcome
	credential atomic.Pointer[v1.ProviderCredential]
}

func (f *fakeProviderReviewObserver) ReadyProviderReviewObservationInstallations() []v1.ProviderReviewObservationInstallationCapability {
	return []v1.ProviderReviewObservationInstallationCapability{{InstallationID: "installation-1", Provider: "github", ProviderInstanceKey: "github.com"}}
}
func (f *fakeProviderReviewObserver) ObserveProviderReviews(_ context.Context, _ v1.ProviderReviewObservationOperation, credential *v1.ProviderCredential) v1.ProviderReviewObservationResultOutcome {
	f.calls.Add(1)
	f.credential.Store(credential)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.outcome
}
func validProviderReviewObservationCommand() v1.CommandPayload {
	now := time.Now().UTC()
	subjects := []v1.ProviderReviewObservationSubject{{ReviewerIdentityID: "identity-1", UserID: "user-1", OrganizationMembershipID: "membership-1", IdentityStateRevision: 1, IdentityRevision: 1, ProviderAccountProofID: "proof-1", ProviderAccountProofStateRevision: 1, ProviderAccountProofRevision: 1, ProviderAccountProofDigest: strings.Repeat("1", 64), ProviderAccountProofExpiresAt: now.Add(2 * time.Hour), IdentityVerificationID: "verification-1", IdentityVerificationDigest: strings.Repeat("2", 64), IdentityVerificationExpiresAt: now.Add(time.Hour), ProviderAccountID: "42"}}
	return v1.CommandPayload{Command: v1.CmdProviderReviewObservation, ProviderReviewObservation: &v1.ProviderReviewObservationOperation{ProviderReviewObservationAuthority: v1.ProviderReviewObservationAuthority{SchemaVersion: 1, ProviderReviewObservationOperationID: "provider-review-observation-operation-" + strings.Repeat("a", 64), OrganizationID: "organization-1", ProjectID: "project-1", WorkflowRunID: "run-1", AssignmentDeliveryID: "delivery-1", AssignmentDeliveryStateRevision: 1, AssignmentPlanID: "plan-1", AssignmentPlanDigest: strings.Repeat("3", 64), AssignmentObservationID: "observation-1", AssignmentResultPayloadChecksum: strings.Repeat("4", 64), SourcePublicationObservationID: "source-observation-1", RepositoryID: "repository-1", ProviderRepositoryID: "123", ExternalRequestID: "7", ExpectedHeadSHA: "0123456789abcdef0123456789abcdef01234567", InstallationID: "installation-1", InstallationStateRevision: 1, InstallationAuthorityRevision: 1, InstallationAuthorityDigest: strings.Repeat("5", 64), Provider: "github", ProviderInstanceKey: "github.com", IdempotencyKey: "provider-review-observation-" + strings.Repeat("6", 64), SubjectIdentitySetDigest: v1.ProviderReviewObservationSubjectIdentitySetDigest(subjects), MaxCollectionPages: 10, CollectionPageSize: 100, OperationDigest: strings.Repeat("7", 64), CommandDigest: strings.Repeat("8", 64), LeaseID: "lease-1", FencingToken: 1, LeaseExpiresAt: now.Add(30 * time.Minute)}, Subjects: subjects}}
}
func successfulProviderReviewObservationOutcome(head string) v1.ProviderReviewObservationResultOutcome {
	return v1.ProviderReviewObservationResultOutcome{Kind: v1.ProviderReviewObservationSucceeded, RequestState: v1.ProviderReviewObservationRequestOpen, ObservedHeadSHA: head, SnapshotSemantics: "current_state", SnapshotComplete: true, Collections: &v1.ProviderReviewObservationCollections{ReviewEvents: v1.ProviderReviewObservationCollectionCompleteness{Complete: true, PagesRead: 1}, Comments: v1.ProviderReviewObservationCollectionCompleteness{Complete: true, PagesRead: 1}}}
}
func TestProviderReviewObservationSeparateCapabilityAndNormalizedEvent(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validProviderReviewObservationCommand()
	id := "provider-review-observation-command-" + strings.Repeat("9", 64)
	if reason := commandRejectionReason(jr.d.executeCommand(context.Background(), v1.Envelope{ID: id}, command)); reason != v1.CommandAckReasonUnsupportedCommand {
		t.Fatalf("reason=%s", reason)
	}
	fake := &fakeProviderReviewObserver{outcome: successfulProviderReviewObservationOutcome(command.ProviderReviewObservation.ExpectedHeadSHA)}
	jr.d.providerReviewObserver = fake
	var event v1.ProviderReviewObservationResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event = payload.(v1.ProviderReviewObservationResultEvent)
		return nil
	}
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: id}, command); err != nil {
		t.Fatal(err)
	}
	if fake.calls.Load() != 1 || event.CommandMessageID != id || event.Outcome.SnapshotSemantics != "current_state" {
		t.Fatalf("event=%#v calls=%d", event, fake.calls.Load())
	}
	command.ProviderReviewerAssignment = &v1.ProviderReviewerAssignmentOperation{}
	if reason := commandRejectionReason(jr.d.executeCommand(context.Background(), v1.Envelope{ID: id}, command)); reason != v1.CommandAckReasonInvalidCommand {
		t.Fatalf("smuggling reason=%s", reason)
	}
}

func TestProviderReviewObservationCompletionRevalidationEmitsExactMergeEvidence(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validProviderReviewObservationCommand()
	command.ProviderReviewObservation.SchemaVersion = v1.ProviderReviewObservationCompletionVersion
	command.ProviderReviewObservation.Purpose = v1.ProviderReviewObservationCompletionRevalidation
	command.ProviderReviewObservation.ExpectedTargetBranch = "main"
	mergedAt := time.Now().UTC().Add(-2 * time.Minute)
	updatedAt := mergedAt.Add(time.Minute)
	fake := &fakeProviderReviewObserver{outcome: v1.ProviderReviewObservationResultOutcome{
		Kind: v1.ProviderReviewObservationSucceeded, RequestState: v1.ProviderReviewObservationRequestMerged,
		ObservedHeadSHA: command.ProviderReviewObservation.ExpectedHeadSHA, SnapshotSemantics: "current_state", SnapshotComplete: true,
		Collections: &v1.ProviderReviewObservationCollections{
			ReviewEvents: v1.ProviderReviewObservationCollectionCompleteness{Complete: true, PagesRead: 1},
			Comments:     v1.ProviderReviewObservationCollectionCompleteness{Complete: true, PagesRead: 1},
		},
		MergeCommitSHA: strings.Repeat("a", 40), TargetBranch: "main", TargetHeadSHA: strings.Repeat("b", 40),
		MergeCommitReachableFromTarget: true, MergedAt: mergedAt, ProviderUpdatedAt: updatedAt,
		MergedByProviderAccountID: "42",
	}}
	jr.d.providerReviewObserver = fake
	var event v1.ProviderReviewObservationResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event = payload.(v1.ProviderReviewObservationResultEvent)
		return nil
	}
	id := "provider-review-observation-command-" + strings.Repeat("d", 64)
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: id}, command); err != nil {
		t.Fatal(err)
	}
	if event.SchemaVersion != v1.ProviderReviewObservationCompletionVersion ||
		event.Purpose != v1.ProviderReviewObservationCompletionRevalidation ||
		event.Outcome.MergeCommitSHA != strings.Repeat("a", 40) ||
		!event.Outcome.MergeCommitReachableFromTarget || event.Outcome.MergedByProviderAccountID != "42" {
		t.Fatalf("event=%#v", event)
	}
}
func TestProviderReviewObservationLeaseCrossingBecomesRetryable(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validProviderReviewObservationCommand()
	command.ProviderReviewObservation.LeaseExpiresAt = time.Now().Add(20 * time.Millisecond)
	command.ProviderReviewObservation.Subjects[0].IdentityVerificationExpiresAt = time.Now().Add(time.Minute)
	command.ProviderReviewObservation.Subjects[0].ProviderAccountProofExpiresAt = time.Now().Add(time.Minute)
	command.ProviderReviewObservation.SubjectIdentitySetDigest = v1.ProviderReviewObservationSubjectIdentitySetDigest(command.ProviderReviewObservation.Subjects)
	fake := &fakeProviderReviewObserver{delay: 40 * time.Millisecond, outcome: successfulProviderReviewObservationOutcome(command.ProviderReviewObservation.ExpectedHeadSHA)}
	jr.d.providerReviewObserver = fake
	var event v1.ProviderReviewObservationResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event = payload.(v1.ProviderReviewObservationResultEvent)
		return nil
	}
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: "provider-review-observation-command-" + strings.Repeat("b", 64)}, command); err != nil {
		t.Fatal(err)
	}
	if event.Outcome.Kind != v1.ProviderReviewObservationRetryable || event.Outcome.Code != v1.ProviderReviewObservationProviderUnavailable || !event.ObservedAt.Equal(command.ProviderReviewObservation.LeaseExpiresAt) {
		t.Fatalf("event=%#v", event)
	}
}
func TestProviderReviewObservationResultObservedAtHasControlPlaneMillisecondPrecision(t *testing.T) {
	for name, completion := range map[string]bool{"review_cycle": false, "completion_revalidation": true} {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			command := validProviderReviewObservationCommand()
			outcome := successfulProviderReviewObservationOutcome(command.ProviderReviewObservation.ExpectedHeadSHA)
			if completion {
				command.ProviderReviewObservation.SchemaVersion = v1.ProviderReviewObservationCompletionVersion
				command.ProviderReviewObservation.Purpose = v1.ProviderReviewObservationCompletionRevalidation
				command.ProviderReviewObservation.ExpectedTargetBranch = "main"
				outcome.RequestState = v1.ProviderReviewObservationRequestMerged
				outcome.MergeCommitSHA, outcome.TargetBranch, outcome.TargetHeadSHA = strings.Repeat("a", 40), "main", strings.Repeat("b", 40)
				outcome.MergeCommitReachableFromTarget, outcome.MergedByProviderAccountID = true, "42"
				outcome.MergedAt = time.Now().UTC().Add(-2 * time.Minute)
				outcome.ProviderUpdatedAt = outcome.MergedAt.Add(time.Minute)
			}
			jr.d.providerReviewObserver = &fakeProviderReviewObserver{outcome: outcome}
			var event v1.ProviderReviewObservationResultEvent
			jr.d.eventSink = func(_ context.Context, payload any) error {
				event = payload.(v1.ProviderReviewObservationResultEvent)
				return nil
			}
			before := time.Now().UTC().Truncate(time.Millisecond)
			if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: "provider-review-observation-command-" + strings.Repeat("e", 64)}, command); err != nil {
				t.Fatal(err)
			}
			if event.Kind != v1.EventProviderReviewObservationResult || event.ObservedAt.IsZero() ||
				event.ObservedAt.Nanosecond()%int(time.Millisecond) != 0 || event.ObservedAt.Before(before) ||
				event.ObservedAt.Location() != time.UTC {
				t.Fatalf("observed_at=%s kind=%s is not a UTC millisecond instant at or after %s", event.ObservedAt.Format(time.RFC3339Nano), event.Kind, before)
			}
		})
	}
}

func TestProviderReviewObservationDurableDuplicateReplaysWithoutRefetch(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validProviderReviewObservationCommand()
	fake := &fakeProviderReviewObserver{outcome: successfulProviderReviewObservationOutcome(command.ProviderReviewObservation.ExpectedHeadSHA)}
	jr.d.providerReviewObserver = fake
	events := atomic.Int32{}
	jr.d.eventSink = func(context.Context, any) error { events.Add(1); return nil }
	envelope := v1.Envelope{Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: "provider-review-observation-command-" + strings.Repeat("c", 64), SentAt: time.Now(), Payload: command}
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	jr.d.providerReviewObservationCommands.Wait()
	envelope.SentAt = time.Now()
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	jr.d.providerReviewObservationCommands.Wait()
	if fake.calls.Load() != 1 || events.Load() != 1 {
		t.Fatalf("calls=%d events=%d", fake.calls.Load(), events.Load())
	}
}

func TestProviderReviewObservationActiveLaneAndSubjectExpiry(t *testing.T) {
	jr, _ := testJobRunner(t)
	if !jr.d.providerReviewObservationCommandStart("command-1") || jr.d.providerReviewObservationCommandStart("command-1") || !jr.d.providerReviewObservationCommandIsActive("command-1") {
		t.Fatal("active command admission is not exclusive")
	}
	jr.d.providerReviewObservationCommandFinish("command-1")
	if jr.d.providerReviewObservationCommandIsActive("command-1") {
		t.Fatal("finished command remains active")
	}

	now := time.Now()
	command := validProviderReviewObservationCommand()
	command.ProviderReviewObservation.Subjects[0].ProviderAccountProofExpiresAt = now
	if code := expiredReviewObservationSubject(*command.ProviderReviewObservation, now); code != v1.ProviderReviewObservationProviderAccountProofExpired {
		t.Fatalf("proof code=%s", code)
	}
	command.ProviderReviewObservation.Subjects[0].ProviderAccountProofExpiresAt = now.Add(time.Hour)
	command.ProviderReviewObservation.Subjects[0].IdentityVerificationExpiresAt = now
	if code := expiredReviewObservationSubject(*command.ProviderReviewObservation, now); code != v1.ProviderReviewObservationSubjectIdentityExpired {
		t.Fatalf("identity code=%s", code)
	}
}
