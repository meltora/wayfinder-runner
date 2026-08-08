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
	timeouts := cap.byKind("decision_timeout")
	if len(timeouts) != 1 {
		t.Fatalf("decision_timeout events = %d", len(timeouts))
	}
	if timeouts[0]["question"] != "Pick one" || timeouts[0]["question_id"] != "timeout-1" {
		t.Fatalf("timeout event = %v", timeouts[0])
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
