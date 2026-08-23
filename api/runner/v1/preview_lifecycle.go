package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const (
	PreviewLifecycleSchemaVersion             = 1
	PreviewLifecycleTransportVersion          = 1
	PreviewLifecycleControllerProtocolVersion = 1
)

type PreviewLifecycleAction string

const (
	PreviewLifecycleDestroy PreviewLifecycleAction = "destroy"
	PreviewLifecycleExtend  PreviewLifecycleAction = "extend"
)

type PreviewLifecycleOperationAuthority struct {
	SchemaVersion                  int                    `json:"schema_version"`
	PreviewLifecycleEffectID       string                 `json:"preview_lifecycle_effect_id"`
	PreviewLifecycleEffectRevision uint64                 `json:"preview_lifecycle_effect_revision"`
	PreviewLifecycleOperationID    string                 `json:"preview_lifecycle_operation_id"`
	OrganizationID                 string                 `json:"organization_id"`
	ProjectID                      string                 `json:"project_id"`
	WorkflowRunID                  string                 `json:"workflow_run_id"`
	PreviewEvidenceID              string                 `json:"preview_evidence_id"`
	PreviewObservationID           string                 `json:"preview_observation_id"`
	PreviewObservationDigest       string                 `json:"preview_observation_digest"`
	ExecutionManifestID            string                 `json:"execution_manifest_id"`
	ExecutionManifestDigest        string                 `json:"execution_manifest_digest"`
	Action                         PreviewLifecycleAction `json:"action"`
	IdempotencyKey                 string                 `json:"idempotency_key"`
	ResourceBindingDigest          string                 `json:"resource_binding_digest"`
	OperationDigest                string                 `json:"operation_digest"`
	CommandDigest                  string                 `json:"command_digest"`
	LeaseID                        string                 `json:"lease_id"`
	FencingToken                   uint64                 `json:"fencing_token"`
	LeaseExpiresAt                 time.Time              `json:"lease_expires_at"`
}

type PreviewLifecycleOperation struct {
	PreviewLifecycleOperationAuthority
	Blueprint                 PreviewObservationBlueprint `json:"blueprint"`
	PreviewURL                string                      `json:"preview_url"`
	CommitSet                 []PreviewObservationCommit  `json:"commit_set"`
	CommitSetDigest           string                      `json:"commit_set_digest"`
	OriginalExpiresAt         time.Time                   `json:"original_expires_at"`
	CurrentEffectiveExpiresAt time.Time                   `json:"current_effective_expires_at"`
	DesiredExpiresAt          *time.Time                  `json:"desired_expires_at,omitempty"`
}

// PreviewLifecycleCommitSetDigest binds the exact already-sorted commit set.
func PreviewLifecycleCommitSetDigest(commits []PreviewObservationCommit) string {
	canonical := make([]map[string]any, len(commits))
	for index, commit := range commits {
		canonical[index] = map[string]any{
			"commit_sha": commit.CommitSHA, "repository_id": commit.RepositoryID,
		}
	}
	return previewLifecycleCanonicalDigest(canonical)
}

// PreviewLifecycleResourceBindingDigest binds the immutable controller target.
func PreviewLifecycleResourceBindingDigest(operation PreviewLifecycleOperation) string {
	return previewLifecycleCanonicalDigest(map[string]any{
		"organization_id":            operation.OrganizationID,
		"project_id":                 operation.ProjectID,
		"workflow_run_id":            operation.WorkflowRunID,
		"preview_evidence_id":        operation.PreviewEvidenceID,
		"preview_observation_id":     operation.PreviewObservationID,
		"preview_observation_digest": operation.PreviewObservationDigest,
		"execution_manifest_id":      operation.ExecutionManifestID,
		"execution_manifest_digest":  operation.ExecutionManifestDigest,
		"blueprint": map[string]any{
			"blueprint_id": operation.Blueprint.BlueprintID,
			"version":      operation.Blueprint.Version,
			"digest":       operation.Blueprint.Digest,
		},
		"preview_url":         operation.PreviewURL,
		"commit_set":          previewLifecycleCanonicalCommits(operation.CommitSet),
		"commit_set_digest":   operation.CommitSetDigest,
		"original_expires_at": operation.OriginalExpiresAt,
	})
}

