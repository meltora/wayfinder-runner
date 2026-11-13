package daemon

import (
	"bytes"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func tokenCount(value uint64) *uint64 { return &value }

func TestConsumeOutputStopsProviderWhenRunTokenBudgetIsReached(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.d.cfg.Provider = "claude"
	jr.job.TokenBudget = &v1.TokenBudget{RunLimitTokens: tokenCount(150), ProjectRemainingTokens: tokenCount(1000)}
	process := &recordingProcess{}
	jr.proc = process
	jr.consumeOutput(bytes.NewBufferString(strings.Join([]string{
		`{"type":"assistant","message":{"id":"msg_1","usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":9000}}}`,
		`{"type":"assistant","message":{"id":"msg_2","usage":{"input_tokens":30,"output_tokens":15}}}`,
	}, "\n") + "\n"))

	failure, ok := jr.currentTokenBudgetFailure()
	if !ok || failure.code != v1.JobFailureRunTokenBudget {
		t.Fatalf("budget failure = %+v %v", failure, ok)
	}
	if process.kills() != 1 {
		t.Fatalf("kills = %d, want 1", process.kills())
	}
	if !strings.Contains(failure.message, "150") || !strings.Contains(failure.message, "155") {
		t.Fatalf("message = %q", failure.message)
	}
	usage := jr.jobFinishedEvent(jobOutcome{State: v1.JobFinishedFailed, FailureCode: failure.code, FailureMessage: failure.message}).ProviderUsage
	if usage == nil || usage.InputTokens != 130 || usage.OutputTokens != 25 {
		t.Fatalf("interrupted run usage = %+v", usage)
	}
}

func TestConsumeOutputReportsProjectBudgetWhenItIsTheTighterLimit(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.d.cfg.Provider = "opencode"
	jr.job.TokenBudget = &v1.TokenBudget{RunLimitTokens: tokenCount(1000), ProjectRemainingTokens: tokenCount(20)}
	process := &recordingProcess{}
	jr.proc = process
	jr.consumeOutput(bytes.NewBufferString(`{"type":"step_finish","part":{"tokens":{"input":15,"output":10,"cache":{"read":0,"write":0}}}}` + "\n" +
		`{"type":"step_finish","part":{"tokens":{"input":15,"output":10,"cache":{"read":0,"write":0}}}}` + "\n"))

	failure, ok := jr.currentTokenBudgetFailure()
	if !ok || failure.code != v1.JobFailureProjectTokenBudget {
		t.Fatalf("budget failure = %+v %v", failure, ok)
	}
	if process.kills() != 1 {
		t.Fatalf("provider must be stopped once, kills = %d", process.kills())
	}
}

func TestConsumeOutputDoesNotStopWithinBudgetOrWithoutBudget(t *testing.T) {
	for name, budget := range map[string]*v1.TokenBudget{
		"within budget": {RunLimitTokens: tokenCount(1000)},
		"no budget":     nil,
	} {
		jr, _ := testJobRunner(t)
		jr.d.cfg.Provider = "codex"
		jr.job.TokenBudget = budget
		process := &recordingProcess{}
		jr.proc = process
		jr.consumeOutput(bytes.NewBufferString(`{"type":"turn.completed","usage":{"input_tokens":400,"cached_input_tokens":300,"output_tokens":50}}` + "\n"))
		if _, ok := jr.currentTokenBudgetFailure(); ok || process.kills() != 0 {
			t.Fatalf("%s: stopped a run that is within its budget", name)
		}
	}
}

func TestTokenBudgetStartFailureRejectsExhaustedProjectBudget(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.job.TokenBudget = &v1.TokenBudget{RunLimitTokens: tokenCount(10), ProjectRemainingTokens: tokenCount(0)}
	failure, ok := jr.tokenBudgetStartFailure()
	if !ok || failure.code != v1.JobFailureProjectTokenBudget || !strings.Contains(failure.message, "before the provider started") {
		t.Fatalf("start failure = %+v %v", failure, ok)
	}
	jr.job.TokenBudget = &v1.TokenBudget{RunLimitTokens: tokenCount(10), ProjectRemainingTokens: tokenCount(1)}
	if _, ok := jr.tokenBudgetStartFailure(); ok {
		t.Fatal("remaining project budget rejected")
	}
}
