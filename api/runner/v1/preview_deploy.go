package v1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

const (
	PreviewDeploySchemaVersion          = 1
	PreviewDeployResultSchemaVersion    = 1
	PreviewHostedDestroyedSchemaVersion = 1
	PreviewHostingProtocolVersion       = 1
	PreviewHostingTransportVersion      = 1
	PreviewDeployLogTailMaxBytes        = 16 << 10
	PreviewDeployLogTailMaxLines        = 200
	previewSecretsRedacted              = "[redacted]"
)

type PreviewDeployOperationAuthority struct {
	SchemaVersion               int       `json:"schema_version"`
	PreviewDeployEffectID       string    `json:"preview_deploy_effect_id"`
	PreviewDeployEffectRevision uint64    `json:"preview_deploy_effect_revision"`
	PreviewDeployOperationID    string    `json:"preview_deploy_operation_id"`
	PreviewID                   string    `json:"preview_id"`
	OrganizationID              string    `json:"organization_id"`
	ProjectID                   string    `json:"project_id"`
	WorkflowRunID               string    `json:"workflow_run_id"`
	ExecutionManifestID         string    `json:"execution_manifest_id"`
	ExecutionManifestDigest     string    `json:"execution_manifest_digest"`
	IdempotencyKey              string    `json:"idempotency_key"`
	ResourceBindingDigest       string    `json:"resource_binding_digest"`
	OperationDigest             string    `json:"operation_digest"`
	CommandDigest               string    `json:"command_digest"`
	LeaseID                     string    `json:"lease_id"`
	FencingToken                uint64    `json:"fencing_token"`
	LeaseExpiresAt              time.Time `json:"lease_expires_at"`
}

type PreviewDeployBlueprint struct {
	BlueprintID string          `json:"blueprint_id"`
	Version     string          `json:"version"`
	Digest      string          `json:"digest"`
	Content     json.RawMessage `json:"content"`
}

func (blueprint PreviewDeployBlueprint) Identity() PreviewObservationBlueprint {
	return PreviewObservationBlueprint{BlueprintID: blueprint.BlueprintID, Version: blueprint.Version, Digest: blueprint.Digest}
}

type PreviewDeployOperation struct {
	PreviewDeployOperationAuthority
	Blueprint       PreviewDeployBlueprint     `json:"blueprint"`
	CommitSet       []PreviewObservationCommit `json:"commit_set"`
	CommitSetDigest string                     `json:"commit_set_digest"`
	PreviewKey      string                     `json:"preview_key"`
	EdgeHost        string                     `json:"edge_host"`
	ExpiresAt       time.Time                  `json:"expires_at"`
}

type PreviewDeployResultAuthority struct {
	PreviewDeployOperationAuthority
	Blueprint       PreviewObservationBlueprint `json:"blueprint"`
	CommitSet       []PreviewObservationCommit  `json:"commit_set"`
	CommitSetDigest string                      `json:"commit_set_digest"`
	PreviewKey      string                      `json:"preview_key"`
	EdgeHost        string                      `json:"edge_host"`
	ExpiresAt       time.Time                   `json:"expires_at"`
}

func PreviewDeployResultAuthorityFromOperation(operation PreviewDeployOperation) PreviewDeployResultAuthority {
	return PreviewDeployResultAuthority{
		PreviewDeployOperationAuthority: operation.PreviewDeployOperationAuthority,
		Blueprint:                       operation.Blueprint.Identity(),
		CommitSet:                       append([]PreviewObservationCommit(nil), operation.CommitSet...),
		CommitSetDigest:                 operation.CommitSetDigest,
		PreviewKey:                      operation.PreviewKey,
		EdgeHost:                        operation.EdgeHost,
		ExpiresAt:                       operation.ExpiresAt,
	}
}

type previewDeployDigestInput struct {
	authority       PreviewDeployOperationAuthority
	blueprint       PreviewObservationBlueprint
	commitSet       []PreviewObservationCommit
	commitSetDigest string
	previewKey      string
	edgeHost        string
	expiresAt       time.Time
}

func (authority PreviewDeployResultAuthority) digestInput() previewDeployDigestInput {
	return previewDeployDigestInput{
		authority: authority.PreviewDeployOperationAuthority, blueprint: authority.Blueprint,
		commitSet: authority.CommitSet, commitSetDigest: authority.CommitSetDigest,
		previewKey: authority.PreviewKey, edgeHost: authority.EdgeHost, expiresAt: authority.ExpiresAt,
	}
}

