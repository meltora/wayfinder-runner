package daemon

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type fakeProviderReviewFeedbackContentReader struct {
	calls   atomic.Int32
	delay   time.Duration
	outcome v1.ProviderReviewFeedbackContentResultOutcome
}

func (f *fakeProviderReviewFeedbackContentReader) ReadyProviderReviewFeedbackContentInstallations() []v1.ProviderReviewFeedbackContentInstallationCapability {
	return []v1.ProviderReviewFeedbackContentInstallationCapability{{InstallationID: "installation-1", Provider: "github", ProviderInstanceKey: "github.com"}}
}
func (f *fakeProviderReviewFeedbackContentReader) FetchProviderReviewFeedbackContent(context.Context, v1.ProviderReviewFeedbackContentOperation) v1.ProviderReviewFeedbackContentResultOutcome {
	f.calls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.outcome
}

func validProviderReviewFeedbackContentCommand() v1.CommandPayload {
	now := time.Now().UTC()
	lease := now.Add(30 * time.Minute)
	expires := lease.Add(time.Hour)
	op := v1.ProviderReviewFeedbackContentOperation{ProviderReviewFeedbackContentAuthority: v1.ProviderReviewFeedbackContentAuthority{SchemaVersion: 1, ProviderReviewFeedbackContentOperationID: "provider-review-feedback-content-operation-" + strings.Repeat("1", 64), OrganizationID: "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1", WorkflowStateRevision: 1, AssignmentDeliveryID: "delivery-1", AssignmentDeliveryStateRevision: 1, AssignmentObservationID: "observation-1", ObservedReviewerID: "observed-1", ProviderReviewObservationID: "review-observation-1", ProviderReviewObservationResultPayloadChecksum: strings.Repeat("2", 64), EvidenceObservedAt: now.Add(-time.Minute), Purpose: "review_feedback_continuation", ContentSource: "provider_review_event", ProviderReviewEventID: "7001", ProviderReviewEventRevision: 1, ReviewEventKind: v1.ProviderReviewObservationChangeRequest, ReviewState: v1.ProviderReviewObservationChangesRequested, ReviewEventOccurredAt: now.Add(-2 * time.Minute), ContentDigest: "938baec10b632617f4a4779cd8c576ac27dd676d399ba72c8fd180e90f490527", ContentByteCount: 25, ProviderAccountID: "42", UserID: "user-1", OrganizationMembershipID: "membership-1", OrganizationMembershipStateRevision: 1, CodeReviewAuthorityRevision: 1, CodeReviewAuthorityDigest: strings.Repeat("3", 64), ReviewerIdentityID: "identity-1", IdentityRevision: 1, IdentityVerificationID: "verification-1", IdentityVerificationDigest: strings.Repeat("4", 64), IdentityVerificationExpiresAt: expires, ProviderAccountProofID: "proof-1", ProviderAccountProofRevision: 1, ProviderAccountProofDigest: strings.Repeat("5", 64), ProviderAccountProofExpiresAt: expires.Add(time.Hour), RepositoryID: "repository-1", ProviderRepositoryID: "123", ExternalRequestID: "7", ExpectedHeadSHA: strings.Repeat("b", 40), InstallationID: "installation-1", InstallationAuthorityDigest: strings.Repeat("a", 64), Provider: "github", ProviderInstanceKey: "github.com", FeedbackContentRequestDigest: strings.Repeat("6", 64), IdempotencyKey: "provider-review-feedback-content-" + strings.Repeat("8", 64), OperationDigest: strings.Repeat("9", 64), CommandDigest: strings.Repeat("e", 64), LeaseID: "lease-1", FencingToken: 1, LeaseExpiresAt: lease}}
	return v1.CommandPayload{Command: v1.CmdProviderReviewFeedbackContent, ProviderReviewFeedbackContent: &op}
}
func successfulFeedbackContent(head string) v1.ProviderReviewFeedbackContentResultOutcome {
	return v1.ProviderReviewFeedbackContentResultOutcome{Kind: v1.ProviderReviewFeedbackContentSucceeded, RequestState: v1.ProviderReviewObservationRequestOpen, ObservedHeadSHA: head, ContentEncoding: "utf-8", ContentFormat: "plain_text", UnicodeNormalization: "NFC", LineEndings: "LF", ContentDigest: "938baec10b632617f4a4779cd8c576ac27dd676d399ba72c8fd180e90f490527", ContentByteCount: 25, Content: "Café\nFix the retry path."}
}

