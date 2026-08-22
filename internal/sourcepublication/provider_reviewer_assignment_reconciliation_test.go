package sourcepublication

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

func reconciliationOperationForTest(provider string) v1.ProviderReviewerAssignmentReconciliationOperation {
	reviewers := []v1.ProviderReviewerAssignmentReconciliationIntendedReviewer{{ReviewerIdentityID: "identity-1", ProviderAccountID: "42"}}
	repositoryID, path, instance := "123", "", "github.com"
	if provider == "gitlab" {
		instance = "gitlab.com"
	}
	if provider == "bitbucket" {
		repositoryID = "{22222222-2222-4222-8222-222222222222}"
		path = "acme/repo"
		instance = "bitbucket.org"
		reviewers[0].ProviderAccountID = "bb-account-42"
	}
	return v1.ProviderReviewerAssignmentReconciliationOperation{ProviderReviewerAssignmentReconciliationOperationAuthority: v1.ProviderReviewerAssignmentReconciliationOperationAuthority{SchemaVersion: 1, ProviderReviewerAssignmentReconciliationOperationID: "provider-reviewer-assignment-reconciliation-operation-" + strings.Repeat("1", 64), OrganizationID: "organization-1", ProjectID: "project-1", WorkflowRunID: "run-1", AssignmentPlanID: "plan-1", AssignmentPlanDigest: strings.Repeat("2", 64), SourcePublicationObservationID: "observation-1", DeliveryID: "delivery-1", AssignmentOperationID: "provider-reviewer-assignment-operation-" + strings.Repeat("3", 64), AssignmentOperationDigest: strings.Repeat("4", 64), AssignmentCommandDigest: strings.Repeat("5", 64), AssignmentObservationID: "assignment-observation-1", AssignmentResultDeliveryID: "result-delivery-1", AssignmentResultPayloadChecksum: strings.Repeat("6", 64), RepositoryID: "repository-1", ProviderRepositoryID: repositoryID, ProviderRepositoryPath: path, ExternalRequestID: "7", ExpectedHeadSHA: "0123456789abcdef0123456789abcdef01234567", InstallationID: "installation-1", InstallationStateRevision: 1, InstallationAuthorityRevision: 1, InstallationAuthorityDigest: strings.Repeat("7", 64), Provider: provider, ProviderInstanceKey: instance, IdempotencyKey: "provider-reviewer-assignment-reconciliation-" + strings.Repeat("8", 64), AssignmentRecipientSetDigest: strings.Repeat("9", 64), IntendedReviewerSetDigest: v1.ProviderReviewerAssignmentReconciliationIntendedReviewerSetDigest(reviewers), OperationDigest: strings.Repeat("a", 64), CommandDigest: strings.Repeat("b", 64), LeaseID: "lease-1", FencingToken: 1, LeaseExpiresAt: time.Now().Add(time.Hour)}, IntendedReviewers: reviewers}
}

func reconcileThroughRegistry(t *testing.T, server *httptest.Server, provider string, operation v1.ProviderReviewerAssignmentReconciliationOperation) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
	t.Helper()
	token := writeToken(t, 0o600)
	installation := config.SourcePublicationInstallationConfig{Provider: provider, APIBaseURL: server.URL, TokenFile: token}
	operation.ProviderInstanceKey = configuredProviderInstanceKey(provider, server.URL)
	registry := &Registry{installations: map[string]config.SourcePublicationInstallationConfig{"installation-1": installation}, httpClient: server.Client()}
	return registry.ReconcileProviderReviewerAssignment(context.Background(), operation)
}

