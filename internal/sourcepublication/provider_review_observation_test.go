package sourcepublication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func observeThroughRegistry(t *testing.T, server *httptest.Server, provider string, operation v1.ProviderReviewObservationOperation) v1.ProviderReviewObservationResultOutcome {
	t.Helper()
	operation.ProviderInstanceKey = configuredProviderInstanceKey(provider, server.URL)
	registry := &Registry{installations: map[string]config.SourcePublicationInstallationConfig{"installation-1": {Provider: provider, APIBaseURL: server.URL, TokenFile: writeToken(t, 0o600)}}, httpClient: server.Client()}
	return registry.ObserveProviderReviews(context.Background(), operation)
}

func reviewObservationOperationForTest(provider string) v1.ProviderReviewObservationOperation {
	repoID, path, instance, account := "123", "", "github.com", "42"
	if provider == "gitlab" {
		instance = "gitlab.com"
	}
	if provider == "bitbucket" {
		repoID = "{22222222-2222-4222-8222-222222222222}"
		path = "acme/repo"
		instance = "bitbucket.org"
		account = "bb-account-42"
	}
	subjects := []v1.ProviderReviewObservationSubject{{ReviewerIdentityID: "identity-1", UserID: "user-1", OrganizationMembershipID: "membership-1", IdentityStateRevision: 1, IdentityRevision: 1, ProviderAccountProofID: "proof-1", ProviderAccountProofStateRevision: 1, ProviderAccountProofRevision: 1, ProviderAccountProofDigest: strings.Repeat("1", 64), ProviderAccountProofExpiresAt: time.Now().Add(2 * time.Hour), IdentityVerificationID: "verification-1", IdentityVerificationDigest: strings.Repeat("2", 64), IdentityVerificationExpiresAt: time.Now().Add(time.Hour), ProviderAccountID: account}}
	return v1.ProviderReviewObservationOperation{ProviderReviewObservationAuthority: v1.ProviderReviewObservationAuthority{SchemaVersion: 1, ProviderReviewObservationOperationID: "provider-review-observation-operation-" + strings.Repeat("a", 64), OrganizationID: "organization-1", ProjectID: "project-1", WorkflowRunID: "run-1", AssignmentDeliveryID: "delivery-1", AssignmentDeliveryStateRevision: 1, AssignmentPlanID: "plan-1", AssignmentPlanDigest: strings.Repeat("3", 64), AssignmentObservationID: "observation-1", AssignmentResultPayloadChecksum: strings.Repeat("4", 64), SourcePublicationObservationID: "source-observation-1", RepositoryID: "repository-1", ProviderRepositoryID: repoID, ProviderRepositoryPath: path, ExternalRequestID: "7", ExpectedHeadSHA: "0123456789abcdef0123456789abcdef01234567", InstallationID: "installation-1", InstallationStateRevision: 1, InstallationAuthorityRevision: 1, InstallationAuthorityDigest: strings.Repeat("5", 64), Provider: provider, ProviderInstanceKey: instance, IdempotencyKey: "provider-review-observation-" + strings.Repeat("6", 64), SubjectIdentitySetDigest: v1.ProviderReviewObservationSubjectIdentitySetDigest(subjects), MaxCollectionPages: 10, CollectionPageSize: 100, OperationDigest: strings.Repeat("7", 64), CommandDigest: strings.Repeat("8", 64), LeaseID: "lease-1", FencingToken: 1, LeaseExpiresAt: time.Now().Add(30 * time.Minute)}, Subjects: subjects}
}

func TestGitHubProviderReviewObservationIsGETOnlyAndMetadataOnly(t *testing.T) {
	methods := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods[r.Method]++
		if r.Method != http.MethodGet {
			t.Fatalf("mutation %s", r.Method)
		}
		switch r.URL.Path {
		case "/repositories/123":
			io.WriteString(w, `{"id":123,"full_name":"acme/repo"}`)
		case "/repos/acme/repo/pulls/7":
			io.WriteString(w, `{"number":7,"state":"open","head":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`)
		case "/repos/acme/repo/pulls/7/reviews":
			io.WriteString(w, `[{"id":7001,"state":"APPROVED","body":"secret review body","submitted_at":"2026-08-21T10:00:00Z","commit_id":"0123456789abcdef0123456789abcdef01234567","user":{"id":42,"login":"renamed","type":"User"}}]`)
		case "/repos/acme/repo/issues/7/comments":
			io.WriteString(w, `[{"id":8001,"body":"secret issue comment","created_at":"2026-08-21T10:01:00Z","updated_at":"2026-08-21T10:02:00Z","user":{"id":42,"login":"renamed","type":"User"}}]`)
		case "/repos/acme/repo/pulls/7/comments":
			io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := newGitHubPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), reviewObservationOperationForTest("github"))
	if out.Kind != v1.ProviderReviewObservationSucceeded || len(out.ReviewEvents) != 1 || len(out.Comments) != 1 || out.ReviewEvents[0].ProviderAccountID != "42" {
		t.Fatalf("out=%#v", out)
	}
	want := sha256.Sum256([]byte("secret review body"))
	if out.ReviewEvents[0].ContentDigest != hex.EncodeToString(want[:]) || out.ReviewEvents[0].ContentByteCount != 18 {
		t.Fatalf("review=%#v", out.ReviewEvents[0])
	}
	raw, _ := json.Marshal(out)
	for _, secret := range []string{"secret review body", "secret issue comment", "renamed"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("raw content leaked: %s", raw)
		}
	}
}

