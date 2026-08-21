package sourcepublication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type gitLabAssignmentSnapshot struct {
	reviewerIDs map[string]uint64
}

func (publisher *gitLabPublisher) assignReviewers(
	ctx context.Context,
	command v1.ProviderReviewerAssignmentOperation,
) v1.ProviderReviewerAssignmentResultOutcome {
	projectPath, failure := publisher.assignmentProject(ctx, command)
	if failure != nil {
		return *failure
	}
	requestID, err := canonicalPositiveInt64(command.ExternalRequestID)
	if err != nil {
		return assignmentPermanent(v1.ProviderReviewerAssignmentRequestNotFound)
	}
	requestPath := projectPath + "/merge_requests/" + strconv.FormatInt(requestID, 10)
	if _, failure = publisher.assignmentSnapshot(ctx, requestPath, command); failure != nil {
		return *failure
	}
	accountIDs, failure := publisher.revalidateAssignmentRecipients(ctx, command)
	if failure != nil {
		return *failure
	}
	current, failure := publisher.assignmentSnapshot(ctx, requestPath, command)
	if failure != nil {
		return *failure
	}
	if containsAllStableIDs(gitLabStableIDSet(current.reviewerIDs), accountIDs) {
		return assignmentSucceeded(command, accountIDs)
	}
	union := make(map[string]uint64, len(current.reviewerIDs)+len(accountIDs))
	for id, numeric := range current.reviewerIDs {
		union[id] = numeric
	}
	for _, id := range accountIDs {
		numeric, _ := strconv.ParseUint(id, 10, 64)
		union[id] = numeric
	}
	reviewerIDs := make([]uint64, 0, len(union))
	for _, numeric := range union {
		reviewerIDs = append(reviewerIDs, numeric)
	}
	sort.Slice(reviewerIDs, func(left, right int) bool { return reviewerIDs[left] < reviewerIDs[right] })
	body, _ := json.Marshal(map[string][]uint64{"reviewer_ids": reviewerIDs})
	_, _ = publisher.request(ctx, http.MethodPut, requestPath, nil, body)
	requiredIDs := make([]string, 0, len(union))
	for id := range union {
		requiredIDs = append(requiredIDs, id)
	}
	sort.Strings(requiredIDs)
	after, reconcileFailure := publisher.assignmentSnapshot(ctx, requestPath, command)
	if reconcileFailure == nil && containsAllStableIDs(gitLabStableIDSet(after.reviewerIDs), requiredIDs) {
		return assignmentSucceeded(command, accountIDs)
	}
	return assignmentUncertain()
}

func (publisher *gitLabPublisher) assignmentProject(
	ctx context.Context,
	command v1.ProviderReviewerAssignmentOperation,
) (string, *v1.ProviderReviewerAssignmentResultOutcome) {
	repositoryID, err := strconv.ParseUint(command.ProviderRepositoryID, 10, 64)
	if err != nil || repositoryID == 0 || strconv.FormatUint(repositoryID, 10) != command.ProviderRepositoryID {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentRequestNotFound)
		return "", &outcome
	}
	projectPath := "/projects/" + url.PathEscape(command.ProviderRepositoryID)
	response, err := publisher.request(ctx, http.MethodGet, projectPath, nil, nil)
	if err != nil {
		outcome := assignmentRequestFailure(err)
		return "", &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := classifyGitLabAssignmentFailure(response, v1.ProviderReviewerAssignmentRequestNotFound)
		return "", &outcome
	}
	var project struct {
		ID                uint64 `json:"id"`
		PathWithNamespace string `json:"path_with_namespace"`
	}
	if json.Unmarshal(response.body, &project) != nil || project.ID != repositoryID || project.PathWithNamespace == "" {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentRepositoryMismatch)
		return "", &outcome
	}
	return projectPath, nil
}

