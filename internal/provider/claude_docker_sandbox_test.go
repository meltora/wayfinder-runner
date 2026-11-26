package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func claudeSandboxSettings(t *testing.T, p StartParams) map[string]any {
	t.Helper()
	if _, err := NewClaude().BuildSpec(p); err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(p.ConfigDir, "claude-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Sandbox map[string]any `json:"sandbox"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	return settings.Sandbox
}

func TestClaudeDockerIsolationUsesTheWeakerNestedSandbox(t *testing.T) {
	for _, readOnly := range []bool{true, false} {
		p := testParams(t)
		p.ReadOnly = readOnly
		p.DockerIsolation = true
		sandbox := claudeSandboxSettings(t, p)
		if sandbox["enableWeakerNestedSandbox"] != true {
			t.Fatalf("readOnly=%v sandbox = %v", readOnly, sandbox)
		}
		if sandbox["enabled"] != true || sandbox["failIfUnavailable"] != true || sandbox["allowUnsandboxedCommands"] != false {
			t.Fatalf("readOnly=%v weakened the sandbox requirement: %v", readOnly, sandbox)
		}
	}
}

func TestClaudeDirectIsolationKeepsTheStrictSandbox(t *testing.T) {
	for _, hardened := range []bool{true, false} {
		p := testParams(t)
		p.HardenedRuntime = hardened
		sandbox := claudeSandboxSettings(t, p)
		if _, present := sandbox["enableWeakerNestedSandbox"]; present {
			t.Fatalf("hardened=%v direct isolation sandbox = %v", hardened, sandbox)
		}
	}
}

const claudeSubprocessEnvironmentScrub = "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"

func hasEnvironmentKey(environment []string, name string) bool {
	for _, entry := range environment {
		if key, _, _ := strings.Cut(entry, "="); key == name {
			return true
		}
	}
	return false
}

func TestClaudeDockerIsolationDoesNotScrubTheSubprocessEnvironment(t *testing.T) {
	for _, readOnly := range []bool{true, false} {
		p := testParams(t)
		p.ReadOnly = readOnly
		p.DockerIsolation = true
		spec, err := NewClaude().BuildSpec(p)
		if err != nil {
			t.Fatal(err)
		}
		if hasEnvironmentKey(spec.Env, claudeSubprocessEnvironmentScrub) {
			t.Fatalf("readOnly=%v docker env = %q", readOnly, spec.Env)
		}
		if container := ContainerEnvironment(spec.Env); hasEnvironmentKey(container, claudeSubprocessEnvironmentScrub) {
			t.Fatalf("readOnly=%v container env = %q", readOnly, container)
		}
	}
}

func TestClaudeHardenedParamsWithoutDockerIsolationSetTheSubprocessEnvironmentScrub(t *testing.T) {
	for _, hardened := range []bool{true, false} {
		p := testParams(t)
		p.HardenedRuntime = hardened
		spec, err := NewClaude().BuildSpec(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := hasEnvironmentKey(spec.Env, claudeSubprocessEnvironmentScrub); got != hardened {
			t.Fatalf("hardened=%v scrub present=%v env=%q", hardened, got, spec.Env)
		}
	}
}

func TestClaudeHardenedSandboxRoutesNodeFetchThroughTheSandboxProxy(t *testing.T) {
	for _, docker := range []bool{true, false} {
		for _, hardened := range []bool{true, false} {
			p := testParams(t)
			p.HardenedRuntime = hardened
			p.DockerIsolation = docker
			spec, err := NewClaude().BuildSpec(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(spec.Env, "NODE_USE_ENV_PROXY=1"); got != hardened {
				t.Fatalf("docker=%v hardened=%v env=%q", docker, hardened, spec.Env)
			}
			if got := slices.Contains(ContainerEnvironment(spec.Env), "NODE_USE_ENV_PROXY=1"); got != hardened {
				t.Fatalf("docker=%v hardened=%v container env=%q", docker, hardened, ContainerEnvironment(spec.Env))
			}
		}
	}
}

func TestClaudePromptOnlySpecDoesNotScrubTheSubprocessEnvironment(t *testing.T) {
	spec, err := NewClaude().BuildPromptOnlySpec(promptOnlyParams(t))
	if err != nil {
		t.Fatal(err)
	}
	if hasEnvironmentKey(spec.Env, claudeSubprocessEnvironmentScrub) {
		t.Fatalf("prompt-only env = %q", spec.Env)
	}
}

func TestClaudePromptOnlySpecNeverWeakensASandbox(t *testing.T) {
	params := promptOnlyParams(t)
	spec, err := NewClaude().BuildPromptOnlySpec(params)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(spec.Args, " "), "enableWeakerNestedSandbox") {
		t.Fatalf("prompt-only args = %q", spec.Args)
	}
	entries, err := os.ReadDir(params.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(params.ConfigDir, entry.Name()))
		if err == nil && strings.Contains(string(raw), "enableWeakerNestedSandbox") {
			t.Fatalf("prompt-only %s contains the weaker sandbox setting", entry.Name())
		}
	}
}
