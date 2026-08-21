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

type bitbucketAssignmentSnapshot struct {
	present       map[string]struct{}
	reviewerUUIDs map[string]struct{}
}

func (publisher *bitbucketPublisher) assignReviewers(
	ctx context.Context,
	operation v1.ProviderReviewerAssignmentOperation,
) v1.ProviderReviewerAssignmentResultOutcome {
	repositoryPath, failure := publisher.assignmentRepository(ctx, operation)
	if failure != nil {
		return *failure
	}
	requestID, err := canonicalPositiveInt64(operation.ExternalRequestID)
	if err != nil {
		return assignmentPermanent(v1.ProviderReviewerAssignmentRequestNotFound)
	}
	requestPath := repositoryPath + "/pullrequests/" + strconv.FormatInt(requestID, 10)
	if _, failure = publisher.assignmentSnapshot(ctx, requestPath, operation); failure != nil {
		return *failure
	}
	desired, accountIDs, failure := publisher.revalidateAssignmentRecipients(ctx, operation)
	if failure != nil {
		return *failure
	}
	current, failure := publisher.assignmentSnapshot(ctx, requestPath, operation)
	if failure != nil {
		return *failure
	}
	if containsAllStableIDs(current.present, accountIDs) {
		return assignmentSucceeded(operation, accountIDs)
	}
	union := make(map[string]struct{}, len(current.reviewerUUIDs)+len(desired))
	for reviewerUUID := range current.reviewerUUIDs {
		union[reviewerUUID] = struct{}{}
	}
	for _, reviewer := range desired {
		union[reviewer.uuid] = struct{}{}
	}
	uuids := make([]string, 0, len(union))
	for reviewerUUID := range union {
		uuids = append(uuids, reviewerUUID)
	}
	sort.Strings(uuids)
	reviewers := make([]map[string]string, 0, len(uuids))
	for _, reviewerUUID := range uuids {
		reviewers = append(reviewers, map[string]string{"uuid": reviewerUUID})
	}
	body, _ := json.Marshal(map[string]any{"reviewers": reviewers})
	_, _ = publisher.request(ctx, http.MethodPut, requestPath, nil, body)
	after, reconcileFailure := publisher.assignmentSnapshot(ctx, requestPath, operation)
	if reconcileFailure == nil && containsAllStableIDs(after.present, accountIDs) &&
		containsAllStableIDs(after.reviewerUUIDs, uuids) {
		return assignmentSucceeded(operation, accountIDs)
	}
	return assignmentUncertain()
}

func (publisher *bitbucketPublisher) assignmentRepository(
	ctx context.Context,
	operation v1.ProviderReviewerAssignmentOperation,
) (string, *v1.ProviderReviewerAssignmentResultOutcome) {
	workspace, repository, valid := bitbucketRepository(operation.ProviderRepositoryPath)
	if !valid || !bitbucketUUIDPattern.MatchString(operation.ProviderRepositoryID) {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentRepositoryMismatch)
		return "", &outcome
	}
	repositoryPath := "/repositories/" + url.PathEscape(workspace) + "/" + url.PathEscape(repository)
	response, err := publisher.request(ctx, http.MethodGet, repositoryPath, nil, nil)
	if err != nil {
		outcome := assignmentRequestFailure(err)
		return "", &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := classifyBitbucketAssignmentFailure(response, v1.ProviderReviewerAssignmentRepositoryMismatch)
		return "", &outcome
	}
	var providerRepository struct {
		UUID     string `json:"uuid"`
		FullName string `json:"full_name"`
	}
	if json.Unmarshal(response.body, &providerRepository) != nil ||
		providerRepository.UUID != operation.ProviderRepositoryID ||
		providerRepository.FullName != operation.ProviderRepositoryPath {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentRepositoryMismatch)
		return "", &outcome
	}
	return repositoryPath, nil
}

