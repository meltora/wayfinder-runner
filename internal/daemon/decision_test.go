package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestHumanInputBlocksUntilAnswer(t *testing.T) {
	jr, cap := testJobRunner(t)
	jr.ctx, jr.cancel = context.WithCancel(context.Background())
	jr.decisions = map[string]chan v1.CommandPayload{}

	type result struct {
		res any
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := jr.handleToolCall(context.Background(), "platform_request_human_input",
			json.RawMessage(`{"question":"Which DB?","options":["pg","mysql"],"idempotency_key":"q-1","timeout_seconds":5}`))
		done <- result{res, err}
	}()

	waitForTest(t, "decision_request event", func() bool {
		return len(cap.byKind("decision_request")) == 1
	})
	event := cap.byKind("decision_request")[0]
	observedAt, observedErr := time.Parse(time.RFC3339Nano, event["observed_at"].(string))
	expiresAt, expiryErr := time.Parse(time.RFC3339Nano, event["expires_at"].(string))
	if observedErr != nil || expiryErr != nil || expiresAt.Sub(observedAt) != 5*time.Second {
		t.Fatalf("decision expiry = %v -> %v (errors %v, %v)", observedAt, expiresAt, observedErr, expiryErr)
	}

	if err := jr.deliverDecision(v1.CommandPayload{QuestionID: "q-1", Decision: "pg", DecidedBy: "omar"}); err != nil {
		t.Fatalf("deliverDecision: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("tool call: %v", r.err)
		}
		m := r.res.(map[string]any)
		if m["status"] != "answered" || m["decision"] != "pg" || m["decided_by"] != "omar" {
			t.Fatalf("result = %v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tool call did not return after answer")
	}
}

func TestHumanInputTimeout(t *testing.T) {
	jr, cap := testJobRunner(t)
	jr.ctx, jr.cancel = context.WithCancel(context.Background())
	jr.decisions = map[string]chan v1.CommandPayload{}

	res, err := jr.handleToolCall(context.Background(), "platform_request_human_input",
		json.RawMessage(`{"question":"Pick one","timeout_seconds":1,"idempotency_key":"timeout-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.(map[string]any)["status"] != "timeout" {
		t.Fatalf("result = %v", res)
	}
	if timeouts := cap.byKind("decision_timeout"); len(timeouts) != 0 {
		t.Fatalf("decision_timeout events = %d, want PostgreSQL-owned expiry only", len(timeouts))
	}
}

func TestHumanInputCancel(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.ctx, jr.cancel = context.WithCancel(context.Background())
	jr.decisions = map[string]chan v1.CommandPayload{}

	done := make(chan error, 1)
	go func() {
		_, err := jr.handleToolCall(context.Background(), "platform_request_human_input",
			json.RawMessage(`{"question":"?","timeout_seconds":60,"idempotency_key":"cancel-1"}`))
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	jr.cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancel error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tool call did not return after cancel")
	}
}

func TestDeliverDecisionUnknownQuestion(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.decisions = map[string]chan v1.CommandPayload{}
	if err := jr.deliverDecision(v1.CommandPayload{QuestionID: "nope", Decision: "x"}); err == nil {
		t.Fatal("expected unknown question error")
	}
	if err := jr.deliverDecision(v1.CommandPayload{Decision: "x"}); err == nil {
		t.Fatal("expected missing question_id error")
	}
	if err := jr.deliverDecision(v1.CommandPayload{QuestionID: "nope", Decision: "x"}); err == nil {
		t.Fatal("expected missing decided_by error")
	}
}

func TestDecisionCommandRequiresExactActiveAuthority(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.decisions = map[string]chan v1.CommandPayload{"q-1": make(chan v1.CommandPayload, 1)}
	jr.d.jobMu.Lock()
	jr.d.activeJob = jr
	jr.d.jobMu.Unlock()

	valid := v1.CommandPayload{
		Command: v1.CmdDecisionResponse, JobID: jr.job.JobID, AttemptID: jr.job.AttemptID,
		FencingToken: jr.job.FencingToken, QuestionID: "q-1", Decision: "postgres", DecidedBy: "reviewer-1",
	}
	cases := []struct {
		name string
		cmd  v1.CommandPayload
	}{
		{"workflow", func() v1.CommandPayload { c := valid; c.JobID = "other-job"; return c }()},
		{"attempt", func() v1.CommandPayload { c := valid; c.AttemptID = "other-attempt"; return c }()},
		{"fence", func() v1.CommandPayload { c := valid; c.FencingToken--; return c }()},
		{"actor", func() v1.CommandPayload { c := valid; c.DecidedBy = ""; return c }()},
	}
	for _, tc := range cases {
		if err := jr.d.executeCommand(context.Background(), v1.Envelope{}, tc.cmd); err == nil {
			t.Fatalf("%s mismatch was accepted", tc.name)
		}
	}
	if len(jr.decisions["q-1"]) != 0 {
		t.Fatal("rejected authority delivered an answer")
	}
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{}, valid); err != nil {
		t.Fatalf("exact decision command: %v", err)
	}
	if len(jr.decisions["q-1"]) != 1 {
		t.Fatal("exact decision command was not delivered")
	}
}

func TestDeliverDecisionIsIdempotentForCommandRecovery(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.decisions = map[string]chan v1.CommandPayload{"q-1": make(chan v1.CommandPayload, 1)}
	answer := v1.CommandPayload{QuestionID: "q-1", Decision: "postgres", DecidedBy: "reviewer-1"}
	if err := jr.deliverDecision(answer); err != nil {
		t.Fatal(err)
	}
	if err := jr.deliverDecision(answer); err != nil {
		t.Fatalf("identical redelivery was not idempotent: %v", err)
	}
	changed := answer
	changed.Decision = "mysql"
	if err := jr.deliverDecision(changed); err == nil {
		t.Fatal("changed answer for an already-delivered question was accepted")
	}
	if got := len(jr.decisions["q-1"]); got != 1 {
		t.Fatalf("delivered answers = %d, want 1", got)
	}
}

func waitForTest(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHumanInputWaitsUpToSevenAndAHalfHours(t *testing.T) {
	_, timeout, err := decisionRequestParameters(map[string]any{"question": "Formal or casual?"})
	if err != nil || timeout != 7*time.Hour+30*time.Minute {
		t.Fatalf("default timeout = %v, %v", timeout, err)
	}
	_, timeout, err = decisionRequestParameters(map[string]any{"question": "Formal or casual?", "timeout_seconds": float64(27000)})
	if err != nil || timeout != 7*time.Hour+30*time.Minute {
		t.Fatalf("maximum timeout = %v, %v", timeout, err)
	}
	if _, _, err := decisionRequestParameters(map[string]any{"question": "Formal or casual?", "timeout_seconds": float64(27001)}); err == nil {
		t.Fatal("timeout above 27000 seconds was accepted")
	}
}