func TestGitLabProviderReviewObservationUsesDerivedStableEventID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatal("mutation")
		}
		switch r.URL.Path {
		case "/projects/123":
			io.WriteString(w, `{"id":123,"path_with_namespace":"acme/repo"}`)
		case "/projects/123/merge_requests/7":
			io.WriteString(w, `{"iid":7,"state":"opened","sha":"0123456789abcdef0123456789abcdef01234567"}`)
		case "/projects/123/merge_requests/7/reviewers":
			io.WriteString(w, `[{"user":{"id":42,"username":"renamed"},"state":"approved","created_at":"2030-08-21T11:58:00.000Z"}]`)
		case "/projects/123/merge_requests/7/notes":
			io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	op := reviewObservationOperationForTest("gitlab")
	op.ProviderRepositoryID = "12345"
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatal("mutation")
		}
		switch r.URL.Path {
		case "/projects/12345":
			io.WriteString(w, `{"id":12345,"path_with_namespace":"acme/repo"}`)
		case "/projects/12345/merge_requests/7":
			io.WriteString(w, `{"iid":7,"state":"opened","sha":"0123456789abcdef0123456789abcdef01234567"}`)
		case "/projects/12345/merge_requests/7/reviewers":
			io.WriteString(w, `[{"user":{"id":42},"state":"approved","created_at":"2030-08-21T11:58:00.000Z"}]`)
		case "/projects/12345/merge_requests/7/notes":
			io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	})
	out := newGitLabPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), op)
	if len(out.ReviewEvents) != 1 || !strings.HasPrefix(out.ReviewEvents[0].ProviderReviewEventID, "derived-") {
		t.Fatalf("out=%#v", out)
	}
}

