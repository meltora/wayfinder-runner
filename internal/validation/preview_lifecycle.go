package validation

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const (
	maxPreviewLifecycleExtension = 30 * 24 * time.Hour
	maxPreviewLifecycleRetryMS   = 24 * 60 * 60 * 1000
)

var (
	previewLifecycleOperationIDPattern = regexp.MustCompile(`^preview-lifecycle-operation-[0-9a-f]{64}$`)
	previewLifecycleIdempotencyPattern = regexp.MustCompile(`^preview-lifecycle-[0-9a-f]{64}$`)
	previewLifecycleCommandIDPattern   = regexp.MustCompile(`^preview-lifecycle-command-[0-9a-f]{64}$`)
)

func previewLifecycleAuthority(value v1.PreviewLifecycleOperationAuthority, hosted bool) error {
	if value.SchemaVersion != v1.PreviewLifecycleSchemaVersion {
		return fmt.Errorf("preview lifecycle schema_version is unsupported")
	}
	ids := map[string]string{
		"preview_lifecycle_effect_id": value.PreviewLifecycleEffectID,
		"organization_id":             value.OrganizationID,
		"project_id":                  value.ProjectID,
		"workflow_run_id":             value.WorkflowRunID,
		"execution_manifest_id":       value.ExecutionManifestID,
		"lease_id":                    value.LeaseID,
	}
	digests := map[string]string{
		"execution_manifest_digest": value.ExecutionManifestDigest,
		"resource_binding_digest":   value.ResourceBindingDigest,
		"operation_digest":          value.OperationDigest,
		"command_digest":            value.CommandDigest,
	}
	if hosted {
		if value.PreviewEvidenceID != "" || value.PreviewObservationID != "" || value.PreviewObservationDigest != "" {
			return fmt.Errorf("hosted preview lifecycle forbids external target authority")
		}
	} else {
		ids["preview_evidence_id"] = value.PreviewEvidenceID
		ids["preview_observation_id"] = value.PreviewObservationID
		digests["preview_observation_digest"] = value.PreviewObservationDigest
	}
	for field, id := range ids {
		if err := ID(field, id); err != nil {
			return err
		}
	}
	if value.PreviewLifecycleEffectRevision == 0 || value.PreviewLifecycleEffectRevision > maxJSONSafeInteger {
		return fmt.Errorf("preview lifecycle effect revision is invalid")
	}
	if !previewLifecycleOperationIDPattern.MatchString(value.PreviewLifecycleOperationID) {
		return fmt.Errorf("preview_lifecycle_operation_id is invalid")
	}
	for field, digest := range digests {
		if err := SHA256Digest(field, digest); err != nil {
			return err
		}
	}
	if value.Action != v1.PreviewLifecycleExtend && value.Action != v1.PreviewLifecycleDestroy {
		return fmt.Errorf("preview lifecycle action is invalid")
	}
	if !previewLifecycleIdempotencyPattern.MatchString(value.IdempotencyKey) {
		return fmt.Errorf("preview lifecycle idempotency_key is invalid")
	}
	if value.FencingToken == 0 || value.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("preview lifecycle fencing_token is invalid")
	}
	if value.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("preview lifecycle lease_expires_at is required")
	}
	return nil
}

