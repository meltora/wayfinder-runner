package daemon

import (
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestProviderSessionIDSupportsOpenCodeAndLegacyEvents(t *testing.T) {
	for name, event := range map[string]map[string]any{
		"legacy":   {"session_id": "legacy-1"},
		"opencode": {"sessionID": "session-1"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := providerSessionID(event); got == "" {
				t.Fatalf("session ID not extracted from %#v", event)
			}
		})
	}
}

func TestProviderErrorMessageExtractsAndRedactsStructuredErrors(t *testing.T) {
	tests := map[string]struct {
		event map[string]any
		want  string
	}{
		"opencode nested error": {
			event: map[string]any{
				"type": "error",
				"error": map[string]any{
					"name": "APIError",
					"data": map[string]any{"message": "provider quota exhausted"},
				},
			},
			want: "provider quota exhausted",
		},
		"top-level message": {
			event: map[string]any{"type": "error", "message": "authentication failed"},
			want:  "authentication failed",
		},
		"claude error result": {
			event: map[string]any{
				"type": "result", "is_error": true,
				"result": "You've hit your usage limit",
			},
			want: "You've hit your usage limit",
		},
		"secret-bearing message": {
			event: map[string]any{"type": "error", "message": "Bearer abcdefghijklmnop"},
			want:  "[redacted]",
		},
		"non-error event": {
			event: map[string]any{"type": "text", "message": "not an error"},
			want:  "",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := providerErrorMessage(tt.event); got != tt.want {
				t.Fatalf("providerErrorMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSafeLogValueRedactsCredentialShapedProviderMetadata(t *testing.T) {
	if got := safeLogValue("Bearer abcdefghijklmnop"); got != "[redacted]" {
		t.Fatalf("safeLogValue()=%q", got)
	}
	if got := safeLogValue("private arbitrary provider detail"); got != "[suppressed]" {
		t.Fatalf("safeLogValue()=%q", got)
	}
	if got := safeLogValue("success"); got != "success" {
		t.Fatalf("safeLogValue()=%q", got)
	}
}

func TestCapacityResumePolicyDefaultsToManual(t *testing.T) {
	if got := (&jobRunner{}).effectiveCapacityResumePolicy(); got != "manual" {
		t.Fatalf("default resume policy = %q", got)
	}
	jr := &jobRunner{job: v1.JobSpec{ProviderCapacityResumePolicy: v1.CapacityResumeAutomatic}}
	if got := jr.effectiveCapacityResumePolicy(); got != "automatic" {
		t.Fatalf("automatic resume policy = %q", got)
	}
}
