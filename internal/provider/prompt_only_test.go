package provider

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func promptOnlyParams(t *testing.T) PromptOnlyParams {
	t.Helper()
	root := t.TempDir()
	params := PromptOnlyParams{
		ExecutionID: "effect-1", WorkDir: filepath.Join(root, "empty-workspace"),
		ConfigDir: filepath.Join(root, "private-config"), Prompt: "Return only the requested JSON.",
		SchemaPath: filepath.Join(root, "private-config", "schema.json"),
		OutputPath: filepath.Join(root, "private-config", "result.json"), Model: "consultation-model",
	}
	if err := os.MkdirAll(params.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(params.WorkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(params.SchemaPath, []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return params
}

func TestCodexPromptOnlySpecUsesEphemeralReadOnlyNoRepositoryExecution(t *testing.T) {
	params := promptOnlyParams(t)
	params.BinaryPath = "/opt/bin/codex"
	spec, err := NewCodex().BuildPromptOnlySpec(params)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.Args, " ")
	for _, required := range []string{"exec", "--ephemeral", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules", "--sandbox read-only", "--output-schema " + params.SchemaPath, "--output-last-message " + params.OutputPath, "--model " + params.Model} {
		if !strings.Contains(joined, required) {
			t.Fatalf("argv missing %q: %q", required, spec.Args)
		}
	}
	for _, feature := range []string{
		"shell_tool", "unified_exec", "code_mode_host", "apps", "browser_use", "browser_use_external",
		"computer_use", "image_generation", "standalone_web_search", "web_search_request",
	} {
		if !containsAdjacentArgs(spec.Args, "--disable", feature) {
			t.Fatalf("Codex feature %q was not explicitly disabled: %q", feature, spec.Args)
		}
	}
	if !containsAdjacentArgs(spec.Args, "-c", `approval_policy="never"`) {
		t.Fatalf("Codex approval policy is not fail-closed: %q", spec.Args)
	}
	assertPromptOnlySpec(t, spec.Dir, spec.Stdin, params, joined)
	if strings.Contains(strings.ToLower(joined), "mcp") {
		t.Fatalf("Codex prompt-only argv contains MCP: %s", joined)
	}
}

func containsAdjacentArgs(args []string, first, second string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == first && args[index+1] == second {
			return true
		}
	}
	return false
}

func TestClaudePromptOnlySpecDisablesAllToolsAndPersistence(t *testing.T) {
	params := promptOnlyParams(t)
	params.BinaryPath = "/opt/bin/claude"
	spec, err := NewClaude().BuildPromptOnlySpec(params)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.Args, " ")
	for _, required := range []string{"--tools ", "--json-schema ", "--no-session-persistence", "--bare", "--model " + params.Model, "--mcp-config " + filepath.Join(params.ConfigDir, "claude-empty-mcp.json"), "--strict-mcp-config"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("argv missing %q: %q", required, spec.Args)
		}
	}
	emptyMCP, err := os.ReadFile(filepath.Join(params.ConfigDir, "claude-empty-mcp.json"))
	if err != nil || string(emptyMCP) != `{"mcpServers":{}}` {
		t.Fatalf("strict empty MCP config = %q err=%v", emptyMCP, err)
	}
	for index, arg := range spec.Args {
		if arg == "--tools" && (index+1 >= len(spec.Args) || spec.Args[index+1] != "") {
			t.Fatalf("--tools must be followed by an empty allowlist: %q", spec.Args)
		}
	}
	assertPromptOnlySpec(t, spec.Dir, spec.Stdin, params, joined)
}

func TestOpenCodePromptOnlySpecDeniesEveryPermission(t *testing.T) {
	params := promptOnlyParams(t)
	params.Model = "openai/consultation-model"
	params.BinaryPath = "/opt/bin/opencode"
	spec, err := NewOpenCode().BuildPromptOnlySpec(params)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.Args, " ")
	if strings.Contains(joined, "--auto") || !strings.Contains(joined, "--pure") || !strings.Contains(joined, "--model "+params.Model) {
		t.Fatalf("OpenCode prompt-only argv is unsafe: %q", spec.Args)
	}
	var config map[string]any
	for _, entry := range spec.Env {
		if raw, ok := strings.CutPrefix(entry, "OPENCODE_CONFIG_CONTENT="); ok {
			if err := json.Unmarshal([]byte(raw), &config); err != nil {
				t.Fatal(err)
			}
		}
	}
	permissions, ok := config["permission"].(map[string]any)
	if !ok || permissions["*"] != "deny" {
		t.Fatalf("OpenCode must deny all permissions: %#v", config)
	}
	for name, value := range permissions {
		if value != "deny" {
			t.Fatalf("permission %q = %v, want deny", name, value)
		}
	}
	if _, ok := config["mcp"]; ok {
		t.Fatalf("prompt-only config must not contain MCP: %#v", config)
	}
	assertPromptOnlySpec(t, spec.Dir, spec.Stdin, params, joined)
	if strings.Contains(strings.ToLower(joined), "mcp") {
		t.Fatalf("OpenCode prompt-only argv contains MCP: %s", joined)
	}
}

func TestPromptOnlySpecsRejectMissingModelAndNonPrivatePaths(t *testing.T) {
	params := promptOnlyParams(t)
	params.Model = ""
	if _, err := NewCodex().BuildPromptOnlySpec(params); err == nil {
		t.Fatal("missing explicit consultation model should fail")
	}
	params = promptOnlyParams(t)
	params.WorkDir = "."
	if _, err := NewClaude().BuildPromptOnlySpec(params); err == nil {
		t.Fatal("relative workspace should fail")
	}
}

func assertPromptOnlySpec(t *testing.T, gotDir string, stdin io.Reader, params PromptOnlyParams, joined string) {
	t.Helper()
	if gotDir != params.WorkDir {
		t.Fatalf("dir = %q, want private empty workspace %q", gotDir, params.WorkDir)
	}
	if strings.Contains(joined, params.Prompt) {
		t.Fatalf("raw prompt leaked into process argv: %s", joined)
	}
	rawPrompt, err := io.ReadAll(stdin)
	if err != nil || string(rawPrompt) != params.Prompt {
		t.Fatalf("stdin prompt = %q err=%v", rawPrompt, err)
	}
	for _, forbidden := range []string{"platform", "add-dir", "resume", "plugin", "dangerously-bypass", "acceptEdits"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("prompt-only argv contains forbidden %q: %s", forbidden, joined)
		}
	}
}
