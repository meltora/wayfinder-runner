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

func (publisher *bitbucketPublisher) reconcileReviewerAssignment(ctx context.Context, operation v1.ProviderReviewerAssignmentReconciliationOperation) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
	workspace, repository, valid := bitbucketRepository(operation.ProviderRepositoryPath)
	if !valid || !bitbucketUUIDPattern.MatchString(operation.ProviderRepositoryID) {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationRepositoryMismatch)
	}
	repositoryPath := "/repositories/" + url.PathEscape(workspace) + "/" + url.PathEscape(repository)
	response, err := publisher.request(ctx, http.MethodGet, repositoryPath, nil, nil)
	if err != nil {
		return reconciliationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyBitbucketReconciliationFailure(response, v1.ProviderReviewerAssignmentReconciliationRepositoryMismatch)
	}
	var repo struct {
		UUID     string `json:"uuid"`
		FullName string `json:"full_name"`
	}
	if json.Unmarshal(response.body, &repo) != nil || repo.UUID != operation.ProviderRepositoryID || repo.FullName != operation.ProviderRepositoryPath {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationRepositoryMismatch)
	}
	id, err := canonicalPositiveInt64(operation.ExternalRequestID)
	if err != nil {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationRequestNotFound)
	}
	requestPath := repositoryPath + "/pullrequests/" + strconv.FormatInt(id, 10)
	response, err = publisher.request(ctx, http.MethodGet, requestPath, nil, nil)
	if err != nil {
		return reconciliationRequestFailure(err)
	}
	if response.status < 200 || response.status >= 300 {
		return classifyBitbucketReconciliationFailure(response, v1.ProviderReviewerAssignmentReconciliationRequestNotFound)
	}
	var request struct {
		ID           int64                        `json:"id"`
		State        string                       `json:"state"`
		Source       bitbucketPullRequestEndpoint `json:"source"`
		Destination  bitbucketPullRequestEndpoint `json:"destination"`
		Reviewers    *[]bitbucketReviewerAccount  `json:"reviewers"`
		Participants *[]struct {
			bitbucketReviewerAccount
			Approved bool   `json:"approved"`
			State    string `json:"state"`
		} `json:"participants"`
	}
	if json.Unmarshal(response.body, &request) != nil || request.ID != id || request.Reviewers == nil || request.Participants == nil || len(*request.Reviewers) > 100 || len(*request.Participants) > 100 || request.Source.Commit.Hash == "" {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
	}
	if request.Source.Repository.FullName != operation.ProviderRepositoryPath || request.Destination.Repository.FullName != operation.ProviderRepositoryPath {
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationRepositoryMismatch)
	}
	state := v1.ProviderReviewerAssignmentReconciliationRequestState("")
	switch request.State {
	case "OPEN":
		state = v1.ProviderReviewerAssignmentReconciliationRequestOpen
	case "DECLINED", "SUPERSEDED":
		state = v1.ProviderReviewerAssignmentReconciliationRequestClosed
	case "MERGED":
		state = v1.ProviderReviewerAssignmentReconciliationRequestMerged
	default:
		return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
	}
	presence := make(map[string]v1.ProviderReviewerAssignmentReconciliationPresence)
	seen := make(map[string]struct{}, len(*request.Reviewers))
	seenAccountIDs := make(map[string]string, len(*request.Reviewers)+len(*request.Participants))
	seenUUIDs := make(map[string]string, len(*request.Reviewers)+len(*request.Participants))
	for _, account := range *request.Reviewers {
		if !validBitbucketObservedAccount(account) || bitbucketObservedAccountConflicts(account, seenAccountIDs, seenUUIDs) {
			return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
		}
		if _, dup := seen[account.UUID]; dup {
			return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
		}
		seen[account.UUID] = struct{}{}
		presence[account.AccountID] = v1.ProviderReviewerAssignmentReconciliationRequested
		presence[account.UUID] = v1.ProviderReviewerAssignmentReconciliationRequested
	}
	participantSeen := make(map[string]struct{}, len(*request.Participants))
	for _, participant := range *request.Participants {
		account := participant.bitbucketReviewerAccount
		if !validBitbucketObservedAccount(account) || bitbucketObservedAccountConflicts(account, seenAccountIDs, seenUUIDs) {
			return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
		}
		if _, dup := participantSeen[account.UUID]; dup {
			return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
		}
		participantSeen[account.UUID] = struct{}{}
		reviewed := participant.Approved || participant.State == "approved" || participant.State == "changes_requested"
		if participant.State != "" && participant.State != "approved" && participant.State != "changes_requested" && participant.State != "participating" {
			return reconciliationPermanent(v1.ProviderReviewerAssignmentReconciliationInvalidProviderResponse)
		}
		if reviewed {
			upgradePresence(presence, account.AccountID, v1.ProviderReviewerAssignmentReconciliationReviewed)
			upgradePresence(presence, account.UUID, v1.ProviderReviewerAssignmentReconciliationReviewed)
		}
	}
	return reconciliationSucceeded(operation, state, request.Source.Commit.Hash, presence)
}

func validBitbucketObservedAccount(account bitbucketReviewerAccount) bool {
	return account.AccountID != "" && bitbucketUUIDPattern.MatchString(account.UUID)
}

func bitbucketObservedAccountConflicts(account bitbucketReviewerAccount, accounts map[string]string, uuids map[string]string) bool {
	if uuid, exists := accounts[account.AccountID]; exists && uuid != account.UUID {
		return true
	}
	if accountID, exists := uuids[account.UUID]; exists && accountID != account.AccountID {
		return true
	}
	accounts[account.AccountID] = account.UUID
	uuids[account.UUID] = account.AccountID
	return false
}

func classifyBitbucketReconciliationFailure(response bitbucketResponse, notFound v1.ProviderReviewerAssignmentReconciliationErrorCode) v1.ProviderReviewerAssignmentReconciliationResultOutcome {
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
