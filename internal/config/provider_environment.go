package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var providerEnvironmentNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

var reservedProviderEnvironmentNames = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "TMPDIR": true,
	"GH_TOKEN": true, "GITHUB_TOKEN": true, "GITLAB_TOKEN": true, "BITBUCKET_TOKEN": true,
	"CLAUDE_CODE_SUBPROCESS_ENV_SCRUB": true,
	"OPENCODE_CONFIG_CONTENT":          true,
	"OPENCODE_CONFIG_DIR":              true,
}

var reservedProviderEnvironmentPrefixes = []string{"GIT_", "SSH_", "PLATFORM_", "OPENCODE_DISABLE_"}

func normalizeProviderEnvironment(in map[string]map[string]string) map[string]map[string]string {
	if in == nil {
		return nil
	}
	home, _ := os.UserHomeDir()
	out := make(map[string]map[string]string, len(in))
	for provider, variables := range in {
		if variables == nil {
			out[provider] = nil
			continue
		}
		copied := make(map[string]string, len(variables))
		for name, value := range variables {
			if home != "" && strings.HasPrefix(value, "~/") {
				value = filepath.Join(home, value[2:])
			}
			copied[name] = value
		}
		out[provider] = copied
	}
	return out
}

func validateProviderEnvironment(environment map[string]map[string]string) error {
	for provider, variables := range environment {
		if !supportedProvider(provider) {
			return fmt.Errorf("provider_environment provider %q is unsupported; use codex, claude, or opencode", provider)
		}
		for name, value := range variables {
			if !providerEnvironmentNamePattern.MatchString(name) {
				return fmt.Errorf("provider_environment.%s variable %q must match ^[A-Z][A-Z0-9_]{0,63}$", provider, name)
			}
			if reservedProviderEnvironmentName(name) {
				return fmt.Errorf("provider_environment.%s variable %s is reserved by the runner and cannot be overridden", provider, name)
			}
			if value == "" {
				return fmt.Errorf("provider_environment.%s.%s must not be empty", provider, name)
			}
			if strings.ContainsAny(value, "\x00\r\n") {
				return fmt.Errorf("provider_environment.%s.%s must not contain NUL or line breaks", provider, name)
			}
			if strings.HasPrefix(value, "~/") {
				return fmt.Errorf("provider_environment.%s.%s could not expand ~/ to the runner user's home", provider, name)
			}
		}
	}
	return nil
}

func reservedProviderEnvironmentName(name string) bool {
	if reservedProviderEnvironmentNames[name] {
		return true
	}
	for _, prefix := range reservedProviderEnvironmentPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func (c Config) AgentEnvironment(provider string) []string {
	names := c.ProviderEnvironmentNames(provider)
	if names == nil {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+c.ProviderEnvironment[provider][name])
	}
	return out
}

func (c Config) ProviderEnvironmentNames(provider string) []string {
	variables := c.ProviderEnvironment[provider]
	if len(variables) == 0 {
		return nil
	}
	names := make([]string, 0, len(variables))
	for name := range variables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
