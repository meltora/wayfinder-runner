package provider

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/supervisor"
)

func testParams(t *testing.T) StartParams {
	t.Helper()
	return StartParams{
		Job: v1.JobSpec{
			JobID: "j1", AttemptID: "a1", Kind: v1.JobKindPlan, FencingToken: 1,
			Ticket: v1.TicketContext{Key: "PROJ-1", Summary: "Do the thing"},
		},
		WorkDir:         t.TempDir(),
		ConfigDir:       t.TempDir(),
		PackDir:         t.TempDir(),
		MCPSock:         "/tmp/job.sock",
		MCPTokenFile:    "/tmp/job.token",
		Prompt:          "plan the ticket",
		HardenedRuntime: true,
	}
}

func TestCodexBuildSpec(t *testing.T) {
	p := testParams(t)
	p.ReadOnly = true
	spec, err := NewCodex().BuildSpec(p)
	if err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	if spec.Command != "codex" || spec.Args[0] != "exec" || spec.Args[1] != "--json" {
		t.Fatalf("args = %v", spec.Args)
	}
	joined := strings.Join(spec.Args, " ")
	for _, want := range []string{"mcp_servers.platform.command", "mcp_servers.platform.args", "mcp-serve"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args %q missing %q", joined, want)
		}
	}
	if !strings.Contains(joined, "--profile runner-plan") || !strings.Contains(joined, "--strict-config") {
		t.Fatalf("planning spec does not select its hardened profile: %q", joined)
	}
	if strings.Contains(joined, "dangerously-bypass") {
		t.Fatalf("planning spec bypasses its sandbox: %q", joined)
	}
	if !strings.Contains(joined, "--token-file") {
		t.Fatalf("MCP token handling is unsafe: %q", joined)
	}
}

func TestClaudeBuildSpec(t *testing.T) {
	p := testParams(t)
	p.ReadOnly = true
	spec, err := NewClaude().BuildSpec(p)
	if err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	joined := strings.Join(spec.Args, " ")
	for _, want := range []string{"-p", "stream-json", "--mcp-config", "--strict-mcp-config", "--restricted", "--settings", "mcp__platform__*"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args %q missing %q", joined, want)
		}
	}
	mcpConfig, err := os.ReadFile(filepath.Join(p.ConfigDir, "claude-mcp.json"))
	if err != nil {
		t.Fatalf("read MCP config: %v", err)
	}
	if !strings.Contains(string(mcpConfig), "--token-file") {
		t.Fatalf("MCP config does not use a token file: %s", mcpConfig)
	}
	settings, err := os.ReadFile(filepath.Join(p.ConfigDir, "claude-settings.json"))
	if err != nil {
		t.Fatalf("read Claude settings: %v", err)
	}
	settingsText := string(settings)
	for _, want := range []string{`"failIfUnavailable": true`, `"allowUnsandboxedCommands": false`, `"~/.claude/.credentials.json"`, `"strictAllowlist": true`} {
		if !strings.Contains(settingsText, want) {
			t.Fatalf("Claude settings %s missing %s", settingsText, want)
		}
	}
	if strings.Contains(joined, "--permission-mode plan") || !strings.Contains(joined, "--permission-mode dontAsk --permission-prompts none") {
		t.Fatalf("planning spec does not use the read-only allowlist mode: %q", joined)
	}
}

func TestHardenedBuildSpecsRemainAutonomousWithoutBypass(t *testing.T) {
	p := testParams(t)
	p.ReadOnly = false
	codex, err := NewCodex().BuildSpec(p)
	if err != nil {
		t.Fatal(err)
	}
	codexArgs := strings.Join(codex.Args, " ")
	if !strings.Contains(codexArgs, "--profile runner-build") || strings.Contains(codexArgs, "dangerously-bypass") {
		t.Fatalf("unsafe Codex build args: %s", codexArgs)
	}
	claude, err := NewClaude().BuildSpec(p)
	if err != nil {
		t.Fatal(err)
	}
	claudeArgs := strings.Join(claude.Args, " ")
	if !strings.Contains(claudeArgs, "--permission-mode acceptEdits") || strings.Contains(claudeArgs, "bypassPermissions") {
		t.Fatalf("unsafe Claude build args: %s", claudeArgs)
	}
}

