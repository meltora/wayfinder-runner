package sourcepublication

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const pullRequestHead = "e31aeb2649bce0a55feed5433206cfb68f4fa94c"

type pullRequestServer struct {
	*httptest.Server
	mu         sync.Mutex
	repository string
	pull       string
	merge      func(http.ResponseWriter, *http.Request)
	writes     []map[string]string
	auth       []string
}

func newPullRequestServer(t *testing.T) *pullRequestServer {
	t.Helper()
	server := &pullRequestServer{
		repository: `{"id":123,"full_name":"acme/repo","allow_merge_commit":false,"allow_squash_merge":true,"allow_rebase_merge":true}`,
		pull:       `{"number":7,"state":"open","merged":false,"mergeable":true,"merge_commit_sha":null,"head":{"sha":"` + pullRequestHead + `"}}`,
	}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.mu.Lock()
		server.auth = append(server.auth, r.Header.Get("Authorization"))
		repository, pull, merge := server.repository, server.pull, server.merge
		server.mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /repositories/123":
			_, _ = io.WriteString(w, repository)
		case "GET /repos/acme/repo/pulls/7":
			_, _ = io.WriteString(w, pull)
		case "PUT /repos/acme/repo/pulls/7/merge":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			server.mu.Lock()
			server.writes = append(server.writes, body)
			server.mu.Unlock()
			if merge != nil {
				merge(w, r)
				return
			}
			_, _ = io.WriteString(w, `{"merged":true,"sha":"`+strings.Repeat("f", 40)+`"}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func pullRequestOperationForTest(server *httptest.Server, operation string) v1.ProviderPullRequestAuthority {
	id := "22345678-1234-4234-8234-123456789abc"
	a := v1.ProviderPullRequestAuthority{AuthorityVersion: v1.ProviderPullRequestTransportVersion, OperationID: id, Operation: operation, OrganizationID: "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1", RepositoryID: "repository-1", Provider: "github", InstallationID: "installation-1", ProviderInstanceKey: reviewInstanceKey(server), ProviderRepositoryID: "123", PullRequestNumber: "7", ExpectedHeadSHA: pullRequestHead, LeaseID: v1.ProviderPullRequestLeaseID(id), FencingToken: 1, LeaseExpiresAt: v1.ProviderPullRequestTime(time.Now().Add(5 * time.Minute))}
	if operation == v1.ProviderPullRequestOperationMerge {
		method := "squash"
		a.MergeMethod = &method
	}
	a.CommandDigest = v1.ProviderPullRequestCommandDigest(a)
	return a
}

func TestProviderPullRequestObservationReportsStateAndAllowedMethodsWithoutWrites(t *testing.T) {
	server := newPullRequestServer(t)
	out := reviewRegistry(t, server.Server, false).RunProviderPullRequest(t.Context(), pullRequestOperationForTest(server.Server, v1.ProviderPullRequestOperationObserve), commandCredential(time.Hour, "123"))
	if out.Kind != v1.ProviderPullRequestOutcomeObserved || out.State != "open" || out.HeadSHA != pullRequestHead || out.Mergeable == nil || !*out.Mergeable || !reflect.DeepEqual(out.AllowedMergeMethods, []string{"squash", "rebase"}) || out.MergeCommitSHA != nil {
		t.Fatalf("out = %#v", out)
	}
	if len(server.writes) != 0 {
		t.Fatalf("writes = %#v", server.writes)
	}
	for _, authorization := range server.auth {
		if authorization != "Bearer "+commandCredentialToken {
			t.Fatalf("authorization = %q", authorization)
		}
	}
}

func TestProviderPullRequestObservationReportsExternalMergeAndUnknownSettings(t *testing.T) {
	server := newPullRequestServer(t)
	server.repository = `{"id":123,"full_name":"acme/repo"}`
	server.pull = `{"number":7,"state":"closed","merged":true,"mergeable":null,"merge_commit_sha":"` + strings.Repeat("a", 40) + `","head":{"sha":"` + pullRequestHead + `"}}`
	out := reviewRegistry(t, server.Server, false).RunProviderPullRequest(t.Context(), pullRequestOperationForTest(server.Server, v1.ProviderPullRequestOperationObserve), commandCredential(time.Hour, "123"))
	if out.Kind != v1.ProviderPullRequestOutcomeObserved || out.State != "merged" || out.MergeCommitSHA == nil || out.AllowedMergeMethods != nil || out.Mergeable != nil {
		t.Fatalf("out = %#v", out)
	}
}

func TestProviderPullRequestMergeBindsExactHeadAndMethod(t *testing.T) {
	server := newPullRequestServer(t)
	out := reviewRegistry(t, server.Server, false).RunProviderPullRequest(t.Context(), pullRequestOperationForTest(server.Server, v1.ProviderPullRequestOperationMerge), commandCredential(time.Hour, "123"))
	if out.Kind != v1.ProviderPullRequestOutcomeMerged || out.MergeCommitSHA == nil || *out.MergeCommitSHA != strings.Repeat("f", 40) {
		t.Fatalf("out = %#v", out)
	}
	if len(server.writes) != 1 || server.writes[0]["sha"] != pullRequestHead || server.writes[0]["merge_method"] != "squash" {
		t.Fatalf("writes = %#v", server.writes)
	}
}

func TestProviderPullRequestMergeRefusesBeforeWritingWhenAuthorityDrifted(t *testing.T) {
	for name, setup := range map[string]struct {
		mutate func(*pullRequestServer)
		code   v1.ProviderPullRequestErrorCode
	}{
		"head changed": {func(s *pullRequestServer) {
			s.pull = `{"number":7,"state":"open","merged":false,"head":{"sha":"` + strings.Repeat("b", 40) + `"}}`
		}, v1.ProviderPullRequestHeadChanged},
		"closed": {func(s *pullRequestServer) {
			s.pull = `{"number":7,"state":"closed","merged":false,"head":{"sha":"` + pullRequestHead + `"}}`
		}, v1.ProviderPullRequestNotOpen},
		"method disabled": {func(s *pullRequestServer) {
			s.repository = `{"id":123,"full_name":"acme/repo","allow_merge_commit":true,"allow_squash_merge":false,"allow_rebase_merge":false}`
		}, v1.ProviderPullRequestMergeMethodNotAllowed},
		"repository swapped": {func(s *pullRequestServer) { s.repository = `{"id":999,"full_name":"acme/repo"}` }, v1.ProviderPullRequestRepositoryMismatch},
		"pull missing": {func(s *pullRequestServer) {
			s.pull = `{"number":8,"state":"open","head":{"sha":"` + pullRequestHead + `"}}`
		}, v1.ProviderPullRequestNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			server := newPullRequestServer(t)
			setup.mutate(server)
			out := reviewRegistry(t, server.Server, false).RunProviderPullRequest(t.Context(), pullRequestOperationForTest(server.Server, v1.ProviderPullRequestOperationMerge), commandCredential(time.Hour, "123"))
			if out.Kind != v1.ProviderPullRequestOutcomeFailed || out.Code != setup.code || len(server.writes) != 0 {
				t.Fatalf("out=%#v writes=%#v", out, server.writes)
			}
		})
	}
}

func TestProviderPullRequestMergeIsIdempotentWhenAlreadyMergedAtExpectedHead(t *testing.T) {
	server := newPullRequestServer(t)
	server.pull = `{"number":7,"state":"closed","merged":true,"merge_commit_sha":"` + strings.Repeat("c", 40) + `","head":{"sha":"` + pullRequestHead + `"}}`
	out := reviewRegistry(t, server.Server, false).RunProviderPullRequest(t.Context(), pullRequestOperationForTest(server.Server, v1.ProviderPullRequestOperationMerge), commandCredential(time.Hour, "123"))
	if out.Kind != v1.ProviderPullRequestOutcomeMerged || *out.MergeCommitSHA != strings.Repeat("c", 40) || len(server.writes) != 0 {
		t.Fatalf("out=%#v writes=%#v", out, server.writes)
	}
}

func TestProviderPullRequestMergeMapsProviderRefusals(t *testing.T) {
	for status, code := range map[int]v1.ProviderPullRequestErrorCode{
		http.StatusMethodNotAllowed:    v1.ProviderPullRequestNotMergeable,
		http.StatusUnprocessableEntity: v1.ProviderPullRequestNotMergeable,
		http.StatusConflict:            v1.ProviderPullRequestHeadChanged,
		http.StatusForbidden:           v1.ProviderPullRequestPermissionDenied,
		http.StatusNotFound:            v1.ProviderPullRequestNotFound,
		http.StatusTooManyRequests:     v1.ProviderPullRequestRateLimited,
		http.StatusBadGateway:          v1.ProviderPullRequestProviderUnavailable,
	} {
		server := newPullRequestServer(t)
		server.merge = func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"message":"`+commandCredentialToken+`"}`)
		}
		out := reviewRegistry(t, server.Server, false).RunProviderPullRequest(t.Context(), pullRequestOperationForTest(server.Server, v1.ProviderPullRequestOperationMerge), commandCredential(time.Hour, "123"))
		encoded, _ := json.Marshal(out)
		if out.Kind != v1.ProviderPullRequestOutcomeFailed || out.Code != code || strings.Contains(string(encoded), commandCredentialToken) {
			t.Fatalf("status %d out=%s", status, encoded)
		}
	}
}

