package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validProviderReviewerAssignmentReconciliationOperation() v1.ProviderReviewerAssignmentReconciliationOperation {
	reviewers := []v1.ProviderReviewerAssignmentReconciliationIntendedReviewer{
		{ReviewerIdentityID: "reviewer-identity-a", ProviderAccountID: "42"},
		{ReviewerIdentityID: "reviewer-identity-b", ProviderAccountID: "99"},
	}
	return v1.ProviderReviewerAssignmentReconciliationOperation{
		ProviderReviewerAssignmentReconciliationOperationAuthority: v1.ProviderReviewerAssignmentReconciliationOperationAuthority{
			SchemaVersion: 1, ProviderReviewerAssignmentReconciliationOperationID: "provider-reviewer-assignment-reconciliation-operation-" + strings.Repeat("1", 64),
			OrganizationID: "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-run-1", AssignmentPlanID: "assignment-plan-1",
			AssignmentPlanDigest: strings.Repeat("2", 64), SourcePublicationObservationID: "source-observation-1", DeliveryID: "delivery-1",
			AssignmentOperationID: "provider-reviewer-assignment-operation-" + strings.Repeat("3", 64), AssignmentOperationDigest: strings.Repeat("4", 64),
			AssignmentCommandDigest: strings.Repeat("5", 64), AssignmentObservationID: "assignment-observation-1", AssignmentResultDeliveryID: "result-delivery-1",
			AssignmentResultPayloadChecksum: strings.Repeat("6", 64), RepositoryID: "repository-1", ProviderRepositoryID: "12345", ExternalRequestID: "17",
			ExpectedHeadSHA: strings.Repeat("7", 40), InstallationID: "installation-1", InstallationStateRevision: 8, InstallationAuthorityRevision: 9,
			InstallationAuthorityDigest: strings.Repeat("8", 64), Provider: "github", ProviderInstanceKey: "github.com",
			IdempotencyKey: "provider-reviewer-assignment-reconciliation-" + strings.Repeat("9", 64), AssignmentRecipientSetDigest: strings.Repeat("a", 64),
			IntendedReviewerSetDigest: v1.ProviderReviewerAssignmentReconciliationIntendedReviewerSetDigest(reviewers), OperationDigest: strings.Repeat("b", 64),
			CommandDigest: strings.Repeat("c", 64), LeaseID: "lease-1", FencingToken: 12, LeaseExpiresAt: time.Now().Add(time.Hour).UTC(),
		},
		IntendedReviewers: reviewers,
	}
}

