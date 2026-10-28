package v1

type ApprovedPlan struct {
	RevisionID string      `json:"revision_id"`
	Revision   int         `json:"revision"`
	Digest     string      `json:"digest"`
	Content    PlanContent `json:"content"`
}
