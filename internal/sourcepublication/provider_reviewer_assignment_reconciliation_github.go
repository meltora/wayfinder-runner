package sourcepublication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var githubReconciliationLinkPattern = regexp.MustCompile(`^<([^<>[:space:]]+)>;[[:space:]]*rel="(next|prev|first|last)"$`)

func (publisher *githubPublisher) reconcileReviewerAssignment(ctx context.Context, operation v1.ProviderReviewerAssignmentReconciliationOperation) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
	repositoryID, err := strconv.ParseUint(operation.ProviderRepositoryID, 10, 64)
	if err != nil || repositoryID == 0 {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationRepositoryMismatch)
	}
	response, err := publisher.request(ctx, http.MethodGet, "/repositories/"+operation.ProviderRepositoryID, nil, nil)
	if err != nil {
		return reconciliationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyGitHubReconciliationFailure(response, v1.ProviderReviewerAssignmentReconciliationRepositoryMismatch)
	}
	var repository struct {
		ID       uint64 `json:"id"`
		FullName string `json:"full_name"`
	}
	if json.Unmarshal(response.body, &repository) != nil || repository.ID != repositoryID {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationRepositoryMismatch)
	}
	owner, repo, valid := githubRepository(repository.FullName)
	if !valid {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationRepositoryMismatch)
	}
	number, err := canonicalPositiveInt64(operation.ExternalRequestID)
	if err != nil {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationRequestNotFound)
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/pulls/" + strconv.FormatInt(number, 10)
	response, err = publisher.request(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return reconciliationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyGitHubReconciliationFailure(response, v1.ProviderReviewerAssignmentReconciliationRequestNotFound)
	}
	var request struct {
		Number int64  `json:"number"`
		State  string `json:"state"`
		Merged bool   `json:"merged"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if json.Unmarshal(response.body, &request) != nil || request.Number != number || request.Head.SHA == "" {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
	}
	state := v1.ProviderReviewerAssignmentReconciliationRequestState("")
	if request.Merged {
		state = v1.ProviderReviewerAssignmentReconciliationRequestMerged
	} else if request.State == "open" {
		state = v1.ProviderReviewerAssignmentReconciliationRequestOpen
	} else if request.State == "closed" {
		state = v1.ProviderReviewerAssignmentReconciliationRequestClosed
	} else {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
	}
	presence := make(map[string]v1.ProviderReviewerAssignmentReconciliationPresence)
	response, err = publisher.request(ctx, http.MethodGet, path+"/requested_reviewers", nil, nil)
	if err != nil {
		return reconciliationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyGitHubReconciliationFailure(response, v1.ProviderReviewerAssignmentReconciliationRequestNotFound)
	}
	var requested struct {
		Users *[]githubReviewerAccount `json:"users"`
		Teams *[]json.RawMessage       `json:"teams"`
	}
	if json.Unmarshal(response.body, &requested) != nil || requested.Users == nil || requested.Teams == nil || len(*requested.Users) > 100 || len(*requested.Teams) > 100 {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
	}
	seen := make(map[string]struct{}, len(*requested.Users))
	for _, account := range *requested.Users {
		if account.ID == 0 || account.Login == "" || account.Type != "User" {
			return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
		}
		id := strconv.FormatUint(account.ID, 10)
		if _, dup := seen[id]; dup {
			return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
		}
		seen[id] = struct{}{}
		presence[id] = v1.ProviderReviewerAssignmentReconciliationRequested
	}
	seenReviewIDs := make(map[uint64]struct{})
	for page := 1; page <= 10; page++ {
		response, err = publisher.request(ctx, http.MethodGet, path+"/reviews", url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}, nil)
		if err != nil {
			return reconciliationRequestFailure(err)
		}
		if response.status < 200 || response.status >= 300 {
			return classifyGitHubReconciliationFailure(response, v1.ProviderReviewerAssignmentReconciliationRequestNotFound)
		}
		var reviews []struct {
			ID    uint64                `json:"id"`
			State string                `json:"state"`
			User  githubReviewerAccount `json:"user"`
		}
		if json.Unmarshal(response.body, &reviews) != nil || len(reviews) > 100 {
			return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
		}
		for _, review := range reviews {
			if review.ID == 0 || review.State == "" || review.User.ID == 0 || review.User.Login == "" || review.User.Type != "User" {
				return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
			}
			if _, duplicate := seenReviewIDs[review.ID]; duplicate {
				return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
			}
			seenReviewIDs[review.ID] = struct{}{}
			switch review.State {
			case "PENDING":
			case "COMMENTED", "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
				upgradePresence(presence, strconv.FormatUint(review.User.ID, 10), v1.ProviderReviewerAssignmentReconciliationReviewed)
			default:
				return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
			}
		}
		hasNext, hasMetadata, validMetadata := githubReconciliationPagination(response.header.Get("Link"))
		if !validMetadata || hasNext && len(reviews) == 0 {
			return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationObservationIncomplete)
		}
		if hasNext {
			if page == 10 {
				return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationObservationIncomplete)
			}
			continue
		}
		if len(reviews) < 100 || hasMetadata {
			break
		}
		if page == 10 {
			return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationObservationIncomplete)
		}
	}
	return reconciliationSucceeded(operation, state, request.Head.SHA, presence)
}

func githubReconciliationPagination(header string) (hasNext, hasMetadata, valid bool) {
	if header == "" {
		return false, false, true
	}
	seen := make(map[string]struct{}, 4)
	for _, part := range strings.Split(header, ",") {
		matches := githubReconciliationLinkPattern.FindStringSubmatch(strings.TrimSpace(part))
		if len(matches) != 3 {
			return false, true, false
		}
		link, err := url.ParseRequestURI(matches[1])
		if err != nil || !link.IsAbs() {
			return false, true, false
		}
		relation := matches[2]
		if _, duplicate := seen[relation]; duplicate {
			return false, true, false
		}
		seen[relation] = struct{}{}
	}
	_, hasNext = seen["next"]
	return hasNext, len(seen) > 0, true
}

func classifyGitHubReconciliationFailure(response githubResponse, notFound v1.ProviderReviewerAssignmentReconciliationErrorCode) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
	if response.status == http.StatusRequestTimeout || response.status >= 500 && response.status <= 599 {
		return reconciliationRetryable(v1.ProviderReviewerAssignmentReconciliationProviderUnavailable, providerRetryAfter(response.header, time.Now()))
	}
	switch response.status {
	case http.StatusUnauthorized:
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInstallationInactive)
	case http.StatusForbidden:
		if response.header.Get("X-RateLimit-Remaining") == "0" || response.header.Get("Retry-After") != "" {
			return reconciliationRetryable(v1.ProviderReviewerAssignmentReconciliationRateLimited, providerRetryAfter(response.header, time.Now()))
		}
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInstallationPermissionDenied)
	case http.StatusNotFound:
		return reconciliationPermanent(notFound)
	case http.StatusTooManyRequests:
		return reconciliationRetryable(v1.ProviderReviewerAssignmentReconciliationRateLimited, providerRetryAfter(response.header, time.Now()))
	default:
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
	}
}
