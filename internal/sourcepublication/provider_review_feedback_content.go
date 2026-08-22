package sourcepublication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

func feedbackPermanent(code v1.ProviderReviewFeedbackContentErrorCode) v1.ProviderReviewFeedbackContentResultOutcome {
	return v1.ProviderReviewFeedbackContentResultOutcome{Kind: v1.ProviderReviewFeedbackContentPermanent, Code: code}
}
func feedbackRetryable(code v1.ProviderReviewFeedbackContentErrorCode, retry *uint64) v1.ProviderReviewFeedbackContentResultOutcome {
	return v1.ProviderReviewFeedbackContentResultOutcome{Kind: v1.ProviderReviewFeedbackContentRetryable, Code: code, RetryAfterMS: retry}
}

func (registry *Registry) FetchProviderReviewFeedbackContent(ctx context.Context, op v1.ProviderReviewFeedbackContentOperation) v1.ProviderReviewFeedbackContentResultOutcome {
	installation, ok := registry.installations[op.InstallationID]
	if !ok {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentInstallationInactive)
	}
	if installation.Provider != op.Provider {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentInstallationPermissionDenied)
	}
	if op.Provider != "github" || configuredProviderInstanceKey(installation.Provider, installation.APIBaseURL) != op.ProviderInstanceKey {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentUnsupportedProviderInstance)
	}
	token, err := readToken(installation.TokenFile)
	if err != nil {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentInstallationInactive)
	}
	publisher := newGitHubPublisher(installation.APIBaseURL, token, registry.httpClient)
	verification := v1.ReviewerIdentityVerificationOperation{Provider: op.Provider, ProviderAccountID: op.ProviderAccountID, InstallationID: op.InstallationID, ProviderInstanceKey: op.ProviderInstanceKey, Repositories: []v1.ReviewerIdentityVerificationRepository{{RepositoryID: op.RepositoryID, RepositoryExternalID: op.ProviderRepositoryID, RepositoryPath: op.ProviderRepositoryPath}}}
	if failure := feedbackIdentityFailure(publisher.verifyReviewerIdentity(ctx, verification)); failure != nil {
		return *failure
	}
	return publisher.fetchReviewFeedbackContent(ctx, op)
}

func feedbackIdentityFailure(v v1.ReviewerIdentityVerificationResultOutcome) *v1.ProviderReviewFeedbackContentResultOutcome {
	if v.Kind == v1.ReviewerIdentityVerificationSucceeded && len(v.Repositories) == 1 && v.Repositories[0].EffectiveReviewerAccess == v1.ReviewerIdentityEligible {
		return nil
	}
	if v.Kind == v1.ReviewerIdentityVerificationRetryable {
		code := v1.ProviderReviewFeedbackContentProviderUnavailable
		if v.Code == v1.ReviewerIdentityRateLimited {
			code = v1.ProviderReviewFeedbackContentRateLimited
		}
		out := feedbackRetryable(code, v.RetryAfterMS)
		return &out
	}
	code := v1.ProviderReviewFeedbackContentReviewerIdentityChanged
	if v.Code == v1.ReviewerIdentityInstallationInactive || v.Code == v1.ReviewerIdentityInstallationNotFound {
		code = v1.ProviderReviewFeedbackContentInstallationInactive
	}
	if v.Code == v1.ReviewerIdentityInstallationPermissionDenied {
		code = v1.ProviderReviewFeedbackContentInstallationPermissionDenied
	}
	out := feedbackPermanent(code)
	return &out
}

