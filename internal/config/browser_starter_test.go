package config

import (
	"os"
	"testing"
)

// The app's focused interoperability suite supplies a private, browser-built fixture.
// Loading it checks the real strict decoder without starting a runner or probing a host.
func TestBrowserStarterConfiguration(t *testing.T) {
	path := os.Getenv("RUNNER_BROWSER_CONFIG_TEST_FILE")
	if path == "" {
		t.Skip("explicit browser-generated starter fixture not supplied")
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("browser-generated configuration did not load: %v", err)
	}
	if cfg.ControlPlaneURL != "wss://gateway.example.test/runner" || cfg.StateDir != "/var/lib/our-runner" || cfg.Provider != "codex" {
		t.Fatal("host or runtime configuration changed across the browser/Go boundary")
	}
	if cfg.Isolation != "docker" || cfg.JobNetwork != "none" || cfg.AllowUnsafeDirect || len(cfg.Checks) != 0 {
		t.Fatal("starter configuration widened execution authority")
	}
	if len(cfg.Repositories) != 2 || cfg.Repositories["repo-1"].URL != "https://github.example.test/team/repo.git" || cfg.Repositories["repo-2"].URL != "ssh://git@example.test/team/second.git" {
		t.Fatal("repository registrations changed across the browser/Go boundary")
	}
	if len(cfg.AgentPackPublicKeys) != 1 || cfg.AgentPackPublicKeys["key-1"] != "MCowBQYDK2VwAyEA11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=" {
		t.Fatal("explicit signed Pack public trust was not preserved")
	}
}
