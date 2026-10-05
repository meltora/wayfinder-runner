package sourcepublication

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

func TestAssignmentRequestFailureNeverLeaksProviderErrors(t *testing.T) {
	invalid := assignmentRequestFailure(errInvalidProviderResponse)
	if invalid.Kind != v1.ProviderReviewerAssignmentPermanent ||
		invalid.Code != v1.ProviderReviewerAssignmentInvalidProviderResponse {
		t.Fatalf("invalid response outcome = %#v", invalid)
	}
	unavailable := assignmentRequestFailure(errors.New("secret provider transport detail"))
	if unavailable.Kind != v1.ProviderReviewerAssignmentRetryable ||
		unavailable.Code != v1.ProviderReviewerAssignmentProviderUnavailable {
		t.Fatalf("transport outcome = %#v", unavailable)
	}
}

func TestAssignmentIdentityFailureNormalizesEveryAuthorityClass(t *testing.T) {
	recipient := assignmentOperationForTest("github").Recipients[0]
	tests := []struct {
		outcome v1.ReviewerIdentityVerificationResultOutcome
		code    v1.ProviderReviewerAssignmentErrorCode
	}{
		{v1.ReviewerIdentityVerificationResultOutcome{Kind: v1.ReviewerIdentityVerificationRetryable, Code: v1.ReviewerIdentityRateLimited}, v1.ProviderReviewerAssignmentRateLimited},
		{v1.ReviewerIdentityVerificationResultOutcome{Kind: v1.ReviewerIdentityVerificationRetryable, Code: v1.ReviewerIdentityProviderUnavailable}, v1.ProviderReviewerAssignmentProviderUnavailable},
		{v1.ReviewerIdentityVerificationResultOutcome{Kind: v1.ReviewerIdentityVerificationPermanent, Code: v1.ReviewerIdentityInstallationInactive}, v1.ProviderReviewerAssignmentInstallationInactive},
		{v1.ReviewerIdentityVerificationResultOutcome{Kind: v1.ReviewerIdentityVerificationPermanent, Code: v1.ReviewerIdentityInstallationPermissionDenied}, v1.ProviderReviewerAssignmentInstallationPermissionDenied},
		{v1.ReviewerIdentityVerificationResultOutcome{Kind: v1.ReviewerIdentityVerificationPermanent, Code: v1.ReviewerIdentityAccountAmbiguous}, v1.ProviderReviewerAssignmentReviewerIdentityChanged},
		{v1.ReviewerIdentityVerificationResultOutcome{Kind: v1.ReviewerIdentityVerificationPermanent, Code: v1.ReviewerIdentityRepositoryAccessDenied}, v1.ProviderReviewerAssignmentReviewerNotAssignable},
		{v1.ReviewerIdentityVerificationResultOutcome{Kind: "unknown"}, v1.ProviderReviewerAssignmentInvalidProviderResponse},
	}
	for _, test := range tests {
		failure := assignmentIdentityFailure(test.outcome, recipient)
		if failure == nil || failure.Code != test.code {
			t.Fatalf("outcome %#v normalized to %#v, want %s", test.outcome, failure, test.code)
		}
	}
	changed := v1.ReviewerIdentityVerificationResultOutcome{
		Kind: v1.ReviewerIdentityVerificationSucceeded, ProviderUsername: "renamed",
		AssignmentPrincipal: recipient.AssignmentPrincipal,
		Repositories:        []v1.ReviewerIdentityRepositoryEvidence{{EffectiveReviewerAccess: v1.ReviewerIdentityEligible}},
	}
	if failure := assignmentIdentityFailure(changed, recipient); failure == nil ||
		failure.Code != v1.ProviderReviewerAssignmentReviewerIdentityChanged {
		t.Fatalf("identity drift = %#v", failure)
	}
	notMember := changed
	notMember.ProviderUsername = recipient.ProviderUsername
	notMember.Repositories[0].EffectiveReviewerAccess = v1.ReviewerIdentityNotMember
	if failure := assignmentIdentityFailure(notMember, recipient); failure == nil ||
		failure.Code != v1.ProviderReviewerAssignmentReviewerNotAssignable {
		t.Fatalf("membership drift = %#v", failure)
	}
	eligible := notMember
	eligible.Repositories[0].EffectiveReviewerAccess = v1.ReviewerIdentityEligible
	if failure := assignmentIdentityFailure(eligible, recipient); failure != nil {
		t.Fatalf("eligible identity rejected: %#v", failure)
	}
}

