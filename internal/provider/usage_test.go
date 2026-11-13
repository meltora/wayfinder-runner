package provider

import (
	"encoding/json"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func observeLines(t *testing.T, accumulator *UsageAccumulator, lines ...string) {
	t.Helper()
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		accumulator.Observe(event)
	}
}

func uint64Pointer(value uint64) *uint64 { return &value }

func TestUsageAccumulatorRejectsUnknownAdapters(t *testing.T) {
	if NewUsageAccumulator("kimi") != nil {
		t.Fatal("unknown adapter must not capture usage")
	}
	var accumulator *UsageAccumulator
	accumulator.Observe(map[string]any{"type": "result"})
	if accumulator.Snapshot() != nil {
		t.Fatal("nil accumulator must not report usage")
	}
}

func TestUsageAccumulatorReportsNothingWithoutUsage(t *testing.T) {
	accumulator := NewUsageAccumulator("claude")
	observeLines(t, accumulator, `{"type":"system","subtype":"init","model":"claude-opus-5-5"}`)
	if accumulator.Snapshot() != nil {
		t.Fatal("model without counters must not report usage")
	}
}

func TestUsageAccumulatorSumsClaudeResultsAcrossProcesses(t *testing.T) {
	accumulator := NewUsageAccumulator("claude")
	observeLines(t, accumulator,
		`{"type":"system","subtype":"init","model":"claude-opus-5-5","session_id":"s1"}`,
		`{"type":"result","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":500,"cache_creation_input_tokens":7}}`,
		`{"type":"result","usage":{"input_tokens":3,"output_tokens":4,"cache_read_input_tokens":5}}`,
	)
	got := accumulator.Snapshot()
	want := &v1.ProviderUsage{
		Provider:                 "claude",
		Model:                    "claude-opus-5-5",
		InputTokens:              103,
		OutputTokens:             24,
		CacheReadInputTokens:     uint64Pointer(505),
		CacheCreationInputTokens: uint64Pointer(7),
	}
	assertUsage(t, got, want)
}

func TestUsageAccumulatorUsesAssistantModelWhenInitIsMissing(t *testing.T) {
	accumulator := NewUsageAccumulator("claude")
	observeLines(t, accumulator,
		`{"type":"assistant","message":{"model":"claude-sonnet-4-5"}}`,
		`{"type":"result","usage":{"input_tokens":1,"output_tokens":2}}`,
	)
	assertUsage(t, accumulator.Snapshot(), &v1.ProviderUsage{
		Provider: "claude", Model: "claude-sonnet-4-5", InputTokens: 1, OutputTokens: 2,
	})
}

func TestUsageAccumulatorSumsCodexTurns(t *testing.T) {
	accumulator := NewUsageAccumulator("codex")
	observeLines(t, accumulator,
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":2}}`,
		`{"type":"turn.completed","usage":{"input_tokens":5,"cached_input_tokens":1,"output_tokens":3}}`,
	)
	assertUsage(t, accumulator.Snapshot(), &v1.ProviderUsage{
		Provider: "codex", InputTokens: 15, OutputTokens: 5, CacheReadInputTokens: uint64Pointer(5),
	})
}

func TestUsageAccumulatorSumsOpenCodeSteps(t *testing.T) {
	accumulator := NewUsageAccumulator("opencode")
	observeLines(t, accumulator,
		`{"type":"step_finish","part":{"modelID":"kimi-k2","tokens":{"input":8,"output":2,"reasoning":1,"cache":{"read":3,"write":4}}}}`,
		`{"type":"step_finish","part":{"tokens":{"input":2,"output":1,"cache":{"read":0,"write":0}}}}`,
		`{"type":"text","part":{"text":"ignored"}}`,
	)
	assertUsage(t, accumulator.Snapshot(), &v1.ProviderUsage{
		Provider:                 "opencode",
		Model:                    "kimi-k2",
		InputTokens:              10,
		OutputTokens:             4,
		CacheReadInputTokens:     uint64Pointer(3),
		CacheCreationInputTokens: uint64Pointer(4),
	})
}