func previewLifecycleBinding(
	blueprint v1.PreviewObservationBlueprint,
	previewURL string,
	commitSet []v1.PreviewObservationCommit,
	commitSetDigest string,
	originalExpiresAt time.Time,
	currentEffectiveExpiresAt time.Time,
	desiredExpiresAt *time.Time,
	action v1.PreviewLifecycleAction,
	hosted bool,
) error {
	if err := ID("blueprint_id", blueprint.BlueprintID); err != nil {
		return err
	}
	if err := boundedText("blueprint version", blueprint.Version, 128, true); err != nil || strings.TrimSpace(blueprint.Version) == "" {
		return fmt.Errorf("preview lifecycle blueprint version is invalid")
	}
	if err := SHA256Digest("blueprint digest", blueprint.Digest); err != nil {
		return err
	}
	if hosted {
		if previewURL != "" {
			return fmt.Errorf("hosted preview lifecycle forbids preview_url")
		}
	} else if err := previewObservationURL(previewURL); err != nil {
		return err
	}
	if err := validatePreviewObservationCommits(commitSet); err != nil {
		return err
	}
	if err := SHA256Digest("commit_set_digest", commitSetDigest); err != nil {
		return err
	}
	if originalExpiresAt.IsZero() || currentEffectiveExpiresAt.IsZero() {
		return fmt.Errorf("preview lifecycle expiry authority is required")
	}
	if currentEffectiveExpiresAt.Before(originalExpiresAt) {
		return fmt.Errorf("preview lifecycle current effective expiry cannot precede original expiry")
	}
	switch action {
	case v1.PreviewLifecycleExtend:
		if desiredExpiresAt == nil || !desiredExpiresAt.After(currentEffectiveExpiresAt) ||
			desiredExpiresAt.Sub(currentEffectiveExpiresAt) > maxPreviewLifecycleExtension {
			return fmt.Errorf("preview lifecycle desired_expires_at is invalid")
		}
	case v1.PreviewLifecycleDestroy:
		if desiredExpiresAt != nil {
			return fmt.Errorf("destroy preview lifecycle operation forbids desired_expires_at")
		}
	default:
		return fmt.Errorf("preview lifecycle action is invalid")
	}
	return nil
}

func PreviewLifecycleOperation(value v1.PreviewLifecycleOperation) error {
	if value.PreviewID != "" || value.PreviewKey != "" {
		return fmt.Errorf("external preview lifecycle forbids hosted preview identity")
	}
	if err := previewLifecycleAuthority(value.PreviewLifecycleOperationAuthority, false); err != nil {
		return err
	}
	if err := previewLifecycleBinding(
		value.Blueprint, value.PreviewURL, value.CommitSet, value.CommitSetDigest,
		value.OriginalExpiresAt, value.CurrentEffectiveExpiresAt, value.DesiredExpiresAt, value.Action, false,
	); err != nil {
		return err
	}
	if value.CommitSetDigest != v1.PreviewLifecycleCommitSetDigest(value.CommitSet) {
		return fmt.Errorf("preview lifecycle commit_set_digest does not bind commit_set")
	}
	if value.ResourceBindingDigest != v1.PreviewLifecycleResourceBindingDigest(value) {
		return fmt.Errorf("preview lifecycle resource_binding_digest does not bind resource authority")
	}
	if value.OperationDigest != v1.PreviewLifecycleOperationDigest(value) {
		return fmt.Errorf("preview lifecycle operation_digest does not bind stable intent")
	}
	if value.CommandDigest != v1.PreviewLifecycleCommandDigest(value) {
		return fmt.Errorf("preview lifecycle command_digest does not bind delivery authority")
	}
	return nil
}

func previewLifecycleOperationFromResult(value v1.PreviewLifecycleResultAuthority) v1.PreviewLifecycleOperation {
	return v1.PreviewLifecycleOperation{
		PreviewLifecycleOperationAuthority: value.PreviewLifecycleOperationAuthority,
		PreviewID:                          value.PreviewID, PreviewKey: value.PreviewKey,
		Blueprint: value.Blueprint, PreviewURL: value.PreviewURL,
		CommitSet:       append([]v1.PreviewObservationCommit(nil), value.CommitSet...),
		CommitSetDigest: value.CommitSetDigest, OriginalExpiresAt: value.OriginalExpiresAt,
		CurrentEffectiveExpiresAt: value.CurrentEffectiveExpiresAt, DesiredExpiresAt: value.DesiredExpiresAt,
	}
}

