package validation

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const MaxPreviewDeployBlueprintContentBytes = 256 << 10

var (
	previewDeployOperationIDPattern = regexp.MustCompile(`^preview-deploy-operation-[0-9a-f]{64}$`)
	previewDeployIdempotencyPattern = regexp.MustCompile(`^preview-deploy-[0-9a-f]{64}$`)
	previewDeployCommandIDPattern   = regexp.MustCompile(`^preview-deploy-command-[0-9a-f]{64}$`)
	previewKeyPattern               = regexp.MustCompile(`^[a-z2-7]{26}$`)
)

func previewDeployAuthority(value v1.PreviewDeployOperationAuthority) error {
	if value.SchemaVersion != v1.PreviewDeploySchemaVersion {
		return fmt.Errorf("preview deploy schema_version is unsupported")
	}
	for field, id := range map[string]string{
		"preview_deploy_effect_id": value.PreviewDeployEffectID,
		"preview_id":               value.PreviewID,
		"organization_id":          value.OrganizationID,
		"project_id":               value.ProjectID,
		"workflow_run_id":          value.WorkflowRunID,
		"execution_manifest_id":    value.ExecutionManifestID,
		"lease_id":                 value.LeaseID,
	} {
		if err := ID(field, id); err != nil {
			return err
		}
	}
	if value.PreviewDeployEffectRevision == 0 || value.PreviewDeployEffectRevision > maxJSONSafeInteger {
		return fmt.Errorf("preview deploy effect revision is invalid")
	}
	if !previewDeployOperationIDPattern.MatchString(value.PreviewDeployOperationID) {
		return fmt.Errorf("preview_deploy_operation_id is invalid")
	}
	for field, digest := range map[string]string{
		"execution_manifest_digest": value.ExecutionManifestDigest,
		"resource_binding_digest":   value.ResourceBindingDigest,
		"operation_digest":          value.OperationDigest,
		"command_digest":            value.CommandDigest,
	} {
		if err := SHA256Digest(field, digest); err != nil {
			return err
		}
	}
	if !previewDeployIdempotencyPattern.MatchString(value.IdempotencyKey) {
		return fmt.Errorf("preview deploy idempotency_key is invalid")
	}
	if value.FencingToken == 0 || value.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("preview deploy fencing_token is invalid")
	}
	if value.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("preview deploy lease_expires_at is required")
	}
	return nil
}

func previewBlueprintIdentity(blueprint v1.PreviewObservationBlueprint) error {
	if err := ID("blueprint_id", blueprint.BlueprintID); err != nil {
		return err
	}
	if err := boundedText("blueprint version", blueprint.Version, 128, true); err != nil || strings.TrimSpace(blueprint.Version) == "" {
		return fmt.Errorf("preview blueprint version is invalid")
	}
	return SHA256Digest("blueprint digest", blueprint.Digest)
}

func PreviewKey(value string) error {
	if !previewKeyPattern.MatchString(value) {
		return fmt.Errorf("preview_key is invalid")
	}
	return nil
}

func previewDeployEdgeHost(previewKey, edgeHost string) error {
	if edgeHost == "" {
		return nil
	}
	domain, found := strings.CutPrefix(edgeHost, previewKey+".")
	if !found || !strings.Contains(domain, ".") || !previewBlueprintIsHostname(domain) || !previewBlueprintIsHostname(edgeHost) {
		return fmt.Errorf("preview deploy edge_host is invalid")
	}
	return nil
}

func previewDeployBinding(
	blueprint v1.PreviewObservationBlueprint,
	commitSet []v1.PreviewObservationCommit,
	commitSetDigest, previewKey, edgeHost string,
	expiresAt, leaseExpiresAt time.Time,
) error {
	if err := previewBlueprintIdentity(blueprint); err != nil {
		return err
	}
	if err := validatePreviewObservationCommits(commitSet); err != nil {
		return err
	}
	if commitSetDigest != v1.PreviewLifecycleCommitSetDigest(commitSet) {
		return fmt.Errorf("preview deploy commit_set_digest does not bind commit_set")
	}
	if err := PreviewKey(previewKey); err != nil {
		return err
	}
	if err := previewDeployEdgeHost(previewKey, edgeHost); err != nil {
		return err
	}
	if expiresAt.IsZero() || !expiresAt.Equal(expiresAt.Truncate(time.Millisecond)) {
		return fmt.Errorf("preview deploy expires_at is invalid")
	}
	if !expiresAt.After(leaseExpiresAt) {
		return fmt.Errorf("preview must remain valid beyond the deploy lease")
	}
	return nil
}

