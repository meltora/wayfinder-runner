package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/example/app-runner/internal/apikey"
)

func normalizeProviderAPIKeys(in map[string]apikey.Source) map[string]apikey.Source {
	if in == nil {
		return nil
	}
	home, _ := os.UserHomeDir()
	out := make(map[string]apikey.Source, len(in))
	for provider, source := range in {
		if home != "" && strings.HasPrefix(source.File, "~/") {
			source.File = filepath.Join(home, source.File[2:])
		}
		out[provider] = source
	}
	return out
}

func validateProviderAPIKeys(c Config) error {
	for provider, source := range c.ProviderAPIKeys {
		if !supportedProvider(provider) {
			return fmt.Errorf("provider_api_keys provider %q is unsupported; use claude or codex", provider)
		}
		target := apikey.EnvironmentName(provider)
		if target == "" {
			return fmt.Errorf("provider_api_keys.%s is not supported; API-key mode is available for claude and codex", provider)
		}
		if (source.Env == "") == (source.File == "") {
			return fmt.Errorf("provider_api_keys.%s must set exactly one of env or file", provider)
		}
		if source.Env != "" && !providerEnvironmentNamePattern.MatchString(source.Env) {
			return fmt.Errorf("provider_api_keys.%s.env must match ^[A-Z][A-Z0-9_]{0,63}$", provider)
		}
		if source.File != "" && (!filepath.IsAbs(source.File) || filepath.Clean(source.File) != source.File) {
			return fmt.Errorf("provider_api_keys.%s.file must be a clean absolute path", provider)
		}
		if _, inline := c.ProviderEnvironment[provider][target]; inline {
			return fmt.Errorf("provider_environment.%s must not set %s when provider_api_keys.%s is configured", provider, target, provider)
		}
		if c.Supervisor == "tmux" || c.Supervisor == "herdr" {
			return fmt.Errorf("provider_api_keys requires the direct supervisor so the key never appears in a command line or script")
		}
	}
	return nil
}

func (c Config) ProviderAPIKeySource(provider string) (apikey.Source, bool) {
	source, ok := c.ProviderAPIKeys[provider]
	return source, ok
}
