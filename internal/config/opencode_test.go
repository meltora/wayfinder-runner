package config

import (
	"strings"
	"testing"
)

func TestOpenCodeProviderAllowsOptionalCustomerModel(t *testing.T) {
	cfg := productionConfig()
	cfg.Provider = "opencode"
	cfg.ProviderBinaries = map[string]string{"opencode": "/usr/local/bin/opencode"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("OpenCode without an explicit model rejected: %v", err)
	}
	cfg.AgentModel = "customer-provider/customer-model"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("provider-neutral model rejected: %v", err)
	}
	for _, model := range []string{
		"customer-provider", "/model", "--help", "provider/model with spaces",
		strings.Repeat("p", 129) + "/model",
	} {
		cfg.AgentModel = model
		if err := cfg.Validate(); err == nil {
			t.Fatalf("invalid agent model %q accepted", model)
		}
	}
}

func TestAgentModelCannotBeSilentlyIgnoredByOtherProviders(t *testing.T) {
	cfg := productionConfig()
	cfg.Provider = "codex"
	cfg.AgentModel = "customer-provider/customer-model"
	if err := cfg.Validate(); err == nil {
		t.Fatal("agent_model was accepted by a provider that ignores it")
	}
}
