package daemon

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/toolsummary"
	"github.com/example/app-runner/internal/validation"
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
	jr.publishSanitizedAgentStatus(map[string]any{"type": "status"}, nil)
	jr.publishSanitizedAgentStatus(map[string]any{"type": "assistant"}, &toolsummary.Summary{Text: "Tool call: Bash", Tools: "Bash"})
	if got := events.byKind("sanitized_log"); len(got) != 0 {
		t.Fatalf("invalid lease emitted sanitized evidence: %v", got)
	}
	if err := jr.d.leases.Acquire(authoritativeTestLease(jr.job.JobID, jr.job.AttemptID, jr.job.FencingToken, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	jr.sanitizedLogs = maxSanitizedLogsPerJob
	jr.publishSanitizedAgentStatus(map[string]any{"type": "status"}, nil)
	jr.publishSanitizedAgentStatus(map[string]any{"type": "ignored"}, nil)
	got := events.byKind("sanitized_log")
	if len(got) != 1 || got[0]["summary"] != "Additional agent status entries were suppressed." || got[0]["truncated"] != true {
		t.Fatalf("cap marker=%v", got)
	}
	redaction := got[0]["redaction"].(map[string]any)
	if !reflect.DeepEqual(redaction["categories"], []any{"tool_output"}) {
		t.Fatalf("cap marker categories=%v", redaction["categories"])
	}
}

const (
	claudeToolUseLine    = `{"type":"assistant","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_offline_1","name":"Bash","input":{"command":"echo sandbox-bash-ok; cat /home/runner/.claude/canary.txt","description":"sandbox probe"}}],"stop_reason":null},"parent_tool_use_id":null,"session_id":"56e883c6-40bb-451f-9f7d-dbfddaf29769","uuid":"120afbc7-be7e-4e6c-90ef-fb6ea967c2f4"}`
	claudeToolResultLine = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_offline_1","type":"tool_result","content":"sandbox-bash-ok\nTOPSECRET","is_error":false}]},"parent_tool_use_id":null,"session_id":"56e883c6-40bb-451f-9f7d-dbfddaf29769","uuid":"83891475-191d-4b71-a8b2-a5e9bdee0231","tool_use_result":{"stdout":"sandbox-bash-ok\nTOPSECRET","stderr":""}}`
	claudeTextLine       = `{"type":"assistant","message":{"id":"msg_2","type":"message","role":"assistant","content":[{"type":"text","text":"done"}]},"session_id":"56e883c6-40bb-451f-9f7d-dbfddaf29769","uuid":"896d3f91-4bd9-49a0-9c08-0f64d51a9749"}`
)

func toolSummaryJobRunner(t *testing.T, providerName string) (*jobRunner, *capturedEvents, *bytes.Buffer) {
	t.Helper()
	jr, events := testJobRunner(t)
	authority := v1.AgentRunAuthority{SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 1}
	jr.job.AgentRunAuthority = &authority
	jr.d.cfg.Provider = providerName
	logs := &bytes.Buffer{}
	jr.logger = slog.New(slog.NewTextHandler(logs, nil))
	return jr, events, logs
}

func toolSummaryValidatedEvents(t *testing.T, events *capturedEvents) []v1.SanitizedLogEvent {
	t.Helper()
	var out []v1.SanitizedLogEvent
	for _, raw := range events.byKind("sanitized_log") {
		encoded, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		var event v1.SanitizedLogEvent
		if err := json.Unmarshal(encoded, &event); err != nil {
			t.Fatal(err)
		}
		if err := validation.SanitizedLogEvent(event); err != nil {
			t.Fatalf("emitted event does not validate: %v: %s", err, encoded)
		}
		if event.ContentDigestSHA256 != v1.SanitizedLogContentDigest(event) {
			t.Fatalf("digest mismatch: %s", encoded)
		}
		if event.Redaction.PolicyVersion != "runner-redaction-3" {
			t.Fatalf("policy version=%q", event.Redaction.PolicyVersion)
		}
		out = append(out, event)
	}
	return out
}

func TestClaudeToolCallAndResultBecomeToolSummaries(t *testing.T) {
	jr, events, logs := toolSummaryJobRunner(t, "claude")
	jr.consumeOutput(bytes.NewBufferString(claudeToolUseLine + "\n" + claudeToolResultLine + "\n" + claudeTextLine + "\n"))

	got := toolSummaryValidatedEvents(t, events)
	if len(got) != 3 {
		t.Fatalf("events=%#v", got)
	}
	call, result, text := got[0], got[1], got[2]
	if call.Source != v1.SanitizedLogToolSummary || call.Summary != "Tool call: Bash" || call.Detail != "" || call.Truncated {
		t.Fatalf("call=%#v", call)
	}
	if !reflect.DeepEqual(call.Redaction.Categories, []string{"command_argument", "tool_output"}) || call.Redaction.RedactionCount != 4+1 {
		t.Fatalf("call redaction=%#v", call.Redaction)
	}
	if result.Source != v1.SanitizedLogToolSummary || result.Summary != "Tool result: Bash succeeded" || result.Detail != "" {
		t.Fatalf("result=%#v", result)
	}
	if !reflect.DeepEqual(result.Redaction.Categories, []string{"tool_output"}) || result.Redaction.RedactionCount != 5+1 {
		t.Fatalf("result redaction=%#v", result.Redaction)
	}
	if text.Source != v1.SanitizedLogAgentStatus || text.Summary != "Agent status: assistant" {
		t.Fatalf("text=%#v", text)
	}
	raw, _ := json.Marshal(events.byKind("sanitized_log"))
	for _, forbidden := range []string{"toolu_", "canary", "TOPSECRET", "sandbox-bash-ok", "/home/runner", "sandbox probe"} {
		if strings.Contains(string(raw), forbidden) || strings.Contains(logs.String(), forbidden) {
			t.Fatalf("%q crossed the boundary: %s\n%s", forbidden, raw, logs.String())
		}
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	var agentLines []string
	for _, line := range lines {
		if strings.Contains(line, `msg="agent event"`) {
			agentLines = append(agentLines, line)
		}
	}
	if len(agentLines) != 3 ||
		!strings.HasSuffix(agentLines[0], `msg="agent event" type=assistant status=[suppressed] tools=Bash`) ||
		!strings.HasSuffix(agentLines[1], `msg="agent event" type=user status=[suppressed] tools=Bash:succeeded`) ||
		!strings.HasSuffix(agentLines[2], `msg="agent event" type=assistant status=[suppressed]`) {
		t.Fatalf("agent event log lines=%q", agentLines)
	}
}

func TestToolSummaryLogLineAndEventForSeveralAndHostileBlocks(t *testing.T) {
	jr, events, logs := toolSummaryJobRunner(t, "claude")
	token := "ghp_" + strings.Repeat("Z", 36)
	key := "-----BEGIN RSA PRIVATE KEY-----"
	nameToken := "ghp_" + strings.Repeat("q", 40)
	blocks := []any{}
	names := []string{"Bash", "Bash; cat /etc/passwd", nameToken, strings.Repeat("x", 200), "Bash\nRead", "mcp__other__tool", "Read", "Edit", "Write"}
	for index, name := range names {
		blocks = append(blocks, map[string]any{"type": "tool_use", "id": "toolu_h" + strconv.Itoa(index), "name": name, "input": map[string]any{"command": "cat /home/omar/.ssh/id_rsa " + token, "key": key}})
	}
	callLine, _ := json.Marshal(map[string]any{"type": "assistant", "session_id": "s", "message": map[string]any{"content": blocks}})
	resultLine, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "toolu_h0", "is_error": true, "content": "/home/omar/.ssh/id_rsa " + token + key},
		map[string]any{"type": "tool_result", "tool_use_id": "toolu_h6", "is_error": "no", "content": []any{map[string]any{"type": "text", "text": token}}},
		map[string]any{"type": "tool_result", "tool_use_id": "toolu_h1", "is_error": false},
	}}})
	jr.consumeOutput(bytes.NewBufferString(string(callLine) + "\n" + string(resultLine) + "\n"))

	got := toolSummaryValidatedEvents(t, events)
	if len(got) != 2 {
		t.Fatalf("events=%#v", got)
	}
	if got[0].Summary != "Tool calls: Bash, unlisted tool, unlisted tool, unlisted tool, unlisted tool, unlisted tool, Read, Edit (+1 more)" || !got[0].Truncated {
		t.Fatalf("call=%#v", got[0])
	}
	if got[0].Redaction.RedactionCount != 2+9+5 || !reflect.DeepEqual(got[0].Redaction.Categories, []string{"command_argument", "tool_output"}) {
		t.Fatalf("call redaction=%#v", got[0].Redaction)
	}
	if got[1].Summary != "Tool results: Bash failed, Read returned, unlisted tool succeeded" || got[1].Truncated {
		t.Fatalf("result=%#v", got[1])
	}
	if got[1].Redaction.RedactionCount != 1+2 || !reflect.DeepEqual(got[1].Redaction.Categories, []string{"tool_output"}) {
		t.Fatalf("result redaction=%#v", got[1].Redaction)
	}
	allowed := regexp.MustCompile(`^Tool (call|calls|result|results): [A-Za-z0-9_,+() ]+$`)
	for _, event := range got {
		if !allowed.MatchString(event.Summary) {
			t.Fatalf("summary characters=%q", event.Summary)
		}
	}
	raw, _ := json.Marshal(events.byKind("sanitized_log"))
	for _, forbidden := range []string{token, nameToken, key, "BEGIN", "passwd", "/home/omar", "id_rsa", "toolu_", "mcp__other", strings.Repeat("x", 200), `\n`} {
		if strings.Contains(string(raw), forbidden) || strings.Contains(logs.String(), forbidden) {
			t.Fatalf("%q crossed the boundary: %s\n%s", forbidden, raw, logs.String())
		}
	}
	if !strings.Contains(logs.String(), `tools="Bash,unlisted tool,unlisted tool,unlisted tool,unlisted tool,unlisted tool,Read,Edit"`) ||
		!strings.Contains(logs.String(), `tools="Bash:failed,Read:returned,unlisted tool:succeeded"`) {
		t.Fatalf("log=%s", logs.String())
	}
}

