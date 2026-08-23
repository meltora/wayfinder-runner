package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type JobKind string

const (
	JobKindPlan  JobKind = "plan"
	JobKindBuild JobKind = "build"
)

type JobSpec struct {
	JobID               string                    `json:"job_id"`
	AttemptID           string                    `json:"attempt_id"`
	ProjectID           string                    `json:"project_id"`
	Kind                JobKind                   `json:"kind"`
	LeaseID             string                    `json:"lease_id"`
	ManifestID          string                    `json:"manifest_id"`
	ManifestDigest      string                    `json:"manifest_digest"`
	WorkflowRevision    int                       `json:"workflow_revision"`
	FencingToken        uint64                    `json:"fencing_token"`
	LeaseExpiresAt      time.Time                 `json:"lease_expires_at"`
	Repositories        []JobRepository           `json:"repositories"`
	Ticket              TicketContext             `json:"ticket"`
	AgentPack           *AgentPackRef             `json:"agent_pack,omitempty"`
	AgentPackAuthority  *SignedAgentPackAuthority `json:"agent_pack_authority,omitempty"`
	PlanRevisionContext *PlanRevisionContext      `json:"plan_revision_context,omitempty"`
	AgentRunAuthority   *AgentRunAuthority        `json:"agent_run_authority,omitempty"`

	ApprovedPlanRevision         int                  `json:"approved_plan_revision,omitempty"`
	RequiredChecks               []string             `json:"required_checks,omitempty"`
	PushBranch                   bool                 `json:"push_branch,omitempty"`
	PRTitle                      string               `json:"pr_title,omitempty"`
	ReviewComment                string               `json:"review_comment,omitempty"`
	ContinuationCount            int                  `json:"continuation_count,omitempty"`
	ResumeSessionID              string               `json:"resume_session_id,omitempty"`
	ProviderCapacityResumePolicy CapacityResumePolicy `json:"provider_capacity_resume_policy,omitempty"`

	reviewCommentPresent bool
}

func (job JobSpec) HasReviewCommentField() bool {
	return job.reviewCommentPresent || job.ReviewComment != ""
}

func (job *JobSpec) UnmarshalJSON(data []byte) error {
	type wireJobSpec JobSpec
	var decoded wireJobSpec
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("decode job spec: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("decode job spec: trailing JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("decode job fields: %w", err)
	}
	*job = JobSpec(decoded)
	_, job.reviewCommentPresent = fields["review_comment"]
	return nil
}

type JobRepository struct {
	RepositoryID string `json:"repository_id"`
	BaseBranch   string `json:"base_branch,omitempty"`
	Primary      bool   `json:"primary,omitempty"`
}

type TicketContext struct {
	Key                string `json:"key"`
	Summary            string `json:"summary"`
	Description        string `json:"description"`
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"`
}

type AgentPackRef struct {
	Version string `json:"version"`
	Digest  string `json:"digest"`
	URL     string `json:"url"`
	KeyID   string `json:"key_id"`
}

