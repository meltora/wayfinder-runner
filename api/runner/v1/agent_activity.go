package v1

import "time"

const (
	EventKindAgentActivity               = "agent_activity"
	AgentActivityTransportVersion        = 1
	AgentActivitySchemaVersion           = 1
	MaxAgentActivityEvents               = 500
	MaxAgentActivityEventsPerMinute      = 60
	MinAgentActivityMessageBytes         = 64
	MaxAgentActivityMessageBytes         = 480
	AgentActivityRedactionPolicyVersion  = "runner-activity-redaction-1"
	AgentActivityRedactionSchemaVersion  = 1
	AgentActivityRedactionCollectorLabel = "runner"
)

type AgentActivityType string

const (
	AgentActivityAssistantMessage AgentActivityType = "assistant_message"
	AgentActivityToolStep         AgentActivityType = "tool_step"
	AgentActivityStatus           AgentActivityType = "status"
	AgentActivityLimitReached     AgentActivityType = "limit_reached"
)

type AgentActivityPolicy struct {
	SchemaVersion      int `json:"schema_version"`
	MaxEvents          int `json:"max_events"`
	MaxEventsPerMinute int `json:"max_events_per_minute"`
	MaxMessageBytes    int `json:"max_message_bytes"`
}

type AgentActivityEvent struct {
	Kind                string                `json:"kind"`
	ManifestKind        JobKind               `json:"manifest_kind"`
	LeaseID             string                `json:"lease_id"`
	JobID               string                `json:"job_id"`
	AttemptID           string                `json:"attempt_id"`
	ManifestID          string                `json:"manifest_id"`
	ManifestDigest      string                `json:"manifest_digest"`
	FencingToken        uint64                `json:"fencing_token"`
	ObservedAt          time.Time             `json:"observed_at"`
	AgentRunAuthority   AgentRunAuthority     `json:"agent_run_authority"`
	Sequence            int                   `json:"sequence"`
	ActivityType        AgentActivityType     `json:"activity_type"`
	Message             string                `json:"message"`
	Truncated           bool                  `json:"truncated"`
	Redaction           SanitizedLogRedaction `json:"redaction"`
	ContentDigestSHA256 string                `json:"content_digest_sha256"`
}

func AgentActivityContentDigest(value AgentActivityEvent) string {
	digest, err := CommandContentDigest(map[string]any{
		"activity_type": value.ActivityType,
		"sequence":      value.Sequence,
		"message":       value.Message,
		"truncated":     value.Truncated,
		"redaction":     value.Redaction,
	})
	if err != nil {
		return ""
	}
	return digest
}