func (operation PreviewDeployOperation) digestInput() previewDeployDigestInput {
	return PreviewDeployResultAuthorityFromOperation(operation).digestInput()
}

func previewDeployBindingMaterial(input previewDeployDigestInput) map[string]any {
	return map[string]any{
		"organization_id":           input.authority.OrganizationID,
		"project_id":                input.authority.ProjectID,
		"workflow_run_id":           input.authority.WorkflowRunID,
		"preview_id":                input.authority.PreviewID,
		"preview_key":               input.previewKey,
		"execution_manifest_id":     input.authority.ExecutionManifestID,
		"execution_manifest_digest": input.authority.ExecutionManifestDigest,
		"blueprint":                 previewCanonicalBlueprint(input.blueprint),
		"commit_set":                previewCanonicalCommits(input.commitSet),
		"commit_set_digest":         input.commitSetDigest,
		"edge_host":                 input.edgeHost,
		"expires_at":                previewLifecycleCanonicalTime(input.expiresAt),
	}
}

func PreviewDeployResourceBindingDigest(operation PreviewDeployOperation) string {
	return PreviewCanonicalDigest(previewDeployBindingMaterial(operation.digestInput()))
}

func PreviewDeployOperationDigest(operation PreviewDeployOperation) string {
	return previewDeployOperationDigest(operation.digestInput())
}

func PreviewDeployCommandDigest(operation PreviewDeployOperation) string {
	return previewDeployCommandDigest(operation.PreviewDeployOperationAuthority)
}

func PreviewDeployResultDigests(authority PreviewDeployResultAuthority) (resourceBinding, operation, command string) {
	input := authority.digestInput()
	return PreviewCanonicalDigest(previewDeployBindingMaterial(input)), previewDeployOperationDigest(input),
		previewDeployCommandDigest(authority.PreviewDeployOperationAuthority)
}

func previewDeployOperationDigest(input previewDeployDigestInput) string {
	material := previewDeployBindingMaterial(input)
	material["schema_version"] = previewCanonicalInteger(uint64(input.authority.SchemaVersion))
	material["preview_deploy_effect_id"] = input.authority.PreviewDeployEffectID
	material["preview_deploy_effect_revision"] = previewCanonicalInteger(input.authority.PreviewDeployEffectRevision)
	material["preview_deploy_operation_id"] = input.authority.PreviewDeployOperationID
	material["idempotency_key"] = input.authority.IdempotencyKey
	material["resource_binding_digest"] = input.authority.ResourceBindingDigest
	return PreviewCanonicalDigest(material)
}

func previewDeployCommandDigest(authority PreviewDeployOperationAuthority) string {
	return PreviewCanonicalDigest(map[string]any{
		"operation_digest":            authority.OperationDigest,
		"preview_deploy_operation_id": authority.PreviewDeployOperationID,
		"idempotency_key":             authority.IdempotencyKey,
		"lease_id":                    authority.LeaseID,
		"fencing_token":               previewCanonicalInteger(authority.FencingToken),
		"lease_expires_at":            previewLifecycleCanonicalTime(authority.LeaseExpiresAt),
	})
}

func PreviewCanonicalDigest(value any) string {
	var out strings.Builder
	if err := writePreviewBlueprintCanonicalJSON(&out, value); err != nil {
		return ""
	}
	digest := sha256.Sum256([]byte(out.String()))
	return hex.EncodeToString(digest[:])
}

func previewCanonicalInteger(value uint64) json.Number {
	return json.Number(strconv.FormatUint(value, 10))
}

func previewCanonicalBlueprint(blueprint PreviewObservationBlueprint) map[string]any {
	return map[string]any{"blueprint_id": blueprint.BlueprintID, "version": blueprint.Version, "digest": blueprint.Digest}
}

func previewCanonicalCommits(commits []PreviewObservationCommit) []any {
	canonical := make([]any, len(commits))
	for index, commit := range commits {
		canonical[index] = map[string]any{"commit_sha": commit.CommitSHA, "repository_id": commit.RepositoryID}
	}
	return canonical
}

type PreviewDeployOutcomeKind string