func TestBitbucketProviderReviewObservationAllowsExplicitTombstone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatal("mutation")
		}
		switch r.URL.Path {
		case "/repositories/acme/repo":
			io.WriteString(w, `{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"acme/repo"}`)
		case "/repositories/acme/repo/pullrequests/7":
			io.WriteString(w, `{"id":7,"state":"OPEN","updated_on":"2026-08-21T10:00:00Z","source":{"commit":{"hash":"0123456789abcdef0123456789abcdef01234567"},"repository":{"full_name":"acme/repo"}},"destination":{"repository":{"full_name":"acme/repo"}},"reviewers":[{"account_id":"bb-account-42","uuid":"{11111111-1111-4111-8111-111111111111}"}],"participants":[]}`)
		case "/repositories/acme/repo/pullrequests/7/comments":
			io.WriteString(w, `{"values":[{"id":91,"content":{"raw":""},"created_on":"2026-08-21T10:01:00Z","updated_on":"2026-08-21T10:02:00Z","deleted":true,"user":{"account_id":"bb-account-42","uuid":"{11111111-1111-4111-8111-111111111111}"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := newBitbucketPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), reviewObservationOperationForTest("bitbucket"))
	if out.Kind != v1.ProviderReviewObservationSucceeded || len(out.Comments) != 1 || !out.Comments[0].Deleted {
		t.Fatalf("out=%#v", out)
	}
}

func TestReviewObservationRegistryRevalidatesStableIdentityBeforeSnapshot(t *testing.T) {
	methods := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods[r.Method]++
		if r.Method != http.MethodGet {
			t.Fatalf("mutation attempted: %s", r.Method)
		}
		switch r.URL.Path {
		case "/user/42":
			io.WriteString(w, `{"id":42,"login":"renamed","type":"User"}`)
		case "/repositories/123":
			io.WriteString(w, `{"id":123,"full_name":"acme/repo"}`)
		case "/repos/acme/repo/collaborators/renamed/permission":
			io.WriteString(w, `{"permission":"write","user":{"id":42,"login":"renamed","type":"User"}}`)
		case "/repos/acme/repo/pulls/7":
			io.WriteString(w, `{"number":7,"state":"open","head":{"sha":"1123456789abcdef0123456789abcdef01234567"}}`)
		case "/repos/acme/repo/pulls/7/reviews", "/repos/acme/repo/issues/7/comments", "/repos/acme/repo/pulls/7/comments":
			io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := observeThroughRegistry(t, server, "github", reviewObservationOperationForTest("github"))
	if out.Kind != v1.ProviderReviewObservationSucceeded || !out.StaleHead || methods[http.MethodGet] != 8 {
		t.Fatalf("out=%#v methods=%#v", out, methods)
	}
}

func TestReviewObservationRegistryDispatchesGitLabAndBitbucketAfterLiveRevalidation(t *testing.T) {
	t.Run("gitlab", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Fatal("mutation")
			}
			switch r.URL.Path {
			case "/users/42":
				io.WriteString(w, `{"id":42,"username":"renamed","state":"active"}`)
			case "/projects/123":
				io.WriteString(w, `{"id":123,"path_with_namespace":"acme/repo"}`)
			case "/projects/123/members/all/42":
				io.WriteString(w, `{"id":42,"username":"renamed","state":"active","access_level":30}`)
			case "/projects/123/merge_requests/7":
				io.WriteString(w, `{"iid":7,"state":"closed","sha":"0123456789abcdef0123456789abcdef01234567"}`)
			case "/projects/123/merge_requests/7/reviewers", "/projects/123/merge_requests/7/notes":
				io.WriteString(w, `[]`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()
		out := observeThroughRegistry(t, server, "gitlab", reviewObservationOperationForTest("gitlab"))
		if out.Kind != v1.ProviderReviewObservationSucceeded || out.RequestState != v1.ProviderReviewObservationRequestClosed {
			t.Fatalf("out=%#v", out)
		}
	})
	t.Run("bitbucket", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Fatal("mutation")
			}
			switch r.URL.Path {
			case "/users/bb-account-42":
				io.WriteString(w, `{"account_id":"bb-account-42","uuid":"{11111111-1111-4111-8111-111111111111}","nickname":"renamed","account_status":"active"}`)
			case "/repositories/acme/repo":
				io.WriteString(w, `{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"acme/repo"}`)
			case "/workspaces/acme/permissions/repositories/repo":
				io.WriteString(w, `{"page":1,"pagelen":100,"size":1,"values":[{"permission":"read","user":{"account_id":"bb-account-42","uuid":"{11111111-1111-4111-8111-111111111111}"},"repository":{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"acme/repo"}}]}`)
			case "/repositories/acme/repo/pullrequests/7":
				io.WriteString(w, `{"id":7,"state":"MERGED","updated_on":"2026-08-21T10:00:00Z","source":{"commit":{"hash":"0123456789abcdef0123456789abcdef01234567"},"repository":{"full_name":"acme/repo"}},"destination":{"repository":{"full_name":"acme/repo"}},"reviewers":[],"participants":[]}`)
			case "/repositories/acme/repo/pullrequests/7/comments":
				io.WriteString(w, `{"values":[]}`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()
		out := observeThroughRegistry(t, server, "bitbucket", reviewObservationOperationForTest("bitbucket"))
		if out.Kind != v1.ProviderReviewObservationSucceeded || out.RequestState != v1.ProviderReviewObservationRequestMerged {
			t.Fatalf("out=%#v", out)
		}
	})
}

func TestReviewObservationRegistryFailsClosedBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	registry := &Registry{installations: map[string]config.SourcePublicationInstallationConfig{"installation-1": {Provider: "github", APIBaseURL: server.URL, TokenFile: writeToken(t, 0o600)}}, httpClient: server.Client()}
	tests := []struct {
		name string
		op   v1.ProviderReviewObservationOperation
		code v1.ProviderReviewObservationErrorCode
	}{
		{"missing installation", func() v1.ProviderReviewObservationOperation {
			op := reviewObservationOperationForTest("github")
			op.InstallationID = "other"
			return op
		}(), v1.ProviderReviewObservationInstallationInactive},
		{"cross provider", reviewObservationOperationForTest("gitlab"), v1.ProviderReviewObservationInstallationPermissionDenied},
		{"instance drift", func() v1.ProviderReviewObservationOperation {
			op := reviewObservationOperationForTest("github")
			op.ProviderInstanceKey = "github.enterprise.example"
			return op
		}(), v1.ProviderReviewObservationUnsupportedProviderInstance},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := registry.ObserveProviderReviews(context.Background(), tc.op)
			if out.Code != tc.code || calls != 0 {
				t.Fatalf("out=%#v calls=%d", out, calls)
			}
		})
	}
	badToken := &Registry{installations: map[string]config.SourcePublicationInstallationConfig{"installation-1": {Provider: "github", APIBaseURL: server.URL, TokenFile: t.TempDir() + "/missing"}}, httpClient: server.Client()}
	op := reviewObservationOperationForTest("github")
	op.ProviderInstanceKey = configuredProviderInstanceKey("github", server.URL)
	if out := badToken.ObserveProviderReviews(context.Background(), op); out.Code != v1.ProviderReviewObservationInstallationInactive || calls != 0 {
		t.Fatalf("token out=%#v calls=%d", out, calls)
	}
}

func TestReviewObservationIdentityFailureMapping(t *testing.T) {
	eligible := v1.ReviewerIdentityVerificationResultOutcome{Kind: v1.ReviewerIdentityVerificationSucceeded, Repositories: []v1.ReviewerIdentityRepositoryEvidence{{EffectiveReviewerAccess: v1.ReviewerIdentityEligible}}}
	if out := reviewObservationIdentityFailure(eligible); out != nil {
		t.Fatalf("eligible=%#v", out)
	}
	for name, input := range map[string]v1.ReviewerIdentityVerificationResultOutcome{
		"missing repository": {Kind: v1.ReviewerIdentityVerificationSucceeded},
		"ineligible":         {Kind: v1.ReviewerIdentityVerificationSucceeded, Repositories: []v1.ReviewerIdentityRepositoryEvidence{{EffectiveReviewerAccess: v1.ReviewerIdentityNotMember}}},
		"rate":               {Kind: v1.ReviewerIdentityVerificationRetryable, Code: v1.ReviewerIdentityRateLimited},
		"unavailable":        {Kind: v1.ReviewerIdentityVerificationRetryable},
		"inactive":           {Kind: v1.ReviewerIdentityVerificationPermanent, Code: v1.ReviewerIdentityInstallationInactive},
		"not found":          {Kind: v1.ReviewerIdentityVerificationPermanent, Code: v1.ReviewerIdentityInstallationNotFound},
		"permission":         {Kind: v1.ReviewerIdentityVerificationPermanent, Code: v1.ReviewerIdentityInstallationPermissionDenied},
		"changed":            {Kind: v1.ReviewerIdentityVerificationPermanent},
	} {
		t.Run(name, func(t *testing.T) {
			if out := reviewObservationIdentityFailure(input); out == nil || out.Kind == v1.ProviderReviewObservationSucceeded {
				t.Fatalf("out=%#v", out)
			}
		})
	}
}

func TestReviewObservationHelpersAndFailureClassification(t *testing.T) {
	if out := reviewObservationRequestFailure(errInvalidProviderResponse); out.Code != v1.ProviderReviewObservationInvalidProviderResponse {
		t.Fatalf("invalid=%#v", out)
	}
	if out := reviewObservationRequestFailure(errors.New("secret provider body")); out.Code != v1.ProviderReviewObservationProviderUnavailable {
		t.Fatalf("transport=%#v", out)
	}
	if _, _, ok := reviewObservationContent(string([]byte{0xff})); ok {
		t.Fatal("invalid UTF-8 accepted")
	}
	if _, _, ok := reviewObservationContent(strings.Repeat("x", 1<<20+1)); ok {
		t.Fatal("oversize accepted")
	}
	if reviewObservationDecode([]byte{0xff}, &struct{}{}) || reviewObservationDecode([]byte(`{`), &struct{}{}) {
		t.Fatal("invalid JSON accepted")
	}
	op := reviewObservationOperationForTest("github")
	reviews := []v1.ProviderReviewObservationReviewEvent{{ProviderReviewEventID: "2"}, {ProviderReviewEventID: "1"}}
	comments := []v1.ProviderReviewObservationComment{{ProviderCommentNamespace: v1.ProviderReviewObservationReviewNamespace, ProviderCommentID: "2"}, {ProviderCommentNamespace: v1.ProviderReviewObservationIssue, ProviderCommentID: "1"}}
	out := reviewObservationSuccess(op, v1.ProviderReviewObservationRequestOpen, op.ExpectedHeadSHA, 1, 1, reviews, comments)
	if out.StaleHead || out.ReviewEvents[0].ProviderReviewEventID != "1" || out.Comments[0].ProviderCommentNamespace != v1.ProviderReviewObservationIssue {
		t.Fatalf("success=%#v", out)
	}

	tests := []struct {
		status int
		header http.Header
		kind   v1.ProviderReviewObservationOutcomeKind
		code   v1.ProviderReviewObservationErrorCode
	}{
		{401, nil, v1.ProviderReviewObservationPermanent, v1.ProviderReviewObservationInstallationInactive},
		{403, nil, v1.ProviderReviewObservationPermanent, v1.ProviderReviewObservationInstallationPermissionDenied},
		{404, nil, v1.ProviderReviewObservationPermanent, v1.ProviderReviewObservationRequestNotFound},
		{408, nil, v1.ProviderReviewObservationRetryable, v1.ProviderReviewObservationProviderUnavailable},
		{429, http.Header{"Retry-After": []string{"1"}}, v1.ProviderReviewObservationRetryable, v1.ProviderReviewObservationRateLimited},
		{503, nil, v1.ProviderReviewObservationRetryable, v1.ProviderReviewObservationProviderUnavailable},
		{409, nil, v1.ProviderReviewObservationPermanent, v1.ProviderReviewObservationInvalidProviderResponse},
	}
	for _, tc := range tests {
		gh := classifyGitHubReviewObservationFailure(githubResponse{status: tc.status, header: tc.header}, v1.ProviderReviewObservationRequestNotFound)
		gl := classifyGitLabReviewObservationFailure(gitLabResponse{status: tc.status, header: tc.header}, v1.ProviderReviewObservationRequestNotFound)
		bb := classifyBitbucketReviewObservationFailure(bitbucketResponse{status: tc.status, header: tc.header}, v1.ProviderReviewObservationRequestNotFound)
		for provider, got := range map[string]v1.ProviderReviewObservationResultOutcome{"github": gh, "gitlab": gl, "bitbucket": bb} {
			if got.Kind != tc.kind || got.Code != tc.code {
				t.Fatalf("%s status=%d got=%#v", provider, tc.status, got)
			}
		}
	}
	rateHeader := make(http.Header)
	rateHeader.Set("X-RateLimit-Remaining", "0")
	if got := classifyGitHubReviewObservationFailure(githubResponse{status: 403, header: rateHeader}, v1.ProviderReviewObservationRequestNotFound); got.Code != v1.ProviderReviewObservationRateLimited {
		t.Fatalf("github rate=%#v", got)
	}
}

func TestReviewObservationProviderStateMappingsAreClosed(t *testing.T) {
	for _, state := range []string{"PENDING", "COMMENTED", "APPROVED", "CHANGES_REQUESTED", "DISMISSED"} {
		if _, _, ok := githubReviewState(state); !ok {
			t.Fatalf("github state %s", state)
		}
	}
	if _, _, ok := githubReviewState("UNKNOWN"); ok {
		t.Fatal("unknown GitHub state accepted")
	}
	for _, state := range []string{"unreviewed", "review_started", "reviewed", "requested_changes", "approved", "unapproved"} {
		if _, _, ok := gitLabReviewState(state); !ok {
			t.Fatalf("gitlab state %s", state)
		}
	}
	if _, _, ok := gitLabReviewState("unknown"); ok {
		t.Fatal("unknown GitLab state accepted")
	}
	for _, tc := range []struct {
		value       string
		page        int
		next, valid bool
	}{{"", 1, false, true}, {"2", 1, true, true}, {"3", 1, true, false}, {"x", 1, true, false}} {
		header := make(http.Header)
		header.Set("X-Next-Page", tc.value)
		next, valid := gitLabObservationHasNext(header, tc.page)
		if next != tc.next || valid != tc.valid {
			t.Fatalf("header=%q next=%v valid=%v", tc.value, next, valid)
		}
	}
}

func TestReviewObservationRejectsAmbiguousOrIncompleteProviderPages(t *testing.T) {
	t.Run("github duplicate and unknown review", func(t *testing.T) {
		for name, reviews := range map[string]string{
			"duplicate": `[{"id":1,"state":"APPROVED","body":"","submitted_at":"2026-08-21T10:00:00Z","user":{"id":42,"type":"User"}},{"id":1,"state":"COMMENTED","body":"","submitted_at":"2026-08-21T10:00:00Z","user":{"id":42,"type":"User"}}]`,
			"unknown":   `[{"id":1,"state":"MYSTERY","body":"","submitted_at":"2026-08-21T10:00:00Z","user":{"id":42,"type":"User"}}]`,
		} {
			t.Run(name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/repositories/123":
						io.WriteString(w, `{"id":123,"full_name":"acme/repo"}`)
					case "/repos/acme/repo/pulls/7":
						io.WriteString(w, `{"number":7,"state":"open","head":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`)
					case "/repos/acme/repo/pulls/7/reviews":
						io.WriteString(w, reviews)
					default:
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				out := newGitHubPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), reviewObservationOperationForTest("github"))
				if out.Code != v1.ProviderReviewObservationInvalidProviderResponse {
					t.Fatalf("out=%#v", out)
				}
			})
		}
	})

	t.Run("gitlab invalid next page", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/projects/123":
				io.WriteString(w, `{"id":123,"path_with_namespace":"acme/repo"}`)
			case "/projects/123/merge_requests/7":
				io.WriteString(w, `{"iid":7,"state":"opened","sha":"0123456789abcdef0123456789abcdef01234567"}`)
			case "/projects/123/merge_requests/7/reviewers":
				w.Header().Set("X-Next-Page", "3")
				io.WriteString(w, `[{"user":{"id":42},"state":"approved","created_at":"2026-08-21T10:00:00Z"}]`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()
		out := newGitLabPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), reviewObservationOperationForTest("gitlab"))
		if out.Code != v1.ProviderReviewObservationInvalidProviderResponse {
			t.Fatalf("out=%#v", out)
		}
	})

	t.Run("bitbucket cross origin next", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/repositories/acme/repo":
				io.WriteString(w, `{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"acme/repo"}`)
			case "/repositories/acme/repo/pullrequests/7":
				io.WriteString(w, `{"id":7,"state":"OPEN","updated_on":"2026-08-21T10:00:00Z","source":{"commit":{"hash":"0123456789abcdef0123456789abcdef01234567"},"repository":{"full_name":"acme/repo"}},"destination":{"repository":{"full_name":"acme/repo"}},"reviewers":[],"participants":[]}`)
			case "/repositories/acme/repo/pullrequests/7/comments":
				io.WriteString(w, `{"next":"https://attacker.invalid/repositories/acme/repo/pullrequests/7/comments?pagelen=100&page=2","values":[]}`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()
		out := newBitbucketPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), reviewObservationOperationForTest("bitbucket"))
		if out.Code != v1.ProviderReviewObservationIncomplete {
			t.Fatalf("out=%#v", out)
		}
	})
}

func TestReviewObservationCommentNormalizationRejectsMalformedMetadata(t *testing.T) {
	body := "ok"
	now := time.Now()
	valid := githubReviewerAccount{ID: 42, Type: "User"}
	if _, ok := githubNormalizedComment(1, &body, now, now, valid, v1.ProviderReviewObservationIssue, v1.ProviderReviewObservationGeneral, "", nil); !ok {
		t.Fatal("valid comment rejected")
	}
	for _, tc := range []struct {
		id               uint64
		body             *string
		created, updated time.Time
		user             githubReviewerAccount
	}{
		{0, &body, now, now, valid}, {1, nil, now, now, valid}, {1, &body, time.Time{}, now, valid}, {1, &body, now, time.Time{}, valid}, {1, &body, now, now, githubReviewerAccount{}}, {1, &body, now, time.Unix(0, 0), valid},
	} {
		if _, ok := githubNormalizedComment(tc.id, tc.body, tc.created, tc.updated, tc.user, v1.ProviderReviewObservationIssue, v1.ProviderReviewObservationGeneral, "", nil); ok {
			t.Fatalf("malformed comment accepted: %#v", tc)
		}
	}
}

func TestGitHubReviewObservationNormalizesAllCommentKinds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repositories/123":
			io.WriteString(w, `{"id":123,"full_name":"acme/repo"}`)
		case "/repos/acme/repo/pulls/7":
			io.WriteString(w, `{"number":7,"state":"closed","merged":true,"head":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`)
		case "/repos/acme/repo/pulls/7/reviews":
			io.WriteString(w, `[]`)
		case "/repos/acme/repo/issues/7/comments":
			io.WriteString(w, `[{"id":20,"body":"issue","created_at":"2026-08-21T10:00:00Z","updated_at":"2026-08-21T10:01:00Z","user":{"id":42,"type":"User"}}]`)
		case "/repos/acme/repo/pulls/7/comments":
			io.WriteString(w, `[{"id":30,"body":"inline","created_at":"2026-08-21T10:00:00Z","updated_at":"2026-08-21T10:01:00Z","path":"a.go","original_line":7,"side":"LEFT","commit_id":"0123456789abcdef0123456789abcdef01234567","user":{"id":42,"type":"User"}},{"id":31,"body":"reply","created_at":"2026-08-21T10:00:00Z","updated_at":"2026-08-21T10:01:00Z","in_reply_to_id":30,"user":{"id":42,"type":"User"}}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := newGitHubPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), reviewObservationOperationForTest("github"))
	if out.Kind != v1.ProviderReviewObservationSucceeded || out.RequestState != v1.ProviderReviewObservationRequestMerged || len(out.Comments) != 3 {
		t.Fatalf("out=%#v", out)
	}
	if out.Comments[1].CommentKind != v1.ProviderReviewObservationInline || out.Comments[1].Position == nil || out.Comments[1].Position.Line != 7 || out.Comments[2].CommentKind != v1.ProviderReviewObservationThreadReply || out.Comments[2].ParentCommentID != "30" {
		t.Fatalf("comments=%#v", out.Comments)
	}
}

func TestGitLabReviewObservationNormalizesNotes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/projects/123":
			io.WriteString(w, `{"id":123,"path_with_namespace":"acme/repo"}`)
		case "/projects/123/merge_requests/7":
			io.WriteString(w, `{"iid":7,"state":"merged","sha":"0123456789abcdef0123456789abcdef01234567"}`)
		case "/projects/123/merge_requests/7/reviewers":
			io.WriteString(w, `[{"user":{"id":42},"state":"requested_changes","created_at":"2026-08-21T10:00:00Z"}]`)
		case "/projects/123/merge_requests/7/notes":
			io.WriteString(w, `[{"id":10,"body":"general","created_at":"2026-08-21T10:00:00Z","updated_at":"2026-08-21T10:01:00Z","type":"DiscussionNote","author":{"id":42}},{"id":11,"body":"new","created_at":"2026-08-21T10:00:00Z","updated_at":"2026-08-21T10:01:00Z","type":"DiffNote","resolvable":true,"resolved":true,"author":{"id":42},"position":{"new_path":"a.go","new_line":9,"head_sha":"0123456789abcdef0123456789abcdef01234567"}},{"id":12,"body":"old","created_at":"2026-08-21T10:00:00Z","updated_at":"2026-08-21T10:01:00Z","type":"DiffNote","author":{"id":42},"position":{"old_path":"b.go","old_line":3,"head_sha":"0123456789abcdef0123456789abcdef01234567"}}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := newGitLabPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), reviewObservationOperationForTest("gitlab"))
	if out.Kind != v1.ProviderReviewObservationSucceeded || out.RequestState != v1.ProviderReviewObservationRequestMerged || len(out.Comments) != 3 || !out.Comments[1].Resolved || out.Comments[2].Position.Side != "left" {
		t.Fatalf("out=%#v", out)
	}
}

func TestBitbucketReviewObservationNormalizesStableStatesAndComments(t *testing.T) {
	op := reviewObservationOperationForTest("bitbucket")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repositories/acme/repo":
			io.WriteString(w, `{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"acme/repo"}`)
		case "/repositories/acme/repo/pullrequests/7":
			io.WriteString(w, `{"id":7,"state":"DECLINED","updated_on":"2026-08-21T10:00:00Z","source":{"commit":{"hash":"0123456789abcdef0123456789abcdef01234567"},"repository":{"full_name":"acme/repo"}},"destination":{"repository":{"full_name":"acme/repo"}},"reviewers":[{"account_id":"bb-account-42","uuid":"{11111111-1111-4111-8111-111111111111}"},{"account_id":"bb-account-99","uuid":"{99999999-9999-4999-8999-999999999999}"}],"participants":[{"account_id":"bb-account-42","uuid":"{11111111-1111-4111-8111-111111111111}","approved":true,"state":"approved"},{"account_id":"bb-account-99","uuid":"{99999999-9999-4999-8999-999999999999}","state":"changes_requested"}]}`)
		case "/repositories/acme/repo/pullrequests/7/comments":
			io.WriteString(w, `{"values":[{"id":10,"content":{"raw":"right"},"created_on":"2026-08-21T10:00:00Z","updated_on":"2026-08-21T10:01:00Z","user":{"account_id":"bb-account-42","uuid":"{11111111-1111-4111-8111-111111111111}"},"inline":{"path":"a.go","to":9}},{"id":11,"content":{"raw":"left"},"created_on":"2026-08-21T10:00:00Z","updated_on":"2026-08-21T10:01:00Z","user":{"account_id":"bb-account-99","uuid":"{99999999-9999-4999-8999-999999999999}"},"inline":{"path":"b.go","from":3}},{"id":12,"parent":{"id":10},"content":{"raw":"reply"},"created_on":"2026-08-21T10:00:00Z","updated_on":"2026-08-21T10:01:00Z","user":{"account_id":"bb-account-42","uuid":"{11111111-1111-4111-8111-111111111111}"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := newBitbucketPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), op)
	if out.Kind != v1.ProviderReviewObservationSucceeded || out.RequestState != v1.ProviderReviewObservationRequestClosed || len(out.ReviewEvents) != 2 || len(out.Comments) != 3 {
		t.Fatalf("out=%#v", out)
	}
	if out.Comments[0].Position.Side != "right" || out.Comments[1].Position.Side != "left" || out.Comments[2].ThreadID != "10" {
		t.Fatalf("comments=%#v", out.Comments)
	}
}

func TestBitbucketReviewObservationRejectsStableIdentityAmbiguityAndUnknownState(t *testing.T) {
	op := reviewObservationOperationForTest("bitbucket")
	now := time.Now()
	uuid1 := "{11111111-1111-4111-8111-111111111111}"
	uuid2 := "{22222222-2222-4222-8222-222222222222}"
	for name, reviewers := range map[string][]bitbucketReviewerAccount{
		"same account conflicting UUID": {{AccountID: "account", UUID: uuid1}, {AccountID: "account", UUID: uuid2}},
		"same UUID conflicting account": {{AccountID: "account-a", UUID: uuid1}, {AccountID: "account-b", UUID: uuid1}},
		"missing stable account":        {{UUID: uuid1}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, failure := bitbucketReviewEvents(op, now, reviewers, nil); failure == nil || failure.Code != v1.ProviderReviewObservationInvalidProviderResponse {
				t.Fatalf("failure=%#v", failure)
			}
		})
	}
	participants := []struct {
		bitbucketReviewerAccount
		Role     string `json:"role"`
		Approved bool   `json:"approved"`
		State    string `json:"state"`
	}{{bitbucketReviewerAccount: bitbucketReviewerAccount{AccountID: "account", UUID: uuid1}, State: "mystery"}}
	if _, failure := bitbucketReviewEvents(op, now, []bitbucketReviewerAccount{{AccountID: "account", UUID: uuid1}}, participants); failure == nil || failure.Code != v1.ProviderReviewObservationInvalidProviderResponse {
		t.Fatalf("unknown state=%#v", failure)
	}
}

func TestReviewObservationRejectsRepositoryAndRequestFenceDrift(t *testing.T) {
	for _, provider := range []string{"github", "gitlab", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			base := reviewObservationOperationForTest(provider)
			badRepo := base
			if provider == "bitbucket" {
				badRepo.ProviderRepositoryID = "not-a-uuid"
			} else {
				badRepo.ProviderRepositoryID = "01"
			}
			var got v1.ProviderReviewObservationResultOutcome
			switch provider {
			case "github":
				got = newGitHubPublisher("https://example.invalid", "secret", http.DefaultClient).observeProviderReviews(context.Background(), badRepo)
			case "gitlab":
				got = newGitLabPublisher("https://example.invalid", "secret", http.DefaultClient).observeProviderReviews(context.Background(), badRepo)
			case "bitbucket":
				got = newBitbucketPublisher("https://example.invalid", "secret", http.DefaultClient).observeProviderReviews(context.Background(), badRepo)
			}
			if got.Code != v1.ProviderReviewObservationRepositoryMismatch {
				t.Fatalf("bad repo=%#v", got)
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repositories/123":
					io.WriteString(w, `{"id":123,"full_name":"acme/repo"}`)
				case "/projects/123":
					io.WriteString(w, `{"id":123,"path_with_namespace":"acme/repo"}`)
				case "/repositories/acme/repo":
					io.WriteString(w, `{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"acme/repo"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			badRequest := base
			badRequest.ExternalRequestID = "01"
			switch provider {
			case "github":
				got = newGitHubPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), badRequest)
			case "gitlab":
				got = newGitLabPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), badRequest)
			case "bitbucket":
				got = newBitbucketPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), badRequest)
			}
			if got.Code != v1.ProviderReviewObservationRequestNotFound {
				t.Fatalf("bad request=%#v", got)
			}
		})
	}
}

func TestReviewObservationRejectsMalformedRequestState(t *testing.T) {
	for _, provider := range []string{"github", "gitlab", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repositories/123":
					io.WriteString(w, `{"id":123,"full_name":"acme/repo"}`)
				case "/projects/123":
					io.WriteString(w, `{"id":123,"path_with_namespace":"acme/repo"}`)
				case "/repositories/acme/repo":
					io.WriteString(w, `{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"acme/repo"}`)
				case "/repos/acme/repo/pulls/7":
					io.WriteString(w, `{"number":7,"state":"mystery","head":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`)
				case "/projects/123/merge_requests/7":
					io.WriteString(w, `{"iid":7,"state":"mystery","sha":"0123456789abcdef0123456789abcdef01234567"}`)
				case "/repositories/acme/repo/pullrequests/7":
					io.WriteString(w, `{"id":7,"state":"MYSTERY","updated_on":"2026-08-21T10:00:00Z","source":{"commit":{"hash":"0123456789abcdef0123456789abcdef01234567"},"repository":{"full_name":"acme/repo"}},"destination":{"repository":{"full_name":"acme/repo"}},"reviewers":[],"participants":[]}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			op := reviewObservationOperationForTest(provider)
			var got v1.ProviderReviewObservationResultOutcome
			switch provider {
			case "github":
				got = newGitHubPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), op)
			case "gitlab":
				got = newGitLabPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), op)
			case "bitbucket":
				got = newBitbucketPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), op)
			}
			if got.Code != v1.ProviderReviewObservationInvalidProviderResponse {
				t.Fatalf("got=%#v", got)
			}
		})
	}
}

