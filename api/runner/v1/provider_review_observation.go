package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

const (
	ProviderReviewObservationAuthorityVersion   = 1
	ProviderReviewObservationTransportVersion   = 1
	ProviderReviewObservationMaxCollectionPages = 10
	ProviderReviewObservationCollectionPageSize = 100
	EventProviderReviewObservationResult        = "provider_review_observation_result"
)

type ProviderReviewObservationSubject struct {
	ReviewerIdentityID                string    `json:"reviewer_identity_id"`
	UserID                            string    `json:"user_id"`
	OrganizationMembershipID          string    `json:"organization_membership_id"`
	IdentityStateRevision             uint64    `json:"identity_state_revision"`
	IdentityRevision                  uint64    `json:"identity_revision"`
	ProviderAccountProofID            string    `json:"provider_account_proof_id"`
	ProviderAccountProofStateRevision uint64    `json:"provider_account_proof_state_revision"`
	ProviderAccountProofRevision      uint64    `json:"provider_account_proof_revision"`
	ProviderAccountProofDigest        string    `json:"provider_account_proof_digest"`
	ProviderAccountProofExpiresAt     time.Time `json:"provider_account_proof_expires_at"`
	IdentityVerificationID            string    `json:"identity_verification_id"`
	IdentityVerificationDigest        string    `json:"identity_verification_digest"`
	IdentityVerificationExpiresAt     time.Time `json:"identity_verification_expires_at"`
	ProviderAccountID                 string    `json:"provider_account_id"`
}

func ProviderReviewObservationSubjectIdentitySetDigest(values []ProviderReviewObservationSubject) string {
	type canonical struct {
		IdentityRevision                  uint64 `json:"identity_revision"`
		IdentityStateRevision             uint64 `json:"identity_state_revision"`
		IdentityVerificationDigest        string `json:"identity_verification_digest"`
		IdentityVerificationExpiresAt     string `json:"identity_verification_expires_at"`
		IdentityVerificationID            string `json:"identity_verification_id"`
		OrganizationMembershipID          string `json:"organization_membership_id"`
		ProviderAccountID                 string `json:"provider_account_id"`
		ProviderAccountProofDigest        string `json:"provider_account_proof_digest"`
		ProviderAccountProofExpiresAt     string `json:"provider_account_proof_expires_at"`
		ProviderAccountProofID            string `json:"provider_account_proof_id"`
		ProviderAccountProofRevision      uint64 `json:"provider_account_proof_revision"`
		ProviderAccountProofStateRevision uint64 `json:"provider_account_proof_state_revision"`
		ReviewerIdentityID                string `json:"reviewer_identity_id"`
		UserID                            string `json:"user_id"`
	}
	items := make([]canonical, len(values))
	for i, v := range values {
		items[i] = canonical{v.IdentityRevision, v.IdentityStateRevision, v.IdentityVerificationDigest, canonicalAssignmentTime(v.IdentityVerificationExpiresAt), v.IdentityVerificationID, v.OrganizationMembershipID, v.ProviderAccountID, v.ProviderAccountProofDigest, canonicalAssignmentTime(v.ProviderAccountProofExpiresAt), v.ProviderAccountProofID, v.ProviderAccountProofRevision, v.ProviderAccountProofStateRevision, v.ReviewerIdentityID, v.UserID}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ProviderAccountID == items[j].ProviderAccountID {
			return items[i].ReviewerIdentityID < items[j].ReviewerIdentityID
		}
		return items[i].ProviderAccountID < items[j].ProviderAccountID
	})
	raw, err := json.Marshal(items)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

