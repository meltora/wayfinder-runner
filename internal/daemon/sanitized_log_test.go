package daemon

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestConsumeOutputPublishesOnlyRunnerSanitizedAgentStatus(t *testing.T) {
	jr, events := testJobRunner(t)
	authority := v1.AgentRunAuthority{SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 1}
	jr.job.AgentRunAuthority = &authority
	secret := "ghp_abcdefghijklmnopqrstuvwxyz012345"
	jr.consumeOutput(bytes.NewBufferString(`{"type":"tool","status":"completed","output":"` + secret + `","prompt":"private"}` + "\n"))

	logs := events.byKind("sanitized_log")
	if len(logs) != 1 {
		t.Fatalf("sanitized logs=%v", logs)
	}
	raw, err := json.Marshal(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "private") {
		t.Fatalf("raw provider content crossed redaction boundary: %s", raw)
	}
	redaction, ok := logs[0]["redaction"].(map[string]any)
	if !ok || redaction["collector"] != "runner" || redaction["redaction_count"].(float64) < 2 {
		t.Fatalf("redaction=%#v", logs[0]["redaction"])
	}
}

func TestConsumeOutputDoesNotUpgradeLegacyJobEvidence(t *testing.T) {
	jr, events := testJobRunner(t)
	jr.consumeOutput(bytes.NewBufferString("{\"type\":\"status\"}\n"))
	if logs := events.byKind("sanitized_log"); len(logs) != 0 {
		t.Fatalf("legacy job emitted v2 evidence: %v", logs)
	}
}

func TestSanitizedLogRedactionHelpersCoverEveryControlledCategory(t *testing.T) {
	cases := map[string]v1.SanitizedLogRedactionCategory{
		"prompt_text":    v1.SanitizedLogPrompt,
		"access_token":   v1.SanitizedLogCredential,
		"environment":    v1.SanitizedLogEnvironment,
		"working_path":   v1.SanitizedLogFilesystemPath,
		"user_email":     v1.SanitizedLogPersonalData,
		"command_args":   v1.SanitizedLogCommandArgument,
		"provider_event": v1.SanitizedLogToolOutput,
	}
	for key, want := range cases {
		if got := classifySuppressedProviderField(key); got != want {
			t.Errorf("category %q=%q want %q", key, got, want)
		}
	}
	if value, category, truncated := sanitizeSelectedLogValue(42, 10, allowedAgentStatus); value != "" || category != "" || truncated {
		t.Fatalf("non-string value was sanitized: %q %v %v", value, category, truncated)
	}
	if value, category, truncated := sanitizeSelectedLogValue("  abc\ndef  ", 5, allowedAgentStatus); value != "" || category != v1.SanitizedLogToolOutput || !truncated {
		t.Fatalf("selected value=%q category=%v truncated=%v", value, category, truncated)
	}
	if value, category, truncated := sanitizeSelectedLogValue("success", 10, allowedAgentStatus); value != "success" || category != "" || truncated {
		t.Fatalf("controlled status=%q category=%v truncated=%v", value, category, truncated)
	}
	if !stringContainsSecret("ghp_abcdefghijklmnopqrstuvwxyz012345") || stringContainsSecret(42) {
		t.Fatal("secret classification did not respect value type and redaction policy")
	}
}

func TestSanitizedLogEnforcesLeaseAndEmissionCap(t *testing.T) {
	jr, events := testJobRunner(t)
	authority := v1.AgentRunAuthority{SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 1}
	jr.job.AgentRunAuthority = &authority
	jr.d.leases.Release(jr.job.AttemptID)
	jr.publishSanitizedAgentStatus(map[string]any{"type": "status"})
	if got := events.byKind("sanitized_log"); len(got) != 0 {
		t.Fatalf("invalid lease emitted sanitized evidence: %v", got)
	}
	if err := jr.d.leases.Acquire(authoritativeTestLease(jr.job.JobID, jr.job.AttemptID, jr.job.FencingToken, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	jr.sanitizedLogs = maxSanitizedLogsPerJob
	jr.publishSanitizedAgentStatus(map[string]any{"type": "status"})
	jr.publishSanitizedAgentStatus(map[string]any{"type": "ignored"})
	got := events.byKind("sanitized_log")
	if len(got) != 1 || got[0]["summary"] != "Additional agent status entries were suppressed." || got[0]["truncated"] != true {
		t.Fatalf("cap marker=%v", got)
	}
	redaction := got[0]["redaction"].(map[string]any)
	if !reflect.DeepEqual(redaction["categories"], []any{"tool_output"}) {
		t.Fatalf("cap marker categories=%v", redaction["categories"])
	}
}
