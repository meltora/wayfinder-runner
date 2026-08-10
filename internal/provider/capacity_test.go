package provider

import (
	"testing"
	"time"
)

func TestClassifyCapacityFailureAcrossSupportedAdapters(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tests := map[string]struct {
		adapter string
		event   map[string]any
		wantAt  time.Time
	}{
		"codex usage limit": {
			adapter: "codex",
			event: map[string]any{
				"type":      "error",
				"message":   "You've hit your usage limit. Try again later.",
				"resets_at": "2026-08-10T14:30:00Z",
			},
			wantAt: time.Date(2026, 8, 10, 14, 30, 0, 0, time.UTC),
		},
		"claude subscription limit": {
			adapter: "claude",
			event: map[string]any{
				"type":                "result",
				"is_error":            true,
				"result":              "You've hit your limit. Your usage resets later.",
				"retry_after_seconds": 7200,
			},
			wantAt: now.Add(2 * time.Hour),
		},
		"opencode delegated provider quota": {
			adapter: "opencode",
			event: map[string]any{
				"type": "error",
				"error": map[string]any{
					"name": "APIError",
					"data": map[string]any{
						"message":  "Kimi For Coding billing-cycle quota exhausted",
						"reset_at": float64(1786377600),
					},
				},
			},
			wantAt: time.Unix(1786377600, 0).UTC(),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			failure, ok := ClassifyCapacityFailure(tt.adapter, tt.event, now)
			if !ok {
				t.Fatal("capacity failure was not detected")
			}
			if failure.Reason != CapacityReasonQuotaExhausted {
				t.Fatalf("reason = %q", failure.Reason)
			}
			if !failure.RetryAt.Equal(tt.wantAt) {
				t.Fatalf("retry_at = %s, want %s", failure.RetryAt, tt.wantAt)
			}
		})
	}
}

func TestClassifyCapacityFailureDoesNotConfuseTransientOrUntrustedOutput(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	for name, event := range map[string]map[string]any{
		"ordinary text":   {"type": "text", "message": "quota exhausted"},
		"transient 429":   {"type": "error", "message": "rate limit exceeded; retry shortly", "status_code": 429},
		"generic failure": {"type": "error", "message": "upstream server error"},
		"past retry hint": {"type": "error", "message": "monthly quota exhausted", "reset_at": "2020-01-01T00:00:00Z"},
	} {
		t.Run(name, func(t *testing.T) {
			failure, ok := ClassifyCapacityFailure("opencode", event, now)
			if name == "past retry hint" {
				if !ok || !failure.RetryAt.IsZero() {
					t.Fatalf("past hint should be discarded while preserving classification: %+v %v", failure, ok)
				}
				return
			}
			if ok {
				t.Fatalf("unexpected capacity classification: %+v", failure)
			}
		})
	}
}

func TestClassifyCapacityFailureFromOpenCodeKimiUsageLimit(t *testing.T) {
	event := map[string]any{
		"type": "error",
		"error": map[string]any{
			"name": "APIError",
			"data": map[string]any{
				"message":    "You've reached your usage limit for this billing cycle. Your quota will be refreshed in the next cycle.",
				"statusCode": float64(403),
			},
		},
	}
	failure, ok := ClassifyCapacityFailure("opencode", event, time.Now())
	if !ok || failure.Reason != CapacityReasonQuotaExhausted {
		t.Fatalf("failure=%+v ok=%v", failure, ok)
	}
}

func TestProviderIdentityDistinguishesAdapterFromDelegatedProvider(t *testing.T) {
	if got := ProviderIdentity("opencode", "kimi-for-coding/k3"); got != "kimi-for-coding" {
		t.Fatalf("delegated provider = %q", got)
	}
	if got := ProviderIdentity("claude", ""); got != "claude" {
		t.Fatalf("direct provider = %q", got)
	}
}