func TestGitHubReconciliationReadsStableRequestedAndReviewedIDsWithoutMutation(t *testing.T) {
	methods := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods[r.Method]++
		if r.Method != http.MethodGet {
			t.Errorf("mutation attempted: %s", r.Method)
			w.WriteHeader(500)
			return
		}
		switch r.URL.Path {
		case "/repositories/123":
			io.WriteString(w, `{"id":123,"full_name":"acme/repo"}`)
		case "/repos/acme/repo/pulls/7":
			io.WriteString(w, `{"number":7,"state":"open","merged":false,"head":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`)
		case "/repos/acme/repo/pulls/7/requested_reviewers":
			io.WriteString(w, `{"users":[{"id":42,"login":"old-name","type":"User"}],"teams":[]}`)
		case "/repos/acme/repo/pulls/7/reviews":
			io.WriteString(w, `[{"id":1,"state":"APPROVED","user":{"id":42,"login":"renamed","type":"User"}}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := reconcileThroughRegistry(t, server, "github", reconciliationOperationForTest("github"))
	if out.Kind != v1.ProviderReviewerAssignmentReconciliationSucceeded || out.Conclusion != v1.ProviderReviewerAssignmentReconciliationSatisfied || out.IntendedReviewers[0].Presence != v1.ProviderReviewerAssignmentReconciliationReviewed || len(methods) != 1 || methods[http.MethodGet] != 4 {
		t.Fatalf("out=%#v methods=%#v", out, methods)
	}
}

func TestGitHubReconciliationFailsClosedAtPaginationBound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repositories/123":
			io.WriteString(w, `{"id":123,"full_name":"acme/repo"}`)
		case "/repos/acme/repo/pulls/7":
			io.WriteString(w, `{"number":7,"state":"open","head":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`)
		case "/repos/acme/repo/pulls/7/requested_reviewers":
			io.WriteString(w, `{"users":[],"teams":[]}`)
		case "/repos/acme/repo/pulls/7/reviews":
			reviews := make([]map[string]any, 100)
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			for i := range reviews {
				reviews[i] = map[string]any{"id": (page-1)*100 + i + 1, "state": "COMMENTED", "user": map[string]any{"id": i + 100, "login": "user", "type": "User"}}
			}
			json.NewEncoder(w).Encode(reviews)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := newGitHubPublisher(server.URL, "secret", server.Client()).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("github"))
	if out.Code != v1.ProviderReviewerAssignmentReconciliationObservationIncomplete {
		t.Fatalf("out=%#v", out)
	}
}

func TestGitHubReconciliationAcceptsExactlyPaginationBoundWhenMetadataProvesComplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repositories/123":
			io.WriteString(w, `{"id":123,"full_name":"acme/repo"}`)
		case "/repos/acme/repo/pulls/7":
			io.WriteString(w, `{"number":7,"state":"open","head":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`)
		case "/repos/acme/repo/pulls/7/requested_reviewers":
			io.WriteString(w, `{"users":[],"teams":[]}`)
		case "/repos/acme/repo/pulls/7/reviews":
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			reviews := make([]map[string]any, 100)
			for i := range reviews {
				reviews[i] = map[string]any{"id": (page-1)*100 + i + 1, "state": "COMMENTED", "user": map[string]any{"id": i + 100, "login": "user", "type": "User"}}
			}
			if page < 10 {
				w.Header().Set("Link", "<https://api.github.com/reviews?page="+strconv.Itoa(page+1)+`>; rel="next"`)
			} else {
				w.Header().Set("Link", `<https://api.github.com/reviews?page=1>; rel="first", <https://api.github.com/reviews?page=9>; rel="prev"`)
			}
			json.NewEncoder(w).Encode(reviews)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := newGitHubPublisher(server.URL, "secret", server.Client()).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("github"))
	if out.Kind != v1.ProviderReviewerAssignmentReconciliationSucceeded {
		t.Fatalf("out=%#v", out)
	}
}

func TestGitHubReconciliationPaginationRejectsAmbiguousMetadata(t *testing.T) {
	for name, header := range map[string]string{
		"malformed":          `<https://api.github.com/reviews?page=2>; rel=next`,
		"duplicate relation": `<https://api.github.com/reviews?page=2>; rel="next", <https://api.github.com/reviews?page=3>; rel="next"`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, valid := githubReconciliationPagination(header); valid {
				t.Fatal("ambiguous pagination metadata accepted")
			}
		})
	}
}

