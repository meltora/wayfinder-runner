package v1

import (
	"encoding/json"
	"time"
)

const (
	ProviderReviewFeedbackContentAuthorityVersion = 1
	ProviderReviewFeedbackContentTransportVersion = 1
	ProviderReviewFeedbackContentMaxBytes         = 16 * 1024
	EventProviderReviewFeedbackContentResult      = "provider_review_feedback_content_result"
)

type ProviderReviewFeedbackContentAuthority struct {
	SchemaVersion                                  int                                      `json:"schema_version"`
	ProviderReviewFeedbackContentOperationID       string                                   `json:"provider_review_feedback_content_operation_id"`
	OrganizationID                                 string                                   `json:"organization_id"`
	ProjectID                                      string                                   `json:"project_id"`
	WorkflowRunID                                  string                                   `json:"workflow_run_id"`
	WorkflowStateRevision                          uint64                                   `json:"workflow_state_revision"`
	AssignmentDeliveryID                           string                                   `json:"assignment_delivery_id"`
	AssignmentDeliveryStateRevision                uint64                                   `json:"assignment_delivery_state_revision"`
	AssignmentObservationID                        string                                   `json:"assignment_observation_id"`
	ObservedReviewerID                             string                                   `json:"observed_reviewer_id"`
	ProviderReviewObservationID                    string                                   `json:"provider_review_observation_id"`
	ProviderReviewObservationResultPayloadChecksum string                                   `json:"provider_review_observation_result_payload_checksum"`
	EvidenceObservedAt                             time.Time                                `json:"evidence_observed_at"`
	Purpose                                        string                                   `json:"purpose"`
	ContentSource                                  string                                   `json:"content_source"`
	ProviderReviewEventID                          string                                   `json:"provider_review_event_id"`
	ProviderReviewEventRevision                    uint64                                   `json:"provider_review_event_revision"`
	ReviewEventKind                                ProviderReviewObservationReviewEventKind `json:"review_event_kind"`
	ReviewState                                    ProviderReviewObservationReviewState     `json:"review_state"`
	ReviewEventOccurredAt                          time.Time                                `json:"review_event_occurred_at"`
	ContentDigest                                  string                                   `json:"content_digest"`
	ContentByteCount                               uint64                                   `json:"content_byte_count"`
	ProviderAccountID                              string                                   `json:"provider_account_id"`
	UserID                                         string                                   `json:"user_id"`
	OrganizationMembershipID                       string                                   `json:"organization_membership_id"`
	OrganizationMembershipStateRevision            uint64                                   `json:"organization_membership_state_revision"`
	CodeReviewAuthorityRevision                    uint64                                   `json:"code_review_authority_revision"`
	CodeReviewAuthorityDigest                      string                                   `json:"code_review_authority_digest"`
	CodeReviewAuthorityExpiresAt                   *time.Time                               `json:"code_review_authority_expires_at,omitempty"`
	ReviewerIdentityID                             string                                   `json:"reviewer_identity_id"`
	IdentityStateRevision                          uint64                                   `json:"identity_state_revision"`
	IdentityRevision                               uint64                                   `json:"identity_revision"`
	IdentityVerificationID                         string                                   `json:"identity_verification_id"`
	IdentityVerificationDigest                     string                                   `json:"identity_verification_digest"`
	IdentityVerificationExpiresAt                  time.Time                                `json:"identity_verification_expires_at"`
	ProviderAccountProofID                         string                                   `json:"provider_account_proof_id"`
	ProviderAccountProofStateRevision              uint64                                   `json:"provider_account_proof_state_revision"`
	ProviderAccountProofRevision                   uint64                                   `json:"provider_account_proof_revision"`
	ProviderAccountProofDigest                     string                                   `json:"provider_account_proof_digest"`
	ProviderAccountProofExpiresAt                  time.Time                                `json:"provider_account_proof_expires_at"`
	RepositoryID                                   string                                   `json:"repository_id"`
	ProviderRepositoryID                           string                                   `json:"provider_repository_id"`
	ProviderRepositoryPath                         string                                   `json:"provider_repository_path,omitempty"`
	ExternalRequestID                              string                                   `json:"external_request_id"`
	ExpectedHeadSHA                                string                                   `json:"expected_head_sha"`
	InstallationID                                 string                                   `json:"installation_id"`
	InstallationStateRevision                      uint64                                   `json:"installation_state_revision"`
	InstallationAuthorityRevision                  uint64                                   `json:"installation_authority_revision"`
	InstallationAuthorityDigest                    string                                   `json:"installation_authority_digest"`
	Provider                                       string                                   `json:"provider"`
	ProviderInstanceKey                            string                                   `json:"provider_instance_key"`
	FeedbackContentRequestDigest                   string                                   `json:"feedback_content_request_digest"`
	IdempotencyKey                                 string                                   `json:"idempotency_key"`
	OperationDigest                                string                                   `json:"operation_digest"`
	CommandDigest                                  string                                   `json:"command_digest"`
	LeaseID                                        string                                   `json:"lease_id"`
	FencingToken                                   uint64                                   `json:"fencing_token"`
	LeaseExpiresAt                                 time.Time                                `json:"lease_expires_at"`
}

type ProviderReviewFeedbackContentOperation struct {
	ProviderReviewFeedbackContentAuthority
}

type ProviderReviewFeedbackContentOutcomeKind string
type ProviderReviewFeedbackContentErrorCode string

