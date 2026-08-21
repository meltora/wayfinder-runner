package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProviderReviewerAssignmentRecipientSetDigestMatchesControlPlaneVector(t *testing.T) {
	parse := func(value string) time.Time {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	recipients := []ProviderReviewerAssignmentRecipient{
		{
			ReviewerIdentityID: "reviewer-identity-a", UserID: "user-a", OrganizationMembershipID: "membership-a",
			IdentityStateRevision: 3, IdentityRevision: 2, ProviderAccountProofID: "provider-account-proof-a",
			ProviderAccountProofRevision: 2, ProviderAccountProofDigest: repeatForAssignmentTest("b", 64),
			ProviderAccountProofExpiresAt: parse("2030-08-21T13:00:00.000Z"),
			IdentityVerificationDigest:    repeatForAssignmentTest("6", 64),
			IdentityVerificationExpiresAt: parse("2030-08-21T12:30:00.000Z"),
			ProviderAccountID:             "42", ProviderUsername: "reviewer-a", AssignmentPrincipal: "reviewer-a",
		},
		{
			ReviewerIdentityID: "reviewer-identity-b", UserID: "user-b", OrganizationMembershipID: "membership-b",
			IdentityStateRevision: 5, IdentityRevision: 4, ProviderAccountProofID: "provider-account-proof-b",
			ProviderAccountProofRevision: 4, ProviderAccountProofDigest: repeatForAssignmentTest("c", 64),
			ProviderAccountProofExpiresAt: parse("2030-08-21T13:00:00.000Z"),
			IdentityVerificationDigest:    repeatForAssignmentTest("7", 64),
			IdentityVerificationExpiresAt: parse("2030-08-21T12:30:00.000Z"),
			ProviderAccountID:             "99", ProviderUsername: "reviewer-b", AssignmentPrincipal: "reviewer-b",
		},
	}
	const expected = "33720ca9cf2d7b1b44d71d8b38465a5c6acec009401c44bf7294c8766b9241f4"
	if got := ProviderReviewerAssignmentRecipientSetDigest(recipients); got != expected {
		t.Fatalf("digest = %s, want %s", got, expected)
	}
}

func TestProviderReviewerAssignmentWireShapeIsFlatAndStableIDBased(t *testing.T) {
	operation := ProviderReviewerAssignmentOperation{
		ProviderReviewerAssignmentOperationAuthority: ProviderReviewerAssignmentOperationAuthority{
			SchemaVersion: 1, ProviderReviewerAssignmentOperationID: "provider-reviewer-assignment-operation-" + strings.Repeat("1", 64),
			ProviderRepositoryID: "42", ProviderRepositoryPath: "",
		},
		Recipients: []ProviderReviewerAssignmentRecipient{{ReviewerIdentityID: "identity-1"}},
	}
	raw, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatal("operation did not marshal as an object")
	}
	if _, nested := decoded["assignment"]; nested {
		t.Fatal("operation regressed to a nested assignment shape")
	}
	if _, present := decoded["provider_repository_path"]; present {
		t.Fatal("empty optional GitHub/GitLab repository path was serialized")
	}
	if _, present := decoded["recipients"]; !present {
		t.Fatal("flat operation omitted recipients")
	}
	outcome := ProviderReviewerAssignmentResultOutcome{
		Kind: ProviderReviewerAssignmentSucceeded,
		RequestedReviewers: []ProviderReviewerAssignmentRequestedReviewer{{
			ReviewerIdentityID: "identity-1", ProviderAccountID: "42", AssignmentPrincipal: "octocat",
		}},
	}
	raw, err = json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"result"`) || !strings.Contains(string(raw), `"provider_account_id":"42"`) {
		t.Fatalf("unexpected result wire shape: %s", raw)
	}
}

func repeatForAssignmentTest(value string, count int) string {
	result := ""
	for index := 0; index < count; index++ {
		result += value
	}
	return result
}