func TestGitHubReconciliationRejectsAmbiguousReviewEvidence(t *testing.T) {
	tests := map[string]struct {
		requested string
		reviews   string
	}{"unknown state": {`{"users":[],"teams":[]}`, `[{"id":1,"state":"MYSTERY","user":{"id":42,"login":"alice","type":"User"}}]`}, "duplicate review id": {`{"users":[],"teams":[]}`, `[{"id":1,"state":"APPROVED","user":{"id":42,"login":"alice","type":"User"}},{"id":1,"state":"COMMENTED","user":{"id":42,"login":"alice","type":"User"}}]`}, "unbounded teams": {func() string {
		teams := make([]map[string]int, 101)
		for i := range teams {
			teams[i] = map[string]int{"id": i + 1}
		}
		raw, _ := json.Marshal(map[string]any{"users": []any{}, "teams": teams})
		return string(raw)
	}(), `[]`}}
	for name, fixture := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repositories/123":
					io.WriteString(w, `{"id":123,"full_name":"acme/repo"}`)
				case "/repos/acme/repo/pulls/7":
					io.WriteString(w, `{"number":7,"state":"open","head":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`)
				case "/repos/acme/repo/pulls/7/requested_reviewers":
					io.WriteString(w, fixture.requested)
				case "/repos/acme/repo/pulls/7/reviews":
					io.WriteString(w, fixture.reviews)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			out := newGitHubPublisher(server.URL, "secret", server.Client()).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("github"))
			if out.Code != v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse {
				t.Fatalf("out=%#v", out)
			}
		})
	}
}

func TestGitHubReconciliationRejectsDuplicateReviewIDAcrossPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repositories/123":
			io.WriteString(w, `{"id":123,"full_name":"acme/repo"}`)
		case "/repos/acme/repo/pulls/7":
			io.WriteString(w, `{"number":7,"state":"open","head":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`)
		case "/repos/acme/repo/pulls/7/requested_reviewers":
			io.WriteString(w, `{"users":[],"teams":[]}`)
		case "/repos/acme/repo/pulls/7/reviews":
			if r.URL.Query().Get("page") == "1" {
				reviews := make([]map[string]any, 100)
				for i := range reviews {
					reviews[i] = map[string]any{"id": i + 1, "state": "COMMENTED", "user": map[string]any{"id": i + 100, "login": "user", "type": "User"}}
				}
				json.NewEncoder(w).Encode(reviews)
			} else {
				io.WriteString(w, `[{"id":1,"state":"APPROVED","user":{"id":42,"login":"alice","type":"User"}}]`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := newGitHubPublisher(server.URL, "secret", server.Client()).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("github"))
	if out.Code != v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse {
		t.Fatalf("out=%#v", out)
	}
}

