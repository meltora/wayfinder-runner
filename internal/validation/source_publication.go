package validation

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const maxSourceControlRetryAfterMS = 24 * 60 * 60 * 1000

var (
	sourceProviderPattern               = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	sourcePublicationIdempotencyPattern = regexp.MustCompile(`^source-publish-[0-9a-f]{64}$`)
)

func sourcePublicationAuthority(value v1.SourcePublicationOperationAuthority) error {
	if value.SchemaVersion != v1.SourcePublicationOperationAuthorityVersion {
		return fmt.Errorf("source publication schema_version is unsupported")
	}
	for field, id := range map[string]string{
		"source_publication_effect_id": value.SourcePublicationEffectID,
		"review_ready_request_id":      value.ReviewReadyRequestID,
		"project_id":                   value.ProjectID,
		"workflow_run_id":              value.WorkflowRunID,
		"attempt_id":                   value.AttemptID,
		"execution_manifest_id":        value.ExecutionManifestID,
		"lease_id":                     value.LeaseID,
	} {
		if err := ID(field, id); err != nil {
			return err
		}
	}
	if err := SHA256Digest("execution_manifest_digest", value.ExecutionManifestDigest); err != nil {
		return err
	}
	if value.FencingToken == 0 || value.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("source publication fencing_token is invalid")
	}
	if value.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("source publication lease_expires_at is required")
	}
	return nil
}

func sourceProvider(value string) error {
	if len(value) == 0 || len(value) > 64 || !sourceProviderPattern.MatchString(value) {
		return fmt.Errorf("source publication provider is invalid")
	}
	return nil
}

func sourceExternalID(field, value string) error {
	if len(value) == 0 || len(value) > 512 || !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is invalid", field)
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r)
	}) >= 0 {
		return fmt.Errorf("%s contains a control character", field)
	}
	return nil
}

func sourcePublicationIdentity(value v1.SourcePublicationResult) error {
	if value.SchemaVersion != v1.SourcePublicationSchemaVersion {
		return fmt.Errorf("source publication result schema_version is unsupported")
	}
	if err := sourceProvider(value.Provider); err != nil {
		return err
	}
	if err := ID("installation_id", value.InstallationID); err != nil {
		return err
	}
	if err := ID("repository_id", value.RepositoryID); err != nil {
		return err
	}
	if err := sourceExternalID("repository_external_id", value.RepositoryExternalID); err != nil {
		return err
	}
	if err := GitRef("base_branch", value.BaseBranch); err != nil {
		return err
	}
	if err := GitRef("head_branch", value.HeadBranch); err != nil {
		return err
	}
	if err := GitCommitSHA("head_sha", value.HeadSHA); err != nil {
		return err
	}
	if !sourcePublicationIdempotencyPattern.MatchString(value.IdempotencyKey) {
		return fmt.Errorf("source publication idempotency_key is invalid")
	}
	return nil
}

func sourcePublicationURL(value string) error {
	if err := WebURL("source publication url", value); err != nil {
		return err
	}
	if strings.Contains(value, "\\") || strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) >= 0 {
		return fmt.Errorf("source publication url contains unsafe characters")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.RawQuery != "" {
		return fmt.Errorf("source publication url must not contain a query")
	}
	return nil
}

func SourcePublicationOperation(value v1.SourcePublicationOperation) error {
	if err := sourcePublicationAuthority(value.SourcePublicationOperationAuthority); err != nil {
		return err
	}
	identity := v1.SourcePublicationResult{
		SchemaVersion: value.Publication.SchemaVersion,
		Provider:      value.Publication.Provider, InstallationID: value.Publication.InstallationID,
		RepositoryID: value.Publication.RepositoryID, RepositoryExternalID: value.Publication.RepositoryExternalID,
		BaseBranch: value.Publication.BaseBranch, HeadBranch: value.Publication.HeadBranch,
		HeadSHA: value.Publication.HeadSHA, IdempotencyKey: value.Publication.IdempotencyKey,
	}
	if err := sourcePublicationIdentity(identity); err != nil {
		return err
	}
	if err := boundedText("source publication title", value.Publication.Title, 512, true); err != nil {
		return err
	}
	if strings.IndexFunc(value.Publication.Title, func(r rune) bool {
		return unicode.IsControl(r)
	}) >= 0 || strings.TrimSpace(value.Publication.Title) == "" {
		return fmt.Errorf("source publication title contains invalid text")
	}
	return nil
}

func sourceControlRetryable(code v1.SourceControlErrorCode) bool {
	return code == v1.SourceControlRateLimited || code == v1.SourceControlProviderUnavailable
}

func sourceControlPermanent(code v1.SourceControlErrorCode) bool {
	switch code {
	case v1.SourceControlAuthenticationFailed,
		v1.SourceControlInstallationSuspended,
		v1.SourceControlInstallationRevoked,
		v1.SourceControlPermissionDenied,
		v1.SourceControlRepositoryNotFound,
		v1.SourceControlBranchNotFound,
		v1.SourceControlHeadMismatch,
		v1.SourceControlInvalidProviderResponse:
		return true
	default:
		return false
	}
}

func sourcePublicationOutcome(value v1.SourcePublicationResultOutcome) error {
	switch value.Kind {
	case v1.SourcePublicationSucceeded:
		if value.Result == nil || value.Code != "" || value.RetryAfterMS != nil {
			return fmt.Errorf("successful source publication outcome is invalid")
		}
		if err := sourcePublicationIdentity(*value.Result); err != nil {
			return err
		}
		if err := sourceExternalID("external_request_id", value.Result.ExternalRequestID); err != nil {
			return err
		}
		if err := sourcePublicationURL(value.Result.URL); err != nil {
			return err
		}
		if value.Result.State != "open" {
			return fmt.Errorf("source publication result state must be open")
		}
	case v1.SourcePublicationRetryable:
		if value.Result != nil || !sourceControlRetryable(value.Code) {
			return fmt.Errorf("retryable source publication outcome is invalid")
		}
		if value.RetryAfterMS != nil && *value.RetryAfterMS > maxSourceControlRetryAfterMS {
			return fmt.Errorf("source publication retry_after_ms is invalid")
		}
	case v1.SourcePublicationPermanent:
		if value.Result != nil || !sourceControlPermanent(value.Code) || value.RetryAfterMS != nil {
			return fmt.Errorf("permanent source publication outcome is invalid")
		}
	default:
		return fmt.Errorf("source publication outcome kind is unsupported")
	}
	return nil
}

func SourcePublicationResultEvent(value v1.SourcePublicationResultEvent) error {
	if value.Kind != v1.EventSourcePublicationResult {
		return fmt.Errorf("source publication result kind is invalid")
	}
	if err := sourcePublicationAuthority(value.SourcePublicationOperationAuthority); err != nil {
		return err
	}
	if err := ID("command_message_id", value.CommandMessageID); err != nil {
		return err
	}
	if value.ObservedAt.IsZero() {
		return fmt.Errorf("source publication observed_at is required")
	}
	return sourcePublicationOutcome(value.Outcome)
}
