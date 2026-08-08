package daemon

import "testing"

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