type ProviderReviewObservationAuthority struct {
	SchemaVersion                        int       `json:"schema_version"`
	ProviderReviewObservationOperationID string    `json:"provider_review_observation_operation_id"`
	OrganizationID                       string    `json:"organization_id"`
	ProjectID                            string    `json:"project_id"`
	WorkflowRunID                        string    `json:"workflow_run_id"`
	AssignmentDeliveryID                 string    `json:"assignment_delivery_id"`
	AssignmentDeliveryStateRevision      uint64    `json:"assignment_delivery_state_revision"`
	AssignmentPlanID                     string    `json:"assignment_plan_id"`
	AssignmentPlanDigest                 string    `json:"assignment_plan_digest"`
	AssignmentObservationID              string    `json:"assignment_observation_id"`
	AssignmentResultPayloadChecksum      string    `json:"assignment_result_payload_checksum"`
	SourcePublicationObservationID       string    `json:"source_publication_observation_id"`
	RepositoryID                         string    `json:"repository_id"`
	ProviderRepositoryID                 string    `json:"provider_repository_id"`
	ProviderRepositoryPath               string    `json:"provider_repository_path,omitempty"`
	ExternalRequestID                    string    `json:"external_request_id"`
	ExpectedHeadSHA                      string    `json:"expected_head_sha"`
	InstallationID                       string    `json:"installation_id"`
	InstallationStateRevision            uint64    `json:"installation_state_revision"`
	InstallationAuthorityRevision        uint64    `json:"installation_authority_revision"`
	InstallationAuthorityDigest          string    `json:"installation_authority_digest"`
	Provider                             string    `json:"provider"`
	ProviderInstanceKey                  string    `json:"provider_instance_key"`
	IdempotencyKey                       string    `json:"idempotency_key"`
	SubjectIdentitySetDigest             string    `json:"subject_identity_set_digest"`
	MaxCollectionPages                   int       `json:"max_collection_pages"`
	CollectionPageSize                   int       `json:"collection_page_size"`
	OperationDigest                      string    `json:"operation_digest"`
	CommandDigest                        string    `json:"command_digest"`
	LeaseID                              string    `json:"lease_id"`
	FencingToken                         uint64    `json:"fencing_token"`
	LeaseExpiresAt                       time.Time `json:"lease_expires_at"`
}
type ProviderReviewObservationOperation struct {
	ProviderReviewObservationAuthority
	Subjects []ProviderReviewObservationSubject `json:"subjects"`
}

type ProviderReviewObservationReviewState string
type ProviderReviewObservationReviewEventKind string
type ProviderReviewObservationCommentKind string
type ProviderReviewObservationCommentNamespace string
type ProviderReviewObservationOutcomeKind string
type ProviderReviewObservationErrorCode string
type ProviderReviewObservationRequestState string

const (
	ProviderReviewObservationUnreviewed       ProviderReviewObservationReviewState      = "unreviewed"
	ProviderReviewObservationReviewStarted    ProviderReviewObservationReviewState      = "review_started"
	ProviderReviewObservationReviewed         ProviderReviewObservationReviewState      = "reviewed"
	ProviderReviewObservationCommented        ProviderReviewObservationReviewState      = "commented"
	ProviderReviewObservationApproved         ProviderReviewObservationReviewState      = "approved"
	ProviderReviewObservationChangesRequested ProviderReviewObservationReviewState      = "changes_requested"
	ProviderReviewObservationDismissed        ProviderReviewObservationReviewState      = "dismissed"
	ProviderReviewObservationUnapproved       ProviderReviewObservationReviewState      = "unapproved"
	ProviderReviewObservationReview           ProviderReviewObservationReviewEventKind  = "review"
	ProviderReviewObservationReviewerState    ProviderReviewObservationReviewEventKind  = "reviewer_state"
	ProviderReviewObservationApproval         ProviderReviewObservationReviewEventKind  = "approval"
	ProviderReviewObservationChangeRequest    ProviderReviewObservationReviewEventKind  = "change_request"
	ProviderReviewObservationDismissal        ProviderReviewObservationReviewEventKind  = "dismissal"
	ProviderReviewObservationGeneral          ProviderReviewObservationCommentKind      = "general"
	ProviderReviewObservationInline           ProviderReviewObservationCommentKind      = "inline"
	ProviderReviewObservationThreadReply      ProviderReviewObservationCommentKind      = "thread_reply"
	ProviderReviewObservationDiscussion       ProviderReviewObservationCommentNamespace = "discussion"
	ProviderReviewObservationIssue            ProviderReviewObservationCommentNamespace = "issue"
	ProviderReviewObservationPullRequest      ProviderReviewObservationCommentNamespace = "pull_request"
	ProviderReviewObservationReviewNamespace  ProviderReviewObservationCommentNamespace = "review"
	ProviderReviewObservationSucceeded        ProviderReviewObservationOutcomeKind      = "succeeded"
	ProviderReviewObservationRetryable        ProviderReviewObservationOutcomeKind      = "retryable"
	ProviderReviewObservationPermanent        ProviderReviewObservationOutcomeKind      = "permanent"
	ProviderReviewObservationRequestOpen      ProviderReviewObservationRequestState     = "open"
	ProviderReviewObservationRequestClosed    ProviderReviewObservationRequestState     = "closed"
	ProviderReviewObservationRequestMerged    ProviderReviewObservationRequestState     = "merged"
)

