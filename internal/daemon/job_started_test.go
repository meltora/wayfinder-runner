package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/lease"
	"github.com/example/app-runner/internal/supervisor"
)

func jobStartedTestJob() v1.JobSpec {
	authority := v1.AgentRunAuthority{
		SchemaVersion: 1, AgentRunID: "agent-run-1",
		StepRunID: "step-run-1", StepSequence: 2,
	}
	job := withTestAuthority(v1.JobSpec{
		JobID: "job-start", AttemptID: "attempt-start", Kind: v1.JobKindBuild,
		FencingToken: 7, AgentRunAuthority: &authority,
	})
	return job
}

func jobStartedTestRunner(
	t *testing.T,
	j *journal.Journal,
	expiresAt time.Time,
	eventSink func(context.Context, any) error,
) *jobRunner {
	t.Helper()
	job := jobStartedTestJob()
	leases := lease.NewManager()
	if err := leases.Acquire(lease.Lease{
		LeaseID: job.LeaseID, JobID: job.JobID, AttemptID: job.AttemptID,
		Kind: job.Kind, ManifestID: job.ManifestID, ManifestDigest: job.ManifestDigest,
		WorkflowRevision: job.WorkflowRevision, FencingToken: job.FencingToken,
		AcquiredAt: time.Now().Add(-time.Minute), ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatal(err)
	}
	return &jobRunner{
		d:   &Daemon{journal: j, leases: leases, eventSink: eventSink},
		job: job, logger: slog.Default(), startEvidenceReady: make(chan struct{}),
	}
}

func TestJobStartedEventIDIsFixedLengthAndBindsExactAttemptAuthority(t *testing.T) {
	job := jobStartedTestJob()
	job.AttemptID = strings.Repeat("a", 128)
	first := jobStartedEventID(job)
	if len(first) != len("job-started-")+64 {
		t.Fatalf("event ID length = %d, want %d", len(first), len("job-started-")+64)
	}
	if strings.Contains(first, job.AttemptID) {
		t.Fatal("stable event ID exposes or concatenates the opaque attempt ID")
	}
	if first != jobStartedEventID(job) {
		t.Fatal("same authority did not derive the same event ID")
	}
	tests := map[string]func(*v1.JobSpec){
		"kind":              func(value *v1.JobSpec) { value.Kind = v1.JobKindPlan },
		"lease":             func(value *v1.JobSpec) { value.LeaseID = "lease-other" },
		"job":               func(value *v1.JobSpec) { value.JobID = "job-other" },
		"project":           func(value *v1.JobSpec) { value.ProjectID = "project-other" },
		"attempt":           func(value *v1.JobSpec) { value.AttemptID = "attempt-other" },
		"manifest":          func(value *v1.JobSpec) { value.ManifestID = "manifest-other" },
		"manifest digest":   func(value *v1.JobSpec) { value.ManifestDigest = strings.Repeat("b", 64) },
		"workflow revision": func(value *v1.JobSpec) { value.WorkflowRevision++ },
		"fence":             func(value *v1.JobSpec) { value.FencingToken++ },
		"agent":             func(value *v1.JobSpec) { value.AgentRunAuthority.AgentRunID = "agent-run-other" },
		"step":              func(value *v1.JobSpec) { value.AgentRunAuthority.StepRunID = "step-run-other" },
		"sequence":          func(value *v1.JobSpec) { value.AgentRunAuthority.StepSequence++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate, err := v1.CloneJobSpec(job)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&candidate)
			if first == jobStartedEventID(candidate) {
				t.Fatalf("different %s authority derived the same event ID", name)
			}
		})
	}
}

