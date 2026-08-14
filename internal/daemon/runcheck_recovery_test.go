package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/lease"
)

func TestRunCheckRetryAfterEventPublishDoesNotReexecute(t *testing.T) {
	jr, cap := runCheckFixture(t)
	counter := filepath.Join(jr.repositories[0].WorkDir, ".crash-count")
	jr.d.cfg.Checks["counting"] = config.CheckConfig{
		Command: "/bin/sh", Args: []string{"-c", "echo run >> \"" + counter + "\""},
	}
	jr.job.RequiredChecks = []string{"counting"}
	executions := func() int {
		data, err := os.ReadFile(counter)
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(data), "run")
	}
	ctx := context.Background()
	scopeTool := "platform_run_check:counting"
	state, _, err := jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, scopeTool, "rc-crash")
	if err != nil || state != journal.ToolCallStarted {
		t.Fatalf("begin tool call = %v, %v", state, err)
	}
	args := map[string]any{"check_id": "counting", "idempotency_key": "rc-crash"}
	first, err := jr.executeRunCheck(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if first.(map[string]any)["result"] != "passed" {
		t.Fatalf("first = %v", first)
	}
	eventID := runCheckEventID(jr.job.AttemptID, "counting", "rc-crash")
	exists, err := jr.d.journal.HasRecord(ctx, eventID)
	if err != nil || !exists {
		t.Fatalf("iterative evidence was not durably recorded under its stable ID: %v, %v", exists, err)
	}
	resetRunCheckMemo(jr)
	reopenRunCheckJournal(t, jr)
	recovered, err := call(t, jr, "platform_run_check", `{"check_id":"counting","idempotency_key":"rc-crash"}`)
	if err != nil {
		t.Fatalf("crash-window retry must recover the recorded result: %v", err)
	}
	recoveredMap := recovered.(map[string]any)
	if !reflect.DeepEqual(recoveredMap, first) {
		t.Fatalf("recovered = %v, want the original result %v", recoveredMap, first)
	}
	if executions() != 1 {
		t.Fatalf("crash-window retry re-executed the check: %d", executions())
	}
	if got := len(cap.byKind("test_results")); got != 1 {
		t.Fatalf("crash-window retry created a second logical event: %d", got)
	}
	state, stored, err := jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, scopeTool, "rc-crash")
	if err != nil || state != journal.ToolCallCompleted {
		t.Fatalf("pending tool call was not completed by reconciliation: %v, %v", state, err)
	}
	var storedResult map[string]any
	if err := json.Unmarshal(stored, &storedResult); err != nil {
		t.Fatalf("stored result is not decodable: %v", err)
	}
	if storedResult["result"] != "passed" || storedResult["check_id"] != "counting" {
		t.Fatalf("stored result = %v", storedResult)
	}
	direct, err := jr.executeRunCheck(ctx, args)
	if err != nil {
		t.Fatalf("direct execution with durable evidence must recover: %v", err)
	}
	if !reflect.DeepEqual(direct.(map[string]any), first) {
		t.Fatalf("direct recovery = %v, want %v", direct, first)
	}
	if executions() != 1 {
		t.Fatalf("direct recovery re-executed the check: %d", executions())
	}
	resetRunCheckMemo(jr)
	replay, err := call(t, jr, "platform_run_check", `{"check_id":"counting","idempotency_key":"rc-crash"}`)
	if err != nil {
		t.Fatalf("replay of a completed call: %v", err)
	}
	replayMap := replay.(map[string]any)
	for _, field := range []string{"check_id", "repository_id", "result", "commit_sha", "worktree_dirty"} {
		if replayMap[field] != first.(map[string]any)[field] {
			t.Fatalf("replay field %s = %v, want %v", field, replayMap[field], first.(map[string]any)[field])
		}
	}
	if executions() != 1 {
		t.Fatalf("replay re-executed the check: %d", executions())
	}
	if got := len(cap.byKind("test_results")); got != 1 {
		t.Fatalf("replay created a second logical event: %d", got)
	}
	recs, err := jr.d.journal.Unacked(ctx, journal.KindEvent, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].ID != eventID {
		t.Fatalf("journal holds %d event records, want exactly the stable event %s", len(recs), eventID)
	}
}