func TestEventsWithoutToolBlocksKeepTheAgentStatusEntry(t *testing.T) {
	lines := []string{
		claudeTextLine,
		`{"type":"user","message":{"content":[{"type":"text","text":"hi"}]},"session_id":"s"}`,
		`{"type":"system","subtype":"init","status":"running","cwd":"/home/runner/work","tools":["Bash"]}`,
		`{"type":"result","status":"success","result":"done"}`,
	}
	for _, line := range lines {
		claudeRunner, claudeEvents, _ := toolSummaryJobRunner(t, "claude")
		legacyRunner, legacyEvents, _ := toolSummaryJobRunner(t, "")
		claudeRunner.consumeOutput(bytes.NewBufferString(line + "\n"))
		legacyRunner.consumeOutput(bytes.NewBufferString(line + "\n"))
		claudeGot := toolSummaryValidatedEvents(t, claudeEvents)
		legacyGot := toolSummaryValidatedEvents(t, legacyEvents)
		if len(claudeGot) != 1 || len(legacyGot) != 1 || claudeGot[0].Source != v1.SanitizedLogAgentStatus {
			t.Fatalf("line %s: %#v %#v", line, claudeGot, legacyGot)
		}
		claudeGot[0].ObservedAt, legacyGot[0].ObservedAt = time.Time{}, time.Time{}
		if !reflect.DeepEqual(claudeGot[0], legacyGot[0]) {
			t.Fatalf("line %s: claude=%#v legacy=%#v", line, claudeGot[0], legacyGot[0])
		}
	}
	jr, events, _ := toolSummaryJobRunner(t, "claude")
	jr.consumeOutput(bytes.NewBufferString(`{"type":"result","status":"success","result":"done"}` + "\n"))
	got := toolSummaryValidatedEvents(t, events)
	want := v1.SanitizedLogEvent{Source: v1.SanitizedLogAgentStatus, Summary: "Agent status: result", Detail: "Status: success"}
	if got[0].Source != want.Source || got[0].Summary != want.Summary || got[0].Detail != want.Detail ||
		got[0].Redaction.RedactionCount != 1 || !reflect.DeepEqual(got[0].Redaction.Categories, []string{"tool_output"}) {
		t.Fatalf("agent status entry=%#v", got[0])
	}
}