// PreviewLifecycleOperationDigest binds stable intent but excludes its digest,
// delivery digest, and the replaceable lease/fence used for one attempt.
func PreviewLifecycleOperationDigest(operation PreviewLifecycleOperation) string {
	material := map[string]any{
		"schema_version":                    operation.SchemaVersion,
		"preview_lifecycle_effect_id":       operation.PreviewLifecycleEffectID,
		"preview_lifecycle_effect_revision": operation.PreviewLifecycleEffectRevision,
		"preview_lifecycle_operation_id":    operation.PreviewLifecycleOperationID,
		"organization_id":                   operation.OrganizationID,
		"project_id":                        operation.ProjectID,
		"workflow_run_id":                   operation.WorkflowRunID,
		"preview_evidence_id":               operation.PreviewEvidenceID,
		"preview_observation_id":            operation.PreviewObservationID,
		"preview_observation_digest":        operation.PreviewObservationDigest,
		"execution_manifest_id":             operation.ExecutionManifestID,
		"execution_manifest_digest":         operation.ExecutionManifestDigest,
		"blueprint": map[string]any{
			"blueprint_id": operation.Blueprint.BlueprintID,
			"version":      operation.Blueprint.Version,
			"digest":       operation.Blueprint.Digest,
		},
		"preview_url":                  operation.PreviewURL,
		"commit_set":                   previewLifecycleCanonicalCommits(operation.CommitSet),
		"commit_set_digest":            operation.CommitSetDigest,
		"original_expires_at":          operation.OriginalExpiresAt,
		"current_effective_expires_at": operation.CurrentEffectiveExpiresAt,
		"action":                       operation.Action,
		"idempotency_key":              operation.IdempotencyKey,
		"resource_binding_digest":      operation.ResourceBindingDigest,
	}
	if operation.DesiredExpiresAt != nil {
		material["desired_expires_at"] = *operation.DesiredExpiresAt
	}
	return previewLifecycleCanonicalDigest(material)
}

// PreviewLifecycleCommandDigest binds one fenced delivery attempt to stable intent.
func PreviewLifecycleCommandDigest(operation PreviewLifecycleOperation) string {
	return previewLifecycleCanonicalDigest(map[string]any{
		"operation_digest":               operation.OperationDigest,
		"preview_lifecycle_operation_id": operation.PreviewLifecycleOperationID,
		"idempotency_key":                operation.IdempotencyKey,
		"lease_id":                       operation.LeaseID,
		"fencing_token":                  operation.FencingToken,
		"lease_expires_at":               operation.LeaseExpiresAt,
	})
}

func previewLifecycleCanonicalCommits(commits []PreviewObservationCommit) []map[string]any {
	canonical := make([]map[string]any, len(commits))
	for index, commit := range commits {
		canonical[index] = map[string]any{"commit_sha": commit.CommitSHA, "repository_id": commit.RepositoryID}
	}
	return canonical
}

