package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/apikey"
	"github.com/example/app-runner/internal/elevatedcapability"
	"github.com/example/app-runner/internal/redact"
)

func TestJobProviderAPIKeyHonoursTheProjectAuthMode(t *testing.T) {
	t.Cleanup(redact.ForgetSecretsForTest)
	t.Setenv("DAEMON_TEST_MODEL_KEY", daemonTestAPIKey)
	jr, _ := testJobRunner(t)
	jr.d.supervisor = namedTestSupervisor("direct")
	jr.d.subscriptionConfigured = func(string) bool { return false }

	for _, mode := range []string{"", v1.ProviderAuthModeSubscription} {
		if key, err := jr.d.jobProviderAPIKey("claude", mode); key != nil || err != nil {
			t.Fatalf("mode %q without a configured key: key=%v err=%v", mode, key, err)
		}
	}
	if key, err := jr.d.jobProviderAPIKey("claude", v1.ProviderAuthModeAPIKey); key != nil || err == nil {
		t.Fatalf("api_key mode without a configured key: key=%v err=%v", key, err)
	}

	jr.d.cfg.ProviderAPIKeys = map[string]apikey.Source{"claude": {Env: "DAEMON_TEST_MODEL_KEY"}}
	for _, mode := range []string{"", v1.ProviderAuthModeSubscription, v1.ProviderAuthModeAPIKey} {
		key, err := jr.d.jobProviderAPIKey("claude", mode)
		if err != nil || key == nil || !strings.HasPrefix(key.entries()[0], "ANTHROPIC_API_KEY=") {
			t.Fatalf("mode %q with a configured key: key=%v err=%v", mode, key, err)
		}
	}
}

func TestJobProviderAPIKeyPrefersAConfiguredSubscriptionInSubscriptionMode(t *testing.T) {
	t.Cleanup(redact.ForgetSecretsForTest)
	t.Setenv("DAEMON_TEST_MODEL_KEY", daemonTestAPIKey)
	jr, _ := testJobRunner(t)
	jr.d.supervisor = namedTestSupervisor("direct")
	jr.d.cfg.ProviderAPIKeys = map[string]apikey.Source{"claude": {Env: "DAEMON_TEST_MODEL_KEY"}}
	asked := []string{}
	jr.d.subscriptionConfigured = func(adapter string) bool {
		asked = append(asked, adapter)
		return true
	}

	if key, err := jr.d.jobProviderAPIKey("claude", v1.ProviderAuthModeSubscription); key != nil || err != nil {
		t.Fatalf("subscription mode with a configured subscription: key=%v err=%v", key, err)
	}
	if len(asked) != 1 || asked[0] != "claude" {
		t.Fatalf("subscription checks = %v", asked)
	}
	for _, mode := range []string{"", v1.ProviderAuthModeAPIKey} {
		key, err := jr.d.jobProviderAPIKey("claude", mode)
		if err != nil || key == nil || !strings.HasPrefix(key.entries()[0], "ANTHROPIC_API_KEY=") {
			t.Fatalf("mode %q must keep using the configured key: key=%v err=%v", mode, key, err)
		}
	}

	jr.d.supervisor = namedTestSupervisor("tmux")
	if key, err := jr.d.jobProviderAPIKey("claude", v1.ProviderAuthModeSubscription); key != nil || err != nil {
		t.Fatalf("a configured subscription must not need the direct supervisor: key=%v err=%v", key, err)
	}
}

func TestJobProviderAPIKeyFallsBackToTheKeyWhenNoSubscriptionIsConfigured(t *testing.T) {
	t.Cleanup(redact.ForgetSecretsForTest)
	t.Setenv("DAEMON_TEST_MODEL_KEY", daemonTestAPIKey)
	jr, _ := testJobRunner(t)
	jr.d.supervisor = namedTestSupervisor("direct")
	jr.d.cfg.ProviderAPIKeys = map[string]apikey.Source{"claude": {Env: "DAEMON_TEST_MODEL_KEY"}}
	jr.d.subscriptionConfigured = func(string) bool { return false }

	key, err := jr.d.jobProviderAPIKey("claude", v1.ProviderAuthModeSubscription)
	if err != nil || key == nil || !strings.HasPrefix(key.entries()[0], "ANTHROPIC_API_KEY=") {
		t.Fatalf("subscription mode without a subscription: key=%v err=%v", key, err)
	}
}

func TestProviderSubscriptionConfiguredReadsTheProviderEnvironment(t *testing.T) {
	jr, _ := testJobRunner(t)
	configDir := t.TempDir()
	jr.d.cfg.ProviderEnvironment = map[string]map[string]string{"claude": {"CLAUDE_CONFIG_DIR": configDir}}
	if jr.d.providerSubscriptionConfigured("claude") {
		t.Fatal("empty config directory reported a subscription")
	}
	if err := os.WriteFile(filepath.Join(configDir, ".credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !jr.d.providerSubscriptionConfigured("claude") {
		t.Fatal("subscription credentials were not detected")
	}
}

func TestJobRunnerResolvesAgentPackGrantsFromTheInstalledPack(t *testing.T) {
	jr, _ := elevatedTestRunner(t)
	packDir := filepath.Join(jr.workDir, "pack")
	if err := os.MkdirAll(packDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packDir, "pack.mcp.json"), []byte(elevatedTestDeclaration), 0o644); err != nil {
		t.Fatal(err)
	}
	packDigest := strings.Repeat("d", 64)
	jr.packDir = packDir
	jr.job.AgentPack = &v1.AgentPackRef{Version: "1.0.0", Digest: packDigest}
	jr.job.ElevatedCapabilities.Grants = []v1.ElevatedCapabilityGrant{{
		RequestID: "pack", Kind: v1.ElevatedCapabilityMCPServer, Key: "pack.mcp.json",
		DeclarationDigest: elevatedTestDigest(elevatedTestDeclaration), ExpiresAt: time.Now().UTC().Add(time.Hour),
		AgentPackDigest: packDigest,
	}}
	grants := jr.resolveElevatedCapabilities("claude", false, false)
	if reason := elevatedOutcomeReasons(jr)["pack"]; reason != elevatedcapability.ReasonApplied || len(grants.MCPServers) != 1 {
		t.Fatalf("reason = %s grants = %+v", reason, grants)
	}

	jr.job.AgentPack = &v1.AgentPackRef{Version: "1.0.1", Digest: strings.Repeat("e", 64)}
	grants = jr.resolveElevatedCapabilities("claude", false, false)
	if reason := elevatedOutcomeReasons(jr)["pack"]; reason != elevatedcapability.ReasonAgentPackNotInJob || len(grants.MCPServers) != 0 {
		t.Fatalf("reason = %s grants = %+v", reason, grants)
	}
}
