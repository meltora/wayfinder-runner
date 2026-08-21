package sourcepublication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type githubReviewerAccount struct {
	ID    uint64 `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

func (publisher *githubPublisher) verifyReviewerIdentity(
	ctx context.Context,
	operation v1.ReviewerIdentityVerificationOperation,
) v1.ReviewerIdentityVerificationResultOutcome {
	accountID, err := strconv.ParseUint(operation.ProviderAccountID, 10, 64)
	if err != nil || accountID == 0 || strconv.FormatUint(accountID, 10) != operation.ProviderAccountID {
		return reviewerIdentityPermanent(v1.ReviewerIdentityAccountNotFound)
	}
	response, err := publisher.request(ctx, http.MethodGet, "/user/"+operation.ProviderAccountID, nil, nil)
	if err != nil {
		return reviewerIdentityRequestError(err, v1.ReviewerIdentityAccountAmbiguous)
	}
	if response.status < 200 || response.status >= 300 {
		return reviewerIdentityHTTPFailure(response.status, response.header, v1.ReviewerIdentityAccountNotFound)
	}
	var account githubReviewerAccount
	if json.Unmarshal(response.body, &account) != nil || account.ID != accountID || account.Login == "" || account.Type != "User" {
		return reviewerIdentityPermanent(v1.ReviewerIdentityAccountNotFound)
	}

	evidence := make([]v1.ReviewerIdentityRepositoryEvidence, 0, len(operation.Repositories))
	for _, repository := range operation.Repositories {
		repositoryEvidence, outcome := publisher.verifyReviewerRepository(
			ctx, repository, account,
		)
		if outcome != nil {
			return *outcome
		}
		evidence = append(evidence, repositoryEvidence)
	}
	return reviewerIdentitySucceeded(account.Login, account.Login, evidence)
}

func (publisher *githubPublisher) verifyReviewerRepository(
	ctx context.Context,
	repository v1.ReviewerIdentityVerificationRepository,
	account githubReviewerAccount,
) (v1.ReviewerIdentityRepositoryEvidence, *v1.ReviewerIdentityVerificationResultOutcome) {
	repositoryID, err := strconv.ParseUint(repository.RepositoryExternalID, 10, 64)
	if err != nil || repositoryID == 0 || strconv.FormatUint(repositoryID, 10) != repository.RepositoryExternalID {
		outcome := reviewerIdentityPermanent(v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	response, err := publisher.request(ctx, http.MethodGet, "/repositories/"+repository.RepositoryExternalID, nil, nil)
	if err != nil {
		outcome := reviewerIdentityRequestError(err, v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	if response.status < 200 || response.status >= 300 {
		outcome := reviewerIdentityHTTPFailure(response.status, response.header, v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	var providerRepository struct {
		ID       uint64 `json:"id"`
		FullName string `json:"full_name"`
	}
	if json.Unmarshal(response.body, &providerRepository) != nil || providerRepository.ID != repositoryID {
		outcome := reviewerIdentityPermanent(v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	owner, name, validPath := githubRepository(providerRepository.FullName)
	if !validPath || repository.RepositoryPath != "" && providerRepository.FullName != repository.RepositoryPath {
		outcome := reviewerIdentityPermanent(v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}

	permissionPath := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) +
		"/collaborators/" + url.PathEscape(account.Login) + "/permission"
	response, err = publisher.request(ctx, http.MethodGet, permissionPath, nil, nil)
	if err != nil {
		outcome := reviewerIdentityRequestError(err, v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	access := v1.ReviewerIdentityNotMember
	if response.status == http.StatusNotFound {
		return githubRepositoryEvidence(repository, access), nil
	}
	if response.status < 200 || response.status >= 300 {
		outcome := reviewerIdentityHTTPFailure(response.status, response.header, v1.ReviewerIdentityRepositoryAccessDenied)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	var permission struct {
		Permission string                `json:"permission"`
		RoleName   string                `json:"role_name"`
		User       githubReviewerAccount `json:"user"`
	}
	if json.Unmarshal(response.body, &permission) != nil || permission.User.ID != account.ID ||
		permission.User.Login != account.Login {
		outcome := reviewerIdentityPermanent(v1.ReviewerIdentityAccountAmbiguous)
		return v1.ReviewerIdentityRepositoryEvidence{}, &outcome
	}
	switch permission.Permission {
	case "read", "triage", "write", "maintain", "admin":
		access = v1.ReviewerIdentityEligible
	case "none":
		access = v1.ReviewerIdentityNotMember
	default:
		access = v1.ReviewerIdentityInsufficientPermission
	}
	return githubRepositoryEvidence(repository, access), nil
}

func githubRepositoryEvidence(
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