const (
	ProviderReviewObservationProviderUnavailable          ProviderReviewObservationErrorCode = "provider_unavailable"
	ProviderReviewObservationRateLimited                  ProviderReviewObservationErrorCode = "rate_limited"
	ProviderReviewObservationRequestNotFound              ProviderReviewObservationErrorCode = "request_not_found"
	ProviderReviewObservationRepositoryMismatch           ProviderReviewObservationErrorCode = "repository_mismatch"
	ProviderReviewObservationInstallationInactive         ProviderReviewObservationErrorCode = "installation_inactive"
	ProviderReviewObservationInstallationAuthorityChanged ProviderReviewObservationErrorCode = "installation_authority_changed"
	ProviderReviewObservationInstallationPermissionDenied ProviderReviewObservationErrorCode = "installation_permission_denied"
	ProviderReviewObservationSubjectIdentityChanged       ProviderReviewObservationErrorCode = "subject_identity_changed"
	ProviderReviewObservationSubjectIdentityExpired       ProviderReviewObservationErrorCode = "subject_identity_expired"
	ProviderReviewObservationProviderAccountProofChanged  ProviderReviewObservationErrorCode = "provider_account_proof_changed"
	ProviderReviewObservationProviderAccountProofExpired  ProviderReviewObservationErrorCode = "provider_account_proof_expired"
	ProviderReviewObservationUnsupportedProviderInstance  ProviderReviewObservationErrorCode = "unsupported_provider_instance"
	ProviderReviewObservationIncomplete                   ProviderReviewObservationErrorCode = "observation_incomplete"
	ProviderReviewObservationInvalidProviderResponse      ProviderReviewObservationErrorCode = "invalid_provider_response"
)

type ProviderReviewObservationDerivedReviewEventIdentity struct {
	Provider             string                                   `json:"provider"`
	ProviderInstanceKey  string                                   `json:"provider_instance_key"`
	ProviderRepositoryID string                                   `json:"provider_repository_id"`
	ExternalRequestID    string                                   `json:"external_request_id"`
	ReviewEventKind      ProviderReviewObservationReviewEventKind `json:"review_event_kind"`
	ProviderAccountID    string                                   `json:"provider_account_id"`
	OccurredAt           time.Time                                `json:"occurred_at"`
}

func ProviderReviewObservationDerivedReviewEventID(v ProviderReviewObservationDerivedReviewEventIdentity) string {
	type canonical struct {
		ExternalRequestID    string                                   `json:"external_request_id"`
		OccurredAt           string                                   `json:"occurred_at"`
		Provider             string                                   `json:"provider"`
		ProviderAccountID    string                                   `json:"provider_account_id"`
		ProviderInstanceKey  string                                   `json:"provider_instance_key"`
		ProviderRepositoryID string                                   `json:"provider_repository_id"`
		ReviewEventKind      ProviderReviewObservationReviewEventKind `json:"review_event_kind"`
	}
	raw, _ := json.Marshal(canonical{v.ExternalRequestID, canonicalAssignmentTime(v.OccurredAt), v.Provider, v.ProviderAccountID, v.ProviderInstanceKey, v.ProviderRepositoryID, v.ReviewEventKind})
	digest := sha256.Sum256(raw)
	return "derived-" + hex.EncodeToString(digest[:])
}