func previewLifecycleCanonicalDigest(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

type PreviewLifecycleResultAuthority struct {
	PreviewLifecycleOperationAuthority
	Blueprint                 PreviewObservationBlueprint `json:"blueprint"`
	PreviewURL                string                      `json:"preview_url"`
	CommitSet                 []PreviewObservationCommit  `json:"commit_set"`
	CommitSetDigest           string                      `json:"commit_set_digest"`
	OriginalExpiresAt         time.Time                   `json:"original_expires_at"`
	CurrentEffectiveExpiresAt time.Time                   `json:"current_effective_expires_at"`
	DesiredExpiresAt          *time.Time                  `json:"desired_expires_at,omitempty"`
}

func PreviewLifecycleResultAuthorityFromOperation(operation PreviewLifecycleOperation) PreviewLifecycleResultAuthority {
	return PreviewLifecycleResultAuthority{
		PreviewLifecycleOperationAuthority: operation.PreviewLifecycleOperationAuthority,
		Blueprint:                          operation.Blueprint,
		PreviewURL:                         operation.PreviewURL,
		CommitSet:                          append([]PreviewObservationCommit(nil), operation.CommitSet...),
		CommitSetDigest:                    operation.CommitSetDigest,
		OriginalExpiresAt:                  operation.OriginalExpiresAt,
		CurrentEffectiveExpiresAt:          operation.CurrentEffectiveExpiresAt,
		DesiredExpiresAt:                   cloneTime(operation.DesiredExpiresAt),
	}
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

type PreviewLifecycleOutcomeKind string

const (
	PreviewLifecycleSucceeded PreviewLifecycleOutcomeKind = "succeeded"
	PreviewLifecycleRetryable PreviewLifecycleOutcomeKind = "retryable"
	PreviewLifecyclePermanent PreviewLifecycleOutcomeKind = "permanent"
	PreviewLifecycleUncertain PreviewLifecycleOutcomeKind = "uncertain"
)

type PreviewLifecycleErrorCode string

const (
	PreviewLifecycleControllerUnavailable     PreviewLifecycleErrorCode = "controller_unavailable"
	PreviewLifecycleRateLimited               PreviewLifecycleErrorCode = "rate_limited"
	PreviewLifecycleTransientResourceConflict PreviewLifecycleErrorCode = "transient_resource_conflict"
	PreviewLifecycleProfileUnavailable        PreviewLifecycleErrorCode = "profile_unavailable"
	PreviewLifecycleResourceBindingMismatch   PreviewLifecycleErrorCode = "resource_binding_mismatch"
	PreviewLifecycleResourceNotFound          PreviewLifecycleErrorCode = "resource_not_found"
	PreviewLifecycleInvalidControllerResponse PreviewLifecycleErrorCode = "invalid_controller_response"
	PreviewLifecyclePolicyDenied              PreviewLifecycleErrorCode = "policy_denied"
	PreviewLifecycleControllerTimeout         PreviewLifecycleErrorCode = "controller_timeout"
	PreviewLifecycleReceiptUnavailable        PreviewLifecycleErrorCode = "receipt_unavailable"
)

type PreviewLifecycleResourceKind string

const (
	PreviewLifecycleResourceActive    PreviewLifecycleResourceKind = "active"
	PreviewLifecycleResourceDestroyed PreviewLifecycleResourceKind = "destroyed"
)

type PreviewLifecycleDestroyDisposition string

const (
	PreviewLifecycleDestroyed     PreviewLifecycleDestroyDisposition = "destroyed"
	PreviewLifecycleAlreadyAbsent PreviewLifecycleDestroyDisposition = "already_absent"
)

type PreviewLifecycleResourceState struct {
	Kind        PreviewLifecycleResourceKind       `json:"kind"`
	PreviewURL  string                             `json:"preview_url,omitempty"`
	Blueprint   *PreviewObservationBlueprint       `json:"blueprint,omitempty"`
	CommitSet   []PreviewObservationCommit         `json:"commit_set,omitempty"`
	ExpiresAt   *time.Time                         `json:"expires_at,omitempty"`
	Disposition PreviewLifecycleDestroyDisposition `json:"disposition,omitempty"`
}

type PreviewLifecycleResultOutcome struct {
	Kind                    PreviewLifecycleOutcomeKind    `json:"kind"`
	ControllerReceiptDigest string                         `json:"controller_receipt_digest,omitempty"`
	ResourceBindingDigest   string                         `json:"resource_binding_digest,omitempty"`
	ResourceState           *PreviewLifecycleResourceState `json:"resource_state,omitempty"`
	Code                    PreviewLifecycleErrorCode      `json:"code,omitempty"`
	RetryAfterMS            *uint64                        `json:"retry_after_ms,omitempty"`
}

const EventPreviewLifecycleResult = "preview_lifecycle_result"

type PreviewLifecycleResultEvent struct {
	Kind string `json:"kind"`
	PreviewLifecycleResultAuthority
	CommandMessageID string                        `json:"command_message_id"`
	ObservedAt       time.Time                     `json:"observed_at"`
	Outcome          PreviewLifecycleResultOutcome `json:"outcome"`
}

type PreviewLifecycleProfileCapability struct {
	BlueprintID               string                   `json:"blueprint_id"`
	Version                   string                   `json:"version"`
	Digest                    string                   `json:"digest"`
	ControllerProtocolVersion int                      `json:"controller_protocol_version"`
	Actions                   []PreviewLifecycleAction `json:"actions"`
	MaxExtensionMS            *uint64                  `json:"max_extension_ms,omitempty"`
}
