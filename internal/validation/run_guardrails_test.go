package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestRunGuardrailsPolicyBounds(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		if err := RunGuardrailsPolicy(v1.RunGuardrailsPolicy{SchemaVersion: version, MaxRefusedRequests: 50}); err != nil {
			t.Fatalf("valid policy version %d rejected: %v", version, err)
		}
	}
	for name, value := range map[string]v1.RunGuardrailsPolicy{
		"schema zero": {SchemaVersion: 0, MaxRefusedRequests: 50},
		"schema":      {SchemaVersion: 4, MaxRefusedRequests: 50},
		"zero":        {SchemaVersion: 1, MaxRefusedRequests: 0},
		"too many":    {SchemaVersion: 1, MaxRefusedRequests: 101},
	} {
		t.Run(name, func(t *testing.T) {
			if err := RunGuardrailsPolicy(value); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

func TestJobRejectsInvalidRunGuardrails(t *testing.T) {
	job := validJob()
	policy := v1.RunGuardrailsPolicy{SchemaVersion: 1, MaxRefusedRequests: 50}
	job.RunGuardrails = &policy
	if err := JobSpec(job); err != nil {
		t.Fatalf("valid job rejected: %v", err)
	}
	policy.MaxRefusedRequests = 0
	if err := JobSpec(job); err == nil {
		t.Fatal("job with invalid run guardrails accepted")
	}
}

func validRefusedRequest() v1.RefusedRequestEvent {
	value := v1.RefusedRequestEvent{
		Kind: v1.EventKindRefusedRequest, ManifestKind: v1.JobKindBuild, LeaseID: "lease-1", JobID: "job-1",
		AttemptID: "attempt-1", ManifestID: "manifest-1", ManifestDigest: strings.Repeat("a", 64),
		FencingToken: 7, ObservedAt: time.Now().UTC(), AgentRunAuthority: validAgentRunAuthority(),
		Sequence: 1, RefusalKind: v1.RefusalNetworkDestination, Enforcer: v1.RefusalEnforcerAgentRuntimePermissions,
		Subject:   "registry.example.com",
		Redaction: v1.SanitizedLogRedaction{SchemaVersion: 1, PolicyVersion: v1.RefusedRequestRedactionPolicyVersion, Collector: "runner", Categories: []string{}},
	}
	value.ContentDigestSHA256 = v1.RefusedRequestContentDigest(value)
	return value
}

func TestRefusedRequestEvent(t *testing.T) {
	if err := RefusedRequestEvent(validRefusedRequest()); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	for _, enforcer := range []v1.RefusalEnforcer{v1.RefusalEnforcerAgentSandbox, v1.RefusalEnforcerJobEgressRules} {
		candidate := validRefusedRequest()
		candidate.Enforcer = enforcer
		candidate.ContentDigestSHA256 = v1.RefusedRequestContentDigest(candidate)
		if err := RefusedRequestEvent(candidate); err != nil {
			t.Fatalf("valid %s event rejected: %v", enforcer, err)
		}
	}
	for name, mutate := range map[string]func(*v1.RefusedRequestEvent){
		"sandbox command": func(value *v1.RefusedRequestEvent) {
			value.Enforcer, value.RefusalKind, value.Subject = v1.RefusalEnforcerAgentSandbox, v1.RefusalCommand, "curl"
		},
		"egress tool": func(value *v1.RefusedRequestEvent) {
			value.Enforcer, value.RefusalKind, value.Subject = v1.RefusalEnforcerJobEgressRules, v1.RefusalTool, "Write"
		},
		"kind":          func(value *v1.RefusedRequestEvent) { value.Kind = "agent_activity" },
		"sequence zero": func(value *v1.RefusedRequestEvent) { value.Sequence = 0 },
		"sequence high": func(value *v1.RefusedRequestEvent) { value.Sequence = 101 },
		"refusal kind":  func(value *v1.RefusedRequestEvent) { value.RefusalKind = "file" },
		"enforcer":      func(value *v1.RefusedRequestEvent) { value.Enforcer = "prompt" },
		"blank subject": func(value *v1.RefusedRequestEvent) { value.Subject = "   " },
		"long subject":  func(value *v1.RefusedRequestEvent) { value.Subject = strings.Repeat("a", 161) },
		"control":       func(value *v1.RefusedRequestEvent) { value.Subject = "a\x00b" },
		"unsorted": func(value *v1.RefusedRequestEvent) {
			value.Redaction.Categories = []string{"personal_data", "credential"}
			value.Redaction.RedactionCount = 2
		},
		"digest":         func(value *v1.RefusedRequestEvent) { value.Subject = "other.example.com" },
		"missing digest": func(value *v1.RefusedRequestEvent) { value.ContentDigestSHA256 = "" },
		"lease":          func(value *v1.RefusedRequestEvent) { value.LeaseID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := validRefusedRequest()
			mutate(&candidate)
			if name != "digest" && name != "missing digest" {
				candidate.ContentDigestSHA256 = v1.RefusedRequestContentDigest(candidate)
			}
			if err := RefusedRequestEvent(candidate); err == nil {
				t.Fatal("invalid event accepted")
			}
		})
	}
}

func validRunNetworkPolicy(isolation v1.RunIsolation, network v1.RunJobNetwork, egress *string) v1.RunNetworkPolicyEvent {
	value := v1.RunNetworkPolicyEvent{
		Kind: v1.EventKindRunNetworkPolicy, ManifestKind: v1.JobKindPlan, LeaseID: "lease-1", JobID: "job-1",
		AttemptID: "attempt-1", ManifestID: "manifest-1", ManifestDigest: strings.Repeat("b", 64),
		FencingToken: 3, ObservedAt: time.Now().UTC(), AgentRunAuthority: validAgentRunAuthority(),
		Isolation: isolation, JobNetwork: network, EgressPolicyID: egress,
		AgentSandboxNetwork: v1.AgentSandboxNetworkDenyAll,
	}
	if isolation == v1.RunIsolationDirect {
		value.AgentSandboxNetwork = v1.AgentSandboxNetworkNotRecorded
	}
	value.ContentDigestSHA256 = v1.RunNetworkPolicyContentDigest(value)
	return value
}

func TestRunNetworkPolicyEvent(t *testing.T) {
	policyID := "egress-dev"
	bad := "bad id!"
	valid := []v1.RunNetworkPolicyEvent{
		validRunNetworkPolicy(v1.RunIsolationDocker, v1.RunJobNetworkNone, nil),
		validRunNetworkPolicy(v1.RunIsolationDocker, v1.RunJobNetworkRestricted, &policyID),
		validRunNetworkPolicy(v1.RunIsolationDirect, v1.RunJobNetworkHost, nil),
	}
	for _, value := range valid {
		if err := RunNetworkPolicyEvent(value); err != nil {
			t.Fatalf("valid policy rejected: %v", err)
		}
	}
	invalid := map[string]v1.RunNetworkPolicyEvent{
		"docker host":          validRunNetworkPolicy(v1.RunIsolationDocker, v1.RunJobNetworkHost, nil),
		"direct none":          validRunNetworkPolicy(v1.RunIsolationDirect, v1.RunJobNetworkNone, nil),
		"direct egress":        validRunNetworkPolicy(v1.RunIsolationDirect, v1.RunJobNetworkHost, &policyID),
		"none egress":          validRunNetworkPolicy(v1.RunIsolationDocker, v1.RunJobNetworkNone, &policyID),
		"restricted no egress": validRunNetworkPolicy(v1.RunIsolationDocker, v1.RunJobNetworkRestricted, nil),
		"restricted bad id":    validRunNetworkPolicy(v1.RunIsolationDocker, v1.RunJobNetworkRestricted, &bad),
		"isolation":            validRunNetworkPolicy("vm", v1.RunJobNetworkNone, nil),
	}
	sandbox := validRunNetworkPolicy(v1.RunIsolationDocker, v1.RunJobNetworkNone, nil)
	sandbox.AgentSandboxNetwork = "open"
	sandbox.ContentDigestSHA256 = v1.RunNetworkPolicyContentDigest(sandbox)
	invalid["sandbox"] = sandbox
	directDenyAll := validRunNetworkPolicy(v1.RunIsolationDirect, v1.RunJobNetworkHost, nil)
	directDenyAll.AgentSandboxNetwork = v1.AgentSandboxNetworkDenyAll
	directDenyAll.ContentDigestSHA256 = v1.RunNetworkPolicyContentDigest(directDenyAll)
	invalid["direct deny all"] = directDenyAll
	tampered := validRunNetworkPolicy(v1.RunIsolationDocker, v1.RunJobNetworkNone, nil)
	tampered.AgentSandboxNetwork = v1.AgentSandboxNetworkNotRecorded
	invalid["digest"] = tampered
	for name, value := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := RunNetworkPolicyEvent(value); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}