func TestAssignmentHTTPClassifiersNormalizeStatuses(t *testing.T) {
	tests := []struct {
		status int
		code   v1.ProviderReviewerAssignmentErrorCode
		kind   v1.ProviderReviewerAssignmentOutcomeKind
	}{
		{http.StatusServiceUnavailable, v1.ProviderReviewerAssignmentProviderUnavailable, v1.ProviderReviewerAssignmentRetryable},
		{http.StatusUnauthorized, v1.ProviderReviewerAssignmentInstallationInactive, v1.ProviderReviewerAssignmentPermanent},
		{http.StatusForbidden, v1.ProviderReviewerAssignmentInstallationPermissionDenied, v1.ProviderReviewerAssignmentPermanent},
		{http.StatusNotFound, v1.ProviderReviewerAssignmentRequestNotFound, v1.ProviderReviewerAssignmentPermanent},
		{http.StatusTooManyRequests, v1.ProviderReviewerAssignmentRateLimited, v1.ProviderReviewerAssignmentRetryable},
		{http.StatusTeapot, v1.ProviderReviewerAssignmentInvalidProviderResponse, v1.ProviderReviewerAssignmentPermanent},
	}
	for _, test := range tests {
		github := classifyGitHubAssignmentFailure(githubResponse{status: test.status, header: http.Header{}}, v1.ProviderReviewerAssignmentRequestNotFound)
		gitlab := classifyGitLabAssignmentFailure(gitLabResponse{status: test.status, header: http.Header{}}, v1.ProviderReviewerAssignmentRequestNotFound)
		bitbucket := classifyBitbucketAssignmentFailure(bitbucketResponse{status: test.status, header: http.Header{}}, v1.ProviderReviewerAssignmentRequestNotFound)
		for provider, outcome := range map[string]v1.ProviderReviewerAssignmentResultOutcome{
			"github": github, "gitlab": gitlab, "bitbucket": bitbucket,
		} {
			if outcome.Code != test.code || outcome.Kind != test.kind {
				t.Fatalf("%s status %d = %#v", provider, test.status, outcome)
			}
		}
	}
	rateLimitHeader := http.Header{}
	rateLimitHeader.Set("X-RateLimit-Remaining", "0")
	rateLimitedGitHub := classifyGitHubAssignmentFailure(githubResponse{
		status: http.StatusForbidden, header: rateLimitHeader,
	}, v1.ProviderReviewerAssignmentRequestNotFound)
	if rateLimitedGitHub.Code != v1.ProviderReviewerAssignmentRateLimited {
		t.Fatalf("GitHub secondary rate limit = %#v", rateLimitedGitHub)
	}
}

func TestRegistryAssignmentBoundaryRejectsMissingChangedAndExpiredInstallations(t *testing.T) {
	operation := assignmentOperationForTest("github")
	missing := (&Registry{installations: map[string]config.SourcePublicationInstallationConfig{}}).
		AssignProviderReviewers(t.Context(), operation, nil)
	if missing.Code != v1.ProviderReviewerAssignmentInstallationInactive {
		t.Fatalf("missing installation = %#v", missing)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("token-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := &Registry{installations: map[string]config.SourcePublicationInstallationConfig{
		"install-1": {Provider: "gitlab", APIBaseURL: "https://gitlab.com/api/v4", TokenFile: tokenFile},
	}}
	changed := registry.AssignProviderReviewers(t.Context(), operation, nil)
	if changed.Code != v1.ProviderReviewerAssignmentInstallationPermissionDenied {
		t.Fatalf("provider drift = %#v", changed)
	}
	operation.Recipients[0].IdentityVerificationExpiresAt = time.Now().Add(-time.Second)
	expired := registry.AssignProviderReviewers(t.Context(), operation, nil)
	if expired.Code != v1.ProviderReviewerAssignmentReviewerIdentityExpired {
		t.Fatalf("expired identity = %#v", expired)
	}
}

func TestProviderAssignmentAdaptersRejectNoncanonicalRepositoryAuthoritiesBeforeNetwork(t *testing.T) {
	githubOperation := assignmentOperationForTest("github")
	githubOperation.ProviderRepositoryID = "0123"
	if _, _, failure := (&githubPublisher{}).assignmentRepository(t.Context(), githubOperation); failure == nil ||
		failure.Code != v1.ProviderReviewerAssignmentRequestNotFound {
		t.Fatalf("GitHub noncanonical repository = %#v", failure)
	}
	gitLabOperation := assignmentOperationForTest("gitlab")
	gitLabOperation.ProviderRepositoryID = "0123"
	if _, failure := (&gitLabPublisher{}).assignmentProject(t.Context(), gitLabOperation); failure == nil ||
		failure.Code != v1.ProviderReviewerAssignmentRequestNotFound {
		t.Fatalf("GitLab noncanonical repository = %#v", failure)
	}
	bitbucketOperation := assignmentOperationForTest("bitbucket")
	bitbucketOperation.ProviderRepositoryPath = "acme/repo/extra"
	if _, failure := (&bitbucketPublisher{}).assignmentRepository(t.Context(), bitbucketOperation); failure == nil ||
		failure.Code != v1.ProviderReviewerAssignmentRepositoryMismatch {
		t.Fatalf("Bitbucket noncanonical repository = %#v", failure)
	}
	if _, err := canonicalPositiveInt64("07"); err == nil {
		t.Fatal("noncanonical external request ID accepted")
	}
}

func TestRegistryAssignmentBoundaryRejectsUnreadableCredentialAndUnknownProvider(t *testing.T) {
	operation := assignmentOperationForTest("github")
	missingToken := filepath.Join(t.TempDir(), "missing-token")
	registry := &Registry{installations: map[string]config.SourcePublicationInstallationConfig{
		"install-1": {Provider: "github", APIBaseURL: "https://api.github.com", TokenFile: missingToken},
	}}
	if outcome := registry.AssignProviderReviewers(t.Context(), operation, nil); outcome.Code != v1.ProviderReviewerAssignmentInstallationInactive {
		t.Fatalf("unreadable credential = %#v", outcome)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("token-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation.Provider = "unknown"
	operation.ProviderInstanceKey = "unknown.example"
	registry = &Registry{installations: map[string]config.SourcePublicationInstallationConfig{
		"install-1": {Provider: "unknown", APIBaseURL: "https://unknown.example", TokenFile: tokenFile},
	}}
	if outcome := registry.AssignProviderReviewers(t.Context(), operation, nil); outcome.Code != v1.ProviderReviewerAssignmentUnsupportedProviderInstance {
		t.Fatalf("unknown provider = %#v", outcome)
	}
}
