package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const (
	ReviewerIdentityVerificationSchemaVersion    = 1
	ReviewerIdentityVerificationTransportVersion = 1
)

type ReviewerIdentityVerificationRepository struct {
	RepositoryID         string `json:"repository_id"`
	RepositoryExternalID string `json:"repository_external_id"`
	RepositoryPath       string `json:"repository_path,omitempty"`
}

type ReviewerIdentityVerificationOperation struct {
	SchemaVersion                           int                                      `json:"schema_version"`
	ReviewerIdentityVerificationOperationID string                                   `json:"reviewer_identity_verification_operation_id"`
	OrganizationID                          string                                   `json:"organization_id"`
	InstallationID                          string                                   `json:"installation_id"`
	InstallationStateRevision               uint64                                   `json:"installation_state_revision"`
	InstallationAuthorityRevision           uint64                                   `json:"installation_authority_revision"`
	InstallationAuthorityDigest             string                                   `json:"installation_authority_digest"`
	Provider                                string                                   `json:"provider"`
	ProviderInstanceKey                     string                                   `json:"provider_instance_key"`
	ProviderAccountProofID                  string                                   `json:"provider_account_proof_id"`
	ProviderAccountProofRevision            uint64                                   `json:"provider_account_proof_revision"`
	ProviderAccountProofDigest              string                                   `json:"provider_account_proof_digest"`
	ProviderAccountID                       string                                   `json:"provider_account_id"`
	RepositorySetDigest                     string                                   `json:"repository_set_digest"`
	OperationDigest                         string                                   `json:"operation_digest"`
	CommandDigest                           string                                   `json:"command_digest"`
	LeaseID                                 string                                   `json:"lease_id"`
	FencingToken                            uint64                                   `json:"fencing_token"`
	LeaseExpiresAt                          time.Time                                `json:"lease_expires_at"`
	Repositories                            []ReviewerIdentityVerificationRepository `json:"repositories"`
}

type ReviewerIdentityVerificationResultAuthority struct {
	SchemaVersion                           int       `json:"schema_version"`
	ReviewerIdentityVerificationOperationID string    `json:"reviewer_identity_verification_operation_id"`
	OrganizationID                          string    `json:"organization_id"`
	InstallationID                          string    `json:"installation_id"`
	InstallationStateRevision               uint64    `json:"installation_state_revision"`
	InstallationAuthorityRevision           uint64    `json:"installation_authority_revision"`
	InstallationAuthorityDigest             string    `json:"installation_authority_digest"`
	Provider                                string    `json:"provider"`
	ProviderInstanceKey                     string    `json:"provider_instance_key"`
	ProviderAccountProofID                  string    `json:"provider_account_proof_id"`
	ProviderAccountProofRevision            uint64    `json:"provider_account_proof_revision"`
	ProviderAccountProofDigest              string    `json:"provider_account_proof_digest"`
	ProviderAccountID                       string    `json:"provider_account_id"`
	RepositorySetDigest                     string    `json:"repository_set_digest"`
	OperationDigest                         string    `json:"operation_digest"`
	CommandDigest                           string    `json:"command_digest"`
	LeaseID                                 string    `json:"lease_id"`
	FencingToken                            uint64    `json:"fencing_token"`
	LeaseExpiresAt                          time.Time `json:"lease_expires_at"`
}

func ReviewerIdentityVerificationResultAuthorityFromOperation(
	operation ReviewerIdentityVerificationOperation,
) ReviewerIdentityVerificationResultAuthority {
	return ReviewerIdentityVerificationResultAuthority{
		SchemaVersion:                           operation.SchemaVersion,
		ReviewerIdentityVerificationOperationID: operation.ReviewerIdentityVerificationOperationID,
		OrganizationID:                          operation.OrganizationID,
		InstallationID:                          operation.InstallationID,
		InstallationStateRevision:               operation.InstallationStateRevision,
		InstallationAuthorityRevision:           operation.InstallationAuthorityRevision,
		InstallationAuthorityDigest:             operation.InstallationAuthorityDigest,
		Provider:                                operation.Provider,
		ProviderInstanceKey:                     operation.ProviderInstanceKey,
		ProviderAccountProofID:                  operation.ProviderAccountProofID,
		ProviderAccountProofRevision:            operation.ProviderAccountProofRevision,
		ProviderAccountProofDigest:              operation.ProviderAccountProofDigest,
		ProviderAccountID:                       operation.ProviderAccountID,
		RepositorySetDigest:                     operation.RepositorySetDigest,
		OperationDigest:                         operation.OperationDigest,
		CommandDigest:                           operation.CommandDigest,
		LeaseID:                                 operation.LeaseID,
		FencingToken:                            operation.FencingToken,
		LeaseExpiresAt:                          operation.LeaseExpiresAt,
	}
}

