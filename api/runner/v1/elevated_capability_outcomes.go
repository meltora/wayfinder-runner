package v1

import "time"

const (
	EventKindElevatedCapabilityOutcomes     = "elevated_capability_outcomes"
	ElevatedCapabilityOutcomesSchemaVersion = 1
	ElevatedCapabilityOutcomeApplied        = "applied"
)

var ElevatedCapabilityOutcomeReasons = []string{
	"applied", "expired", "adapter_unsupported", "read_only_job", "declaration_unreadable",
	"digest_mismatch", "declaration_invalid", "server_name_conflict", "host_invalid", "hook_script_not_a_declaration",
	"remote_host_not_approved", "command_references_repository_file", "referenced_file_unverifiable",
	"program_not_absolute", "hook_event_not_allowed", "repository_not_bound", "repository_not_in_job",
	"container_program_unverifiable",
}

type ElevatedCapabilityOutcome struct {
	RequestID string `json:"request_id"`
	Applied   bool   `json:"applied"`
	Reason    string `json:"reason"`
}

type ElevatedCapabilityOutcomesEvent struct {
	Kind                string                      `json:"kind"`
	ManifestKind        JobKind                     `json:"manifest_kind"`
	LeaseID             string                      `json:"lease_id"`
	JobID               string                      `json:"job_id"`
	AttemptID           string                      `json:"attempt_id"`
	ManifestID          string                      `json:"manifest_id"`
	ManifestDigest      string                      `json:"manifest_digest"`
	FencingToken        uint64                      `json:"fencing_token"`
	ObservedAt          time.Time                   `json:"observed_at"`
	AgentRunAuthority   *AgentRunAuthority          `json:"agent_run_authority,omitempty"`
	SchemaVersion       int                         `json:"schema_version"`
	Outcomes            []ElevatedCapabilityOutcome `json:"outcomes"`
	ContentDigestSHA256 string                      `json:"content_digest_sha256"`
}

func ElevatedCapabilityOutcomesContentDigest(value ElevatedCapabilityOutcomesEvent) string {
	outcomes := make([]any, 0, len(value.Outcomes))
	for _, outcome := range value.Outcomes {
		outcomes = append(outcomes, map[string]any{
			"request_id": outcome.RequestID, "applied": outcome.Applied, "reason": outcome.Reason,
		})
	}
	digest, err := CommandContentDigest(map[string]any{"schema_version": value.SchemaVersion, "outcomes": outcomes})
	if err != nil {
		return ""
	}
	return digest
}
