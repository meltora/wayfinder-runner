package v1

import "time"

type JobFinishedState string

const (
	JobFinishedSucceeded   JobFinishedState = "succeeded"
	JobFinishedFailed      JobFinishedState = "failed"
	JobFinishedCancelled   JobFinishedState = "cancelled"
	JobFinishedQuarantined JobFinishedState = "quarantined"
)

type JobFinishedFailureCode string

const (
	JobFailureAgentStart               JobFinishedFailureCode = "agent_start_failed"
	JobFailureAgentExit                JobFinishedFailureCode = "agent_exit_failed"
	JobFailureWorkspacePreparation     JobFinishedFailureCode = "workspace_preparation_failed"
	JobFailureAgentPackMaterialization JobFinishedFailureCode = "agent_pack_materialization_failed"
	JobFailureBuildFinalization        JobFinishedFailureCode = "build_finalization_failed"
	JobFailureRequiredChecks           JobFinishedFailureCode = "required_checks_failed"
	JobFailureRunnerInternal           JobFinishedFailureCode = "runner_internal_error"
	JobFailureCancelled                JobFinishedFailureCode = "cancelled"
	JobFailureLeaseLost                JobFinishedFailureCode = "lease_lost"
	JobFailureCleanup                  JobFinishedFailureCode = "cleanup_failed"
	JobFailurePlanNotPublished         JobFinishedFailureCode = "plan_not_published"
	JobFailureRunTokenBudget           JobFinishedFailureCode = "run_token_budget_exceeded"
	JobFailureProjectTokenBudget       JobFinishedFailureCode = "project_token_budget_exceeded"
)

type AgentPackMaterializationFailureCategory string

const (
	AgentPackFailureDownloadUnavailable AgentPackMaterializationFailureCategory = "download_unavailable"
	AgentPackFailureStorageUnavailable  AgentPackMaterializationFailureCategory = "storage_unavailable"
	AgentPackFailureDigestMismatch      AgentPackMaterializationFailureCategory = "digest_mismatch"
	AgentPackFailureArchiveInvalid      AgentPackMaterializationFailureCategory = "archive_invalid"
)

type JobFinishedEvent struct {
	Kind              string                                  `json:"kind"`
	ManifestKind      JobKind                                 `json:"manifest_kind"`
	LeaseID           string                                  `json:"lease_id"`
	JobID             string                                  `json:"job_id"`
	AttemptID         string                                  `json:"attempt_id"`
	ManifestID        string                                  `json:"manifest_id"`
	ManifestDigest    string                                  `json:"manifest_digest"`
	FencingToken      uint64                                  `json:"fencing_token"`
	ObservedAt        time.Time                               `json:"observed_at"`
	State             JobFinishedState                        `json:"state"`
	ProviderSessionID string                                  `json:"provider_session_id,omitempty"`
	FailureCode       JobFinishedFailureCode                  `json:"failure_code,omitempty"`
	FailureCategory   AgentPackMaterializationFailureCategory `json:"failure_category,omitempty"`
	Retryable         *bool                                   `json:"retryable,omitempty"`
	FailureMessage    string                                  `json:"failure_message,omitempty"`
	AgentRunAuthority *AgentRunAuthority                      `json:"agent_run_authority,omitempty"`
	ProviderUsage     *ProviderUsage                          `json:"provider_usage,omitempty"`
}
