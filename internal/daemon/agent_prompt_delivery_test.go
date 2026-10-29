package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/supervisor"
)

type recordingDirectSupervisor struct {
	inner   *supervisor.DirectProcess
	mu      sync.Mutex
	specs   []supervisor.Spec
	onStart func(supervisor.Spec)
}

func (*recordingDirectSupervisor) Name() string    { return "direct" }
func (*recordingDirectSupervisor) Available() bool { return true }

func (s *recordingDirectSupervisor) Start(ctx context.Context, spec supervisor.Spec) (supervisor.Process, error) {
	s.mu.Lock()
	s.specs = append(s.specs, spec)
	s.mu.Unlock()
	if s.onStart != nil {
		s.onStart(spec)
	}
	return s.inner.Start(ctx, spec)
}

func (s *recordingDirectSupervisor) started(t *testing.T) supervisor.Spec {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.specs) != 1 {
		t.Fatalf("provider starts = %d, want 1", len(s.specs))
	}
	return s.specs[0]
}

type promptDeliveryRun struct {
	*compositionRun
	sup      *recordingDirectSupervisor
	received string
	logs     *bytes.Buffer
}

func newPromptDeliveryRun(t *testing.T, kind v1.JobKind) *promptDeliveryRun {
	t.Helper()
	run := &promptDeliveryRun{compositionRun: newCompositionRun(t, false), logs: &bytes.Buffer{}}
	run.jr.job.Kind = kind
	dir := t.TempDir()
	run.received = filepath.Join(dir, "received")
	script := filepath.Join(dir, "claude")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat > '"+run.received+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	run.jr.d.cfg.ProviderBinaries = map[string]string{"claude": script}
	run.sup = &recordingDirectSupervisor{inner: supervisor.NewDirectProcess()}
	run.jr.d.supervisor = run.sup
	logger := slog.New(slog.NewTextHandler(run.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	run.jr.logger = logger
	run.jr.d.logger = logger
	return run
}

func sentinelText(name string, size int) string {
	start, end := name+"-START-", "-"+name+"-END"
	return start + strings.Repeat("x", size-len(start)-len(end)) + end
}

func maximumRequiredChecks() []string {
	checks := make([]string, 0, 64)
	for index := range 64 {
		prefix := fmt.Sprintf("check-%02d-", index)
		checks = append(checks, prefix+strings.Repeat("c", 128-len(prefix)))
	}
	return checks
}

func maximumHumanChangeRequest() *v1.ReviewFeedbackContinuationAuthority {
	content := "CONTENT-START" + strings.Repeat("<", v1.ReviewFeedbackContinuationMaxContentBytes-len("CONTENT-START")-len("CONTENT-END")) + "CONTENT-END"
	sum := sha256.Sum256([]byte(content))
	return &v1.ReviewFeedbackContinuationAuthority{
		ContentSource: v1.HumanChangeRequestContentSource, ExternalRequestID: "42", RepositoryID: "repo-other",
		ExpectedHeadSHA: strings.Repeat("e", 40), ContentDigest: fmt.Sprintf("%x", sum), ContentByteCount: uint64(len(content)),
		Feedback: v1.ReviewFeedbackContinuationFeedback{Content: content},
	}
}

func TestProductMaximumPromptsReachTheProviderOnStdinByteForByte(t *testing.T) {
	for name, test := range map[string]struct {
		kind      v1.JobKind
		configure func(*jobRunner)
		sentinels []string
	}{
		"build with a 256 KiB description": {
			kind:      v1.JobKindBuild,
			configure: func(jr *jobRunner) { jr.job.Ticket.Description = sentinelText("DESC", 256<<10) },
			sentinels: []string{"DESC-START-", "-DESC-END"},
		},
		"plan with a 256 KiB description and 128 KiB criteria": {
			kind: v1.JobKindPlan,
			configure: func(jr *jobRunner) {
				jr.job.Ticket.Description = sentinelText("DESC", 256<<10)
				jr.job.Ticket.AcceptanceCriteria = sentinelText("CRIT", 128<<10)
			},
			sentinels: []string{"DESC-START-", "-DESC-END", "CRIT-START-", "-CRIT-END"},
		},
		"build human change request at every maximum": {
			kind: v1.JobKindBuild,
			configure: func(jr *jobRunner) {
				jr.job.Ticket.Summary = sentinelText("SUM", 1<<10)
				jr.job.Ticket.Description = sentinelText("DESC", 256<<10)
				jr.job.Ticket.AcceptanceCriteria = sentinelText("CRIT", 128<<10)
				jr.job.ReviewComment = sentinelText("REVIEW", 128<<10)
				jr.job.RequiredChecks = maximumRequiredChecks()
				jr.reviewFeedbackContinuation = maximumHumanChangeRequest()
			},
			sentinels: []string{
				"SUM-START-", "-SUM-END", "DESC-START-", "-DESC-END", "CRIT-START-", "-CRIT-END",
				"REVIEW-START-", "-REVIEW-END", "check-00-", "check-63-", "CONTENT-START", "CONTENT-END",
			},
		},
		"minimal summary-only prompt": {
			kind:      v1.JobKindPlan,
			configure: func(jr *jobRunner) { jr.job.Ticket = v1.TicketContext{Key: "PROJ-1", Summary: "S"} },
		},
	} {
		t.Run(name, func(t *testing.T) {
			run := newPromptDeliveryRun(t, test.kind)
			test.configure(run.jr)
			run.execute(t)
			if code := str(run.finished(t), "failure_code"); code == string(v1.JobFailureAgentStart) {
				t.Fatalf("provider did not start: %v", run.finished(t))
			}
			received, err := os.ReadFile(run.received)
			if err != nil {
				t.Fatalf("provider stdin was not captured: %v", err)
			}
			want := run.jr.buildPrompt()
			if len(received) != len(want) || sha256.Sum256(received) != sha256.Sum256([]byte(want)) {
				t.Fatalf("provider received %d bytes, want the %d-byte prompt", len(received), len(want))
			}
			for _, sentinel := range test.sentinels {
				if !bytes.Contains(received, []byte(sentinel)) {
					t.Fatalf("provider stdin lacks %q", sentinel)
				}
			}
			spec := run.sup.started(t)
			if spec.Stdin != nil || spec.StdinFile == "" {
				t.Fatalf("provider stdin = reader %v file %q, want only a runner-private file", spec.Stdin, spec.StdinFile)
			}
			for _, arg := range append(append([]string{spec.Command}, spec.Args...), append(spec.Env, spec.ProviderEnvironment...)...) {
				if len(want) > 64 && strings.Contains(arg, want[:64]) {
					t.Fatalf("argv or environment carries prompt bytes: %.80q", arg)
				}
				for _, sentinel := range test.sentinels {
					if strings.Contains(arg, sentinel) {
						t.Fatalf("argv or environment carries %q", sentinel)
					}
				}
			}
		})
	}
}

func TestPromptBytesStayOutOfLogsEventsAndCommandLines(t *testing.T) {
	const sentinel = "FR08-SECRET-SENTINEL-0123456789A"
	run := newPromptDeliveryRun(t, v1.JobKindBuild)
	run.jr.job.Ticket.Description = sentinel + sentinelText("DESC", 256<<10-2*len(sentinel)) + sentinel
	run.execute(t)
	received, err := os.ReadFile(run.received)
	if err != nil || !bytes.Contains(received, []byte(sentinel)) {
		t.Fatalf("provider did not receive the sentinel: %v", err)
	}
	events, err := json.Marshal(run.events.events)
	if err != nil {
		t.Fatal(err)
	}
	spec := run.sup.started(t)
	surfaces := map[string]string{
		"logs":    run.logs.String(),
		"events":  string(events),
		"command": strings.Join(append([]string{spec.Command}, spec.Args...), "\x00"),
		"env":     strings.Join(append(append([]string(nil), spec.Env...), spec.ProviderEnvironment...), "\x00"),
	}
	for surface, text := range surfaces {
		if strings.Contains(text, sentinel) {
			t.Fatalf("%s carries the prompt sentinel", surface)
		}
	}
	for _, surface := range []string{"logs", "events"} {
		if strings.Contains(surfaces[surface], filepath.Join("supervisor", "prompts")) {
			t.Fatalf("%s reveal the prompt file path", surface)
		}
	}
}
