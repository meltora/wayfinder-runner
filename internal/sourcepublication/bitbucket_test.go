package sourcepublication

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

func TestBitbucketPublicationCreatesExactPullRequest(t *testing.T) {
	var createCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertBitbucketToken(t, request)
		switch request.Method + " " + request.URL.Path {
		case "GET /2.0/repositories/acme/example":
			_, _ = writer.Write([]byte(`{"uuid":"{repository-uuid}","full_name":"acme/example"}`))
		case "GET /2.0/repositories/acme/example/refs/branches/job/feature-1":
			if request.URL.EscapedPath() != "/2.0/repositories/acme/example/refs/branches/job%2Ffeature-1" {
				t.Fatalf("branch path = %q", request.URL.EscapedPath())
			}
			_, _ = writer.Write([]byte(`{"name":"job/feature-1","target":{"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`))
		case "GET /2.0/repositories/acme/example/pullrequests":
			assertBitbucketPullRequestQuery(t, request.URL.Query())
			_, _ = writer.Write([]byte(`{"pagelen":50,"values":[]}`))
		case "POST /2.0/repositories/acme/example/pullrequests":
			createCalls.Add(1)
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			assertBitbucketCreateBody(t, body)
			_, _ = writer.Write(bitbucketPullRequestJSON(42, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
		default:
			http.Error(writer, request.Method+" "+request.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()

	outcome := testBitbucketRegistryForServer(t, server).Publish(t.Context(), testBitbucketOperation(), nil)
	if outcome.Kind != v1.SourcePublicationSucceeded || outcome.Result == nil {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.Result.ExternalRequestID != "42" ||
		outcome.Result.URL != "https://bitbucket.example/acme/example/pull-requests/42" ||
		outcome.Result.HeadSHA != testBitbucketOperation().Publication.HeadSHA || outcome.Result.State != "open" {
		t.Fatalf("result = %+v", outcome.Result)
	}
	if createCalls.Load() != 1 {
		t.Fatalf("create calls = %d", createCalls.Load())
	}
}

func TestBitbucketPublicationReusesExistingExactPullRequest(t *testing.T) {
	var createCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /2.0/repositories/acme/example":
			_, _ = writer.Write([]byte(`{"uuid":"{repository-uuid}","full_name":"acme/example"}`))
		case "GET /2.0/repositories/acme/example/refs/branches/job/feature-1":
			_, _ = writer.Write([]byte(`{"name":"job/feature-1","target":{"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`))
		case "GET /2.0/repositories/acme/example/pullrequests":
			_, _ = writer.Write([]byte(`{"pagelen":50,"values":[`))
			_, _ = writer.Write(bitbucketPullRequestJSON(7, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
			_, _ = writer.Write([]byte(`]}`))
		case "POST /2.0/repositories/acme/example/pullrequests":
			createCalls.Add(1)
			http.Error(writer, "unexpected create", http.StatusInternalServerError)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	outcome := testBitbucketRegistryForServer(t, server).Publish(t.Context(), testBitbucketOperation(), nil)
	if outcome.Kind != v1.SourcePublicationSucceeded || outcome.Result == nil || outcome.Result.ExternalRequestID != "7" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if createCalls.Load() != 0 {
		t.Fatalf("idempotent replay created %d requests", createCalls.Load())
	}
}

func TestBitbucketPublicationReconcilesCreateConflict(t *testing.T) {
	var listCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /2.0/repositories/acme/example":
			_, _ = writer.Write([]byte(`{"uuid":"{repository-uuid}","full_name":"acme/example"}`))
		case "GET /2.0/repositories/acme/example/refs/branches/job/feature-1":
			_, _ = writer.Write([]byte(`{"name":"job/feature-1","target":{"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`))
		case "GET /2.0/repositories/acme/example/pullrequests":
			if listCalls.Add(1) == 1 {
				_, _ = writer.Write([]byte(`{"pagelen":50,"values":[]}`))
				return
			}
			_, _ = writer.Write([]byte(`{"pagelen":50,"values":[`))
			_, _ = writer.Write(bitbucketPullRequestJSON(9, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
			_, _ = writer.Write([]byte(`]}`))
		case "POST /2.0/repositories/acme/example/pullrequests":
			writer.WriteHeader(http.StatusConflict)
			_, _ = writer.Write([]byte(`{"error":{"message":"private provider detail"}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	outcome := testBitbucketRegistryForServer(t, server).Publish(t.Context(), testBitbucketOperation(), nil)
	if outcome.Kind != v1.SourcePublicationSucceeded || outcome.Result == nil || outcome.Result.ExternalRequestID != "9" {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestBitbucketPublicationFailsClosedOnHeadMismatch(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/2.0/repositories/acme/example" {
			_, _ = writer.Write([]byte(`{"uuid":"{repository-uuid}","full_name":"acme/example"}`))
			return
		}
		_, _ = writer.Write([]byte(`{"name":"job/feature-1","target":{"hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`))
	}))
	defer server.Close()

	outcome := testBitbucketRegistryForServer(t, server).Publish(t.Context(), testBitbucketOperation(), nil)
	if outcome.Kind != v1.SourcePublicationPermanent || outcome.Code != v1.SourceControlHeadMismatch {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestBitbucketPublicationRejectsAmbiguousPagination(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/2.0/repositories/acme/example":
			_, _ = writer.Write([]byte(`{"uuid":"{repository-uuid}","full_name":"acme/example"}`))
		case "/2.0/repositories/acme/example/refs/branches/job/feature-1":
			_, _ = writer.Write([]byte(`{"name":"job/feature-1","target":{"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`))
		default:
			_, _ = writer.Write([]byte(`{"pagelen":50,"next":"https://api.bitbucket.org/2.0/private-page","values":[]}`))
		}
	}))
	defer server.Close()

	outcome := testBitbucketRegistryForServer(t, server).Publish(t.Context(), testBitbucketOperation(), nil)
	if outcome.Kind != v1.SourcePublicationPermanent || outcome.Code != v1.SourceControlInvalidProviderResponse {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestBitbucketPublicationRejectsMissingPaginationValues(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"pagelen":50}`))
	}))
	defer server.Close()
	publisher := newBitbucketPublisher(server.URL+"/2.0", "private-token", server.Client())

	_, found, failure := publisher.findExisting(
		t.Context(),
		"/repositories/acme/example",
		testBitbucketOperation().Publication,
	)
	if found || failure == nil || failure.Kind != v1.SourcePublicationPermanent ||
		failure.Code != v1.SourceControlInvalidProviderResponse {
		t.Fatalf("found = %v, failure = %+v", found, failure)
	}
}

func TestBitbucketPullRequestFilterEscapesProviderQueryText(t *testing.T) {
	command := testBitbucketOperation().Publication
	command.HeadBranch = `job/"quoted"`
	filter := bitbucketPullRequestFilter(command)
	if filter != `state = "OPEN" AND source.repository.full_name = "acme/example" AND source.branch.name = "job/\"quoted\"" AND destination.branch.name = "main"` {
		t.Fatalf("filter = %q", filter)
	}
}

func TestBitbucketPublicationClassifiesHTTPFailures(t *testing.T) {
	for name, test := range map[string]struct {
		status int
		kind   v1.SourcePublicationOutcomeKind
		code   v1.SourceControlErrorCode
	}{
		"authentication": {status: http.StatusUnauthorized, kind: v1.SourcePublicationPermanent, code: v1.SourceControlAuthenticationFailed},
		"permission":     {status: http.StatusForbidden, kind: v1.SourcePublicationPermanent, code: v1.SourceControlPermissionDenied},
		"repository":     {status: http.StatusNotFound, kind: v1.SourcePublicationPermanent, code: v1.SourceControlRepositoryNotFound},
		"rate limit":     {status: http.StatusTooManyRequests, kind: v1.SourcePublicationRetryable, code: v1.SourceControlRateLimited},
		"timeout":        {status: http.StatusRequestTimeout, kind: v1.SourcePublicationRetryable, code: v1.SourceControlProviderUnavailable},
		"server":         {status: http.StatusNotImplemented, kind: v1.SourcePublicationRetryable, code: v1.SourceControlProviderUnavailable},
		"malformed":      {status: http.StatusTeapot, kind: v1.SourcePublicationPermanent, code: v1.SourceControlInvalidProviderResponse},
	} {
		t.Run(name, func(t *testing.T) {
			outcome := classifyBitbucketFailure(
				bitbucketResponse{status: test.status, header: make(http.Header)},
				v1.SourceControlRepositoryNotFound,
			)
			if outcome.Kind != test.kind || outcome.Code != test.code {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}
}

func TestBitbucketPublicationRefusesRedirectAndOversizedResponses(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
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
		outcome := testBitbucketRegistryForServer(t, server).Publish(t.Context(), testBitbucketOperation(), nil)
		if outcome.Kind != v1.SourcePublicationPermanent || redirectedCalls.Load() != 0 {
			t.Fatalf("outcome = %+v redirected = %d", outcome, redirectedCalls.Load())
		}
	})

	t.Run("oversized", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write(bytes.Repeat([]byte("x"), maxProviderResponseBytes+1))
		}))
		defer server.Close()
		outcome := testBitbucketRegistryForServer(t, server).Publish(t.Context(), testBitbucketOperation(), nil)
		if outcome.Kind != v1.SourcePublicationPermanent || outcome.Code != v1.SourceControlInvalidProviderResponse {
			t.Fatalf("outcome = %+v", outcome)
		}
	})
}

func testBitbucketRegistryForServer(t *testing.T, server *httptest.Server) *Registry {
	t.Helper()
	registry, err := NewRegistry(map[string]config.SourcePublicationInstallationConfig{
		"installation-1": {Provider: "bitbucket", APIBaseURL: server.URL + "/2.0", TokenFile: writeToken(t, 0o600)},
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func testBitbucketOperation() v1.SourcePublicationOperation {
	operation := testOperation()
	operation.Publication.Provider = "bitbucket"
	operation.Publication.RepositoryExternalID = "acme/example"
	return operation
}

func assertBitbucketToken(t *testing.T, request *http.Request) {
	t.Helper()
	if got := request.Header.Get("Authorization"); got != "Bearer github-token-123" {
		t.Fatalf("Authorization = %q", got)
	}
}

func assertBitbucketPullRequestQuery(t *testing.T, query url.Values) {
	t.Helper()
	wantFilter := `state = "OPEN" AND source.repository.full_name = "acme/example" AND source.branch.name = "job/feature-1" AND destination.branch.name = "main"`
	if len(query) != 2 || query.Get("q") != wantFilter || query.Get("pagelen") != "50" {
		t.Fatalf("query = %v", query)
	}
}

func assertBitbucketCreateBody(t *testing.T, body map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"destination":{"branch":{"name":"main"}},"source":{"branch":{"name":"job/feature-1"}},"title":"Implement ABC-123"}`
	if string(encoded) != want {
		t.Fatalf("body = %s", encoded)
	}
}

func bitbucketPullRequestJSON(id int, sha string) []byte {
	number := strconv.Itoa(id)
	return []byte(`{"id":` + number + `,"state":"OPEN","links":{"html":{"href":"https://bitbucket.example/acme/example/pull-requests/` + number + `"}},"source":{"branch":{"name":"job/feature-1"},"commit":{"hash":"` + sha + `"},"repository":{"full_name":"acme/example"}},"destination":{"branch":{"name":"main"},"repository":{"full_name":"acme/example"}}}`)
}