func TestGitLabReconciliationNormalizesPartialAndNeverWrites(t *testing.T) {
	op := reconciliationOperationForTest("gitlab")
	second := v1.ProviderReviewerAssignmentReconciliationIntendedReviewer{ReviewerIdentityID: "identity-2", ProviderAccountID: "99"}
	op.IntendedReviewers = append(op.IntendedReviewers, second)
	op.IntendedReviewerSetDigest = v1.ProviderReviewerAssignmentReconciliationIntendedReviewerSetDigest(op.IntendedReviewers)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("mutation attempted: %s", r.Method)
		}
		switch r.URL.Path {
		case "/projects/123":
			io.WriteString(w, `{"id":123,"path_with_namespace":"acme/repo"}`)
		case "/projects/123/merge_requests/7":
			io.WriteString(w, `{"iid":7,"state":"opened","sha":"0123456789abcdef0123456789abcdef01234567"}`)
		case "/projects/123/merge_requests/7/reviewers":
			io.WriteString(w, `[{"user":{"id":42,"username":"renamed"},"state":"unreviewed"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := reconcileThroughRegistry(t, server, "gitlab", op)
	if out.Conclusion != v1.ProviderReviewerAssignmentReconciliationPartial || out.IntendedReviewers[0].Presence != v1.ProviderReviewerAssignmentReconciliationRequested || out.IntendedReviewers[1].Presence != v1.ProviderReviewerAssignmentReconciliationNotPresent {
		t.Fatalf("out=%#v", out)
	}
}

func TestGitLabReconciliationMapsMergedReviewedState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects/123":
			io.WriteString(w, `{"id":123,"path_with_namespace":"acme/repo"}`)
		case "/projects/123/merge_requests/7":
			io.WriteString(w, `{"iid":7,"state":"merged","sha":"0123456789abcdef0123456789abcdef01234567"}`)
		case "/projects/123/merge_requests/7/reviewers":
			io.WriteString(w, `[{"user":{"id":42,"username":"renamed"},"state":"reviewed"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := newGitLabPublisher(server.URL, "secret", server.Client()).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("gitlab"))
	if out.Conclusion != v1.ProviderReviewerAssignmentReconciliationSuperseded || out.RequestState != v1.ProviderReviewerAssignmentReconciliationRequestMerged || out.IntendedReviewers[0].Presence != v1.ProviderReviewerAssignmentReconciliationReviewed {
		t.Fatalf("out=%#v", out)
	}
}

func TestGitLabReconciliationMapsAllCanonicalReviewerStates(t *testing.T) {
	for _, state := range []string{"unreviewed", "review_started", "reviewed", "requested_changes", "approved", "unapproved"} {
		t.Run(state, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/projects/123":
					io.WriteString(w, `{"id":123,"path_with_namespace":"acme/repo"}`)
				case "/projects/123/merge_requests/7":
					io.WriteString(w, `{"iid":7,"state":"opened","sha":"0123456789abcdef0123456789abcdef01234567"}`)
				case "/projects/123/merge_requests/7/reviewers":
					io.WriteString(w, `[{"user":{"id":42,"username":"alice"},"state":"`+state+`"}]`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			out := newGitLabPublisher(server.URL, "secret", server.Client()).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("gitlab"))
			want := v1.ProviderReviewerAssignmentReconciliationReviewed
			if state == "unreviewed" || state == "review_started" {
				want = v1.ProviderReviewerAssignmentReconciliationRequested
			}
			if out.Kind != v1.ProviderReviewerAssignmentReconciliationSucceeded || out.IntendedReviewers[0].Presence != want {
				t.Fatalf("out=%#v", out)
			}
		})
	}
}

func TestBitbucketReconciliationUsesAccountIDAndMarksHeadDriftSuperseded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("mutation attempted: %s", r.Method)
		}
		switch r.URL.Path {
		case "/repositories/acme/repo":
			io.WriteString(w, `{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"acme/repo"}`)
		case "/repositories/acme/repo/pullrequests/7":
			io.WriteString(w, `{"id":7,"state":"MERGED","source":{"commit":{"hash":"1123456789abcdef0123456789abcdef01234567"},"repository":{"full_name":"acme/repo"}},"destination":{"repository":{"full_name":"acme/repo"}},"reviewers":[{"account_id":"bb-account-42","uuid":"{11111111-1111-4111-8111-111111111111}","nickname":"renamed"}],"participants":[{"account_id":"bb-account-42","uuid":"{11111111-1111-4111-8111-111111111111}","nickname":"renamed","approved":true,"state":"approved"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := reconcileThroughRegistry(t, server, "bitbucket", reconciliationOperationForTest("bitbucket"))
	if out.Conclusion != v1.ProviderReviewerAssignmentReconciliationSuperseded || out.RequestState != v1.ProviderReviewerAssignmentReconciliationRequestMerged || out.IntendedReviewers[0].Presence != v1.ProviderReviewerAssignmentReconciliationReviewed {
		t.Fatalf("out=%#v", out)
	}
}

func TestBitbucketReconciliationAcceptsMissingNicknameButRejectsConflictingStableIdentity(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing nickname", true: "conflicting stable ID"}[conflict], func(t *testing.T) {
			participants := `[]`
			if conflict {
				participants = `[{"account_id":"bb-account-42","uuid":"{33333333-3333-4333-8333-333333333333}","approved":true,"state":"approved"}]`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repositories/acme/repo":
					io.WriteString(w, `{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"acme/repo"}`)
				case "/repositories/acme/repo/pullrequests/7":
					io.WriteString(w, `{"id":7,"state":"OPEN","source":{"commit":{"hash":"0123456789abcdef0123456789abcdef01234567"},"repository":{"full_name":"acme/repo"}},"destination":{"repository":{"full_name":"acme/repo"}},"reviewers":[{"account_id":"bb-account-42","uuid":"{11111111-1111-4111-8111-111111111111}"}],"participants":`+participants+`}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			out := newBitbucketPublisher(server.URL, "secret", server.Client()).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("bitbucket"))
			if conflict && out.Code != v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse {
				t.Fatalf("conflict out=%#v", out)
			}
			if !conflict && (out.Kind != v1.ProviderReviewerAssignmentReconciliationSucceeded || out.IntendedReviewers[0].Presence != v1.ProviderReviewerAssignmentReconciliationRequested) {
				t.Fatalf("missing nickname out=%#v", out)
			}
		})
	}
}

