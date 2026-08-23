package daemon

import (
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func manifestBoundEvent(job v1.JobSpec, kind string, fields map[string]any, observedAt time.Time) map[string]any {
	payload := authorityBoundEvent(job.ManifestAuthority(), job.JobID, job.AttemptID, job.FencingToken, kind, fields, observedAt)
	if job.AgentRunAuthority != nil {
		authority := *job.AgentRunAuthority
		payload["agent_run_authority"] = authority
	}
	return payload
}

func authorityBoundEvent(authority v1.ManifestAuthority, jobID, attemptID string, fencingToken uint64, kind string, fields map[string]any, observedAt time.Time) map[string]any {
	payload := make(map[string]any, len(fields)+9)
	for key, value := range fields {
		payload[key] = value
	}
	payload["kind"] = kind
	payload["manifest_kind"] = string(authority.Kind)
	payload["lease_id"] = authority.LeaseID
	payload["job_id"] = jobID
	payload["attempt_id"] = attemptID
	payload["manifest_id"] = authority.ManifestID
	payload["manifest_digest"] = authority.ManifestDigest
	payload["fencing_token"] = fencingToken
	payload["observed_at"] = observedAt.UTC().Format(time.RFC3339Nano)
	return payload
}
