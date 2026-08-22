package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validProviderReviewFeedbackContentOperation() v1.ProviderReviewFeedbackContentOperation {
	evidence := time.Date(2030, 8, 22, 12, 0, 0, 0, time.UTC)
	lease := evidence.Add(4 * time.Minute)
	expires := lease.Add(time.Hour)
	return v1.ProviderReviewFeedbackContentOperation{ProviderReviewFeedbackContentAuthority: v1.ProviderReviewFeedbackContentAuthority{
		SchemaVersion: 1, ProviderReviewFeedbackContentOperationID: "provider-review-feedback-content-operation-" + strings.Repeat("1", 64),
		OrganizationID: "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1", WorkflowStateRevision: 1,
		AssignmentDeliveryID: "delivery-1", AssignmentDeliveryStateRevision: 1, AssignmentObservationID: "observation-1", ObservedReviewerID: "observed-reviewer-1",
		ProviderReviewObservationID: "review-observation-1", ProviderReviewObservationResultPayloadChecksum: strings.Repeat("2", 64), EvidenceObservedAt: evidence,
		Purpose: "review_feedback_continuation", ContentSource: "provider_review_event", ProviderReviewEventID: "7001", ProviderReviewEventRevision: 1, ReviewEventKind: v1.ProviderReviewObservationChangeRequest, ReviewState: v1.ProviderReviewObservationChangesRequested,
		ReviewEventOccurredAt: evidence.Add(-time.Minute),
		ContentDigest:         "938baec10b632617f4a4779cd8c576ac27dd676d399ba72c8fd180e90f490527", ContentByteCount: 25, ProviderAccountID: "42",
		UserID: "user-1", OrganizationMembershipID: "membership-1", OrganizationMembershipStateRevision: 1, CodeReviewAuthorityRevision: 1, CodeReviewAuthorityDigest: strings.Repeat("3", 64), CodeReviewAuthorityExpiresAt: &expires,
		ReviewerIdentityID: "identity-1", IdentityStateRevision: 0, IdentityRevision: 1, IdentityVerificationID: "verification-1", IdentityVerificationDigest: strings.Repeat("4", 64), IdentityVerificationExpiresAt: expires,
		ProviderAccountProofID: "proof-1", ProviderAccountProofStateRevision: 0, ProviderAccountProofRevision: 1, ProviderAccountProofDigest: strings.Repeat("5", 64), ProviderAccountProofExpiresAt: expires.Add(time.Hour),
		RepositoryID: "repository-1", ProviderRepositoryID: "123", ExternalRequestID: "7", ExpectedHeadSHA: strings.Repeat("b", 40), InstallationID: "installation-1",
		InstallationStateRevision: 0, InstallationAuthorityRevision: 0, InstallationAuthorityDigest: strings.Repeat("a", 64), Provider: "github", ProviderInstanceKey: "github.com",
		FeedbackContentRequestDigest: strings.Repeat("6", 64), IdempotencyKey: "provider-review-feedback-content-" + strings.Repeat("8", 64), OperationDigest: strings.Repeat("9", 64), CommandDigest: strings.Repeat("e", 64), LeaseID: "lease-1", FencingToken: 1, LeaseExpiresAt: lease,
	}}
}

func TestProviderReviewFeedbackContentOperationAcceptsExactZeroStateRevisions(t *testing.T) {
	value := validProviderReviewFeedbackContentOperation()
	value.WorkflowStateRevision = 0
	value.OrganizationMembershipStateRevision = 0
	if err := ProviderReviewFeedbackContentOperation(value); err != nil {
		t.Fatal(err)
	}
}

