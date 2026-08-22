package validation

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const maxProviderReviewerAssignmentRecipients = 100

var (
	providerReviewerAssignmentOperationIDPattern = regexp.MustCompile(`^provider-reviewer-assignment-operation-[0-9a-f]{64}$`)
	providerReviewerAssignmentCommandIDPattern   = regexp.MustCompile(`^provider-reviewer-assignment-command-[0-9a-f]{64}$`)
	providerReviewerAssignmentIdempotencyPattern = regexp.MustCompile(`^provider-reviewer-assignment-[0-9a-f]{64}$`)
)

func providerReviewerAssignmentAuthority(
	value v1.ProviderReviewerAssignmentOperationAuthority,
) error {
	if value.SchemaVersion != v1.ProviderReviewerAssignmentOperationAuthorityVersion {
		return fmt.Errorf("provider reviewer assignment schema_version is unsupported")
	}
	if !providerReviewerAssignmentOperationIDPattern.MatchString(value.ProviderReviewerAssignmentOperationID) {
		return fmt.Errorf("provider_reviewer_assignment_operation_id is invalid")
	}
	for field, id := range map[string]string{
		"organization_id":                   value.OrganizationID,
		"assignment_plan_id":                value.AssignmentPlanID,
		"source_publication_observation_id": value.SourcePublicationObservationID,
		"delivery_id":                       value.DeliveryID,
		"repository_id":                     value.RepositoryID,
		"installation_id":                   value.InstallationID,
		"lease_id":                          value.LeaseID,
	} {
		if err := ID(field, id); err != nil {
			return err
		}
	}
	for field, digest := range map[string]string{
		"assignment_plan_digest":        value.AssignmentPlanDigest,
		"installation_authority_digest": value.InstallationAuthorityDigest,
		"recipient_set_digest":          value.RecipientSetDigest,
		"operation_digest":              value.OperationDigest,
		"command_digest":                value.CommandDigest,
	} {
		if err := SHA256Digest(field, digest); err != nil {
			return err
		}
	}
	if value.InstallationStateRevision > maxJSONSafeInteger ||
		value.InstallationAuthorityRevision > maxJSONSafeInteger {
		return fmt.Errorf("provider reviewer assignment installation authority is invalid")
	}
	if value.Provider != "github" && value.Provider != "gitlab" && value.Provider != "bitbucket" {
		return fmt.Errorf("provider reviewer assignment provider is unsupported")
	}
	if len(value.ProviderInstanceKey) > 255 || !providerInstanceKeyPattern.MatchString(value.ProviderInstanceKey) {
		return fmt.Errorf("provider reviewer assignment provider_instance_key is invalid")
	}
	if value.Provider == "bitbucket" && value.ProviderInstanceKey != "bitbucket.org" {
		return fmt.Errorf("provider reviewer assignment bitbucket provider_instance_key is unsupported")
	}
	if err := providerReviewerAssignmentRepository(value); err != nil {
		return err
	}
	requestID, err := strconv.ParseUint(value.ExternalRequestID, 10, 64)
	if err != nil || requestID == 0 || strconv.FormatUint(requestID, 10) != value.ExternalRequestID {
		return fmt.Errorf("provider reviewer assignment external_request_id is invalid")
	}
	if err := GitCommitSHA("head_sha", value.HeadSHA); err != nil {
		return err
	}
	if !providerReviewerAssignmentIdempotencyPattern.MatchString(value.IdempotencyKey) {
		return fmt.Errorf("provider reviewer assignment idempotency_key is invalid")
	}
	if value.FencingToken == 0 || value.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("provider reviewer assignment fencing_token is invalid")
	}
	if value.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("provider reviewer assignment lease_expires_at is required")
	}
	return nil
}

func providerReviewerAssignmentRepository(
	value v1.ProviderReviewerAssignmentOperationAuthority,
) error {
	if err := sourceExternalID("provider_repository_id", value.ProviderRepositoryID); err != nil {
		return err
	}
	switch value.Provider {
	case "github", "gitlab":
		parsed, err := strconv.ParseUint(value.ProviderRepositoryID, 10, 64)
		if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != value.ProviderRepositoryID {
			return fmt.Errorf("provider reviewer assignment provider_repository_id is not canonical")
		}
		if value.ProviderRepositoryPath != "" {
			return fmt.Errorf("provider reviewer assignment %s repository path is unsupported", value.Provider)
		}
	case "bitbucket":
		if err := sourceExternalID("provider_repository_path", value.ProviderRepositoryPath); err != nil {
			return err
		}
		if !bitbucketProviderUUIDPattern.MatchString(value.ProviderRepositoryID) ||
			!bitbucketRepositoryPathPattern.MatchString(value.ProviderRepositoryPath) {
			return fmt.Errorf("provider reviewer assignment Bitbucket repository identity is invalid")
		}
	}
	return nil
}

