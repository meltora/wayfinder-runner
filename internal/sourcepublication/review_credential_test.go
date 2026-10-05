package sourcepublication

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

const reviewCredentialHead = "0123456789abcdef0123456789abcdef01234567"

type reviewCredentialServer struct {
	*httptest.Server
	requests       atomic.Int32
	mu             sync.Mutex
	authorizations []string
	assigned       bool
}

func newReviewCredentialServer(t *testing.T, head string) *reviewCredentialServer {
	t.Helper()
	server := &reviewCredentialServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		server.requests.Add(1)
		server.mu.Lock()
		server.authorizations = append(server.authorizations, request.Header.Get("Authorization"))
		assigned := server.assigned
		server.mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /user/42":
			_, _ = io.WriteString(writer, `{"id":42,"login":"alice","type":"User"}`)
		case "GET /repositories/123":
			_, _ = io.WriteString(writer, `{"id":123,"full_name":"acme/repo"}`)
		case "GET /repos/acme/repo/collaborators/alice/permission":
			_, _ = io.WriteString(writer, `{"permission":"write","role_name":"write","user":{"id":42,"login":"alice","type":"User"}}`)
		case "GET /repos/acme/repo/pulls/7":
			_, _ = io.WriteString(writer, `{"number":7,"state":"open","head":{"sha":"`+head+`"}}`)
		case "GET /repos/acme/repo/pulls/7/requested_reviewers":
			if assigned {
				_, _ = io.WriteString(writer, `{"users":[{"id":42,"login":"alice","type":"User"}],"teams":[]}`)
			} else {
				_, _ = io.WriteString(writer, `{"users":[],"teams":[]}`)
			}
		case "POST /repos/acme/repo/pulls/7/requested_reviewers":
			server.mu.Lock()
			server.assigned = true
			server.mu.Unlock()
			_, _ = io.WriteString(writer, `{}`)
		case "GET /repos/acme/repo/pulls/7/reviews", "GET /repos/acme/repo/issues/7/comments", "GET /repos/acme/repo/pulls/7/comments":
			_, _ = io.WriteString(writer, `[]`)
		case "GET /repos/acme/repo/pulls/7/reviews/7001":
			_, _ = io.WriteString(writer, `{"id":7001,"state":"CHANGES_REQUESTED","body":"Café\nFix the retry path.","submitted_at":"2026-08-22T11:58:00Z","commit_id":"`+head+`","user":{"id":42,"type":"User"}}`)
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func (server *reviewCredentialServer) seenAuthorizations() []string {
	server.mu.Lock()
	defer server.mu.Unlock()
	return append([]string(nil), server.authorizations...)
}

type reviewOperationCase struct {
	name string
	head string
	run  func(context.Context, *Registry, string, *v1.ProviderCredential) (string, string, any)
}

func reviewOperationCases() []reviewOperationCase {
	return []reviewOperationCase{
		{
			name: "reviewer identity verification",
			head: reviewCredentialHead,
			run: func(ctx context.Context, registry *Registry, key string, credential *v1.ProviderCredential) (string, string, any) {
				repositories := []v1.ReviewerIdentityVerificationRepository{{RepositoryID: "repository-1", RepositoryExternalID: "123"}}
				operation := v1.ReviewerIdentityVerificationOperation{
					Provider: "github", ProviderInstanceKey: key, InstallationID: "installation-1", ProviderAccountID: "42",
					Repositories: repositories, RepositorySetDigest: v1.ReviewerIdentityRepositorySetDigest(repositories),
				}
				outcome := registry.VerifyReviewerIdentity(ctx, operation, credential)
				return string(outcome.Kind), string(outcome.Code), outcome
			},
		},
		{
			name: "provider reviewer assignment",
			head: reviewCredentialHead,
			run: func(ctx context.Context, registry *Registry, key string, credential *v1.ProviderCredential) (string, string, any) {
				operation := assignmentOperationForTest("github")
				operation.InstallationID = "installation-1"
				operation.ProviderInstanceKey = key
				outcome := registry.AssignProviderReviewers(ctx, operation, credential)
				return string(outcome.Kind), string(outcome.Code), outcome
			},
		},
		{
			name: "provider reviewer assignment reconciliation",
			head: reviewCredentialHead,
			run: func(ctx context.Context, registry *Registry, key string, credential *v1.ProviderCredential) (string, string, any) {
				operation := reconciliationOperationForTest("github")
				operation.ProviderInstanceKey = key
				outcome := registry.ReconcileProviderReviewerAssignment(ctx, operation, credential)
				return string(outcome.Kind), string(outcome.Code), outcome
			},
		},
		{
			name: "provider review observation",
			head: reviewCredentialHead,
			run: func(ctx context.Context, registry *Registry, key string, credential *v1.ProviderCredential) (string, string, any) {
				operation := reviewObservationOperationForTest("github")
				operation.ProviderInstanceKey = key
				outcome := registry.ObserveProviderReviews(ctx, operation, credential)
				return string(outcome.Kind), string(outcome.Code), outcome
			},
		},
		{
			name: "provider review feedback content",
			head: strings.Repeat("b", 40),
			run: func(ctx context.Context, registry *Registry, key string, credential *v1.ProviderCredential) (string, string, any) {
				operation := feedbackOperationForTest()
				operation.ProviderInstanceKey = key
				outcome := registry.FetchProviderReviewFeedbackContent(ctx, operation, credential)
				return string(outcome.Kind), string(outcome.Code), outcome
			},
		},
	}
}

func reviewRegistry(t *testing.T, server *httptest.Server, withTokenFile bool) *Registry {
	t.Helper()
	installation := config.SourcePublicationInstallationConfig{Provider: "github", APIBaseURL: server.URL}
	if withTokenFile {
		installation.TokenFile = writeToken(t, 0o600)
	}
	return &Registry{
		installations: map[string]config.SourcePublicationInstallationConfig{"installation-1": installation},
		httpClient:    server.Client(),
	}
}

func reviewInstanceKey(server *httptest.Server) string {
	return configuredProviderInstanceKey("github", server.URL)
}

func TestReviewOperationsUseCommandCredentialInsteadOfTokenFile(t *testing.T) {
	for _, operation := range reviewOperationCases() {
		t.Run(operation.name, func(t *testing.T) {
			server := newReviewCredentialServer(t, operation.head)
			kind, code, outcome := operation.run(t.Context(), reviewRegistry(t, server.Server, true), reviewInstanceKey(server.Server), commandCredential(time.Hour, "123"))
			if kind != "succeeded" {
				t.Fatalf("kind=%s code=%s outcome=%+v", kind, code, outcome)
			}
			authorizations := server.seenAuthorizations()
			if len(authorizations) == 0 {
				t.Fatal("no GitHub requests were made")
			}
			for _, authorization := range authorizations {
				if authorization != "Bearer "+commandCredentialToken {
					t.Fatalf("authorization = %q", authorization)
				}
			}
		})
	}
}

func TestReviewOperationsFallBackToTokenFileWithoutCredential(t *testing.T) {
	for _, operation := range reviewOperationCases() {
		t.Run(operation.name, func(t *testing.T) {
			server := newReviewCredentialServer(t, operation.head)
			kind, code, outcome := operation.run(t.Context(), reviewRegistry(t, server.Server, true), reviewInstanceKey(server.Server), nil)
			if kind != "succeeded" {
				t.Fatalf("kind=%s code=%s outcome=%+v", kind, code, outcome)
			}
			for _, authorization := range server.seenAuthorizations() {
				if authorization != "Bearer github-token-123" {
					t.Fatalf("authorization = %q", authorization)
				}
			}
		})
	}
}

func TestReviewOperationsSucceedOnCredentialOnlyInstallation(t *testing.T) {
	for _, operation := range reviewOperationCases() {
		t.Run(operation.name, func(t *testing.T) {
			server := newReviewCredentialServer(t, operation.head)
			kind, code, outcome := operation.run(t.Context(), reviewRegistry(t, server.Server, false), reviewInstanceKey(server.Server), commandCredential(time.Hour, "123"))
			if kind != "succeeded" {
				t.Fatalf("kind=%s code=%s outcome=%+v", kind, code, outcome)
			}
		})
	}
}

func TestReviewOperationsWithoutUsableCredentialAreRetryableWithoutNetworkCall(t *testing.T) {
	credentials := map[string]*v1.ProviderCredential{
		"credential-only without credential": nil,
		"near expiry":                        commandCredential(30*time.Second, "123"),
		"expired":                            commandCredential(-time.Second, "123"),
	}
	for credentialName, credential := range credentials {
		for _, operation := range reviewOperationCases() {
			t.Run(credentialName+"/"+operation.name, func(t *testing.T) {
				server := newReviewCredentialServer(t, operation.head)
				kind, code, outcome := operation.run(t.Context(), reviewRegistry(t, server.Server, false), reviewInstanceKey(server.Server), credential)
				if kind != "retryable" || code != "provider_unavailable" {
					t.Fatalf("kind=%s code=%s outcome=%+v", kind, code, outcome)
				}
				if server.requests.Load() != 0 {
					t.Fatalf("GitHub requests = %d", server.requests.Load())
				}
			})
		}
	}
}

func TestReviewOperationsNearExpiryCredentialIsNotReplacedByTokenFile(t *testing.T) {
	for _, operation := range reviewOperationCases() {
		t.Run(operation.name, func(t *testing.T) {
			server := newReviewCredentialServer(t, operation.head)
			kind, code, outcome := operation.run(t.Context(), reviewRegistry(t, server.Server, true), reviewInstanceKey(server.Server), commandCredential(30*time.Second, "123"))
			if kind != "retryable" || code != "provider_unavailable" || server.requests.Load() != 0 {
				t.Fatalf("kind=%s code=%s requests=%d outcome=%+v", kind, code, server.requests.Load(), outcome)
			}
		})
	}
}

func TestReviewOperationsRejectCredentialForAnotherRepository(t *testing.T) {
	for _, operation := range reviewOperationCases() {
		t.Run(operation.name, func(t *testing.T) {
			server := newReviewCredentialServer(t, operation.head)
			kind, code, outcome := operation.run(t.Context(), reviewRegistry(t, server.Server, true), reviewInstanceKey(server.Server), commandCredential(time.Hour, "999"))
			if kind != "permanent" || code != "installation_permission_denied" {
				t.Fatalf("kind=%s code=%s outcome=%+v", kind, code, outcome)
			}
			if server.requests.Load() != 0 {
				t.Fatalf("GitHub requests = %d", server.requests.Load())
			}
		})
	}
}

func TestReviewOperationsNeverSurfaceTokenEchoedByProvider(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusInternalServerError} {
		for _, operation := range reviewOperationCases() {
			t.Run(http.StatusText(status)+"/"+operation.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					echoed := request.Header.Get("Authorization")
					writer.Header().Set("X-Echo", echoed)
					writer.WriteHeader(status)
					_, _ = io.WriteString(writer, `{"message":"bad credentials `+echoed+`"}`)
				}))
				defer server.Close()
				kind, _, outcome := operation.run(t.Context(), reviewRegistry(t, server, false), reviewInstanceKey(server), commandCredential(time.Hour, "123"))
				if kind == "succeeded" {
					t.Fatalf("outcome = %+v", outcome)
				}
				raw, err := json.Marshal(outcome)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), commandCredentialToken) {
					t.Fatalf("outcome leaked token: %s", raw)
				}
			})
		}
	}
}