func TestFinalizeProviderStartDurablyRecordsExactEventOnce(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	var events []v1.JobStartedEvent
	sink := func(_ context.Context, payload any) error {
		event, ok := payload.(v1.JobStartedEvent)
		if !ok {
			t.Fatalf("event payload type = %T", payload)
		}
		events = append(events, event)
		return nil
	}
	jr := jobStartedTestRunner(t, j, time.Now().Add(time.Hour), sink)
	proc := &leaseTestProcess{}
	if err := jr.finalizeProviderStart(context.Background(), proc); err != nil {
		t.Fatal(err)
	}
	if proc.killed {
		t.Fatal("successfully evidenced process was killed")
	}
	if len(events) != 1 {
		t.Fatalf("job_started events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Kind != "job_started" || event.ManifestKind != jr.job.Kind ||
		event.LeaseID != jr.job.LeaseID || event.JobID != jr.job.JobID ||
		event.AttemptID != jr.job.AttemptID || event.ManifestID != jr.job.ManifestID ||
		event.ManifestDigest != jr.job.ManifestDigest || event.FencingToken != jr.job.FencingToken ||
		event.AgentRunAuthority != *jr.job.AgentRunAuthority {
		t.Fatalf("job_started authority = %+v", event)
	}
	select {
	case <-jr.startEvidenceReady:
	default:
		t.Fatal("agent evidence gate did not open after durable start evidence")
	}

	replayed := jobStartedTestRunner(t, j, time.Now().Add(time.Hour), sink)
	if err := replayed.finalizeProviderStart(context.Background(), &leaseTestProcess{}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("replay delivered %d logical events, want exactly 1", len(events))
	}
	record, found, err := j.LoadRecord(context.Background(), jobStartedEventID(jr.job))
	if err != nil || !found {
		t.Fatalf("load stable event: found=%v err=%v", found, err)
	}
	var envelope v1.Envelope
	if err := json.Unmarshal(record.Payload, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.ID != jobStartedEventID(jr.job) || record.Kind != journal.KindEvent {
		t.Fatalf("durable record = %+v", record)
	}
}

func TestJobStartedEventRequiresValidAgentRunAuthority(t *testing.T) {
	job := jobStartedTestJob()
	job.AgentRunAuthority = nil
	if _, err := jobStartedEvent(job, time.Now()); err == nil {
		t.Fatal("job_started event accepted missing AgentRun authority")
	}
	authority := v1.AgentRunAuthority{SchemaVersion: 1, StepRunID: "step-run-1", StepSequence: 1}
	job.AgentRunAuthority = &authority
	if _, err := jobStartedEvent(job, time.Now()); err == nil {
		t.Fatal("job_started event accepted invalid AgentRun authority")
	}
}

type jobStartedKillFailureProcess struct{ leaseTestProcess }

func (p *jobStartedKillFailureProcess) Kill() error { return errors.New("kill failed") }

var _ supervisor.Process = (*jobStartedKillFailureProcess)(nil)

func TestTerminateStartedProcessPreservesEvidenceAndKillFailures(t *testing.T) {
	cause := errors.New("evidence failed")
	err := terminateStartedProcess(&jobStartedKillFailureProcess{}, cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "kill failed") {
		t.Fatalf("combined termination error = %v", err)
	}
}

func TestJobStartedReplayAfterJournalRestartDoesNotDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	deliveries := 0
	sink := func(context.Context, any) error {
		deliveries++
		return nil
	}
	first := jobStartedTestRunner(t, j, time.Now().Add(time.Hour), sink)
	if err := first.finalizeProviderStart(context.Background(), &leaseTestProcess{}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replayed := jobStartedTestRunner(t, reopened, time.Now().Add(time.Hour), sink)
	if err := replayed.finalizeProviderStart(context.Background(), &leaseTestProcess{}); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 {
		t.Fatalf("logical job_started deliveries across restart = %d, want 1", deliveries)
	}
	if found, err := reopened.HasRecord(context.Background(), jobStartedEventID(first.job)); err != nil || !found {
		t.Fatalf("stable job_started after restart: found=%v err=%v", found, err)
	}
}

func TestFinalizeProviderStartFailsClosedWhenDurableRecordFails(t *testing.T) {
	j, err := journal.Open(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	jr := jobStartedTestRunner(t, j, time.Now().Add(time.Hour), nil)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	proc := &leaseTestProcess{}
	if err := jr.finalizeProviderStart(context.Background(), proc); err == nil {
		t.Fatal("closed journal did not fail job_started recording")
	}
	if !proc.killed {
		t.Fatal("provider process survived missing durable job_started evidence")
	}
	select {
	case <-jr.startEvidenceReady:
		t.Fatal("agent evidence gate opened despite recording failure")
	default:
	}
}

func TestFinalizeProviderStartEmitsNothingWithoutCurrentStartAuthority(t *testing.T) {
	tests := map[string]struct {
		configure func(*testing.T, *jobRunner) context.Context
		wantError error
	}{
		"cancelled": {
			configure: func(_ *testing.T, _ *jobRunner) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantError: errJobStartCancelled,
		},
		"expired lease": {
			configure: func(t *testing.T, jr *jobRunner) context.Context {
				jr.d.leases = lease.NewManager()
				job := jr.job
				if err := jr.d.leases.Acquire(lease.Lease{
					LeaseID: job.LeaseID, JobID: job.JobID, AttemptID: job.AttemptID,
					Kind: job.Kind, ManifestID: job.ManifestID, ManifestDigest: job.ManifestDigest,
					WorkflowRevision: job.WorkflowRevision, FencingToken: job.FencingToken,
					AcquiredAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(-time.Minute),
				}); err != nil {
					t.Fatal(err)
				}
				return context.Background()
			},
			wantError: errJobStartLeaseUnavailable,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			j, err := journal.Open(filepath.Join(t.TempDir(), "journal.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			jr := jobStartedTestRunner(t, j, time.Now().Add(time.Hour), nil)
			ctx := test.configure(t, jr)
			proc := &leaseTestProcess{}
			err = jr.finalizeProviderStart(ctx, proc)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v, want %v", err, test.wantError)
			}
			if !proc.killed {
				t.Fatal("provider process survived unavailable start authority")
			}
			if found, err := j.HasRecord(context.Background(), jobStartedEventID(jr.job)); err != nil || found {
				t.Fatalf("job_started durable record: found=%v err=%v", found, err)
			}
		})
	}
}

func TestBoundAgentEvidenceWaitsForDurableJobStarted(t *testing.T) {
	jr := &jobRunner{job: jobStartedTestJob(), startEvidenceReady: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := jr.awaitStartEvidence(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed gate error = %v", err)
	}
	close(jr.startEvidenceReady)
	if err := jr.awaitStartEvidence(context.Background()); err != nil {
		t.Fatalf("open gate error = %v", err)
	}
	legacy := &jobRunner{job: v1.JobSpec{}}
	if err := legacy.awaitStartEvidence(context.Background()); err != nil {
		t.Fatalf("legacy evidence gate = %v", err)
	}
	boundTestDouble := &jobRunner{job: jobStartedTestJob()}
	if err := boundTestDouble.awaitStartEvidence(context.Background()); err != nil {
		t.Fatalf("bound test double without a production gate = %v", err)
	}
}
