package capability

import "github.com/example/app-runner/internal/apikey"

const (
	AuthModeSubscription = "subscription"
	AuthModeAPIKey       = "api_key"
)

func (d *Detector) WithProviderAPIKeys(sources map[string]apikey.Source) *Detector {
	configured := *d
	configured.providerAPIKeys = make(map[string]apikey.Source, len(sources))
	for provider, source := range sources {
		if apikey.EnvironmentName(provider) != "" {
			configured.providerAPIKeys[provider] = source
		}
	}
	return &configured
}

func (d *Detector) authMode(provider string) string {
	if _, ok := d.providerAPIKeys[provider]; ok {
		return AuthModeAPIKey
	}
	return AuthModeSubscription
}

func apiKeyResolvable(source apikey.Source) bool {
	_, err := apikey.Resolve(source)
	return err == nil
}