func PreviewDeployBlueprintContent(blueprint v1.PreviewDeployBlueprint) (v1.PreviewBlueprintContentV1, error) {
	if len(blueprint.Content) == 0 || len(blueprint.Content) > MaxPreviewDeployBlueprintContentBytes {
		return v1.PreviewBlueprintContentV1{}, fmt.Errorf("preview deploy blueprint content size is invalid")
	}
	digest, err := v1.PreviewBlueprintDigestV1(blueprint.Content)
	if err != nil {
		return v1.PreviewBlueprintContentV1{}, err
	}
	if digest != blueprint.Digest {
		return v1.PreviewBlueprintContentV1{}, fmt.Errorf("preview deploy blueprint digest does not bind content")
	}
	return ParsePreviewBlueprintContentV1(blueprint.Content)
}

func PreviewDeployOperation(value v1.PreviewDeployOperation) error {
	if err := previewDeployAuthority(value.PreviewDeployOperationAuthority); err != nil {
		return err
	}
	if err := previewDeployBinding(
		value.Blueprint.Identity(), value.CommitSet, value.CommitSetDigest,
		value.PreviewKey, value.EdgeHost, value.ExpiresAt, value.LeaseExpiresAt,
	); err != nil {
		return err
	}
	if _, err := PreviewDeployBlueprintContent(value.Blueprint); err != nil {
		return err
	}
	if value.ResourceBindingDigest != v1.PreviewDeployResourceBindingDigest(value) {
		return fmt.Errorf("preview deploy resource_binding_digest does not bind resource authority")
	}
	if value.OperationDigest != v1.PreviewDeployOperationDigest(value) {
		return fmt.Errorf("preview deploy operation_digest does not bind stable intent")
	}
	if value.CommandDigest != v1.PreviewDeployCommandDigest(value) {
		return fmt.Errorf("preview deploy command_digest does not bind delivery authority")
	}
	return nil
}

func PreviewDeployCommandMessageID(value string) error {
	if !previewDeployCommandIDPattern.MatchString(value) {
		return fmt.Errorf("preview deploy command_message_id is invalid")
	}
	return nil
}

func PreviewDeployLogTail(value string) error {
	if len(value) > v1.PreviewDeployLogTailMaxBytes || !utf8.ValidString(value) {
		return fmt.Errorf("preview deploy log_tail is invalid")
	}
	if strings.Count(strings.TrimSuffix(value, "\n"), "\n")+1 > v1.PreviewDeployLogTailMaxLines {
		return fmt.Errorf("preview deploy log_tail has too many lines")
	}
	return nil
}

func previewDeployChecks(checks []v1.PreviewObservationCheck, requirePassed bool) error {
	if len(checks) > maxPreviewObservationChecks || (requirePassed && len(checks) == 0) {
		return fmt.Errorf("preview deploy checks are invalid")
	}
	previous := ""
	for _, check := range checks {
		if err := ID("check_id", check.CheckID); err != nil {
			return err
		}
		if check.CheckID <= previous {
			return fmt.Errorf("preview deploy checks must be unique and sorted")
		}
		if check.Status != v1.PreviewObservationCheckPassed && check.Status != v1.PreviewObservationCheckFailed {
			return fmt.Errorf("preview deploy check status is invalid")
		}
		if requirePassed && check.Status != v1.PreviewObservationCheckPassed {
			return fmt.Errorf("deployed preview requires every health check to pass")
		}
		if check.HTTPStatus < 100 || check.HTTPStatus > 599 || check.DurationMS > maxPreviewObservationDurationMS {
			return fmt.Errorf("preview deploy check measurement is invalid")
		}
		previous = check.CheckID
	}
	return nil
}