func TestProviderPullRequestRequiresCredentialCoverageWithoutNetworkCalls(t *testing.T) {
	server := newPullRequestServer(t)
	registry := reviewRegistry(t, server.Server, false)
	op := pullRequestOperationForTest(server.Server, v1.ProviderPullRequestOperationMerge)
	if out := registry.RunProviderPullRequest(t.Context(), op, commandCredential(time.Hour, "456")); out.Code != v1.ProviderPullRequestPermissionDenied {
		t.Fatalf("uncovered repository = %#v", out)
	}
	if out := registry.RunProviderPullRequest(t.Context(), op, nil); out.Code != v1.ProviderPullRequestProviderUnavailable {
		t.Fatalf("missing credential = %#v", out)
	}
	op.InstallationID = "installation-2"
	if out := registry.RunProviderPullRequest(t.Context(), op, commandCredential(time.Hour, "123")); out.Code != v1.ProviderPullRequestInstallationInactive {
		t.Fatalf("unknown installation = %#v", out)
	}
	if len(server.auth) != 0 {
		t.Fatalf("requests = %d", len(server.auth))
	}
	if got := registry.ReadyProviderPullRequestInstallations(); len(got) != 1 || got[0].InstallationID != "installation-1" || got[0].Provider != "github" {
		t.Fatalf("ready = %#v", got)
	}
}