type githubFeedbackRequest struct {
	Number int64  `json:"number"`
	State  string `json:"state"`
	Merged bool   `json:"merged"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

func (p *githubPublisher) fetchReviewFeedbackContent(ctx context.Context, op v1.ProviderReviewFeedbackContentOperation) v1.ProviderReviewFeedbackContentResultOutcome {
	repoID, err := strconv.ParseUint(op.ProviderRepositoryID, 10, 64)
	if err != nil || repoID == 0 || strconv.FormatUint(repoID, 10) != op.ProviderRepositoryID {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentRepositoryMismatch)
	}
	response, err := p.request(ctx, http.MethodGet, "/repositories/"+op.ProviderRepositoryID, nil, nil)
	if err != nil {
		return feedbackRetryable(v1.ProviderReviewFeedbackContentProviderUnavailable, nil)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyFeedbackHTTP(response.status, v1.ProviderReviewFeedbackContentRepositoryMismatch)
	}
	var repo struct {
		ID       uint64 `json:"id"`
		FullName string `json:"full_name"`
	}
	if !reviewObservationDecode(response.body, &repo) || repo.ID != repoID {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentRepositoryMismatch)
	}
	owner, name, ok := githubRepository(repo.FullName)
	if !ok {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentRepositoryMismatch)
	}
	number, err := canonicalPositiveInt64(op.ExternalRequestID)
	if err != nil {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentRequestNotFound)
	}
	requestPath := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/pulls/" + strconv.FormatInt(number, 10)
	request, failure := p.githubFeedbackRequest(ctx, requestPath, number)
	if failure != nil {
		return *failure
	}
	if request.Merged || request.State != "open" {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentRequestNotOpen)
	}
	if request.Head.SHA != op.ExpectedHeadSHA {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentHeadMismatch)
	}
	response, err = p.request(ctx, http.MethodGet, requestPath+"/reviews/"+op.ProviderReviewEventID, nil, nil)
	if err != nil {
		return feedbackRetryable(v1.ProviderReviewFeedbackContentProviderUnavailable, nil)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyFeedbackHTTP(response.status, v1.ProviderReviewFeedbackContentReviewEventNotFound)
	}
	var review struct {
		ID          uint64                `json:"id"`
		State       string                `json:"state"`
		Body        *string               `json:"body"`
		SubmittedAt time.Time             `json:"submitted_at"`
		CommitID    string                `json:"commit_id"`
		User        githubReviewerAccount `json:"user"`
	}
	if !reviewObservationDecode(response.body, &review) || review.ID == 0 || strconv.FormatUint(review.ID, 10) != op.ProviderReviewEventID || review.Body == nil || review.User.ID == 0 || review.User.Type != "User" || strconv.FormatUint(review.User.ID, 10) != op.ProviderAccountID || review.SubmittedAt.IsZero() || !review.SubmittedAt.Equal(op.ReviewEventOccurredAt) || review.CommitID != op.ExpectedHeadSHA || op.ProviderReviewEventRevision != 1 {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentReviewEventMetadataChanged)
	}
	state, kind, validState := githubReviewState(review.State)
	if !validState || kind != op.ReviewEventKind || state != op.ReviewState {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentReviewEventMetadataChanged)
	}
	if outcome := feedbackSuccess(op, *review.Body); outcome.Kind != v1.ProviderReviewFeedbackContentSucceeded {
		return outcome
	}
	post, failure := p.githubFeedbackRequest(ctx, requestPath, number)
	if failure != nil {
		return *failure
	}
	if post.Merged || post.State != "open" {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentRequestNotOpen)
	}
	if post.Head.SHA != op.ExpectedHeadSHA {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentHeadMismatch)
	}
	return feedbackSuccess(op, *review.Body)
}

func (p *githubPublisher) githubFeedbackRequest(ctx context.Context, path string, number int64) (githubFeedbackRequest, *v1.ProviderReviewFeedbackContentResultOutcome) {
	response, err := p.request(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		out := feedbackRetryable(v1.ProviderReviewFeedbackContentProviderUnavailable, nil)
		return githubFeedbackRequest{}, &out
	}
	if response.status < 200 || response.status >= 300 {
		out := classifyFeedbackHTTP(response.status, v1.ProviderReviewFeedbackContentRequestNotFound)
		return githubFeedbackRequest{}, &out
	}
	var request githubFeedbackRequest
	if !reviewObservationDecode(response.body, &request) || request.Number != number || request.Head.SHA == "" {
		out := feedbackPermanent(v1.ProviderReviewFeedbackContentInvalidProviderResponse)
		return githubFeedbackRequest{}, &out
	}
	return request, nil
}

func feedbackSuccess(op v1.ProviderReviewFeedbackContentOperation, content string) v1.ProviderReviewFeedbackContentResultOutcome {
	if uint64(len([]byte(content))) > v1.ProviderReviewFeedbackContentMaxBytes {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentTooLarge)
	}
	digest := sha256.Sum256([]byte(content))
	if uint64(len([]byte(content))) != op.ContentByteCount || hex.EncodeToString(digest[:]) != op.ContentDigest {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentReviewEventMetadataChanged)
	}
	if err := validation.ProviderReviewFeedbackContentNormalizedText(content, op.ContentDigest, op.ContentByteCount); err != nil {
		return feedbackPermanent(v1.ProviderReviewFeedbackContentInvalidPlainText)
	}
	return v1.ProviderReviewFeedbackContentResultOutcome{Kind: v1.ProviderReviewFeedbackContentSucceeded, RequestState: v1.ProviderReviewObservationRequestOpen, ObservedHeadSHA: op.ExpectedHeadSHA, ContentEncoding: "utf-8", ContentFormat: "plain_text", UnicodeNormalization: "NFC", LineEndings: "LF", ContentDigest: op.ContentDigest, ContentByteCount: op.ContentByteCount, Content: content}
}

func classifyFeedbackHTTP(status int, notFound v1.ProviderReviewFeedbackContentErrorCode) v1.ProviderReviewFeedbackContentResultOutcome {
	switch status {
	case http.StatusNotFound:
		return feedbackPermanent(notFound)
	case http.StatusUnauthorized, http.StatusForbidden:
		return feedbackPermanent(v1.ProviderReviewFeedbackContentInstallationPermissionDenied)
	case http.StatusTooManyRequests:
		return feedbackRetryable(v1.ProviderReviewFeedbackContentRateLimited, nil)
	default:
		if status >= 500 {
			return feedbackRetryable(v1.ProviderReviewFeedbackContentProviderUnavailable, nil)
		}
		return feedbackPermanent(v1.ProviderReviewFeedbackContentInvalidProviderResponse)
	}
}
