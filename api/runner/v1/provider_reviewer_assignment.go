package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const (
	ProviderReviewerAssignmentOperationAuthorityVersion = 1
	ProviderReviewerAssignmentTransportVersion          = 1
)

type ProviderReviewerAssignmentRecipient struct {
	ReviewerIdentityID            string    `json:"reviewer_identity_id"`
	UserID                        string    `json:"user_id"`
	OrganizationMembershipID      string    `json:"organization_membership_id"`
	IdentityStateRevision         uint64    `json:"identity_state_revision"`
	IdentityRevision              uint64    `json:"identity_revision"`
	ProviderAccountProofID        string    `json:"provider_account_proof_id"`
	ProviderAccountProofRevision  uint64    `json:"provider_account_proof_revision"`
	ProviderAccountProofDigest    string    `json:"provider_account_proof_digest"`
	ProviderAccountProofExpiresAt time.Time `json:"provider_account_proof_expires_at"`
	IdentityVerificationDigest    string    `json:"identity_verification_digest"`
	IdentityVerificationExpiresAt time.Time `json:"identity_verification_expires_at"`
	ProviderAccountID             string    `json:"provider_account_id"`
	ProviderUsername              string    `json:"provider_username"`
	AssignmentPrincipal           string    `json:"assignment_principal"`
}

type ProviderReviewerAssignmentOperationAuthority struct {
	SchemaVersion                         int       `json:"schema_version"`
	ProviderReviewerAssignmentOperationID string    `json:"provider_reviewer_assignment_operation_id"`
	OrganizationID                        string    `json:"organization_id"`
	AssignmentPlanID                      string    `json:"assignment_plan_id"`
	AssignmentPlanDigest                  string    `json:"assignment_plan_digest"`
	SourcePublicationObservationID        string    `json:"source_publication_observation_id"`
	DeliveryID                            string    `json:"delivery_id"`
	RepositoryID                          string    `json:"repository_id"`
	ProviderRepositoryID                  string    `json:"provider_repository_id"`
	ProviderRepositoryPath                string    `json:"provider_repository_path,omitempty"`
	ExternalRequestID                     string    `json:"external_request_id"`
	HeadSHA                               string    `json:"head_sha"`
	InstallationID                        string    `json:"installation_id"`
	InstallationStateRevision             uint64    `json:"installation_state_revision"`
	InstallationAuthorityRevision         uint64    `json:"installation_authority_revision"`
	InstallationAuthorityDigest           string    `json:"installation_authority_digest"`
	Provider                              string    `json:"provider"`
	ProviderInstanceKey                   string    `json:"provider_instance_key"`
	IdempotencyKey                        string    `json:"idempotency_key"`
	RecipientSetDigest                    string    `json:"recipient_set_digest"`
	OperationDigest                       string    `json:"operation_digest"`
	CommandDigest                         string    `json:"command_digest"`
	LeaseID                               string    `json:"lease_id"`
	FencingToken                          uint64    `json:"fencing_token"`
	LeaseExpiresAt                        time.Time `json:"lease_expires_at"`
}

type ProviderReviewerAssignmentOperation struct {
	ProviderReviewerAssignmentOperationAuthority
	Recipients []ProviderReviewerAssignmentRecipient `json:"recipients"`
}

