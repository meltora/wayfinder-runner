package sourcepublication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

func feedbackOperationForTest() v1.ProviderReviewFeedbackContentOperation {
	now := time.Now().UTC()
	lease := now.Add(30 * time.Minute)
	expires := lease.Add(time.Hour)
	return v1.ProviderReviewFeedbackContentOperation{ProviderReviewFeedbackContentAuthority: v1.ProviderReviewFeedbackContentAuthority{SchemaVersion: 1, ProviderReviewFeedbackContentOperationID: "provider-review-feedback-content-operation-" + strings.Repeat("1", 64), OrganizationID: "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1", WorkflowStateRevision: 1, AssignmentDeliveryID: "delivery-1", AssignmentDeliveryStateRevision: 1, AssignmentObservationID: "observation-1", ObservedReviewerID: "observed-1", ProviderReviewObservationID: "review-observation-1", ProviderReviewObservationResultPayloadChecksum: strings.Repeat("2", 64), EvidenceObservedAt: now.Add(-time.Minute), Purpose: "review_feedback_continuation", ContentSource: "provider_review_event", ProviderReviewEventID: "7001", ProviderReviewEventRevision: 1, ReviewEventKind: v1.ProviderReviewObservationChangeRequest, ReviewState: v1.ProviderReviewObservationChangesRequested, ReviewEventOccurredAt: time.Date(2026, 8, 22, 11, 58, 0, 0, time.UTC), ContentDigest: "938baec10b632617f4a4779cd8c576ac27dd676d399ba72c8fd180e90f490527", ContentByteCount: 25, ProviderAccountID: "42", UserID: "user-1", OrganizationMembershipID: "membership-1", OrganizationMembershipStateRevision: 1, CodeReviewAuthorityRevision: 1, CodeReviewAuthorityDigest: strings.Repeat("3", 64), ReviewerIdentityID: "identity-1", IdentityRevision: 1, IdentityVerificationID: "verification-1", IdentityVerificationDigest: strings.Repeat("4", 64), IdentityVerificationExpiresAt: expires, ProviderAccountProofID: "proof-1", ProviderAccountProofRevision: 1, ProviderAccountProofDigest: strings.Repeat("5", 64), ProviderAccountProofExpiresAt: expires.Add(time.Hour), RepositoryID: "repository-1", ProviderRepositoryID: "123", ExternalRequestID: "7", ExpectedHeadSHA: strings.Repeat("b", 40), InstallationID: "installation-1", InstallationAuthorityDigest: strings.Repeat("a", 64), Provider: "github", ProviderInstanceKey: "github.com", FeedbackContentRequestDigest: strings.Repeat("6", 64), IdempotencyKey: "provider-review-feedback-content-" + strings.Repeat("8", 64), OperationDigest: strings.Repeat("9", 64), CommandDigest: strings.Repeat("e", 64), LeaseID: "lease-1", FencingToken: 1, LeaseExpiresAt: lease}}
}

func TestReviewFeedbackContentNormalizesFailuresWithoutRawProviderData(t *testing.T) {
	for name, value := range map[string]v1.ProviderReviewFeedbackContentResultOutcome{
		"not found":   classifyFeedbackHTTP(http.StatusNotFound, v1.ProviderReviewFeedbackContentReviewEventNotFound),
		"permission":  classifyFeedbackHTTP(http.StatusForbidden, v1.ProviderReviewFeedbackContentReviewEventNotFound),
		"rate":        classifyFeedbackHTTP(http.StatusTooManyRequests, v1.ProviderReviewFeedbackContentReviewEventNotFound),
		"unavailable": classifyFeedbackHTTP(http.StatusBadGateway, v1.ProviderReviewFeedbackContentReviewEventNotFound),
		"invalid":     classifyFeedbackHTTP(http.StatusTeapot, v1.ProviderReviewFeedbackContentReviewEventNotFound),
	} {
		if value.Kind == "" || value.Code == "" {
			t.Fatalf("%s=%#v", name, value)
		}
	}
	retry := feedbackIdentityFailure(v1.ReviewerIdentityVerificationResultOutcome{Kind: v1.ReviewerIdentityVerificationRetryable, Code: v1.ReviewerIdentityRateLimited})
	inactive := feedbackIdentityFailure(v1.ReviewerIdentityVerificationResultOutcome{Kind: v1.ReviewerIdentityVerificationPermanent, Code: v1.ReviewerIdentityInstallationInactive})
	permission := feedbackIdentityFailure(v1.ReviewerIdentityVerificationResultOutcome{Kind: v1.ReviewerIdentityVerificationPermanent, Code: v1.ReviewerIdentityInstallationPermissionDenied})
	if retry == nil || retry.Code != v1.ProviderReviewFeedbackContentRateLimited || inactive.Code != v1.ProviderReviewFeedbackContentInstallationInactive || permission.Code != v1.ProviderReviewFeedbackContentInstallationPermissionDenied {
		t.Fatal("identity failure mapping changed")
	}
}

