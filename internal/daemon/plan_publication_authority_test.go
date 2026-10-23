package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func planAgentRunAuthority(agentRunID string) v1.AgentRunAuthority {
	return v1.AgentRunAuthority{
		SchemaVersion: 1,
		AgentRunID:    agentRunID,
		StepRunID:     "step-run-1",
		StepSequence:  1,
	}
}

func configurePlanPublicationRepositories(t *testing.T, jr *jobRunner) []v1.JobRepository {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "runner@example.test"},
		{"config", "user.name", "Runner Test"},
	} {
		command := exec.Command("git", args...)
		command.Dir = dir
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("observed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "observed"}} {
		command := exec.Command("git", args...)
		command.Dir = dir
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	baseSHA, err := gitOutput(context.Background(), dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	repositories := []v1.JobRepository{
		{
			RepositoryID: "repository-1", RepositoryHeadObservationID: "head-observation-1",
			BaseBranch: "main", BaseSHA: baseSHA, Primary: true,
		},
		{
			RepositoryID: "repository-2", RepositoryHeadObservationID: "head-observation-2",
			BaseBranch: "release", BaseSHA: baseSHA,
		},
	}
	jr.job.Kind = v1.JobKindPlan
	jr.job.Repositories = append([]v1.JobRepository(nil), repositories...)
	jr.verifiedPlanRepositories = append([]v1.JobRepository(nil), repositories...)
	baseline, err := planCheckoutBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	jr.repositories = []checkedOutRepository{
		{ID: "repository-1", Base: "main", WorkDir: dir, Primary: true, PlanBaseline: baseline},
		{ID: "repository-2", Base: "release", WorkDir: dir, PlanBaseline: baseline},
	}
	return repositories
}

func TestPlanPublicationCopiesBoundAgentRunAuthority(t *testing.T) {
	for _, test := range []struct {
		name           string
		tool           string
		baseRevisionID string
		revision       bool
	}{
		{name: "initial", tool: publishPlanTool},
		{name: "revision", tool: publishPlanRevisionTool, baseRevisionID: "revision-1", revision: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			jr, captured := testJobRunner(t)
			repositories := configurePlanPublicationRepositories(t, jr)
			if test.revision {
				jr.job.PlanRevisionContext = validDaemonRevisionContext()
			}
			authority := planAgentRunAuthority("agent-run-1")
			jr.job.AgentRunAuthority = &authority

			if _, err := call(t, jr, test.tool, planPublicationArgs(t, "plan-authority", "Bound plan", "Behavior", test.baseRevisionID)); err != nil {
				t.Fatal(err)
			}
			events := captured.byKind("plan_published")
			if len(events) != 1 || events[0]["agent_run_authority"] != authority {
				t.Fatalf("plan authority = %#v", events)
			}
			got, ok := events[0]["repositories"].([]v1.PlanRepositoryAuthority)
			if !ok || !planRepositoryAuthorityMatches(got, repositories) {
				t.Fatalf("plan repositories = %#v", events[0]["repositories"])
			}
		})
	}
}

func TestLegacyUnboundPlanPublicationOmitsAgentRunAuthority(t *testing.T) {
	jr, captured := testJobRunner(t)
	configurePlanPublicationRepositories(t, jr)

	if _, err := call(t, jr, publishPlanTool, planPublicationArgs(t, "legacy-plan", "Legacy plan", "Behavior", "")); err != nil {
		t.Fatal(err)
	}
	events := captured.byKind("plan_published")
	if len(events) != 1 {
		t.Fatalf("plan events = %d", len(events))
	}
	if _, exists := events[0]["agent_run_authority"]; exists {
		t.Fatalf("legacy plan invented AgentRun authority: %#v", events[0])
	}
}

func TestBoundPlanPublicationRecoveryRejectsMissingOrChangedAuthority(t *testing.T) {
	for _, test := range []struct {
		name      string
		stored    *v1.AgentRunAuthority
		current   v1.AgentRunAuthority
		errorText string
	}{
		{
			name:      "missing durable authority",
			current:   planAgentRunAuthority("agent-run-1"),
			errorText: "agent run authority",
		},
		{
			name:      "changed durable authority",
			stored:    authorityPointer(planAgentRunAuthority("agent-run-old")),
			current:   planAgentRunAuthority("agent-run-new"),
			errorText: "agent run authority",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			jr, captured := testJobRunner(t)
			configurePlanPublicationRepositories(t, jr)
			jr.job.AgentRunAuthority = test.stored
			args := planPublicationArgs(t, "plan-recovery", "Recovered plan", "Behavior", "")
			raw := map[string]any{}
			if err := json.Unmarshal([]byte(args), &raw); err != nil {
				t.Fatal(err)
			}
			claim, err := jr.preparePlanPublicationClaim(context.Background(), publishPlanTool, "plan-recovery", raw)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := jr.d.journal.ClaimPlanPublication(context.Background(), claim); err != nil {
				t.Fatal(err)
			}

			jr.job.AgentRunAuthority = &test.current
			if _, _, err := jr.handlePlanPublication(context.Background(), publishPlanTool, "plan-recovery", raw); err == nil || !strings.Contains(strings.ToLower(err.Error()), test.errorText) {
				t.Fatalf("recovery error = %v", err)
			}
			if events := captured.byKind("plan_published"); len(events) != 0 {
				t.Fatalf("rejected recovery emitted %d plan events", len(events))
			}
		})
	}
}

