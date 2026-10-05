package sourcepublication

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type githubAssignmentSnapshot struct {
	present map[string]struct{}
}

const maxAssignmentRecipients = 100

func (publisher *githubPublisher) assignReviewers(
	ctx context.Context,
	command v1.ProviderReviewerAssignmentOperation,
) v1.ProviderReviewerAssignmentResultOutcome {
	owner, repository, failure := publisher.assignmentRepository(ctx, command)
	if failure != nil {
		return *failure
	}
	requestNumber, err := canonicalPositiveInt64(command.ExternalRequestID)
	if err != nil {
		return assignmentPermanent(v1.ProviderReviewerAssignmentRequestNotFound)
	}
	repositoryPath := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repository)
	requestPath := repositoryPath + "/pulls/" + strconv.FormatInt(requestNumber, 10)
	if _, failure = publisher.assignmentSnapshot(ctx, requestPath, command); failure != nil {
		return *failure
	}
	principals, accountIDs, failure := publisher.revalidateAssignmentRecipients(ctx, command)
	if failure != nil {
		return *failure
	}
	// This second read is the pre-mutation authority fence. It happens after
	// every provider-account and membership read so a drifting head cannot be
	// mutated under stale evidence.
	current, failure := publisher.assignmentSnapshot(ctx, requestPath, command)
	if failure != nil {
		return *failure
	}
	if containsAllStableIDs(current.present, accountIDs) {
		return assignmentSucceeded(command, accountIDs)
	}
	body, _ := json.Marshal(map[string][]string{"reviewers": principals})
	response, mutationErr := publisher.request(
		ctx, http.MethodPost, requestPath+"/requested_reviewers", nil, body,
	)
	if errors.Is(mutationErr, errProviderCredentialExpiring) {
		return assignmentRetryable(v1.ProviderReviewerAssignmentProviderUnavailable, nil)
	}
	// Once the mutation has been attempted, no transport or status response is
	// sufficient proof. Only a fresh provider read may turn it into success.
	after, reconcileFailure := publisher.assignmentSnapshot(ctx, requestPath, command)
	if reconcileFailure == nil && containsAllStableIDs(after.present, accountIDs) {
		return assignmentSucceeded(command, accountIDs)
	}
	_ = response
	_ = mutationErr
	return assignmentUncertain()
}

func (publisher *githubPublisher) assignmentRepository(
	ctx context.Context,
	command v1.ProviderReviewerAssignmentOperation,
) (string, string, *v1.ProviderReviewerAssignmentResultOutcome) {
	repositoryID, err := strconv.ParseUint(command.ProviderRepositoryID, 10, 64)
	if err != nil || repositoryID == 0 || strconv.FormatUint(repositoryID, 10) != command.ProviderRepositoryID {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentRequestNotFound)
		return "", "", &outcome
	}
	response, err := publisher.request(ctx, http.MethodGet, "/repositories/"+command.ProviderRepositoryID, nil, nil)
	if err != nil {
		outcome := assignmentRequestFailure(err)
		return "", "", &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := classifyGitHubAssignmentFailure(response, v1.ProviderReviewerAssignmentRequestNotFound)
		return "", "", &outcome
	}
	var providerRepository struct {
		ID       uint64 `json:"id"`
		FullName string `json:"full_name"`
	}
	if json.Unmarshal(response.body, &providerRepository) != nil || providerRepository.ID != repositoryID {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentRepositoryMismatch)
		return "", "", &outcome
	}
	owner, repository, valid := githubRepository(providerRepository.FullName)
	if !valid {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentRepositoryMismatch)
		return "", "", &outcome
	}
	return owner, repository, nil
}

