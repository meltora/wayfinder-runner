package v1

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReviewerIdentityVerificationResultAuthorityCopiesEveryBoundField(t *testing.T) {
	leaseExpiresAt := time.Date(2030, 8, 21, 12, 0, 0, 0, time.UTC)
	operation := ReviewerIdentityVerificationOperation{
		SchemaVersion:                           1,
		ReviewerIdentityVerificationOperationID: "operation",
		OrganizationID:                          "organization",
		InstallationID:                          "installation",
		InstallationStateRevision:               2,
		InstallationAuthorityRevision:           3,
		InstallationAuthorityDigest:             "installation-digest",
		Provider:                                "github",
		ProviderInstanceKey:                     "github.com",
		ProviderAccountProofID:                  "proof",
		ProviderAccountProofRevision:            4,
		ProviderAccountProofDigest:              "proof-digest",
		ProviderAccountID:                       "42",
		RepositorySetDigest:                     "repository-digest",
		OperationDigest:                         "operation-digest",
		CommandDigest:                           "command-digest",
		LeaseID:                                 "lease",
		FencingToken:                            5,
		LeaseExpiresAt:                          leaseExpiresAt,
		Repositories: []ReviewerIdentityVerificationRepository{
			{RepositoryID: "repository-1", RepositoryExternalID: "101"},
		},
	}
	want := ReviewerIdentityVerificationResultAuthority{
		SchemaVersion:                           1,
		ReviewerIdentityVerificationOperationID: "operation",
		OrganizationID:                          "organization",
		InstallationID:                          "installation",
		InstallationStateRevision:               2,
		InstallationAuthorityRevision:           3,
		InstallationAuthorityDigest:             "installation-digest",
		Provider:                                "github",
		ProviderInstanceKey:                     "github.com",
		ProviderAccountProofID:                  "proof",
		ProviderAccountProofRevision:            4,
		ProviderAccountProofDigest:              "proof-digest",
		ProviderAccountID:                       "42",
		RepositorySetDigest:                     "repository-digest",
		OperationDigest:                         "operation-digest",
		CommandDigest:                           "command-digest",
		LeaseID:                                 "lease",
		FencingToken:                            5,
		LeaseExpiresAt:                          leaseExpiresAt,
	}
	if got := ReviewerIdentityVerificationResultAuthorityFromOperation(operation); !reflect.DeepEqual(got, want) {
		t.Fatalf("authority = %+v, want %+v", got, want)
	}
}

