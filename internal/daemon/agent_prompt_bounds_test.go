package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func assertNoPromptFiles(t *testing.T, stateDir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(stateDir, "supervisor", "prompts"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("prompt files left behind: %v", entries)
	}
}

func assertPromptRefusal(t *testing.T, finished map[string]any) {
	t.Helper()
	if finished["state"] != string(v1.JobFinishedFailed) || str(finished, "failure_code") != string(v1.JobFailureAgentStart) ||
		str(finished, "failure_message") != agentPromptTooLargeMessage {
		t.Fatalf("job_finished = %v, want the prompt size refusal", finished)
	}
}

func TestAgentInputLimitsAcceptTheMaximumAndRefuseOneByteMore(t *testing.T) {
	overLongCheck := maximumRequiredChecks()
	overLongCheck[0] += "c"
	for name, test := range map[string]struct {
		atLimit, overLimit func(*jobRunner)
	}{
		"summary": {
			atLimit:   func(jr *jobRunner) { jr.job.Ticket.Summary = sentinelText("SUM", 1<<10) },
			overLimit: func(jr *jobRunner) { jr.job.Ticket.Summary = sentinelText("SUM", 1<<10+1) },
		},
		"description": {
			atLimit:   func(jr *jobRunner) { jr.job.Ticket.Description = sentinelText("DESC", 256<<10) },
			overLimit: func(jr *jobRunner) { jr.job.Ticket.Description = sentinelText("DESC", 256<<10+1) },
		},
		"acceptance criteria": {
			atLimit:   func(jr *jobRunner) { jr.job.Ticket.AcceptanceCriteria = sentinelText("CRIT", 128<<10) },
			overLimit: func(jr *jobRunner) { jr.job.Ticket.AcceptanceCriteria = sentinelText("CRIT", 128<<10+1) },
		},
		"review comment": {
			atLimit:   func(jr *jobRunner) { jr.job.ReviewComment = sentinelText("REVIEW", 128<<10) },
			overLimit: func(jr *jobRunner) { jr.job.ReviewComment = sentinelText("REVIEW", 128<<10+1) },
		},
		"required check count": {
			atLimit:   func(jr *jobRunner) { jr.job.RequiredChecks = maximumRequiredChecks() },
			overLimit: func(jr *jobRunner) { jr.job.RequiredChecks = append(maximumRequiredChecks(), "check-64") },
		},
		"required check length": {
			atLimit:   func(jr *jobRunner) { jr.job.RequiredChecks = maximumRequiredChecks() },
			overLimit: func(jr *jobRunner) { jr.job.RequiredChecks = overLongCheck },
		},
		"continuation content": {
			atLimit: func(jr *jobRunner) { jr.reviewFeedbackContinuation = maximumHumanChangeRequest() },
			overLimit: func(jr *jobRunner) {
				jr.reviewFeedbackContinuation = maximumHumanChangeRequest()
				jr.reviewFeedbackContinuation.Feedback.Content += "<"
			},
		},
	} {
		t.Run(name+" at the limit", func(t *testing.T) {
			run := newCompositionRun(t, false)
			run.jr.job.Kind = v1.JobKindBuild
			test.atLimit(run.jr)
			run.execute(t)
			if run.supervisor.starts != 1 {
				t.Fatalf("provider starts = %d, finished %v", run.supervisor.starts, run.finished(t))
			}
		})
		t.Run(name+" one over the limit", func(t *testing.T) {
			run := newPromptDeliveryRun(t, v1.JobKindBuild)
			test.overLimit(run.jr)
			run.execute(t)
			assertPromptRefusal(t, run.finished(t))
			if len(run.sup.specs) != 0 {
				t.Fatalf("provider started %d times", len(run.sup.specs))
			}
			if progress := run.events.byKind("progress"); len(progress) != 0 {
				t.Fatalf("workspace preparation began before the refusal: %v", progress)
			}
			if strings.Contains(run.logs.String(), "-START-") {
				t.Fatal("refusal logged input text")
			}
			assertNoPromptFiles(t, run.jr.d.cfg.StateDir)
		})
	}
}

func TestAgentPromptCeilingAcceptsOneMebibyteAndRefusesOneByteMore(t *testing.T) {
	if !agentPromptWithinLimit(strings.Repeat("p", maxAgentPromptBytes), maxAgentPromptBytes) {
		t.Fatal("a 1 MiB prompt was refused")
	}
	if agentPromptWithinLimit(strings.Repeat("p", maxAgentPromptBytes+1), maxAgentPromptBytes) {
		t.Fatal("a 1 MiB + 1 prompt was accepted")
	}
	if maxAgentPromptBytes != 1<<20 {
		t.Fatalf("maxAgentPromptBytes = %d", maxAgentPromptBytes)
	}
	run := newPromptDeliveryRun(t, v1.JobKindBuild)
	if run.jr.agentPromptByteLimit() != maxAgentPromptBytes {
		t.Fatalf("default prompt limit = %d", run.jr.agentPromptByteLimit())
	}
	prompt := run.jr.buildPrompt()
	run.jr.agentPromptLimit = len(prompt)
	run.execute(t)
	if len(run.sup.specs) != 1 {
		t.Fatalf("a prompt at the ceiling did not start: %v", run.finished(t))
	}

	refused := newPromptDeliveryRun(t, v1.JobKindBuild)
	refused.jr.agentPromptLimit = len(refused.jr.buildPrompt()) - 1
	refused.execute(t)
	assertPromptRefusal(t, refused.finished(t))
	if len(refused.sup.specs) != 0 {
		t.Fatal("an over-ceiling prompt started the provider")
	}
	for _, progress := range refused.events.byKind("progress") {
		if progress["milestone"] == "provider_process_starting" {
			t.Fatal("an over-ceiling prompt reached provider start")
		}
	}
	assertNoPromptFiles(t, refused.jr.d.cfg.StateDir)
}

func TestPromptRefusalUnderDockerNeverReachesTheIsolatorOrSupervisor(t *testing.T) {
	for name, configure := range map[string]func(*ownershipRun){
		"field limit": func(run *ownershipRun) {
			run.jr.job.Ticket.Description = sentinelText("DESC", 256<<10+1)
		},
		"prompt ceiling": func(run *ownershipRun) {
			run.jr.agentPromptLimit = 16
		},
	} {
		t.Run(name, func(t *testing.T) {
			run := newOwnershipRun(t, testRootlessMapping)
			configure(run)
			run.execute(t)
			assertPromptRefusal(t, run.finished(t))
			run.expectCalls(t, "")
			if run.spec.Command != "" {
				t.Fatalf("supervisor received spec %q", run.spec.Command)
			}
			assertNoPromptFiles(t, run.jr.d.cfg.StateDir)
		})
	}
}