func TestPlanPublicationRejectsCheckoutMismatch(t *testing.T) {
	jr, captured := testJobRunner(t)
	configurePlanPublicationRepositories(t, jr)
	dir := jr.repositories[0].WorkDir
	if err := os.WriteFile(filepath.Join(dir, "moved.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "moved.txt"}, {"commit", "-m", "changed after checkout"}} {
		command := exec.Command("git", args...)
		command.Dir = dir
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}

	if _, err := call(t, jr, publishPlanTool, planPublicationArgs(t, "checkout-mismatch", "Plan", "Behavior", "")); err == nil || !strings.Contains(err.Error(), "checkout mismatch") {
		t.Fatalf("checkout mismatch error = %v", err)
	}
	if events := captured.byKind("plan_published"); len(events) != 0 {
		t.Fatalf("checkout mismatch emitted %d plan events", len(events))
	}
}

func TestPlanPublicationRejectsDirtyPlanningCheckout(t *testing.T) {
	jr, captured := testJobRunner(t)
	configurePlanPublicationRepositories(t, jr)
	if err := os.WriteFile(filepath.Join(jr.repositories[0].WorkDir, "untracked.txt"), []byte("injected\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := call(t, jr, publishPlanTool, planPublicationArgs(t, "dirty-checkout", "Plan", "Behavior", "")); err == nil || !strings.Contains(err.Error(), "checkout mismatch") {
		t.Fatalf("dirty checkout error = %v", err)
	}
	if events := captured.byKind("plan_published"); len(events) != 0 {
		t.Fatalf("dirty checkout emitted %d plan events", len(events))
	}
}

func TestPlanPublicationDurableReplayRejectsRepositoryAuthorityMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*planPublishedEvent)
	}{
		{name: "missing", mutate: func(event *planPublishedEvent) { event.Repositories = nil }},
		{name: "changed observation", mutate: func(event *planPublishedEvent) {
			event.Repositories[0].RepositoryHeadObservationID = "head-observation-injected"
		}},
		{name: "changed sha", mutate: func(event *planPublishedEvent) {
			event.Repositories[0].BaseSHA = strings.Repeat("f", 40)
		}},
		{name: "changed order", mutate: func(event *planPublishedEvent) {
			event.Repositories[0], event.Repositories[1] = event.Repositories[1], event.Repositories[0]
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			jr, captured := testJobRunner(t)
			configurePlanPublicationRepositories(t, jr)
			args := planPublicationArgs(t, "repository-replay", "Plan", "Behavior", "")
			raw := map[string]any{}
			if err := json.Unmarshal([]byte(args), &raw); err != nil {
				t.Fatal(err)
			}
			claim, err := jr.preparePlanPublicationClaim(context.Background(), publishPlanTool, "repository-replay", raw)
			if err != nil {
				t.Fatal(err)
			}
			var event planPublishedEvent
			if err := json.Unmarshal(claim.Payload, &event); err != nil {
				t.Fatal(err)
			}
			test.mutate(&event)
			claim.Payload, err = json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := jr.d.journal.ClaimPlanPublication(context.Background(), claim); err != nil {
				t.Fatal(err)
			}

			if _, _, err := jr.handlePlanPublication(context.Background(), publishPlanTool, "repository-replay", raw); err == nil || !strings.Contains(err.Error(), "repository authority") {
				t.Fatalf("durable replay error = %v", err)
			}
			if events := captured.byKind("plan_published"); len(events) != 0 {
				t.Fatalf("rejected replay emitted %d plan events", len(events))
			}
		})
	}
}

func TestPreparePlanPublicationRejectsInvalidAuthorityBoundaries(t *testing.T) {
	for _, test := range []struct {
		name   string
		tool   string
		key    string
		mutate func(*jobRunner, map[string]any)
	}{
		{name: "build job", tool: publishPlanTool, key: "invalid", mutate: func(jr *jobRunner, _ map[string]any) {
			jr.job.Kind = v1.JobKindBuild
		}},
		{name: "changed idempotency key", tool: publishPlanTool, key: "other"},
		{name: "blank summary", tool: publishPlanTool, key: "invalid", mutate: func(_ *jobRunner, raw map[string]any) {
			raw["change_summary"] = "  "
		}},
		{name: "invalid plan", tool: publishPlanTool, key: "invalid", mutate: func(_ *jobRunner, raw map[string]any) {
			raw["plan"] = map[string]any{}
		}},
		{name: "initial base revision", tool: publishPlanTool, key: "invalid", mutate: func(_ *jobRunner, raw map[string]any) {
			raw["base_revision_id"] = "revision-1"
		}},
		{name: "invalid revision id", tool: publishPlanRevisionTool, key: "invalid", mutate: func(jr *jobRunner, raw map[string]any) {
			jr.job.PlanRevisionContext = validDaemonRevisionContext()
			raw["base_revision_id"] = "bad/id"
		}},
		{name: "unsupported tool", tool: "platform_publish_unknown", key: "invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			configurePlanPublicationRepositories(t, jr)
			raw := map[string]any{}
			if err := json.Unmarshal([]byte(planPublicationArgs(t, "invalid", "Plan", "Behavior", "")), &raw); err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(jr, raw)
			}
			if _, err := jr.preparePlanPublicationClaim(context.Background(), test.tool, test.key, raw); err == nil {
				t.Fatal("invalid plan publication boundary was accepted")
			}
		})
	}
}

func authorityPointer(value v1.AgentRunAuthority) *v1.AgentRunAuthority {
	return &value
}
