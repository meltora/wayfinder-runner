package capability

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/apikey"
)

const capabilityTestKey = "capability-test-key-0123456789abcdef"

func TestCollectReportsAPIKeyModeWithoutKeyMaterial(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "anthropic.key")
	if err := os.WriteFile(keyFile, []byte(capabilityTestKey), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPABILITY_TEST_CODEX_KEY", "")
	detector := fakeDetector().WithProviderAPIKeys(map[string]apikey.Source{
		"claude": {File: keyFile},
		"codex":  {Env: "CAPABILITY_TEST_CODEX_KEY"},
	})
	detector.LookPath = func(binary string) (string, error) { return "/usr/bin/" + binary, nil }
	detector.AuthReady = func(string) bool { return true }
	manifest := detector.Collect(context.Background())

	claude := providerCapability(t, manifest, "claude")
	if claude.AuthMode != "api_key" || !claude.AuthReady {
		t.Fatalf("claude capability = %+v", claude)
	}
	if canonical := providerCapability(t, manifest, "claude-code"); canonical.AuthMode != "api_key" || !canonical.AuthReady {
		t.Fatalf("claude-code capability = %+v", canonical)
	}
	codex := providerCapability(t, manifest, "codex")
	if codex.AuthMode != "api_key" || codex.AuthReady {
		t.Fatalf("unresolvable codex key reported ready: %+v", codex)
	}
	if opencode := providerCapability(t, manifest, "opencode"); opencode.AuthMode != "subscription" || !opencode.AuthReady {
		t.Fatalf("opencode capability = %+v", opencode)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{capabilityTestKey, keyFile, "CAPABILITY_TEST_CODEX_KEY"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("capability manifest leaked %q", private)
		}
	}
}

func TestCollectReportsPerAgentWhetherASubscriptionIsConfigured(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "anthropic.key")
	if err := os.WriteFile(keyFile, []byte(capabilityTestKey), 0o600); err != nil {
		t.Fatal(err)
	}
	detector := fakeDetector().WithProviderAPIKeys(map[string]apikey.Source{"claude": {File: keyFile}})
	detector.LookPath = func(binary string) (string, error) {
		if binary == "opencode" {
			return "", os.ErrNotExist
		}
		return "/usr/bin/" + binary, nil
	}
	detector.AuthReady = func(provider string) bool { return provider == "claude" }
	manifest := detector.Collect(context.Background())

	claude := providerCapability(t, manifest, "claude")
	if !claude.SubscriptionConfigured || claude.AuthMode != "api_key" || !claude.AuthReady {
		t.Fatalf("claude capability = %+v", claude)
	}
	if canonical := providerCapability(t, manifest, "claude-code"); !canonical.SubscriptionConfigured {
		t.Fatalf("claude-code capability = %+v", canonical)
	}
	if codex := providerCapability(t, manifest, "codex"); codex.SubscriptionConfigured || codex.AuthReady {
		t.Fatalf("codex capability = %+v", codex)
	}
	if opencode := providerCapability(t, manifest, "opencode"); opencode.SubscriptionConfigured {
		t.Fatalf("unavailable opencode reported a subscription: %+v", opencode)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"subscription_configured":false}`) || !strings.Contains(string(raw), `"subscription_configured":true}`) {
		t.Fatalf("subscription_configured must always be encoded: %s", raw)
	}
}

func TestSubscriptionConfiguredUsesTheProviderEnvironment(t *testing.T) {
	codexHome := t.TempDir()
	if SubscriptionConfigured("codex", map[string]string{"CODEX_HOME": codexHome}) {
		t.Fatal("empty CODEX_HOME reported a subscription")
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !SubscriptionConfigured("codex", map[string]string{"CODEX_HOME": codexHome}) {
		t.Fatal("codex subscription was not detected")
	}
}

func TestCollectReportsSubscriptionModeByDefault(t *testing.T) {
	detector := fakeDetector()
	detector.LookPath = func(binary string) (string, error) { return "/usr/bin/" + binary, nil }
	detector.AuthReady = func(string) bool { return false }
	for _, agent := range detector.Collect(context.Background()).Agents {
		if agent.AuthMode != "subscription" {
			t.Fatalf("%s auth mode = %q", agent.Provider, agent.AuthMode)
		}
	}
}
