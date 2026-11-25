package claudeoffline

import (
	"strings"
	"testing"
)

func TestToolResultReadsStringContent(t *testing.T) {
	line := `{"type":"user","message":{"content":[{"type":"tool_result","content":"preview ok","is_error":false}]}}`
	result, isError, ok := ToolResult(line)
	if !ok || isError || result != "preview ok" {
		t.Fatalf("ToolResult = %q %v %v", result, isError, ok)
	}
}

func TestToolResultJoinsTextParts(t *testing.T) {
	line := `{"type":"user","message":{"content":[{"type":"text","text":"x"},{"type":"tool_result","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"is_error":true}]}}`
	result, isError, ok := ToolResult(line)
	if !ok || !isError || result != "ab" {
		t.Fatalf("ToolResult = %q %v %v", result, isError, ok)
	}
}

func TestToolResultIgnoresOtherLines(t *testing.T) {
	for _, line := range []string{
		"",
		"not json",
		`{"type":"assistant","message":{"content":[{"type":"tool_result","content":"x"}]}}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"x"}]}}`,
	} {
		if _, _, ok := ToolResult(line); ok {
			t.Fatalf("ToolResult(%q) reported a tool result", line)
		}
	}
}

func TestMessagesAPIScriptAnswersWithTheConfiguredBashCommand(t *testing.T) {
	script := MessagesAPIScript()
	for _, want := range []string{"process.env.FAKE_BASH_COMMAND", "'127.0.0.1'", "8099", "name: 'Bash'", "end_turn", "process.env.FAKE_BASH_AWAIT_HOST", "'{{address}}'", "==awaiting-host:"} {
		if !strings.Contains(script, want) {
			t.Fatalf("messages API script lacks %q", want)
		}
	}
	if Port != 8099 || BaseURL != "http://127.0.0.1:8099" {
		t.Fatalf("Port = %d, BaseURL = %q", Port, BaseURL)
	}
}
