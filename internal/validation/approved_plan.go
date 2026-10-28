package validation

import (
	"fmt"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const maxApprovedPlanBytes = 800 << 10

func ApprovedPlan(job v1.JobSpec) error {
	if job.Kind != v1.JobKindBuild {
		if job.ApprovedPlan != nil {
			return fmt.Errorf("plan job cannot contain approved_plan")
		}
		return nil
	}
	if job.ApprovedPlan == nil {
		return fmt.Errorf("build job requires approved_plan")
	}
	plan := *job.ApprovedPlan
	if err := ID("approved_plan.revision_id", plan.RevisionID); err != nil {
		return err
	}
	if plan.Revision < 1 {
		return fmt.Errorf("approved_plan.revision must be positive")
	}
	if plan.Revision != job.ApprovedPlanRevision {
		return fmt.Errorf("approved_plan.revision must equal approved_plan_revision")
	}
	if err := SHA256Digest("approved_plan.digest", plan.Digest); err != nil {
		return err
	}
	if err := PlanContent(plan.Content); err != nil {
		return fmt.Errorf("approved_plan.content: %w", err)
	}
	return aggregateJSONLimit("approved_plan", plan, maxApprovedPlanBytes)
}