func TestReviewPublisherRefusesRequestOnceCredentialNearsExpiry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	registry := reviewRegistry(t, server, false)
	publisher := registry.newGitHubReviewPublisher(registry.installations["installation-1"], reviewAuthority{
		token: commandCredentialToken, credentialExpiresAt: time.Now().Add(30 * time.Second),
	})
	_, err := publisher.request(t.Context(), http.MethodGet, "/repositories/123", nil, nil)
	if !errors.Is(err, errProviderCredentialExpiring) || requests.Load() != 0 {
		t.Fatalf("err=%v requests=%d", err, requests.Load())
	}
	if outcome := feedbackIdentityFailure(publisher.verifyReviewerIdentity(t.Context(), v1.ReviewerIdentityVerificationOperation{ProviderAccountID: "42"})); outcome == nil ||
		outcome.Kind != v1.ProviderReviewFeedbackContentRetryable || outcome.Code != v1.ProviderReviewFeedbackContentProviderUnavailable {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestCredentialOnlyInstallationAdvertisesReviewCapabilities(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	registry, err := NewRegistry(map[string]config.SourcePublicationInstallationConfig{
		"installation-1": {Provider: "github", APIBaseURL: server.URL},
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.ReadyReviewerIdentityInstallations(); len(got) != 1 || got[0].InstallationID != "installation-1" {
		t.Fatalf("reviewer identity = %+v", got)
	}
	if got := registry.ReadyProviderReviewerAssignmentInstallations(); len(got) != 1 {
		t.Fatalf("assignment = %+v", got)
	}
	if got := registry.ReadyProviderReviewerAssignmentReconciliationInstallations(); len(got) != 1 {
		t.Fatalf("reconciliation = %+v", got)
	}
	if got := registry.ReadyProviderReviewObservationInstallations(); len(got) != 1 {
		t.Fatalf("observation = %+v", got)
	}
	if got := registry.ReadyProviderReviewFeedbackContentInstallations(); len(got) != 1 {
		t.Fatalf("feedback content = %+v", got)
	}
}

type expireBeforeMutation struct {
	base        http.RoundTripper
	publisher   *githubPublisher
	reviewReads int
	posts       int
}

func (transport *expireBeforeMutation) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodPost {
		transport.posts++
	}
	if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/pulls/7/reviews") {
		transport.reviewReads++
		if transport.reviewReads == 2 {
			transport.publisher.credentialExpiresAt = time.Now().Add(30 * time.Second)
		}
	}
	return transport.base.RoundTrip(request)
}

func TestAssignmentCredentialExpiringBeforeMutationIsRetryable(t *testing.T) {
	server := newReviewCredentialServer(t, reviewCredentialHead)
	transport := &expireBeforeMutation{base: server.Client().Transport}
	registry := reviewRegistry(t, server.Server, false)
	registry.httpClient = &http.Client{Transport: transport}
	publisher := registry.newGitHubReviewPublisher(registry.installations["installation-1"], reviewAuthority{
		token: commandCredentialToken, credentialExpiresAt: time.Now().Add(time.Hour),
	})
	transport.publisher = publisher
	operation := assignmentOperationForTest("github")
	operation.InstallationID = "installation-1"
	outcome := publisher.assignReviewers(t.Context(), operation)
	if outcome.Kind != v1.ProviderReviewerAssignmentRetryable || outcome.Code != v1.ProviderReviewerAssignmentProviderUnavailable {
		t.Fatalf("outcome = %+v", outcome)
	}
	if transport.reviewReads != 2 || transport.posts != 0 {
		t.Fatalf("review reads = %d posts = %d", transport.reviewReads, transport.posts)
	}
}