func (publisher *githubPublisher) assignmentSnapshot(
	ctx context.Context,
	requestPath string,
	command v1.ProviderReviewerAssignmentOperation,
) (githubAssignmentSnapshot, *v1.ProviderReviewerAssignmentResultOutcome) {
	response, err := publisher.request(ctx, http.MethodGet, requestPath, nil, nil)
	if err != nil {
		outcome := assignmentRequestFailure(err)
		return githubAssignmentSnapshot{}, &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := classifyGitHubAssignmentFailure(response, v1.ProviderReviewerAssignmentRequestNotFound)
		return githubAssignmentSnapshot{}, &outcome
	}
	var request struct {
		Number int64  `json:"number"`
		State  string `json:"state"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	expectedNumber, _ := canonicalPositiveInt64(command.ExternalRequestID)
	if json.Unmarshal(response.body, &request) != nil || request.Number != expectedNumber || request.Head.SHA == "" {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentRequestNotFound)
		return githubAssignmentSnapshot{}, &outcome
	}
	if request.State != "open" {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentRequestNotOpen)
		return githubAssignmentSnapshot{}, &outcome
	}
	if request.Head.SHA != command.HeadSHA {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentHeadMismatch)
		return githubAssignmentSnapshot{}, &outcome
	}
	present := make(map[string]struct{})
	response, err = publisher.request(ctx, http.MethodGet, requestPath+"/requested_reviewers", nil, nil)
	if err != nil {
		outcome := assignmentRequestFailure(err)
		return githubAssignmentSnapshot{}, &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := classifyGitHubAssignmentFailure(response, v1.ProviderReviewerAssignmentRequestNotFound)
		return githubAssignmentSnapshot{}, &outcome
	}
	var requested struct {
		Users *[]githubReviewerAccount `json:"users"`
		Teams *[]json.RawMessage       `json:"teams"`
	}
	if json.Unmarshal(response.body, &requested) != nil || requested.Users == nil || requested.Teams == nil ||
		len(*requested.Users) > maxAssignmentRecipients {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentInvalidProviderResponse)
		return githubAssignmentSnapshot{}, &outcome
	}
	for _, account := range *requested.Users {
		if account.ID == 0 || account.Login == "" || account.Type != "User" {
			outcome := assignmentPermanent(v1.ProviderReviewerAssignmentInvalidProviderResponse)
			return githubAssignmentSnapshot{}, &outcome
		}
		present[strconv.FormatUint(account.ID, 10)] = struct{}{}
	}
	for page := 1; page <= 10; page++ {
		response, err = publisher.request(ctx, http.MethodGet, requestPath+"/reviews", url.Values{
			"per_page": {"100"}, "page": {strconv.Itoa(page)},
		}, nil)
		if err != nil {
			outcome := assignmentRequestFailure(err)
			return githubAssignmentSnapshot{}, &outcome
		}
		if response.status < 200 || response.status >= 300 {
			outcome := classifyGitHubAssignmentFailure(response, v1.ProviderReviewerAssignmentRequestNotFound)
			return githubAssignmentSnapshot{}, &outcome
		}
		var reviews []struct {
			ID    uint64                `json:"id"`
			State string                `json:"state"`
			User  githubReviewerAccount `json:"user"`
		}
		if json.Unmarshal(response.body, &reviews) != nil || len(reviews) > 100 {
			outcome := assignmentPermanent(v1.ProviderReviewerAssignmentInvalidProviderResponse)
			return githubAssignmentSnapshot{}, &outcome
		}
		for _, review := range reviews {
			if review.ID == 0 || review.State == "" || review.User.ID == 0 || review.User.Login == "" || review.User.Type != "User" {
				outcome := assignmentPermanent(v1.ProviderReviewerAssignmentInvalidProviderResponse)
				return githubAssignmentSnapshot{}, &outcome
			}
			if review.State != "PENDING" {
				present[strconv.FormatUint(review.User.ID, 10)] = struct{}{}
			}
		}
		if len(reviews) < 100 {
			break
		}
		if page == 10 {
			outcome := assignmentPermanent(v1.ProviderReviewerAssignmentInvalidProviderResponse)
			return githubAssignmentSnapshot{}, &outcome
		}
	}
	return githubAssignmentSnapshot{present: present}, nil
}

func (publisher *githubPublisher) revalidateAssignmentRecipients(
	ctx context.Context,
	command v1.ProviderReviewerAssignmentOperation,
) ([]string, []string, *v1.ProviderReviewerAssignmentResultOutcome) {
	principals := make([]string, 0, len(command.Recipients))
	accountIDs := make([]string, 0, len(command.Recipients))
	for _, recipient := range command.Recipients {
		verification := publisher.verifyReviewerIdentity(ctx, assignmentIdentityVerification(command, recipient))
		if failure := assignmentIdentityFailure(verification, recipient); failure != nil {
			return nil, nil, failure
		}
		principals = append(principals, verification.AssignmentPrincipal)
		accountIDs = append(accountIDs, recipient.ProviderAccountID)
	}
	sort.Strings(principals)
	sort.Strings(accountIDs)
	return principals, accountIDs, nil
}

func classifyGitHubAssignmentFailure(
	response githubResponse,
	notFoundCode v1.ProviderReviewerAssignmentErrorCode,
) v1.ProviderReviewerAssignmentResultOutcome {
	if response.status == http.StatusRequestTimeout || response.status >= 500 && response.status <= 599 {
		return assignmentRetryable(v1.ProviderReviewerAssignmentProviderUnavailable, providerRetryAfter(response.header, time.Now()))
	}
	switch response.status {
	case http.StatusUnauthorized:
		return assignmentPermanent(v1.ProviderReviewerAssignmentInstallationInactive)
	case http.StatusForbidden:
		if response.header.Get("X-RateLimit-Remaining") == "0" || response.header.Get("Retry-After") != "" {
			return assignmentRetryable(v1.ProviderReviewerAssignmentRateLimited, providerRetryAfter(response.header, time.Now()))
		}
		return assignmentPermanent(v1.ProviderReviewerAssignmentInstallationPermissionDenied)
	case http.StatusNotFound:
		return assignmentPermanent(notFoundCode)
	case http.StatusTooManyRequests:
		return assignmentRetryable(v1.ProviderReviewerAssignmentRateLimited, providerRetryAfter(response.header, time.Now()))
	default:
		return assignmentPermanent(v1.ProviderReviewerAssignmentInvalidProviderResponse)
	}
}

func canonicalPositiveInt64(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, strconv.ErrSyntax
	}
	return parsed, nil
}

func containsAllStableIDs(present map[string]struct{}, expected []string) bool {
	for _, id := range expected {
		if _, ok := present[id]; !ok {
			return false
		}
	}
	return true
}
