package v1

import (
	"encoding/json"
	"reflect"
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

func TestPlanningRepositoryAuthorityWireRoundTripsExactlyInOrder(t *testing.T) {
	want := []JobRepository{
		{
			RepositoryID: "repo-primary", RepositoryHeadObservationID: "head-observation-1",
			BaseBranch: "main", BaseSHA: strings.Repeat("1", 40), Primary: true,
		},
		{
			RepositoryID: "repo-secondary", RepositoryHeadObservationID: "head-observation-2",
			BaseBranch: "release", BaseSHA: strings.Repeat("2", 40),
		},
	}
	raw, err := json.Marshal(JobSpec{Repositories: want})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"repository_id", "repository_head_observation_id", "base_branch", "base_sha", "primary"} {
		if !strings.Contains(string(raw), `"`+field+`"`) {
			t.Fatalf("planning repository wire omitted %q: %s", field, raw)
		}
	}
	var decoded JobSpec
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Repositories, want) {
		t.Fatalf("repositories = %#v, want %#v", decoded.Repositories, want)
	}
}

func TestPublishedPlanRepositoryAuthorityIncludesEveryField(t *testing.T) {
	raw, err := json.Marshal(PlanRepositoryAuthority{
		RepositoryID: "repo-secondary", RepositoryHeadObservationID: "head-observation-2",
		BaseBranch: "release", BaseSHA: strings.Repeat("2", 40), Primary: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"repository_id", "repository_head_observation_id", "base_branch", "base_sha", "primary"} {
		if !strings.Contains(string(raw), `"`+field+`"`) {
			t.Fatalf("published repository authority omitted %q: %s", field, raw)
		}
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