func TestParseLine(t *testing.T) {
	if _, ok := NewCodex().ParseLine([]byte(`{"type":"event"}`)); !ok {
		t.Fatal("codex should parse json line")
	}
	if _, ok := NewCodex().ParseLine([]byte(`not json`)); ok {
		t.Fatal("non-json should not parse")
	}
	if _, ok := NewClaude().ParseLine([]byte(`{"type":"assistant"}`)); !ok {
		t.Fatal("claude should parse json line")
	}
}

func TestFor(t *testing.T) {
	if _, err := For("codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := For("claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := For("nope"); err == nil {
		t.Fatal("expected unknown provider error")
	}
}

func TestProviderIdentityAvailabilityAndDevelopmentSpecs(t *testing.T) {
	codex := NewCodex()
	codex.LookPath = func(name string) (string, error) {
		if name != "codex" {
			t.Fatalf("lookup=%q", name)
		}
		return "/bin/codex", nil
	}
	claude := NewClaude()
	claude.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	if codex.Name() != "codex" || !codex.Available() {
		t.Fatalf("codex name=%q available=%v", codex.Name(), codex.Available())
	}
	if claude.Name() != "claude" || claude.Available() {
		t.Fatalf("claude name=%q available=%v", claude.Name(), claude.Available())
	}

	p := testParams(t)
	p.HardenedRuntime = false
	p.ReadOnly = false
	p.BinaryPath = "/opt/codex"
	p.ResumeSessionID = "session-1"
	p.MCPBinary = "/usr/local/bin/our-runner"
	spec, err := codex.BuildSpec(p)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.Args, " ")
	if spec.Command != "/opt/codex" || !strings.Contains(joined, "dangerously-bypass") || !strings.Contains(joined, "resume session-1") {
		t.Fatalf("development spec=%+v", spec)
	}
	p.ReadOnly = true
	spec, err = codex.BuildSpec(p)
	if err != nil || !strings.Contains(strings.Join(spec.Args, " "), "dangerously-bypass") {
		t.Fatalf("read-only development spec=%+v err=%v", spec, err)
	}
}

func TestClaudePluginResumeAndConfigDirectoryValidation(t *testing.T) {
	p := testParams(t)
	p.ConfigDir = "relative"
	if _, err := NewClaude().BuildSpec(p); err == nil {
		t.Fatal("relative config directory accepted")
	}
	p = testParams(t)
	pluginDir := filepath.Join(p.PackDir, ".claude-plugin")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"name":"test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p.ResumeSessionID = "session-1"
	p.BinaryPath = "/opt/claude"
	spec, err := NewClaude().BuildSpec(p)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.Args, " ")
	if spec.Command != "/opt/claude" || !strings.Contains(joined, "--plugin-dir") || !strings.Contains(joined, "--resume session-1") {
		t.Fatalf("Claude spec=%+v", spec)
	}
	if _, ok := NewClaude().ParseLine([]byte(`not json`)); ok {
		t.Fatal("Claude parsed invalid JSON")
	}
}

func TestAllowedProviderEnvironmentExcludesSecrets(t *testing.T) {
	t.Setenv("RUNNER_PRIVATE_SECRET", "hidden")
	t.Setenv("HTTPS_PROXY", "http://proxy.example.test")
	env := strings.Join(allowedEnvironment(), "\n")
	if strings.Contains(env, "RUNNER_PRIVATE_SECRET") || !strings.Contains(env, "HTTPS_PROXY=") {
		t.Fatalf("environment=%q", env)
	}
}

func TestContainerEnvironmentForwardsOnlyRunnerGeneratedControls(t *testing.T) {
	env := ContainerEnvironment([]string{
		"HOME=/home/runner",
		"HTTPS_PROXY=http://secret-proxy",
		"OPENCODE_CONFIG_CONTENT={}",
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1",
		"UNTRUSTED=value",
	})
	joined := strings.Join(env, "\n")
	for _, want := range []string{"OPENCODE_CONFIG_CONTENT={}", "OPENCODE_DISABLE_PROJECT_CONFIG=1", "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("container environment %q missing %q", joined, want)
		}
	}
	for _, denied := range []string{"HOME=", "HTTPS_PROXY=", "UNTRUSTED="} {
		if strings.Contains(joined, denied) {
			t.Fatalf("container environment leaked %q: %q", denied, joined)
		}
	}
}

func TestDefaultProviderAvailabilityLookupDoesNotPanic(t *testing.T) {
	// Availability is host-dependent; this exercises the real PATH lookup used
	// by the capability manifest without asserting that either CLI is installed.
	_ = NewCodex().Available()
	_ = NewClaude().Available()
}

