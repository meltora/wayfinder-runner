package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/supervisor"
	"github.com/example/app-runner/internal/validation"
)

var (
	errJobStartCancelled        = errors.New("job was cancelled before start evidence")
	errJobStartLeaseUnavailable = errors.New("job start lease authority is unavailable")
)

func jobStartedEventID(job v1.JobSpec) string {
	authority := v1.AgentRunAuthority{}
	if job.AgentRunAuthority != nil {
		authority = *job.AgentRunAuthority
	}
	material := strings.Join([]string{
		string(job.Kind),
		job.LeaseID,
		job.JobID,
		job.ProjectID,
		job.AttemptID,
		job.ManifestID,
		job.ManifestDigest,
		strconv.Itoa(job.WorkflowRevision),
		strconv.FormatUint(job.FencingToken, 10),
		strconv.Itoa(authority.SchemaVersion),
		authority.AgentRunID,
		authority.StepRunID,
		strconv.FormatUint(authority.StepSequence, 10),
	}, "\x00")
	digest := sha256.Sum256([]byte(material))
	return "job-started-" + hex.EncodeToString(digest[:])
}

func jobStartedEvent(job v1.JobSpec, observedAt time.Time) (v1.JobStartedEvent, error) {
	if job.AgentRunAuthority == nil {
		return v1.JobStartedEvent{}, fmt.Errorf("job_started requires AgentRun authority")
	}
	event := v1.JobStartedEvent{
		Kind: "job_started", ManifestKind: job.Kind,
		LeaseID: job.LeaseID, JobID: job.JobID, AttemptID: job.AttemptID,
		ManifestID: job.ManifestID, ManifestDigest: job.ManifestDigest,
		FencingToken: job.FencingToken, ObservedAt: observedAt.UTC(),
		AgentRunAuthority: *job.AgentRunAuthority,
	}
	if err := validation.JobStartedEvent(event); err != nil {
		return v1.JobStartedEvent{}, err
	}
	return event, nil
}

func terminateStartedProcess(proc supervisor.Process, cause error) error {
	if err := proc.Kill(); err != nil {
		return errors.Join(cause, fmt.Errorf("terminate process without start evidence: %w", err))
	}
	return cause
}

// finalizeProviderStart is called while procMu is held. This prevents a
// concurrent cancellation from crossing the final authority check and durable
// journal write. Agent-facing evidence stays gated until the write succeeds.
func (jr *jobRunner) finalizeProviderStart(ctx context.Context, proc supervisor.Process) error {
	if jr.job.AgentRunAuthority == nil {
		return nil
	}
	if ctx.Err() != nil || jr.cancelled {
		return terminateStartedProcess(proc, errJobStartCancelled)
	}
	if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
		return terminateStartedProcess(
			proc,
			fmt.Errorf("%w: %v", errJobStartLeaseUnavailable, err),
		)
	}
	event, err := jobStartedEvent(jr.job, time.Now())
	if err != nil {
		return terminateStartedProcess(proc, fmt.Errorf("build job_started event: %w", err))
	}
	if _, err := jr.d.sendEventWithStableID(ctx, jobStartedEventID(jr.job), event); err != nil {
		return terminateStartedProcess(proc, fmt.Errorf("durably record job_started: %w", err))
	}
	jr.startEvidenceOnce.Do(func() {
		if jr.startEvidenceReady != nil {
			close(jr.startEvidenceReady)
		}
	})
	return nil
}

func (jr *jobRunner) awaitStartEvidence(ctx context.Context) error {
	if jr.startEvidenceRecorded() {
		return nil
	}
	select {
	case <-jr.startEvidenceReady:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (jr *jobRunner) startEvidenceRecorded() bool {
	if jr.job.AgentRunAuthority == nil || jr.startEvidenceReady == nil {
		return true
	}
	select {
	case <-jr.startEvidenceReady:
		return true
	default:
		return false
	}
}