// ReviewerIdentityRepositorySetDigest binds the exact ordered repository set.
// Validation requires callers to sort by repository_id before calculating it.
func ReviewerIdentityRepositorySetDigest(repositories []ReviewerIdentityVerificationRepository) string {
	// The control plane recursively sorts object keys before JSON encoding.
	// Keep this declaration in lexicographic JSON-key order so both runtimes
	// hash the same compact representation while omitting an empty path.
	type canonicalRepository struct {
		RepositoryExternalID string `json:"repository_external_id"`
		RepositoryID         string `json:"repository_id"`
		RepositoryPath       string `json:"repository_path,omitempty"`
	}
	canonical := make([]canonicalRepository, len(repositories))
	for index, repository := range repositories {
		canonical[index] = canonicalRepository{
			RepositoryExternalID: repository.RepositoryExternalID,
			RepositoryID:         repository.RepositoryID,
			RepositoryPath:       repository.RepositoryPath,
		}
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

type ReviewerIdentityEffectiveAccess string

const (
	ReviewerIdentityEligible               ReviewerIdentityEffectiveAccess = "eligible"
	ReviewerIdentityNotMember              ReviewerIdentityEffectiveAccess = "not_member"
	ReviewerIdentityInsufficientPermission ReviewerIdentityEffectiveAccess = "insufficient_permission"
)

type ReviewerIdentityRepositoryEvidence struct {
	RepositoryID            string                          `json:"repository_id"`
	RepositoryExternalID    string                          `json:"repository_external_id"`
	RepositoryPath          string                          `json:"repository_path,omitempty"`
	EffectiveReviewerAccess ReviewerIdentityEffectiveAccess `json:"effective_reviewer_access"`
}

type ReviewerIdentityVerificationOutcomeKind string

const (
	ReviewerIdentityVerificationSucceeded ReviewerIdentityVerificationOutcomeKind = "succeeded"
	ReviewerIdentityVerificationRetryable ReviewerIdentityVerificationOutcomeKind = "retryable"
	ReviewerIdentityVerificationPermanent ReviewerIdentityVerificationOutcomeKind = "permanent"
)

type ReviewerIdentityVerificationErrorCode string

const (
	ReviewerIdentityProviderUnavailable          ReviewerIdentityVerificationErrorCode = "provider_unavailable"
	ReviewerIdentityRateLimited                  ReviewerIdentityVerificationErrorCode = "rate_limited"
	ReviewerIdentityAccountNotFound              ReviewerIdentityVerificationErrorCode = "account_not_found"
	ReviewerIdentityAccountAmbiguous             ReviewerIdentityVerificationErrorCode = "account_ambiguous"
	ReviewerIdentityProviderMismatch             ReviewerIdentityVerificationErrorCode = "provider_mismatch"
	ReviewerIdentityInstallationNotFound         ReviewerIdentityVerificationErrorCode = "installation_not_found"
	ReviewerIdentityInstallationInactive         ReviewerIdentityVerificationErrorCode = "installation_inactive"
	ReviewerIdentityInstallationPermissionDenied ReviewerIdentityVerificationErrorCode = "installation_permission_denied"
	ReviewerIdentityUnsupportedProviderInstance  ReviewerIdentityVerificationErrorCode = "unsupported_provider_instance"
	ReviewerIdentityRepositoryAccessDenied       ReviewerIdentityVerificationErrorCode = "repository_access_denied"
)

type ReviewerIdentityVerificationResultOutcome struct {
	Kind                ReviewerIdentityVerificationOutcomeKind `json:"kind"`
	ProviderUsername    string                                  `json:"provider_username,omitempty"`
	AssignmentPrincipal string                                  `json:"assignment_principal,omitempty"`
	Repositories        []ReviewerIdentityRepositoryEvidence    `json:"repositories,omitempty"`
	Code                ReviewerIdentityVerificationErrorCode   `json:"code,omitempty"`
	RetryAfterMS        *uint64                                 `json:"retry_after_ms,omitempty"`
}

const EventReviewerIdentityVerificationResult = "reviewer_identity_verification_result"

type ReviewerIdentityVerificationResultEvent struct {
	Kind string `json:"kind"`
	ReviewerIdentityVerificationResultAuthority
	CommandMessageID string                                    `json:"command_message_id"`
	ObservedAt       time.Time                                 `json:"observed_at"`
	Outcome          ReviewerIdentityVerificationResultOutcome `json:"outcome"`
}
