package validation

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const maxReviewerIdentityRepositories = 64

var (
	providerInstanceKeyPattern         = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,252}[a-z0-9])?(?::[1-9][0-9]{0,4})?$`)
	bitbucketAccountIDPattern          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_-]{0,127}$`)
	bitbucketProviderUUIDPattern       = regexp.MustCompile(`^\{[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\}$`)
	bitbucketRepositoryPathPattern     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,98}[a-z0-9])?/[a-z0-9](?:[a-z0-9._-]{0,98}[a-z0-9])?$`)
	reviewerIdentityOperationIDPattern = regexp.MustCompile(`^reviewer-identity-verification-operation-[0-9a-f]{64}$`)
	reviewerIdentityCommandIDPattern   = regexp.MustCompile(`^reviewer-identity-verification-command-[0-9a-f]{64}$`)
)

func reviewerIdentityAuthority(value v1.ReviewerIdentityVerificationResultAuthority) error {
	if value.SchemaVersion != v1.ReviewerIdentityVerificationSchemaVersion {
		return fmt.Errorf("reviewer identity verification schema_version is unsupported")
	}
	for field, id := range map[string]string{
		"organization_id":           value.OrganizationID,
		"installation_id":           value.InstallationID,
		"provider_account_proof_id": value.ProviderAccountProofID,
		"lease_id":                  value.LeaseID,
	} {
		if err := ID(field, id); err != nil {
			return err
		}
	}
	if !reviewerIdentityOperationIDPattern.MatchString(value.ReviewerIdentityVerificationOperationID) {
		return fmt.Errorf("reviewer_identity_verification_operation_id is invalid")
	}
	if value.InstallationAuthorityRevision == 0 || value.InstallationAuthorityRevision > maxJSONSafeInteger ||
		value.ProviderAccountProofRevision == 0 || value.ProviderAccountProofRevision > maxJSONSafeInteger {
		return fmt.Errorf("reviewer identity authority revision is invalid")
	}
	if value.InstallationStateRevision > maxJSONSafeInteger {
		return fmt.Errorf("reviewer identity installation_state_revision is invalid")
	}
	for field, digest := range map[string]string{
		"installation_authority_digest": value.InstallationAuthorityDigest,
		"provider_account_proof_digest": value.ProviderAccountProofDigest,
		"repository_set_digest":         value.RepositorySetDigest,
		"operation_digest":              value.OperationDigest,
		"command_digest":                value.CommandDigest,
	} {
		if err := SHA256Digest(field, digest); err != nil {
			return err
		}
	}
	if err := sourceProvider(value.Provider); err != nil {
		return err
	}
	if value.Provider != "github" && value.Provider != "gitlab" && value.Provider != "bitbucket" {
		return fmt.Errorf("reviewer identity provider is unsupported")
	}
	if len(value.ProviderInstanceKey) > 255 || !providerInstanceKeyPattern.MatchString(value.ProviderInstanceKey) {
		return fmt.Errorf("reviewer identity provider_instance_key is invalid")
	}
	if value.Provider == "bitbucket" && value.ProviderInstanceKey != "bitbucket.org" {
		return fmt.Errorf("reviewer identity bitbucket provider_instance_key is unsupported")
	}
	if err := reviewerProviderAccountID(value.Provider, value.ProviderAccountID); err != nil {
		return err
	}
	if value.FencingToken == 0 || value.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("reviewer identity fencing_token is invalid")
	}
	if value.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("reviewer identity lease_expires_at is required")
	}
	return nil
}

func reviewerProviderAccountID(provider, accountID string) error {
	switch provider {
	case "github", "gitlab":
		parsed, err := strconv.ParseUint(accountID, 10, 64)
		if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != accountID {
			return fmt.Errorf("reviewer identity provider_account_id is not a canonical numeric ID")
		}
	case "bitbucket":
		if !bitbucketAccountIDPattern.MatchString(accountID) && !bitbucketProviderUUIDPattern.MatchString(accountID) {
			return fmt.Errorf("reviewer identity provider_account_id is invalid")
		}
	default:
		if err := sourceExternalID("provider_account_id", accountID); err != nil {
			return err
		}
	}
	return nil
}

func reviewerIdentityRepositories(provider string, values []v1.ReviewerIdentityVerificationRepository) error {
	if len(values) == 0 || len(values) > maxReviewerIdentityRepositories {
		return fmt.Errorf("reviewer identity repositories must contain between 1 and %d entries", maxReviewerIdentityRepositories)
	}
	seenExternalIDs := make(map[string]struct{}, len(values))
	seenRepositoryPaths := make(map[string]struct{}, len(values))
	previousID := ""
	for _, repository := range values {
		if err := ID("repository_id", repository.RepositoryID); err != nil {
			return err
		}
		if previousID != "" && repository.RepositoryID <= previousID {
			return fmt.Errorf("reviewer identity repositories must be sorted and unique by repository_id")
		}
		previousID = repository.RepositoryID
		if err := sourceExternalID("repository_external_id", repository.RepositoryExternalID); err != nil {
			return err
		}
		if err := reviewerIdentityProviderRepository(provider, repository); err != nil {
			return err
		}
		if _, exists := seenExternalIDs[repository.RepositoryExternalID]; exists {
			return fmt.Errorf("reviewer identity repository_external_id appears more than once")
		}
		seenExternalIDs[repository.RepositoryExternalID] = struct{}{}
		if repository.RepositoryPath != "" {
			if _, exists := seenRepositoryPaths[repository.RepositoryPath]; exists {
				return fmt.Errorf("reviewer identity repository_path appears more than once")
			}
			seenRepositoryPaths[repository.RepositoryPath] = struct{}{}
			if err := sourceExternalID("repository_path", repository.RepositoryPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func reviewerIdentityProviderRepository(
	provider string,
	repository v1.ReviewerIdentityVerificationRepository,
) error {
	switch provider {
	case "github", "gitlab":
		providerID, err := strconv.ParseUint(repository.RepositoryExternalID, 10, 64)
		if err != nil || providerID == 0 || strconv.FormatUint(providerID, 10) != repository.RepositoryExternalID {
			return fmt.Errorf("reviewer identity repository_external_id is not a canonical numeric ID")
		}
		if repository.RepositoryPath != "" {
			return fmt.Errorf("reviewer identity %s repository_path is unsupported", provider)
		}
	case "bitbucket":
		if !bitbucketProviderUUIDPattern.MatchString(repository.RepositoryExternalID) {
			return fmt.Errorf("reviewer identity bitbucket repository_external_id is not a canonical UUID")
		}
		if !bitbucketRepositoryPathPattern.MatchString(repository.RepositoryPath) {
			return fmt.Errorf("reviewer identity bitbucket repository_path is required")
		}
	default:
		return fmt.Errorf("reviewer identity provider is unsupported")
	}
	return nil
}

func ReviewerIdentityVerificationOperation(value v1.ReviewerIdentityVerificationOperation) error {
	authority := v1.ReviewerIdentityVerificationResultAuthorityFromOperation(value)
	if err := reviewerIdentityAuthority(authority); err != nil {
		return err
	}
	if err := reviewerIdentityRepositories(value.Provider, value.Repositories); err != nil {
		return err
	}
	if got := v1.ReviewerIdentityRepositorySetDigest(value.Repositories); got == "" || got != value.RepositorySetDigest {
		return fmt.Errorf("reviewer identity repository_set_digest does not bind repositories")
	}
	return nil
}

func ReviewerIdentityVerificationCommandMessageID(value string) error {
	if !reviewerIdentityCommandIDPattern.MatchString(value) {
		return fmt.Errorf("reviewer identity command_message_id is invalid")
	}
	return nil
}

func reviewerIdentityText(field, value string) error {
	if len(value) == 0 || len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is invalid", field)
	}
	if strings.IndexFunc(value, func(character rune) bool {
		return unicode.IsControl(character)
	}) >= 0 {
		return fmt.Errorf("%s contains a control character", field)
	}
	return nil
}

func reviewerIdentityOutcome(
	value v1.ReviewerIdentityVerificationResultOutcome,
	provider string,
	repositorySetDigest string,
) error {
	switch value.Kind {
	case v1.ReviewerIdentityVerificationSucceeded:
		if value.Code != "" || value.RetryAfterMS != nil {
			return fmt.Errorf("successful reviewer identity verification outcome is invalid")
		}
		if err := reviewerIdentityText("provider_username", value.ProviderUsername); err != nil {
			return err
		}
		if err := reviewerIdentityText("assignment_principal", value.AssignmentPrincipal); err != nil {
			return err
		}
		repositories := make([]v1.ReviewerIdentityVerificationRepository, len(value.Repositories))
		for index, repository := range value.Repositories {
			switch repository.EffectiveReviewerAccess {
			case v1.ReviewerIdentityEligible,
				v1.ReviewerIdentityNotMember,
				v1.ReviewerIdentityInsufficientPermission:
			default:
				return fmt.Errorf("reviewer identity effective_reviewer_access is invalid")
			}
			repositories[index] = v1.ReviewerIdentityVerificationRepository{
				RepositoryID:         repository.RepositoryID,
				RepositoryExternalID: repository.RepositoryExternalID,
				RepositoryPath:       repository.RepositoryPath,
			}
		}
		if err := reviewerIdentityRepositories(provider, repositories); err != nil {
			return err
		}
		if v1.ReviewerIdentityRepositorySetDigest(repositories) != repositorySetDigest {
			return fmt.Errorf("reviewer identity result repositories do not bind repository_set_digest")
		}
	case v1.ReviewerIdentityVerificationRetryable:
		if value.ProviderUsername != "" || value.AssignmentPrincipal != "" || len(value.Repositories) != 0 ||
			(value.Code != v1.ReviewerIdentityProviderUnavailable && value.Code != v1.ReviewerIdentityRateLimited) {
			return fmt.Errorf("retryable reviewer identity verification outcome is invalid")
		}
		if value.RetryAfterMS != nil && *value.RetryAfterMS > maxSourceControlRetryAfterMS {
			return fmt.Errorf("reviewer identity retry_after_ms is invalid")
		}
	case v1.ReviewerIdentityVerificationPermanent:
		if value.ProviderUsername != "" || value.AssignmentPrincipal != "" || len(value.Repositories) != 0 || value.RetryAfterMS != nil ||
			!reviewerIdentityPermanentCode(value.Code) {
			return fmt.Errorf("permanent reviewer identity verification outcome is invalid")
		}
	default:
		return fmt.Errorf("reviewer identity verification outcome kind is unsupported")
	}
	return nil
}

func reviewerIdentityPermanentCode(code v1.ReviewerIdentityVerificationErrorCode) bool {
	switch code {
	case v1.ReviewerIdentityAccountNotFound,
		v1.ReviewerIdentityAccountAmbiguous,
		v1.ReviewerIdentityProviderMismatch,
		v1.ReviewerIdentityInstallationNotFound,
		v1.ReviewerIdentityInstallationInactive,
		v1.ReviewerIdentityInstallationPermissionDenied,
		v1.ReviewerIdentityUnsupportedProviderInstance,
		v1.ReviewerIdentityRepositoryAccessDenied:
		return true
	default:
		return false
	}
}

func ReviewerIdentityVerificationResultEvent(value v1.ReviewerIdentityVerificationResultEvent) error {
	if value.Kind != v1.EventReviewerIdentityVerificationResult {
		return fmt.Errorf("reviewer identity verification result kind is invalid")
	}
	if err := reviewerIdentityAuthority(value.ReviewerIdentityVerificationResultAuthority); err != nil {
		return err
	}
	if err := ReviewerIdentityVerificationCommandMessageID(value.CommandMessageID); err != nil {
		return err
	}
	if value.ObservedAt.IsZero() || value.ObservedAt.After(value.LeaseExpiresAt) {
		return fmt.Errorf("reviewer identity observed_at is outside its authority window")
	}
	return reviewerIdentityOutcome(value.Outcome, value.Provider, value.RepositorySetDigest)
}