type CommandPayload struct {
	Command                                  string                                             `json:"command"`
	Job                                      *JobSpec                                           `json:"job,omitempty"`
	JobID                                    string                                             `json:"job_id,omitempty"`
	AttemptID                                string                                             `json:"attempt_id,omitempty"`
	LeaseID                                  string                                             `json:"lease_id,omitempty"`
	ManifestID                               string                                             `json:"manifest_id,omitempty"`
	ManifestDigest                           string                                             `json:"manifest_digest,omitempty"`
	FencingToken                             uint64                                             `json:"fencing_token,omitempty"`
	LeaseExpiresAt                           time.Time                                          `json:"lease_expires_at,omitempty"`
	Reason                                   string                                             `json:"reason,omitempty"`
	QuestionID                               string                                             `json:"question_id,omitempty"`
	Decision                                 string                                             `json:"decision,omitempty"`
	DecidedBy                                string                                             `json:"decided_by,omitempty"`
	UpdateID                                 string                                             `json:"update_id,omitempty"`
	DrainDeadline                            *time.Time                                         `json:"drain_deadline,omitempty"`
	SourcePublication                        *SourcePublicationOperation                        `json:"source_publication,omitempty"`
	ProviderReviewerAssignment               *ProviderReviewerAssignmentOperation               `json:"provider_reviewer_assignment,omitempty"`
	ProviderReviewerAssignmentReconciliation *ProviderReviewerAssignmentReconciliationOperation `json:"provider_reviewer_assignment_reconciliation,omitempty"`
	ProviderReviewObservation                *ProviderReviewObservationOperation                `json:"provider_review_observation,omitempty"`
	ProviderReviewFeedbackContent            *ProviderReviewFeedbackContentOperation            `json:"provider_review_feedback_content,omitempty"`
	ReviewFeedbackContinuation               *ReviewFeedbackContinuationAuthority               `json:"review_feedback_continuation,omitempty"`
	PreviewObservation                       *PreviewObservationOperation                       `json:"preview_observation,omitempty"`
	PreviewLifecycle                         *PreviewLifecycleOperation                         `json:"preview_lifecycle,omitempty"`
	ReviewerIdentityVerification             *ReviewerIdentityVerificationOperation             `json:"reviewer_identity_verification,omitempty"`
	ControlID                                string                                             `json:"control_id,omitempty"`
	Action                                   JobControlAction                                   `json:"action,omitempty"`
	RequestedAt                              string                                             `json:"requested_at,omitempty"`
	ExpiresAt                                string                                             `json:"expires_at,omitempty"`
	AgentRunAuthority                        *AgentRunAuthority                                 `json:"agent_run_authority,omitempty"`
	CommandDigest                            string                                             `json:"command_digest,omitempty"`
}

func (command CommandPayload) MarshalJSON() ([]byte, error) {
	type wireCommandPayload CommandPayload
	raw, err := json.Marshal(wireCommandPayload(command))
	if err != nil {
		return nil, err
	}
	if !command.LeaseExpiresAt.IsZero() {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	delete(fields, "lease_expires_at")
	return json.Marshal(fields)
}

const (
	CmdStartJob                                 = "start_job"
	CmdCancelJob                                = "cancel_job"
	CmdEmergencyStop                            = "emergency_stop"
	CmdResumeRunner                             = "resume_runner"
	CmdReviewComment                            = "review_comment"
	CmdUpdate                                   = "update"
	CmdDecisionResponse                         = "decision_response"
	CmdRotateCert                               = "rotate_cert"
	CmdRenewLease                               = "renew_lease"
	CmdSourcePublication                        = "source_publication"
	CmdProviderReviewerAssignment               = "provider_reviewer_assignment"
	CmdProviderReviewerAssignmentReconciliation = "provider_reviewer_assignment_reconciliation"
	CmdProviderReviewObservation                = "provider_review_observation"
	CmdProviderReviewFeedbackContent            = "provider_review_feedback_content"
	CmdReviewFeedbackContinuation               = "review_feedback_continuation"
	CmdPreviewObservation                       = "preview_observation"
	CmdPreviewLifecycle                         = "preview_lifecycle"
	CmdReviewerIdentityVerification             = "reviewer_identity_verification"
	CmdControlJob                               = "control_job"
)

type ProgressReport struct {
	JobID        string `json:"job_id"`
	AttemptID    string `json:"attempt_id"`
	FencingToken uint64 `json:"fencing_token"`
	Milestone    string `json:"milestone"`
	CurrentTask  string `json:"current_task,omitempty"`
	NextAction   string `json:"next_action,omitempty"`
}

type Checkpoint struct {
	JobID             string                     `json:"job_id"`
	AttemptID         string                     `json:"attempt_id"`
	FencingToken      uint64                     `json:"fencing_token"`
	PlanStep          string                     `json:"plan_step,omitempty"`
	BranchState       string                     `json:"branch_state,omitempty"`
	NextAction        string                     `json:"next_action,omitempty"`
	ProviderSessionID string                     `json:"provider_session_id,omitempty"`
	CreatedAt         time.Time                  `json:"created_at"`
	TrustedRecovery   *TrustedRecoveryCheckpoint `json:"recovery_checkpoint,omitempty"`
}

type ArtifactRef struct {
	JobID        string `json:"job_id"`
	AttemptID    string `json:"attempt_id"`
	FencingToken uint64 `json:"fencing_token"`
	Name         string `json:"name"`
	ContentType  string `json:"content_type"`
	DigestSHA256 string `json:"digest_sha256"`
	SizeBytes    int64  `json:"size_bytes"`
}
