package agentactivity

import (
	"encoding/json"
	"reflect"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func decodeEvent(t *testing.T, raw string) map[string]any {
	t.Helper()
	var event map[string]any
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestExtract(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		raw      string
		want     []Item
	}{
		{"claude text and tools", "claude",
			`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"secret plan"},{"type":"text","text":"I will update the parser."},{"type":"tool_use","name":"Bash","input":{"command":"rm -rf /"}},{"type":"tool_use","name":"mcp__other__x","input":{}},{"type":"tool_use","name":"Custom","input":{}}]}}`,
			[]Item{
				{v1.AgentActivityAssistantMessage, "I will update the parser."},
				{v1.AgentActivityToolStep, "Using tool: Bash"},
				{v1.AgentActivityToolStep, "Using an MCP tool"},
				{v1.AgentActivityToolStep, "Using a tool"},
			}},
		{"claude tool result ignored", "claude", `{"type":"user","message":{"content":[{"type":"tool_result","content":"output"}]}}`, nil},
		{"claude result success", "claude", `{"type":"result","subtype":"success","is_error":false,"result":"done text"}`,
			[]Item{{v1.AgentActivityStatus, "Agent turn finished."}}},
		{"claude result error", "claude", `{"type":"result","subtype":"error_max_turns","is_error":true}`,
			[]Item{{v1.AgentActivityStatus, "Agent turn ended with an error."}}},
		{"claude init", "claude", `{"type":"system","subtype":"init","cwd":"/work"}`,
			[]Item{{v1.AgentActivityStatus, "Agent session started."}}},
		{"codex message", "codex", `{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"Tests pass now."}}`,
			[]Item{{v1.AgentActivityAssistantMessage, "Tests pass now."}}},
		{"codex reasoning ignored", "codex", `{"type":"item.completed","item":{"type":"reasoning","text":"hidden"}}`, nil},
		{"codex command", "codex", `{"type":"item.completed","item":{"type":"command_execution","command":"cat .env","aggregated_output":"x"}}`,
			[]Item{{v1.AgentActivityToolStep, "Ran a shell command"}}},
		{"codex file change", "codex", `{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"/a"}]}}`,
			[]Item{{v1.AgentActivityToolStep, "Changed files"}}},
		{"codex mcp", "codex", `{"type":"item.completed","item":{"type":"mcp_tool_call","server":"s","tool":"t"}}`,
			[]Item{{v1.AgentActivityToolStep, "Using an MCP tool"}}},
		{"codex started ignored", "codex", `{"type":"item.started","item":{"type":"command_execution","command":"ls"}}`, nil},
		{"codex turn", "codex", `{"type":"turn.completed","usage":{}}`, []Item{{v1.AgentActivityStatus, "Agent turn finished."}}},
		{"codex turn failed", "codex", `{"type":"turn.failed","error":{"message":"boom"}}`, []Item{{v1.AgentActivityStatus, "Agent turn ended with an error."}}},
		{"opencode text", "opencode", `{"type":"text","part":{"type":"text","text":"Refactoring the module."}}`,
			[]Item{{v1.AgentActivityAssistantMessage, "Refactoring the module."}}},
		{"opencode tool", "opencode", `{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"ls"}}}}`,
			[]Item{{v1.AgentActivityToolStep, "Using tool: bash"}}},
		{"opencode unknown tool", "opencode", `{"type":"tool_use","part":{"type":"tool","tool":"secret_tool"}}`,
			[]Item{{v1.AgentActivityToolStep, "Using a tool"}}},
		{"opencode reasoning ignored", "opencode", `{"type":"reasoning","part":{"type":"reasoning","text":"hidden"}}`, nil},
		{"opencode step finish", "opencode", `{"type":"step_finish","part":{"type":"step-finish","reason":"stop"}}`,
			[]Item{{v1.AgentActivityStatus, "Agent step finished."}}},
		{"unknown provider", "other", `{"type":"assistant"}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Extract(tc.provider, decodeEvent(t, tc.raw))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v want %#v", got, tc.want)
			}
		})
	}
}
