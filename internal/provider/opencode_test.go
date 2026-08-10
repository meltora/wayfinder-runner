package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeBuildSpecUsesConfiguredModelWithoutChoosingAProvider(t *testing.T) {
	p := testParams(t)
	p.Model = "customer-provider/customer-model"
	p.ReadOnly = false

	spec, err := NewOpenCode().BuildSpec(p)
	if err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	if spec.Command != "opencode" {
		t.Fatalf("command = %q", spec.Command)
	}
	joined := strings.Join(spec.Args, " ")
	for _, want := range []string{"run", "--format json", "--model customer-provider/customer-model", "--agent build", "--auto", "--pure"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args %q missing %q", joined, want)
		}
	}

	config := openCodeConfigFromEnv(t, spec.Env)
	if _, exists := config["model"]; exists {
		t.Fatal("runtime config must not hardcode a model")
	}
	permissions := config["permission"].(map[string]any)
	for _, denied := range []string{"bash", "lsp", "task", "webfetch", "websearch", "external_directory"} {
		if permissions[denied] != "deny" {
			t.Fatalf("permission %s = %#v, want deny", denied, permissions[denied])
		}
	}
	if permissions["edit"] != "allow" || permissions["platform_*"] != "allow" {
		t.Fatalf("build permissions = %#v", permissions)
	}
	assertOpenCodeSafetyEnvironment(t, spec.Env)
}

func TestOpenCodeBuildSpecUsesCustomerDefaultModelAndReadOnlyMode(t *testing.T) {
	p := testParams(t)
	p.ReadOnly = true

	spec, err := NewOpenCode().BuildSpec(p)
	if err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	joined := strings.Join(spec.Args, " ")
	if strings.Contains(joined, "--model") {
		t.Fatalf("default model was overridden: %q", joined)
	}
	if !strings.Contains(joined, "--agent plan") {
		t.Fatalf("plan agent not selected: %q", joined)
	}
	config := openCodeConfigFromEnv(t, spec.Env)
	permissions := config["permission"].(map[string]any)
	if permissions["edit"] != "deny" {
		t.Fatalf("plan edit permission = %#v", permissions["edit"])
	}
}

func TestOpenCodeBuildSpecConfiguresOnlyPlatformMCP(t *testing.T) {
	p := testParams(t)
	p.HardenedRuntime = false
	p.ResumeSessionID = "session-1"
	p.PackDir = "/packs/customer"
	p.BinaryPath = "/opt/opencode"
	p.MCPBinary = "our-runner"

	spec, err := NewOpenCode().BuildSpec(p)
	if err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	joined := strings.Join(spec.Args, " ")
	if spec.Command != "/opt/opencode" ||
		!strings.Contains(joined, "--session session-1") ||
		!strings.Contains(joined, "--fork") {
		t.Fatalf("spec = %+v", spec)
	}
	config := openCodeConfigFromEnv(t, spec.Env)
	mcp := config["mcp"].(map[string]any)
	if len(mcp) != 1 {
		t.Fatalf("MCP config = %#v", mcp)
	}
	platform := mcp["platform"].(map[string]any)
	command := platform["command"].([]any)
	if strings.Join(anyStrings(t, command), " ") != "our-runner mcp-serve --socket /tmp/job.sock --token-file /tmp/job.token" {
		t.Fatalf("MCP command = %#v", command)
	}
	if got := envValue(spec.Env, "OPENCODE_CONFIG_DIR"); got != p.PackDir {
		t.Fatalf("OPENCODE_CONFIG_DIR = %q", got)
	}
	if _, ok := NewOpenCode().ParseLine([]byte(`{"type":"text","sessionID":"ses_1"}`)); !ok {
		t.Fatal("OpenCode should parse JSON event lines")
	}
}

func TestOpenCodeHardenedJobLoadsInstructionsButNotEphemeralSession(t *testing.T) {
	p := testParams(t)
	p.ResumeSessionID = "session-1"
	if err := os.WriteFile(filepath.Join(p.WorkDir, "AGENTS.md"), []byte("# rules"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := NewOpenCode().BuildSpec(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(spec.Args, " "), "--session") {
		t.Fatalf("hardened job attempted to resume an ephemeral session: %v", spec.Args)
	}
	config := openCodeConfigFromEnv(t, spec.Env)
	instructions := config["instructions"].([]any)
	if len(instructions) != 1 || instructions[0] != filepath.Join(p.WorkDir, "AGENTS.md") {
		t.Fatalf("instructions = %#v", instructions)
	}
}

func TestOpenCodeBuildSpecRejectsUnsafeInputs(t *testing.T) {
	cases := map[string]func(*StartParams){
		"relative config": func(p *StartParams) { p.ConfigDir = "relative" },
		"bad model":       func(p *StartParams) { p.Model = "--help" },
		"bad session":     func(p *StartParams) { p.ResumeSessionID = "session with spaces" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := testParams(t)
			mutate(&p)
			if _, err := NewOpenCode().BuildSpec(p); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
}

func openCodeConfigFromEnv(t *testing.T, env []string) map[string]any {
	t.Helper()
	raw := envValue(env, "OPENCODE_CONFIG_CONTENT")
	if raw == "" {
		t.Fatal("OPENCODE_CONFIG_CONTENT is missing")
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatalf("parse OpenCode config: %v", err)
	}
	return config
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func anyStrings(t *testing.T, values []any) []string {
	t.Helper()
	out := make([]string, len(values))
	for i, value := range values {
		var ok bool
		out[i], ok = value.(string)
		if !ok {
			t.Fatalf("value %d = %#v, want string", i, value)
		}
	}
	return out
}

func assertOpenCodeSafetyEnvironment(t *testing.T, env []string) {
	t.Helper()
	for _, key := range []string{
		"OPENCODE_DISABLE_AUTOUPDATE",
		"OPENCODE_DISABLE_SHARE",
		"OPENCODE_DISABLE_LSP_DOWNLOAD",
		"OPENCODE_DISABLE_DEFAULT_PLUGINS",
	} {
		if got := envValue(env, key); got != "1" {
			t.Fatalf("%s = %q, want 1", key, got)
		}
	}
}