func TestGitLabReviewObservationRejectsMalformedNotes(t *testing.T) {
	fixtures := map[string]string{
		"missing body":          `[{"id":1,"created_at":"2026-08-21T10:00:00Z","updated_at":"2026-08-21T10:01:00Z","author":{"id":42}}]`,
		"diff missing position": `[{"id":1,"body":"x","created_at":"2026-08-21T10:00:00Z","updated_at":"2026-08-21T10:01:00Z","type":"DiffNote","author":{"id":42}}]`,
		"unknown type":          `[{"id":1,"body":"x","created_at":"2026-08-21T10:00:00Z","updated_at":"2026-08-21T10:01:00Z","type":"SystemNote","author":{"id":42}}]`,
		"duplicate id":          `[{"id":1,"body":"x","created_at":"2026-08-21T10:00:00Z","updated_at":"2026-08-21T10:01:00Z","author":{"id":42}},{"id":1,"body":"y","created_at":"2026-08-21T10:00:00Z","updated_at":"2026-08-21T10:01:00Z","author":{"id":42}}]`,
	}
	for name, notes := range fixtures {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/projects/123":
					io.WriteString(w, `{"id":123,"path_with_namespace":"acme/repo"}`)
				case "/projects/123/merge_requests/7":
					io.WriteString(w, `{"iid":7,"state":"opened","sha":"0123456789abcdef0123456789abcdef01234567"}`)
				case "/projects/123/merge_requests/7/reviewers":
					io.WriteString(w, `[]`)
				case "/projects/123/merge_requests/7/notes":
					io.WriteString(w, notes)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			out := newGitLabPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), reviewObservationOperationForTest("gitlab"))
			if out.Code != v1.ProviderReviewObservationInvalidProviderResponse {
				t.Fatalf("out=%#v", out)
			}
		})
	}
}

