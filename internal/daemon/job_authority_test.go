package daemon

import (
	"fmt"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/lease"
)

func withTestAuthority(job v1.JobSpec) v1.JobSpec {
	job.ProjectID = "project-1"
	job.LeaseID = "lease-" + job.AttemptID
	job.ManifestID = "manifest-" + job.JobID
	job.ManifestDigest = strings.Repeat("a", 64)
	job.WorkflowRevision = 1
	if job.Kind == v1.JobKindPlan {
		for index := range job.Repositories {
			if job.Repositories[index].RepositoryHeadObservationID == "" {
				job.Repositories[index].RepositoryHeadObservationID = fmt.Sprintf("head-observation-%d", index+1)
			}
			if job.Repositories[index].BaseBranch == "" {
				job.Repositories[index].BaseBranch = "main"
			}
			if job.Repositories[index].BaseSHA == "" {
				job.Repositories[index].BaseSHA = strings.Repeat(fmt.Sprintf("%x", index%15+1), 40)
			}
		}
	}
	return job
}

func authoritativeTestLease(jobID, attemptID string, fencingToken uint64, expiresAt time.Time) lease.Lease {
	job := withTestAuthority(v1.JobSpec{
		JobID: jobID, AttemptID: attemptID, Kind: v1.JobKindBuild, FencingToken: fencingToken,
	})
	return lease.Lease{
		LeaseID: job.LeaseID, JobID: job.JobID, AttemptID: job.AttemptID, Kind: job.Kind,
		ManifestID: job.ManifestID, ManifestDigest: job.ManifestDigest, WorkflowRevision: job.WorkflowRevision,
		FencingToken: job.FencingToken, AcquiredAt: time.Now(), ExpiresAt: expiresAt,
	}
}

func validDaemonRevisionContext() *v1.PlanRevisionContext {
	return &v1.PlanRevisionContext{
		RequestID: "request-1", RequestDigest: strings.Repeat("b", 64), RequestedAt: time.Date(2026, 8, 14, 18, 0, 0, 0, time.UTC),
		Feedback: v1.PlanRevisionFeedback{Summary: "Clarify rollback.", RequestedChanges: []string{"Add recovery evidence."}, Questions: []string{}},
		BasePlan: v1.BasePlan{
			RevisionID: "revision-1", Revision: 1, Digest: strings.Repeat("c", 64),
			Content: v1.PlanContent{
				Behavior: "Preserve reviewed behavior.", InScope: []string{"Revision planning"}, OutOfScope: []string{},
				Assumptions: []string{}, UnansweredQuestions: []string{}, Architecture: "Use immutable authority.",
				APIImpact: "None.", DataImpact: "None.", SecurityImpact: "Fail closed.", OperationalImpact: "Audited retry.",
				PreviewRequirements: "No preview.", AutomatedTestStrategy: "Contract tests.", ManualTestPlan: "Review output.",
				RolloutStrategy: "Runner first.", RollbackStrategy: "Disable dispatch.", Risks: []string{},
				Alternatives: []string{}, Dependencies: []string{}, DeliveryOrder: []string{"Validate", "publish"},
			},
		},
	}
}

func TestPassiveJobEvidenceUsesExactCommandAuthority(t *testing.T) {
	jr, captured := testJobRunner(t)

	if _, err := call(t, jr, "platform_report_progress", `{"milestone":"planning","idempotency_key":"progress-authority"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, jr, "platform_checkpoint", `{"plan_step":"review feedback","idempotency_key":"checkpoint-authority"}`); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"progress", "checkpoint"} {
		events := captured.byKind(kind)
		if len(events) != 1 {
			t.Fatalf("%s events = %v", kind, events)
		}
		event := events[0]
		want := map[string]any{
			"manifest_kind":   "build",
			"lease_id":        "lease-1",
			"job_id":          "j1",
			"attempt_id":      "a1",
			"manifest_id":     "manifest-1",
			"manifest_digest": strings.Repeat("a", 64),
			"fencing_token":   uint64(7),
		}
		for field, expected := range want {
			if event[field] != expected {
				t.Fatalf("%s %s = %v, want %v", kind, field, event[field], expected)
			}
		}
		observed, ok := event["observed_at"].(string)
		if !ok || observed == "" {
			t.Fatalf("%s observed_at = %v", kind, event["observed_at"])
		}
	}
}

func TestRevisionPlanningContextIsAvailableWithoutEmbeddingMutableAuthorityInPrompt(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.job.Kind = "plan"
	jr.job.ApprovedPlanRevision = 0
	jr.job.PlanRevisionContext = validDaemonRevisionContext()

	prompt := jr.buildPrompt()
	if !strings.Contains(prompt, "request-1") || !strings.Contains(prompt, "platform_get_job_context") {
		t.Fatalf("revision prompt does not identify structured context: %q", prompt)
	}
	if strings.Contains(prompt, jr.job.PlanRevisionContext.BasePlan.Content.Architecture) {
		t.Fatal("large reviewed plan was copied into the process command prompt")
	}

	result, err := call(t, jr, "platform_get_job_context", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	context := result.(map[string]any)
	if context["plan_revision_context"] != jr.job.PlanRevisionContext {
		t.Fatalf("revision context = %#v", context["plan_revision_context"])
	}
	if context["workflow_revision"] != 2 {
		t.Fatalf("workflow revision = %v", context["workflow_revision"])
	}
}
