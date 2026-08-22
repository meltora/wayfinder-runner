package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProviderReviewerAssignmentReconciliationIntendedReviewerSetDigestMatchesControlPlaneVector(t *testing.T) {
	reviewers := []ProviderReviewerAssignmentReconciliationIntendedReviewer{
		{ReviewerIdentityID: "reviewer-identity-a", ProviderAccountID: "42"},
		{ReviewerIdentityID: "reviewer-identity-b", ProviderAccountID: "99"},
	}
	const expected = "bcdc9118a19c9a016014d2a0a3fdea9a563eedcb75a017ff3ad88e1981445f7a"
	if got := ProviderReviewerAssignmentReconciliationIntendedReviewerSetDigest(reviewers); got != expected {
		t.Fatalf("digest = %q, want %q", got, expected)
	}
}

func TestProviderReviewerAssignmentReconciliationIntendedReviewerSetDigestCanonicalizesWithoutMutation(t *testing.T) {
	reviewers := []ProviderReviewerAssignmentReconciliationIntendedReviewer{
		{ReviewerIdentityID: "reviewer-identity-b", ProviderAccountID: "99"},
		{ReviewerIdentityID: "reviewer-identity-a", ProviderAccountID: "42"},
	}
	const expected = "bcdc9118a19c9a016014d2a0a3fdea9a563eedcb75a017ff3ad88e1981445f7a"
	if got := ProviderReviewerAssignmentReconciliationIntendedReviewerSetDigest(reviewers); got != expected {
		t.Fatalf("permuted digest=%s, want %s", got, expected)
	}
	if reviewers[0].ProviderAccountID != "99" || reviewers[1].ProviderAccountID != "42" {
		t.Fatalf("digest helper mutated caller order: %#v", reviewers)
	}
}

func TestProviderReviewerAssignmentReconciliationBitbucketDigestMatchesControlPlaneVector(t *testing.T) {
	reviewers := []ProviderReviewerAssignmentReconciliationIntendedReviewer{
		{ReviewerIdentityID: "reviewer-identity-a", ProviderAccountID: "{223e4567-e89b-42d3-a456-426614174000}"},
		{ReviewerIdentityID: "reviewer-identity-b", ProviderAccountID: "{323e4567-e89b-42d3-a456-426614174000}"},
	}
	const expected = "8218e1da06b0ca6a9b1c7d774ba8ce1e75912c7f9310a16def492318ed8f6696"
	if got := ProviderReviewerAssignmentReconciliationIntendedReviewerSetDigest(reviewers); got != expected {
		t.Fatalf("digest = %q, want %q", got, expected)
	}
}

func TestProviderReviewerAssignmentReconciliationWireShapeIsFlatAndReadOnly(t *testing.T) {
	operation := ProviderReviewerAssignmentReconciliationOperation{
		ProviderReviewerAssignmentReconciliationOperationAuthority: ProviderReviewerAssignmentReconciliationOperationAuthority{
			SchemaVersion: 1,
			ProviderReviewerAssignmentReconciliationOperationID: "provider-reviewer-assignment-reconciliation-operation-" + strings.Repeat("1", 64),
			OrganizationID: "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-run-1", AssignmentPlanID: "assignment-plan-1",
			AssignmentPlanDigest: strings.Repeat("2", 64), SourcePublicationObservationID: "source-observation-1",
			DeliveryID: "delivery-1", AssignmentOperationID: "provider-reviewer-assignment-operation-" + strings.Repeat("3", 64),
			AssignmentOperationDigest: strings.Repeat("4", 64), AssignmentCommandDigest: strings.Repeat("5", 64),
			AssignmentObservationID: "assignment-observation-1", AssignmentResultDeliveryID: "assignment-result-delivery-1",
			AssignmentResultPayloadChecksum: strings.Repeat("6", 64),
			RepositoryID:                    "repository-1", ProviderRepositoryID: "101", ExternalRequestID: "17",
			ExpectedHeadSHA: strings.Repeat("7", 40), InstallationID: "installation-1",
			InstallationStateRevision: 8, InstallationAuthorityRevision: 9,
			InstallationAuthorityDigest: strings.Repeat("a", 64), Provider: "github", ProviderInstanceKey: "github.com",
			IdempotencyKey:               "provider-reviewer-assignment-reconciliation-" + strings.Repeat("b", 64),
			AssignmentRecipientSetDigest: strings.Repeat("c", 64), IntendedReviewerSetDigest: strings.Repeat("d", 64), OperationDigest: strings.Repeat("e", 64),
			CommandDigest: strings.Repeat("f", 64), LeaseID: "reconciliation-lease-1", FencingToken: 10,
			LeaseExpiresAt: time.Date(2030, 8, 22, 12, 0, 0, 0, time.UTC),
		},
		IntendedReviewers: []ProviderReviewerAssignmentReconciliationIntendedReviewer{{
			ReviewerIdentityID: "reviewer-identity-1", ProviderAccountID: "42",
		}},
	}
	raw, err := json.Marshal(CommandPayload{
		Command:                                  CmdProviderReviewerAssignmentReconciliation,
		ProviderReviewerAssignmentReconciliation: &operation,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, expected := range []string{
		`"command":"provider_reviewer_assignment_reconciliation"`,
		`"provider_reviewer_assignment_reconciliation":`,
		`"assignment_observation_id":"assignment-observation-1"`,
		`"provider_account_id":"42"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("wire payload missing %s: %s", expected, text)
		}
	}
	for _, forbidden := range []string{"provider_username", "assignment_principal", "recipients", "requested_reviewers"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("wire payload leaked %q: %s", forbidden, text)
		}
	}
}

func TestProviderReviewerAssignmentReconciliationResultCarriesOnlyNormalizedEvidence(t *testing.T) {
	outcome := ProviderReviewerAssignmentReconciliationResultOutcome{
		Kind:            ProviderReviewerAssignmentReconciliationSucceeded,
		Conclusion:      ProviderReviewerAssignmentReconciliationSatisfied,
		RequestState:    ProviderReviewerAssignmentReconciliationRequestOpen,
		ObservedHeadSHA: strings.Repeat("1", 40),
		IntendedReviewers: []ProviderReviewerAssignmentReconciliationObservedReviewer{{
			ReviewerIdentityID: "reviewer-identity-1", ProviderAccountID: "42",
			Presence: ProviderReviewerAssignmentReconciliationReviewed,
		}},
	}
	raw, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, expected := range []string{`"kind":"succeeded"`, `"conclusion":"satisfied"`, `"presence":"reviewed"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("normalized result missing %s: %s", expected, text)
		}
	}
	for _, forbidden := range []string{"body", "url", "header", "token", "provider_username"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("normalized result leaked %q: %s", forbidden, text)
		}
	}
}