var updateProviderGoldens = flag.Bool("update-provider-goldens", false, "rewrite provider baseline goldens")

type goldenFile struct {
	Name    string `json:"name"`
	Mode    string `json:"mode"`
	Content string `json:"content"`
}

type goldenSpec struct {
	Name                string                          `json:"name"`
	Command             string                          `json:"command"`
	Args                []string                        `json:"args"`
	Dir                 string                          `json:"dir"`
	EnvExtra            []string                        `json:"env_extra"`
	Stdin               string                          `json:"stdin"`
	ProviderEnvironment []string                        `json:"provider_environment"`
	ManagedConfigMounts []supervisor.ManagedConfigMount `json:"managed_config_mounts"`
	ConfigFiles         []goldenFile                    `json:"config_files"`
}

func goldenParams(t *testing.T, readOnly, hardened bool) StartParams {
	t.Helper()
	return StartParams{
		Job: v1.JobSpec{
			JobID: "j1", AttemptID: "a1", Kind: v1.JobKindBuild, FencingToken: 1,
			Ticket: v1.TicketContext{Key: "PROJ-1", Summary: "Do the thing"},
		},
		WorkDir:         t.TempDir(),
		ConfigDir:       t.TempDir(),
		PackDir:         t.TempDir(),
		MCPSock:         "/run/runner/job.sock",
		MCPTokenFile:    "/run/runner/job.token",
		MCPBinary:       "/usr/local/bin/our-runner",
		Prompt:          "do the ticket",
		Model:           "anthropic/claude-test",
		ReadOnly:        readOnly,
		HardenedRuntime: hardened,
		Environment:     []string{"CLAUDE_CONFIG_DIR=/home/runner/.claude-config"},
	}
}

func snapshotSpec(t *testing.T, p StartParams, spec supervisor.Spec) []byte {
	t.Helper()
	baseline := map[string]bool{}
	for _, entry := range allowedEnvironment() {
		baseline[entry] = true
	}
	snapshot := goldenSpec{
		Name: spec.Name, Command: spec.Command, Args: spec.Args, Dir: spec.Dir,
		ProviderEnvironment: spec.ProviderEnvironment, ManagedConfigMounts: spec.ManagedConfigMounts,
	}
	for _, entry := range spec.Env {
		if !baseline[entry] {
			snapshot.EnvExtra = append(snapshot.EnvExtra, entry)
		}
	}
	if spec.Stdin != nil {
		raw, err := io.ReadAll(spec.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Stdin = string(raw)
	}
	entries, err := os.ReadDir(p.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(p.ConfigDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		snapshot.ConfigFiles = append(snapshot.ConfigFiles, goldenFile{Name: entry.Name(), Mode: info.Mode().Perm().String(), Content: string(raw)})
	}
	sort.Slice(snapshot.ConfigFiles, func(i, j int) bool { return snapshot.ConfigFiles[i].Name < snapshot.ConfigFiles[j].Name })
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.NewReplacer(p.ConfigDir, "$CONFIG", p.WorkDir, "$WORK", p.PackDir, "$PACK").Replace(string(raw))
	return []byte(text + "\n")
}

func TestProviderSpecsMatchBaselineGoldens(t *testing.T) {
	cases := []struct {
		name     string
		adapter  Adapter
		readOnly bool
	}{
		{"claude-build", NewClaude(), false},
		{"codex-build", NewCodex(), false},
		{"codex-plan", NewCodex(), true},
		{"opencode-build", NewOpenCode(), false},
		{"opencode-plan", NewOpenCode(), true},
	}
	for _, tc := range cases {
		for _, hardened := range []bool{false, true} {
			name := tc.name
			if hardened {
				name += "-hardened"
			}
			t.Run(name, func(t *testing.T) {
				p := goldenParams(t, tc.readOnly, hardened)
				if tc.readOnly {
					p.Job.Kind = v1.JobKindPlan
				}
				spec, err := tc.adapter.BuildSpec(p)
				if err != nil {
					t.Fatalf("BuildSpec: %v", err)
				}
				got := snapshotSpec(t, p, spec)
				path := filepath.Join("testdata", "baseline-"+name+".golden.json")
				if *updateProviderGoldens {
					if err := os.WriteFile(path, got, 0o600); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read golden: %v", err)
				}
				if string(got) != string(want) {
					t.Fatalf("%s drifted from baseline golden\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
				}
			})
		}
	}
}
