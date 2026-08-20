package v1

import "time"

const (
	SourcePublicationOperationAuthorityVersion = 1
	SourcePublicationSchemaVersion             = 1
	SourcePublicationTransportVersion          = 1
)

type SourcePublicationOperationAuthority struct {
	SchemaVersion             int       `json:"schema_version"`
	SourcePublicationEffectID string    `json:"source_publication_effect_id"`
	ReviewReadyRequestID      string    `json:"review_ready_request_id"`
	ProjectID                 string    `json:"project_id"`
	WorkflowRunID             string    `json:"workflow_run_id"`
	AttemptID                 string    `json:"attempt_id"`
	ExecutionManifestID       string    `json:"execution_manifest_id"`
	ExecutionManifestDigest   string    `json:"execution_manifest_digest"`
	LeaseID                   string    `json:"lease_id"`
	FencingToken              uint64    `json:"fencing_token"`
	LeaseExpiresAt            time.Time `json:"lease_expires_at"`
}

type SourcePublicationOperation struct {
	SourcePublicationOperationAuthority
	Publication SourcePublicationCommand `json:"publication"`
}

type SourcePublicationCommand struct {
	SchemaVersion        int    `json:"schema_version"`
	Provider             string `json:"provider"`
	InstallationID       string `json:"installation_id"`
	RepositoryID         string `json:"repository_id"`
	RepositoryExternalID string `json:"repository_external_id"`
	BaseBranch           string `json:"base_branch"`
	HeadBranch           string `json:"head_branch"`
	HeadSHA              string `json:"head_sha"`
	IdempotencyKey       string `json:"idempotency_key"`
	Title                string `json:"title"`
}

type SourcePublicationResult struct {
	SchemaVersion        int    `json:"schema_version"`
	Provider             string `json:"provider"`
	InstallationID       string `json:"installation_id"`
	RepositoryID         string `json:"repository_id"`
	RepositoryExternalID string `json:"repository_external_id"`
	BaseBranch           string `json:"base_branch"`
	HeadBranch           string `json:"head_branch"`
	HeadSHA              string `json:"head_sha"`
	IdempotencyKey       string `json:"idempotency_key"`
	ExternalRequestID    string `json:"external_request_id"`
	URL                  string `json:"url"`
	State                string `json:"state"`
}

type SourceControlErrorCode string

const (
	SourceControlAuthenticationFailed    SourceControlErrorCode = "authentication_failed"
	SourceControlInstallationSuspended   SourceControlErrorCode = "installation_suspended"
	SourceControlInstallationRevoked     SourceControlErrorCode = "installation_revoked"
	SourceControlPermissionDenied        SourceControlErrorCode = "permission_denied"
	SourceControlRepositoryNotFound      SourceControlErrorCode = "repository_not_found"
	SourceControlBranchNotFound          SourceControlErrorCode = "branch_not_found"
	SourceControlHeadMismatch            SourceControlErrorCode = "head_mismatch"
	SourceControlRateLimited             SourceControlErrorCode = "rate_limited"
	SourceControlProviderUnavailable     SourceControlErrorCode = "provider_unavailable"
	SourceControlInvalidProviderResponse SourceControlErrorCode = "invalid_provider_response"
)

type SourcePublicationOutcomeKind string

const (
	SourcePublicationSucceeded SourcePublicationOutcomeKind = "succeeded"
	SourcePublicationRetryable SourcePublicationOutcomeKind = "retryable"
	SourcePublicationPermanent SourcePublicationOutcomeKind = "permanent"
)

type SourcePublicationResultOutcome struct {
	Kind         SourcePublicationOutcomeKind `json:"kind"`
	Result       *SourcePublicationResult     `json:"result,omitempty"`
	Code         SourceControlErrorCode       `json:"code,omitempty"`
	RetryAfterMS *uint64                      `json:"retry_after_ms,omitempty"`
}

const EventSourcePublicationResult = "source_publication_result"

type SourcePublicationResultEvent struct {
	Kind string `json:"kind"`
	SourcePublicationOperationAuthority
	CommandMessageID string                         `json:"command_message_id"`
	ObservedAt       time.Time                      `json:"observed_at"`
	Outcome          SourcePublicationResultOutcome `json:"outcome"`
}