func (publisher *bitbucketPublisher) assignmentSnapshot(
	ctx context.Context,
	requestPath string,
	operation v1.ProviderReviewerAssignmentOperation,
) (bitbucketAssignmentSnapshot, *v1.ProviderReviewerAssignmentResultOutcome) {
	response, err := publisher.request(ctx, http.MethodGet, requestPath, nil, nil)
	if err != nil {
		outcome := assignmentRequestFailure(err)
		return bitbucketAssignmentSnapshot{}, &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := classifyBitbucketAssignmentFailure(response, v1.ProviderReviewerAssignmentRequestNotFound)
		return bitbucketAssignmentSnapshot{}, &outcome
	}
	var request struct {
		ID          int64                        `json:"id"`
		State       string                       `json:"state"`
		Source      bitbucketPullRequestEndpoint `json:"source"`
		Destination bitbucketPullRequestEndpoint `json:"destination"`
		Reviewers   *[]bitbucketReviewerAccount  `json:"reviewers"`
	}
	expectedID, _ := canonicalPositiveInt64(operation.ExternalRequestID)
	if json.Unmarshal(response.body, &request) != nil || request.ID != expectedID || request.Reviewers == nil ||
		len(*request.Reviewers) > maxAssignmentRecipients {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentInvalidProviderResponse)
		return bitbucketAssignmentSnapshot{}, &outcome
	}
	if request.State != "OPEN" {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentRequestNotOpen)
		return bitbucketAssignmentSnapshot{}, &outcome
	}
	if request.Source.Repository.FullName != operation.ProviderRepositoryPath ||
		request.Destination.Repository.FullName != operation.ProviderRepositoryPath {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentRepositoryMismatch)
		return bitbucketAssignmentSnapshot{}, &outcome
	}
	if request.Source.Commit.Hash != operation.HeadSHA {
		outcome := assignmentPermanent(v1.ProviderReviewerAssignmentHeadMismatch)
		return bitbucketAssignmentSnapshot{}, &outcome
	}
	present := make(map[string]struct{}, len(*request.Reviewers)*2)
	uuids := make(map[string]struct{}, len(*request.Reviewers))
	for _, reviewer := range *request.Reviewers {
		if reviewer.AccountID == "" || !bitbucketUUIDPattern.MatchString(reviewer.UUID) || reviewer.Nickname == "" {
			outcome := assignmentPermanent(v1.ProviderReviewerAssignmentInvalidProviderResponse)
			return bitbucketAssignmentSnapshot{}, &outcome
		}
		if _, duplicate := uuids[reviewer.UUID]; duplicate {
			outcome := assignmentPermanent(v1.ProviderReviewerAssignmentInvalidProviderResponse)
			return bitbucketAssignmentSnapshot{}, &outcome
		}
		present[reviewer.AccountID] = struct{}{}
		present[reviewer.UUID] = struct{}{}
		uuids[reviewer.UUID] = struct{}{}
	}
	return bitbucketAssignmentSnapshot{present: present, reviewerUUIDs: uuids}, nil
}

type bitbucketDesiredReviewer struct {
	uuid string
}

func (publisher *bitbucketPublisher) revalidateAssignmentRecipients(
	ctx context.Context,
	operation v1.ProviderReviewerAssignmentOperation,
) ([]bitbucketDesiredReviewer, []string, *v1.ProviderReviewerAssignmentResultOutcome) {
	desired := make([]bitbucketDesiredReviewer, 0, len(operation.Recipients))
	accountIDs := make([]string, 0, len(operation.Recipients))
	for _, recipient := range operation.Recipients {
		verification := publisher.verifyReviewerIdentity(ctx, assignmentIdentityVerification(operation, recipient))
		if failure := assignmentIdentityFailure(verification, recipient); failure != nil {
			return nil, nil, failure
		}
		if !bitbucketUUIDPattern.MatchString(verification.AssignmentPrincipal) {
			outcome := assignmentPermanent(v1.ProviderReviewerAssignmentReviewerIdentityChanged)
			return nil, nil, &outcome
		}
		desired = append(desired, bitbucketDesiredReviewer{uuid: verification.AssignmentPrincipal})
		accountIDs = append(accountIDs, recipient.ProviderAccountID)
	}
	sort.Slice(desired, func(left, right int) bool { return desired[left].uuid < desired[right].uuid })
	sort.Strings(accountIDs)
	return desired, accountIDs, nil
}

func classifyBitbucketAssignmentFailure(
	response bitbucketResponse,
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
