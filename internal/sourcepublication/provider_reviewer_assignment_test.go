package sourcepublication

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

func TestRegistryAssignProviderReviewersFailsClosedForInstallationDrift(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("runner-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := &Registry{
		installations: map[string]config.SourcePublicationInstallationConfig{
			"install-1": {Provider: "github", APIBaseURL: "https://api.github.com", TokenFile: tokenFile},
		},
		httpClient: &http.Client{},
	}
	operation := assignmentOperationForTest("github")
	operation.ProviderInstanceKey = "github.enterprise.invalid"
	outcome := registry.AssignProviderReviewers(context.Background(), operation, nil)
	if outcome.Kind != v1.ProviderReviewerAssignmentPermanent ||
		outcome.Code != v1.ProviderReviewerAssignmentUnsupportedProviderInstance {
		t.Fatalf("unexpected outcome: %#v", outcome)
	}
}

func assignmentOperationForTest(provider string) v1.ProviderReviewerAssignmentOperation {
	principal := "alice"
	repositoryExternalID := "123"
	repositoryPath := ""
	providerAccountID := "42"
	if provider == "gitlab" {
		principal = "42"
	} else if provider == "bitbucket" {
		principal = "{11111111-1111-4111-8111-111111111111}"
		repositoryExternalID = "{22222222-2222-4222-8222-222222222222}"
		repositoryPath = "acme/repo"
		providerAccountID = "bb-account-42"
	}
	return v1.ProviderReviewerAssignmentOperation{
		ProviderReviewerAssignmentOperationAuthority: v1.ProviderReviewerAssignmentOperationAuthority{
			SchemaVersion:                         1,
			ProviderReviewerAssignmentOperationID: "provider-reviewer-assignment-operation-" + strings.Repeat("1", 64),
			OrganizationID:                        "org-1", AssignmentPlanID: "plan-1", AssignmentPlanDigest: strings.Repeat("2", 64),
			SourcePublicationObservationID: "observation-1", DeliveryID: "delivery-1", RepositoryID: "repo-1",
			ProviderRepositoryID: repositoryExternalID, ProviderRepositoryPath: repositoryPath,
			ExternalRequestID: "7", HeadSHA: "0123456789abcdef0123456789abcdef01234567",
			InstallationID: "install-1", InstallationAuthorityRevision: 1,
			InstallationAuthorityDigest: strings.Repeat("4", 64), Provider: provider,
			ProviderInstanceKey: map[string]string{"github": "github.com", "gitlab": "gitlab.com", "bitbucket": "bitbucket.org"}[provider],
			IdempotencyKey:      "provider-reviewer-assignment-" + strings.Repeat("5", 64),
			OperationDigest:     strings.Repeat("9", 64), CommandDigest: strings.Repeat("a", 64),
			LeaseID: "lease-1", FencingToken: 1, LeaseExpiresAt: time.Now().Add(time.Minute),
		},
		Recipients: []v1.ProviderReviewerAssignmentRecipient{{
			ReviewerIdentityID: "identity-1", UserID: "user-1", OrganizationMembershipID: "membership-1",
			IdentityStateRevision: 1, IdentityRevision: 1, ProviderAccountProofID: "proof-1",
			ProviderAccountProofRevision: 1, ProviderAccountProofDigest: strings.Repeat("b", 64),
			ProviderAccountProofExpiresAt: time.Now().Add(10 * time.Minute),
			IdentityVerificationDigest:    strings.Repeat("6", 64), IdentityVerificationExpiresAt: time.Now().Add(5 * time.Minute),
			ProviderAccountID: providerAccountID, ProviderUsername: "alice", AssignmentPrincipal: principal,
		}},
	}
}
