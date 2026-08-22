package v1

import (
	"sort"
	"time"
)

const (
	ReviewFeedbackContinuationAuthorityVersion = 1
	ReviewFeedbackContinuationMaxIndex         = 10
	ReviewFeedbackContinuationMaxCommandBudget = 1000
	ReviewFeedbackContinuationMaxContentBytes  = 16 << 10

	ReviewFeedbackContinuationPurpose          = "review_feedback_continuation"
	ReviewFeedbackContinuationContentSource    = "provider_review_event"
	ReviewFeedbackContinuationUntrustedText    = "untrusted_provider_text"
	ReviewFeedbackContinuationNonAuthoritative = "non_authoritative"
)

type ReviewFeedbackContinuationFeedback struct {
	Trust                string `json:"trust"`
	Authority            string `json:"authority"`
	ContentEncoding      string `json:"content_encoding"`
	ContentFormat        string `json:"content_format"`
	UnicodeNormalization string `json:"unicode_normalization"`
	LineEndings          string `json:"line_endings"`
	ContentDigest        string `json:"content_digest"`
	ContentByteCount     uint64 `json:"content_byte_count"`
	Content              string `json:"content"`
}

type ReviewFeedbackContinuationAuthority struct {
	SchemaVersion                                      int                                      `json:"schema_version"`
	Purpose                                            string                                   `json:"purpose"`
	ReviewFeedbackContinuationID                       string                                   `json:"review_feedback_continuation_id"`
	OrganizationID                                     string                                   `json:"organization_id"`
	ProjectID                                          string                                   `json:"project_id"`
	WorkflowRunID                                      string                                   `json:"workflow_run_id"`
	SourceWorkflowRevision                             uint64                                   `json:"source_workflow_revision"`
	ResultingWorkflowRevision                          uint64                                   `json:"resulting_workflow_revision"`
	SourceAttemptID                                    string                                   `json:"source_attempt_id"`
	SourceExecutionManifestID                          string                                   `json:"source_execution_manifest_id"`
	SourceExecutionManifestDigest                      string                                   `json:"source_execution_manifest_digest"`
	AssignmentDeliveryID                               string                                   `json:"assignment_delivery_id"`
	AssignmentDeliveryStateRevision                    uint64                                   `json:"assignment_delivery_state_revision"`
	AssignmentObservationID                            string                                   `json:"assignment_observation_id"`
	ObservedReviewerID                                 string                                   `json:"observed_reviewer_id"`
	ProviderReviewObservationID                        string                                   `json:"provider_review_observation_id"`
	ProviderReviewObservationResultPayloadChecksum     string                                   `json:"provider_review_observation_result_payload_checksum"`
	ContentSource                                      string                                   `json:"content_source"`
	ProviderReviewEventID                              string                                   `json:"provider_review_event_id"`
	ProviderReviewEventRevision                        uint64                                   `json:"provider_review_event_revision"`
	ReviewEventKind                                    ProviderReviewObservationReviewEventKind `json:"review_event_kind"`
	ReviewState                                        ProviderReviewObservationReviewState     `json:"review_state"`
	ReviewEventOccurredAt                              time.Time                                `json:"review_event_occurred_at"`
	ProviderReviewEvidenceDigest                       string                                   `json:"provider_review_evidence_digest"`
	EvidenceObservedAt                                 time.Time                                `json:"evidence_observed_at"`
	ProviderReviewFeedbackContentOperationID           string                                   `json:"provider_review_feedback_content_operation_id"`
	ProviderReviewFeedbackContentResultPayloadChecksum string                                   `json:"provider_review_feedback_content_result_payload_checksum"`
	FeedbackContentRequestDigest                       string                                   `json:"feedback_content_request_digest"`
	FeedbackContentObservedAt                          time.Time                                `json:"feedback_content_observed_at"`
	ContentDigest                                      string                                   `json:"content_digest"`
	ContentByteCount                                   uint64                                   `json:"content_byte_count"`
	ProviderAccountID                                  string                                   `json:"provider_account_id"`
	UserID                                             string                                   `json:"user_id"`
	OrganizationMembershipID                           string                                   `json:"organization_membership_id"`
	ReviewerIdentityID                                 string                                   `json:"reviewer_identity_id"`
	InstallationID                                     string                                   `json:"installation_id"`
	InstallationStateRevision                          uint64                                   `json:"installation_state_revision"`
	InstallationAuthorityRevision                      uint64                                   `json:"installation_authority_revision"`
	InstallationAuthorityDigest                        string                                   `json:"installation_authority_digest"`
	Provider                                           string                                   `json:"provider"`
	ProviderInstanceKey                                string                                   `json:"provider_instance_key"`
	RepositoryID                                       string                                   `json:"repository_id"`
	ProviderRepositoryID                               string                                   `json:"provider_repository_id"`
	ProviderRepositoryPath                             string                                   `json:"provider_repository_path,omitempty"`
	ExternalRequestID                                  string                                   `json:"external_request_id"`
	ExpectedHeadSHA                                    string                                   `json:"expected_head_sha"`
	ApprovedScopeDigest                                string                                   `json:"approved_scope_digest"`
	ContinuationIndex                                  uint64                                   `json:"continuation_index"`
	RemainingCommandBudget                             uint64                                   `json:"remaining_command_budget"`
	IdempotencyKey                                     string                                   `json:"idempotency_key"`
	LeaseID                                            string                                   `json:"lease_id"`
	FencingToken                                       uint64                                   `json:"fencing_token"`
	LeaseExpiresAt                                     time.Time                                `json:"lease_expires_at"`
	DeadlineAt                                         time.Time                                `json:"deadline_at"`
	Feedback                                           ReviewFeedbackContinuationFeedback       `json:"feedback"`
}

