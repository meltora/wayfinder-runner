package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

func guardrailsJobRunner(t *testing.T, policy *v1.RunGuardrailsPolicy) (*jobRunner, *capturedEvents) {
	t.Helper()
	jr, events := testJobRunner(t)
	authority := v1.AgentRunAuthority{SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 1}
	jr.job.AgentRunAuthority = &authority
	jr.job.RunGuardrails = policy
	jr.d.cfg.Provider = "claude"
	jr.d.cfg.Isolation = "docker"
	jr.d.cfg.JobNetwork = "none"
	return jr, events
}

func decodeRefusedRequests(t *testing.T, events *capturedEvents) []v1.RefusedRequestEvent {
	t.Helper()
	raw, err := json.Marshal(events.byKind(v1.EventKindRefusedRequest))
	if err != nil {
		t.Fatal(err)
	}
	var decoded []v1.RefusedRequestEvent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

const claudeDenialLine = `{"type":"result","subtype":"success","is_error":false,"permission_denials":[{"tool_name":"WebFetch","tool_use_id":"t1","tool_input":{"url":"https://registry.example.com/pkg?token=abc"}},{"tool_name":"Bash","tool_use_id":"t2","tool_input":{"command":"curl https://x"}},{"tool_name":"WebFetch","tool_use_id":"t3","tool_input":{"url":"https://registry.example.com/other"}}]}`

func TestRefusedRequestsPublishedFromClaudeResult(t *testing.T) {
	jr, events := guardrailsJobRunner(t, &v1.RunGuardrailsPolicy{SchemaVersion: 1, MaxRefusedRequests: 50})
	jr.consumeOutput(bytes.NewBufferString(claudeDenialLine + "\n"))
	refused := decodeRefusedRequests(t, events)
	if len(refused) != 2 {
		t.Fatalf("refused=%#v", refused)
	}
	if refused[0].Sequence != 1 || refused[0].RefusalKind != v1.RefusalNetworkDestination || refused[0].Subject != "registry.example.com" {
		t.Fatalf("first=%#v", refused[0])
	}
	if refused[1].Sequence != 2 || refused[1].RefusalKind != v1.RefusalCommand || refused[1].Subject != "curl" {
		t.Fatalf("second=%#v", refused[1])
	}
	for _, event := range refused {
		if event.Enforcer != v1.RefusalEnforcerAgentRuntimePermissions || event.AgentRunAuthority.AgentRunID != "agent-run-1" {
			t.Fatalf("event=%#v", event)
		}
		if err := validation.RefusedRequestEvent(event); err != nil {
			t.Fatalf("published invalid event: %v", err)
		}
	}
}

func TestRefusedRequestsRespectCapAcrossResults(t *testing.T) {
	jr, events := guardrailsJobRunner(t, &v1.RunGuardrailsPolicy{SchemaVersion: 1, MaxRefusedRequests: 1})
	jr.consumeOutput(bytes.NewBufferString(claudeDenialLine + "\n" + claudeDenialLine + "\n"))
	if refused := decodeRefusedRequests(t, events); len(refused) != 1 {
		t.Fatalf("refused=%#v", refused)
	}
}

func TestRefusedRequestsRedactRunnerKnownSecrets(t *testing.T) {
	jr, events := guardrailsJobRunner(t, &v1.RunGuardrailsPolicy{SchemaVersion: 1, MaxRefusedRequests: 10})
	jr.jobToken = "plainjobtokenvalue"
	jr.publishRefusedRequests(map[string]any{"type": "result", "permission_denials": []any{
		map[string]any{"tool_name": "WebFetch", "tool_input": map[string]any{"url": "https://plainjobtokenvalue.example.com/"}},
	}})
	refused := decodeRefusedRequests(t, events)
	if len(refused) != 1 || refused[0].Subject != "[redacted].example.com" || refused[0].Redaction.RedactionCount != 1 {
		t.Fatalf("refused=%#v", refused)
	}
}

func TestRefusedRequestsRequirePolicyAuthorityAndLease(t *testing.T) {
	denial := map[string]any{"type": "result", "permission_denials": []any{map[string]any{"tool_name": "Write"}}}
	jr, events := guardrailsJobRunner(t, nil)
	jr.publishRefusedRequests(denial)
	if got := events.byKind(v1.EventKindRefusedRequest); len(got) != 0 {
		t.Fatalf("refused without policy: %v", got)
	}

	jr, events = guardrailsJobRunner(t, &v1.RunGuardrailsPolicy{SchemaVersion: 1, MaxRefusedRequests: 10})
	jr.job.AgentRunAuthority = nil
	jr.publishRefusedRequests(denial)
	if got := events.byKind(v1.EventKindRefusedRequest); len(got) != 0 {
		t.Fatalf("refused without authority: %v", got)
	}

	jr, events = guardrailsJobRunner(t, &v1.RunGuardrailsPolicy{SchemaVersion: 1, MaxRefusedRequests: 10})
	jr.d.leases.Release(jr.job.AttemptID)
	jr.publishRefusedRequests(denial)
	if got := events.byKind(v1.EventKindRefusedRequest); len(got) != 0 {
		t.Fatalf("refused without lease: %v", got)
	}

	jr, events = guardrailsJobRunner(t, &v1.RunGuardrailsPolicy{SchemaVersion: 1, MaxRefusedRequests: 10})
	jr.d.cfg.Provider = "codex"
	jr.publishRefusedRequests(denial)
	if got := events.byKind(v1.EventKindRefusedRequest); len(got) != 0 {
		t.Fatalf("refused for codex: %v", got)
	}
}

func TestRunNetworkPolicyReflectsRunnerConfiguration(t *testing.T) {
	for name, tc := range map[string]struct {
		isolation, network, egress, provider string
		want                                 v1.RunNetworkPolicyEvent
	}{
		"docker none claude": {"docker", "none", "", "claude", v1.RunNetworkPolicyEvent{
			Isolation: v1.RunIsolationDocker, JobNetwork: v1.RunJobNetworkNone, AgentSandboxNetwork: v1.AgentSandboxNetworkDenyAll}},
		"docker restricted codex": {"docker", "restricted", "egress-dev", "codex", v1.RunNetworkPolicyEvent{
			Isolation: v1.RunIsolationDocker, JobNetwork: v1.RunJobNetworkRestricted, AgentSandboxNetwork: v1.AgentSandboxNetworkNotRecorded}},
		"direct claude": {"direct", "none", "", "claude", v1.RunNetworkPolicyEvent{
			Isolation: v1.RunIsolationDirect, JobNetwork: v1.RunJobNetworkHost, AgentSandboxNetwork: v1.AgentSandboxNetworkNotRecorded}},
	} {
		t.Run(name, func(t *testing.T) {
			jr, events := guardrailsJobRunner(t, &v1.RunGuardrailsPolicy{SchemaVersion: 1, MaxRefusedRequests: 10})
			jr.d.cfg.Isolation, jr.d.cfg.JobNetwork, jr.d.cfg.EgressPolicyID, jr.d.cfg.Provider = tc.isolation, tc.network, tc.egress, tc.provider
			jr.publishRunNetworkPolicy(context.Background())
			jr.publishRunNetworkPolicy(context.Background())
			published := events.byKind(v1.EventKindRunNetworkPolicy)
			if len(published) != 1 {
				t.Fatalf("published=%v", published)
			}
			raw, _ := json.Marshal(published[0])
			var event v1.RunNetworkPolicyEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.Isolation != tc.want.Isolation || event.JobNetwork != tc.want.JobNetwork || event.AgentSandboxNetwork != tc.want.AgentSandboxNetwork {
				t.Fatalf("event=%#v", event)
			}
			if (tc.egress == "") != (event.EgressPolicyID == nil) || (event.EgressPolicyID != nil && *event.EgressPolicyID != tc.egress) {
				t.Fatalf("egress=%v", event.EgressPolicyID)
			}
			if err := validation.RunNetworkPolicyEvent(event); err != nil {
				t.Fatalf("published invalid event: %v", err)
			}
		})
	}
}

func TestRunNetworkPolicyRequiresGuardrailsAndAuthority(t *testing.T) {
	jr, events := guardrailsJobRunner(t, nil)
	jr.publishRunNetworkPolicy(context.Background())
	jr2, events2 := guardrailsJobRunner(t, &v1.RunGuardrailsPolicy{SchemaVersion: 1, MaxRefusedRequests: 10})
	jr2.job.AgentRunAuthority = nil
	jr2.publishRunNetworkPolicy(context.Background())
	if len(events.byKind(v1.EventKindRunNetworkPolicy))+len(events2.byKind(v1.EventKindRunNetworkPolicy)) != 0 {
		t.Fatal("network policy published without guardrails or authority")
	}
}