func TestReviewFeedbackContentRejectsSizeDigestAndUnsafeText(t *testing.T) {
	op := feedbackOperationForTest()
	if got := feedbackSuccess(op, strings.Repeat("x", v1.ProviderReviewFeedbackContentMaxBytes+1)); got.Code != v1.ProviderReviewFeedbackContentTooLarge {
		t.Fatalf("large=%#v", got)
	}
	if got := feedbackSuccess(op, "different"); got.Code != v1.ProviderReviewFeedbackContentReviewEventMetadataChanged {
		t.Fatalf("drift=%#v", got)
	}
	unsafe := "bad\rtext"
	sum := sha256.Sum256([]byte(unsafe))
	op.ContentDigest = hex.EncodeToString(sum[:])
	op.ContentByteCount = uint64(len(unsafe))
	if got := feedbackSuccess(op, unsafe); got.Code != v1.ProviderReviewFeedbackContentInvalidPlainText {
		t.Fatalf("unsafe=%#v", got)
	}
}

func TestGitHubFeedbackRequestRejectsMalformedAndUnavailableResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		case "/bad":
			io.WriteString(w, `{"number":7,"state":"open"}`)
		default:
			http.Error(w, "down", http.StatusBadGateway)
		}
	}))
	defer server.Close()
	p := newGitHubPublisher(server.URL, "secret", server.Client())
	for _, path := range []string{"/missing", "/bad", "/down"} {
		if _, failure := p.githubFeedbackRequest(context.Background(), path, 7); failure == nil {
			t.Fatalf("path %s accepted", path)
		}
	}
}