type reviewFeedbackContinuationScopeRepository struct {
	RepositoryID string `json:"repository_id"`
	BaseBranch   string `json:"base_branch"`
	Primary      bool   `json:"primary"`
}

type reviewFeedbackContinuationScopeTicket struct {
	Key                string `json:"key"`
	Summary            string `json:"summary"`
	Description        string `json:"description"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
}

type reviewFeedbackContinuationJobScope struct {
	SchemaVersion                int                                         `json:"schema_version"`
	ProjectID                    string                                      `json:"project_id"`
	Kind                         JobKind                                     `json:"kind"`
	Repositories                 []reviewFeedbackContinuationScopeRepository `json:"repositories"`
	Ticket                       reviewFeedbackContinuationScopeTicket       `json:"ticket"`
	AgentPack                    *AgentPackRef                               `json:"agent_pack"`
	AgentPackAuthority           *SignedAgentPackAuthority                   `json:"agent_pack_authority"`
	PlanRevisionContext          *PlanRevisionContext                        `json:"plan_revision_context"`
	ApprovedPlanRevision         int                                         `json:"approved_plan_revision"`
	RequiredChecks               []string                                    `json:"required_checks"`
	PushBranch                   bool                                        `json:"push_branch"`
	PRTitle                      string                                      `json:"pr_title"`
	ResumeSessionID              string                                      `json:"resume_session_id"`
	ProviderCapacityResumePolicy CapacityResumePolicy                        `json:"provider_capacity_resume_policy"`
}

func ReviewFeedbackContinuationJobScopeDigest(job JobSpec) string {
	repositories := make([]reviewFeedbackContinuationScopeRepository, len(job.Repositories))
	for i, repository := range job.Repositories {
		repositories[i] = reviewFeedbackContinuationScopeRepository{
			RepositoryID: repository.RepositoryID,
			BaseBranch:   repository.BaseBranch,
			Primary:      repository.Primary,
		}
	}
	sort.Slice(repositories, func(i, j int) bool {
		return repositories[i].RepositoryID < repositories[j].RepositoryID
	})
	requiredChecks := append([]string(nil), job.RequiredChecks...)
	sort.Strings(requiredChecks)
	if requiredChecks == nil {
		requiredChecks = []string{}
	}
	projection := reviewFeedbackContinuationJobScope{
		SchemaVersion: 1,
		ProjectID:     job.ProjectID,
		Kind:          job.Kind,
		Repositories:  repositories,
		Ticket: reviewFeedbackContinuationScopeTicket{
			Key: job.Ticket.Key, Summary: job.Ticket.Summary,
			Description: job.Ticket.Description, AcceptanceCriteria: job.Ticket.AcceptanceCriteria,
		},
		AgentPack: job.AgentPack, AgentPackAuthority: job.AgentPackAuthority,
		PlanRevisionContext: job.PlanRevisionContext, ApprovedPlanRevision: job.ApprovedPlanRevision,
		RequiredChecks: requiredChecks, PushBranch: job.PushBranch, PRTitle: job.PRTitle,
		ResumeSessionID: job.ResumeSessionID, ProviderCapacityResumePolicy: job.ProviderCapacityResumePolicy,
	}
	digest, err := CommandContentDigest(projection)
	if err != nil {
		return ""
	}
	return digest
}
