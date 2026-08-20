package v1

import "time"

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

	ApprovedPlanRevision         int                  `json:"approved_plan_revision,omitempty"`
	RequiredChecks               []string             `json:"required_checks,omitempty"`
	PushBranch                   bool                 `json:"push_branch,omitempty"`
	PRTitle                      string               `json:"pr_title,omitempty"`
	ReviewComment                string               `json:"review_comment,omitempty"`
	ContinuationCount            int                  `json:"continuation_count,omitempty"`
	ResumeSessionID              string               `json:"resume_session_id,omitempty"`
	ProviderCapacityResumePolicy CapacityResumePolicy `json:"provider_capacity_resume_policy,omitempty"`
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
	Command            string                       `json:"command"`
	Job                *JobSpec                     `json:"job,omitempty"`
	JobID              string                       `json:"job_id,omitempty"`
	AttemptID          string                       `json:"attempt_id,omitempty"`
	FencingToken       uint64                       `json:"fencing_token,omitempty"`
	LeaseExpiresAt     time.Time                    `json:"lease_expires_at,omitempty"`
	Reason             string                       `json:"reason,omitempty"`
	QuestionID         string                       `json:"question_id,omitempty"`
	Decision           string                       `json:"decision,omitempty"`
	DecidedBy          string                       `json:"decided_by,omitempty"`
	UpdateID           string                       `json:"update_id,omitempty"`
	DrainDeadline      *time.Time                   `json:"drain_deadline,omitempty"`
	SourcePublication  *SourcePublicationOperation  `json:"source_publication,omitempty"`
	PreviewObservation *PreviewObservationOperation `json:"preview_observation,omitempty"`
}

const (
	CmdStartJob           = "start_job"
	CmdCancelJob          = "cancel_job"
	CmdEmergencyStop      = "emergency_stop"
	CmdResumeRunner       = "resume_runner"
	CmdReviewComment      = "review_comment"
	CmdUpdate             = "update"
	CmdDecisionResponse   = "decision_response"
	CmdRotateCert         = "rotate_cert"
	CmdRenewLease         = "renew_lease"
	CmdSourcePublication  = "source_publication"
	CmdPreviewObservation = "preview_observation"
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
	JobID             string    `json:"job_id"`
	AttemptID         string    `json:"attempt_id"`
	FencingToken      uint64    `json:"fencing_token"`
	PlanStep          string    `json:"plan_step,omitempty"`
	BranchState       string    `json:"branch_state,omitempty"`
	NextAction        string    `json:"next_action,omitempty"`
	ProviderSessionID string    `json:"provider_session_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
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