const (
	PreviewDeployDeployed          PreviewDeployOutcomeKind = "deployed"
	PreviewDeployFailed            PreviewDeployOutcomeKind = "failed"
	PreviewDeployCapacityExhausted PreviewDeployOutcomeKind = "capacity_exhausted"
	PreviewDeployUncertain         PreviewDeployOutcomeKind = "uncertain"
)

type PreviewDeployPhase string

const (
	PreviewDeployPhaseValidate     PreviewDeployPhase = "validate"
	PreviewDeployPhaseCommitSet    PreviewDeployPhase = "commit_set"
	PreviewDeployPhaseCheckout     PreviewDeployPhase = "checkout"
	PreviewDeployPhasePull         PreviewDeployPhase = "pull"
	PreviewDeployPhaseServices     PreviewDeployPhase = "services"
	PreviewDeployPhaseInstall      PreviewDeployPhase = "install"
	PreviewDeployPhaseBuild        PreviewDeployPhase = "build"
	PreviewDeployPhaseMigrate      PreviewDeployPhase = "migrate"
	PreviewDeployPhaseSeed         PreviewDeployPhase = "seed"
	PreviewDeployPhaseStart        PreviewDeployPhase = "start"
	PreviewDeployPhaseHealth       PreviewDeployPhase = "health"
	PreviewDeployPhaseLeaseExpired PreviewDeployPhase = "lease_expired"
)

func (phase PreviewDeployPhase) Valid() bool {
	switch phase {
	case PreviewDeployPhaseValidate, PreviewDeployPhaseCommitSet, PreviewDeployPhaseCheckout, PreviewDeployPhasePull,
		PreviewDeployPhaseServices, PreviewDeployPhaseInstall, PreviewDeployPhaseBuild, PreviewDeployPhaseMigrate,
		PreviewDeployPhaseSeed, PreviewDeployPhaseStart, PreviewDeployPhaseHealth, PreviewDeployPhaseLeaseExpired:
		return true
	}
	return false
}

type PreviewDeployOutcome struct {
	Kind    PreviewDeployOutcomeKind  `json:"kind"`
	Phase   PreviewDeployPhase        `json:"phase,omitempty"`
	Checks  []PreviewObservationCheck `json:"checks,omitempty"`
	LogTail string                    `json:"log_tail,omitempty"`
}

const EventPreviewDeployResult = "preview_deploy_result"

type PreviewDeployResultEvent struct {
	Kind string `json:"kind"`
	PreviewDeployResultAuthority
	CommandMessageID string               `json:"command_message_id"`
	ObservedAt       time.Time            `json:"observed_at"`
	Outcome          PreviewDeployOutcome `json:"outcome"`
}

func (event PreviewDeployResultEvent) MarshalJSON() ([]byte, error) {
	type alias PreviewDeployResultEvent
	return json.Marshal(struct {
		alias
		LeaseExpiresAt string `json:"lease_expires_at"`
		ExpiresAt      string `json:"expires_at"`
		ObservedAt     string `json:"observed_at"`
	}{
		alias:          alias(event),
		LeaseExpiresAt: previewLifecycleCanonicalTime(event.LeaseExpiresAt),
		ExpiresAt:      previewLifecycleCanonicalTime(event.ExpiresAt),
		ObservedAt:     previewLifecycleCanonicalTime(event.ObservedAt),
	})
}

func PreviewDeployResultEventID(idempotencyKey string) string {
	digest := sha256.Sum256([]byte(idempotencyKey))
	return "preview-deploy-result-" + hex.EncodeToString(digest[:])
}

type PreviewHostedDestroyReason string

const (
	PreviewHostedDestroyExpired          PreviewHostedDestroyReason = "expired"
	PreviewHostedDestroyEmergencyStop    PreviewHostedDestroyReason = "emergency_stop"
	PreviewHostedDestroyReconcileExpired PreviewHostedDestroyReason = "reconcile_expired"
)

type PreviewHostedDestroyOutcome string

const (
	PreviewHostedDestroyOutcomeDestroyed PreviewHostedDestroyOutcome = "destroyed"
	PreviewHostedDestroyOutcomeUncertain PreviewHostedDestroyOutcome = "uncertain"
)

const EventPreviewHostedDestroyed = "preview_hosted_destroyed"

