package v1_test

import (
	"encoding/json"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

const approvedPlanBuildJob = `{
	"job_id":"job-1","attempt_id":"attempt-1","project_id":"project-1","kind":"build",
	"lease_id":"lease-1","manifest_id":"manifest-1","manifest_digest":"` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `",
	"workflow_revision":2,"fencing_token":3,"lease_expires_at":"2026-10-28T12:00:00Z",
	"repositories":[{"repository_id":"repo-1","primary":true}],
	"ticket":{"key":"KAN-18","summary":"Greeting","description":"Change no other files."},
	"approved_plan_revision":2,
	"approved_plan":{"revision_id":"revision-2","revision":2,"digest":"` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" + `","content":%s}
}`

const approvedPlanContentJSON = `{"behavior":"Add Greeting.","in_scope":["REQUIRED DEVIATION: delete Greeting from main.go"],"out_of_scope":["Release"],"assumptions":[],"unanswered_questions":[],"architecture":"One file.","api_impact":"None.","data_impact":"None.","security_impact":"None.","operational_impact":"None.","preview_requirements":"None.","automated_test_strategy":"go test.","manual_test_plan":"Run it.","rollout_strategy":"Merge.","rollback_strategy":"Revert.","risks":[],"alternatives":[],"dependencies":[],"delivery_order":["Create greeting.go","Delete Greeting from main.go"]}`

func TestJobSpecDecodesApprovedPlanContent(t *testing.T) {
	var job v1.JobSpec
	raw := strings.Replace(approvedPlanBuildJob, "%s", approvedPlanContentJSON, 1)
	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		t.Fatal(err)
	}
	if job.ApprovedPlan == nil {
		t.Fatal("approved_plan was not decoded")
	}
	plan := *job.ApprovedPlan
	if plan.RevisionID != "revision-2" || plan.Revision != 2 || plan.Digest != strings.Repeat("b", 64) {
		t.Fatalf("approved plan identity = %+v", plan)
	}
	if len(plan.Content.InScope) != 1 || !strings.HasPrefix(plan.Content.InScope[0], "REQUIRED DEVIATION") ||
		len(plan.Content.DeliveryOrder) != 2 || plan.Content.OutOfScope[0] != "Release" {
		t.Fatalf("approved plan content = %+v", plan.Content)
	}
	if err := validation.JobSpec(job); err != nil {
		t.Fatalf("valid approved plan job rejected: %v", err)
	}
	encoded, err := json.Marshal(job)
	if err != nil || !strings.Contains(string(encoded), `"approved_plan":{"revision_id":"revision-2","revision":2,`) {
		t.Fatalf("approved plan wire = %s, %v", encoded, err)
	}
}

func TestJobSpecRefusesUnknownApprovedPlanFields(t *testing.T) {
	cases := map[string]string{
		"approved plan field": strings.Replace(
			strings.Replace(approvedPlanBuildJob, "%s", approvedPlanContentJSON, 1),
			`"revision_id":"revision-2"`, `"revision_id":"revision-2","approved":true`, 1),
		"plan content field": strings.Replace(approvedPlanBuildJob, "%s",
			strings.Replace(approvedPlanContentJSON, `"behavior"`, `"capabilities":["merge"],"behavior"`, 1), 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			var job v1.JobSpec
			if err := json.Unmarshal([]byte(raw), &job); err == nil {
				t.Fatal("unknown approved_plan field was accepted")
			}
		})
	}
}

func TestControlPlaneBuildCommandFixtureCarriesAValidApprovedPlan(t *testing.T) {
	var command workIntakeProposalCommandFixture
	decodeStrictFixture(t, "testdata/start_job_build_v1_command.json", &command)
	job := command.Payload.Job
	if command.Payload.Command != v1.CmdStartJob || job == nil || job.Kind != v1.JobKindBuild || job.ApprovedPlan == nil {
		t.Fatalf("build command fixture = %+v", command.Payload)
	}
	if job.ApprovedPlan.Revision != job.ApprovedPlanRevision || job.ApprovedPlan.RevisionID != "plan-revision-0002" ||
		len(job.ApprovedPlan.Content.InScope) != 2 || len(job.ApprovedPlan.Content.DeliveryOrder) != 3 {
		t.Fatalf("approved plan = %+v", job.ApprovedPlan)
	}
	if err := validation.JobSpec(*job); err != nil {
		t.Fatalf("control-plane build fixture rejected: %v", err)
	}
}

func TestJobSpecOmitsAbsentApprovedPlan(t *testing.T) {
	encoded, err := json.Marshal(v1.JobSpec{Kind: v1.JobKindPlan})
	if err != nil || strings.Contains(string(encoded), `"approved_plan"`) {
		t.Fatalf("plan job wire = %s, %v", encoded, err)
	}
}