func TestReviewObservationNormalizesHTTPFailuresAtEveryReadStage(t *testing.T) {
	for _, provider := range []string{"github", "gitlab", "bitbucket"} {
		stages := []string{"repository", "request", "comments"}
		if provider != "bitbucket" {
			stages = append(stages, "reviews")
		}
		for _, stage := range stages {
			t.Run(provider+"/"+stage, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					current := ""
					switch r.URL.Path {
					case "/repositories/123", "/projects/123", "/repositories/acme/repo":
						current = "repository"
					case "/repos/acme/repo/pulls/7", "/projects/123/merge_requests/7", "/repositories/acme/repo/pullrequests/7":
						current = "request"
					case "/repos/acme/repo/pulls/7/reviews", "/projects/123/merge_requests/7/reviewers":
						current = "reviews"
					case "/repos/acme/repo/issues/7/comments", "/repos/acme/repo/pulls/7/comments", "/projects/123/merge_requests/7/notes", "/repositories/acme/repo/pullrequests/7/comments":
						current = "comments"
					}
					if current == stage {
						http.NotFound(w, r)
						return
					}
					switch r.URL.Path {
					case "/repositories/123":
						io.WriteString(w, `{"id":123,"full_name":"acme/repo"}`)
					case "/projects/123":
						io.WriteString(w, `{"id":123,"path_with_namespace":"acme/repo"}`)
					case "/repositories/acme/repo":
						io.WriteString(w, `{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"acme/repo"}`)
					case "/repos/acme/repo/pulls/7":
						io.WriteString(w, `{"number":7,"state":"open","head":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`)
					case "/projects/123/merge_requests/7":
						io.WriteString(w, `{"iid":7,"state":"opened","sha":"0123456789abcdef0123456789abcdef01234567"}`)
					case "/repositories/acme/repo/pullrequests/7":
						io.WriteString(w, `{"id":7,"state":"OPEN","updated_on":"2026-08-21T10:00:00Z","source":{"commit":{"hash":"0123456789abcdef0123456789abcdef01234567"},"repository":{"full_name":"acme/repo"}},"destination":{"repository":{"full_name":"acme/repo"}},"reviewers":[],"participants":[]}`)
					case "/repos/acme/repo/pulls/7/reviews", "/projects/123/merge_requests/7/reviewers", "/repos/acme/repo/issues/7/comments", "/repos/acme/repo/pulls/7/comments", "/projects/123/merge_requests/7/notes":
						io.WriteString(w, `[]`)
					case "/repositories/acme/repo/pullrequests/7/comments":
						io.WriteString(w, `{"values":[]}`)
					default:
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				op := reviewObservationOperationForTest(provider)
				var out v1.ProviderReviewObservationResultOutcome
				switch provider {
				case "github":
					out = newGitHubPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), op)
				case "gitlab":
					out = newGitLabPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), op)
				case "bitbucket":
					out = newBitbucketPublisher(server.URL, "secret", server.Client()).observeProviderReviews(context.Background(), op)
				}
				if out.Kind != v1.ProviderReviewObservationPermanent || out.Code == "" {
					t.Fatalf("out=%#v", out)
				}
			})
		}
	}
}

