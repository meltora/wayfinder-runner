package v1

import (
	"strings"
	"testing"
)

func TestJobManifestAuthorityIsAnExactValueCopy(t *testing.T) {
	job := JobSpec{
		Kind: JobKindPlan, LeaseID: "lease-1", ManifestID: "manifest-1",
		ManifestDigest: strings.Repeat("a", 64), WorkflowRevision: 7,
	}
	want := ManifestAuthority{
		Kind: JobKindPlan, LeaseID: "lease-1", ManifestID: "manifest-1",
		ManifestDigest: strings.Repeat("a", 64), WorkflowRevision: 7,
	}
	if got := job.ManifestAuthority(); got != want {
		t.Fatalf("ManifestAuthority = %+v, want %+v", got, want)
	}
}

func TestCloneJobSpecDeepCopiesNestedCommandData(t *testing.T) {
	job := JobSpec{
		Repositories:   []JobRepository{{RepositoryID: "repo-1"}},
		RequiredChecks: []string{"unit"},
		PlanRevisionContext: &PlanRevisionContext{
			Feedback: PlanRevisionFeedback{RequestedChanges: []string{"add rollback"}},
			BasePlan: BasePlan{Content: PlanContent{Risks: []string{"migration"}}},
		},
	}
	clone, err := CloneJobSpec(job)
	if err != nil {
		t.Fatal(err)
	}
	job.Repositories[0].RepositoryID = "mutated"
	job.RequiredChecks[0] = "mutated"
	job.PlanRevisionContext.Feedback.RequestedChanges[0] = "mutated"
	job.PlanRevisionContext.BasePlan.Content.Risks[0] = "mutated"

	if clone.Repositories[0].RepositoryID != "repo-1" || clone.RequiredChecks[0] != "unit" ||
		clone.PlanRevisionContext.Feedback.RequestedChanges[0] != "add rollback" ||
		clone.PlanRevisionContext.BasePlan.Content.Risks[0] != "migration" {
		t.Fatalf("clone shares nested command state: %+v", clone)
	}
}
