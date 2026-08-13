package v1

import "time"

type CapacityResumePolicy string

const (
	CapacityResumeManual    CapacityResumePolicy = "manual"
	CapacityResumeAutomatic CapacityResumePolicy = "automatic"
)

type ProviderCapacityExhaustedEvent struct {
	Kind              string               `json:"kind"`
	JobID             string               `json:"job_id"`
	AttemptID         string               `json:"attempt_id"`
	FencingToken      uint64               `json:"fencing_token"`
	Adapter           string               `json:"adapter"`
	Provider          string               `json:"provider"`
	Model             string               `json:"model,omitempty"`
	Reason            string               `json:"reason"`
	ResumePolicy      CapacityResumePolicy `json:"resume_policy"`
	RetryAt           *time.Time           `json:"retry_at,omitempty"`
	ProviderSessionID string               `json:"provider_session_id,omitempty"`
	BranchRef         string               `json:"branch_ref,omitempty"`
	CommitSHA         string               `json:"commit_sha,omitempty"`
	CheckpointRef     string               `json:"checkpoint_ref,omitempty"`
	Message           string               `json:"message"`
}
