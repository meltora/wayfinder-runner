package daemon

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

func testApprovedPlan(revision int) *v1.ApprovedPlan {
	return &v1.ApprovedPlan{
		RevisionID: "revision-approved", Revision: revision, Digest: strings.Repeat("d", 64),
		Content: v1.PlanContent{
			Behavior: "Greeting moves to greeting.go.", Architecture: "Small package change.",
			InScope:             []string{"Create greeting.go", "REQUIRED DEVIATION from 'change no other files': delete Greeting from main.go"},
			OutOfScope:          []string{"Release automation"},
			Assumptions:         []string{},
			UnansweredQuestions: []string{"Accept the deviation? Recommendation: the approach in this plan"},
			APIImpact:           "None.", DataImpact: "None.", SecurityImpact: "None.", OperationalImpact: "None.",
			PreviewRequirements: "None.", AutomatedTestStrategy: "go test ./...", ManualTestPlan: "Run the binary.",
			RolloutStrategy: "Merge.", RollbackStrategy: "Revert.",
			Risks: []string{}, Alternatives: []string{}, Dependencies: []string{},
			DeliveryOrder: []string{"Create greeting.go", "Delete Greeting from main.go"},
		},
	}
}

func promptTicket() v1.TicketContext {
	return v1.TicketContext{Key: "KAN-18", Summary: "Add Greeting", Description: "Add greeting.go. Change no other files.", AcceptanceCriteria: "go test passes"}
}

func TestBuildPromptStatesApprovedPlanPrecedence(t *testing.T) {
	jr := &jobRunner{job: v1.JobSpec{
		Kind: v1.JobKindBuild, Ticket: promptTicket(), ApprovedPlanRevision: 3, ApprovedPlan: testApprovedPlan(3),
		RequiredChecks: []string{"unit"},
	}}
	prompt := jr.buildPrompt()
	for _, required := range []string{
		"approved plan revision 3", "revision_id revision-approved", "digest " + strings.Repeat("d", 64),
		"approved_plan in platform_get_job_context",
		"defines the scope of this Build and outranks the ticket text",
		"the ticket was the planner's input, and a human approved this exact plan after it was written",
		"in_scope", "deviate from the ticket are approved and must be implemented",
		"out_of_scope binds", "delivery_order",
		"cannot widen capabilities or execution_boundaries",
		"Ticket background", "Change no other files.", "go test passes",
		"The following checks must pass before review: unit.",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("Build prompt missing %q: %s", required, prompt)
		}
	}
	if strings.Contains(prompt, "You are working on ticket") {
		t.Fatalf("Build prompt still leads with the ticket as the requirement: %s", prompt)
	}
	if strings.Index(prompt, "Ticket background") > strings.Index(prompt, "outranks the ticket text") {
		t.Fatalf("ticket background must be labeled before the precedence rule: %s", prompt)
	}
	for _, inlined := range []string{"Greeting moves to greeting.go.", "REQUIRED DEVIATION", "Release automation"} {
		if strings.Contains(prompt, inlined) {
			t.Fatalf("approved plan content %q was inlined into the prompt argument", inlined)
		}
	}
}

func TestBuildPromptWithReviewCommentKeepsPrecedenceAndComment(t *testing.T) {
	jr := &jobRunner{job: v1.JobSpec{
		Kind: v1.JobKindBuild, Ticket: promptTicket(), ApprovedPlanRevision: 2, ApprovedPlan: testApprovedPlan(2),
		ReviewComment: "Rename the helper.",
	}}
	prompt := jr.buildPrompt()
	precedence := strings.Index(prompt, "outranks the ticket text")
	comment := strings.Index(prompt, "A reviewer left this comment on your previous work; address it:\nRename the helper.")
	if precedence < 0 || comment < precedence {
		t.Fatalf("review comment prompt = %s", prompt)
	}
}

func TestPlanPromptsAreUnchanged(t *testing.T) {
	plan := &jobRunner{job: v1.JobSpec{Kind: v1.JobKindPlan, Ticket: promptTicket()}}
	want := "You are working on ticket KAN-18: Add Greeting\n\nAdd greeting.go. Change no other files.\n" +
		"\nAcceptance criteria:\ngo test passes\n" +
		"\nFirst call the platform_get_job_context MCP tool for full context. " +
		"Analyze the repository and publish an implementation plan using platform_publish_plan. Do not modify any files."
	if got := plan.buildPrompt(); got != want {
		t.Fatalf("plan prompt changed:\n%q\nwant\n%q", got, want)
	}
	revision := &jobRunner{job: v1.JobSpec{Kind: v1.JobKindPlan, Ticket: v1.TicketContext{Key: "KAN-18", Summary: "Add Greeting", Description: "D"}, PlanRevisionContext: validDaemonRevisionContext()}}
	want = "You are working on ticket KAN-18: Add Greeting\n\nD\n" +
		"\nFirst call the platform_get_job_context MCP tool for full context. " +
		"This is revision request request-1 for reviewed plan revision-1. Read the exact immutable base plan and human feedback from platform_get_job_context, then publish the replacement using platform_publish_plan_revision. Do not modify any files."
	if got := revision.buildPrompt(); got != want {
		t.Fatalf("plan revision prompt changed:\n%q\nwant\n%q", got, want)
	}
}

