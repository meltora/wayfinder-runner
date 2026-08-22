package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

const (
	ProviderReviewerAssignmentReconciliationAuthorityVersion = 1
	ProviderReviewerAssignmentReconciliationTransportVersion = 1
	EventProviderReviewerAssignmentReconciliationResult      = "provider_reviewer_assignment_reconciliation_result"
)

type ProviderReviewerAssignmentReconciliationIntendedReviewer struct {
	ReviewerIdentityID string `json:"reviewer_identity_id"`
	ProviderAccountID  string `json:"provider_account_id"`
}

func ProviderReviewerAssignmentReconciliationIntendedReviewerSetDigest(reviewers []ProviderReviewerAssignmentReconciliationIntendedReviewer) string {
	type canonicalReviewer struct {
		ProviderAccountID  string `json:"provider_account_id"`
		ReviewerIdentityID string `json:"reviewer_identity_id"`
	}
	canonical := make([]canonicalReviewer, len(reviewers))
	for index, reviewer := range reviewers {
		canonical[index] = canonicalReviewer{ProviderAccountID: reviewer.ProviderAccountID, ReviewerIdentityID: reviewer.ReviewerIdentityID}
	}
	sort.Slice(canonical, func(i, j int) bool {
		if canonical[i].ProviderAccountID == canonical[j].ProviderAccountID {
			return canonical[i].ReviewerIdentityID < canonical[j].ReviewerIdentityID
		}
		return canonical[i].ProviderAccountID < canonical[j].ProviderAccountID
	})
	raw, err := json.Marshal(canonical)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

type ProviderReviewerAssignmentReconciliationOperationAuthority struct {
	SchemaVersion                                       int       `json:"schema_version"`
	ProviderReviewerAssignmentReconciliationOperationID string    `json:"provider_reviewer_assignment_reconciliation_operation_id"`
	OrganizationID                                      string    `json:"organization_id"`
	ProjectID                                           string    `json:"project_id"`
	WorkflowRunID                                       string    `json:"workflow_run_id"`
	AssignmentPlanID                                    string    `json:"assignment_plan_id"`
	AssignmentPlanDigest                                string    `json:"assignment_plan_digest"`
	SourcePublicationObservationID                      string    `json:"source_publication_observation_id"`
	DeliveryID                                          string    `json:"delivery_id"`
	AssignmentOperationID                               string    `json:"assignment_operation_id"`
	AssignmentOperationDigest                           string    `json:"assignment_operation_digest"`
	AssignmentCommandDigest                             string    `json:"assignment_command_digest"`
	AssignmentObservationID                             string    `json:"assignment_observation_id"`
	AssignmentResultDeliveryID                          string    `json:"assignment_result_delivery_id"`
	AssignmentResultPayloadChecksum                     string    `json:"assignment_result_payload_checksum"`
	RepositoryID                                        string    `json:"repository_id"`
	ProviderRepositoryID                                string    `json:"provider_repository_id"`
	ProviderRepositoryPath                              string    `json:"provider_repository_path,omitempty"`
	ExternalRequestID                                   string    `json:"external_request_id"`
	ExpectedHeadSHA                                     string    `json:"expected_head_sha"`
	InstallationID                                      string    `json:"installation_id"`
	InstallationStateRevision                           uint64    `json:"installation_state_revision"`
	InstallationAuthorityRevision                       uint64    `json:"installation_authority_revision"`
	InstallationAuthorityDigest                         string    `json:"installation_authority_digest"`
	Provider                                            string    `json:"provider"`
	ProviderInstanceKey                                 string    `json:"provider_instance_key"`
	IdempotencyKey                                      string    `json:"idempotency_key"`
	AssignmentRecipientSetDigest                        string    `json:"assignment_recipient_set_digest"`
	IntendedReviewerSetDigest                           string    `json:"intended_reviewer_set_digest"`
	OperationDigest                                     string    `json:"operation_digest"`
	CommandDigest                                       string    `json:"command_digest"`
	LeaseID                                             string    `json:"lease_id"`
	FencingToken                                        uint64    `json:"fencing_token"`
	LeaseExpiresAt                                      time.Time `json:"lease_expires_at"`
}

type ProviderReviewerAssignmentReconciliationOperation struct {
	ProviderReviewerAssignmentReconciliationOperationAuthority
	IntendedReviewers []ProviderReviewerAssignmentReconciliationIntendedReviewer `json:"intended_reviewers"`
}

type ProviderReviewerAssignmentReconciliationOutcomeKind string
type ProviderReviewerAssignmentReconciliationConclusion string
type ProviderReviewerAssignmentReconciliationRequestState string
type ProviderReviewerAssignmentReconciliationPresence string
type ProviderReviewerAssignmentReconciliationErrorCode string

const (
	ProviderReviewerAssignmentReconciliationSucceeded ProviderReviewerAssignmentReconciliationOutcomeKind = "succeeded"
	ProviderReviewerAssignmentReconciliationRetryable ProviderReviewerAssignmentReconciliationOutcomeKind = "retryable"
	ProviderReviewerAssignmentReconciliationPermanent ProviderReviewerAssignmentReconciliationOutcomeKind = "permanent"

	ProviderReviewerAssignmentReconciliationSatisfied  ProviderReviewerAssignmentReconciliationConclusion = "satisfied"
	ProviderReviewerAssignmentReconciliationAbsent     ProviderReviewerAssignmentReconciliationConclusion = "absent"
	ProviderReviewerAssignmentReconciliationPartial    ProviderReviewerAssignmentReconciliationConclusion = "partial"
	ProviderReviewerAssignmentReconciliationSuperseded ProviderReviewerAssignmentReconciliationConclusion = "superseded"

	ProviderReviewerAssignmentReconciliationRequestOpen   ProviderReviewerAssignmentReconciliationRequestState = "open"
	ProviderReviewerAssignmentReconciliationRequestClosed ProviderReviewerAssignmentReconciliationRequestState = "closed"
	ProviderReviewerAssignmentReconciliationRequestMerged ProviderReviewerAssignmentReconciliationRequestState = "merged"

	ProviderReviewerAssignmentReconciliationRequested  ProviderReviewerAssignmentReconciliationPresence = "requested"
	ProviderReviewerAssignmentReconciliationReviewed   ProviderReviewerAssignmentReconciliationPresence = "reviewed"
	ProviderReviewerAssignmentReconciliationNotPresent ProviderReviewerAssignmentReconciliationPresence = "absent"

	ProviderReviewerAssignmentReconciliationProviderUnavailable          ProviderReviewerAssignmentReconciliationErrorCode = "provider_unavailable"
	ProviderReviewerAssignmentReconciliationRateLimited                  ProviderReviewerAssignmentReconciliationErrorCode = "rate_limited"
	ProviderReviewerAssignmentReconciliationRequestNotFound              ProviderReviewerAssignmentReconciliationErrorCode = "request_not_found"
	ProviderReviewerAssignmentReconciliationRepositoryMismatch           ProviderReviewerAssignmentReconciliationErrorCode = "repository_mismatch"
	ProviderReviewerAssignmentReconciliationInstallationInactive         ProviderReviewerAssignmentReconciliationErrorCode = "installation_inactive"
	ProviderReviewerAssignmentReconciliationInstallationAuthorityChanged ProviderReviewerAssignmentReconciliationErrorCode = "installation_authority_changed"
	ProviderReviewerAssignmentReconciliationInstallationPermissionDenied ProviderReviewerAssignmentReconciliationErrorCode = "installation_permission_denied"
	ProviderReviewerAssignmentReconciliationUnsupportedProviderInstance  ProviderReviewerAssignmentReconciliationErrorCode = "unsupported_provider_instance"
	ProviderReviewerAssignmentReconciliationObservationIncomplete        ProviderReviewerAssignmentReconciliationErrorCode = "observation_incomplete"
	ProviderReviewerAssignmentReconciliationInvalidProviderResponse      ProviderReviewerAssignmentReconciliationErrorCode = "invalid_provider_response"
)

type ProviderReviewerAssignmentReconciliationObservedReviewer struct {
	ReviewerIdentityID string                                           `json:"reviewer_identity_id"`
	ProviderAccountID  string                                           `json:"provider_account_id"`
	Presence           ProviderReviewerAssignmentReconciliationPresence `json:"presence"`
}

type ProviderReviewerAssignmentReconciliationResultOutcome struct {
	Kind              ProviderReviewerAssignmentReconciliationOutcomeKind        `json:"kind"`
	Conclusion        ProviderReviewerAssignmentReconciliationConclusion         `json:"conclusion,omitempty"`
	RequestState      ProviderReviewerAssignmentReconciliationRequestState       `json:"request_state,omitempty"`
	ObservedHeadSHA   string                                                     `json:"observed_head_sha,omitempty"`
	IntendedReviewers []ProviderReviewerAssignmentReconciliationObservedReviewer `json:"intended_reviewers,omitempty"`
	Code              ProviderReviewerAssignmentReconciliationErrorCode          `json:"code,omitempty"`
	RetryAfterMS      *uint64                                                    `json:"retry_after_ms,omitempty"`
}

type ProviderReviewerAssignmentReconciliationResultEvent struct {
	Kind string `json:"kind"`
	ProviderReviewerAssignmentReconciliationOperationAuthority
	CommandMessageID string                                                `json:"command_message_id"`
	ObservedAt       time.Time                                             `json:"observed_at"`
	Outcome          ProviderReviewerAssignmentReconciliationResultOutcome `json:"outcome"`
}
