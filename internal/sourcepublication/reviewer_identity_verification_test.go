package sourcepublication

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

func reviewerIdentityOperation(provider, providerInstance string) v1.ReviewerIdentityVerificationOperation {
	repositories := []v1.ReviewerIdentityVerificationRepository{
		{RepositoryID: "repository-1", RepositoryExternalID: "101"},
	}
	return v1.ReviewerIdentityVerificationOperation{
		Provider: provider, ProviderInstanceKey: providerInstance,
		InstallationID: "installation-1", ProviderAccountID: "42",
		Repositories: repositories, RepositorySetDigest: v1.ReviewerIdentityRepositorySetDigest(repositories),
	}
}

func reviewerRegistryForServer(t *testing.T, provider string, server *httptest.Server) *Registry {
	t.Helper()
	registry, err := NewRegistry(map[string]config.SourcePublicationInstallationConfig{
		"installation-1": {Provider: provider, APIBaseURL: server.URL, TokenFile: writeToken(t, 0o600)},
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func serverInstanceKey(t *testing.T, server *httptest.Server) string {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Host
}

func TestGitHubReviewerIdentityVerificationUsesExactAccountAndEveryRepository(t *testing.T) {
	var paths []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertBearerToken(t, request)
		paths = append(paths, request.URL.EscapedPath())
		switch request.URL.Path {
		case "/user/42":
			_, _ = writer.Write([]byte(`{"id":42,"login":"octocat","type":"User"}`))
		case "/repositories/101":
			_, _ = writer.Write([]byte(`{"id":101,"full_name":"octo/example"}`))
		case "/repos/octo/example/collaborators/octocat/permission":
			_, _ = writer.Write([]byte(`{"permission":"write","role_name":"write","user":{"id":42,"login":"octocat"}}`))
		case "/repositories/102":
			_, _ = writer.Write([]byte(`{"id":102,"full_name":"octo/second"}`))
		case "/repos/octo/second/collaborators/octocat/permission":
			http.NotFound(writer, request)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	operation := reviewerIdentityOperation("github", serverInstanceKey(t, server))
	operation.Repositories = append(operation.Repositories, v1.ReviewerIdentityVerificationRepository{
		RepositoryID: "repository-2", RepositoryExternalID: "102",
	})
	operation.RepositorySetDigest = v1.ReviewerIdentityRepositorySetDigest(operation.Repositories)
	outcome := reviewerRegistryForServer(t, "github", server).VerifyReviewerIdentity(t.Context(), operation, nil)
	if outcome.Kind != v1.ReviewerIdentityVerificationSucceeded || outcome.ProviderUsername != "octocat" ||
		outcome.AssignmentPrincipal != "octocat" || len(outcome.Repositories) != 2 ||
		outcome.Repositories[0].EffectiveReviewerAccess != v1.ReviewerIdentityEligible ||
		outcome.Repositories[1].EffectiveReviewerAccess != v1.ReviewerIdentityNotMember {
		t.Fatalf("outcome = %+v", outcome)
	}
	for _, path := range paths {
		if strings.Contains(path, "@") || strings.Contains(strings.ToLower(path), "email") {
			t.Fatalf("provider request used loose identity lookup: %v", paths)
		}
	}
}

func TestGitHubReviewerIdentityVerificationFailsClosedOnMismatchedAccount(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"id":99,"login":"octocat","type":"User"}`))
	}))
	defer server.Close()
	outcome := reviewerRegistryForServer(t, "github", server).VerifyReviewerIdentity(
		t.Context(), reviewerIdentityOperation("github", serverInstanceKey(t, server)), nil,
	)
	if outcome.Kind != v1.ReviewerIdentityVerificationPermanent || outcome.Code != v1.ReviewerIdentityAccountNotFound {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGitHubReviewerIdentityVerificationRejectsRedirectAndOversizedResponses(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"redirect": func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, "https://attacker.example/users/octocat", http.StatusFound)
		},
		"oversized": func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write(bytes.Repeat([]byte("x"), maxProviderResponseBytes+1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(handler)
			defer server.Close()
			outcome := reviewerRegistryForServer(t, "github", server).VerifyReviewerIdentity(
				t.Context(), reviewerIdentityOperation("github", serverInstanceKey(t, server)), nil,
			)
			if outcome.Kind != v1.ReviewerIdentityVerificationPermanent {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}
}

func TestGitHubReviewerIdentityVerificationClassifiesAuthenticationAndRateFailures(t *testing.T) {
	for name, test := range map[string]struct {
		status int
		code   v1.ReviewerIdentityVerificationErrorCode
		kind   v1.ReviewerIdentityVerificationOutcomeKind
	}{
		"authentication": {status: http.StatusUnauthorized, code: v1.ReviewerIdentityInstallationInactive, kind: v1.ReviewerIdentityVerificationPermanent},
		"permission":     {status: http.StatusForbidden, code: v1.ReviewerIdentityInstallationPermissionDenied, kind: v1.ReviewerIdentityVerificationPermanent},
		"rate limit":     {status: http.StatusTooManyRequests, code: v1.ReviewerIdentityRateLimited, kind: v1.ReviewerIdentityVerificationRetryable},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				if test.status == http.StatusTooManyRequests {
					writer.Header().Set("Retry-After", "5")
				}
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(`{"message":"private provider detail"}`))
			}))
			defer server.Close()
			outcome := reviewerRegistryForServer(t, "github", server).VerifyReviewerIdentity(
				t.Context(), reviewerIdentityOperation("github", serverInstanceKey(t, server)), nil,
			)
			if outcome.Kind != test.kind || outcome.Code != test.code {
				t.Fatalf("outcome = %+v", outcome)
			}
			if test.status == http.StatusTooManyRequests && (outcome.RetryAfterMS == nil || *outcome.RetryAfterMS != 5000) {
				t.Fatalf("retry hint = %v", outcome.RetryAfterMS)
			}
		})
	}
}

func TestGitHubReviewerIdentityVerificationRejectsRepositoryIdentityDrift(t *testing.T) {
	for name, operation := range map[string]func(*v1.ReviewerIdentityVerificationOperation){
		"stable ID mismatch": func(*v1.ReviewerIdentityVerificationOperation) {},
		"optional path mismatch": func(operation *v1.ReviewerIdentityVerificationOperation) {
			operation.Repositories[0].RepositoryPath = "octo/expected"
			operation.RepositorySetDigest = v1.ReviewerIdentityRepositorySetDigest(operation.Repositories)
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/user/42":
					_, _ = writer.Write([]byte(`{"id":42,"login":"renamed-user","type":"User"}`))
				case "/repositories/101":
					if name == "stable ID mismatch" {
						_, _ = writer.Write([]byte(`{"id":999,"full_name":"octo/expected"}`))
						return
					}
					_, _ = writer.Write([]byte(`{"id":101,"full_name":"attacker/substitute"}`))
				default:
					http.NotFound(writer, request)
				}
			}))
			defer server.Close()
			verification := reviewerIdentityOperation("github", serverInstanceKey(t, server))
			operation(&verification)
			outcome := reviewerRegistryForServer(t, "github", server).VerifyReviewerIdentity(t.Context(), verification, nil)
			if outcome.Kind != v1.ReviewerIdentityVerificationPermanent || outcome.Code != v1.ReviewerIdentityRepositoryAccessDenied {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}
}

func TestGitLabReviewerIdentityVerificationUsesNumericMemberID(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("PRIVATE-TOKEN"); got != "github-token-123" {
			t.Fatalf("private token = %q", got)
		}
		switch request.URL.Path {
		case "/users/42":
			_, _ = writer.Write([]byte(`{"id":42,"username":"gitlab-user","state":"active"}`))
		case "/projects/101":
			_, _ = writer.Write([]byte(`{"id":101,"path_with_namespace":"octo/example"}`))
		case "/projects/101/members/all/42":
			_, _ = writer.Write([]byte(`{"id":42,"username":"gitlab-user","state":"active","access_level":30}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	operation := reviewerIdentityOperation("gitlab", serverInstanceKey(t, server))
	outcome := reviewerRegistryForServer(t, "gitlab", server).VerifyReviewerIdentity(t.Context(), operation, nil)
	if outcome.Kind != v1.ReviewerIdentityVerificationSucceeded || outcome.ProviderUsername != "gitlab-user" ||
		outcome.AssignmentPrincipal != "42" || len(outcome.Repositories) != 1 ||
		outcome.Repositories[0].EffectiveReviewerAccess != v1.ReviewerIdentityEligible {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGitLabReviewerIdentityVerificationRejectsMismatchedStableAccountID(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/users/42" {
			t.Fatalf("unexpected provider request after account mismatch: %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"id":99,"username":"gitlab-user","state":"active"}`))
	}))
	defer server.Close()
	outcome := reviewerRegistryForServer(t, "gitlab", server).VerifyReviewerIdentity(
		t.Context(), reviewerIdentityOperation("gitlab", serverInstanceKey(t, server)), nil,
	)
	if outcome.Kind != v1.ReviewerIdentityVerificationPermanent || outcome.Code != v1.ReviewerIdentityAccountNotFound {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGitLabReviewerIdentityVerificationRejectsInactiveAccount(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"id":42,"username":"gitlab-user","state":"blocked"}`))
	}))
	defer server.Close()
	outcome := reviewerRegistryForServer(t, "gitlab", server).VerifyReviewerIdentity(
		t.Context(), reviewerIdentityOperation("gitlab", serverInstanceKey(t, server)), nil,
	)
	if outcome.Kind != v1.ReviewerIdentityVerificationPermanent || outcome.Code != v1.ReviewerIdentityInstallationInactive {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGitLabReviewerIdentityVerificationNormalizesInsufficientPermission(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/users/42":
			_, _ = writer.Write([]byte(`{"id":42,"username":"gitlab-user","state":"active"}`))
		case "/projects/101":
			_, _ = writer.Write([]byte(`{"id":101,"path_with_namespace":"octo/example"}`))
		default:
			_, _ = writer.Write([]byte(`{"id":42,"username":"gitlab-user","state":"active","access_level":20}`))
		}
	}))
	defer server.Close()
	outcome := reviewerRegistryForServer(t, "gitlab", server).VerifyReviewerIdentity(
		t.Context(), reviewerIdentityOperation("gitlab", serverInstanceKey(t, server)), nil,
	)
	if outcome.Kind != v1.ReviewerIdentityVerificationSucceeded ||
		outcome.Repositories[0].EffectiveReviewerAccess != v1.ReviewerIdentityInsufficientPermission {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGitLabReviewerIdentityVerificationRejectsRepositoryIDDrift(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/users/42":
			_, _ = writer.Write([]byte(`{"id":42,"username":"gitlab-user","state":"active"}`))
		case "/projects/101":
			_, _ = writer.Write([]byte(`{"id":999,"path_with_namespace":"octo/example"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	outcome := reviewerRegistryForServer(t, "gitlab", server).VerifyReviewerIdentity(
		t.Context(), reviewerIdentityOperation("gitlab", serverInstanceKey(t, server)), nil,
	)
	if outcome.Kind != v1.ReviewerIdentityVerificationPermanent || outcome.Code != v1.ReviewerIdentityRepositoryAccessDenied {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGitLabReviewerIdentityVerificationRejectsRepositoryPathDrift(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/users/42":
			_, _ = writer.Write([]byte(`{"id":42,"username":"gitlab-user","state":"active"}`))
		case "/projects/101":
			_, _ = writer.Write([]byte(`{"id":101,"path_with_namespace":"attacker/substitute"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	operation := reviewerIdentityOperation("gitlab", serverInstanceKey(t, server))
	operation.Repositories[0].RepositoryPath = "octo/example"
	operation.RepositorySetDigest = v1.ReviewerIdentityRepositorySetDigest(operation.Repositories)
	outcome := reviewerRegistryForServer(t, "gitlab", server).VerifyReviewerIdentity(t.Context(), operation, nil)
	if outcome.Kind != v1.ReviewerIdentityVerificationPermanent || outcome.Code != v1.ReviewerIdentityRepositoryAccessDenied {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestBitbucketReviewerIdentityVerificationUsesAccountIDAndRejectsPartialPage(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/users/557058:account-42":
			_, _ = writer.Write([]byte(`{"account_id":"557058:account-42","uuid":"{504c3b62-8120-4f0c-a7bc-87800b9d6f70}","nickname":"bitbucket-user","account_status":"active"}`))
		case "/repositories/octo/example":
			_, _ = writer.Write([]byte(`{"uuid":"{85d08b4e-571d-44e9-a507-fa476535aa98}","full_name":"octo/example"}`))
		case "/workspaces/octo/permissions/repositories/example":
			if !strings.Contains(request.URL.Query().Get("q"), "557058:account-42") {
				t.Fatalf("permission filter = %q", request.URL.Query().Get("q"))
			}
			_, _ = writer.Write([]byte(`{"pagelen":100,"page":1,"size":2,"values":[{"permission":"read","user":{"account_id":"557058:account-42","uuid":"{504c3b62-8120-4f0c-a7bc-87800b9d6f70}"},"repository":{"uuid":"{85d08b4e-571d-44e9-a507-fa476535aa98}","full_name":"octo/example"}}]}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	operation := reviewerIdentityOperation("bitbucket", serverInstanceKey(t, server))
	operation.ProviderAccountID = "557058:account-42"
	operation.Repositories[0].RepositoryExternalID = "{85d08b4e-571d-44e9-a507-fa476535aa98}"
	operation.Repositories[0].RepositoryPath = "octo/example"
	operation.RepositorySetDigest = v1.ReviewerIdentityRepositorySetDigest(operation.Repositories)
	outcome := reviewerRegistryForServer(t, "bitbucket", server).VerifyReviewerIdentity(t.Context(), operation, nil)
	if outcome.Kind != v1.ReviewerIdentityVerificationPermanent || outcome.Code != v1.ReviewerIdentityAccountAmbiguous {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestBitbucketReviewerIdentityMatchesOnlyExactStableUUID(t *testing.T) {
	const accountUUID = "{504c3b62-8120-4f0c-a7bc-87800b9d6f70}"
	account := bitbucketReviewerAccount{AccountID: "557058:account-42", UUID: accountUUID}
	if !bitbucketAccountMatches(accountUUID, account) {
		t.Fatal("exact stable Bitbucket UUID did not match")
	}
	account.UUID = "{123e4567-e89b-42d3-a456-426614174000}"
	if bitbucketAccountMatches(accountUUID, account) {
		t.Fatal("mismatched stable Bitbucket UUID was accepted")
	}
	account.UUID = accountUUID
	account.AccountID = ""
	if bitbucketAccountMatches(accountUUID, account) {
		t.Fatal("UUID response without the canonical account_id was accepted")
	}
}

func TestBitbucketReviewerIdentityVerificationAcceptsOneExactEffectivePermission(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/users/557058:account-42":
			_, _ = writer.Write([]byte(`{"account_id":"557058:account-42","uuid":"{504c3b62-8120-4f0c-a7bc-87800b9d6f70}","nickname":"bitbucket-user","account_status":"active"}`))
		case "/repositories/octo/example":
			_, _ = writer.Write([]byte(`{"uuid":"{85d08b4e-571d-44e9-a507-fa476535aa98}","full_name":"octo/example"}`))
		default:
			response := map[string]any{
				"pagelen": 100, "page": 1, "size": 1,
				"values": []any{map[string]any{
					"permission": "write",
					"user":       map[string]any{"account_id": "557058:account-42", "uuid": "{504c3b62-8120-4f0c-a7bc-87800b9d6f70}"},
					"repository": map[string]any{"uuid": "{85d08b4e-571d-44e9-a507-fa476535aa98}", "full_name": "octo/example"},
				}},
			}
			_ = json.NewEncoder(writer).Encode(response)
		}
	}))
	defer server.Close()
	operation := reviewerIdentityOperation("bitbucket", serverInstanceKey(t, server))
	operation.ProviderAccountID = "557058:account-42"
	operation.Repositories[0].RepositoryExternalID = "{85d08b4e-571d-44e9-a507-fa476535aa98}"
	operation.Repositories[0].RepositoryPath = "octo/example"
	operation.RepositorySetDigest = v1.ReviewerIdentityRepositorySetDigest(operation.Repositories)
	outcome := reviewerRegistryForServer(t, "bitbucket", server).VerifyReviewerIdentity(t.Context(), operation, nil)
	if outcome.Kind != v1.ReviewerIdentityVerificationSucceeded || outcome.ProviderUsername != "bitbucket-user" ||
		outcome.AssignmentPrincipal != "{504c3b62-8120-4f0c-a7bc-87800b9d6f70}" ||
		outcome.Repositories[0].EffectiveReviewerAccess != v1.ReviewerIdentityEligible {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestBitbucketReviewerIdentityVerificationRejectsRepositoryUUIDDrift(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/users/557058:account-42":
			_, _ = writer.Write([]byte(`{"account_id":"557058:account-42","uuid":"{504c3b62-8120-4f0c-a7bc-87800b9d6f70}","nickname":"bitbucket-user","account_status":"active"}`))
		case "/repositories/octo/example":
			_, _ = writer.Write([]byte(`{"uuid":"{123e4567-e89b-42d3-a456-426614174000}","full_name":"octo/example"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	operation := reviewerIdentityOperation("bitbucket", serverInstanceKey(t, server))
	operation.ProviderAccountID = "557058:account-42"
	operation.Repositories[0].RepositoryExternalID = "{85d08b4e-571d-44e9-a507-fa476535aa98}"
	operation.Repositories[0].RepositoryPath = "octo/example"
	operation.RepositorySetDigest = v1.ReviewerIdentityRepositorySetDigest(operation.Repositories)
	outcome := reviewerRegistryForServer(t, "bitbucket", server).VerifyReviewerIdentity(t.Context(), operation, nil)
	if outcome.Kind != v1.ReviewerIdentityVerificationPermanent || outcome.Code != v1.ReviewerIdentityRepositoryAccessDenied {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestReviewerIdentityRegistryFailsClosedOnInstallationAndInstanceDrift(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("provider must not be called for mismatched local authority")
	}))
	defer server.Close()
	registry := reviewerRegistryForServer(t, "github", server)

	missing := reviewerIdentityOperation("github", serverInstanceKey(t, server))
	missing.InstallationID = "installation-missing"
	if outcome := registry.VerifyReviewerIdentity(t.Context(), missing, nil); outcome.Code != v1.ReviewerIdentityInstallationNotFound {
		t.Fatalf("missing installation outcome = %+v", outcome)
	}
	mismatch := reviewerIdentityOperation("gitlab", serverInstanceKey(t, server))
	if outcome := registry.VerifyReviewerIdentity(t.Context(), mismatch, nil); outcome.Code != v1.ReviewerIdentityProviderMismatch {
		t.Fatalf("provider mismatch outcome = %+v", outcome)
	}
	drift := reviewerIdentityOperation("github", "attacker.example")
	if outcome := registry.VerifyReviewerIdentity(t.Context(), drift, nil); outcome.Code != v1.ReviewerIdentityUnsupportedProviderInstance {
		t.Fatalf("provider instance drift outcome = %+v", outcome)
	}
}
