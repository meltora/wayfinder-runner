package sourcepublication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var bitbucketUUIDPattern = regexp.MustCompile(`^\{[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\}$`)

type bitbucketReviewerAccount struct {
	AccountID     string `json:"account_id"`
	UUID          string `json:"uuid"`
	Nickname      string `json:"nickname"`
	AccountStatus string `json:"account_status"`
}

func (publisher *bitbucketPublisher) verifyReviewerIdentity(
	ctx context.Context,
	operation v1.ReviewerIdentityVerificationOperation,
) v1.ReviewerIdentityVerificationResultOutcome {
	response, err := publisher.request(
		ctx, http.MethodGet, "/users/"+url.PathEscape(operation.ProviderAccountID), nil, nil,
	)
	if err != nil {
		return reviewerIdentityRequestError(err, v1.ReviewerIdentityAccountAmbiguous)
	}
	if response.status < 200 || response.status >= 300 {
		return reviewerIdentityHTTPFailure(response.status, response.header, v1.ReviewerIdentityAccountNotFound)
	}
	var account bitbucketReviewerAccount
	if json.Unmarshal(response.body, &account) != nil || !bitbucketAccountMatches(operation.ProviderAccountID, account) ||
		!bitbucketUUIDPattern.MatchString(account.UUID) || account.Nickname == "" {
		return reviewerIdentityPermanent(v1.ReviewerIdentityAccountNotFound)
	}
	if account.AccountStatus != "active" {
		return reviewerIdentityPermanent(v1.ReviewerIdentityInstallationInactive)
	}

	evidence := make([]v1.ReviewerIdentityRepositoryEvidence, 0, len(operation.Repositories))
	for _, repository := range operation.Repositories {
		repositoryEvidence, outcome := publisher.verifyReviewerRepository(ctx, repository, account)
		if outcome != nil {
			return *outcome
		}
		evidence = append(evidence, repositoryEvidence)
	}
	return reviewerIdentitySucceeded(account.Nickname, account.UUID, evidence)
}

func bitbucketAccountMatches(providerAccountID string, account bitbucketReviewerAccount) bool {
	if bitbucketUUIDPattern.MatchString(providerAccountID) {
		return account.UUID == providerAccountID && account.AccountID != ""
	}
	return account.AccountID == providerAccountID
}

func (publisher *bitbucketPublisher) verifyReviewerRepository(
	ctx context.Context,
	repository v1.ReviewerIdentityVerificationRepository,
	account bitbucketReviewerAccount,
) (v1.ReviewerIdentityRepositoryEvidence, *v1.ReviewerIdentityVerificationResultOutcome) {
	workspace, name, validPath := bitbucketRepository(repository.RepositoryPath)
	if !validPath || !bitbucketUUIDPattern.MatchString(repository.RepositoryExternalID) {
		outcome := reviewerIdentityPermanent(v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	repositoryPath := "/repositories/" + url.PathEscape(workspace) + "/" + url.PathEscape(name)
	response, err := publisher.request(ctx, http.MethodGet, repositoryPath, nil, nil)
	if err != nil {
		outcome := reviewerIdentityRequestError(err, v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := reviewerIdentityHTTPFailure(response.status, response.header, v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	var providerRepository struct {
		UUID     string `json:"uuid"`
		FullName string `json:"full_name"`
	}
	if json.Unmarshal(response.body, &providerRepository) != nil || providerRepository.UUID != repository.RepositoryExternalID ||
		providerRepository.FullName != repository.RepositoryPath {
		outcome := reviewerIdentityPermanent(v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}

	permissionPath := "/workspaces/" + url.PathEscape(workspace) + "/permissions/repositories/" + url.PathEscape(name)
	query := url.Values{
		"q":       {"user.account_id = " + strconv.Quote(account.AccountID)},
		"pagelen": {"100"},
	}
	response, err = publisher.request(ctx, http.MethodGet, permissionPath, query, nil)
	if err != nil {
		outcome := reviewerIdentityRequestError(err, v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := reviewerIdentityHTTPFailure(response.status, response.header, v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	var page struct {
		Page    int    `json:"page"`
		PageLen int    `json:"pagelen"`
		Size    int    `json:"size"`
		Next    string `json:"next"`
		Values  *[]struct {
			Permission string `json:"permission"`
			User       struct {
				AccountID string `json:"account_id"`
				UUID      string `json:"uuid"`
			} `json:"user"`
			Repository struct {
				UUID     string `json:"uuid"`
				FullName string `json:"full_name"`
			} `json:"repository"`
		} `json:"values"`
	}
	if json.Unmarshal(response.body, &page) != nil || page.Values == nil || page.Page != 1 ||
		page.PageLen < 1 || page.PageLen > 100 || page.Size != len(*page.Values) || page.Next != "" ||
		len(*page.Values) > 1 || len(*page.Values) > page.PageLen {
		outcome := reviewerIdentityPermanent(v1.ReviewerIdentityAccountAmbiguous)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	matchingPermissions := make([]string, 0, 1)
	for _, permission := range *page.Values {
		if permission.User.AccountID != account.AccountID || permission.User.UUID != account.UUID ||
			permission.Repository.UUID != providerRepository.UUID || permission.Repository.FullName != providerRepository.FullName {
			outcome := reviewerIdentityPermanent(v1.ReviewerIdentityAccountAmbiguous)
			return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
		}
		matchingPermissions = append(matchingPermissions, permission.Permission)
	}
	if len(matchingPermissions) > 1 {
		outcome := reviewerIdentityPermanent(v1.ReviewerIdentityAccountAmbiguous)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	access := v1.ReviewerIdentityNotMember
	if len(matchingPermissions) == 1 {
		switch matchingPermissions[0] {
		case "read", "write", "admin":
			access = v1.ReviewerIdentityEligible
		default:
			access = v1.ReviewerIdentityInsufficientPermission
		}
	}
	return bitbucketRepositoryEvidence(repository, access), nil
}

func bitbucketRepositoryEvidence(
	repository v1.ReviewerIdentityVerificationRepository,
	access v1.ReviewerIdentityEffectiveAccess,
) v1.ReviewerIdentityRepositoryEvidence {
	return v1.ReviewerIdentityRepositoryEvidence{
		RepositoryID:            repository.RepositoryID,
		RepositoryExternalID:    repository.RepositoryExternalID,
		RepositoryPath:          repository.RepositoryPath,
		EffectiveReviewerAccess: access,
	}
}
