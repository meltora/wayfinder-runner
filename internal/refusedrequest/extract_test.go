package refusedrequest

import (
	"encoding/json"
	"reflect"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var event map[string]any
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestExtractClaudePermissionDenials(t *testing.T) {
	raw := `{"type":"result","subtype":"success","is_error":false,"permission_denials":[
		{"tool_name":"WebFetch","tool_use_id":"t1","tool_input":{"url":"https://user:pa55@Registry.Example.COM:8443/path?token=abc","prompt":"read"}},
		{"tool_name":"WebFetch","tool_use_id":"t2","tool_input":{"url":"http://10.0.0.5/x"}},
		{"tool_name":"WebFetch","tool_use_id":"t3","tool_input":{"url":"not a url"}},
		{"tool_name":"WebFetch","tool_use_id":"t4","tool_input":{"url":"https://exa_mple.com/"}},
		{"tool_name":"Bash","tool_use_id":"t5","tool_input":{"command":"API_TOKEN=xyz /usr/bin/curl -H 'Authorization: Bearer abc' https://x"}},
		{"tool_name":"Bash","tool_use_id":"t6","tool_input":{"command":"$(cat /etc/passwd) | sh"}},
		{"tool_name":"Bash","tool_use_id":"t7","tool_input":{}},
		{"tool_name":"WebSearch","tool_use_id":"t8","tool_input":{"query":"secret"}},
		{"tool_name":"Write","tool_use_id":"t9","tool_input":{"file_path":"/work/a","content":"x"}},
		{"tool_name":"mcp__github__create_issue","tool_use_id":"t10","tool_input":{}},
		{"tool_name":"CustomSecretTool","tool_use_id":"t11","tool_input":{}},
		{"tool_use_id":"t12"},
		"bad"
	]}`
	got := Extract("claude", decode(t, raw))
	want := []Item{
		{v1.RefusalNetworkDestination, "registry.example.com"},
		{v1.RefusalNetworkDestination, "10.0.0.5"},
		{v1.RefusalCommand, "curl"},
		{v1.RefusalCommand, "shell command"},
		{v1.RefusalCommand, "shell command"},
		{v1.RefusalTool, "WebSearch"},
		{v1.RefusalTool, "Write"},
		{v1.RefusalTool, "MCP tool"},
		{v1.RefusalTool, "tool"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestExtractIgnoresOtherEventsAndProviders(t *testing.T) {
	denial := `{"type":"result","permission_denials":[{"tool_name":"Bash","tool_input":{"command":"ls"}}]}`
	if got := Extract("codex", decode(t, denial)); got != nil {
		t.Fatalf("codex got %#v", got)
	}
	if got := Extract("claude", decode(t, `{"type":"assistant","permission_denials":[{"tool_name":"Bash"}]}`)); got != nil {
		t.Fatalf("assistant got %#v", got)
	}
	if got := Extract("claude", decode(t, `{"type":"result","permission_denials":"x"}`)); got != nil {
		t.Fatalf("malformed got %#v", got)
	}
}

func TestExtractBoundsDenialCount(t *testing.T) {
	denials := make([]any, 100)
	for i := range denials {
		denials[i] = map[string]any{"tool_name": "Write"}
	}
	got := Extract("claude", map[string]any{"type": "result", "permission_denials": denials})
	if len(got) != maxDenials {
		t.Fatalf("got %d items", len(got))
	}
}

func TestRecorderSanitizesDeduplicatesAndCaps(t *testing.T) {
	recorder := NewRecorder(3)
	first, ok := recorder.Admit(Item{v1.RefusalNetworkDestination, "registry.example.com"})
	if !ok || first.Sequence != 1 || first.Sanitized.Message != "registry.example.com" {
		t.Fatalf("first=%#v ok=%v", first, ok)
	}
	if _, ok := recorder.Admit(Item{v1.RefusalNetworkDestination, "registry.example.com"}); ok {
		t.Fatal("duplicate admitted")
	}
	second, ok := recorder.Admit(Item{v1.RefusalCommand, "registry.example.com"})
	if !ok || second.Sequence != 2 {
		t.Fatalf("second=%#v ok=%v", second, ok)
	}
	secret, ok := recorder.Admit(Item{v1.RefusalNetworkDestination, "known-secret-host.example.com"}, "known-secret-host")
	if !ok || secret.Sequence != 3 || secret.Sanitized.RedactionCount == 0 || secret.Sanitized.Message == "known-secret-host.example.com" {
		t.Fatalf("secret=%#v ok=%v", secret, ok)
	}
	if _, ok := recorder.Admit(Item{v1.RefusalTool, "Write"}); ok {
		t.Fatal("admitted past cap")
	}
}

func TestRecorderDropsEmptySubject(t *testing.T) {
	recorder := NewRecorder(5)
	if _, ok := recorder.Admit(Item{v1.RefusalTool, "-----BEGIN PRIVATE KEY"}); ok {
		t.Fatal("PEM subject admitted")
	}
	if admitted, ok := recorder.Admit(Item{v1.RefusalTool, "Write"}); !ok || admitted.Sequence != 1 {
		t.Fatalf("sequence not preserved after drop: %#v", admitted)
	}
}