func TestReconciliationRegistryFailsClosedBeforeNetworkForCrossInstallationAuthority(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	registry, err := NewRegistry(map[string]config.SourcePublicationInstallationConfig{"installation-1": {Provider: "github", APIBaseURL: server.URL, TokenFile: writeToken(t, 0o600)}}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	op := reconciliationOperationForTest("gitlab")
	out := registry.ReconcileProviderReviewerAssignment(context.Background(), op)
	if out.Code != v1.ProviderReviewerAssignmentReconciliationInstallationPermissionDenied || calls != 0 {
		t.Fatalf("out=%#v calls=%d", out, calls)
	}
	op = reconciliationOperationForTest("github")
	op.ProviderInstanceKey = "github.enterprise.example"
	out = registry.ReconcileProviderReviewerAssignment(context.Background(), op)
	if out.Code != v1.ProviderReviewerAssignmentReconciliationUnsupportedProviderInstance || calls != 0 {
		t.Fatalf("out=%#v calls=%d", out, calls)
	}
	op.InstallationID = "other-tenant-installation"
	out = registry.ReconcileProviderReviewerAssignment(context.Background(), op)
	if out.Code != v1.ProviderReviewerAssignmentReconciliationInstallationInactive || calls != 0 {
		t.Fatalf("out=%#v calls=%d", out, calls)
	}
}

func TestReconciliationNormalizesFailuresWithoutProviderContent(t *testing.T) {
	if out := reconciliationRequestFailure(errInvalidProviderResponse); out.Code != v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse {
		t.Fatalf("invalid response=%#v", out)
	}
	if out := reconciliationRequestFailure(errors.New("secret upstream body")); out.Code != v1.ProviderReviewerAssignmentReconciliationProviderUnavailable {
		t.Fatalf("transport=%#v", out)
	}
	tests := []struct {
		name     string
		status   int
		headers  http.Header
		wantKind v1.ProviderReviewerAssignmentReconciliationOutcomeKind
		wantCode v1.ProviderReviewerAssignmentReconciliationErrorCode
	}{
		{"unauthorized", 401, nil, v1.ProviderReviewerAssignmentReconciliationPermanent, v1.ProviderReviewerAssignmentReconciliationInstallationInactive},
		{"forbidden", 403, nil, v1.ProviderReviewerAssignmentReconciliationPermanent, v1.ProviderReviewerAssignmentReconciliationInstallationPermissionDenied},
		{"not found", 404, nil, v1.ProviderReviewerAssignmentReconciliationPermanent, v1.ProviderReviewerAssignmentReconciliationRequestNotFound},
		{"rate limited", 429, http.Header{"Retry-After": []string{"1"}}, v1.ProviderReviewerAssignmentReconciliationRetryable, v1.ProviderReviewerAssignmentReconciliationRateLimited},
		{"unavailable", 503, nil, v1.ProviderReviewerAssignmentReconciliationRetryable, v1.ProviderReviewerAssignmentReconciliationProviderUnavailable},
		{"invalid", 409, nil, v1.ProviderReviewerAssignmentReconciliationPermanent, v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gh := classifyGitHubReconciliationFailure(githubResponse{status: tc.status, header: tc.headers}, v1.ProviderReviewerAssignmentReconciliationRequestNotFound)
			gl := classifyGitLabReconciliationFailure(gitLabResponse{status: tc.status, header: tc.headers}, v1.ProviderReviewerAssignmentReconciliationRequestNotFound)
			bb := classifyBitbucketReconciliationFailure(bitbucketResponse{status: tc.status, header: tc.headers}, v1.ProviderReviewerAssignmentReconciliationRequestNotFound)
			for provider, out := range map[string]v1.ProviderReviewerAssignmentReconciliationResultOutcome{"github": gh, "gitlab": gl, "bitbucket": bb} {
				if out.Kind != tc.wantKind || out.Code != tc.wantCode {
					t.Fatalf("%s=%#v", provider, out)
				}
			}
		})
	}
	rateHeaders := make(http.Header)
	rateHeaders.Set("X-RateLimit-Remaining", "0")
	githubRate := classifyGitHubReconciliationFailure(githubResponse{status: 403, header: rateHeaders}, v1.ProviderReviewerAssignmentReconciliationRequestNotFound)
	if githubRate.Code != v1.ProviderReviewerAssignmentReconciliationRateLimited {
		t.Fatalf("github rate=%#v", githubRate)
	}
}