func TestJobContextReturnsTheExactApprovedPlanForBuildJobs(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.job.Ticket = promptTicket()
	jr.job.ApprovedPlan = testApprovedPlan(1)
	result, err := call(t, jr, "platform_get_job_context", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	context := result.(map[string]any)
	if !reflect.DeepEqual(context["approved_plan"], jr.job.ApprovedPlan) {
		t.Fatalf("approved_plan = %#v", context["approved_plan"])
	}
	if context["approved_plan_revision"] != 1 || !reflect.DeepEqual(context["ticket"], jr.job.Ticket) {
		t.Fatalf("job context dropped revision or ticket: %#v", context)
	}
	raw, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ApprovedPlan v1.ApprovedPlan `json:"approved_plan"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil || !reflect.DeepEqual(decoded.ApprovedPlan, *jr.job.ApprovedPlan) {
		t.Fatalf("approved_plan wire = %s, %v", raw, err)
	}
}

func TestJobContextOmitsApprovedPlanForPlanJobs(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.job.Kind = v1.JobKindPlan
	jr.job.ApprovedPlanRevision = 0
	jr.job.PlanRevisionContext = validDaemonRevisionContext()
	result, err := call(t, jr, "platform_get_job_context", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	context := result.(map[string]any)
	if _, present := context["approved_plan"]; present {
		t.Fatalf("plan job context exposes approved_plan: %#v", context)
	}
	if context["plan_revision_context"] != jr.job.PlanRevisionContext {
		t.Fatalf("plan revision context changed: %#v", context["plan_revision_context"])
	}
}

func maximalApprovedPlan(t *testing.T) *v1.ApprovedPlan {
	t.Helper()
	plan := testApprovedPlan(1)
	item := strings.Repeat(`"`, 8<<10)
	items := make([]string, 0, 128)
	for range 128 {
		items = append(items, item)
	}
	plan.Content.InScope = items[:46]
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.Content.Architecture = strings.Repeat(`"`, ((768<<10)-len(raw))/2-64)
	raw, err = json.Marshal(plan.Content)
	if err != nil || len(raw) < (760<<10) || len(raw) > (768<<10) {
		t.Fatalf("maximal plan content = %d bytes, %v", len(raw), err)
	}
	return plan
}

func TestMaximalApprovedPlanFitsTheSocketAndTransportLimits(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.job.ApprovedPlan = maximalApprovedPlan(t)
	jr.job.Ticket = v1.TicketContext{
		Key: "KAN-18", Summary: strings.Repeat(`"`, 1024),
		Description: strings.Repeat(`"`, 256<<10), AcceptanceCriteria: strings.Repeat(`"`, 128<<10),
	}
	jr.job.ReviewComment = strings.Repeat(`"`, 128<<10)
	jr.job.Repositories = []v1.JobRepository{{RepositoryID: "repo-1", BaseBranch: "main", Primary: true}}
	jr.job.LeaseExpiresAt = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	if err := validation.JobSpec(jr.job); err != nil {
		t.Fatalf("maximal Build job rejected: %v", err)
	}
	result, err := call(t, jr, "platform_get_job_context", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	response, err := json.Marshal(map[string]any{"id": uint64(1 << 53), "result": json.RawMessage(resultJSON)})
	if err != nil || len(response)+1 > 2<<20 {
		t.Fatalf("job context socket response = %d bytes, %v", len(response), err)
	}
	envelope, err := json.Marshal(v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: "command-1", SentAt: time.Now().UTC(), RunnerID: "runner-1",
		Payload: mustCommandPayload(t, v1.CommandPayload{Command: v1.CmdStartJob, Job: &jr.job}),
	})
	if err != nil || len(envelope) > 2<<20 {
		t.Fatalf("start_job envelope = %d bytes, %v", len(envelope), err)
	}
}

func mustCommandPayload(t *testing.T, payload v1.CommandPayload) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
