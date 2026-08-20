package sourcepublication

import (
	"context"
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
		if copyValue.Provider == "github" && copyValue.APIBaseURL == "" {
			copyValue.APIBaseURL = "https://api.github.com"
		}
		if _, err := url.Parse(copyValue.APIBaseURL); err != nil {
			return nil, err
		}
		if _, err := readToken(copyValue.TokenFile); err != nil {
			return nil, err
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

func (registry *Registry) Publish(
	ctx context.Context,
	operation v1.SourcePublicationOperation,
) v1.SourcePublicationResultOutcome {
	installation, ok := registry.installations[operation.Publication.InstallationID]
	if !ok {
		return permanent(v1.SourceControlInstallationRevoked)
	}
	if installation.Provider != operation.Publication.Provider {
		return permanent(v1.SourceControlPermissionDenied)
	}
	token, err := readToken(installation.TokenFile)
	if err != nil {
		return permanent(v1.SourceControlAuthenticationFailed)
	}
	switch installation.Provider {
	case "github":
		return newGitHubPublisher(installation.APIBaseURL, token, registry.httpClient).publish(ctx, operation.Publication)
	default:
		return permanent(v1.SourceControlInstallationRevoked)
	}
}
