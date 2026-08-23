package v1

import "time"

const AgentRunTransportVersion = 1

// AgentRunAuthority is the complete versioned causal identity for a job-backed
// agent step. Keeping it nested prevents partially upgraded senders from
// accidentally creating a weaker, mixed-version identity.
type AgentRunAuthority struct {
	SchemaVersion int    `json:"schema_version"`
	AgentRunID    string `json:"agent_run_id"`
	StepRunID     string `json:"step_run_id"`
	StepSequence  uint64 `json:"step_sequence"`
}

type JobControlAction string

const (
	JobControlPause  JobControlAction = "pause"
	JobControlCancel JobControlAction = "cancel"
)

type JobControlOutcome string

const (
	JobControlApplied        JobControlOutcome = "applied"
	JobControlAlreadyApplied JobControlOutcome = "already_applied"
	JobControlRejected       JobControlOutcome = "rejected"
)

type JobControlReasonCode string

const (
	JobControlReasonAlreadyPaused         JobControlReasonCode = "already_paused"
	JobControlReasonAlreadyCancelled      JobControlReasonCode = "already_cancelled"
	JobControlReasonStaleAuthority        JobControlReasonCode = "stale_authority"
	JobControlReasonTerminalAttempt       JobControlReasonCode = "terminal_attempt"
	JobControlReasonCheckpointUnavailable JobControlReasonCode = "checkpoint_unavailable"
	JobControlReasonPolicyDenied          JobControlReasonCode = "policy_denied"
	JobControlReasonRunnerInternalError   JobControlReasonCode = "runner_internal_error"
)

type JobControlOperation struct {
	ControlID         string            `json:"control_id"`
	Action            JobControlAction  `json:"action"`
	Reason            string            `json:"reason"`
	RequestedAt       string            `json:"requested_at"`
	ExpiresAt         string            `json:"expires_at"`
	JobID             string            `json:"job_id"`
	AttemptID         string            `json:"attempt_id"`
	LeaseID           string            `json:"lease_id"`
	ManifestID        string            `json:"manifest_id"`
	ManifestDigest    string            `json:"manifest_digest"`
	FencingToken      uint64            `json:"fencing_token"`
	AgentRunAuthority AgentRunAuthority `json:"agent_run_authority"`
	CommandDigest     string            `json:"command_digest"`
}

func JobControlCommandDigest(value JobControlOperation) string {
	digest, err := CommandContentDigest(map[string]any{
		"action": value.Action, "agent_run_authority": value.AgentRunAuthority,
		"attempt_id": value.AttemptID, "command": CmdControlJob, "control_id": value.ControlID,
		"expires_at": value.ExpiresAt, "fencing_token": value.FencingToken,
		"job_id": value.JobID, "lease_id": value.LeaseID, "manifest_digest": value.ManifestDigest,
		"manifest_id": value.ManifestID, "reason": value.Reason, "requested_at": value.RequestedAt,
	})
	if err != nil {
		return ""
	}
	return digest
}

func (command CommandPayload) JobControlOperation() JobControlOperation {
	authority := AgentRunAuthority{}
	if command.AgentRunAuthority != nil {
		authority = *command.AgentRunAuthority
	}
	return JobControlOperation{
		ControlID: command.ControlID, Action: command.Action, Reason: command.Reason,
		RequestedAt: command.RequestedAt, ExpiresAt: command.ExpiresAt,
		JobID: command.JobID, AttemptID: command.AttemptID, LeaseID: command.LeaseID,
		ManifestID: command.ManifestID, ManifestDigest: command.ManifestDigest,
		FencingToken: command.FencingToken, AgentRunAuthority: authority,
		CommandDigest: command.CommandDigest,
	}
}

type JobControlResultEvent struct {
	Kind              string               `json:"kind"`
	ManifestKind      JobKind              `json:"manifest_kind"`
	LeaseID           string               `json:"lease_id"`
	JobID             string               `json:"job_id"`
	AttemptID         string               `json:"attempt_id"`
	ManifestID        string               `json:"manifest_id"`
	ManifestDigest    string               `json:"manifest_digest"`
	FencingToken      uint64               `json:"fencing_token"`
	ObservedAt        time.Time            `json:"observed_at"`
	AgentRunAuthority AgentRunAuthority    `json:"agent_run_authority"`
	ControlID         string               `json:"control_id"`
	Action            JobControlAction     `json:"action"`
	Outcome           JobControlOutcome    `json:"outcome"`
	ReasonCode        JobControlReasonCode `json:"reason_code,omitempty"`
	CommandDigest     string               `json:"command_digest"`
}

