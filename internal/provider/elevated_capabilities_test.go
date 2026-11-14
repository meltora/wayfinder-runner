package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/elevatedcapability"
)

func testElevatedGrants() elevatedcapability.Grants {
	return elevatedcapability.Grants{
		MCPServers: map[string]map[string]any{"docs": {"type": "stdio", "command": "node", "args": []string{"docs.js"}}},
		Hooks: map[string][]map[string]any{"PostToolUse": {{
			"matcher": "Edit", "hooks": []map[string]any{{"type": "command", "command": "./format.sh"}},
		}}},
		AllowedDomains: []string{"registry.npmjs.org"},
	}
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestClaudeBuildSpecAppliesApprovedElevatedCapabilities(t *testing.T) {
	p := testParams(t)
	p.Job.Kind = v1.JobKindBuild
	p.Elevated = testElevatedGrants()
	spec, err := NewClaude().BuildSpec(p)
	if err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	mcp := readJSONFile(t, filepath.Join(p.ConfigDir, "claude-mcp.json"))["mcpServers"].(map[string]any)
	if len(mcp) != 2 || mcp["platform"] == nil || mcp["docs"].(map[string]any)["command"] != "node" {
		t.Fatalf("MCP config = %#v", mcp)
	}
	allowed, _ := flagValues(spec.Args, "--allowedTools")
	if !reflect.DeepEqual(allowed, []string{"mcp__platform__*", "Bash", "mcp__docs__*"}) {
		t.Fatalf("allowed tools = %v", allowed)
	}
	settings := readJSONFile(t, filepath.Join(p.ConfigDir, "claude-settings.json"))
	hooks := settings["hooks"].(map[string]any)
	post := hooks["PostToolUse"].([]any)
	if last := post[len(post)-1].(map[string]any); last["matcher"] != "Edit" {
		t.Fatalf("approved hook missing: %#v", post)
	}
	if hooks["PreToolUse"] == nil {
		t.Fatalf("managed hooks were dropped: %#v", hooks)
	}
	network := settings["sandbox"].(map[string]any)["network"].(map[string]any)
	if network["strictAllowlist"] != true || !reflect.DeepEqual(network["allowedDomains"], []any{"registry.npmjs.org"}) {
		t.Fatalf("sandbox network = %#v", network)
	}
}

func TestClaudeBuildSpecWithoutElevatedCapabilitiesKeepsDefaults(t *testing.T) {
	p := testParams(t)
	p.Job.Kind = v1.JobKindBuild
	spec, err := NewClaude().BuildSpec(p)
	if err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	mcp := readJSONFile(t, filepath.Join(p.ConfigDir, "claude-mcp.json"))["mcpServers"].(map[string]any)
	if len(mcp) != 1 || mcp["platform"] == nil {
		t.Fatalf("MCP config = %#v", mcp)
	}
	allowed, _ := flagValues(spec.Args, "--allowedTools")
	if !reflect.DeepEqual(allowed, []string{"mcp__platform__*", "Bash"}) {
		t.Fatalf("allowed tools = %v", allowed)
	}
	network := readJSONFile(t, filepath.Join(p.ConfigDir, "claude-settings.json"))["sandbox"].(map[string]any)["network"].(map[string]any)
	if !reflect.DeepEqual(network["allowedDomains"], []any{}) {
		t.Fatalf("sandbox network = %#v", network)
	}
}

func TestClaudeBuildSpecIgnoresElevatedCapabilitiesForReadOnlyJobs(t *testing.T) {
	p := testParams(t)
	p.ReadOnly = true
	p.Elevated = testElevatedGrants()
	if _, err := NewClaude().BuildSpec(p); err != nil {
		t.Fatalf("BuildSpec: %v", err)
	}
	mcp := readJSONFile(t, filepath.Join(p.ConfigDir, "claude-mcp.json"))["mcpServers"].(map[string]any)
	if len(mcp) != 1 {
		t.Fatalf("read-only job received elevated MCP servers: %#v", mcp)
	}
	network := readJSONFile(t, filepath.Join(p.ConfigDir, "claude-settings.json"))["sandbox"].(map[string]any)["network"].(map[string]any)
	if !reflect.DeepEqual(network["allowedDomains"], []any{}) {
		t.Fatalf("read-only job received elevated hosts: %#v", network)
	}
}