func TestReconciliationConclusionIsDerivedOnlyFromExactStableIDs(t *testing.T) {
	op := reconciliationOperationForTest("github")
	absent := reconciliationSucceeded(op, v1.ProviderReviewerAssignmentReconciliationRequestOpen, op.ExpectedHeadSHA, nil)
	if absent.Conclusion != v1.ProviderReviewerAssignmentReconciliationAbsent {
		t.Fatalf("absent=%#v", absent)
	}
	closed := reconciliationSucceeded(op, v1.ProviderReviewerAssignmentReconciliationRequestClosed, op.ExpectedHeadSHA, nil)
	if closed.Conclusion != v1.ProviderReviewerAssignmentReconciliationSuperseded {
		t.Fatalf("closed=%#v", closed)
	}
}

func TestProviderReconciliationRejectsRepositoryIdentityDriftBeforeRequestRead(t *testing.T) {
	tests := []struct {
		name, provider, body string
		run                  func(string, *http.Client) v1.ProviderReviewerAssignmentReconciliationResultOutcome
	}{
		{"github", "github", `{"id":999,"full_name":"attacker/repo"}`, func(base string, client *http.Client) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
			return newGitHubPublisher(base, "secret", client).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("github"))
		}},
		{"gitlab", "gitlab", `{"id":999,"path_with_namespace":"attacker/repo"}`, func(base string, client *http.Client) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
			return newGitLabPublisher(base, "secret", client).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("gitlab"))
		}},
		{"bitbucket", "bitbucket", `{"uuid":"{99999999-9999-4999-8999-999999999999}","full_name":"attacker/repo"}`, func(base string, client *http.Client) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
			return newBitbucketPublisher(base, "secret", client).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("bitbucket"))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; io.WriteString(w, tc.body) }))
			defer server.Close()
			out := tc.run(server.URL, server.Client())
			if out.Code != v1.ProviderReviewerAssignmentReconciliationRepositoryMismatch || requests != 1 {
				t.Fatalf("out=%#v requests=%d", out, requests)
			}
		})
	}
}

type reconciliationRoundTripFunc func(*http.Request) (*http.Response, error)

func (f reconciliationRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderReconciliationNormalizesRepositoryReadFailures(t *testing.T) {
	transportClient := &http.Client{Transport: reconciliationRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret transport failure") })}
	providers := []struct {
		name string
		run  func(*http.Client) v1.ProviderReviewerAssignmentReconciliationResultOutcome
	}{
		{"github", func(client *http.Client) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
			return newGitHubPublisher("https://github.invalid", "secret", client).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("github"))
		}},
		{"gitlab", func(client *http.Client) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
			return newGitLabPublisher("https://gitlab.invalid", "secret", client).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("gitlab"))
		}},
		{"bitbucket", func(client *http.Client) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
			return newBitbucketPublisher("https://bitbucket.invalid", "secret", client).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("bitbucket"))
		}},
	}
	for _, provider := range providers {
		t.Run(provider.name+" transport", func(t *testing.T) {
			out := provider.run(transportClient)
			if out.Code != v1.ProviderReviewerAssignmentReconciliationProviderUnavailable {
				t.Fatalf("out=%#v", out)
			}
		})
		t.Run(provider.name+" not-found", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
			defer server.Close()
			out := func() v1.ProviderReviewerAssignmentReconciliationResultOutcome {
				switch provider.name {
				case "github":
					return newGitHubPublisher(server.URL, "secret", server.Client()).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("github"))
				case "gitlab":
					return newGitLabPublisher(server.URL, "secret", server.Client()).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("gitlab"))
				default:
					return newBitbucketPublisher(server.URL, "secret", server.Client()).reconcileReviewerAssignment(context.Background(), reconciliationOperationForTest("bitbucket"))
				}
			}()
			if out.Kind != v1.ProviderReviewerAssignmentReconciliationPermanent {
				t.Fatalf("out=%#v", out)
			}
		})
	}
}