func TestRunCheckRecoveryAfterEventAck(t *testing.T) {
	jr, cap := runCheckFixture(t)
	counter := filepath.Join(jr.repositories[0].WorkDir, ".ack-count")
	jr.d.cfg.Checks["counting"] = config.CheckConfig{
		Command: "/bin/sh", Args: []string{"-c", "echo run >> \"" + counter + "\""},
	}
	jr.job.RequiredChecks = []string{"counting"}
	ctx := context.Background()
	scopeTool := "platform_run_check:counting"
	if state, _, err := jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, scopeTool, "rc-ack"); err != nil || state != journal.ToolCallStarted {
		t.Fatalf("begin tool call = %v, %v", state, err)
	}
	first, err := jr.executeRunCheck(ctx, map[string]any{"check_id": "counting", "idempotency_key": "rc-ack"})
	if err != nil {
		t.Fatal(err)
	}
	eventID := runCheckEventID(jr.job.AttemptID, "counting", "rc-ack")
	if err := jr.d.journal.Ack(ctx, eventID); err != nil {
		t.Fatal(err)
	}
	resetRunCheckMemo(jr)
	reopenRunCheckJournal(t, jr)
	recovered, err := call(t, jr, "platform_run_check", `{"check_id":"counting","idempotency_key":"rc-ack"}`)
	if err != nil {
		t.Fatalf("acked evidence must still recover the recorded result: %v", err)
	}
	if !reflect.DeepEqual(recovered.(map[string]any), first) {
		t.Fatalf("recovered = %v, want %v", recovered, first)
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "run"); got != 1 {
		t.Fatalf("acked recovery re-executed the check: %d", got)
	}
	if got := len(cap.byKind("test_results")); got != 1 {
		t.Fatalf("acked recovery created a second logical event: %d", got)
	}
	state, _, err := jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, scopeTool, "rc-ack")
	if err != nil || state != journal.ToolCallCompleted {
		t.Fatalf("pending tool call was not completed after ack: %v, %v", state, err)
	}
	recs, err := jr.d.journal.Unacked(ctx, journal.KindEvent, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("acked event resurfaced as unacked: %d", len(recs))
	}
	exists, err := jr.d.journal.HasRecord(ctx, eventID)
	if err != nil || !exists {
		t.Fatalf("acked event was deleted: %v, %v", exists, err)
	}
}

