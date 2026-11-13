package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentActivityContentDigestMatchesCanonicalVector(t *testing.T) {
	event := AgentActivityEvent{
		Sequence: 3, ActivityType: AgentActivityAssistantMessage,
		Message: "Updated the parser and ran the tests.", Truncated: false,
		Redaction: SanitizedLogRedaction{
			SchemaVersion: 1, PolicyVersion: AgentActivityRedactionPolicyVersion, Collector: "runner",
			RedactionCount: 1, Categories: []string{"filesystem_path"},
		},
	}
	got := AgentActivityContentDigest(event)
	if got != agentActivityDigestVector {
		t.Fatalf("digest=%s", got)
	}
}

const agentActivityDigestVector = "95d38f68063f7c4bf52e0a9e2cfc00b947a222ab34c195f025ec7f8005c298c4"

func TestAgentActivityContentDigestBindsMessageAndSequence(t *testing.T) {
	base := AgentActivityEvent{Sequence: 1, ActivityType: AgentActivityStatus, Message: "Run finished.",
		Redaction: SanitizedLogRedaction{SchemaVersion: 1, PolicyVersion: "p", Collector: "runner", Categories: []string{}}}
	changed := base
	changed.Sequence = 2
	if AgentActivityContentDigest(base) == AgentActivityContentDigest(changed) {
		t.Fatal("sequence not bound")
	}
	changed = base
	changed.Message = "Run failed."
	if AgentActivityContentDigest(base) == AgentActivityContentDigest(changed) {
		t.Fatal("message not bound")
	}
}

func TestAgentActivityEventWireFieldOrder(t *testing.T) {
	raw, err := json.Marshal(AgentActivityEvent{Kind: EventKindAgentActivity})
	if err != nil {
		t.Fatal(err)
	}
	order := []string{"kind", "manifest_kind", "lease_id", "job_id", "attempt_id", "manifest_id", "manifest_digest",
		"fencing_token", "observed_at", "agent_run_authority", "sequence", "activity_type", "message", "truncated",
		"redaction", "content_digest_sha256"}
	last := -1
	for _, field := range order {
		index := strings.Index(string(raw), `"`+field+`":`)
		if index <= last {
			t.Fatalf("field %s out of order in %s", field, raw)
		}
		last = index
	}
}

func TestJobSpecAcceptsAgentActivityPolicy(t *testing.T) {
	var job JobSpec
	raw := `{"job_id":"j","attempt_id":"a","project_id":"p","kind":"build","lease_id":"l","manifest_id":"m","manifest_digest":"d","workflow_revision":1,"fencing_token":1,"lease_expires_at":"2026-11-13T00:00:00Z","repositories":[],"ticket":{"key":"t","summary":"x","description":""},"agent_activity":{"schema_version":1,"max_events":200,"max_events_per_minute":30,"max_message_bytes":280}}`
	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		t.Fatal(err)
	}
	if job.AgentActivity == nil || job.AgentActivity.MaxEvents != 200 || job.AgentActivity.MaxMessageBytes != 280 {
		t.Fatalf("agent activity=%#v", job.AgentActivity)
	}
}