func TestUsageAccumulatorIgnoresMalformedCountersAndModels(t *testing.T) {
	accumulator := NewUsageAccumulator("claude")
	observeLines(t, accumulator,
		`{"type":"system","subtype":"init","model":"bad model; rm -rf"}`,
		`{"type":"result","usage":{"input_tokens":-1,"output_tokens":2}}`,
		`{"type":"result","usage":{"input_tokens":1.5,"output_tokens":2}}`,
		`{"type":"result","usage":{"input_tokens":"12","output_tokens":2}}`,
		`{"type":"result","usage":{"input_tokens":9007199254740992,"output_tokens":2}}`,
	)
	if got := accumulator.Snapshot(); got != nil {
		t.Fatalf("malformed usage must not be reported, got %+v", got)
	}
}

func TestUsageAccumulatorInvalidatesWhenMalformedUsageFollowsValidUsage(t *testing.T) {
	cases := []struct {
		name    string
		adapter string
		lines   []string
	}{
		{"claude malformed required counter", "claude", []string{
			`{"type":"result","usage":{"input_tokens":10,"output_tokens":2}}`,
			`{"type":"result","usage":{"input_tokens":-1,"output_tokens":2}}`,
		}},
		{"claude malformed cache counter", "claude", []string{
			`{"type":"result","usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":4}}`,
			`{"type":"result","usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":"4"}}`,
		}},
		{"claude usage that is not an object", "claude", []string{
			`{"type":"result","usage":{"input_tokens":10,"output_tokens":2}}`,
			`{"type":"result","usage":"lots"}`,
		}},
		{"codex malformed required counter", "codex", []string{
			`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}`,
			`{"type":"turn.completed","usage":{"input_tokens":1.5,"output_tokens":2}}`,
		}},
		{"codex malformed cache counter", "codex", []string{
			`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":1,"output_tokens":2}}`,
			`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":-3,"output_tokens":2}}`,
		}},
		{"opencode malformed required counter", "opencode", []string{
			`{"type":"step_finish","part":{"tokens":{"input":8,"output":2}}}`,
			`{"type":"step_finish","part":{"tokens":{"input":8,"output":"2"}}}`,
		}},
		{"opencode malformed cache counter", "opencode", []string{
			`{"type":"step_finish","part":{"tokens":{"input":8,"output":2,"cache":{"read":1,"write":1}}}}`,
			`{"type":"step_finish","part":{"tokens":{"input":8,"output":2,"cache":{"read":1,"write":0.5}}}}`,
		}},
		{"opencode malformed reasoning counter", "opencode", []string{
			`{"type":"step_finish","part":{"tokens":{"input":8,"output":2,"reasoning":1}}}`,
			`{"type":"step_finish","part":{"tokens":{"input":8,"output":2,"reasoning":-1}}}`,
		}},
		{"opencode cache that is not an object", "opencode", []string{
			`{"type":"step_finish","part":{"tokens":{"input":8,"output":2}}}`,
			`{"type":"step_finish","part":{"tokens":{"input":8,"output":2,"cache":7}}}`,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accumulator := NewUsageAccumulator(tc.adapter)
			observeLines(t, accumulator, tc.lines...)
			if got := accumulator.Snapshot(); got != nil {
				t.Fatalf("malformed usage after valid usage must withhold all usage, got %+v", got)
			}
		})
	}
}

func TestUsageAccumulatorDoesNotAddOpenCodeReasoningWhenStepCountersFail(t *testing.T) {
	accumulator := NewUsageAccumulator("opencode")
	observeLines(t, accumulator,
		`{"type":"step_finish","part":{"tokens":{"input":8,"output":2}}}`,
		`{"type":"step_finish","part":{"tokens":{"input":"x","output":2,"reasoning":5}}}`,
	)
	if got := accumulator.Snapshot(); got != nil {
		t.Fatalf("step with malformed counters must withhold usage, got %+v", got)
	}
}

func TestUsageAccumulatorWithholdsOverflowingTotals(t *testing.T) {
	accumulator := NewUsageAccumulator("claude")
	observeLines(t, accumulator,
		`{"type":"result","usage":{"input_tokens":9007199254740991,"output_tokens":0}}`,
		`{"type":"result","usage":{"input_tokens":1,"output_tokens":0}}`,
	)
	if got := accumulator.Snapshot(); got != nil {
		t.Fatalf("overflowing usage must not be reported, got %+v", got)
	}
}

func assertUsage(t *testing.T, got, want *v1.ProviderUsage) {
	t.Helper()
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("usage = %s, want %s", gotJSON, wantJSON)
	}
}