func TestRunCheckRecoveryFailsClosedOnMalformedOrMismatchedEvidence(t *testing.T) {
	jr, _ := runCheckFixture(t)
	jr.job.RequiredChecks = []string{"unit-check"}
	ctx := context.Background()
	scopeTool := "platform_run_check:unit-check"
	validPayload := func(eventID string) map[string]any {
		payload := map[string]any{}
		payload["kind"] = "test_results"
		payload["phase"] = "iterative"
		payload["source"] = "runner"
		payload["suite"] = "unit-check"
		payload["check_id"] = "unit-check"
		payload["job_id"] = jr.job.JobID
		payload["attempt_id"] = jr.job.AttemptID
		payload["fencing_token"] = jr.job.FencingToken
		payload["result"] = "passed"
		payload["repository_id"] = "primary"
		payload["duration_ms"] = 1
		payload["commit_sha"] = "abc123"
		payload["worktree_dirty"] = false
		return payload
	}
	appendEvidence := func(eventID string, kind journal.Kind, payload any) {
		t.Helper()
		env := v1.Envelope{Version: v1.ProtocolVersion, Type: v1.TypeEvent, ID: eventID, SentAt: time.Now(), Payload: payload}
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		if err := jr.d.journal.Append(ctx, journal.Record{
			ID: eventID, Kind: kind, Payload: raw, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name  string
		plant func(eventID string)
	}{
		{name: "missing evidence", plant: func(eventID string) {}},
		{name: "malformed payload", plant: func(eventID string) {
			if err := jr.d.journal.Append(ctx, journal.Record{
				ID: eventID, Kind: journal.KindEvent, Payload: []byte("{not json"), CreatedAt: time.Now(),
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wrong record kind", plant: func(eventID string) {
			appendEvidence(eventID, journal.KindCommand, validPayload(eventID))
		}},
		{name: "non-iterative event", plant: func(eventID string) {
			payload := validPayload(eventID)
			payload["phase"] = "terminal"
			appendEvidence(eventID, journal.KindEvent, payload)
		}},
		{name: "wrong event kind", plant: func(eventID string) {
			payload := validPayload(eventID)
			payload["kind"] = "job_failure"
			appendEvidence(eventID, journal.KindEvent, payload)
		}},
		{name: "attempt mismatch", plant: func(eventID string) {
			payload := validPayload(eventID)
			payload["attempt_id"] = "other-attempt"
			appendEvidence(eventID, journal.KindEvent, payload)
		}},
		{name: "check mismatch", plant: func(eventID string) {
			payload := validPayload(eventID)
			payload["check_id"] = "other-check"
			appendEvidence(eventID, journal.KindEvent, payload)
		}},
		{name: "fencing token mismatch", plant: func(eventID string) {
			payload := validPayload(eventID)
			payload["fencing_token"] = jr.job.FencingToken + 1
			appendEvidence(eventID, journal.KindEvent, payload)
		}},
		{name: "invalid result value", plant: func(eventID string) {
			payload := validPayload(eventID)
			payload["result"] = "unknown"
			appendEvidence(eventID, journal.KindEvent, payload)
		}},
		{name: "missing repository", plant: func(eventID string) {
			payload := validPayload(eventID)
			delete(payload, "repository_id")
			appendEvidence(eventID, journal.KindEvent, payload)
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := fmt.Sprintf("rc-closed-%d", i)
			eventID := runCheckEventID(jr.job.AttemptID, "unit-check", key)
			state, _, err := jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, scopeTool, key)
			if err != nil || state != journal.ToolCallStarted {
				t.Fatalf("begin tool call = %v, %v", state, err)
			}
			tc.plant(eventID)
			args := fmt.Sprintf(`{"check_id":"unit-check","idempotency_key":%q}`, key)
			if _, err := call(t, jr, "platform_run_check", args); err == nil {
				t.Fatal("malformed or mismatched evidence recovered a result")
			}
			if _, err := call(t, jr, "platform_run_check", args); err == nil {
				t.Fatal("retry after failed reconciliation did not re-attempt recovery")
			}
			state, _, err = jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, scopeTool, key)
			if err != nil || state != journal.ToolCallPending {
				t.Fatalf("failed reconciliation must leave the call retryable: %v, %v", state, err)
			}
			if tc.name != "missing evidence" {
				exists, err := jr.d.journal.HasRecord(ctx, eventID)
				if err != nil || !exists {
					t.Fatalf("failed reconciliation deleted durable evidence: %v, %v", exists, err)
				}
			}
		})
	}
}

func TestRunCheckRecoveryPreservesExactFencingTokenAboveFloat53(t *testing.T) {
	jr, cap := runCheckFixture(t)
	counter := filepath.Join(jr.repositories[0].WorkDir, ".exact-fence-count")
	jr.d.cfg.Checks["counting"] = config.CheckConfig{
		Command: "/bin/sh", Args: []string{"-c", "echo run >> \"" + counter + "\""},
	}
	jr.job.RequiredChecks = []string{"counting"}
	const exactToken = uint64(9007199254740993)
	setRunCheckFencingToken(t, jr, exactToken)
	ctx := context.Background()
	scopeTool := "platform_run_check:counting"
	if state, _, err := jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, scopeTool, "rc-exact-fence"); err != nil || state != journal.ToolCallStarted {
		t.Fatalf("begin tool call = %v, %v", state, err)
	}
	eventID := runCheckEventID(jr.job.AttemptID, "counting", "rc-exact-fence")
	raw := rawRunCheckEvidence(jr, eventID, "counting", `"fencing_token":9007199254740993`, "")
	if err := jr.d.journal.Append(ctx, journal.Record{
		ID: eventID, Kind: journal.KindEvent, Payload: raw, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	recovered, err := call(t, jr, "platform_run_check", `{"check_id":"counting","idempotency_key":"rc-exact-fence"}`)
	if err != nil {
		t.Fatalf("evidence with the exact >2^53 fencing token must recover: %v", err)
	}
	want := map[string]any{
		"check_id": "counting", "repository_id": "primary", "result": "passed",
		"duration_ms": int64(12), "commit_sha": "abc123", "worktree_dirty": false,
	}
	if !reflect.DeepEqual(recovered.(map[string]any), want) {
		t.Fatalf("recovered = %v, want %v", recovered, want)
	}
	if data, err := os.ReadFile(counter); !os.IsNotExist(err) {
		t.Fatalf("recovery re-executed the check: %q, %v", data, err)
	}
	if got := len(cap.byKind("test_results")); got != 0 {
		t.Fatalf("recovery created a second logical event: %d", got)
	}
	if state, _, err := jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, scopeTool, "rc-exact-fence"); err != nil || state != journal.ToolCallCompleted {
		t.Fatalf("pending tool call was not completed by exact-token recovery: %v, %v", state, err)
	}
}

func TestRunCheckRecoveryFailsClosedOnInexactOrMalformedFencingToken(t *testing.T) {
	jr, _ := runCheckFixture(t)
	jr.job.RequiredChecks = []string{"unit-check"}
	setRunCheckFencingToken(t, jr, 9007199254740993)
	ctx := context.Background()
	scopeTool := "platform_run_check:unit-check"
	cases := []struct {
		name   string
		token  string
		suffix string
	}{
		{name: "adjacent token below at float53 rounding point", token: "9007199254740992"},
		{name: "adjacent token above", token: "9007199254740994"},
		{name: "fractional token", token: "9007199254740993.0"},
		{name: "negative token", token: "-9007199254740993"},
		{name: "exponent token", token: "9.007199254740993e15"},
		{name: "uint64 overflow", token: "18446744073709551616"},
		{name: "string token", token: `"9007199254740993"`},
		{name: "missing token", token: ""},
		{name: "trailing json data", token: "9007199254740993", suffix: ` {"trailing":true}`},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := fmt.Sprintf("rc-fence-%d", i)
			eventID := runCheckEventID(jr.job.AttemptID, "unit-check", key)
			state, _, err := jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, scopeTool, key)
			if err != nil || state != journal.ToolCallStarted {
				t.Fatalf("begin tool call = %v, %v", state, err)
			}
			tokenField := `"fencing_token":` + tc.token
			if tc.token == "" {
				tokenField = `"fencing_token_absent":0`
			}
			raw := rawRunCheckEvidence(jr, eventID, "unit-check", tokenField, tc.suffix)
			if err := jr.d.journal.Append(ctx, journal.Record{
				ID: eventID, Kind: journal.KindEvent, Payload: raw, CreatedAt: time.Now(),
			}); err != nil {
				t.Fatal(err)
			}
			args := fmt.Sprintf(`{"check_id":"unit-check","idempotency_key":%q}`, key)
			if _, err := call(t, jr, "platform_run_check", args); err == nil {
				t.Fatal("inexact or malformed fencing token evidence recovered a result")
			}
			if _, err := call(t, jr, "platform_run_check", args); err == nil {
				t.Fatal("retry after failed fencing validation did not fail closed")
			}
			state, _, err = jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, scopeTool, key)
			if err != nil || state != journal.ToolCallPending {
				t.Fatalf("failed fencing validation must leave the call retryable: %v, %v", state, err)
			}
			exists, err := jr.d.journal.HasRecord(ctx, eventID)
			if err != nil || !exists {
				t.Fatalf("failed fencing validation deleted durable evidence: %v, %v", exists, err)
			}
		})
	}
}

