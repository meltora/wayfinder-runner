package daemon

import (
	"time"

	"github.com/example/app-runner/internal/hookspool"
)

func hookEventPayload(event hookspool.Event) map[string]any {
	payload := map[string]any{
		"kind":            "hook_event",
		"hook_type":       event.Type,
		"manifest_kind":   string(event.ManifestKind),
		"lease_id":        event.LeaseID,
		"job_id":          event.JobID,
		"attempt_id":      event.AttemptID,
		"manifest_id":     event.ManifestID,
		"manifest_digest": event.ManifestDigest,
		"fencing_token":   event.FencingToken,
		"observed_at":     event.Timestamp.UTC().Format(time.RFC3339Nano),
	}
	if event.AgentRunAuthority != nil {
		authority := *event.AgentRunAuthority
		payload["agent_run_authority"] = authority
	}
	return payload
}
