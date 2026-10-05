package capability

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func providerEnvironmentDetector(t *testing.T, accountDir string) *Detector {
	t.Helper()
	detector := fakeDetector().WithProviderEnvironment(map[string]map[string]string{
		"claude": {"CLAUDE_CONFIG_DIR": accountDir, "ANTHROPIC_LOG": "private-log-level"},
		"custom": {"IGNORED": "private-ignored"},
	})
	detector.LookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	return detector
}

func TestProviderVersionRunsWithConfiguredEnvironmentOnly(t *testing.T) {
	detector := providerEnvironmentDetector(t, "/private/accounts/xogito")
	var claudeEnvironment []string
	detector.RunProviderVersion = func(_ context.Context, env []string, name string, args ...string) (string, error) {
		if name != "claude" || !reflect.DeepEqual(args, []string{"--version"}) {
			t.Fatalf("unexpected environment version probe %s %v", name, args)
		}
		claudeEnvironment = append([]string(nil), env...)
		return "2.1.0 (Claude Code)", nil
	}
	manifest := detector.Collect(context.Background())
	want := []string{"ANTHROPIC_LOG=private-log-level", "CLAUDE_CONFIG_DIR=/private/accounts/xogito"}
	if !reflect.DeepEqual(claudeEnvironment, want) {
		t.Fatalf("version probe environment = %v", claudeEnvironment)
	}
	if got := providerCapability(t, manifest, "claude").Version; got != "2.1.0 (Claude Code)" {
		t.Fatalf("claude version = %q", got)
	}
	if got := providerCapability(t, manifest, "codex").Version; got != "codex version 1.0" {
		t.Fatalf("codex version = %q", got)
	}
}

func TestManifestNeverDisclosesProviderEnvironment(t *testing.T) {
	detector := providerEnvironmentDetector(t, "/private/accounts/xogito")
	detector.RunProviderVersion = func(context.Context, []string, string, ...string) (string, error) {
		return "claude using /private/accounts/xogito", nil
	}
	manifest := detector.Collect(context.Background())
	if providerCapability(t, manifest, "claude").Version != "" {
		t.Fatal("version echoing a configured value was reported")
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"/private/accounts/xogito", "private-log-level", "private-ignored", "CLAUDE_CONFIG_DIR", "ANTHROPIC_LOG"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("manifest disclosed %q: %s", private, raw)
		}
	}
}

func TestProviderEnvironmentConfigurationIsCopied(t *testing.T) {
	source := map[string]map[string]string{"claude": {"CLAUDE_CONFIG_DIR": "/a"}}
	original := fakeDetector()
	detector := original.WithProviderEnvironment(source)
	source["claude"]["CLAUDE_CONFIG_DIR"] = "/changed"
	if detector == original || original.providerEnvironment != nil {
		t.Fatal("configuration mutated original detector")
	}
	if got := detector.providerEnvironment["claude"]; !reflect.DeepEqual(got, []string{"CLAUDE_CONFIG_DIR=/a"}) {
		t.Fatalf("copied environment = %v", got)
	}
}

func TestAuthReadinessUsesConfiguredAccountDirectories(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	claudeDir := t.TempDir()
	codexDir := t.TempDir()
	detector := fakeDetector().WithProviderEnvironment(map[string]map[string]string{
		"claude": {"CLAUDE_CONFIG_DIR": claudeDir},
		"codex":  {"CODEX_HOME": codexDir},
	})
	if detector.authReady("claude") || detector.authReady("codex") {
		t.Fatal("empty configured account directories reported sign-in")
	}
	if err := os.WriteFile(filepath.Join(claudeDir, ".claude.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !detector.authReady("claude") || !detector.authReady("codex") {
		t.Fatal("configured account sign-in not detected")
	}
	if fakeDetector().authReady("claude") {
		t.Fatal("default account detection used configured directory")
	}
}

func TestShortNonPathEnvironmentValuesDoNotHideVersion(t *testing.T) {
	detector := fakeDetector().WithProviderEnvironment(map[string]map[string]string{
		"claude": {"ANTHROPIC_LOG": "debug", "CLAUDE_CONFIG_DIR": "/a"},
	})
	detector.LookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	detector.RunProviderVersion = func(context.Context, []string, string, ...string) (string, error) {
		return "2.1.0 (Claude Code) debug build", nil
	}
	if got := providerCapability(t, detector.Collect(context.Background()), "claude").Version; got != "2.1.0 (Claude Code) debug build" {
		t.Fatalf("version = %q", got)
	}
	detector.RunProviderVersion = func(context.Context, []string, string, ...string) (string, error) {
		return "2.1.0 config /a", nil
	}
	if got := providerCapability(t, detector.Collect(context.Background()), "claude").Version; got != "" {
		t.Fatalf("path-like value not redacted: %q", got)
	}
}