func previewLifecycleSuccess(value v1.PreviewLifecycleResultEvent) error {
	outcome := value.Outcome
	if outcome.Code != "" || outcome.RetryAfterMS != nil || outcome.ResourceState == nil {
		return fmt.Errorf("successful preview lifecycle outcome is invalid")
	}
	if err := SHA256Digest("controller_receipt_digest", outcome.ControllerReceiptDigest); err != nil {
		return err
	}
	if outcome.ResourceBindingDigest != value.ResourceBindingDigest {
		return fmt.Errorf("preview lifecycle result resource binding does not match authority")
	}
	state := outcome.ResourceState
	switch value.Action {
	case v1.PreviewLifecycleExtend:
		if state.Kind != v1.PreviewLifecycleResourceActive || state.PreviewURL != value.PreviewURL ||
			state.Blueprint == nil || *state.Blueprint != value.Blueprint ||
			!slices.Equal(state.CommitSet, value.CommitSet) || state.ExpiresAt == nil ||
			value.DesiredExpiresAt == nil || !state.ExpiresAt.Equal(*value.DesiredExpiresAt) || state.Disposition != "" {
			return fmt.Errorf("extended preview lifecycle resource state is invalid")
		}
	case v1.PreviewLifecycleDestroy:
		if state.Kind != v1.PreviewLifecycleResourceDestroyed ||
			(state.Disposition != v1.PreviewLifecycleDestroyed && state.Disposition != v1.PreviewLifecycleAlreadyAbsent) ||
			state.PreviewURL != "" || state.Blueprint != nil || len(state.CommitSet) != 0 || state.ExpiresAt != nil {
			return fmt.Errorf("destroyed preview lifecycle resource state is invalid")
		}
	default:
		return fmt.Errorf("preview lifecycle action is invalid")
	}
	return nil
}

func previewLifecycleFailure(value v1.PreviewLifecycleResultOutcome) error {
	if value.ControllerReceiptDigest != "" || value.ResourceBindingDigest != "" || value.ResourceState != nil {
		return fmt.Errorf("failed preview lifecycle outcome contains success fields")
	}
	if value.RetryAfterMS != nil && *value.RetryAfterMS > maxPreviewLifecycleRetryMS {
		return fmt.Errorf("preview lifecycle retry_after_ms is invalid")
	}
	switch value.Kind {
	case v1.PreviewLifecycleRetryable:
		if value.Code != v1.PreviewLifecycleControllerUnavailable && value.Code != v1.PreviewLifecycleRateLimited &&
			value.Code != v1.PreviewLifecycleTransientResourceConflict {
			return fmt.Errorf("retryable preview lifecycle outcome code is invalid")
		}
	case v1.PreviewLifecyclePermanent:
		if value.RetryAfterMS != nil || (value.Code != v1.PreviewLifecycleProfileUnavailable &&
			value.Code != v1.PreviewLifecycleResourceBindingMismatch && value.Code != v1.PreviewLifecycleResourceNotFound &&
			value.Code != v1.PreviewLifecycleInvalidControllerResponse && value.Code != v1.PreviewLifecyclePolicyDenied) {
			return fmt.Errorf("permanent preview lifecycle outcome is invalid")
		}
	case v1.PreviewLifecycleUncertain:
		if value.Code != v1.PreviewLifecycleControllerTimeout && value.Code != v1.PreviewLifecycleReceiptUnavailable {
			return fmt.Errorf("uncertain preview lifecycle outcome code is invalid")
		}
	default:
		return fmt.Errorf("preview lifecycle outcome kind is unsupported")
	}
	return nil
}

func PreviewLifecycleResultEvent(value v1.PreviewLifecycleResultEvent) error {
	if value.Kind != v1.EventPreviewLifecycleResult {
		return fmt.Errorf("preview lifecycle result kind is invalid")
	}
	if err := PreviewLifecycleOperation(previewLifecycleOperationFromResult(value.PreviewLifecycleResultAuthority)); err != nil {
		return err
	}
	if !previewLifecycleCommandIDPattern.MatchString(value.CommandMessageID) {
		return fmt.Errorf("preview lifecycle command_message_id is invalid")
	}
	if value.ObservedAt.IsZero() || value.ObservedAt.After(value.LeaseExpiresAt) {
		return fmt.Errorf("preview lifecycle observed_at is invalid")
	}
	if value.Outcome.Kind == v1.PreviewLifecycleSucceeded {
		return previewLifecycleSuccess(value)
	}
	return previewLifecycleFailure(value.Outcome)
}

func PreviewLifecycleCommandMessageID(value string) error {
	if !previewLifecycleCommandIDPattern.MatchString(value) {
		return fmt.Errorf("preview lifecycle command_message_id is invalid")
	}
	return nil
}
