package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validProviderReviewerAssignmentOperationForValidation() v1.ProviderReviewerAssignmentOperation {
	now := time.Now().UTC().Truncate(time.Millisecond)
	recipients := []v1.ProviderReviewerAssignmentRecipient{{
		ReviewerIdentityID: "identity-a", UserID: "user-a", OrganizationMembershipID: "membership-a",
		IdentityStateRevision: 1, IdentityRevision: 2, ProviderAccountProofID: "proof-a",
		ProviderAccountProofRevision: 3, ProviderAccountProofDigest: strings.Repeat("b", 64),
		ProviderAccountProofExpiresAt: now.Add(10 * time.Minute), IdentityVerificationDigest: strings.Repeat("c", 64),
		IdentityVerificationExpiresAt: now.Add(5 * time.Minute), ProviderAccountID: "42",
		ProviderUsername: "alice", AssignmentPrincipal: "alice",
	}, {
		ReviewerIdentityID: "identity-b", UserID: "user-b", OrganizationMembershipID: "membership-b",
		IdentityStateRevision: 4, IdentityRevision: 5, ProviderAccountProofID: "proof-b",
		ProviderAccountProofRevision: 6, ProviderAccountProofDigest: strings.Repeat("d", 64),
		ProviderAccountProofExpiresAt: now.Add(10 * time.Minute), IdentityVerificationDigest: strings.Repeat("e", 64),
		IdentityVerificationExpiresAt: now.Add(5 * time.Minute), ProviderAccountID: "99",
		ProviderUsername: "bob", AssignmentPrincipal: "bob",
	}}
	return v1.ProviderReviewerAssignmentOperation{
		ProviderReviewerAssignmentOperationAuthority: v1.ProviderReviewerAssignmentOperationAuthority{
			SchemaVersion:                         1,
			ProviderReviewerAssignmentOperationID: "provider-reviewer-assignment-operation-" + strings.Repeat("1", 64),
			OrganizationID:                        "organization-1", AssignmentPlanID: "plan-1", AssignmentPlanDigest: strings.Repeat("2", 64),
			SourcePublicationObservationID: "observation-1", DeliveryID: "delivery-1", RepositoryID: "repository-1",
			ProviderRepositoryID: "12345", ExternalRequestID: "17", HeadSHA: strings.Repeat("3", 40),
			InstallationID: "installation-1", InstallationStateRevision: 8, InstallationAuthorityRevision: 9,
			InstallationAuthorityDigest: strings.Repeat("4", 64), Provider: "github", ProviderInstanceKey: "github.com",
			IdempotencyKey:     "provider-reviewer-assignment-" + strings.Repeat("5", 64),
			RecipientSetDigest: v1.ProviderReviewerAssignmentRecipientSetDigest(recipients),
			OperationDigest:    strings.Repeat("6", 64), CommandDigest: strings.Repeat("7", 64),
			LeaseID: "lease-1", FencingToken: 10, LeaseExpiresAt: now.Add(time.Minute),
		},
		Recipients: recipients,
	}
}

func TestProviderReviewerAssignmentOperationAcceptsBoundCanonicalAuthority(t *testing.T) {
	if err := ProviderReviewerAssignmentOperation(validProviderReviewerAssignmentOperationForValidation()); err != nil {
		t.Fatal(err)
	}
}

func TestProviderReviewerAssignmentOperationRejectsHostileAuthority(t *testing.T) {
	tests := map[string]func(*v1.ProviderReviewerAssignmentOperation){
		"loose operation id": func(value *v1.ProviderReviewerAssignmentOperation) {
			value.ProviderReviewerAssignmentOperationID = "operation-1"
		},
		"noncanonical repository id": func(value *v1.ProviderReviewerAssignmentOperation) {
			value.ProviderRepositoryID = "012345"
		},
		"username path for stable GitHub id": func(value *v1.ProviderReviewerAssignmentOperation) {
			value.ProviderRepositoryPath = "owner/repo"
		},
		"digest mismatch": func(value *v1.ProviderReviewerAssignmentOperation) {
			value.Recipients[0].ProviderAccountID = "43"
		},
		"duplicate provider account": func(value *v1.ProviderReviewerAssignmentOperation) {
			value.Recipients[1].ProviderAccountID = "42"
			value.RecipientSetDigest = v1.ProviderReviewerAssignmentRecipientSetDigest(value.Recipients)
		},
		"unsorted tenant users": func(value *v1.ProviderReviewerAssignmentOperation) {
			value.Recipients[0].UserID = "user-z"
			value.RecipientSetDigest = v1.ProviderReviewerAssignmentRecipientSetDigest(value.Recipients)
		},
		"lease outlives identity": func(value *v1.ProviderReviewerAssignmentOperation) {
			value.LeaseExpiresAt = value.Recipients[0].IdentityVerificationExpiresAt.Add(time.Second)
		},
		"cross-provider account": func(value *v1.ProviderReviewerAssignmentOperation) {
			value.Provider = "bitbucket"
			value.ProviderInstanceKey = "bitbucket.org"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validProviderReviewerAssignmentOperationForValidation()
			mutate(&value)
			if err := ProviderReviewerAssignmentOperation(value); err == nil {
				t.Fatal("hostile operation accepted")
			}
		})
	}
}

func TestProviderReviewerAssignmentResultEventAcceptsOnlyNormalizedOutcomes(t *testing.T) {
	operation := validProviderReviewerAssignmentOperationForValidation()
	base := v1.ProviderReviewerAssignmentResultEvent{
		Kind: v1.EventProviderReviewerAssignmentResult,
		ProviderReviewerAssignmentOperationAuthority: operation.ProviderReviewerAssignmentOperationAuthority,
		CommandMessageID: "provider-reviewer-assignment-command-" + strings.Repeat("8", 64),
		ObservedAt:       time.Now().UTC(),
	}
	success := base
	success.Outcome = v1.ProviderReviewerAssignmentResultOutcome{
		Kind: v1.ProviderReviewerAssignmentSucceeded,
		RequestedReviewers: []v1.ProviderReviewerAssignmentRequestedReviewer{
			{ReviewerIdentityID: "identity-a", ProviderAccountID: "42", AssignmentPrincipal: "alice"},
			{ReviewerIdentityID: "identity-b", ProviderAccountID: "99", AssignmentPrincipal: "bob"},
		},
	}
	if err := ProviderReviewerAssignmentResultEvent(success); err != nil {
		t.Fatal(err)
	}
	uncertain := base
	uncertain.Outcome = v1.ProviderReviewerAssignmentResultOutcome{
		Kind: v1.ProviderReviewerAssignmentUncertain, Code: v1.ProviderReviewerAssignmentDeliveryUncertain,
	}
	if err := ProviderReviewerAssignmentResultEvent(uncertain); err != nil {
		t.Fatal(err)
	}
	uncertain.Outcome.Kind = v1.ProviderReviewerAssignmentRetryable
	if err := ProviderReviewerAssignmentResultEvent(uncertain); err == nil {
		t.Fatal("delivery_uncertain accepted as retryable")
	}
	success.Outcome.RequestedReviewers[0], success.Outcome.RequestedReviewers[1] =
		success.Outcome.RequestedReviewers[1], success.Outcome.RequestedReviewers[0]
	if err := ProviderReviewerAssignmentResultEvent(success); err == nil {
		t.Fatal("unsorted stable provider account evidence accepted")
	}
}