func TestProviderReviewFeedbackContentSeparateDurableLane(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validProviderReviewFeedbackContentCommand()
	id := "provider-review-feedback-content-command-" + strings.Repeat("c", 64)
	if reason := commandRejectionReason(jr.d.executeCommand(context.Background(), v1.Envelope{ID: id}, command)); reason != v1.CommandAckReasonUnsupportedCommand {
		t.Fatalf("reason=%s", reason)
	}
	fake := &fakeProviderReviewFeedbackContentReader{outcome: successfulFeedbackContent(command.ProviderReviewFeedbackContent.ExpectedHeadSHA)}
	jr.d.providerReviewFeedbackContentReader = fake
	events := atomic.Int32{}
	jr.d.eventSink = func(context.Context, any) error { events.Add(1); return nil }
	envelope := v1.Envelope{Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: id, SentAt: time.Now(), Payload: command}
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	jr.d.providerReviewFeedbackContentCommands.Wait()
	envelope.SentAt = time.Now()
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	jr.d.providerReviewFeedbackContentCommands.Wait()
	if fake.calls.Load() != 1 || events.Load() != 1 {
		t.Fatalf("calls=%d events=%d", fake.calls.Load(), events.Load())
	}
}

func TestProviderReviewFeedbackContentLeaseCrossingFailsClosed(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validProviderReviewFeedbackContentCommand()
	command.ProviderReviewFeedbackContent.LeaseExpiresAt = time.Now().Add(20 * time.Millisecond)
	command.ProviderReviewFeedbackContent.IdentityVerificationExpiresAt = time.Now().Add(time.Minute)
	command.ProviderReviewFeedbackContent.ProviderAccountProofExpiresAt = time.Now().Add(time.Hour)
	fake := &fakeProviderReviewFeedbackContentReader{delay: 40 * time.Millisecond, outcome: successfulFeedbackContent(command.ProviderReviewFeedbackContent.ExpectedHeadSHA)}
	jr.d.providerReviewFeedbackContentReader = fake
	var event v1.ProviderReviewFeedbackContentResultEvent
	jr.d.eventSink = func(_ context.Context, payload any) error {
		event = payload.(v1.ProviderReviewFeedbackContentResultEvent)
		return nil
	}
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: "provider-review-feedback-content-command-" + strings.Repeat("d", 64)}, command); err != nil {
		t.Fatal(err)
	}
	if event.Outcome.Kind != v1.ProviderReviewFeedbackContentRetryable || event.Outcome.Code != v1.ProviderReviewFeedbackContentProviderUnavailable || !event.ObservedAt.Equal(command.ProviderReviewFeedbackContent.LeaseExpiresAt) {
		t.Fatalf("event=%#v", event)
	}
}

func TestProviderReviewFeedbackContentRejectsCrossCapabilitySmuggling(t *testing.T) {
	command := validProviderReviewFeedbackContentCommand()
	command.ProviderReviewObservation = &v1.ProviderReviewObservationOperation{}
	if reason := commandRejectionReason(validateProviderReviewFeedbackContentCommandBoundary(command)); reason != v1.CommandAckReasonInvalidCommand {
		t.Fatalf("reason=%s", reason)
	}
}

func TestProviderReviewFeedbackContentAuthorityExpiryClassification(t *testing.T) {
	now := time.Now()
	command := validProviderReviewFeedbackContentCommand()
	op := *command.ProviderReviewFeedbackContent
	op.ProviderAccountProofExpiresAt = now
	if got := expiredProviderReviewFeedbackAuthority(op, now); got != v1.ProviderReviewFeedbackContentProviderAccountProofExpired {
		t.Fatalf("proof=%s", got)
	}
	op = *command.ProviderReviewFeedbackContent
	op.IdentityVerificationExpiresAt = now
	if got := expiredProviderReviewFeedbackAuthority(op, now); got != v1.ProviderReviewFeedbackContentReviewerIdentityExpired {
		t.Fatalf("identity=%s", got)
	}
	op = *command.ProviderReviewFeedbackContent
	op.CodeReviewAuthorityExpiresAt = &now
	if got := expiredProviderReviewFeedbackAuthority(op, now); got != v1.ProviderReviewFeedbackContentCodeReviewAuthorityChanged {
		t.Fatalf("authority=%s", got)
	}
	if got := expiredProviderReviewFeedbackAuthority(*command.ProviderReviewFeedbackContent, now); got != "" {
		t.Fatalf("fresh=%s", got)
	}
	if feedbackContentPermanent(v1.ProviderReviewFeedbackContentReviewEventNotFound).Kind != v1.ProviderReviewFeedbackContentPermanent {
		t.Fatal("permanent helper mismatch")
	}
	if validateProviderReviewFeedbackContentCommandBoundary(v1.CommandPayload{Command: v1.CmdStartJob}) != nil {
		t.Fatal("unrelated empty pointer rejected")
	}
	if reason := commandRejectionReason(validateProviderReviewFeedbackContentCommandBoundary(v1.CommandPayload{Command: v1.CmdStartJob, ProviderReviewFeedbackContent: command.ProviderReviewFeedbackContent})); reason != v1.CommandAckReasonInvalidCommand {
		t.Fatalf("smuggling=%s", reason)
	}
}