func (publisher *gitLabPublisher) assignmentSnapshot(
	ctx context.Context,
	requestPath string,
	command v1.ProviderReviewerAssignmentOperation,
) (gitLabAssignmentSnapshot, *v1.ProviderReviewerAssignmentResultOutcome) {
	response, err := publisher.request(ctx, http.MethodGet, requestPath, nil, nil)
	if err != nil {
		outcome := assignmentRequestFailure(err)
		return gitLabAssignmentSnapshot{}, &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := classifyGitLabAssignmentFailure(response, v1.ProviderReviewerAssignmentRequestNotFound)
		return gitLabAssignmentSnapshot{}, &outcome
	}
	var request struct {
		IID       int64  `json:"iid"`
		State     string `json:"state"`
		SHA       string `json:"sha"`
		Reviewers *[]struct {
			ID       uint64 `json:"id"`
			Username string `json:"username"`
		} `json:"reviewers"`
	}
	expectedIID, _ := canonicalPositiveInt64(command.ExternalRequestID)
	if json.Unmarshal(response.body, &request) != nil || request.IID != expectedIID ||
		request.SHA == "" || request.Reviewers == nil || len(*request.Reviewers) > maxAssignmentRecipients {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentInvalidProviderResponse)
		return gitLabAssignmentSnapshot{}, &outcome
	}
	if request.State != "opened" {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentRequestNotOpen)
		return gitLabAssignmentSnapshot{}, &outcome
	}
	if request.SHA != command.HeadSHA {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentHeadMismatch)
		return gitLabAssignmentSnapshot{}, &outcome
	}
	reviewers := make(map[string]uint64, len(*request.Reviewers))
	for _, reviewer := range *request.Reviewers {
		id := strconv.FormatUint(reviewer.ID, 10)
		if reviewer.ID == 0 || reviewer.Username == "" {
			outcome := assignmentPermanent(v1.ProviderReviewerAssignmentInvalidProviderResponse)
			return gitLabAssignmentSnapshot{}, &outcome
		}
		if _, duplicate := reviewers[id]; duplicate {
			outcome := assignmentPermanent(v1.ProviderReviewerAssignmentInvalidProviderResponse)
			return gitLabAssignmentSnapshot{}, &outcome
		}
		reviewers[id] = reviewer.ID
	}
	return gitLabAssignmentSnapshot{reviewerIDs: reviewers}, nil
}

func (publisher *gitLabPublisher) revalidateAssignmentRecipients(
	ctx context.Context,
	command v1.ProviderReviewerAssignmentOperation,
) ([]string, *v1.ProviderReviewerAssignmentResultOutcome) {
	accountIDs := make([]string, 0, len(command.Recipients))
	for _, recipient := range command.Recipients {
		verification := publisher.verifyReviewerIdentity(ctx, assignmentIdentityVerification(command, recipient))
		if failure := assignmentIdentityFailure(verification, recipient); failure != nil {
			return nil, failure
		}
		accountIDs = append(accountIDs, recipient.ProviderAccountID)
	}
	sort.Strings(accountIDs)
	return accountIDs, nil
}

func gitLabStableIDSet(values map[string]uint64) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for id := range values {
		result[id] = struct{}{}
	}
	return result
}

func classifyGitLabAssignmentFailure(
	response gitLabResponse,
	notFoundCode v1.ProviderReviewerAssignmentErrorCode,
) v1.ProviderReviewerAssignmentResultOutcome {
	if response.status == http.StatusRequestTimeout || response.status >= 500 && response.status <= 599 {
		return assignmentRetryable(v1.ProviderReviewerAssignmentProviderUnavailable, providerRetryAfter(response.header, time.Now()))
	}
	switch response.status {
	case http.StatusUnauthorized:
		return assignmentPermanent(v1.ProviderReviewerAssignmentInstallationInactive)
	case http.StatusForbidden:
		return assignmentPermanent(v1.ProviderReviewerAssignmentInstallationPermissionDenied)
	case http.StatusNotFound:
		return assignmentPermanent(notFoundCode)
	case http.StatusTooManyRequests:
		return assignmentRetryable(v1.ProviderReviewerAssignmentRateLimited, providerRetryAfter(response.header, time.Now()))
	default:
		return assignmentPermanent(v1.ProviderReviewerAssignmentInvalidProviderResponse)
	}
}
