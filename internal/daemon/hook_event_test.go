package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/hookspool"
)

func TestHookEventPayloadContainsOnlyDaemonDerivedAuthority(t *testing.T) {
	observed := time.Date(2030, 8, 15, 12, 0, 0, 123, time.UTC)
	payload := hookEventPayload(hookspool.Event{
		Type: "post-tool", Timestamp: observed, ManifestKind: "build",
		LeaseID: "lease-1", JobID: "job-1", AttemptID: "attempt-1",
		ManifestID: "manifest-1", ManifestDigest: strings.Repeat("a", 64),
		FencingToken: 7,
	})
	if len(payload) != 10 || payload["kind"] != "hook_event" ||
		payload["hook_type"] != "post-tool" || payload["job_id"] != "job-1" ||
		payload["observed_at"] != observed.Format(time.RFC3339Nano) {
		t.Fatalf("payload=%+v", payload)
	}
	for _, forbidden := range []string{"event", "data", "command", "prompt", "output", "token"} {
		if _, ok := payload[forbidden]; ok {
			t.Fatalf("payload contains forbidden field %q: %+v", forbidden, payload)
		}
	}
}
