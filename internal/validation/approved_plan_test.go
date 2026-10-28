package validation

import (
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validBuildJob() v1.JobSpec {
	job := validJob()
	job.Kind = v1.JobKindBuild
	job.ApprovedPlanRevision = 2
	job.ApprovedPlan = &v1.ApprovedPlan{
		RevisionID: "revision-2", Revision: 2, Digest: strings.Repeat("c", 64), Content: validPlanContent(),
	}
	return job
}

func TestJobSpecAcceptsBuildJobWithApprovedPlan(t *testing.T) {
	if err := JobSpec(validBuildJob()); err != nil {
		t.Fatalf("valid Build job rejected: %v", err)
	}
}

func TestJobSpecRejectsInvalidApprovedPlan(t *testing.T) {
	cases := map[string]struct {
		mutate func(*v1.JobSpec)
		want   string
	}{
		"missing": {func(job *v1.JobSpec) { job.ApprovedPlan = nil }, "build job requires approved_plan"},
		"revision mismatch": {func(job *v1.JobSpec) { job.ApprovedPlan.Revision = 1 },
			"approved_plan.revision must equal approved_plan_revision"},
		"zero revision":     {func(job *v1.JobSpec) { job.ApprovedPlan.Revision = 0 }, "approved_plan.revision must be positive"},
		"revision id":       {func(job *v1.JobSpec) { job.ApprovedPlan.RevisionID = "bad/id" }, "approved_plan.revision_id"},
		"empty revision id": {func(job *v1.JobSpec) { job.ApprovedPlan.RevisionID = "" }, "approved_plan.revision_id"},
		"uppercase digest":  {func(job *v1.JobSpec) { job.ApprovedPlan.Digest = strings.Repeat("C", 64) }, "approved_plan.digest"},
		"short digest":      {func(job *v1.JobSpec) { job.ApprovedPlan.Digest = "c" }, "approved_plan.digest"},
		"content behavior":  {func(job *v1.JobSpec) { job.ApprovedPlan.Content.Behavior = "  " }, "approved_plan.content: plan.behavior"},
		"content list":      {func(job *v1.JobSpec) { job.ApprovedPlan.Content.DeliveryOrder = nil }, "approved_plan.content: plan.delivery_order"},
		"content item": {func(job *v1.JobSpec) {
			job.ApprovedPlan.Content.InScope = []string{strings.Repeat("x", (16<<10)+1)}
		}, "approved_plan.content: plan.in_scope[0]"},
		"plan job": {func(job *v1.JobSpec) {
			job.Kind = v1.JobKindPlan
			job.ApprovedPlanRevision = 0
		}, "plan job cannot contain approved_plan"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			job := validBuildJob()
			plan := *job.ApprovedPlan
			job.ApprovedPlan = &plan
			tc.mutate(&job)
			err := JobSpec(job)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("JobSpec() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestJobSpecBoundsApprovedPlanAggregate(t *testing.T) {
	item := strings.Repeat("x", 16<<10-16)
	items := make([]string, 0, 46)
	for range 46 {
		items = append(items, item)
	}
	job := validBuildJob()
	job.ApprovedPlan.Content.InScope = items
	if err := JobSpec(job); err != nil {
		t.Fatalf("approved plan under the aggregate limit rejected: %v", err)
	}
	job.ApprovedPlan.Content.Architecture = strings.Repeat("y", 100<<10)
	err := JobSpec(job)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized approved plan error = %v", err)
	}
}

func TestApprovedPlanAggregateLimitIsMeasuredOnTheWholeObject(t *testing.T) {
	job := validBuildJob()
	job.ApprovedPlan.Content.Architecture = strings.Repeat("y", 100)
	if err := ApprovedPlan(job); err != nil {
		t.Fatal(err)
	}
	if maxApprovedPlanBytes != 800<<10 {
		t.Fatalf("approved plan aggregate limit = %d", maxApprovedPlanBytes)
	}
}
