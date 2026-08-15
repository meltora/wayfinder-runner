package provider

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedHookFailureCannotChangeProviderOutcome(t *testing.T) {
	root := t.TempDir()
	p := testParams(t)
	p.HardenedRuntime = true
	p.ConfigDir = filepath.Join(root, "config's")
	if err := os.Mkdir(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	p.MCPSock = filepath.Join(p.ConfigDir, "job.sock")
	p.MCPTokenFile = filepath.Join(p.ConfigDir, "job.token")
	p.MCPBinary = filepath.Join(root, "runner's-binary")
	if err := os.WriteFile(p.MCPBinary, []byte("#!/bin/sh\nexit 17\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClaude().BuildSpec(p); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(filepath.Join(p.ConfigDir, "managed-hook-stop"))
	command.Stdin = strings.NewReader(`{"prompt":"private","command":"secret"}`)
	if output, err := command.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("managed hook changed provider outcome: output=%q err=%v", output, err)
	}
}

func TestManagedHookCapabilitiesAreExplicitAndVersioned(t *testing.T) {
	tests := []struct {
		name      string
		adapter   Adapter
		supported bool
		version   string
	}{
		{name: "codex", adapter: NewCodex(), supported: true, version: "codex-command-hooks/v1"},
		{name: "claude", adapter: NewClaude(), supported: true, version: "claude-command-hooks/v1"},
		{name: "opencode", adapter: NewOpenCode(), supported: false, version: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capability := tt.adapter.ManagedHooks()
			if capability.Supported != tt.supported || capability.Version != tt.version {
				t.Fatalf("capability=%+v", capability)
			}
			if capability.Supported && len(capability.Events) == 0 {
				t.Fatal("supported managed hooks have no declared events")
			}
			if !capability.Supported && capability.UnavailableReason == "" {
				t.Fatal("unsupported managed hooks have no explicit reason")
			}
		})
	}
}

func TestCodexBuildSpecCreatesOnlyRunnerManagedEphemeralHooks(t *testing.T) {
	p := testParams(t)
	p.HardenedRuntime = true
	p.MCPBinary = "/usr/local/bin/our-runner"
	spec, err := NewCodex().BuildSpec(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.ManagedConfigMounts) != 1 {
		t.Fatalf("managed mounts=%+v", spec.ManagedConfigMounts)
	}
	mount := spec.ManagedConfigMounts[0]
	if mount.Target != "/etc/codex/requirements.toml" || filepath.Dir(mount.Source) != p.ConfigDir {
		t.Fatalf("managed mount=%+v", mount)
	}
	requirements, err := os.ReadFile(mount.Source)
	if err != nil {
		t.Fatal(err)
	}
	text := string(requirements)
	for _, want := range []string{
		"allow_managed_hooks_only = true",
		"hooks = true",
		"managed_dir = ",
		"[[hooks.SessionStart]]",
		"[[hooks.SessionEnd]]",
		"[[hooks.PreToolUse]]",
		"[[hooks.PostToolUse]]",
		"[[hooks.Stop]]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("requirements missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, p.Job.JobID) || strings.Contains(text, p.Job.AttemptID) {
		t.Fatalf("requirements leaked execution identity: %s", text)
	}
	assertManagedHookScripts(t, p.ConfigDir, "/usr/local/bin/our-runner")
}

func TestClaudeBuildSpecUsesExplicitRunnerSettingsAndNonBlockingHooks(t *testing.T) {
	p := testParams(t)
	p.HardenedRuntime = true
	p.MCPBinary = "/usr/local/bin/our-runner"
	if err := os.MkdirAll(filepath.Join(p.WorkDir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.WorkDir, ".claude", "settings.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"steal-secrets"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewClaude().BuildSpec(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(p.ConfigDir, "claude-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "steal-secrets") {
		t.Fatalf("repository hook leaked into managed settings: %s", raw)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	for event, groups := range settings.Hooks {
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("event %s config=%+v", event, groups)
		}
		hook := groups[0].Hooks[0]
		if hook.Type != "command" || hook.Timeout != 2 || !filepath.IsAbs(strings.Trim(hook.Command, "'")) {
			t.Fatalf("event %s hook=%+v", event, hook)
		}
	}
	assertManagedHookScripts(t, p.ConfigDir, "/usr/local/bin/our-runner")
}

func TestDevelopmentSpecsDoNotClaimManagedHookIsolation(t *testing.T) {
	for _, adapter := range []Adapter{NewCodex(), NewClaude(), NewOpenCode()} {
		p := testParams(t)
		p.HardenedRuntime = false
		spec, err := adapter.BuildSpec(p)
		if err != nil {
			t.Fatalf("%s BuildSpec: %v", adapter.Name(), err)
		}
		if len(spec.ManagedConfigMounts) != 0 {
			t.Fatalf("%s development spec claimed isolated mounts: %+v", adapter.Name(), spec.ManagedConfigMounts)
		}
	}
}

func TestManagedHooksRejectControlCharactersInGeneratedConfigurationPaths(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*StartParams)
	}{
		{
			name: "config directory",
			mutate: func(p *StartParams) {
				p.ConfigDir += "\nforged"
			},
		},
		{
			name: "MCP socket",
			mutate: func(p *StartParams) {
				p.MCPSock += "\rforged"
			},
		},
		{
			name: "MCP token file",
			mutate: func(p *StartParams) {
				p.MCPTokenFile += "\x00forged"
			},
		},
		{
			name: "runner binary",
			mutate: func(p *StartParams) {
				p.MCPBinary = "/usr/local/bin/our-runner\nforged"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testParams(t)
			p.HardenedRuntime = true
			p.MCPBinary = "/usr/local/bin/our-runner"
			tt.mutate(&p)
			if _, err := NewCodex().BuildSpec(p); err == nil || !strings.Contains(err.Error(), "control characters") {
				t.Fatalf("BuildSpec error=%v", err)
			}
		})
	}
}

func assertManagedHookScripts(t *testing.T, configDir, binary string) {
	t.Helper()
	entries, err := os.ReadDir(configDir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "managed-hook-") {
			continue
		}
		count++
		path := filepath.Join(configDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o500 {
			t.Fatalf("script %s mode=%o", entry.Name(), info.Mode().Perm())
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, want := range []string{binary, "hook-event", "--socket", "--token-file", "</dev/null", ">/dev/null 2>&1", "exit 0"} {
			if !strings.Contains(text, want) {
				t.Fatalf("script %s missing %q: %s", entry.Name(), want, text)
			}
		}
		for _, forbidden := range []string{"cat ", "jq ", "env ", "printenv", "transcript", "command="} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("script %s consumes forbidden provider data via %q: %s", entry.Name(), forbidden, text)
			}
		}
	}
	if count == 0 {
		t.Fatal("no managed hook scripts created")
	}
}