func TestProviderReviewerAssignmentReconciliationOperationValidation(t *testing.T) {
	valid := validProviderReviewerAssignmentReconciliationOperation()
	if err := ProviderReviewerAssignmentReconciliationOperation(valid); err != nil {
		t.Fatalf("valid operation: %v", err)
	}

	tests := map[string]func(*v1.ProviderReviewerAssignmentReconciliationOperation){
		"wrong digest": func(v *v1.ProviderReviewerAssignmentReconciliationOperation) {
			v.IntendedReviewerSetDigest = strings.Repeat("f", 64)
		},
		"unsorted reviewers": func(v *v1.ProviderReviewerAssignmentReconciliationOperation) {
			v.IntendedReviewers[0], v.IntendedReviewers[1] = v.IntendedReviewers[1], v.IntendedReviewers[0]
		},
		"duplicate identity": func(v *v1.ProviderReviewerAssignmentReconciliationOperation) {
			v.IntendedReviewers[1].ReviewerIdentityID = v.IntendedReviewers[0].ReviewerIdentityID
			v.IntendedReviewerSetDigest = v1.ProviderReviewerAssignmentReconciliationIntendedReviewerSetDigest(v.IntendedReviewers)
		},
		"noncanonical account": func(v *v1.ProviderReviewerAssignmentReconciliationOperation) {
			v.IntendedReviewers[0].ProviderAccountID = "042"
			v.IntendedReviewerSetDigest = v1.ProviderReviewerAssignmentReconciliationIntendedReviewerSetDigest(v.IntendedReviewers)
		},
		"noncanonical instance":      func(v *v1.ProviderReviewerAssignmentReconciliationOperation) { v.ProviderInstanceKey = "GitHub.com" },
		"github path":                func(v *v1.ProviderReviewerAssignmentReconciliationOperation) { v.ProviderRepositoryPath = "owner/repo" },
		"unsafe revision":            func(v *v1.ProviderReviewerAssignmentReconciliationOperation) { v.InstallationStateRevision = 1 << 53 },
		"missing authority id":       func(v *v1.ProviderReviewerAssignmentReconciliationOperation) { v.ProjectID = "" },
		"malformed authority digest": func(v *v1.ProviderReviewerAssignmentReconciliationOperation) { v.AssignmentPlanDigest = "not-a-digest" },
		"noncanonical request id":    func(v *v1.ProviderReviewerAssignmentReconciliationOperation) { v.ExternalRequestID = "017" },
		"malformed expected head":    func(v *v1.ProviderReviewerAssignmentReconciliationOperation) { v.ExpectedHeadSHA = "main" },
		"loose idempotency":          func(v *v1.ProviderReviewerAssignmentReconciliationOperation) { v.IdempotencyKey = "reconcile-1" },
		"missing fencing token":      func(v *v1.ProviderReviewerAssignmentReconciliationOperation) { v.FencingToken = 0 },
		"missing lease expiry":       func(v *v1.ProviderReviewerAssignmentReconciliationOperation) { v.LeaseExpiresAt = time.Time{} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := valid
			value.IntendedReviewers = append([]v1.ProviderReviewerAssignmentReconciliationIntendedReviewer(nil), valid.IntendedReviewers...)
			mutate(&value)
			if err := ProviderReviewerAssignmentReconciliationOperation(value); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestProviderReviewerAssignmentReconciliationAcceptsCanonicalBitbucketAuthority(t *testing.T) {
	value := validProviderReviewerAssignmentReconciliationOperation()
	value.Provider = "bitbucket"
	value.ProviderInstanceKey = "bitbucket.org"
	value.ProviderRepositoryID = "{123e4567-e89b-42d3-a456-426614174000}"
	value.ProviderRepositoryPath = "meltora/wayfinder"
	value.IntendedReviewers = []v1.ProviderReviewerAssignmentReconciliationIntendedReviewer{{ReviewerIdentityID: "reviewer-identity-a", ProviderAccountID: "{223e4567-e89b-42d3-a456-426614174000}"}}
	value.IntendedReviewerSetDigest = v1.ProviderReviewerAssignmentReconciliationIntendedReviewerSetDigest(value.IntendedReviewers)
	if err := ProviderReviewerAssignmentReconciliationOperation(value); err != nil {
		t.Fatalf("canonical bitbucket rejected: %v", err)
	}
}

func TestProviderReviewerAssignmentReconciliationResultValidation(t *testing.T) {
	op := validProviderReviewerAssignmentReconciliationOperation()
	event := v1.ProviderReviewerAssignmentReconciliationResultEvent{
		Kind: v1.EventProviderReviewerAssignmentReconciliationResult,
		ProviderReviewerAssignmentReconciliationOperationAuthority: op.ProviderReviewerAssignmentReconciliationOperationAuthority,
		CommandMessageID: "provider-reviewer-assignment-reconciliation-command-" + strings.Repeat("d", 64), ObservedAt: op.LeaseExpiresAt,
		Outcome: v1.ProviderReviewerAssignmentReconciliationResultOutcome{Kind: v1.ProviderReviewerAssignmentReconciliationSucceeded, Conclusion: v1.ProviderReviewerAssignmentReconciliationSatisfied, RequestState: v1.ProviderReviewerAssignmentReconciliationRequestOpen, ObservedHeadSHA: op.ExpectedHeadSHA,
			IntendedReviewers: []v1.ProviderReviewerAssignmentReconciliationObservedReviewer{{ReviewerIdentityID: "reviewer-identity-a", ProviderAccountID: "42", Presence: v1.ProviderReviewerAssignmentReconciliationReviewed}, {ReviewerIdentityID: "reviewer-identity-b", ProviderAccountID: "99", Presence: v1.ProviderReviewerAssignmentReconciliationRequested}}},
	}
	if err := ProviderReviewerAssignmentReconciliationResultEvent(event); err != nil {
		t.Fatalf("valid event: %v", err)
	}
	invalid := event
	invalid.Outcome.Conclusion = v1.ProviderReviewerAssignmentReconciliationAbsent
	if err := ProviderReviewerAssignmentReconciliationResultEvent(invalid); err == nil {
		t.Fatal("accepted inconsistent conclusion")
	}
	invalid = event
	invalid.ObservedAt = op.LeaseExpiresAt.Add(time.Nanosecond)
	if err := ProviderReviewerAssignmentReconciliationResultEvent(invalid); err == nil {
		t.Fatal("accepted late observation")
	}
	invalid = event
	invalid.Outcome.Kind = v1.ProviderReviewerAssignmentReconciliationRetryable
	invalid.Outcome.Conclusion = ""
	invalid.Outcome.RequestState = ""
	invalid.Outcome.ObservedHeadSHA = ""
	invalid.Outcome.IntendedReviewers = nil
	invalid.Outcome.Code = "delivery_uncertain"
	if err := ProviderReviewerAssignmentReconciliationResultEvent(invalid); err == nil {
		t.Fatal("accepted uncertain outcome")
	}
	retryable := event
	retryable.Outcome = v1.ProviderReviewerAssignmentReconciliationResultOutcome{Kind: v1.ProviderReviewerAssignmentReconciliationRetryable, Code: v1.ProviderReviewerAssignmentReconciliationProviderUnavailable}
	if err := ProviderReviewerAssignmentReconciliationResultEvent(retryable); err != nil {
		t.Fatalf("valid retryable rejected: %v", err)
	}
	retryAfter := uint64(maxSourceControlRetryAfterMS)
	retryable.Outcome.Code = v1.ProviderReviewerAssignmentReconciliationRateLimited
	retryable.Outcome.RetryAfterMS = &retryAfter
	if err := ProviderReviewerAssignmentReconciliationResultEvent(retryable); err != nil {
		t.Fatalf("valid rate limit rejected: %v", err)
	}
	retryAfter++
	if err := ProviderReviewerAssignmentReconciliationResultEvent(retryable); err == nil {
		t.Fatal("oversized retry-after accepted")
	}
	for _, code := range []v1.ProviderReviewerAssignmentReconciliationErrorCode{v1.ProviderReviewerAssignmentReconciliationRequestNotFound, v1.ProviderReviewerAssignmentReconciliationRepositoryMismatch, v1.ProviderReviewerAssignmentReconciliationInstallationInactive, v1.ProviderReviewerAssignmentReconciliationInstallationAuthorityChanged, v1.ProviderReviewerAssignmentReconciliationInstallationPermissionDenied, v1.ProviderReviewerAssignmentReconciliationUnsupportedProviderInstance, v1.ProviderReviewerAssignmentReconciliationObservationIncomplete, v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse} {
		permanent := event
		permanent.Outcome = v1.ProviderReviewerAssignmentReconciliationResultOutcome{Kind: v1.ProviderReviewerAssignmentReconciliationPermanent, Code: code}
		if err := ProviderReviewerAssignmentReconciliationResultEvent(permanent); err != nil {
			t.Fatalf("valid permanent %s rejected: %v", code, err)
		}
	}
	superseded := event
	superseded.Outcome.Conclusion = v1.ProviderReviewerAssignmentReconciliationSuperseded
	superseded.Outcome.RequestState = v1.ProviderReviewerAssignmentReconciliationRequestMerged
	if err := ProviderReviewerAssignmentReconciliationResultEvent(superseded); err != nil {
		t.Fatalf("valid superseded rejected: %v", err)
	}
	badState := superseded
	badState.Outcome.RequestState = "unknown"
	if err := ProviderReviewerAssignmentReconciliationResultEvent(badState); err == nil {
		t.Fatal("unknown request state accepted")
	}
	badKind := event
	badKind.Kind = "wrong"
	if err := ProviderReviewerAssignmentReconciliationResultEvent(badKind); err == nil {
		t.Fatal("wrong event kind accepted")
	}
	if err := ProviderReviewerAssignmentReconciliationCommandMessageID("provider-reviewer-assignment-reconciliation-command-" + strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if err := ProviderReviewerAssignmentReconciliationCommandMessageID("loose"); err == nil {
		t.Fatal("loose command ID accepted")
	}
}