func TestOtherProvidersKeepAgentStatusForToolShapedEvents(t *testing.T) {
	for _, providerName := range []string{"codex", "opencode"} {
		for _, line := range []string{claudeToolUseLine, claudeToolResultLine, claudeTextLine, `{"type":"tool_use","name":"Bash","status":"completed"}`} {
			jr, events, logs := toolSummaryJobRunner(t, providerName)
			jr.consumeOutput(bytes.NewBufferString(line + "\n"))
			got := toolSummaryValidatedEvents(t, events)
			if len(got) != 1 || got[0].Source != v1.SanitizedLogAgentStatus || !strings.HasPrefix(got[0].Summary, "Agent status: ") {
				t.Fatalf("%s %s: %#v", providerName, line, got)
			}
			if strings.Contains(logs.String(), "tools=") || jr.toolTracker != nil {
				t.Fatalf("%s tracked tools: %s", providerName, logs.String())
			}
		}
	}
}

func TestResumedSessionStartsWithAnEmptyToolTracker(t *testing.T) {
	jr, events, _ := toolSummaryJobRunner(t, "claude")
	jr.consumeOutput(bytes.NewBufferString(claudeToolUseLine + "\n"))
	jr.consumeOutput(bytes.NewBufferString(claudeToolResultLine + "\n"))
	got := toolSummaryValidatedEvents(t, events)
	if len(got) != 2 || got[1].Summary != "Tool result: unknown tool succeeded" {
		t.Fatalf("events=%#v", got)
	}
}