type PreviewHostedDestroyedSubject struct {
	PreviewID      string
	OrganizationID string
	ProjectID      string
	PreviewKey     string
	IdempotencyKey string
}

type PreviewHostedDestroyedEvent struct {
	Kind           string                      `json:"kind"`
	SchemaVersion  int                         `json:"schema_version"`
	PreviewID      string                      `json:"preview_id"`
	OrganizationID string                      `json:"organization_id"`
	ProjectID      string                      `json:"project_id"`
	PreviewKey     string                      `json:"preview_key"`
	IdempotencyKey string                      `json:"idempotency_key"`
	DestroyedAt    time.Time                   `json:"destroyed_at"`
	Reason         PreviewHostedDestroyReason  `json:"reason"`
	Outcome        PreviewHostedDestroyOutcome `json:"outcome"`
}

func (event PreviewHostedDestroyedEvent) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Kind           string                      `json:"kind"`
		SchemaVersion  int                         `json:"schema_version"`
		PreviewID      string                      `json:"preview_id"`
		OrganizationID string                      `json:"organization_id"`
		ProjectID      string                      `json:"project_id"`
		PreviewKey     string                      `json:"preview_key"`
		IdempotencyKey string                      `json:"idempotency_key"`
		DestroyedAt    string                      `json:"destroyed_at"`
		Reason         PreviewHostedDestroyReason  `json:"reason"`
		Outcome        PreviewHostedDestroyOutcome `json:"outcome"`
	}{
		Kind: event.Kind, SchemaVersion: event.SchemaVersion, PreviewID: event.PreviewID,
		OrganizationID: event.OrganizationID, ProjectID: event.ProjectID, PreviewKey: event.PreviewKey,
		IdempotencyKey: event.IdempotencyKey, DestroyedAt: previewLifecycleCanonicalTime(event.DestroyedAt),
		Reason: event.Reason, Outcome: event.Outcome,
	})
}

func NewPreviewHostedDestroyedEvent(
	subject PreviewHostedDestroyedSubject,
	destroyedAt time.Time,
	reason PreviewHostedDestroyReason,
	outcome PreviewHostedDestroyOutcome,
) PreviewHostedDestroyedEvent {
	return PreviewHostedDestroyedEvent{
		Kind: EventPreviewHostedDestroyed, SchemaVersion: PreviewHostedDestroyedSchemaVersion,
		PreviewID: subject.PreviewID, OrganizationID: subject.OrganizationID, ProjectID: subject.ProjectID,
		PreviewKey: subject.PreviewKey, IdempotencyKey: subject.IdempotencyKey,
		DestroyedAt: destroyedAt.UTC().Truncate(time.Millisecond), Reason: reason, Outcome: outcome,
	}
}

func PreviewHostedDestroyedEventID(previewID, idempotencyKey string, outcome PreviewHostedDestroyOutcome) string {
	digest := sha256.Sum256([]byte(previewID + "\x00" + idempotencyKey + "\x00" + string(outcome)))
	return "preview-hosted-destroyed-" + hex.EncodeToString(digest[:])
}

type PreviewHostingCapability struct {
	ProtocolVersion int                      `json:"protocol_version"`
	MaxConcurrent   int                      `json:"max_concurrent"`
	Actions         []PreviewLifecycleAction `json:"actions"`
	MaxExtensionMS  *uint64                  `json:"max_extension_ms,omitempty"`
}

type PreviewSecrets map[string]string

func (PreviewSecrets) String() string {
	return previewSecretsRedacted
}

func (PreviewSecrets) GoString() string {
	return "v1.PreviewSecrets{" + previewSecretsRedacted + "}"
}

func (secrets PreviewSecrets) Format(state fmt.State, verb rune) {
	if verb == 'v' && state.Flag('#') {
		_, _ = io.WriteString(state, secrets.GoString())
		return
	}
	_, _ = io.WriteString(state, previewSecretsRedacted)
}

func (PreviewSecrets) MarshalJSON() ([]byte, error) {
	return []byte(`"` + previewSecretsRedacted + `"`), nil
}

func (PreviewSecrets) LogValue() slog.Value {
	return slog.StringValue(previewSecretsRedacted)
}

func (secrets *PreviewSecrets) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*secrets = nil
		return nil
	}
	decoded := map[string]string{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("decode preview secrets: invalid shape")
	}
	*secrets = decoded
	return nil
}