func setRunCheckFencingToken(t *testing.T, jr *jobRunner, token uint64) {
	t.Helper()
	jr.job.FencingToken = token
	jr.d.leases.Release(jr.job.AttemptID)
	if err := jr.d.leases.Acquire(lease.Lease{
		LeaseID: jr.job.LeaseID, JobID: jr.job.JobID, AttemptID: jr.job.AttemptID, FencingToken: token,
		Kind: jr.job.Kind, ManifestID: jr.job.ManifestID, ManifestDigest: jr.job.ManifestDigest,
		WorkflowRevision: jr.job.WorkflowRevision,
		ExpiresAt:        time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
}

func rawRunCheckEvidence(jr *jobRunner, eventID, checkID, tokenField, suffix string) []byte {
	return []byte(fmt.Sprintf(`{"version":%d,"type":"event","id":%q,"sent_at":%q,`+
		`"payload":{"kind":"test_results","phase":"iterative","source":"runner",`+
		`"suite":%q,"check_id":%q,"job_id":%q,"attempt_id":%q,%s,`+
		`"result":"passed","repository_id":"primary","duration_ms":12,`+
		`"commit_sha":"abc123","worktree_dirty":false}}%s`,
		v1.ProtocolVersion, eventID, time.Now().UTC().Format(time.RFC3339Nano),
		checkID, checkID, jr.job.JobID, jr.job.AttemptID, tokenField, suffix))
}

func TestRunCheckConcurrentRecoveryConverges(t *testing.T) {
	jr, cap := runCheckFixture(t)
	counter := filepath.Join(jr.repositories[0].WorkDir, ".concurrent-count")
	jr.d.cfg.Checks["counting"] = config.CheckConfig{
		Command: "/bin/sh", Args: []string{"-c", "echo run >> \"" + counter + "\""},
	}
	jr.job.RequiredChecks = []string{"counting"}
	ctx := context.Background()
	scopeTool := "platform_run_check:counting"
	if state, _, err := jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, scopeTool, "rc-conc"); err != nil || state != journal.ToolCallStarted {
		t.Fatalf("begin tool call = %v, %v", state, err)
	}
	first, err := jr.executeRunCheck(ctx, map[string]any{"check_id": "counting", "idempotency_key": "rc-conc"})
	if err != nil {
		t.Fatal(err)
	}
	resetRunCheckMemo(jr)
	reopenRunCheckJournal(t, jr)
	const callers = 4
	results := make([]any, callers)
	errs := make([]error, callers)
	barrier := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-barrier
			results[i], errs[i] = jr.handleToolCall(ctx, runCheckTool,
				json.RawMessage(`{"check_id":"counting","idempotency_key":"rc-conc"}`))
		}(i)
	}
	close(barrier)
	wg.Wait()
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("concurrent recovery %d: %v", i, errs[i])
		}
		got := results[i].(map[string]any)
		for _, field := range []string{"check_id", "repository_id", "result", "commit_sha", "worktree_dirty"} {
			if got[field] != first.(map[string]any)[field] {
				t.Fatalf("concurrent recovery %d field %s = %v, want %v", i, field, got[field], first.(map[string]any)[field])
			}
		}
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "run"); got != 1 {
		t.Fatalf("concurrent recovery re-executed the check: %d", got)
	}
	if got := len(cap.byKind("test_results")); got != 1 {
		t.Fatalf("concurrent recovery duplicated events: %d", got)
	}
	state, _, err := jr.d.journal.BeginToolCall(ctx, jr.job.AttemptID, scopeTool, "rc-conc")
	if err != nil || state != journal.ToolCallCompleted {
		t.Fatalf("concurrent recovery did not complete the pending call: %v, %v", state, err)
	}
}