func providerReviewerAssignmentRecipients(
	provider string,
	values []v1.ProviderReviewerAssignmentRecipient,
	leaseExpiresAt time.Time,
) error {
	if len(values) == 0 || len(values) > maxProviderReviewerAssignmentRecipients {
		return fmt.Errorf("provider reviewer assignment recipients count is invalid")
	}
	seenIdentityIDs := make(map[string]struct{}, len(values))
	seenAccountIDs := make(map[string]struct{}, len(values))
	seenPrincipals := make(map[string]struct{}, len(values))
	previousUserID := ""
	for _, recipient := range values {
		for field, id := range map[string]string{
			"reviewer_identity_id":       recipient.ReviewerIdentityID,
			"user_id":                    recipient.UserID,
			"organization_membership_id": recipient.OrganizationMembershipID,
			"provider_account_proof_id":  recipient.ProviderAccountProofID,
		} {
			if err := ID(field, id); err != nil {
				return err
			}
		}
		if previousUserID != "" && recipient.UserID <= previousUserID {
			return fmt.Errorf("provider reviewer assignment recipients must have unique sorted user IDs")
		}
		previousUserID = recipient.UserID
		if recipient.IdentityStateRevision > maxJSONSafeInteger ||
			recipient.IdentityRevision == 0 || recipient.IdentityRevision > maxJSONSafeInteger ||
			recipient.ProviderAccountProofRevision == 0 || recipient.ProviderAccountProofRevision > maxJSONSafeInteger {
			return fmt.Errorf("provider reviewer assignment recipient revision is invalid")
		}
		if err := SHA256Digest("provider_account_proof_digest", recipient.ProviderAccountProofDigest); err != nil {
			return err
		}
		if err := SHA256Digest("identity_verification_digest", recipient.IdentityVerificationDigest); err != nil {
			return err
		}
		if recipient.ProviderAccountProofExpiresAt.IsZero() || recipient.IdentityVerificationExpiresAt.IsZero() ||
			recipient.IdentityVerificationExpiresAt.After(recipient.ProviderAccountProofExpiresAt) ||
			leaseExpiresAt.After(recipient.IdentityVerificationExpiresAt) ||
			leaseExpiresAt.After(recipient.ProviderAccountProofExpiresAt) {
			return fmt.Errorf("provider reviewer assignment recipient authority expiry is invalid")
		}
		if err := reviewerProviderAccountID(provider, recipient.ProviderAccountID); err != nil {
			return err
		}
		if err := reviewerIdentityText("provider_username", recipient.ProviderUsername); err != nil {
			return err
		}
		if err := reviewerIdentityText("assignment_principal", recipient.AssignmentPrincipal); err != nil {
			return err
		}
		if _, duplicate := seenIdentityIDs[recipient.ReviewerIdentityID]; duplicate {
			return fmt.Errorf("provider reviewer assignment reviewer identity is duplicated")
		}
		if _, duplicate := seenAccountIDs[recipient.ProviderAccountID]; duplicate {
			return fmt.Errorf("provider reviewer assignment provider account is duplicated")
		}
		if _, duplicate := seenPrincipals[recipient.AssignmentPrincipal]; duplicate {
			return fmt.Errorf("provider reviewer assignment principal is duplicated")
		}
		seenIdentityIDs[recipient.ReviewerIdentityID] = struct{}{}
		seenAccountIDs[recipient.ProviderAccountID] = struct{}{}
		seenPrincipals[recipient.AssignmentPrincipal] = struct{}{}
	}
	return nil
}

func ProviderReviewerAssignmentOperation(value v1.ProviderReviewerAssignmentOperation) error {
	if err := providerReviewerAssignmentAuthority(value.ProviderReviewerAssignmentOperationAuthority); err != nil {
		return err
	}
	if err := providerReviewerAssignmentRecipients(value.Provider, value.Recipients, value.LeaseExpiresAt); err != nil {
		return err
	}
	if got := v1.ProviderReviewerAssignmentRecipientSetDigest(value.Recipients); got == "" || got != value.RecipientSetDigest {
		return fmt.Errorf("provider reviewer assignment recipient_set_digest does not bind recipients")
	}
	return nil
}

func ProviderReviewerAssignmentCommandMessageID(value string) error {
	if !providerReviewerAssignmentCommandIDPattern.MatchString(value) {
		return fmt.Errorf("provider reviewer assignment command_message_id is invalid")
	}
	return nil
}