func TestReviewObservationPaginationAndProviderDispatchAreClosed(t *testing.T) {
	publisher := newBitbucketPublisher("https://api.bitbucket.org/2.0", "secret", http.DefaultClient)
	if !publisher.bitbucketObservationNextValid("https://api.bitbucket.org/2.0/repositories/acme/repo/pullrequests/7/comments?pagelen=100&page=2", "/repositories/acme/repo/pullrequests/7/comments", 1) {
		t.Fatal("canonical next page rejected")
	}
	if publisher.bitbucketObservationNextValid("://bad", "/repositories/acme/repo/pullrequests/7/comments", 1) {
		t.Fatal("malformed next page accepted")
	}
	registry := &Registry{installations: map[string]config.SourcePublicationInstallationConfig{"installation-1": {Provider: "unknown", APIBaseURL: "https://provider.example", TokenFile: writeToken(t, 0o600)}}, httpClient: http.DefaultClient}
	op := reviewObservationOperationForTest("github")
	op.Provider = "unknown"
	op.ProviderInstanceKey = configuredProviderInstanceKey("unknown", "https://provider.example")
	if out := registry.ObserveProviderReviews(context.Background(), op); out.Code != v1.ProviderReviewObservationUnsupportedProviderInstance {
		t.Fatalf("out=%#v", out)
	}
	badSubject := reviewObservationOperationForTest("gitlab")
	badSubject.Subjects[0].ProviderAccountID = "01"
	if out := newGitLabPublisher("https://example.invalid", "secret", http.DefaultClient).revalidateReviewObservationSubjects(context.Background(), badSubject); out == nil || out.Code != v1.ProviderReviewObservationSubjectIdentityChanged {
		t.Fatalf("bad subject=%#v", out)
	}
}