func TestRunCheckExecutionBudgetIsDurable(t *testing.T) {
	jr, cap := runCheckFixture(t)
	counter := filepath.Join(jr.repositories[0].WorkDir, ".budget-count")
	jr.d.cfg.Checks["expensive"] = config.CheckConfig{
		Command: "/bin/sh", Args: []string{"-c", "echo run >> \"" + counter + "\""},
	}
	jr.job.RequiredChecks = []string{"expensive"}
	executions := func() int {
		data, err := os.ReadFile(counter)
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(data), "run")
	}
	for i := 0; i < runCheckPerCheckExecBudget; i++ {
		args := fmt.Sprintf(`{"check_id":"expensive","idempotency_key":"rc-budget-%d"}`, i)
		if _, err := call(t, jr, "platform_run_check", args); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := call(t, jr, "platform_run_check", `{"check_id":"expensive","idempotency_key":"rc-budget-over"}`); err == nil {
		t.Fatal("per-check execution budget was not enforced")
	}
	if got := executions(); got != runCheckPerCheckExecBudget {
		t.Fatalf("executions = %d, want %d", got, runCheckPerCheckExecBudget)
	}
	resetRunCheckMemo(jr)
	replay, err := call(t, jr, "platform_run_check", `{"check_id":"expensive","idempotency_key":"rc-budget-0"}`)
	if err != nil {
		t.Fatalf("same-key replay must not consume a budget slot: %v", err)
	}
	if replay.(map[string]any)["result"] != "passed" {
		t.Fatalf("replay = %v", replay)
	}
	if got := executions(); got != runCheckPerCheckExecBudget {
		t.Fatalf("replay consumed budget: executions = %d", got)
	}
	reopenRunCheckJournal(t, jr)
	if _, err := call(t, jr, "platform_run_check", `{"check_id":"expensive","idempotency_key":"rc-budget-after-restart"}`); err == nil {
		t.Fatal("per-check execution budget was not durable across restart")
	}
	if got := executions(); got != runCheckPerCheckExecBudget {
		t.Fatalf("executions after restart = %d, want %d", got, runCheckPerCheckExecBudget)
	}
	if got := len(cap.byKind("test_results")); got != runCheckPerCheckExecBudget {
		t.Fatalf("events = %d, want %d", got, runCheckPerCheckExecBudget)
	}
}