func providerReviewerAssignmentOutcome(value v1.ProviderReviewerAssignmentResultOutcome, provider string) error {
	switch value.Kind {
	case v1.ProviderReviewerAssignmentSucceeded:
		if value.Code != "" || value.RetryAfterMS != nil || len(value.RequestedReviewers) == 0 ||
			len(value.RequestedReviewers) > maxProviderReviewerAssignmentRecipients {
			return fmt.Errorf("successful provider reviewer assignment outcome is invalid")
		}
		previousAccountID := ""
		seenIdentities := make(map[string]struct{}, len(value.RequestedReviewers))
		seenPrincipals := make(map[string]struct{}, len(value.RequestedReviewers))
		for _, reviewer := range value.RequestedReviewers {
			if err := ID("reviewer_identity_id", reviewer.ReviewerIdentityID); err != nil {
				return err
			}
			if err := reviewerProviderAccountID(provider, reviewer.ProviderAccountID); err != nil {
				return err
			}
			if err := reviewerIdentityText("assignment_principal", reviewer.AssignmentPrincipal); err != nil {
				return err
			}
			if previousAccountID != "" && reviewer.ProviderAccountID <= previousAccountID {
				return fmt.Errorf("provider reviewer assignment requested reviewers must be sorted and unique")
			}
			previousAccountID = reviewer.ProviderAccountID
			if _, duplicate := seenIdentities[reviewer.ReviewerIdentityID]; duplicate {
				return fmt.Errorf("provider reviewer assignment requested reviewer identity is duplicated")
			}
			if _, duplicate := seenPrincipals[reviewer.AssignmentPrincipal]; duplicate {
				return fmt.Errorf("provider reviewer assignment requested reviewer principal is duplicated")
			}
			seenIdentities[reviewer.ReviewerIdentityID] = struct{}{}
			seenPrincipals[reviewer.AssignmentPrincipal] = struct{}{}
		}
	case v1.ProviderReviewerAssignmentRetryable:
		if len(value.RequestedReviewers) != 0 ||
			(value.Code != v1.ProviderReviewerAssignmentProviderUnavailable && value.Code != v1.ProviderReviewerAssignmentRateLimited) ||
			value.RetryAfterMS != nil && *value.RetryAfterMS > maxSourceControlRetryAfterMS {
			return fmt.Errorf("retryable provider reviewer assignment outcome is invalid")
		}
	case v1.ProviderReviewerAssignmentPermanent:
		if len(value.RequestedReviewers) != 0 || value.RetryAfterMS != nil || !providerReviewerAssignmentPermanent(value.Code) {
			return fmt.Errorf("permanent provider reviewer assignment outcome is invalid")
		}
	case v1.ProviderReviewerAssignmentUncertain:
		if len(value.RequestedReviewers) != 0 || value.RetryAfterMS != nil || value.Code != v1.ProviderReviewerAssignmentDeliveryUncertain {
			return fmt.Errorf("uncertain provider reviewer assignment outcome is invalid")
		}
	default:
		return fmt.Errorf("provider reviewer assignment outcome kind is unsupported")
	}
	return nil
}

func providerReviewerAssignmentPermanent(code v1.ProviderReviewerAssignmentErrorCode) bool {
	switch code {
	case v1.ProviderReviewerAssignmentRequestNotFound,
		v1.ProviderReviewerAssignmentRequestNotOpen,
		v1.ProviderReviewerAssignmentHeadMismatch,
		v1.ProviderReviewerAssignmentRepositoryMismatch,
		v1.ProviderReviewerAssignmentInstallationInactive,
		v1.ProviderReviewerAssignmentInstallationAuthorityChanged,
		v1.ProviderReviewerAssignmentInstallationPermissionDenied,
		v1.ProviderReviewerAssignmentReviewerIdentityChanged,
		v1.ProviderReviewerAssignmentReviewerIdentityExpired,
		v1.ProviderReviewerAssignmentReviewerNotAssignable,
		v1.ProviderReviewerAssignmentUnsupportedProviderInstance,
		v1.ProviderReviewerAssignmentInvalidProviderResponse:
		return true
	default:
		return false
	}
}

func ProviderReviewerAssignmentResultEvent(value v1.ProviderReviewerAssignmentResultEvent) error {
	if value.Kind != v1.EventProviderReviewerAssignmentResult {
		return fmt.Errorf("provider reviewer assignment result kind is invalid")
	}
	if err := providerReviewerAssignmentAuthority(value.ProviderReviewerAssignmentOperationAuthority); err != nil {
		return err
	}
	if err := ProviderReviewerAssignmentCommandMessageID(value.CommandMessageID); err != nil {
		return err
	}
	if value.ObservedAt.IsZero() || value.ObservedAt.After(value.LeaseExpiresAt) {
		return fmt.Errorf("provider reviewer assignment observed_at is outside its authority window")
	}
	return providerReviewerAssignmentOutcome(value.Outcome, value.Provider)
}
