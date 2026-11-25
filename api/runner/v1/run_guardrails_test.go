package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRefusedRequestContentDigestMatchesCanonicalVector(t *testing.T) {
	event := RefusedRequestEvent{
		Sequence: 2, RefusalKind: RefusalNetworkDestination, Enforcer: RefusalEnforcerAgentRuntimePermissions,
		Subject: "registry.example.com",
		Redaction: SanitizedLogRedaction{
			SchemaVersion: 1, PolicyVersion: RefusedRequestRedactionPolicyVersion, Collector: "runner",
			Categories: []string{},
		},
	}
	if got := RefusedRequestContentDigest(event); got != refusedRequestDigestVector {
		t.Fatalf("digest=%s", got)
	}
}

const refusedRequestDigestVector = "4c5473d70331dbe97d49cadd288dbb82b00aaef2dc24c629bb9eba983056aba2"

func TestRunNetworkPolicyContentDigestMatchesCanonicalVectors(t *testing.T) {
	policyID := "egress-dev"
	restricted := RunNetworkPolicyEvent{
		Isolation: RunIsolationDocker, JobNetwork: RunJobNetworkRestricted, EgressPolicyID: &policyID,
		AgentSandboxNetwork: AgentSandboxNetworkDenyAll,
	}
	if got := RunNetworkPolicyContentDigest(restricted); got != runNetworkPolicyRestrictedVector {
		t.Fatalf("restricted digest=%s", got)
	}
	direct := RunNetworkPolicyEvent{
		Isolation: RunIsolationDirect, JobNetwork: RunJobNetworkHost, AgentSandboxNetwork: AgentSandboxNetworkNotRecorded,
	}
	if got := RunNetworkPolicyContentDigest(direct); got != runNetworkPolicyDirectVector {
		t.Fatalf("direct digest=%s", got)
	}
	previewOnly := RunNetworkPolicyEvent{
		Isolation: RunIsolationDocker, JobNetwork: RunJobNetworkRestricted, EgressPolicyID: &policyID,
		AgentSandboxNetwork: AgentSandboxNetworkPreviewOnly,
	}
	if got := RunNetworkPolicyContentDigest(previewOnly); got != runNetworkPolicyPreviewOnlyVector {
		t.Fatalf("preview_only digest=%s", got)
	}
}

const (
	runNetworkPolicyRestrictedVector  = "31311165b68cbeea638984d1b0d6f50b0788a164ea505397ef27556ccdab4c90"
	runNetworkPolicyDirectVector      = "1665652301e5ad3336d0d21997e28f8c3a97ec6868db9c78984f168cccfdd47a"
	runNetworkPolicyPreviewOnlyVector = "d86b639694cf42f4d300aaed8dfda0791ec0c7c486b219d854ae1db74a5a26a7"
)

func TestRunGuardrailEventWireFieldOrder(t *testing.T) {
	for _, tc := range []struct {
		value any
		order []string
	}{
		{RefusedRequestEvent{Kind: EventKindRefusedRequest}, []string{"kind", "manifest_kind", "lease_id", "job_id", "attempt_id",
			"manifest_id", "manifest_digest", "fencing_token", "observed_at", "agent_run_authority", "sequence", "refusal_kind",
			"enforcer", "subject", "truncated", "redaction", "content_digest_sha256"}},
		{RunNetworkPolicyEvent{Kind: EventKindRunNetworkPolicy}, []string{"kind", "manifest_kind", "lease_id", "job_id",
			"attempt_id", "manifest_id", "manifest_digest", "fencing_token", "observed_at", "agent_run_authority", "isolation",
			"job_network", "egress_policy_id", "agent_sandbox_network", "content_digest_sha256"}},
	} {
		raw, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatal(err)
		}
		last := -1
		for _, field := range tc.order {
			index := strings.Index(string(raw), `"`+field+`":`)
			if index <= last {
				t.Fatalf("field %s out of order in %s", field, raw)
			}
			last = index
		}
	}
}

func TestJobSpecAcceptsRunGuardrails(t *testing.T) {
	var job JobSpec
	raw := `{"job_id":"j","attempt_id":"a","project_id":"p","kind":"build","lease_id":"l","manifest_id":"m","manifest_digest":"d","workflow_revision":1,"fencing_token":1,"lease_expires_at":"2026-11-13T00:00:00Z","repositories":[],"ticket":{"key":"t","summary":"x","description":""},"run_guardrails":{"schema_version":1,"max_refused_requests":50}}`
	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		t.Fatal(err)
	}
	if job.RunGuardrails == nil || job.RunGuardrails.MaxRefusedRequests != 50 {
		t.Fatalf("run guardrails=%#v", job.RunGuardrails)
	}
}
