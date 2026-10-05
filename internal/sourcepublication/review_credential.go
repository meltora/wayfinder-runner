package sourcepublication

import (
	"errors"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

type reviewAccess int

const (
	reviewAccessReady reviewAccess = iota
	reviewAccessCredentialUnavailable
	reviewAccessRepositoryDenied
	reviewAccessTokenFileInvalid
)

var errProviderCredentialExpiring = errors.New("provider credential is too close to expiry")

type reviewAuthority struct {
	token               string
	credentialExpiresAt time.Time
}

func reviewUsesCredential(
	installation config.SourcePublicationInstallationConfig,
	credential *v1.ProviderCredential,
) bool {
	return installation.Provider == "github" && (credential != nil || credentialOnlyInstallation(installation))
}

func resolveReviewAuthority(
	installation config.SourcePublicationInstallationConfig,
	credential *v1.ProviderCredential,
	providerRepositoryIDs ...string,
) (reviewAuthority, reviewAccess) {
	if !reviewUsesCredential(installation, credential) {
		token, err := readToken(installation.TokenFile)
		if err != nil {
			return reviewAuthority{}, reviewAccessTokenFileInvalid
		}
		return reviewAuthority{token: token}, reviewAccessReady
	}
	if !credential.UsableAt(time.Now()) {
		return reviewAuthority{}, reviewAccessCredentialUnavailable
	}
	for _, repositoryID := range providerRepositoryIDs {
		if !credentialCoversRepository(credential, repositoryID) {
			return reviewAuthority{}, reviewAccessRepositoryDenied
		}
	}
	return reviewAuthority{token: credential.Token, credentialExpiresAt: credential.ExpiresAt}, reviewAccessReady
}

func credentialCoversRepository(credential *v1.ProviderCredential, repositoryID string) bool {
	if repositoryID == "" {
		return false
	}
	for _, allowed := range credential.RepositoryIDs {
		if allowed == repositoryID {
			return true
		}
	}
	return false
}

func (registry *Registry) newGitHubReviewPublisher(
	installation config.SourcePublicationInstallationConfig,
	authority reviewAuthority,
) *githubPublisher {
	publisher := newGitHubPublisher(installation.APIBaseURL, authority.token, registry.httpClient)
	publisher.credentialExpiresAt = authority.credentialExpiresAt
	return publisher
}

func reviewInstallationReady(installation config.SourcePublicationInstallationConfig) bool {
	if credentialOnlyInstallation(installation) {
		return true
	}
	_, err := readToken(installation.TokenFile)
	return err == nil
}
