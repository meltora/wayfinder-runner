package main

import (
	"testing"

	"github.com/example/app-runner/internal/apikey"
	"github.com/example/app-runner/internal/config"
)

func TestDoctorOptionsCarryTheClaudeLoginSource(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Provider = "claude"
	opts := loadedConfigurationDoctorOptions(cfg)
	if opts.ClaudeAPIKey || opts.ClaudeConfigDir != "" {
		t.Fatalf("options = %+v without a key or config dir", opts)
	}
	cfg.ProviderEnvironment = map[string]map[string]string{"claude": {"CLAUDE_CONFIG_DIR": "/srv/claude", "OTHER": "fake-value"}}
	cfg.ProviderAPIKeys = map[string]apikey.Source{"claude": {File: "/srv/key"}}
	opts = loadedConfigurationDoctorOptions(cfg)
	if !opts.ClaudeAPIKey || opts.ClaudeConfigDir != "/srv/claude" {
		t.Fatalf("ClaudeAPIKey = %v, ClaudeConfigDir = %q", opts.ClaudeAPIKey, opts.ClaudeConfigDir)
	}
}