func TestToolSummaryCountsAgainstTheSanitizedLogCap(t *testing.T) {
	jr, events, logs := toolSummaryJobRunner(t, "claude")
	jr.sanitizedLogs = maxSanitizedLogsPerJob - 1
	jr.consumeOutput(bytes.NewBufferString(claudeToolUseLine + "\n" + claudeToolUseLine + "\n" + claudeToolResultLine + "\n"))
	got := toolSummaryValidatedEvents(t, events)
	if len(got) != 2 || got[0].Summary != "Tool call: Bash" || got[1].Summary != "Additional agent status entries were suppressed." ||
		got[1].Source != v1.SanitizedLogAgentStatus || !reflect.DeepEqual(got[1].Redaction.Categories, []string{"tool_output"}) {
		t.Fatalf("events=%#v", got)
	}
	if jr.sanitizedLogs != maxSanitizedLogsPerJob+2 {
		t.Fatalf("sanitized log sequence=%d", jr.sanitizedLogs)
	}
	if !strings.Contains(logs.String(), "tools=Bash:succeeded") {
		t.Fatalf("suppressed lines still keep the local log: %s", logs.String())
	}
}

func TestToolSummaryWaitsForStartEvidence(t *testing.T) {
	jr, events, _ := toolSummaryJobRunner(t, "claude")
	jr.startEvidenceReady = make(chan struct{})
	jr.consumeOutput(bytes.NewBufferString(claudeToolUseLine + "\n"))
	if got := events.byKind("sanitized_log"); len(got) != 0 || jr.sanitizedLogs != 0 {
		t.Fatalf("tool summary emitted before start evidence: %v", got)
	}
}
