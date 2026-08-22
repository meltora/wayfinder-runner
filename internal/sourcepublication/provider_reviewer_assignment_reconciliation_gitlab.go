package sourcepublication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func (publisher *gitLabPublisher) reconcileReviewerAssignment(ctx context.Context, operation v1.ProviderReviewerAssignmentReconciliationOperation) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
	repositoryID, err := strconv.ParseUint(operation.ProviderRepositoryID, 10, 64)
	if err != nil || repositoryID == 0 {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationRepositoryMismatch)
	}
	projectPath := "/projects/" + url.PathEscape(operation.ProviderRepositoryID)
	response, err := publisher.request(ctx, http.MethodGet, projectPath, nil, nil)
	if err != nil {
		return reconciliationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyGitLabReconciliationFailure(response, v1.ProviderReviewerAssignmentReconciliationRepositoryMismatch)
	}
	var project struct {
		ID                uint64 `json:"id"`
		PathWithNamespace string `json:"path_with_namespace"`
	}
	if json.Unmarshal(response.body, &project) != nil || project.ID != repositoryID || project.PathWithNamespace == "" {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationRepositoryMismatch)
	}
	iid, err := canonicalPositiveInt64(operation.ExternalRequestID)
	if err != nil {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationRequestNotFound)
	}
	requestPath := projectPath + "/merge_requests/" + strconv.FormatInt(iid, 10)
	response, err = publisher.request(ctx, http.MethodGet, requestPath, nil, nil)
	if err != nil {
		return reconciliationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyGitLabReconciliationFailure(response, v1.ProviderReviewerAssignmentReconciliationRequestNotFound)
	}
	var request struct {
		IID   int64  `json:"iid"`
		State string `json:"state"`
		SHA   string `json:"sha"`
	}
	if json.Unmarshal(response.body, &request) != nil || request.IID != iid || request.SHA == "" {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
	}
	state := v1.ProviderReviewerAssignmentReconciliationRequestState("")
	switch request.State {
	case "opened":
		state = v1.ProviderReviewerAssignmentReconciliationRequestOpen
	case "closed":
		state = v1.ProviderReviewerAssignmentReconciliationRequestClosed
	case "merged":
		state = v1.ProviderReviewerAssignmentReconciliationRequestMerged
	default:
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
	}
	response, err = publisher.request(ctx, http.MethodGet, requestPath+"/reviewers", nil, nil)
	if err != nil {
		return reconciliationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyGitLabReconciliationFailure(response, v1.ProviderReviewerAssignmentReconciliationRequestNotFound)
	}
	var reviewers []struct {
		User struct {
			ID       uint64 `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
		State string `json:"state"`
	}
	if json.Unmarshal(response.body, &reviewers) != nil || len(reviewers) > 100 {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationObservationIncomplete)
	}
	presence := make(map[string]v1.ProviderReviewerAssignmentReconciliationPresence, len(reviewers))
	seen := make(map[string]struct{}, len(reviewers))
	for _, reviewer := range reviewers {
		if reviewer.User.ID == 0 || reviewer.User.Username == "" {
			return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
		}
		id := strconv.FormatUint(reviewer.User.ID, 10)
		if _, dup := seen[id]; dup {
			return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
		}
		seen[id] = struct{}{}
		switch reviewer.State {
		case "unreviewed", "review_started":
			presence[id] = v1.ProviderReviewerAssignmentReconciliationRequested
		case "reviewed", "requested_changes", "approved", "unapproved":
			presence[id] = v1.ProviderReviewerAssignmentReconciliationReviewed
		default:
			return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
		}
	}
	return reconciliationSucceeded(operation, state, request.SHA, presence)
}

func classifyGitLabReconciliationFailure(response gitLabResponse, notFound v1.ProviderReviewerAssignmentReconciliationErrorCode) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
	if response.status == http.StatusRequestTimeout || response.status >= 500 && response.status <= 599 {
		return reconciliationRetryable(v1.ProviderReviewerAssignmentReconciliationProviderUnavailable, providerRetryAfter(response.header, time.Now()))
	}
	switch response.status {
	case http.StatusUnauthorized:
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInstallationInactive)
	case http.StatusForbidden:
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInstallationPermissionDenied)
	case http.StatusNotFound:
		return reconciliationPermanent(notFound)
	case http.StatusTooManyRequests:
		return reconciliationRetryable(v1.ProviderReviewerAssignmentReconciliationRateLimited, providerRetryAfter(response.header, time.Now()))
	default:
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
	}
}