type TrustedRecoveryCheckpoint struct {
	SchemaVersion            int                              `json:"schema_version"`
	CheckpointID             string                           `json:"checkpoint_id"`
	DigestSHA256             string                           `json:"digest_sha256"`
	Collector                string                           `json:"collector"`
	Repositories             []RecoveryCheckpointRepository   `json:"repositories"`
	RepositorySetDigest      string                           `json:"repository_set_digest"`
	SafetyClassification     RecoverySafetyClassification     `json:"safety_classification"`
	SideEffectClassification RecoverySideEffectClassification `json:"side_effect_classification"`
	TestClassification       RecoveryTestClassification       `json:"test_classification"`
}

type RecoverySafetyClassification string

const (
	RecoverySafetyDisplayOnly  RecoverySafetyClassification = "display_only"
	RecoverySafetySafeToResume RecoverySafetyClassification = "safe_to_resume"
)

type RecoverySideEffectClassification string

const (
	RecoverySideEffectsNone     RecoverySideEffectClassification = "none"
	RecoverySideEffectsResolved RecoverySideEffectClassification = "resolved"
	RecoverySideEffectsUnknown  RecoverySideEffectClassification = "unknown"
)

type RecoveryTestClassification string

const (
	RecoveryTestsPassed  RecoveryTestClassification = "passed"
	RecoveryTestsFailed  RecoveryTestClassification = "failed"
	RecoveryTestsUnknown RecoveryTestClassification = "unknown"
)

type RecoveryCheckpointRepository struct {
	RepositoryID      string `json:"repository_id"`
	Branch            string `json:"branch"`
	CommitSHA         string `json:"commit_sha"`
	PatchDigestSHA256 string `json:"patch_digest_sha256,omitempty"`
}

func TrustedRecoveryCheckpointDigest(value TrustedRecoveryCheckpoint) string {
	digest, err := CommandContentDigest(map[string]any{
		"checkpoint_id": value.CheckpointID, "collector": value.Collector,
		"repositories": value.Repositories, "repository_set_digest": value.RepositorySetDigest,
		"safety_classification": value.SafetyClassification, "schema_version": value.SchemaVersion,
		"side_effect_classification": value.SideEffectClassification, "test_classification": value.TestClassification,
	})
	if err != nil {
		return ""
	}
	return digest
}

func RecoveryRepositorySetDigest(repositories []RecoveryCheckpointRepository) string {
	digest, err := CommandContentDigest(repositories)
	if err != nil {
		return ""
	}
	return digest
}

type SanitizedLogRedaction struct {
	SchemaVersion  int      `json:"schema_version"`
	PolicyVersion  string   `json:"policy_version"`
	Collector      string   `json:"collector"`
	RedactionCount int      `json:"redaction_count"`
	Categories     []string `json:"categories"`
}

type SanitizedLogRedactionCategory string

const (
	SanitizedLogCommandArgument SanitizedLogRedactionCategory = "command_argument"
	SanitizedLogCredential      SanitizedLogRedactionCategory = "credential"
	SanitizedLogEnvironment     SanitizedLogRedactionCategory = "environment"
	SanitizedLogFilesystemPath  SanitizedLogRedactionCategory = "filesystem_path"
	SanitizedLogPersonalData    SanitizedLogRedactionCategory = "personal_data"
	SanitizedLogPrompt          SanitizedLogRedactionCategory = "prompt"
	SanitizedLogToolOutput      SanitizedLogRedactionCategory = "tool_output"
)

type SanitizedLogSource string

const (
	SanitizedLogAgentStatus  SanitizedLogSource = "agent_status"
	SanitizedLogRunnerStatus SanitizedLogSource = "runner_status"
	SanitizedLogToolSummary  SanitizedLogSource = "tool_summary"
	SanitizedLogCheckSummary SanitizedLogSource = "check_summary"
)

type SanitizedLogEvent struct {
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
	Source              SanitizedLogSource    `json:"source"`
	Summary             string                `json:"summary"`
	Detail              string                `json:"detail,omitempty"`
	ContentDigestSHA256 string                `json:"content_digest_sha256"`
	Truncated           bool                  `json:"truncated"`
	Redaction           SanitizedLogRedaction `json:"redaction"`
}

func SanitizedLogContentDigest(value SanitizedLogEvent) string {
	material := map[string]any{
		"source": value.Source, "summary": value.Summary, "truncated": value.Truncated,
		"redaction": value.Redaction,
	}
	if value.Detail != "" {
		material["detail"] = value.Detail
	}
	digest, err := CommandContentDigest(material)
	if err != nil {
		return ""
	}
	return digest
}
