package sourcepublication

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

func TestGitLabPublicationCreatesExactMergeRequest(t *testing.T) {
	var createCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertGitLabToken(t, request)
		assertEncodedGitLabProject(t, request)
		switch request.Method + " " + request.URL.Path {
		case "GET /api/v4/projects/group/subgroup/example":
			_, _ = writer.Write([]byte(`{"id":17}`))
		case "GET /api/v4/projects/group/subgroup/example/repository/branches/job/feature-1":
			_, _ = writer.Write([]byte(`{"name":"job/feature-1","commit":{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`))
		case "GET /api/v4/projects/group/subgroup/example/merge_requests":
			assertGitLabMergeRequestQuery(t, request.URL.Query())
			_, _ = writer.Write([]byte(`[]`))
		case "POST /api/v4/projects/group/subgroup/example/merge_requests":
			createCalls.Add(1)
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["source_branch"] != "job/feature-1" || body["target_branch"] != "main" || body["title"] != "Implement ABC-123" {
				t.Fatalf("body = %#v", body)
			}
			_, _ = writer.Write([]byte(`{"iid":42,"web_url":"https://gitlab.example/group/subgroup/example/-/merge_requests/42","state":"opened","source_branch":"job/feature-1","target_branch":"main","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`))
		default:
			http.Error(writer, request.Method+" "+request.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()

	outcome := testGitLabRegistryForServer(t, server).Publish(t.Context(), testGitLabOperation(), nil)
	if outcome.Kind != v1.SourcePublicationSucceeded || outcome.Result == nil {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.Result.ExternalRequestID != "42" ||
		outcome.Result.URL != "https://gitlab.example/group/subgroup/example/-/merge_requests/42" ||
		outcome.Result.HeadSHA != testGitLabOperation().Publication.HeadSHA || outcome.Result.State != "open" {
		t.Fatalf("result = %+v", outcome.Result)
	}
	if createCalls.Load() != 1 {
		t.Fatalf("create calls = %d", createCalls.Load())
	}
}

func TestGitLabPublicationReusesExistingExactMergeRequest(t *testing.T) {
	var createCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertGitLabToken(t, request)
		switch request.Method + " " + request.URL.Path {
		case "GET /api/v4/projects/group/subgroup/example":
			_, _ = writer.Write([]byte(`{"id":17}`))
		case "GET /api/v4/projects/group/subgroup/example/repository/branches/job/feature-1":
			_, _ = writer.Write([]byte(`{"commit":{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`))
		case "GET /api/v4/projects/group/subgroup/example/merge_requests":
			_, _ = writer.Write([]byte(`[{"iid":7,"web_url":"https://gitlab.example/group/subgroup/example/-/merge_requests/7","state":"opened","source_branch":"job/feature-1","target_branch":"main","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]`))
		case "POST /api/v4/projects/group/subgroup/example/merge_requests":
			createCalls.Add(1)
			http.Error(writer, "unexpected create", http.StatusInternalServerError)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	outcome := testGitLabRegistryForServer(t, server).Publish(t.Context(), testGitLabOperation(), nil)
	if outcome.Kind != v1.SourcePublicationSucceeded || outcome.Result == nil || outcome.Result.ExternalRequestID != "7" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if createCalls.Load() != 0 {
		t.Fatalf("idempotent replay created %d requests", createCalls.Load())
	}
}

func TestGitLabPublicationReconcilesCreateConflict(t *testing.T) {
	var listCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /api/v4/projects/group/subgroup/example":
			_, _ = writer.Write([]byte(`{"id":17}`))
		case "GET /api/v4/projects/group/subgroup/example/repository/branches/job/feature-1":
			_, _ = writer.Write([]byte(`{"commit":{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`))
		case "GET /api/v4/projects/group/subgroup/example/merge_requests":
			if listCalls.Add(1) == 1 {
				_, _ = writer.Write([]byte(`[]`))
				return
			}
			_, _ = writer.Write([]byte(`[{"iid":9,"web_url":"https://gitlab.example/group/subgroup/example/-/merge_requests/9","state":"opened","source_branch":"job/feature-1","target_branch":"main","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]`))
		case "POST /api/v4/projects/group/subgroup/example/merge_requests":
			writer.WriteHeader(http.StatusConflict)
			_, _ = writer.Write([]byte(`{"message":"already exists; private provider detail"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	outcome := testGitLabRegistryForServer(t, server).Publish(t.Context(), testGitLabOperation(), nil)
	if outcome.Kind != v1.SourcePublicationSucceeded || outcome.Result == nil || outcome.Result.ExternalRequestID != "9" {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGitLabPublicationFailsClosedOnHeadMismatch(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v4/projects/group/subgroup/example" {
			_, _ = writer.Write([]byte(`{"id":17}`))
			return
		}
		_, _ = writer.Write([]byte(`{"commit":{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`))
	}))
	defer server.Close()

	outcome := testGitLabRegistryForServer(t, server).Publish(t.Context(), testGitLabOperation(), nil)
	if outcome.Kind != v1.SourcePublicationPermanent || outcome.Code != v1.SourceControlHeadMismatch {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGitLabPublicationClassifiesRateLimitWithoutLeakingProviderBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Retry-After", "30")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"message":"private-token leaked"}`))
	}))
	defer server.Close()

	outcome := testGitLabRegistryForServer(t, server).Publish(t.Context(), testGitLabOperation(), nil)
	if outcome.Kind != v1.SourcePublicationRetryable || outcome.Code != v1.SourceControlRateLimited ||
		outcome.RetryAfterMS == nil || *outcome.RetryAfterMS != 30000 {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGitLabPublicationRetriesEveryTransientHTTPFailure(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusNotImplemented} {
		outcome := classifyGitLabFailure(gitLabResponse{status: status, header: make(http.Header)}, v1.SourceControlRepositoryNotFound)
		if outcome.Kind != v1.SourcePublicationRetryable || outcome.Code != v1.SourceControlProviderUnavailable {
			t.Fatalf("status %d outcome = %+v", status, outcome)
		}
	}
}

func TestGitLabPublicationClassifiesPermanentHTTPFailures(t *testing.T) {
	for name, test := range map[string]struct {
		status int
		code   v1.SourceControlErrorCode
	}{
		"authentication": {status: http.StatusUnauthorized, code: v1.SourceControlAuthenticationFailed},
		"permission":     {status: http.StatusForbidden, code: v1.SourceControlPermissionDenied},
		"repository":     {status: http.StatusNotFound, code: v1.SourceControlRepositoryNotFound},
		"malformed":      {status: http.StatusTeapot, code: v1.SourceControlInvalidProviderResponse},
	} {
		t.Run(name, func(t *testing.T) {
			outcome := classifyGitLabFailure(
				gitLabResponse{status: test.status, header: make(http.Header)},
				v1.SourceControlRepositoryNotFound,
			)
			if outcome.Kind != v1.SourcePublicationPermanent || outcome.Code != test.code {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}
}

func TestGitLabPublicationRefusesRedirectWithoutForwardingToken(t *testing.T) {
	var redirectedCalls atomic.Int32
	redirected := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedCalls.Add(1)
	}))
	defer redirected.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", redirected.URL+"/capture")
		writer.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	outcome := testGitLabRegistryForServer(t, server).Publish(t.Context(), testGitLabOperation(), nil)
	if outcome.Kind != v1.SourcePublicationPermanent || outcome.Code != v1.SourceControlInvalidProviderResponse {
		t.Fatalf("outcome = %+v", outcome)
	}
	if redirectedCalls.Load() != 0 {
		t.Fatal("publication client followed a redirect and exposed its credential boundary")
	}
}

func TestGitLabPublicationRejectsOversizedProviderResponsePermanently(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(bytes.Repeat([]byte("x"), maxProviderResponseBytes+1))
	}))
	defer server.Close()

	outcome := testGitLabRegistryForServer(t, server).Publish(t.Context(), testGitLabOperation(), nil)
	if outcome.Kind != v1.SourcePublicationPermanent || outcome.Code != v1.SourceControlInvalidProviderResponse {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func testGitLabRegistryForServer(t *testing.T, server *httptest.Server) *Registry {
	t.Helper()
	registry, err := NewRegistry(map[string]config.SourcePublicationInstallationConfig{
		"installation-1": {Provider: "gitlab", APIBaseURL: server.URL + "/api/v4", TokenFile: writeToken(t, 0o600)},
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func testGitLabOperation() v1.SourcePublicationOperation {
	operation := testOperation()
	operation.Publication.Provider = "gitlab"
	operation.Publication.RepositoryExternalID = "group/subgroup/example"
	return operation
}

func assertGitLabToken(t *testing.T, request *http.Request) {
	t.Helper()
	if got := request.Header.Get("PRIVATE-TOKEN"); got != "github-token-123" {
		t.Fatalf("PRIVATE-TOKEN = %q", got)
	}
	if got := request.Header.Get("Authorization"); got != "" {
		t.Fatalf("unexpected Authorization header = %q", got)
	}
}

func assertEncodedGitLabProject(t *testing.T, request *http.Request) {
	t.Helper()
	if got := request.URL.EscapedPath(); got != "/api/v4/projects/group%2Fsubgroup%2Fexample" &&
		got != "/api/v4/projects/group%2Fsubgroup%2Fexample/repository/branches/job%2Ffeature-1" &&
		got != "/api/v4/projects/group%2Fsubgroup%2Fexample/merge_requests" {
		t.Fatalf("project path was not encoded as one identifier: %q", got)
	}
}

func assertGitLabMergeRequestQuery(t *testing.T, query url.Values) {
	t.Helper()
	want := map[string]string{
		"state": "opened", "source_branch": "job/feature-1", "target_branch": "main",
		"scope": "all", "page": "1", "per_page": "100",
	}
	if len(query) != len(want) {
		t.Fatalf("query = %v", query)
	}
	for key, value := range want {
		if query.Get(key) != value || len(query[key]) != 1 {
			t.Fatalf("query = %v", query)
		}
	}
}
