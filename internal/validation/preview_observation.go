package validation

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const (
	maxPreviewObservationChecks     = 128
	maxPreviewObservationCommits    = 16
	maxPreviewObservationDurationMS = 120_000
	maxPreviewObservationRetryMS    = 24 * 60 * 60 * 1000
)

func previewObservationAuthority(value v1.PreviewObservationOperationAuthority) error {
	if value.SchemaVersion != v1.PreviewObservationOperationAuthorityVersion {
		return fmt.Errorf("preview observation schema_version is unsupported")
	}
	for field, id := range map[string]string{
		"preview_observation_effect_id": value.PreviewObservationEffectID,
		"preview_evidence_id":           value.PreviewEvidenceID,
		"review_ready_request_id":       value.ReviewReadyRequestID,
		"project_id":                    value.ProjectID,
		"workflow_run_id":               value.WorkflowRunID,
		"attempt_id":                    value.AttemptID,
		"execution_manifest_id":         value.ExecutionManifestID,
		"lease_id":                      value.LeaseID,
	} {
		if err := ID(field, id); err != nil {
			return err
		}
	}
	if err := SHA256Digest("execution_manifest_digest", value.ExecutionManifestDigest); err != nil {
		return err
	}
	if value.FencingToken == 0 || value.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("preview observation fencing_token is invalid")
	}
	if value.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("preview observation lease_expires_at is required")
	}
	return nil
}

func previewObservationURL(value string) error {
	if err := WebURL("preview observation url", value); err != nil {
		return err
	}
	if strings.Contains(value, "\\") || strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) >= 0 {
		return fmt.Errorf("preview observation url contains unsafe characters")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.RawQuery != "" || parsed.ForceQuery {
		return fmt.Errorf("preview observation url must not contain a query")
	}
	return nil
}

func validatePreviewObservationCommits(commits []v1.PreviewObservationCommit) error {
	if len(commits) < 1 || len(commits) > maxPreviewObservationCommits {
		return fmt.Errorf("preview observation commit_set is invalid")
	}
	previous := ""
	for _, commit := range commits {
		if err := ID("repository_id", commit.RepositoryID); err != nil {
			return err
		}
		if commit.RepositoryID <= previous {
			return fmt.Errorf("preview observation commits must be unique and sorted")
		}
		if err := GitCommitSHA("commit_sha", commit.CommitSHA); err != nil {
			return err
		}
		previous = commit.RepositoryID
	}
	return nil
}

func validatePreviewObservationCheckIDs(checks []string) error {
	if len(checks) < 1 || len(checks) > maxPreviewObservationChecks {
		return fmt.Errorf("preview observation health checks are invalid")
	}
	previous := ""
	for _, checkID := range checks {
		if err := ID("health_check_id", checkID); err != nil {
			return err
		}
		if checkID <= previous {
			return fmt.Errorf("preview observation health checks must be unique and sorted")
		}
		previous = checkID
	}
	return nil
}

func PreviewObservationOperation(value v1.PreviewObservationOperation) error {
	if err := previewObservationAuthority(value.PreviewObservationOperationAuthority); err != nil {
		return err
	}
	if err := ID("blueprint_id", value.Blueprint.BlueprintID); err != nil {
		return err
	}
	if err := boundedText("blueprint version", value.Blueprint.Version, 128, true); err != nil ||
		strings.TrimSpace(value.Blueprint.Version) == "" {
		return fmt.Errorf("preview observation blueprint version is invalid")
	}
	if err := SHA256Digest("blueprint digest", value.Blueprint.Digest); err != nil {
		return err
	}
	if err := previewObservationURL(value.PreviewURL); err != nil {
		return err
	}
	if value.PreviewExpiresAt.IsZero() || !value.PreviewExpiresAt.After(value.LeaseExpiresAt) {
		return fmt.Errorf("preview must remain valid beyond the observation lease")
	}
	if err := validatePreviewObservationCommits(value.CommitSet); err != nil {
		return err
	}
	return validatePreviewObservationCheckIDs(value.RequiredHealthCheckIDs)
}

func previewObservationRetryable(code v1.PreviewObservationErrorCode) bool {
	return code == v1.PreviewObserverUnavailable || code == v1.PreviewUnreachable ||
		code == v1.PreviewTLSVerificationFailed
}

func previewObservationPermanent(code v1.PreviewObservationErrorCode) bool {
	return code == v1.PreviewProfileUnavailable || code == v1.PreviewTargetNotAllowed ||
		code == v1.PreviewInvalidObserverResponse
}

func previewObservationResult(value v1.PreviewObservation) error {
	if value.SchemaVersion != v1.PreviewObservationSchemaVersion {
		return fmt.Errorf("preview observation result schema_version is unsupported")
	}
	if err := previewObservationURL(value.PreviewURL); err != nil {
		return err
	}
	if err := validatePreviewObservationCommits(value.CommitSet); err != nil {
		return err
	}
	if len(value.Checks) < 1 || len(value.Checks) > maxPreviewObservationChecks {
		return fmt.Errorf("preview observation result checks are invalid")
	}
	previous := ""
	for _, check := range value.Checks {
		if err := ID("check_id", check.CheckID); err != nil {
			return err
		}
		if check.CheckID <= previous {
			return fmt.Errorf("preview observation result checks must be unique and sorted")
		}
		if check.Status != v1.PreviewObservationCheckPassed && check.Status != v1.PreviewObservationCheckFailed {
			return fmt.Errorf("preview observation check status is invalid")
		}
		if check.HTTPStatus < 100 || check.HTTPStatus > 599 || check.DurationMS > maxPreviewObservationDurationMS {
			return fmt.Errorf("preview observation check measurement is invalid")
		}
		previous = check.CheckID
	}
	return nil
}

func previewObservationOutcome(value v1.PreviewObservationResultOutcome) error {
	switch value.Kind {
	case v1.PreviewObservationObserved:
		if value.Observation == nil || value.Code != "" || value.RetryAfterMS != nil {
			return fmt.Errorf("observed preview outcome is invalid")
		}
		return previewObservationResult(*value.Observation)
	case v1.PreviewObservationRetryable:
		if value.Observation != nil || !previewObservationRetryable(value.Code) {
			return fmt.Errorf("retryable preview observation outcome is invalid")
		}
		if value.RetryAfterMS != nil && *value.RetryAfterMS > maxPreviewObservationRetryMS {
			return fmt.Errorf("preview observation retry_after_ms is invalid")
		}
		return nil
	case v1.PreviewObservationPermanent:
		if value.Observation != nil || !previewObservationPermanent(value.Code) || value.RetryAfterMS != nil {
			return fmt.Errorf("permanent preview observation outcome is invalid")
		}
		return nil
	default:
		return fmt.Errorf("preview observation outcome kind is unsupported")
	}
}

func PreviewObservationResultEvent(value v1.PreviewObservationResultEvent) error {
	if value.Kind != v1.EventPreviewObservationResult {
		return fmt.Errorf("preview observation result kind is invalid")
	}
	if err := previewObservationAuthority(value.PreviewObservationOperationAuthority); err != nil {
		return err
	}
	if err := ID("command_message_id", value.CommandMessageID); err != nil {
		return err
	}
	if value.ObservedAt.IsZero() || value.ObservedAt.After(value.LeaseExpiresAt) {
		return fmt.Errorf("preview observation observed_at is invalid")
	}
	return previewObservationOutcome(value.Outcome)
}
