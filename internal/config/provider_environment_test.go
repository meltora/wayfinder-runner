package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadParsesProviderEnvironmentAndExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("control_plane_url: wss://cp.example.com/stream\n" +
		"job_image: registry.example.com/runner/job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"provider: claude\n" +
		"provider_environment:\n" +
		"  claude:\n" +
		"    CLAUDE_CONFIG_DIR: ~/.claude-xogito\n" +
		"    ANTHROPIC_LOG: debug\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude-xogito"), "ANTHROPIC_LOG": "debug"}
	if !reflect.DeepEqual(cfg.ProviderEnvironment["claude"], want) {
		t.Fatalf("provider environment = %v", cfg.ProviderEnvironment)
	}
	if got := cfg.AgentEnvironment("claude"); !reflect.DeepEqual(got, []string{"ANTHROPIC_LOG=debug", "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude-xogito")}) {
		t.Fatalf("agent environment = %v", got)
	}
	if got := cfg.ProviderEnvironmentNames("claude"); !reflect.DeepEqual(got, []string{"ANTHROPIC_LOG", "CLAUDE_CONFIG_DIR"}) {
		t.Fatalf("names = %v", got)
	}
	if cfg.AgentEnvironment("codex") != nil || cfg.ProviderEnvironmentNames("codex") != nil {
		t.Fatal("unconfigured provider received environment")
	}
}

func TestNormalizeDeepCopiesProviderEnvironment(t *testing.T) {
	in := Config{ProviderEnvironment: map[string]map[string]string{"claude": {"CLAUDE_CONFIG_DIR": "/accounts/a"}}}
	out := Normalize(in)
	out.ProviderEnvironment["claude"]["CLAUDE_CONFIG_DIR"] = "changed"
	out.ProviderEnvironment["codex"] = map[string]string{"CODEX_HOME": "/x"}
	if in.ProviderEnvironment["claude"]["CLAUDE_CONFIG_DIR"] != "/accounts/a" || len(in.ProviderEnvironment) != 1 {
		t.Fatal("Normalize mutated the caller's provider environment")
	}
}

func TestNormalizeLeavesNonLeadingTildeUnchanged(t *testing.T) {
	t.Setenv("HOME", "/home/runner")
	out := Normalize(Config{ProviderEnvironment: map[string]map[string]string{"claude": {
		"A": "~/x", "B": "/a/~/b", "C": "~other/x", "D": "~",
	}}})
	env := out.ProviderEnvironment["claude"]
	if env["A"] != "/home/runner/x" || env["B"] != "/a/~/b" || env["C"] != "~other/x" || env["D"] != "~" {
		t.Fatalf("expansion = %v", env)
	}
}

func TestValidateAcceptsProviderEnvironment(t *testing.T) {
	cfg := productionConfig()
	cfg.ProviderEnvironment = map[string]map[string]string{
		"claude":   {"CLAUDE_CONFIG_DIR": "/Users/operator/.claude-xogito"},
		"codex":    {"CODEX_HOME": "/srv/codex", "RUST_LOG": "warn"},
		"opencode": {"A": "value with spaces", "X_" + strings.Repeat("A", 62): "v"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid provider environment rejected: %v", err)
	}
}

func TestValidateRejectsProviderEnvironment(t *testing.T) {
	t.Parallel()
	cases := map[string]map[string]map[string]string{
		"unsupported provider":   {"gemini": {"A": "b"}},
		"lowercase name":         {"claude": {"claude_config_dir": "x"}},
		"leading digit":          {"claude": {"1A": "x"}},
		"leading underscore":     {"claude": {"_A": "x"}},
		"dash":                   {"claude": {"A-B": "x"}},
		"too long":               {"claude": {"A" + strings.Repeat("B", 64): "x"}},
		"empty name":             {"claude": {"": "x"}},
		"PATH":                   {"claude": {"PATH": "/bin"}},
		"HOME":                   {"claude": {"HOME": "/tmp"}},
		"USER":                   {"claude": {"USER": "root"}},
		"SHELL":                  {"claude": {"SHELL": "/bin/sh"}},
		"TMPDIR":                 {"claude": {"TMPDIR": "/tmp"}},
		"LOGNAME":                {"claude": {"LOGNAME": "root"}},
		"GH_TOKEN":               {"claude": {"GH_TOKEN": "ghp_secretvalue"}},
		"GITHUB_TOKEN":           {"codex": {"GITHUB_TOKEN": "ghs_secretvalue"}},
		"GITLAB_TOKEN":           {"opencode": {"GITLAB_TOKEN": "glpat-secretvalue"}},
		"BITBUCKET_TOKEN":        {"claude": {"BITBUCKET_TOKEN": "bb-secretvalue"}},
		"GIT prefix":             {"claude": {"GIT_DIR": "/tmp"}},
		"SSH prefix":             {"claude": {"SSH_AUTH_SOCK": "/tmp/a"}},
		"MCP socket":             {"claude": {"PLATFORM_MCP_SOCKET": "/tmp/s"}},
		"MCP token":              {"codex": {"PLATFORM_MCP_TOKEN_FILE": "/tmp/t"}},
		"platform prefix":        {"codex": {"PLATFORM_OTHER": "x"}},
		"env scrub":              {"claude": {"CLAUDE_CODE_SUBPROCESS_ENV_SCRUB": "0"}},
		"opencode config":        {"opencode": {"OPENCODE_CONFIG_CONTENT": "{}"}},
		"opencode config dir":    {"opencode": {"OPENCODE_CONFIG_DIR": "/tmp"}},
		"opencode disable":       {"opencode": {"OPENCODE_DISABLE_SHARE": "0"}},
		"opencode disable other": {"opencode": {"OPENCODE_DISABLE_ANYTHING": "0"}},
		"empty value":            {"claude": {"CLAUDE_CONFIG_DIR": ""}},
		"newline value":          {"claude": {"CLAUDE_CONFIG_DIR": "/a\n/b"}},
		"carriage return value":  {"claude": {"CLAUDE_CONFIG_DIR": "/a\r"}},
		"NUL value":              {"claude": {"CLAUDE_CONFIG_DIR": "/a\x00"}},
		"unexpanded home":        {"claude": {"CLAUDE_CONFIG_DIR": "~/x"}},
	}
	for name, environment := range cases {
		name, environment := name, environment
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := productionConfig()
			cfg.ProviderEnvironment = environment
			err := cfg.Validate()
			if err == nil {
				t.Fatal("invalid provider environment accepted")
			}
			if !strings.Contains(err.Error(), "provider_environment") {
				t.Fatalf("error lacks context: %v", err)
			}
			for _, values := range environment {
				for _, value := range values {
					if len(value) > 2 && strings.Contains(err.Error(), value) {
						t.Fatalf("error disclosed value: %v", err)
					}
				}
			}
		})
	}
}
