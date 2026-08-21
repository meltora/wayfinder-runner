package sourcepublication

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type gitLabReviewerAccount struct {
	ID       uint64 `json:"id"`
	Username string `json:"username"`
	State    string `json:"state"`
}

func (publisher *gitLabPublisher) verifyReviewerIdentity(
	ctx context.Context,
	operation v1.ReviewerIdentityVerificationOperation,
) v1.ReviewerIdentityVerificationResultOutcome {
	accountID, err := strconv.ParseUint(operation.ProviderAccountID, 10, 64)
	if err != nil || accountID == 0 || strconv.FormatUint(accountID, 10) != operation.ProviderAccountID {
		return reviewerIdentityPermanent(v1.ReviewerIdentityAccountNotFound)
	}
	response, err := publisher.request(ctx, http.MethodGet, "/users/"+operation.ProviderAccountID, nil, nil)
	if err != nil {
		return reviewerIdentityRequestError(err, v1.ReviewerIdentityAccountAmbiguous)
	}
	if response.status < 200 || response.status >= 300 {
		return reviewerIdentityHTTPFailure(response.status, response.header, v1.ReviewerIdentityAccountNotFound)
	}
	var account gitLabReviewerAccount
	if json.Unmarshal(response.body, &account) != nil || account.ID != accountID || account.Username == "" {
		return reviewerIdentityPermanent(v1.ReviewerIdentityAccountNotFound)
	}
	if account.State != "active" {
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
	return reviewerIdentitySucceeded(account.Username, operation.ProviderAccountID, evidence)
}

func (publisher *gitLabPublisher) verifyReviewerRepository(
	ctx context.Context,
	repository v1.ReviewerIdentityVerificationRepository,
	account gitLabReviewerAccount,
) (v1.ReviewerIdentityRepositoryEvidence, *v1.ReviewerIdentityVerificationResultOutcome) {
	repositoryID, err := strconv.ParseUint(repository.RepositoryExternalID, 10, 64)
	if err != nil || repositoryID == 0 || strconv.FormatUint(repositoryID, 10) != repository.RepositoryExternalID {
		outcome := reviewerIdentityPermanent(v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	projectPath := "/projects/" + repository.RepositoryExternalID
	response, err := publisher.request(ctx, http.MethodGet, projectPath, nil, nil)
	if err != nil {
		outcome := reviewerIdentityRequestError(err, v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := reviewerIdentityHTTPFailure(response.status, response.header, v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	var providerRepository struct {
		ID                uint64 `json:"id"`
		PathWithNamespace string `json:"path_with_namespace"`
	}
	if json.Unmarshal(response.body, &providerRepository) != nil || providerRepository.ID != repositoryID ||
		(repository.RepositoryPath != "" && repository.RepositoryPath != providerRepository.PathWithNamespace) {
		outcome := reviewerIdentityPermanent(v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}

	memberPath := projectPath + "/members/all/" + strconv.FormatUint(account.ID, 10)
	response, err = publisher.request(ctx, http.MethodGet, memberPath, nil, nil)
	if err != nil {
		outcome := reviewerIdentityRequestError(err, v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	if response.status == http.StatusNotFound {
		return gitLabRepositoryEvidence(repository, v1.ReviewerIdentityNotMember), nil
	}
	if response.status < 200 || response.status >= 300 {
		outcome := reviewerIdentityHTTPFailure(response.status, response.header, v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	var member struct {
		gitLabReviewerAccount
		AccessLevel int `json:"access_level"`
	}
	if json.Unmarshal(response.body, &member) != nil || member.ID != account.ID || member.Username != account.Username {
		outcome := reviewerIdentityPermanent(v1.ReviewerIdentityAccountAmbiguous)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	access := v1.ReviewerIdentityInsufficientPermission
	if member.State != "active" {
		access = v1.ReviewerIdentityNotMember
	} else if member.AccessLevel >= 30 {
		access = v1.ReviewerIdentityEligible
	}
	return gitLabRepositoryEvidence(repository, access), nil
}

func gitLabRepositoryEvidence(
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
