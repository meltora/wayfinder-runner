package daemon

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestJobFinishedEventBindsTrustedJobAuthorityAndBoundsDetail(t *testing.T) {
	t.Parallel()
	job := withTestAuthority(v1.JobSpec{
		JobID: "job-1", AttemptID: "attempt-1", Kind: v1.JobKindBuild, FencingToken: 42,
	})
	jr := &jobRunner{job: job, sessionID: "session-1"}
	event := jr.jobFinishedEvent(jobOutcome{
		State: v1.JobFinishedFailed, FailureCode: v1.JobFailureAgentExit,
		FailureMessage: strings.Repeat("€", 400),
	})
	if event.ManifestKind != job.Kind || event.LeaseID != job.LeaseID || event.JobID != job.JobID ||
		event.AttemptID != job.AttemptID || event.ManifestID != job.ManifestID ||
		event.ManifestDigest != job.ManifestDigest || event.FencingToken != job.FencingToken {
		t.Fatalf("terminal authority = %#v", event)
	}
	if event.ProviderSessionID != "session-1" || event.ObservedAt.IsZero() || event.ObservedAt.Location() != time.UTC {
		t.Fatalf("terminal evidence = %#v", event)
	}
	if len(event.FailureMessage) > 512 {
		t.Fatalf("failure message bytes = %d", len(event.FailureMessage))
	}
}

func TestRecordCompletionEvidenceFailsClosedBeforeQueueingInvalidOutcome(t *testing.T) {
	t.Parallel()
	called := false
	job := withTestAuthority(v1.JobSpec{
		JobID: "job-invalid", AttemptID: "attempt-invalid", Kind: v1.JobKindPlan, FencingToken: 3,
	})
	d := &Daemon{eventSink: func(context.Context, any) error {
		called = true
		return nil
	}}
	jr := &jobRunner{d: d, job: job, logger: slog.Default()}
	recorded := jr.recordCompletionEvidence(context.Background(), jobOutcome{
		State: v1.JobFinishedSucceeded, FailureCode: v1.JobFailureAgentExit,
	}, nil)
	if recorded || called || !d.recoveredLease.Load() {
		t.Fatalf("invalid terminal recorded=%v called=%v recovered=%v", recorded, called, d.recoveredLease.Load())
	}
}

func TestRecordCompletionEvidenceQueuesTypedTerminal(t *testing.T) {
	t.Parallel()
	job := withTestAuthority(v1.JobSpec{
		JobID: "job-valid", AttemptID: "attempt-valid", Kind: v1.JobKindPlan, FencingToken: 5,
	})
	var captured any
	d := &Daemon{eventSink: func(_ context.Context, event any) error {
		captured = event
		return nil
	}}
	jr := &jobRunner{d: d, job: job, logger: slog.Default()}
	if !jr.recordCompletionEvidence(context.Background(), jobOutcome{State: v1.JobFinishedSucceeded}, nil) {
		t.Fatal("valid terminal was not recorded")
	}
	terminal, ok := captured.(v1.JobFinishedEvent)
	if !ok || terminal.ManifestID != job.ManifestID || terminal.State != v1.JobFinishedSucceeded {
		t.Fatalf("captured event = %#v", captured)
	}
}