type ProviderReviewObservationReviewEvent struct {
	ProviderReviewEventID       string                                   `json:"provider_review_event_id"`
	ProviderReviewEventRevision uint64                                   `json:"provider_review_event_revision"`
	ProviderAccountID           string                                   `json:"provider_account_id"`
	ReviewEventKind             ProviderReviewObservationReviewEventKind `json:"review_event_kind"`
	ReviewState                 ProviderReviewObservationReviewState     `json:"review_state"`
	OccurredAt                  time.Time                                `json:"occurred_at"`
	ProviderUpdatedAt           *time.Time                               `json:"provider_updated_at,omitempty"`
	CommitSHA                   string                                   `json:"commit_sha,omitempty"`
	ContentDigest               string                                   `json:"content_digest"`
	ContentByteCount            uint64                                   `json:"content_byte_count"`
}
type ProviderReviewObservationCommentPosition struct {
	FilePath  string `json:"file_path"`
	Line      uint64 `json:"line"`
	Side      string `json:"side"`
	CommitSHA string `json:"commit_sha"`
}
type ProviderReviewObservationComment struct {
	ProviderCommentID        string                                    `json:"provider_comment_id"`
	ProviderCommentRevision  uint64                                    `json:"provider_comment_revision"`
	ProviderAccountID        string                                    `json:"provider_account_id"`
	ProviderCommentNamespace ProviderReviewObservationCommentNamespace `json:"provider_comment_namespace"`
	CommentKind              ProviderReviewObservationCommentKind      `json:"comment_kind"`
	CreatedAt                time.Time                                 `json:"created_at"`
	UpdatedAt                time.Time                                 `json:"updated_at"`
	ThreadID                 string                                    `json:"thread_id,omitempty"`
	ParentCommentID          string                                    `json:"parent_comment_id,omitempty"`
	Position                 *ProviderReviewObservationCommentPosition `json:"position,omitempty"`
	Resolved                 bool                                      `json:"resolved"`
	Deleted                  bool                                      `json:"deleted"`
	ContentDigest            string                                    `json:"content_digest"`
	ContentByteCount         uint64                                    `json:"content_byte_count"`
}
type ProviderReviewObservationCollectionCompleteness struct {
	Complete      bool `json:"complete"`
	PagesRead     int  `json:"pages_read"`
	ItemsObserved int  `json:"items_observed"`
}
type ProviderReviewObservationCollections struct {
	ReviewEvents ProviderReviewObservationCollectionCompleteness `json:"review_events"`
	Comments     ProviderReviewObservationCollectionCompleteness `json:"comments"`
}
type ProviderReviewObservationResultOutcome struct {
	Kind              ProviderReviewObservationOutcomeKind   `json:"kind"`
	RequestState      ProviderReviewObservationRequestState  `json:"request_state,omitempty"`
	ObservedHeadSHA   string                                 `json:"observed_head_sha,omitempty"`
	StaleHead         bool                                   `json:"stale_head,omitempty"`
	SnapshotSemantics string                                 `json:"snapshot_semantics,omitempty"`
	SnapshotComplete  bool                                   `json:"snapshot_complete,omitempty"`
	Collections       *ProviderReviewObservationCollections  `json:"collections,omitempty"`
	ReviewEvents      []ProviderReviewObservationReviewEvent `json:"review_events,omitempty"`
	Comments          []ProviderReviewObservationComment     `json:"comments,omitempty"`
	Code              ProviderReviewObservationErrorCode     `json:"code,omitempty"`
	RetryAfterMS      *uint64                                `json:"retry_after_ms,omitempty"`
}

func (value ProviderReviewObservationResultOutcome) MarshalJSON() ([]byte, error) {
	switch value.Kind {
	case ProviderReviewObservationSucceeded:
		reviews := value.ReviewEvents
		if reviews == nil {
			reviews = []ProviderReviewObservationReviewEvent{}
		}
		comments := value.Comments
		if comments == nil {
			comments = []ProviderReviewObservationComment{}
		}
		return json.Marshal(struct {
			Kind              ProviderReviewObservationOutcomeKind   `json:"kind"`
			RequestState      ProviderReviewObservationRequestState  `json:"request_state"`
			ObservedHeadSHA   string                                 `json:"observed_head_sha"`
			StaleHead         bool                                   `json:"stale_head"`
			SnapshotSemantics string                                 `json:"snapshot_semantics"`
			SnapshotComplete  bool                                   `json:"snapshot_complete"`
			Collections       *ProviderReviewObservationCollections  `json:"collections"`
			ReviewEvents      []ProviderReviewObservationReviewEvent `json:"review_events"`
			Comments          []ProviderReviewObservationComment     `json:"comments"`
		}{value.Kind, value.RequestState, value.ObservedHeadSHA, value.StaleHead, value.SnapshotSemantics, value.SnapshotComplete, value.Collections, reviews, comments})
	case ProviderReviewObservationRetryable:
		return json.Marshal(struct {
			Kind         ProviderReviewObservationOutcomeKind `json:"kind"`
			Code         ProviderReviewObservationErrorCode   `json:"code"`
			RetryAfterMS *uint64                              `json:"retry_after_ms,omitempty"`
		}{value.Kind, value.Code, value.RetryAfterMS})
	case ProviderReviewObservationPermanent:
		return json.Marshal(struct {
			Kind ProviderReviewObservationOutcomeKind `json:"kind"`
			Code ProviderReviewObservationErrorCode   `json:"code"`
		}{value.Kind, value.Code})
	default:
		return json.Marshal(struct {
			Kind ProviderReviewObservationOutcomeKind `json:"kind"`
		}{value.Kind})
	}
}

type ProviderReviewObservationResultEvent struct {
	Kind string `json:"kind"`
	ProviderReviewObservationAuthority
	CommandMessageID string                                 `json:"command_message_id"`
	ObservedAt       time.Time                              `json:"observed_at"`
	Outcome          ProviderReviewObservationResultOutcome `json:"outcome"`
}