func ProviderReviewerAssignmentRecipientSetDigest(
	recipients []ProviderReviewerAssignmentRecipient,
) string {
	// Fields are in lexicographic JSON-key order to mirror the control plane's
	// recursive canonical object-key sort.
	type canonicalRecipient struct {
		AssignmentPrincipal           string `json:"assignment_principal"`
		IdentityRevision              uint64 `json:"identity_revision"`
		IdentityStateRevision         uint64 `json:"identity_state_revision"`
		IdentityVerificationDigest    string `json:"identity_verification_digest"`
		IdentityVerificationExpiresAt string `json:"identity_verification_expires_at"`
		OrganizationMembershipID      string `json:"organization_membership_id"`
		ProviderAccountID             string `json:"provider_account_id"`
		ProviderAccountProofDigest    string `json:"provider_account_proof_digest"`
		ProviderAccountProofExpiresAt string `json:"provider_account_proof_expires_at"`
		ProviderAccountProofID        string `json:"provider_account_proof_id"`
		ProviderAccountProofRevision  uint64 `json:"provider_account_proof_revision"`
		ProviderUsername              string `json:"provider_username"`
		ReviewerIdentityID            string `json:"reviewer_identity_id"`
		UserID                        string `json:"user_id"`
	}
	canonical := make([]canonicalRecipient, len(recipients))
	for index, recipient := range recipients {
		canonical[index] = canonicalRecipient{
			AssignmentPrincipal:           recipient.AssignmentPrincipal,
			IdentityRevision:              recipient.IdentityRevision,
			IdentityStateRevision:         recipient.IdentityStateRevision,
			IdentityVerificationDigest:    recipient.IdentityVerificationDigest,
			IdentityVerificationExpiresAt: canonicalAssignmentTime(recipient.IdentityVerificationExpiresAt),
			OrganizationMembershipID:      recipient.OrganizationMembershipID,
			ProviderAccountID:             recipient.ProviderAccountID,
			ProviderAccountProofDigest:    recipient.ProviderAccountProofDigest,
			ProviderAccountProofExpiresAt: canonicalAssignmentTime(recipient.ProviderAccountProofExpiresAt),
			ProviderAccountProofID:        recipient.ProviderAccountProofID,
			ProviderAccountProofRevision:  recipient.ProviderAccountProofRevision,
			ProviderUsername:              recipient.ProviderUsername,
			ReviewerIdentityID:            recipient.ReviewerIdentityID,
			UserID:                        recipient.UserID,
		}
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func canonicalAssignmentTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

type ProviderReviewerAssignmentRequestedReviewer struct {
	ReviewerIdentityID  string `json:"reviewer_identity_id"`
	ProviderAccountID   string `json:"provider_account_id"`
	AssignmentPrincipal string `json:"assignment_principal"`
}

type ProviderReviewerAssignmentErrorCode string

const (
	ProviderReviewerAssignmentProviderUnavailable          ProviderReviewerAssignmentErrorCode = "provider_unavailable"
	ProviderReviewerAssignmentRateLimited                  ProviderReviewerAssignmentErrorCode = "rate_limited"
	ProviderReviewerAssignmentRequestNotFound              ProviderReviewerAssignmentErrorCode = "request_not_found"
	ProviderReviewerAssignmentRequestNotOpen               ProviderReviewerAssignmentErrorCode = "request_not_open"
	ProviderReviewerAssignmentHeadMismatch                 ProviderReviewerAssignmentErrorCode = "head_mismatch"
	ProviderReviewerAssignmentRepositoryMismatch           ProviderReviewerAssignmentErrorCode = "repository_mismatch"
	ProviderReviewerAssignmentInstallationInactive         ProviderReviewerAssignmentErrorCode = "installation_inactive"
	ProviderReviewerAssignmentInstallationAuthorityChanged ProviderReviewerAssignmentErrorCode = "installation_authority_changed"
	ProviderReviewerAssignmentInstallationPermissionDenied ProviderReviewerAssignmentErrorCode = "installation_permission_denied"
	ProviderReviewerAssignmentReviewerIdentityChanged      ProviderReviewerAssignmentErrorCode = "reviewer_identity_changed"
	ProviderReviewerAssignmentReviewerIdentityExpired      ProviderReviewerAssignmentErrorCode = "reviewer_identity_expired"
	ProviderReviewerAssignmentUnsupportedProviderInstance  ProviderReviewerAssignmentErrorCode = "unsupported_provider_instance"
	ProviderReviewerAssignmentReviewerNotAssignable        ProviderReviewerAssignmentErrorCode = "reviewer_not_assignable"
	ProviderReviewerAssignmentInvalidProviderResponse      ProviderReviewerAssignmentErrorCode = "invalid_provider_response"
	ProviderReviewerAssignmentDeliveryUncertain            ProviderReviewerAssignmentErrorCode = "delivery_uncertain"
)

type ProviderReviewerAssignmentOutcomeKind string

const (
	ProviderReviewerAssignmentSucceeded ProviderReviewerAssignmentOutcomeKind = "succeeded"
	ProviderReviewerAssignmentRetryable ProviderReviewerAssignmentOutcomeKind = "retryable"
	ProviderReviewerAssignmentPermanent ProviderReviewerAssignmentOutcomeKind = "permanent"
	ProviderReviewerAssignmentUncertain ProviderReviewerAssignmentOutcomeKind = "uncertain"
)

type ProviderReviewerAssignmentResultOutcome struct {
	Kind               ProviderReviewerAssignmentOutcomeKind         `json:"kind"`
	RequestedReviewers []ProviderReviewerAssignmentRequestedReviewer `json:"requested_reviewers,omitempty"`
	Code               ProviderReviewerAssignmentErrorCode           `json:"code,omitempty"`
	RetryAfterMS       *uint64                                       `json:"retry_after_ms,omitempty"`
}

const EventProviderReviewerAssignmentResult = "provider_reviewer_assignment_result"

type ProviderReviewerAssignmentResultEvent struct {
	Kind string `json:"kind"`
	ProviderReviewerAssignmentOperationAuthority
	CommandMessageID string                                  `json:"command_message_id"`
	ObservedAt       time.Time                               `json:"observed_at"`
	Outcome          ProviderReviewerAssignmentResultOutcome `json:"outcome"`
}
