package validation

import (
	"fmt"

	v1 "github.com/example/app-runner/api/runner/v1"
)

// JobStartedEvent validates runner-owned process-start evidence before it is
// durably queued.
func JobStartedEvent(event v1.JobStartedEvent) error {
	if event.Kind != "job_started" {
		return fmt.Errorf("job started event kind is invalid")
	}
	if event.ManifestKind != v1.JobKindPlan && event.ManifestKind != v1.JobKindBuild {
		return fmt.Errorf("job started manifest kind is invalid")
	}
	for field, value := range map[string]string{
		"lease_id": event.LeaseID, "job_id": event.JobID,
		"attempt_id": event.AttemptID, "manifest_id": event.ManifestID,
	} {
		if err := ID(field, value); err != nil {
			return err
		}
	}
	if err := SHA256Digest("manifest_digest", event.ManifestDigest); err != nil {
		return err
	}
	if event.FencingToken == 0 || event.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("fencing_token is outside the protocol JSON-safe range")
	}
	if event.ObservedAt.IsZero() {
		return fmt.Errorf("observed_at is required")
	}
	return AgentRunAuthority(event.AgentRunAuthority)
}
