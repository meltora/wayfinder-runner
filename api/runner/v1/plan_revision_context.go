package v1

import "time"

type PlanRevisionFeedback struct {
	Summary          string   `json:"summary"`
	RequestedChanges []string `json:"requested_changes"`
	Questions        []string `json:"questions"`
}

type BasePlan struct {
	RevisionID string      `json:"revision_id"`
	Revision   int         `json:"revision"`
	Digest     string      `json:"digest"`
	Content    PlanContent `json:"content"`
}

type PlanRevisionContext struct {
	RequestID     string               `json:"request_id"`
	RequestDigest string               `json:"request_digest"`
	RequestedAt   time.Time            `json:"requested_at"`
	Feedback      PlanRevisionFeedback `json:"feedback"`
	BasePlan      BasePlan             `json:"base_plan"`
}
