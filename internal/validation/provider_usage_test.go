package validation

import (
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestJobFinishedEventValidatesProviderUsage(t *testing.T) {
	t.Parallel()
	over := v1.ProviderUsageMaxCount + 1
	valid := validJobFinishedEvent()
	valid.ProviderUsage = &v1.ProviderUsage{Provider: "opencode", Model: "moonshot/kimi-k2", InputTokens: 1, OutputTokens: 2}
	if err := JobFinishedEvent(valid); err != nil {
		t.Fatalf("valid usage rejected: %v", err)
	}
	for name, usage := range map[string]v1.ProviderUsage{
		"unknown provider": {Provider: "other", InputTokens: 1, OutputTokens: 1},
		"model text":       {Provider: "claude", Model: "two words", InputTokens: 1, OutputTokens: 1},
		"input overflow":   {Provider: "claude", InputTokens: over, OutputTokens: 1},
		"cache overflow":   {Provider: "codex", InputTokens: 1, OutputTokens: 1, CacheReadInputTokens: &over},
	} {
		event := validJobFinishedEvent()
		usageCopy := usage
		event.ProviderUsage = &usageCopy
		if err := JobFinishedEvent(event); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
