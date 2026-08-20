package v1

import "time"

const (
	PreviewObservationOperationAuthorityVersion = 1
	PreviewObservationSchemaVersion             = 1
	PreviewObservationTransportVersion          = 1
)

type PreviewObservationOperationAuthority struct {
	SchemaVersion              int       `json:"schema_version"`
	PreviewObservationEffectID string    `json:"preview_observation_effect_id"`
	PreviewEvidenceID          string    `json:"preview_evidence_id"`
	ReviewReadyRequestID       string    `json:"review_ready_request_id"`
	ProjectID                  string    `json:"project_id"`
	WorkflowRunID              string    `json:"workflow_run_id"`
	AttemptID                  string    `json:"attempt_id"`
	ExecutionManifestID        string    `json:"execution_manifest_id"`
	ExecutionManifestDigest    string    `json:"execution_manifest_digest"`
	LeaseID                    string    `json:"lease_id"`
	FencingToken               uint64    `json:"fencing_token"`
	LeaseExpiresAt             time.Time `json:"lease_expires_at"`
}

type PreviewObservationBlueprint struct {
	BlueprintID string `json:"blueprint_id"`
	Version     string `json:"version"`
	Digest      string `json:"digest"`
}

type PreviewObservationCommit struct {
	RepositoryID string `json:"repository_id"`
	CommitSHA    string `json:"commit_sha"`
}

type PreviewObservationOperation struct {
	PreviewObservationOperationAuthority
	Blueprint              PreviewObservationBlueprint `json:"blueprint"`
	PreviewURL             string                      `json:"preview_url"`
	PreviewExpiresAt       time.Time                   `json:"preview_expires_at"`
	CommitSet              []PreviewObservationCommit  `json:"commit_set"`
	RequiredHealthCheckIDs []string                    `json:"required_health_check_ids"`
}

type PreviewObservationCheckStatus string

const (
	PreviewObservationCheckPassed PreviewObservationCheckStatus = "passed"
	PreviewObservationCheckFailed PreviewObservationCheckStatus = "failed"
)

type PreviewObservationCheck struct {
	CheckID    string                        `json:"check_id"`
	Status     PreviewObservationCheckStatus `json:"status"`
	HTTPStatus int                           `json:"http_status"`
	DurationMS uint64                        `json:"duration_ms"`
}

type PreviewObservation struct {
	SchemaVersion int                        `json:"schema_version"`
	PreviewURL    string                     `json:"preview_url"`
	CommitSet     []PreviewObservationCommit `json:"commit_set"`
	Checks        []PreviewObservationCheck  `json:"checks"`
}

type PreviewObservationOutcomeKind string

const (
	PreviewObservationObserved  PreviewObservationOutcomeKind = "observed"
	PreviewObservationRetryable PreviewObservationOutcomeKind = "retryable"
	PreviewObservationPermanent PreviewObservationOutcomeKind = "permanent"
)

type PreviewObservationErrorCode string

const (
	PreviewObserverUnavailable     PreviewObservationErrorCode = "observer_unavailable"
	PreviewUnreachable             PreviewObservationErrorCode = "preview_unreachable"
	PreviewTLSVerificationFailed   PreviewObservationErrorCode = "tls_verification_failed"
	PreviewProfileUnavailable      PreviewObservationErrorCode = "profile_unavailable"
	PreviewTargetNotAllowed        PreviewObservationErrorCode = "target_not_allowed"
	PreviewInvalidObserverResponse PreviewObservationErrorCode = "invalid_observer_response"
)

type PreviewObservationResultOutcome struct {
	Kind         PreviewObservationOutcomeKind `json:"kind"`
	Observation  *PreviewObservation           `json:"observation,omitempty"`
	Code         PreviewObservationErrorCode   `json:"code,omitempty"`
	RetryAfterMS *uint64                       `json:"retry_after_ms,omitempty"`
}

const EventPreviewObservationResult = "preview_observation_result"

type PreviewObservationResultEvent struct {
	Kind string `json:"kind"`
	PreviewObservationOperationAuthority
	CommandMessageID string                          `json:"command_message_id"`
	ObservedAt       time.Time                       `json:"observed_at"`
	Outcome          PreviewObservationResultOutcome `json:"outcome"`
}

type PreviewObservationProfileCapability struct {
	BlueprintID    string   `json:"blueprint_id"`
	Version        string   `json:"version"`
	Digest         string   `json:"digest"`
	HealthCheckIDs []string `json:"health_check_ids"`
}
