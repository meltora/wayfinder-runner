package sourcepublication

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

const publicationRequestTimeout = 30 * time.Second

type Registry struct {
	installations map[string]config.SourcePublicationInstallationConfig
	httpClient    *http.Client
}

func NewRegistry(
	installations map[string]config.SourcePublicationInstallationConfig,
	baseClient *http.Client,
) (*Registry, error) {
	copyInstallations := make(map[string]config.SourcePublicationInstallationConfig, len(installations))
	for id, installation := range installations {
		copyValue := installation
		switch copyValue.Provider {
		case "github", "gitlab", "bitbucket":
		default:
			return nil, fmt.Errorf("source publication installation %s uses an unsupported provider", id)
		}
		if copyValue.APIBaseURL == "" {
			switch copyValue.Provider {
			case "github":
				copyValue.APIBaseURL = "https://api.github.com"
			case "gitlab":
				copyValue.APIBaseURL = "https://gitlab.com/api/v4"
			case "bitbucket":
				copyValue.APIBaseURL = "https://api.bitbucket.org/2.0"
			}
		}
		parsedBaseURL, err := url.Parse(copyValue.APIBaseURL)
		if err != nil || parsedBaseURL.Scheme != "https" || parsedBaseURL.Host == "" || parsedBaseURL.User != nil ||
			parsedBaseURL.RawQuery != "" || parsedBaseURL.Fragment != "" {
			if err == nil {
				err = fmt.Errorf("must be an HTTPS URL without credentials, query, or fragment")
			}
			return nil, err
		}
		if !credentialOnlyInstallation(copyValue) {
			if _, err := readToken(copyValue.TokenFile); err != nil {
				return nil, err
			}
		}
		copyInstallations[id] = copyValue
	}
	client := &http.Client{}
	if baseClient != nil {
		copyClient := *baseClient
		client = &copyClient
	}
	client.Timeout = publicationRequestTimeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Registry{installations: copyInstallations, httpClient: client}, nil
}

func (registry *Registry) ReadyProviders() []string {
	providers := map[string]struct{}{}
	for _, installation := range registry.installations {
		if credentialOnlyInstallation(installation) {
			providers[installation.Provider] = struct{}{}
			continue
		}
		if _, err := readToken(installation.TokenFile); err == nil {
			providers[installation.Provider] = struct{}{}
		}
	}
	result := make([]string, 0, len(providers))
	for provider := range providers {
		result = append(result, provider)
	}
	sort.Strings(result)
	return result
}

func (registry *Registry) ReadyReviewerIdentityInstallations() []v1.ReviewerIdentityVerificationInstallationCapability {
	result := make([]v1.ReviewerIdentityVerificationInstallationCapability, 0, len(registry.installations))
	for installationID, installation := range registry.installations {
		if !reviewInstallationReady(installation) {
			continue
		}
		result = append(result, v1.ReviewerIdentityVerificationInstallationCapability{
			InstallationID:      installationID,
			Provider:            installation.Provider,
			ProviderInstanceKey: configuredProviderInstanceKey(installation.Provider, installation.APIBaseURL),
		})
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].InstallationID < result[right].InstallationID
	})
	return result
}

func (registry *Registry) ReadyProviderReviewerAssignmentInstallations() []v1.ProviderReviewerAssignmentInstallationCapability {
	result := make([]v1.ProviderReviewerAssignmentInstallationCapability, 0, len(registry.installations))
	for installationID, installation := range registry.installations {
		if !reviewInstallationReady(installation) {
			continue
		}
		result = append(result, v1.ProviderReviewerAssignmentInstallationCapability{
			InstallationID: installationID, Provider: installation.Provider,
			ProviderInstanceKey: configuredProviderInstanceKey(installation.Provider, installation.APIBaseURL),
		})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].InstallationID < result[right].InstallationID })
	return result
}

func (registry *Registry) ReadyProviderReviewerAssignmentReconciliationInstallations() []v1.ProviderReviewerAssignmentReconciliationInstallationCapability {
	result := make([]v1.ProviderReviewerAssignmentReconciliationInstallationCapability, 0, len(registry.installations))
	for installationID, installation := range registry.installations {
		if !reviewInstallationReady(installation) {
			continue
		}
		result = append(result, v1.ProviderReviewerAssignmentReconciliationInstallationCapability{InstallationID: installationID, Provider: installation.Provider, ProviderInstanceKey: configuredProviderInstanceKey(installation.Provider, installation.APIBaseURL)})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].InstallationID < result[right].InstallationID })
	return result
}

func (registry *Registry) ReadyProviderReviewObservationInstallations() []v1.ProviderReviewObservationInstallationCapability {
	result := make([]v1.ProviderReviewObservationInstallationCapability, 0, len(registry.installations))
	for id, installation := range registry.installations {
		if !reviewInstallationReady(installation) {
			continue
		}
		result = append(result, v1.ProviderReviewObservationInstallationCapability{InstallationID: id, Provider: installation.Provider, ProviderInstanceKey: configuredProviderInstanceKey(installation.Provider, installation.APIBaseURL)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].InstallationID < result[j].InstallationID })
	return result
}

func (registry *Registry) ReadyProviderReviewFeedbackContentInstallations() []v1.ProviderReviewFeedbackContentInstallationCapability {
	result := make([]v1.ProviderReviewFeedbackContentInstallationCapability, 0, len(registry.installations))
	for id, installation := range registry.installations {
		if installation.Provider != "github" {
			continue
		}
		if !reviewInstallationReady(installation) {
			continue
		}
		result = append(result, v1.ProviderReviewFeedbackContentInstallationCapability{InstallationID: id, Provider: installation.Provider, ProviderInstanceKey: configuredProviderInstanceKey(installation.Provider, installation.APIBaseURL)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].InstallationID < result[j].InstallationID })
	return result
}

func (registry *Registry) Publish(
	ctx context.Context,
	operation v1.SourcePublicationOperation,
	credential *v1.ProviderCredential,
) v1.SourcePublicationResultOutcome {
	installation, ok := registry.installations[operation.Publication.InstallationID]
	if !ok {
		return permanent(v1.SourceControlInstallationRevoked)
	}
	if installation.Provider != operation.Publication.Provider {
		return permanent(v1.SourceControlPermissionDenied)
	}
	if installation.Provider == "github" && (credential != nil || credentialOnlyInstallation(installation)) {
		if !credential.UsableAt(time.Now()) {
			return retryable(v1.SourceControlProviderUnavailable, nil)
		}
		publisher := newGitHubPublisher(installation.APIBaseURL, credential.Token, registry.httpClient)
		publisher.repositoryIDs = append([]string(nil), credential.RepositoryIDs...)
		return publisher.publish(ctx, operation.Publication)
	}
	token, err := readToken(installation.TokenFile)
	if err != nil {
		return permanent(v1.SourceControlAuthenticationFailed)
	}
	switch installation.Provider {
	case "github":
		return newGitHubPublisher(installation.APIBaseURL, token, registry.httpClient).publish(ctx, operation.Publication)
	case "gitlab":
		return newGitLabPublisher(installation.APIBaseURL, token, registry.httpClient).publish(ctx, operation.Publication)
	case "bitbucket":
		return newBitbucketPublisher(installation.APIBaseURL, token, registry.httpClient).publish(ctx, operation.Publication)
	default:
		return permanent(v1.SourceControlInstallationRevoked)
	}
}

func credentialOnlyInstallation(installation config.SourcePublicationInstallationConfig) bool {
	return installation.Provider == "github" && installation.TokenFile == ""
}