const (
	ProviderReviewFeedbackContentSucceeded                    ProviderReviewFeedbackContentOutcomeKind = "succeeded"
	ProviderReviewFeedbackContentRetryable                    ProviderReviewFeedbackContentOutcomeKind = "retryable"
	ProviderReviewFeedbackContentPermanent                    ProviderReviewFeedbackContentOutcomeKind = "permanent"
	ProviderReviewFeedbackContentProviderUnavailable          ProviderReviewFeedbackContentErrorCode   = "provider_unavailable"
	ProviderReviewFeedbackContentRateLimited                  ProviderReviewFeedbackContentErrorCode   = "rate_limited"
	ProviderReviewFeedbackContentRequestNotFound              ProviderReviewFeedbackContentErrorCode   = "request_not_found"
	ProviderReviewFeedbackContentRequestNotOpen               ProviderReviewFeedbackContentErrorCode   = "request_not_open"
	ProviderReviewFeedbackContentRepositoryMismatch           ProviderReviewFeedbackContentErrorCode   = "repository_mismatch"
	ProviderReviewFeedbackContentHeadMismatch                 ProviderReviewFeedbackContentErrorCode   = "head_mismatch"
	ProviderReviewFeedbackContentReviewEventNotFound          ProviderReviewFeedbackContentErrorCode   = "review_event_not_found"
	ProviderReviewFeedbackContentReviewEventMetadataChanged   ProviderReviewFeedbackContentErrorCode   = "review_event_metadata_changed"
	ProviderReviewFeedbackContentInstallationInactive         ProviderReviewFeedbackContentErrorCode   = "installation_inactive"
	ProviderReviewFeedbackContentInstallationAuthorityChanged ProviderReviewFeedbackContentErrorCode   = "installation_authority_changed"
	ProviderReviewFeedbackContentInstallationPermissionDenied ProviderReviewFeedbackContentErrorCode   = "installation_permission_denied"
	ProviderReviewFeedbackContentReviewerIdentityChanged      ProviderReviewFeedbackContentErrorCode   = "reviewer_identity_changed"
	ProviderReviewFeedbackContentReviewerIdentityExpired      ProviderReviewFeedbackContentErrorCode   = "reviewer_identity_expired"
	ProviderReviewFeedbackContentProviderAccountProofChanged  ProviderReviewFeedbackContentErrorCode   = "provider_account_proof_changed"
	ProviderReviewFeedbackContentProviderAccountProofExpired  ProviderReviewFeedbackContentErrorCode   = "provider_account_proof_expired"
	ProviderReviewFeedbackContentMembershipAuthorityChanged   ProviderReviewFeedbackContentErrorCode   = "membership_authority_changed"
	ProviderReviewFeedbackContentCodeReviewAuthorityChanged   ProviderReviewFeedbackContentErrorCode   = "code_review_authority_changed"
	ProviderReviewFeedbackContentUnsupportedProviderInstance  ProviderReviewFeedbackContentErrorCode   = "unsupported_provider_instance"
	ProviderReviewFeedbackContentTooLarge                     ProviderReviewFeedbackContentErrorCode   = "content_too_large"
	ProviderReviewFeedbackContentInvalidPlainText             ProviderReviewFeedbackContentErrorCode   = "invalid_plain_text"
	ProviderReviewFeedbackContentInvalidProviderResponse      ProviderReviewFeedbackContentErrorCode   = "invalid_provider_response"
)

type ProviderReviewFeedbackContentResultOutcome struct {
	Kind                 ProviderReviewFeedbackContentOutcomeKind
	RequestState         ProviderReviewObservationRequestState
	ObservedHeadSHA      string
	ContentEncoding      string
	ContentFormat        string
	UnicodeNormalization string
	LineEndings          string
	ContentDigest        string
	ContentByteCount     uint64
	Content              string
	Code                 ProviderReviewFeedbackContentErrorCode
	RetryAfterMS         *uint64
}

func (v ProviderReviewFeedbackContentResultOutcome) MarshalJSON() ([]byte, error) {
	switch v.Kind {
	case ProviderReviewFeedbackContentSucceeded:
		return json.Marshal(struct {
			Kind                 ProviderReviewFeedbackContentOutcomeKind `json:"kind"`
			RequestState         ProviderReviewObservationRequestState    `json:"request_state"`
			ObservedHeadSHA      string                                   `json:"observed_head_sha"`
			ContentEncoding      string                                   `json:"content_encoding"`
			ContentFormat        string                                   `json:"content_format"`
			UnicodeNormalization string                                   `json:"unicode_normalization"`
			LineEndings          string                                   `json:"line_endings"`
			ContentDigest        string                                   `json:"content_digest"`
			ContentByteCount     uint64                                   `json:"content_byte_count"`
			Content              string                                   `json:"content"`
		}{v.Kind, v.RequestState, v.ObservedHeadSHA, v.ContentEncoding, v.ContentFormat, v.UnicodeNormalization, v.LineEndings, v.ContentDigest, v.ContentByteCount, v.Content})
	case ProviderReviewFeedbackContentRetryable:
		return json.Marshal(struct {
			Kind         ProviderReviewFeedbackContentOutcomeKind `json:"kind"`
			Code         ProviderReviewFeedbackContentErrorCode   `json:"code"`
			RetryAfterMS *uint64                                  `json:"retry_after_ms,omitempty"`
		}{v.Kind, v.Code, v.RetryAfterMS})
	default:
		return json.Marshal(struct {
			Kind ProviderReviewFeedbackContentOutcomeKind `json:"kind"`
			Code ProviderReviewFeedbackContentErrorCode   `json:"code"`
		}{v.Kind, v.Code})
	}
}

type ProviderReviewFeedbackContentResultEvent struct {
	Kind string `json:"kind"`
	ProviderReviewFeedbackContentAuthority
	CommandMessageID string                                     `json:"command_message_id"`
	ObservedAt       time.Time                                  `json:"observed_at"`
	Outcome          ProviderReviewFeedbackContentResultOutcome `json:"outcome"`
}