func TestReviewerIdentityVerificationWireShapeCarriesOnlyStableProviderIdentifiers(t *testing.T) {
	repositories := []ReviewerIdentityVerificationRepository{
		{RepositoryID: "repository-1", RepositoryExternalID: "101"},
	}
	command := CommandPayload{
		Command: CmdReviewerIdentityVerification,
		ReviewerIdentityVerification: &ReviewerIdentityVerificationOperation{
			SchemaVersion:                           ReviewerIdentityVerificationSchemaVersion,
			ReviewerIdentityVerificationOperationID: "reviewer-identity-verification-operation-" + strings.Repeat("f", 64),
			OrganizationID:                          "organization-1", InstallationID: "installation-1",
			InstallationStateRevision: 2, InstallationAuthorityRevision: 3,
			InstallationAuthorityDigest: strings.Repeat("a", 64),
			Provider:                    "github", ProviderInstanceKey: "github.com",
			ProviderAccountProofID: "account-proof-1", ProviderAccountProofRevision: 4,
			ProviderAccountProofDigest: strings.Repeat("b", 64), ProviderAccountID: "42",
			RepositorySetDigest: ReviewerIdentityRepositorySetDigest(repositories),
			OperationDigest:     strings.Repeat("c", 64), CommandDigest: strings.Repeat("d", 64),
			LeaseID: "identity-lease-1", FencingToken: 5,
			LeaseExpiresAt: time.Date(2030, 8, 21, 12, 0, 0, 0, time.UTC),
			Repositories:   repositories,
		},
	}
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"command":"reviewer_identity_verification"`) ||
		!strings.Contains(string(raw), `"provider_account_id":"42"`) ||
		!strings.Contains(string(raw), `"repository_external_id":"101"`) {
		t.Fatalf("unexpected command wire shape: %s", raw)
	}
	for _, forbidden := range []string{`"email"`, `"token"`, `"credential"`, `"provider_username"`, `"assignment_principal"`} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("command leaked or accepted forbidden identity field %q: %s", forbidden, raw)
		}
	}
}

func TestReviewerIdentityVerificationResultWireShapeIsNormalized(t *testing.T) {
	event := ReviewerIdentityVerificationResultEvent{
		Kind: EventReviewerIdentityVerificationResult,
		ReviewerIdentityVerificationResultAuthority: ReviewerIdentityVerificationResultAuthority{
			SchemaVersion:                           ReviewerIdentityVerificationSchemaVersion,
			ReviewerIdentityVerificationOperationID: "reviewer-identity-verification-operation-" + strings.Repeat("f", 64),
			OrganizationID:                          "organization-1", InstallationID: "installation-1",
			InstallationStateRevision: 2, InstallationAuthorityRevision: 3,
			InstallationAuthorityDigest: strings.Repeat("a", 64),
			Provider:                    "github", ProviderInstanceKey: "github.com",
			ProviderAccountProofID: "account-proof-1", ProviderAccountProofRevision: 4,
			ProviderAccountProofDigest: strings.Repeat("b", 64), ProviderAccountID: "42",
			RepositorySetDigest: strings.Repeat("e", 64),
			OperationDigest:     strings.Repeat("c", 64), CommandDigest: strings.Repeat("d", 64),
			LeaseID: "identity-lease-1", FencingToken: 5,
			LeaseExpiresAt: time.Date(2030, 8, 21, 12, 0, 0, 0, time.UTC),
		},
		CommandMessageID: "reviewer-identity-verification-command-" + strings.Repeat("f", 64),
		ObservedAt:       time.Date(2030, 8, 21, 11, 59, 0, 0, time.UTC),
		Outcome: ReviewerIdentityVerificationResultOutcome{
			Kind:             ReviewerIdentityVerificationSucceeded,
			ProviderUsername: "octocat", AssignmentPrincipal: "octocat",
			Repositories: []ReviewerIdentityRepositoryEvidence{
				{RepositoryID: "repository-1", RepositoryExternalID: "101", EffectiveReviewerAccess: ReviewerIdentityEligible},
			},
		},
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kind":"reviewer_identity_verification_result"`) ||
		!strings.Contains(string(raw), `"provider_username":"octocat"`) ||
		!strings.Contains(string(raw), `"effective_reviewer_access":"eligible"`) {
		t.Fatalf("unexpected result wire shape: %s", raw)
	}
	for _, forbidden := range []string{`"email"`, `"token"`, `"credential"`, `"raw_response"`, `"display_name"`} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("result leaked forbidden field %q: %s", forbidden, raw)
		}
	}
}

func TestReviewerIdentityCapabilityContainsOnlyBoundedInstallationCoordinates(t *testing.T) {
	manifest := CapabilityManifest{
		ReviewerIdentityVerificationTransportVersion: ReviewerIdentityVerificationTransportVersion,
		ReviewerIdentityVerificationProviders:        []string{"github"},
		ReviewerIdentityVerificationInstallations: []ReviewerIdentityVerificationInstallationCapability{
			{InstallationID: "installation-1", Provider: "github", ProviderInstanceKey: "github.com"},
		},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"installation_id":"installation-1"`) {
		t.Fatalf("installation capability missing: %s", raw)
	}
	for _, forbidden := range []string{`"token"`, `"credential"`, `"api_base_url"`} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("capability leaked %q: %s", forbidden, raw)
		}
	}
}

func TestReviewerIdentityRepositorySetDigestMatchesControlPlaneFixture(t *testing.T) {
	repositories := []ReviewerIdentityVerificationRepository{
		{RepositoryID: "repository-1", RepositoryExternalID: "101"},
		{RepositoryID: "repository-2", RepositoryExternalID: "202"},
	}
	const expected = "cf5caa17c3a64e74653f72fd586369c9f0bfe2b8b65cfa7be77409a283b77efc"
	if got := ReviewerIdentityRepositorySetDigest(repositories); got != expected {
		t.Fatalf("repository set digest = %s, want control-plane vector %s", got, expected)
	}
}

func TestReviewerIdentityRepositorySetDigestIncludesBitbucketPath(t *testing.T) {
	repositories := []ReviewerIdentityVerificationRepository{{
		RepositoryID:         "repository-1",
		RepositoryExternalID: "{123e4567-e89b-42d3-a456-426614174000}",
		RepositoryPath:       "octo/alpha",
	}}
	const expected = "01c12f7a7b99d6967727bcf323ab6476f456f815584bcf04e11008ba2eaeac50"
	if got := ReviewerIdentityRepositorySetDigest(repositories); got != expected {
		t.Fatalf("repository set digest = %s, want path vector %s", got, expected)
	}
}
