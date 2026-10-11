package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
)

func claimTestPlanPublication(t *testing.T, jr *jobRunner, tool, eventID string, appendEvent bool) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := jr.d.journal.ClaimPlanPublication(ctx, journal.PlanPublicationClaim{
		AttemptID: jr.job.AttemptID, Tool: tool, IdempotencyKey: "publish-1", EventID: eventID,
		InputDigest: strings.Repeat("c", 64), Payload: []byte(`{"kind":"plan_published"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if appendEvent {
		if err := jr.d.journal.Append(ctx, journal.Record{
			ID: eventID, Kind: journal.KindEvent, Payload: []byte(`{"kind":"plan_published"}`), CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlanExitOutcomeRequiresDurablePlanPublication(t *testing.T) {
	notPublished := jobOutcome{
		State: v1.JobFinishedFailed, FailureCode: v1.JobFailurePlanNotPublished,
		FailureMessage: "planning finished without publishing a plan",
	}
	cases := []struct {
		name     string
		revision bool
		tool     string
		claim    bool
		event    bool
		want     jobOutcome
	}{
		{name: "initial plan without claim", want: notPublished},
		{name: "revision without claim", revision: true, want: notPublished},
		{name: "initial plan claim without event", tool: publishPlanTool, claim: true, want: notPublished},
		{name: "revision claim without event", revision: true, tool: publishPlanRevisionTool, claim: true, want: notPublished},
		{name: "initial plan published", tool: publishPlanTool, claim: true, event: true, want: jobOutcome{State: v1.JobFinishedSucceeded}},
		{name: "revision published", revision: true, tool: publishPlanRevisionTool, claim: true, event: true, want: jobOutcome{State: v1.JobFinishedSucceeded}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			jr.job.Kind = v1.JobKindPlan
			if tc.revision {
				jr.job.PlanRevisionContext = &v1.PlanRevisionContext{}
			}
			if tc.claim {
				claimTestPlanPublication(t, jr, tc.tool, "plan-event-1", tc.event)
			}
			got := jr.planExitOutcome(context.Background())
			if got.State != tc.want.State || got.FailureCode != tc.want.FailureCode ||
				got.FailureMessage != tc.want.FailureMessage || got.FailureCategory != "" || got.Retryable != nil {
				t.Fatalf("outcome = %#v, want %#v", got, tc.want)
			}
			if got.State == v1.JobFinishedFailed {
				event := jr.jobFinishedEvent(got)
				event.ManifestKind = v1.JobKindPlan
				if event.FailureCode != v1.JobFailurePlanNotPublished || event.State != v1.JobFinishedFailed {
					t.Fatalf("terminal event = %#v", event)
				}
			}
		})
	}
}

func TestPlanExitOutcomeReportsJournalReadFailureAsInternalError(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.job.Kind = v1.JobKindPlan
	if err := jr.d.journal.Close(); err != nil {
		t.Fatal(err)
	}
	got := jr.planExitOutcome(context.Background())
	if got.State != v1.JobFinishedFailed || got.FailureCode != v1.JobFailureRunnerInternal {
		t.Fatalf("outcome = %#v", got)
	}
}
