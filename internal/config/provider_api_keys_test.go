package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/apikey"
)

func TestLoadParsesProviderAPIKeySourcesAndExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("control_plane_url: wss://cp.example.com/stream\n" +
		"job_image: registry.example.com/runner/job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"provider: claude\n" +
		"provider_api_keys:\n" +
		"  claude:\n" +
		"    file: ~/.secrets/anthropic.key\n" +
		"  codex:\n" +
		"    env: RUNNER_OPENAI_KEY\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ProviderAPIKeys["claude"]; got.File != filepath.Join(home, ".secrets/anthropic.key") || got.Env != "" {
		t.Fatalf("claude source = %+v", got)
	}
	if got := cfg.ProviderAPIKeys["codex"]; got.Env != "RUNNER_OPENAI_KEY" {
		t.Fatalf("codex source = %+v", got)
	}
	source, ok := cfg.ProviderAPIKeySource("claude")
	if !ok || source.File == "" {
		t.Fatal("configured provider API key source not returned")
	}
	if _, ok := cfg.ProviderAPIKeySource("opencode"); ok {
		t.Fatal("unconfigured provider returned an API key source")
	}
}

func TestValidateRejectsUnsafeProviderAPIKeySources(t *testing.T) {
	for name, tc := range map[string]struct {
		keys        map[string]apikey.Source
		supervisor  string
		environment map[string]map[string]string
		want        string
	}{
		"opencode":         {keys: map[string]apikey.Source{"opencode": {Env: "OPENCODE_KEY"}}, want: "not supported"},
		"unknown provider": {keys: map[string]apikey.Source{"kimi": {Env: "KIMI_KEY"}}, want: "unsupported"},
		"no source":        {keys: map[string]apikey.Source{"claude": {}}, want: "exactly one"},
		"two sources":      {keys: map[string]apikey.Source{"claude": {Env: "A_KEY", File: "/k"}}, want: "exactly one"},
		"bad env name":     {keys: map[string]apikey.Source{"claude": {Env: "lower"}}, want: "must match"},
		"relative file":    {keys: map[string]apikey.Source{"claude": {File: "keys/a"}}, want: "absolute"},
		"unclean file":     {keys: map[string]apikey.Source{"claude": {File: "/keys/../a"}}, want: "absolute"},
		"tmux supervisor":  {keys: map[string]apikey.Source{"claude": {Env: "A_KEY"}}, supervisor: "tmux", want: "direct"},
		"herdr supervisor": {keys: map[string]apikey.Source{"codex": {Env: "A_KEY"}}, supervisor: "herdr", want: "direct"},
		"environment conflict": {
			keys:        map[string]apikey.Source{"claude": {Env: "A_KEY"}},
			environment: map[string]map[string]string{"claude": {"ANTHROPIC_API_KEY": "inline"}},
			want:        "ANTHROPIC_API_KEY",
		},
	} {
		cfg := Default()
		cfg.ControlPlaneURL = "wss://cp.example.com/stream"
		cfg.JobImage = "registry.example.com/runner/job@sha256:" + strings.Repeat("a", 64)
		cfg.StateDir = filepath.Join(t.TempDir(), "state")
		cfg.Provider = "claude"
		cfg.ProviderAPIKeys = tc.keys
		cfg.ProviderEnvironment = tc.environment
		if tc.supervisor != "" {
			cfg.Supervisor = tc.supervisor
		}
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err=%v, want %q", name, err, tc.want)
		}
	}
}

func TestNormalizeDeepCopiesProviderAPIKeys(t *testing.T) {
	in := Config{ProviderAPIKeys: map[string]apikey.Source{"claude": {Env: "A_KEY"}}}
	out := Normalize(in)
	out.ProviderAPIKeys["codex"] = apikey.Source{Env: "B_KEY"}
	if len(in.ProviderAPIKeys) != 1 {
		t.Fatal("Normalize mutated the caller's provider API keys")
	}
}
