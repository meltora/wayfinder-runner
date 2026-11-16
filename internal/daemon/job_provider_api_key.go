package daemon

import (
	"errors"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/apikey"
	"github.com/example/app-runner/internal/capability"
	"github.com/example/app-runner/internal/redact"
)

const providerAPIKeyUnavailableMessage = "provider API key is not available on the runner host"

type providerAPIKeyEnvironment struct {
	name  string
	value string
}

func (d *Daemon) providerAPIKey(adapter string) (*providerAPIKeyEnvironment, error) {
	d.hotMu.RLock()
	source, configured := d.cfg.ProviderAPIKeySource(adapter)
	d.hotMu.RUnlock()
	if !configured {
		return nil, nil
	}
	if d.supervisor == nil || d.supervisor.Name() != "direct" {
		return nil, errors.New("provider API-key mode requires the direct supervisor")
	}
	name := apikey.EnvironmentName(adapter)
	if name == "" {
		return nil, errors.New("provider API-key mode is not supported for this provider")
	}
	value, err := apikey.Resolve(source)
	if err != nil {
		return nil, err
	}
	redact.RememberSecret(value)
	return &providerAPIKeyEnvironment{name: name, value: value}, nil
}

func (d *Daemon) providerSubscriptionConfigured(adapter string) bool {
	if d.subscriptionConfigured != nil {
		return d.subscriptionConfigured(adapter)
	}
	d.hotMu.RLock()
	environment := d.cfg.ProviderEnvironment[adapter]
	d.hotMu.RUnlock()
	return capability.SubscriptionConfigured(adapter, environment)
}

func (d *Daemon) jobProviderAPIKey(adapter, authMode string) (*providerAPIKeyEnvironment, error) {
	if authMode == v1.ProviderAuthModeSubscription && d.providerSubscriptionConfigured(adapter) {
		return nil, nil
	}
	key, err := d.providerAPIKey(adapter)
	if err != nil {
		return nil, err
	}
	if key == nil && authMode == v1.ProviderAuthModeAPIKey {
		return nil, errors.New("project requires provider API-key mode but no provider API key is configured")
	}
	return key, nil
}

func (key *providerAPIKeyEnvironment) entries() []string {
	if key == nil {
		return nil
	}
	return []string{key.name + "=" + key.value}
}

func (key *providerAPIKeyEnvironment) names() []string {
	if key == nil {
		return nil
	}
	return []string{key.name}
}
