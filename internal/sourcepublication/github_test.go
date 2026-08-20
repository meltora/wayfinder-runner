package sourcepublication

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestGitHubPublicationCreatesExactPullRequest(t *testing.T) {
	var createCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertBearerToken(t, request)
		switch request.Method + " " + request.URL.Path {
		case "GET /repos/octo/example":
			_, _ = writer.Write([]byte(`{"id":1}`))
		case "GET /repos/octo/example/git/ref/heads/job/feature-1":
			_, _ = writer.Write([]byte(`{"object":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`))
		case "GET /repos/octo/example/pulls":
			_, _ = writer.Write([]byte(`[]`))
		case "POST /repos/octo/example/pulls":
			createCalls.Add(1)
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["head"] != "job/feature-1" || body["base"] != "main" || body["title"] != "Implement ABC-123" {
				t.Fatalf("body = %#v", body)
			}
			_, _ = writer.Write([]byte(`{"number":42,"html_url":"https://github.example/octo/example/pull/42","state":"open","head":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"base":{"ref":"main"}}`))
		default:
			http.Error(writer, request.Method+" "+request.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()

	outcome := testRegistryForServer(t, server).Publish(t.Context(), testOperation())
	if outcome.Kind != v1.SourcePublicationSucceeded || outcome.Result == nil {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.Result.ExternalRequestID != "42" || outcome.Result.URL != "https://github.example/octo/example/pull/42" ||
		outcome.Result.HeadSHA != testOperation().Publication.HeadSHA || outcome.Result.State != "open" {
		t.Fatalf("result = %+v", outcome.Result)
	}
	if createCalls.Load() != 1 {
		t.Fatalf("create calls = %d", createCalls.Load())
	}
}

func TestGitHubPublicationReusesExistingExactPullRequest(t *testing.T) {
	var createCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertBearerToken(t, request)
		switch request.Method + " " + request.URL.Path {
		case "GET /repos/octo/example":
			_, _ = writer.Write([]byte(`{"id":1}`))
		case "GET /repos/octo/example/git/ref/heads/job/feature-1":
			_, _ = writer.Write([]byte(`{"object":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`))
		case "GET /repos/octo/example/pulls":
			_, _ = writer.Write([]byte(`[{"number":7,"html_url":"https://github.example/octo/example/pull/7","state":"open","head":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"base":{"ref":"main"}}]`))
		case "POST /repos/octo/example/pulls":
			createCalls.Add(1)
			http.Error(writer, "unexpected create", http.StatusInternalServerError)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	outcome := testRegistryForServer(t, server).Publish(t.Context(), testOperation())
	if outcome.Kind != v1.SourcePublicationSucceeded || outcome.Result == nil || outcome.Result.ExternalRequestID != "7" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if createCalls.Load() != 0 {
		t.Fatalf("idempotent replay created %d requests", createCalls.Load())
	}
}

func TestGitHubPublicationFailsClosedOnHeadMismatch(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertBearerToken(t, request)
		if request.URL.Path == "/repos/octo/example" {
			_, _ = writer.Write([]byte(`{"id":1}`))
			return
		}
		_, _ = writer.Write([]byte(`{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`))
	}))
	defer server.Close()

	outcome := testRegistryForServer(t, server).Publish(t.Context(), testOperation())
	if outcome.Kind != v1.SourcePublicationPermanent || outcome.Code != v1.SourceControlHeadMismatch {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGitHubPublicationClassifiesRateLimitWithoutLeakingProviderBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertBearerToken(t, request)
		writer.Header().Set("Retry-After", "30")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"message":"token github-secret leaked"}`))
	}))
	defer server.Close()

	outcome := testRegistryForServer(t, server).Publish(t.Context(), testOperation())
	if outcome.Kind != v1.SourcePublicationRetryable || outcome.Code != v1.SourceControlRateLimited ||
		outcome.RetryAfterMS == nil || *outcome.RetryAfterMS != 30000 {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGitHubPublicationRetriesEveryTransientHTTPFailure(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusNotImplemented} {
		outcome := classifyGitHubFailure(githubResponse{status: status, header: make(http.Header)}, v1.SourceControlRepositoryNotFound)
		if outcome.Kind != v1.SourcePublicationRetryable || outcome.Code != v1.SourceControlProviderUnavailable {
			t.Fatalf("status %d outcome = %+v", status, outcome)
		}
	}
}

func TestGitHubPublicationClassifiesPermanentHTTPFailures(t *testing.T) {
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
			outcome := classifyGitHubFailure(
				githubResponse{status: test.status, header: make(http.Header)},
				v1.SourceControlRepositoryNotFound,
			)
			if outcome.Kind != v1.SourcePublicationPermanent || outcome.Code != test.code {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}
}

func TestProviderRetryAfterAcceptsOnlyBoundedFutureHints(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	tests := map[string]struct {
		value string
		want  uint64
		ok    bool
	}{
		"seconds":   {value: "30", want: 30000, ok: true},
		"HTTP date": {value: now.Add(2 * time.Minute).Format(http.TimeFormat), want: 120000, ok: true},
		"past":      {value: now.Add(-time.Minute).Format(http.TimeFormat)},
		"too long":  {value: "86401"},
		"malformed": {value: "later"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			header := make(http.Header)
			header.Set("Retry-After", test.value)
			got := providerRetryAfter(header, now)
			if !test.ok {
				if got != nil {
					t.Fatalf("retry delay = %d", *got)
				}
				return
			}
			if got == nil || *got != test.want {
				t.Fatalf("retry delay = %v, want %d", got, test.want)
			}
		})
	}
}

func TestGitHubPublicationRejectsExistingPullRequestAtDifferentHead(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/octo/example":
			_, _ = writer.Write([]byte(`{"id":1}`))
		case "/repos/octo/example/git/ref/heads/job/feature-1":
			_, _ = writer.Write([]byte(`{"object":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`))
		default:
			_, _ = writer.Write([]byte(`[{"number":7,"state":"open","head":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"base":{"ref":"main"}}]`))
		}
	}))
	defer server.Close()

	outcome := testRegistryForServer(t, server).Publish(t.Context(), testOperation())
	if outcome.Kind != v1.SourcePublicationPermanent || outcome.Code != v1.SourceControlHeadMismatch {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGitHubRepositoryIdentifierValidation(t *testing.T) {
	for _, value := range []string{"owner", "owner/repo/extra", "../repo", "owner/..", "owner/repo name"} {
		if _, _, ok := githubRepository(value); ok {
			t.Fatalf("invalid repository identifier %q accepted", value)
		}
	}
}

func TestGitHubPublicationRejectsOversizedProviderResponsePermanently(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(bytes.Repeat([]byte("x"), maxProviderResponseBytes+1))
	}))
	defer server.Close()

	outcome := testRegistryForServer(t, server).Publish(t.Context(), testOperation())
	if outcome.Kind != v1.SourcePublicationPermanent || outcome.Code != v1.SourceControlInvalidProviderResponse {
		t.Fatalf("outcome = %+v", outcome)
	}
}
