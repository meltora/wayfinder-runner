package validation

import (
	"fmt"
	"regexp"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var providerUsageModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$`)

func ProviderUsage(usage v1.ProviderUsage) error {
	switch usage.Provider {
	case "claude", "codex", "opencode":
	default:
		return fmt.Errorf("provider_usage provider is invalid")
	}
	if usage.Model != "" && !providerUsageModelPattern.MatchString(usage.Model) {
		return fmt.Errorf("provider_usage model is invalid")
	}
	counts := []uint64{usage.InputTokens, usage.OutputTokens}
	if usage.CacheReadInputTokens != nil {
		counts = append(counts, *usage.CacheReadInputTokens)
	}
	if usage.CacheCreationInputTokens != nil {
		counts = append(counts, *usage.CacheCreationInputTokens)
	}
	for _, count := range counts {
		if count > v1.ProviderUsageMaxCount {
			return fmt.Errorf("provider_usage count is out of range")
		}
	}
	return nil
}
