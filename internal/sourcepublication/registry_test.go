package sourcepublication

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

func writeToken(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("github-token-123\n"), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRegistryAdvertisesOnlyReadyProviders(t *testing.T) {
	tokenFile := writeToken(t, 0o600)
	registry, err := NewRegistry(map[string]config.SourcePublicationInstallationConfig{
		"installation-b": {Provider: "github", APIBaseURL: "https://api.github.com", TokenFile: tokenFile},
		"installation-a": {Provider: "github", APIBaseURL: "https://api.github.com", TokenFile: tokenFile},
		"installation-c": {Provider: "gitlab", APIBaseURL: "https://gitlab.com/api/v4", TokenFile: tokenFile},
		"installation-d": {Provider: "bitbucket", APIBaseURL: "https://api.bitbucket.org/2.0", TokenFile: tokenFile},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	providers := registry.ReadyProviders()
	if len(providers) != 3 || providers[0] != "bitbucket" || providers[1] != "github" || providers[2] != "gitlab" {
		t.Fatalf("providers = %v", providers)
	}

	if err := os.Chmod(tokenFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if providers := registry.ReadyProviders(); len(providers) != 0 {
		t.Fatalf("unreadable credential remained advertised: %v", providers)
	}
}

func TestRegistryAdvertisesExactReviewerIdentityInstallations(t *testing.T) {
	tokenFile := writeToken(t, 0o600)
	registry, err := NewRegistry(map[string]config.SourcePublicationInstallationConfig{
		"installation-b": {Provider: "gitlab", APIBaseURL: "https://gitlab.example/api/v4", TokenFile: tokenFile},
		"installation-a": {Provider: "github", APIBaseURL: "https://api.github.com", TokenFile: tokenFile},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	installations := registry.ReadyReviewerIdentityInstallations()
	if len(installations) != 2 || installations[0].InstallationID != "installation-a" ||
		installations[0].ProviderInstanceKey != "github.com" ||
		installations[1].ProviderInstanceKey != "gitlab.example" {
		t.Fatalf("installations = %+v", installations)
	}
	if err := os.Chmod(tokenFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if installations := registry.ReadyReviewerIdentityInstallations(); len(installations) != 0 {
		t.Fatalf("unready installations advertised: %+v", installations)
	}
}

func TestRegistryAdvertisesExactProviderReviewerAssignmentInstallations(t *testing.T) {
	tokenFile := writeToken(t, 0o600)
	registry, err := NewRegistry(map[string]config.SourcePublicationInstallationConfig{
		"installation-b": {Provider: "gitlab", APIBaseURL: "https://gitlab.example/api/v4", TokenFile: tokenFile},
		"installation-a": {Provider: "github", APIBaseURL: "https://api.github.com", TokenFile: tokenFile},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	installations := registry.ReadyProviderReviewerAssignmentInstallations()
	if len(installations) != 2 || installations[0].InstallationID != "installation-a" ||
		installations[0].ProviderInstanceKey != "github.com" ||
		installations[1].ProviderInstanceKey != "gitlab.example" {
		t.Fatalf("installations = %+v", installations)
	}
	if err := os.Chmod(tokenFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if installations := registry.ReadyProviderReviewerAssignmentInstallations(); len(installations) != 0 {
		t.Fatalf("unready installations advertised: %+v", installations)
	}
}

func TestRegistryRejectsUnsupportedProvider(t *testing.T) {
	if _, err := NewRegistry(map[string]config.SourcePublicationInstallationConfig{
		"installation-1": {Provider: "unknown", APIBaseURL: "https://source.example/api", TokenFile: writeToken(t, 0o600)},
	}, nil); err == nil {
		t.Fatal("unsupported source publication provider accepted")
	}
}

func TestRegistryRejectsUnsafeOrMalformedCredentialFiles(t *testing.T) {
	worldReadable := writeToken(t, 0o644)
	if _, err := NewRegistry(map[string]config.SourcePublicationInstallationConfig{
		"installation-1": {Provider: "github", APIBaseURL: "https://api.github.com", TokenFile: worldReadable},
	}, nil); err == nil {
		t.Fatal("world-readable token accepted")
	}

	malformed := writeToken(t, 0o600)
	if err := os.WriteFile(malformed, []byte("token with spaces"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistry(map[string]config.SourcePublicationInstallationConfig{
		"installation-1": {Provider: "github", APIBaseURL: "https://api.github.com", TokenFile: malformed},
	}, nil); err == nil {
		t.Fatal("malformed token accepted")
	}

	nonASCII := writeToken(t, 0o600)
	if err := os.WriteFile(nonASCII, []byte("github-tokén-123"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistry(map[string]config.SourcePublicationInstallationConfig{
		"installation-1": {Provider: "github", APIBaseURL: "https://api.github.com", TokenFile: nonASCII},
	}, nil); err == nil {
		t.Fatal("non-ASCII token accepted")
	}
}

func TestRegistryRejectsUnconfiguredOrMismatchedInstallation(t *testing.T) {
	registry, err := NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation := testOperation()
	outcome := registry.Publish(context.Background(), operation)
	if outcome.Kind != v1.SourcePublicationPermanent || outcome.Code != v1.SourceControlInstallationRevoked {
		t.Fatalf("outcome = %+v", outcome)
	}

	registry, err = NewRegistry(map[string]config.SourcePublicationInstallationConfig{
		"installation-1": {Provider: "github", TokenFile: writeToken(t, 0o600)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation.Publication.Provider = "gitlab"
	outcome = registry.Publish(context.Background(), operation)
	if outcome.Kind != v1.SourcePublicationPermanent || outcome.Code != v1.SourceControlPermissionDenied {
		t.Fatalf("mismatched provider outcome = %+v", outcome)
	}
}

func testRegistryForServer(t *testing.T, server *httptest.Server) *Registry {
	t.Helper()
	registry, err := NewRegistry(map[string]config.SourcePublicationInstallationConfig{
		"installation-1": {Provider: "github", APIBaseURL: server.URL, TokenFile: writeToken(t, 0o600)},
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func testOperation() v1.SourcePublicationOperation {
	return v1.SourcePublicationOperation{Publication: v1.SourcePublicationCommand{
		SchemaVersion: v1.SourcePublicationSchemaVersion,
		Provider:      "github", InstallationID: "installation-1", RepositoryID: "repository-1",
		RepositoryExternalID: "octo/example", BaseBranch: "main", HeadBranch: "job/feature-1",
		HeadSHA:        "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		IdempotencyKey: "source-publish-cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Title:          "Implement ABC-123",
	}}
}

func assertBearerToken(t *testing.T, request *http.Request) {
	t.Helper()
	if got := request.Header.Get("Authorization"); got != "Bearer github-token-123" {
		t.Fatalf("authorization = %q", got)
	}
}
