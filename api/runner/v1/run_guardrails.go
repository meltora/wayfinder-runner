package v1

import "time"

const (
	EventKindRefusedRequest               = "refused_request"
	EventKindRunNetworkPolicy             = "run_network_policy"
	RunGuardrailTransportVersion          = 1
	RunGuardrailsSchemaVersion            = 1
	MaxRefusedRequests                    = 100
	MaxRefusedRequestSubjectBytes         = 160
	RefusedRequestRedactionPolicyVersion  = AgentActivityRedactionPolicyVersion
	RefusedRequestRedactionSchemaVersion  = AgentActivityRedactionSchemaVersion
	RefusedRequestRedactionCollectorLabel = AgentActivityRedactionCollectorLabel
)

type RefusalKind string

const (
	RefusalNetworkDestination RefusalKind = "network_destination"
	RefusalCommand            RefusalKind = "command"
	RefusalTool               RefusalKind = "tool"
)

type RefusalEnforcer string

const (
	RefusalEnforcerAgentRuntimePermissions RefusalEnforcer = "agent_runtime_permissions"
	RefusalEnforcerAgentSandbox            RefusalEnforcer = "agent_sandbox"
	RefusalEnforcerJobEgressRules          RefusalEnforcer = "job_egress_rules"
)

type RunIsolation string

const (
	RunIsolationDocker RunIsolation = "docker"
	RunIsolationDirect RunIsolation = "direct"
)

type RunJobNetwork string

const (
	RunJobNetworkNone       RunJobNetwork = "none"
	RunJobNetworkRestricted RunJobNetwork = "restricted"
	RunJobNetworkHost       RunJobNetwork = "host"
)

type AgentSandboxNetwork string

const (
	AgentSandboxNetworkDenyAll     AgentSandboxNetwork = "deny_all"
	AgentSandboxNetworkNotRecorded AgentSandboxNetwork = "not_recorded"
)

type RunGuardrailsPolicy struct {
	SchemaVersion      int `json:"schema_version"`
	MaxRefusedRequests int `json:"max_refused_requests"`
}

type RefusedRequestEvent struct {
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
	RefusalKind         RefusalKind           `json:"refusal_kind"`
	Enforcer            RefusalEnforcer       `json:"enforcer"`
	Subject             string                `json:"subject"`
	Truncated           bool                  `json:"truncated"`
	Redaction           SanitizedLogRedaction `json:"redaction"`
	ContentDigestSHA256 string                `json:"content_digest_sha256"`
}

type RunNetworkPolicyEvent struct {
	Kind                string              `json:"kind"`
	ManifestKind        JobKind             `json:"manifest_kind"`
	LeaseID             string              `json:"lease_id"`
	JobID               string              `json:"job_id"`
	AttemptID           string              `json:"attempt_id"`
	ManifestID          string              `json:"manifest_id"`
	ManifestDigest      string              `json:"manifest_digest"`
	FencingToken        uint64              `json:"fencing_token"`
	ObservedAt          time.Time           `json:"observed_at"`
	AgentRunAuthority   AgentRunAuthority   `json:"agent_run_authority"`
	Isolation           RunIsolation        `json:"isolation"`
	JobNetwork          RunJobNetwork       `json:"job_network"`
	EgressPolicyID      *string             `json:"egress_policy_id"`
	AgentSandboxNetwork AgentSandboxNetwork `json:"agent_sandbox_network"`
	EgressRules         *RunEgressRules     `json:"egress_rules,omitempty"`
	ContentDigestSHA256 string              `json:"content_digest_sha256"`
}

func RefusedRequestContentDigest(value RefusedRequestEvent) string {
	digest, err := CommandContentDigest(map[string]any{
		"refusal_kind": value.RefusalKind,
		"enforcer":     value.Enforcer,
		"sequence":     value.Sequence,
		"subject":      value.Subject,
		"truncated":    value.Truncated,
		"redaction":    value.Redaction,
	})
	if err != nil {
		return ""
	}
	return digest
}

func RunNetworkPolicyContentDigest(value RunNetworkPolicyEvent) string {
	var egressPolicyID any
	if value.EgressPolicyID != nil {
		egressPolicyID = *value.EgressPolicyID
	}
	content := map[string]any{
		"isolation":             value.Isolation,
		"job_network":           value.JobNetwork,
		"egress_policy_id":      egressPolicyID,
		"agent_sandbox_network": value.AgentSandboxNetwork,
	}
	if value.EgressRules != nil {
		content["egress_rules"] = value.EgressRules
	}
	digest, err := CommandContentDigest(content)
	if err != nil {
		return ""
	}
	return digest
}
