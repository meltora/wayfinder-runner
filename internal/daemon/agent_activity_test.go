package daemon

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

func activityJobRunner(t *testing.T, policy *v1.AgentActivityPolicy) (*jobRunner, *capturedEvents) {
	t.Helper()
	jr, events := testJobRunner(t)
	authority := v1.AgentRunAuthority{SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 1}
	jr.job.AgentRunAuthority = &authority
	jr.job.AgentActivity = policy
	jr.d.cfg.Provider = "claude"
	return jr, events
}

func decodeActivity(t *testing.T, events *capturedEvents) []v1.AgentActivityEvent {
	t.Helper()
	raw, err := json.Marshal(events.byKind(v1.EventKindAgentActivity))
	if err != nil {
		t.Fatal(err)
	}
	var decoded []v1.AgentActivityEvent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

const claudeActivityLine = `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"private reasoning"},{"type":"text","text":"Editing /home/dev/repo/main.go with key sk-abcdefghijklmnopqrst"},{"type":"tool_use","name":"Bash","input":{"command":"cat /etc/shadow"}}]}}`

func TestConsumeOutputPublishesSanitizedAgentActivity(t *testing.T) {
	jr, events := activityJobRunner(t, &v1.AgentActivityPolicy{SchemaVersion: 1, MaxEvents: 50, MaxEventsPerMinute: 30, MaxMessageBytes: 280})
	jr.consumeOutput(bytes.NewBufferString(claudeActivityLine + "\n"))

	activity := decodeActivity(t, events)
	if len(activity) != 2 {
		t.Fatalf("activity=%#v", activity)
	}
	raw, _ := json.Marshal(activity)
	for _, leaked := range []string{"sk-abcdefghijklmnopqrst", "/home/dev", "private reasoning", "/etc/shadow", "cat "} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("activity leaked %q: %s", leaked, raw)
		}
	}
	if activity[0].Sequence != 1 || activity[0].ActivityType != v1.AgentActivityAssistantMessage ||
		activity[0].Message != "Editing [redacted] with key [redacted]" || activity[0].Redaction.RedactionCount != 2 {
		t.Fatalf("first=%#v", activity[0])
	}
	if activity[1].Sequence != 2 || activity[1].ActivityType != v1.AgentActivityToolStep || activity[1].Message != "Using tool: Bash" {
		t.Fatalf("second=%#v", activity[1])
	}
	for _, event := range activity {
		if err := validation.AgentActivityEvent(event, 280); err != nil {
			t.Fatalf("invalid event published: %v", err)
		}
	}
}

func TestAgentActivityRequiresOptInAuthorityAndLease(t *testing.T) {
	jr, events := activityJobRunner(t, nil)
	jr.consumeOutput(bytes.NewBufferString(claudeActivityLine + "\n"))
	if got := events.byKind(v1.EventKindAgentActivity); len(got) != 0 {
		t.Fatalf("activity without policy: %v", got)
	}

	jr, events = activityJobRunner(t, &v1.AgentActivityPolicy{SchemaVersion: 1, MaxEvents: 50, MaxEventsPerMinute: 30, MaxMessageBytes: 280})
	jr.job.AgentRunAuthority = nil
	jr.consumeOutput(bytes.NewBufferString(claudeActivityLine + "\n"))
	if got := events.byKind(v1.EventKindAgentActivity); len(got) != 0 {
		t.Fatalf("activity without authority: %v", got)
	}

	jr, events = activityJobRunner(t, &v1.AgentActivityPolicy{SchemaVersion: 1, MaxEvents: 50, MaxEventsPerMinute: 30, MaxMessageBytes: 280})
	jr.d.leases.Release(jr.job.AttemptID)
	jr.publishAgentActivity(map[string]any{"type": "result", "is_error": false})
	if got := events.byKind(v1.EventKindAgentActivity); len(got) != 0 {
		t.Fatalf("activity without lease: %v", got)
	}
}

func TestAgentActivityStopsAfterTotalLimitNotice(t *testing.T) {
	jr, events := activityJobRunner(t, &v1.AgentActivityPolicy{SchemaVersion: 1, MaxEvents: 2, MaxEventsPerMinute: 30, MaxMessageBytes: 280})
	jr.consumeOutput(bytes.NewBufferString(claudeActivityLine + "\n" + claudeActivityLine + "\n"))
	activity := decodeActivity(t, events)
	if len(activity) != 2 || activity[1].ActivityType != v1.AgentActivityLimitReached || activity[1].Sequence != 2 {
		t.Fatalf("activity=%#v", activity)
	}
}

func TestAgentActivityTruncatesToPolicyBytes(t *testing.T) {
	jr, events := activityJobRunner(t, &v1.AgentActivityPolicy{SchemaVersion: 1, MaxEvents: 10, MaxEventsPerMinute: 10, MaxMessageBytes: 64})
	jr.publishAgentActivity(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "text", "text": strings.Repeat("word ", 40)},
	}}})
	activity := decodeActivity(t, events)
	if len(activity) != 1 || !activity[0].Truncated || len(activity[0].Message) > 64 {
		t.Fatalf("activity=%#v", activity)
	}
}