func TestGitHubReviewFeedbackContentFetchesExactNativeReviewAndRechecksHead(t *testing.T) {
	op := feedbackOperationForTest()
	methods := map[string]int{}
	pullReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods[r.Method]++
		if r.Method != http.MethodGet {
			t.Fatalf("mutation %s", r.Method)
		}
		switch r.URL.Path {
		case "/repositories/123":
			io.WriteString(w, `{"id":123,"full_name":"acme/repo"}`)
		case "/repos/acme/repo/pulls/7":
			pullReads++
			io.WriteString(w, `{"number":7,"state":"open","head":{"sha":"`+op.ExpectedHeadSHA+`"}}`)
		case "/repos/acme/repo/pulls/7/reviews/7001":
			io.WriteString(w, `{"id":7001,"state":"CHANGES_REQUESTED","body":"Café\nFix the retry path.","submitted_at":"2026-08-22T11:58:00Z","commit_id":"`+op.ExpectedHeadSHA+`","user":{"id":42,"type":"User"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := newGitHubPublisher(server.URL, "secret", server.Client()).fetchReviewFeedbackContent(context.Background(), op)
	if out.Kind != v1.ProviderReviewFeedbackContentSucceeded || out.Content != "Café\nFix the retry path." || pullReads != 2 {
		t.Fatalf("out=%#v pullReads=%d", out, pullReads)
	}
	if len(methods) != 1 {
		t.Fatalf("methods=%v", methods)
	}
}

func TestGitHubReviewFeedbackContentRejectsHeadDriftAfterFetch(t *testing.T) {
	op := feedbackOperationForTest()
	pullReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repositories/123":
			io.WriteString(w, `{"id":123,"full_name":"acme/repo"}`)
		case "/repos/acme/repo/pulls/7":
			pullReads++
			head := op.ExpectedHeadSHA
			if pullReads == 2 {
				head = strings.Repeat("a", 40)
			}
			io.WriteString(w, `{"number":7,"state":"open","head":{"sha":"`+head+`"}}`)
		case "/repos/acme/repo/pulls/7/reviews/7001":
			io.WriteString(w, `{"id":7001,"state":"CHANGES_REQUESTED","body":"Café\nFix the retry path.","submitted_at":"2026-08-22T11:58:00Z","commit_id":"`+op.ExpectedHeadSHA+`","user":{"id":42,"type":"User"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	out := newGitHubPublisher(server.URL, "secret", server.Client()).fetchReviewFeedbackContent(context.Background(), op)
	if out.Kind != v1.ProviderReviewFeedbackContentPermanent || out.Code != v1.ProviderReviewFeedbackContentHeadMismatch {
		t.Fatalf("out=%#v", out)
	}
}

func TestReviewFeedbackContentRegistryRejectsStableActorDriftBeforeFetch(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/user/42" {
			io.WriteString(w, `{"id":99,"login":"impostor","type":"User"}`)
			return
		}
		t.Fatalf("unexpected read %s", r.URL.Path)
	}))
	defer server.Close()
	registry := &Registry{installations: map[string]config.SourcePublicationInstallationConfig{"installation-1": {Provider: "github", APIBaseURL: server.URL, TokenFile: writeToken(t, 0o600)}}, httpClient: server.Client()}
	op := feedbackOperationForTest()
	op.ProviderInstanceKey = configuredProviderInstanceKey("github", server.URL)
	out := registry.FetchProviderReviewFeedbackContent(context.Background(), op)
	if out.Kind != v1.ProviderReviewFeedbackContentPermanent || out.Code != v1.ProviderReviewFeedbackContentReviewerIdentityChanged || requests != 1 {
		t.Fatalf("out=%#v requests=%d", out, requests)
	}
}

func TestReviewFeedbackContentRejectsProviderWithoutNativeReviewFetch(t *testing.T) {
	for _, provider := range []string{"gitlab", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests++
			}))
			defer server.Close()
			registry := &Registry{installations: map[string]config.SourcePublicationInstallationConfig{"installation-1": {Provider: provider, APIBaseURL: server.URL, TokenFile: writeToken(t, 0o600)}}, httpClient: server.Client()}
			op := feedbackOperationForTest()
			op.Provider = provider
			op.ProviderInstanceKey = configuredProviderInstanceKey(provider, server.URL)
			out := registry.FetchProviderReviewFeedbackContent(context.Background(), op)
			if out.Code != v1.ProviderReviewFeedbackContentUnsupportedProviderInstance || requests != 0 {
				t.Fatalf("out=%#v requests=%d", out, requests)
			}
		})
	}
}

func TestReviewFeedbackContentRegistryFailsClosedAtInstallationBoundary(t *testing.T) {
	op := feedbackOperationForTest()
	empty := &Registry{installations: map[string]config.SourcePublicationInstallationConfig{}, httpClient: http.DefaultClient}
	if got := empty.FetchProviderReviewFeedbackContent(context.Background(), op); got.Code != v1.ProviderReviewFeedbackContentInstallationInactive {
		t.Fatalf("missing=%#v", got)
	}
	wrong := &Registry{installations: map[string]config.SourcePublicationInstallationConfig{"installation-1": {Provider: "gitlab", APIBaseURL: "https://gitlab.com", TokenFile: writeToken(t, 0o600)}}, httpClient: http.DefaultClient}
	if got := wrong.FetchProviderReviewFeedbackContent(context.Background(), op); got.Code != v1.ProviderReviewFeedbackContentInstallationPermissionDenied {
		t.Fatalf("provider=%#v", got)
	}
	badToken := &Registry{installations: map[string]config.SourcePublicationInstallationConfig{"installation-1": {Provider: "github", APIBaseURL: "https://api.github.com", TokenFile: "/missing/token"}}, httpClient: http.DefaultClient}
	if got := badToken.FetchProviderReviewFeedbackContent(context.Background(), op); got.Code != v1.ProviderReviewFeedbackContentInstallationInactive {
		t.Fatalf("token=%#v", got)
	}
}