func TestProviderReviewFeedbackContentOperationFailsClosed(t *testing.T) {
	valid := validProviderReviewFeedbackContentOperation()
	for name, mutate := range map[string]func(*v1.ProviderReviewFeedbackContentOperation){
		"loose account":   func(v *v1.ProviderReviewFeedbackContentOperation) { v.ProviderAccountID = "042" },
		"repository path": func(v *v1.ProviderReviewFeedbackContentOperation) { v.ProviderRepositoryPath = "owner/repo" },
		"lease": func(v *v1.ProviderReviewFeedbackContentOperation) {
			v.LeaseExpiresAt = v.IdentityVerificationExpiresAt.Add(time.Second)
		},
		"oversize": func(v *v1.ProviderReviewFeedbackContentOperation) {
			v.ContentByteCount = v1.ProviderReviewFeedbackContentMaxBytes + 1
		},
		"forged event": func(v *v1.ProviderReviewFeedbackContentOperation) {
			v.ProviderReviewEventID = "derived-" + strings.Repeat("f", 64)
		},
		"empty content": func(v *v1.ProviderReviewFeedbackContentOperation) { v.ContentByteCount = 0 },
		"wrong source":  func(v *v1.ProviderReviewFeedbackContentOperation) { v.ContentSource = "provider_comment" },
	} {
		t.Run(name, func(t *testing.T) {
			value := valid
			mutate(&value)
			if ProviderReviewFeedbackContentOperation(value) == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestProviderReviewFeedbackContentResultNormalizedText(t *testing.T) {
	op := validProviderReviewFeedbackContentOperation()
	e := v1.ProviderReviewFeedbackContentResultEvent{Kind: v1.EventProviderReviewFeedbackContentResult, ProviderReviewFeedbackContentAuthority: op.ProviderReviewFeedbackContentAuthority, CommandMessageID: "provider-review-feedback-content-command-" + strings.Repeat("c", 64), ObservedAt: op.EvidenceObservedAt.Add(time.Minute), Outcome: v1.ProviderReviewFeedbackContentResultOutcome{Kind: v1.ProviderReviewFeedbackContentSucceeded, RequestState: v1.ProviderReviewObservationRequestOpen, ObservedHeadSHA: op.ExpectedHeadSHA, ContentEncoding: "utf-8", ContentFormat: "plain_text", UnicodeNormalization: "NFC", LineEndings: "LF", ContentDigest: op.ContentDigest, ContentByteCount: op.ContentByteCount, Content: "Café\nFix the retry path."}}
	if err := ProviderReviewFeedbackContentResultEvent(e); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*v1.ProviderReviewFeedbackContentResultEvent){
		"decomposed": func(v *v1.ProviderReviewFeedbackContentResultEvent) {
			v.Outcome.Content = "Cafe\u0301\nFix the retry path."
		},
		"carriage return": func(v *v1.ProviderReviewFeedbackContentResultEvent) {
			v.Outcome.Content = "Café\r\nFix the retry path."
		},
		"head": func(v *v1.ProviderReviewFeedbackContentResultEvent) {
			v.Outcome.ObservedHeadSHA = strings.Repeat("a", 40)
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := e
			mutate(&value)
			if ProviderReviewFeedbackContentResultEvent(value) == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestProviderReviewFeedbackContentResultErrorShapesAndCommandID(t *testing.T) {
	op := validProviderReviewFeedbackContentOperation()
	base := v1.ProviderReviewFeedbackContentResultEvent{Kind: v1.EventProviderReviewFeedbackContentResult, ProviderReviewFeedbackContentAuthority: op.ProviderReviewFeedbackContentAuthority, CommandMessageID: "provider-review-feedback-content-command-" + strings.Repeat("c", 64), ObservedAt: op.EvidenceObservedAt.Add(time.Minute)}
	retry := base
	retry.Outcome = v1.ProviderReviewFeedbackContentResultOutcome{Kind: v1.ProviderReviewFeedbackContentRetryable, Code: v1.ProviderReviewFeedbackContentRateLimited}
	if err := ProviderReviewFeedbackContentResultEvent(retry); err != nil {
		t.Fatal(err)
	}
	permanent := base
	permanent.Outcome = v1.ProviderReviewFeedbackContentResultOutcome{Kind: v1.ProviderReviewFeedbackContentPermanent, Code: v1.ProviderReviewFeedbackContentReviewEventMetadataChanged}
	if err := ProviderReviewFeedbackContentResultEvent(permanent); err != nil {
		t.Fatal(err)
	}
	invalid := retry
	invalid.Outcome.Content = "secret"
	if ProviderReviewFeedbackContentResultEvent(invalid) == nil {
		t.Fatal("retryable content smuggling accepted")
	}
	if ProviderReviewFeedbackContentCommandMessageID(base.CommandMessageID) != nil || ProviderReviewFeedbackContentCommandMessageID("bad") == nil {
		t.Fatal("command ID validation mismatch")
	}
	if !providerReviewFeedbackContentPermanent(v1.ProviderReviewFeedbackContentReviewEventNotFound) || providerReviewFeedbackContentPermanent("unknown") {
		t.Fatal("permanent code classification mismatch")
	}
	if err := ProviderReviewFeedbackContentNormalizedText("Café\nFix the retry path.", op.ContentDigest, op.ContentByteCount); err != nil {
		t.Fatal(err)
	}
	tooEarly := permanent
	tooEarly.ObservedAt = op.EvidenceObservedAt.Add(-time.Second)
	if ProviderReviewFeedbackContentResultEvent(tooEarly) == nil {
		t.Fatal("result predating evidence accepted")
	}
	badKind := permanent
	badKind.Kind = "wrong"
	if ProviderReviewFeedbackContentResultEvent(badKind) == nil {
		t.Fatal("wrong event kind accepted")
	}
	badRetry := retry
	badRetry.Outcome.Code = "unknown"
	if ProviderReviewFeedbackContentResultEvent(badRetry) == nil {
		t.Fatal("unknown retry code accepted")
	}
	badPermanent := permanent
	badPermanent.Outcome.Code = "unknown"
	if ProviderReviewFeedbackContentResultEvent(badPermanent) == nil {
		t.Fatal("unknown permanent code accepted")
	}
}
