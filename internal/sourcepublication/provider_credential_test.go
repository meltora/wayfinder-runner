package sourcepublication

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

const commandCredentialToken = "ghs_command0000000000000000000000000000000"

func commandCredential(expiresIn time.Duration, repositoryIDs ...string) *v1.ProviderCredential {
	if len(repositoryIDs) == 0 {
		repositoryIDs = []string{"424242"}
	}
	return &v1.ProviderCredential{
		Kind:          v1.ProviderCredentialGitHubInstallationToken,
		Token:         commandCredentialToken,
		ExpiresAt:     time.Now().Add(expiresIn),
		RepositoryIDs: repositoryIDs,
	}
}

type recordingGitHubServer struct {
	*httptest.Server
	requests       atomic.Int32
	authorizations chan string
}

func newRecordingGitHubServer(t *testing.T, repositoryBody string) *recordingGitHubServer {
	t.Helper()
	recorder := &recordingGitHubServer{authorizations: make(chan string, 16)}
	recorder.Server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorder.requests.Add(1)
		recorder.authorizations <- request.Header.Get("Authorization")
		switch request.Method + " " + request.URL.Path {
		case "GET /repos/octo/example":
			_, _ = writer.Write([]byte(repositoryBody))
		case "GET /repos/octo/example/git/ref/heads/job/feature-1":
			_, _ = writer.Write([]byte(`{"object":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`))
		case "GET /repos/octo/example/pulls":
			_, _ = writer.Write([]byte(`[{"number":7,"html_url":"https://github.example/octo/example/pull/7","state":"open","head":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"base":{"ref":"main"}}]`))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(recorder.Close)
	return recorder
}

func (recorder *recordingGitHubServer) drainAuthorizations() []string {
	var values []string
	for {
		select {
		case value := <-recorder.authorizations:
			values = append(values, value)
		default:
			return values
		}
	}
}

func credentialOnlyRegistry(t *testing.T, server *httptest.Server) *Registry {
	t.Helper()
	registry, err := NewRegistry(map[string]config.SourcePublicationInstallationConfig{
		"installation-1": {Provider: "github", APIBaseURL: server.URL},
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestGitHubPublicationUsesCommandCredentialInsteadOfTokenFile(t *testing.T) {
	server := newRecordingGitHubServer(t, `{"id":424242}`)
	outcome := testRegistryForServer(t, server.Server).Publish(t.Context(), testOperation(), commandCredential(time.Hour))
	if outcome.Kind != v1.SourcePublicationSucceeded {
		t.Fatalf("outcome = %+v", outcome)
	}
	authorizations := server.drainAuthorizations()
	if len(authorizations) == 0 {
		t.Fatal("no GitHub requests were made")
	}
	for _, authorization := range authorizations {
		if authorization != "Bearer "+commandCredentialToken {
			t.Fatalf("authorization = %q", authorization)
		}
	}
}

func TestGitHubPublicationFallsBackToTokenFileWithoutCredential(t *testing.T) {
	server := newRecordingGitHubServer(t, `{"id":424242}`)
	outcome := testRegistryForServer(t, server.Server).Publish(t.Context(), testOperation(), nil)
	if outcome.Kind != v1.SourcePublicationSucceeded {
		t.Fatalf("outcome = %+v", outcome)
	}
	for _, authorization := range server.drainAuthorizations() {
		if authorization != "Bearer github-token-123" {
			t.Fatalf("authorization = %q", authorization)
		}
	}
}

func TestCredentialOnlyInstallationUsesCommandCredential(t *testing.T) {
	server := newRecordingGitHubServer(t, `{"id":424242}`)
	registry := credentialOnlyRegistry(t, server.Server)
	if providers := registry.ReadyProviders(); len(providers) != 1 || providers[0] != "github" {
		t.Fatalf("providers = %v", providers)
	}
	outcome := registry.Publish(t.Context(), testOperation(), commandCredential(time.Hour))
	if outcome.Kind != v1.SourcePublicationSucceeded {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestCredentialOnlyInstallationWithoutCredentialIsRetryable(t *testing.T) {
	server := newRecordingGitHubServer(t, `{"id":424242}`)
	outcome := credentialOnlyRegistry(t, server.Server).Publish(t.Context(), testOperation(), nil)
	assertCredentialUnavailable(t, outcome)
	if server.requests.Load() != 0 {
		t.Fatalf("GitHub requests = %d", server.requests.Load())
	}
}

func TestExpiringCredentialIsRetryableWithoutNetworkCall(t *testing.T) {
	for name, expiresIn := range map[string]time.Duration{
		"near expiry": 30 * time.Second,
		"expired":     -time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			server := newRecordingGitHubServer(t, `{"id":424242}`)
			for registryName, registry := range map[string]*Registry{
				"token file":      testRegistryForServer(t, server.Server),
				"credential only": credentialOnlyRegistry(t, server.Server),
			} {
				outcome := registry.Publish(t.Context(), testOperation(), commandCredential(expiresIn))
				assertCredentialUnavailable(t, outcome)
				if server.requests.Load() != 0 {
					t.Fatalf("%s: GitHub requests = %d", registryName, server.requests.Load())
				}
			}
		})
	}
}

func TestCredentialRepositoryMismatchIsDenied(t *testing.T) {
	server := newRecordingGitHubServer(t, `{"id":999}`)
	outcome := credentialOnlyRegistry(t, server.Server).Publish(t.Context(), testOperation(), commandCredential(time.Hour, "424242"))
	if outcome.Kind != v1.SourcePublicationPermanent || outcome.Code != v1.SourceControlPermissionDenied {
		t.Fatalf("outcome = %+v", outcome)
	}
	if server.requests.Load() != 1 {
		t.Fatalf("credential was used beyond the repository lookup: %d requests", server.requests.Load())
	}
}

func TestCredentialRepositoryWithoutProviderIDFailsClosed(t *testing.T) {
	for name, body := range map[string]string{
		"missing id":  `{"full_name":"octo/example"}`,
		"string id":   `{"id":"424242"}`,
		"negative id": `{"id":-424242}`,
		"zero id":     `{"id":0}`,
		"not json":    `not json`,
	} {
		t.Run(name, func(t *testing.T) {
			server := newRecordingGitHubServer(t, body)
			outcome := credentialOnlyRegistry(t, server.Server).Publish(t.Context(), testOperation(), commandCredential(time.Hour))
			if outcome.Kind != v1.SourcePublicationPermanent || outcome.Code != v1.SourceControlInvalidProviderResponse {
				t.Fatalf("outcome = %+v", outcome)
			}
			if server.requests.Load() != 1 {
				t.Fatalf("credential was used beyond the repository lookup: %d requests", server.requests.Load())
			}
		})
	}
}

func TestGitHubErrorBodyEchoingCredentialIsNotSurfaced(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("WWW-Authenticate", "Bearer "+commandCredentialToken)
		http.Error(writer, fmt.Sprintf(`{"message":"Bad credentials %s","auth":%q}`, commandCredentialToken, request.Header.Get("Authorization")), http.StatusInternalServerError)
	}))
	defer server.Close()
	outcome := credentialOnlyRegistry(t, server).Publish(t.Context(), testOperation(), commandCredential(time.Hour))
	encoded, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), commandCredentialToken) || strings.Contains(fmt.Sprintf("%+v", outcome), commandCredentialToken) {
		t.Fatalf("outcome = %s", encoded)
	}
	if outcome.Kind == v1.SourcePublicationSucceeded {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGitHubTransportErrorIsScrubbed(t *testing.T) {
	publisher := newGitHubPublisher("https://127.0.0.1:1/"+commandCredentialToken, commandCredentialToken, &http.Client{Timeout: time.Second})
	_, err := publisher.request(t.Context(), http.MethodGet, "/repos/octo/example", nil, nil)
	if err == nil {
		t.Fatal("request to a closed port succeeded")
	}
	if strings.Contains(err.Error(), commandCredentialToken) {
		t.Fatalf("error = %v", err)
	}
}

func assertCredentialUnavailable(t *testing.T, outcome v1.SourcePublicationResultOutcome) {
	t.Helper()
	if outcome.Kind != v1.SourcePublicationRetryable || outcome.Code != v1.SourceControlProviderUnavailable ||
		outcome.Result != nil || outcome.RetryAfterMS != nil {
		t.Fatalf("outcome = %+v", outcome)
	}
}
