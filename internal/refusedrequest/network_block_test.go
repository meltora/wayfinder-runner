package refusedrequest

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func bashCall(id, command string) map[string]any {
	return map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": command}},
	}}}
}

func toolCall(id, name string) map[string]any {
	return map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}},
	}}}
}

func toolResult(id string, content any) map[string]any {
	return map[string]any{"type": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": id, "content": content, "is_error": true},
	}}}
}

func sandboxLayers() NetworkLayers {
	return NetworkLayers{AgentSandbox: true, JobEgress: true}
}

func observeAll(detector *NetworkBlockDetector, events ...map[string]any) []NetworkBlock {
	var blocks []NetworkBlock
	for _, event := range events {
		blocks = append(blocks, detector.Observe(event)...)
	}
	return blocks
}

func TestNetworkBlockDetectorRecordsSandboxProxyBlocks(t *testing.T) {
	detector := NewNetworkBlockDetector("claude", sandboxLayers())
	got := observeAll(detector,
		bashCall("t1", "curl -sS https://user:pw@Registry.Example.COM:8443/pkg?token=abc"),
		toolResult("t1", "curl: (56) CONNECT tunnel failed, response 403\nConnection blocked by network allowlist"),
		bashCall("t2", "npm install"),
		toolResult("t2", []any{map[string]any{"type": "text", "text": "npm error 403 Forbidden\nX-Proxy-Error: blocked-by-allowlist"}}),
	)
	want := []NetworkBlock{
		{Enforcer: v1.RefusalEnforcerAgentSandbox, Subject: "registry.example.com"},
		{Enforcer: v1.RefusalEnforcerAgentSandbox, Subject: unidentifiedDestinationSubject},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestNetworkBlockDetectorAttributesConnectionFailuresToTheEnforcingLayer(t *testing.T) {
	detector := NewNetworkBlockDetector("claude", sandboxLayers())
	got := observeAll(detector,
		bashCall("t1", "git clone https://github.com/acme/repo"),
		toolResult("t1", "fatal: unable to access 'https://github.com/acme/repo/': Could not resolve host: github.com"),
		bashCall("t2", "node fetch.js"),
		toolResult("t2", "Error: getaddrinfo ENOTFOUND api.Example.org."),
		bashCall("t3", "curl http://10.0.0.5/"),
		toolResult("t3", "curl: (7) Failed to connect to 10.0.0.5 port 80 after 3 ms: Couldn't connect to server"),
		bashCall("t4", "pip install requests"),
		toolResult("t4", "Temporary failure in name resolution"),
	)
	want := []NetworkBlock{
		{Enforcer: v1.RefusalEnforcerAgentSandbox, Subject: "github.com"},
		{Enforcer: v1.RefusalEnforcerAgentSandbox, Subject: "api.example.org"},
		{Enforcer: v1.RefusalEnforcerAgentSandbox, Subject: "10.0.0.5"},
		{Enforcer: v1.RefusalEnforcerAgentSandbox, Subject: unidentifiedDestinationSubject},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestNetworkBlockDetectorUsesJobEgressWithoutTheAgentSandbox(t *testing.T) {
	detector := NewNetworkBlockDetector("claude", NetworkLayers{JobEgress: true})
	got := observeAll(detector,
		bashCall("t1", "curl https://registry.example.com"),
		toolResult("t1", "curl: (6) Could not resolve host: registry.example.com"),
		bashCall("t2", "curl https://other.example.com"),
		toolResult("t2", "Connection blocked by network allowlist"),
	)
	want := []NetworkBlock{{Enforcer: v1.RefusalEnforcerJobEgressRules, Subject: "registry.example.com"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestNetworkBlockDetectorHandlesApprovedHosts(t *testing.T) {
	unverified := NewNetworkBlockDetector("claude", NetworkLayers{
		AgentSandbox: true, JobEgress: true, ApprovedHosts: []string{"registry.example.com"},
	})
	got := observeAll(unverified,
		bashCall("t1", "curl https://registry.example.com"),
		toolResult("t1", "curl: (6) Could not resolve host: registry.example.com"),
	)
	want := []NetworkBlock{{Enforcer: v1.RefusalEnforcerJobEgressRules, Subject: "registry.example.com"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unverified got %#v", got)
	}

	verified := NewNetworkBlockDetector("claude", NetworkLayers{
		AgentSandbox: true, JobEgress: true, EgressRulesVerified: true, ApprovedHosts: []string{"registry.example.com"},
	})
	if got := observeAll(verified,
		bashCall("t1", "curl https://registry.example.com"),
		toolResult("t1", "curl: (6) Could not resolve host: registry.example.com"),
	); len(got) != 0 {
		t.Fatalf("verified approved host got %#v", got)
	}
}

func TestNetworkBlockDetectorIgnoresUnrelatedOutput(t *testing.T) {
	detector := NewNetworkBlockDetector("claude", sandboxLayers())
	got := observeAll(detector,
		bashCall("t1", "curl http://localhost:3000"),
		toolResult("t1", "curl: (7) Failed to connect to localhost port 3000: Connection refused"),
		bashCall("t2", "curl http://127.0.0.1:3000"),
		toolResult("t2", "curl: (6) Could not resolve host: 127.0.0.1"),
		bashCall("t3", "pnpm test"),
		toolResult("t3", "connect ECONNREFUSED 10.1.2.3:5432"),
		bashCall("t4", "ls"),
		toolResult("t4", "README.md"),
		toolCall("t5", "Read"),
		toolResult("t5", "Could not resolve host: docs.example.com"),
		toolResult("t9", "Could not resolve host: unknown.example.com"),
		map[string]any{"type": "result", "result": "Could not resolve host: x.example.com"},
	)
	if len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}

func TestNetworkBlockDetectorReportsEachToolResultOnce(t *testing.T) {
	detector := NewNetworkBlockDetector("claude", sandboxLayers())
	got := observeAll(detector,
		bashCall("t1", "curl https://a.example.com https://b.example.com"),
		toolResult("t1", "Could not resolve host: a.example.com\nCould not resolve host: b.example.com"),
		toolResult("t1", "Could not resolve host: c.example.com"),
	)
	want := []NetworkBlock{{Enforcer: v1.RefusalEnforcerAgentSandbox, Subject: "a.example.com"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
}

func TestNetworkBlockDetectorRequiresClaudeAndAnEnforcingLayer(t *testing.T) {
	if detector := NewNetworkBlockDetector("codex", sandboxLayers()); detector != nil {
		t.Fatal("codex detector created")
	}
	if detector := NewNetworkBlockDetector("claude", NetworkLayers{}); detector != nil {
		t.Fatal("detector created without an enforcing layer")
	}
	var detector *NetworkBlockDetector
	if got := detector.Observe(toolResult("t1", "Could not resolve host: a.example.com")); got != nil {
		t.Fatalf("nil detector got %#v", got)
	}
}

func TestNetworkBlockDetectorBoundsTrackedCallsAndScannedText(t *testing.T) {
	detector := NewNetworkBlockDetector("claude", sandboxLayers())
	for i := 0; i < maxTrackedCommands+10; i++ {
		detector.Observe(bashCall(fmt.Sprintf("t%d", i), "ls"))
	}
	if len(detector.commands) > maxTrackedCommands {
		t.Fatalf("tracked %d commands", len(detector.commands))
	}
	detector = NewNetworkBlockDetector("claude", sandboxLayers())
	detector.Observe(bashCall("t1", "make"))
	late := strings.Repeat("a", maxScannedResultBytes) + "Could not resolve host: late.example.com"
	if got := detector.Observe(toolResult("t1", late)); len(got) != 0 {
		t.Fatalf("scanned past bound: %#v", got)
	}
}

func TestRecorderKeepsEnforcersApart(t *testing.T) {
	recorder := NewRecorder(5)
	first, ok := recorder.Admit(Item{v1.RefusalNetworkDestination, "registry.example.com"})
	if !ok || first.Enforcer != v1.RefusalEnforcerAgentRuntimePermissions {
		t.Fatalf("first=%#v", first)
	}
	block, ok := recorder.AdmitNetworkBlock(NetworkBlock{Enforcer: v1.RefusalEnforcerAgentSandbox, Subject: "registry.example.com"})
	if !ok || block.Sequence != 2 || block.Kind != v1.RefusalNetworkDestination || block.Enforcer != v1.RefusalEnforcerAgentSandbox {
		t.Fatalf("block=%#v ok=%v", block, ok)
	}
	if _, ok := recorder.AdmitNetworkBlock(NetworkBlock{Enforcer: v1.RefusalEnforcerAgentSandbox, Subject: "registry.example.com"}); ok {
		t.Fatal("duplicate block admitted")
	}
}