func PreviewDeployOutcome(value v1.PreviewDeployOutcome) error {
	switch value.Kind {
	case v1.PreviewDeployDeployed:
		if value.Phase != "" {
			return fmt.Errorf("deployed preview outcome forbids phase")
		}
		if err := previewDeployChecks(value.Checks, true); err != nil {
			return err
		}
	case v1.PreviewDeployFailed:
		if !value.Phase.Valid() {
			return fmt.Errorf("failed preview deploy phase is invalid")
		}
		if err := previewDeployChecks(value.Checks, false); err != nil {
			return err
		}
	case v1.PreviewDeployCapacityExhausted, v1.PreviewDeployUncertain:
		if value.Phase != "" || len(value.Checks) != 0 || value.LogTail != "" {
			return fmt.Errorf("%s preview deploy outcome carries extra fields", value.Kind)
		}
		return nil
	default:
		return fmt.Errorf("preview deploy outcome kind is unsupported")
	}
	return PreviewDeployLogTail(value.LogTail)
}

func PreviewDeployResultEvent(value v1.PreviewDeployResultEvent) error {
	if value.Kind != v1.EventPreviewDeployResult {
		return fmt.Errorf("preview deploy result kind is invalid")
	}
	authority := value.PreviewDeployResultAuthority
	if err := previewDeployAuthority(authority.PreviewDeployOperationAuthority); err != nil {
		return err
	}
	if err := previewDeployBinding(
		authority.Blueprint, authority.CommitSet, authority.CommitSetDigest,
		authority.PreviewKey, authority.EdgeHost, authority.ExpiresAt, authority.LeaseExpiresAt,
	); err != nil {
		return err
	}
	binding, operation, command := v1.PreviewDeployResultDigests(authority)
	if authority.ResourceBindingDigest != binding || authority.OperationDigest != operation || authority.CommandDigest != command {
		return fmt.Errorf("preview deploy result digests do not bind authority")
	}
	if err := PreviewDeployCommandMessageID(value.CommandMessageID); err != nil {
		return err
	}
	if value.ObservedAt.IsZero() || value.ObservedAt.After(value.LeaseExpiresAt) {
		return fmt.Errorf("preview deploy observed_at is invalid")
	}
	return PreviewDeployOutcome(value.Outcome)
}

func PreviewHostedLifecycleOperation(value v1.PreviewLifecycleOperation) error {
	if err := ID("preview_id", value.PreviewID); err != nil {
		return err
	}
	if err := PreviewKey(value.PreviewKey); err != nil {
		return err
	}
	if err := previewLifecycleAuthority(value.PreviewLifecycleOperationAuthority, true); err != nil {
		return err
	}
	if err := previewLifecycleBinding(
		value.Blueprint, value.PreviewURL, value.CommitSet, value.CommitSetDigest,
		value.OriginalExpiresAt, value.CurrentEffectiveExpiresAt, value.DesiredExpiresAt, value.Action, true,
	); err != nil {
		return err
	}
	if value.CommitSetDigest != v1.PreviewLifecycleCommitSetDigest(value.CommitSet) {
		return fmt.Errorf("preview lifecycle commit_set_digest does not bind commit_set")
	}
	if value.ResourceBindingDigest != v1.PreviewHostedLifecycleResourceBindingDigest(value) {
		return fmt.Errorf("hosted preview lifecycle resource_binding_digest does not bind resource authority")
	}
	if value.OperationDigest != v1.PreviewHostedLifecycleOperationDigest(value) {
		return fmt.Errorf("hosted preview lifecycle operation_digest does not bind stable intent")
	}
	if value.CommandDigest != v1.PreviewLifecycleCommandDigest(value) {
		return fmt.Errorf("hosted preview lifecycle command_digest does not bind delivery authority")
	}
	return nil
}

func PreviewHostedLifecycleResultEvent(value v1.PreviewLifecycleResultEvent) error {
	if value.Kind != v1.EventPreviewLifecycleResult {
		return fmt.Errorf("preview lifecycle result kind is invalid")
	}
	if err := PreviewHostedLifecycleOperation(previewLifecycleOperationFromResult(value.PreviewLifecycleResultAuthority)); err != nil {
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
