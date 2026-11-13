package provider

import "testing"

func TestBudgetedTokensExcludesCacheReads(t *testing.T) {
	accumulator := NewUsageAccumulator("opencode")
	observeLines(t, accumulator, `{"type":"step_finish","part":{"tokens":{"input":10,"output":5,"reasoning":2,"cache":{"read":1000,"write":3}}}}`)
	if total, ok := accumulator.BudgetedTokens(); !ok || total != 20 {
		t.Fatalf("budgeted = %d %v, want 20", total, ok)
	}
}

func TestBudgetedTokensRemovesCodexCachedInputFromInput(t *testing.T) {
	accumulator := NewUsageAccumulator("codex")
	observeLines(t, accumulator,
		`{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":800,"output_tokens":50}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5}}`,
	)
	if total, ok := accumulator.BudgetedTokens(); !ok || total != 265 {
		t.Fatalf("budgeted = %d %v, want 265", total, ok)
	}
	snapshot := accumulator.Snapshot()
	if snapshot == nil || snapshot.InputTokens != 1010 || snapshot.CacheReadInputTokens == nil || *snapshot.CacheReadInputTokens != 800 {
		t.Fatalf("snapshot must keep provider-reported counters, got %+v", snapshot)
	}
}

func TestBudgetedTokensNeverRemovesMoreThanCodexInput(t *testing.T) {
	accumulator := NewUsageAccumulator("codex")
	observeLines(t, accumulator, `{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":40,"output_tokens":5}}`)
	if total, ok := accumulator.BudgetedTokens(); !ok || total != 5 {
		t.Fatalf("budgeted = %d %v, want 5", total, ok)
	}
}

func TestBudgetedTokensCountsClaudeAssistantUsageBeforeResult(t *testing.T) {
	accumulator := NewUsageAccumulator("claude")
	observeLines(t, accumulator,
		`{"type":"assistant","message":{"id":"msg_1","model":"claude-opus-5-5","usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":500,"cache_creation_input_tokens":20}}}`,
		`{"type":"assistant","message":{"id":"msg_1","usage":{"input_tokens":100,"output_tokens":12,"cache_read_input_tokens":500,"cache_creation_input_tokens":20}}}`,
		`{"type":"assistant","message":{"id":"msg_2","usage":{"input_tokens":5,"output_tokens":3}}}`,
	)
	if total, ok := accumulator.BudgetedTokens(); !ok || total != 140 {
		t.Fatalf("pending budgeted = %d %v, want 140", total, ok)
	}
	snapshot := accumulator.Snapshot()
	if snapshot == nil || snapshot.InputTokens != 105 || snapshot.OutputTokens != 15 ||
		snapshot.CacheReadInputTokens == nil || *snapshot.CacheReadInputTokens != 500 ||
		snapshot.CacheCreationInputTokens == nil || *snapshot.CacheCreationInputTokens != 20 {
		t.Fatalf("interrupted snapshot = %+v", snapshot)
	}
	observeLines(t, accumulator, `{"type":"result","usage":{"input_tokens":105,"output_tokens":15,"cache_read_input_tokens":500,"cache_creation_input_tokens":20}}`)
	if total, ok := accumulator.BudgetedTokens(); !ok || total != 140 {
		t.Fatalf("budgeted after result = %d %v, want 140 without double counting", total, ok)
	}
	snapshot = accumulator.Snapshot()
	if snapshot.InputTokens != 105 || snapshot.OutputTokens != 15 {
		t.Fatalf("snapshot after result = %+v", snapshot)
	}
}

func TestBudgetedTokensIgnoresMalformedClaudeAssistantUsage(t *testing.T) {
	accumulator := NewUsageAccumulator("claude")
	observeLines(t, accumulator,
		`{"type":"assistant","message":{"id":"msg_1","usage":{"input_tokens":-1,"output_tokens":1}}}`,
		`{"type":"assistant","message":{"usage":{"input_tokens":4,"output_tokens":1}}}`,
	)
	if total, ok := accumulator.BudgetedTokens(); ok || total != 0 {
		t.Fatalf("budgeted = %d %v, want nothing reported", total, ok)
	}
	if snapshot := accumulator.Snapshot(); snapshot != nil {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestBudgetedTokensReportsNothingWhenCountersAreInvalid(t *testing.T) {
	accumulator := NewUsageAccumulator("codex")
	observeLines(t, accumulator,
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":2}}`,
	)
	if total, ok := accumulator.BudgetedTokens(); !ok || total != 8 {
		t.Fatalf("codex budgeted = %d %v, want 8", total, ok)
	}
	observeLines(t, accumulator, `{"type":"turn.completed","usage":{"input_tokens":"x","output_tokens":2}}`)
	if _, ok := accumulator.BudgetedTokens(); ok {
		t.Fatal("invalid counters still reported a budgeted total")
	}
}
