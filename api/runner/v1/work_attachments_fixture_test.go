package v1_test

import (
	"encoding/json"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

func TestControlPlanePlanCommandFixtureCarriesValidWorkAttachments(t *testing.T) {
	var command workIntakeProposalCommandFixture
	decodeStrictFixture(t, "testdata/start_job_plan_work_attachments_v1_command.json", &command)
	job := command.Payload.Job
	if command.Payload.Command != v1.CmdStartJob || job == nil || len(job.WorkAttachments) != 2 {
		t.Fatalf("work attachment command fixture = %+v", command.Payload)
	}
	first := job.WorkAttachments[0]
	if first.Name != "design-notes.md" || first.MediaType != "text/markdown" || first.SizeBytes != 42 ||
		first.Trust != v1.WorkAttachmentTrustUntrustedUser || first.Usage != v1.WorkAttachmentUsageContextOnly {
		t.Fatalf("first attachment = %+v", first)
	}
	if err := validation.JobSpec(*job); err != nil {
		t.Fatalf("control-plane work attachment fixture rejected: %v", err)
	}
}

func TestJobSpecOmitsAbsentWorkAttachments(t *testing.T) {
	encoded, err := json.Marshal(v1.JobSpec{Kind: v1.JobKindPlan})
	if err != nil || strings.Contains(string(encoded), `"work_attachments"`) {
		t.Fatalf("plan job wire = %s, %v", encoded, err)
	}
}

func TestJobSpecRejectsUnknownWorkAttachmentFields(t *testing.T) {
	var job v1.JobSpec
	raw := `{"job_id":"job-1","work_attachments":[{"attachment_id":"00000000-0000-4000-8000-000000000001","url":"https://example.test/a"}]}`
	if err := json.Unmarshal([]byte(raw), &job); err == nil {
		t.Fatal("unknown work attachment field was accepted")
	}
}
