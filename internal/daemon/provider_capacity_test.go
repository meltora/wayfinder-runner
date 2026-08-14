package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/collector"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/lease"
	"github.com/example/app-runner/internal/provider"
)

func capacityTestLease(job v1.JobSpec) *lease.Manager {
	manager := lease.NewManager()
	_ = manager.Acquire(lease.Lease{
		LeaseID: job.LeaseID, JobID: job.JobID, AttemptID: job.AttemptID, FencingToken: job.FencingToken,
		Kind: job.Kind, ManifestID: job.ManifestID, ManifestDigest: job.ManifestDigest,
		WorkflowRevision: job.WorkflowRevision,
		ExpiresAt:        time.Now().Add(time.Minute),
	})
	return manager
}

func TestProviderCapacityEventUsesOnlyTrustedSingleRepositoryLineage(t *testing.T) {
	t.Parallel()
	store := collector.NewStore(t.TempDir())
	job := withTestAuthority(v1.JobSpec{
		JobID: "job-1", AttemptID: "attempt-1", Kind: v1.JobKindBuild, FencingToken: 7,
		ProviderCapacityResumePolicy: v1.CapacityResumeAutomatic,
	})
	checkpoint := collector.NewCheckpoint(job, "implement", "untrusted agent text", "resume")
	checkpoint.CreatedAt = time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	if err := store.SaveCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	jr := &jobRunner{
		d:         &Daemon{cfg: config.Config{Provider: "claude"}, collectors: store, leases: capacityTestLease(job)},
		job:       job,
		sessionID: "provider-session-1",
		repositories: []checkedOutRepository{{
			ID: "repo-1", Branch: "job/job-1/attempt-1", Primary: true,
		}},
	}

	lineage := &capacityRecoveryLineage{
		BranchRef: "job/job-1/attempt-1",
		CommitSHA: "0123456789abcdef0123456789abcdef01234567",
	}
	checkpointRef := jr.persistCapacityCheckpoint(lineage)
	event := jr.providerCapacityEvent(
		provider.CapacityFailure{Reason: provider.CapacityReasonQuotaExhausted},
		lineage,
		checkpointRef,
	)
	if event.ManifestKind != job.Kind || event.LeaseID != job.LeaseID ||
		event.ManifestID != job.ManifestID || event.ManifestDigest != job.ManifestDigest ||
		event.JobID != job.JobID || event.AttemptID != job.AttemptID ||
		event.FencingToken != job.FencingToken || event.ObservedAt.IsZero() ||
		event.ObservedAt.Location() != time.UTC {
		t.Fatalf("capacity event authority = %#v", event)
	}
	if event.BranchRef != lineage.BranchRef || event.CommitSHA != lineage.CommitSHA {
		t.Fatalf("event lineage = %#v, want %#v", event, lineage)
	}
	if event.CheckpointRef != "attempt-1" {
		t.Fatalf("checkpoint_ref = %q, want current attempt ID", event.CheckpointRef)
	}
	if event.CheckpointRef == checkpoint.BranchState {
		t.Fatal("agent-controlled branch_state became an authoritative checkpoint reference")
	}
	persisted, err := store.CheckpointForAttempt(job.JobID, job.AttemptID, job.FencingToken)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ProviderSessionID != "provider-session-1" {
		t.Fatalf("provider session was not durably bound before emission: %#v", persisted)
	}

	legacy := jr.providerCapacityEvent(
		provider.CapacityFailure{Reason: provider.CapacityReasonQuotaExhausted},
		nil,
		"",
	)
	if legacy.BranchRef != "" || legacy.CommitSHA != "" || legacy.CheckpointRef != "" {
		t.Fatalf("lineage-free event gained recovery authority: %#v", legacy)
	}
}

func TestProviderCapacityEventMatchesStrictWireContract(t *testing.T) {
	t.Parallel()
	job := withTestAuthority(v1.JobSpec{
		JobID: "job-wire", AttemptID: "attempt-wire", Kind: v1.JobKindPlan,
		FencingToken: (1 << 53) - 1,
	})
	jr := &jobRunner{
		d:   &Daemon{cfg: config.Config{Provider: "claude"}},
		job: job,
	}
	jr.providerError = strings.Repeat("€", 400)
	event := jr.providerCapacityEvent(
		provider.CapacityFailure{Reason: provider.CapacityReasonQuotaExhausted},
		nil,
		"",
	)
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "workflow_revision") {
		t.Fatalf("event leaked non-contract workflow authority: %s", raw)
	}
	var decoded map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if got := decoded["fencing_token"].(json.Number).String(); got != "9007199254740991" {
		t.Fatalf("fencing token = %s", got)
	}
	for _, field := range []string{"manifest_kind", "lease_id", "job_id", "attempt_id", "manifest_id", "manifest_digest", "observed_at"} {
		if decoded[field] == nil || decoded[field] == "" {
			t.Fatalf("strict field %s missing from %s", field, raw)
		}
	}
	if !utf8.ValidString(event.Message) || len(event.Message) > 512 {
		t.Fatalf("provider message is not a bounded UTF-8 string: bytes=%d", len(event.Message))
	}
}

func TestCapacityCheckpointCanBeCreatedAfterProviderExit(t *testing.T) {
	t.Parallel()
	store := collector.NewStore(t.TempDir())
	job := withTestAuthority(v1.JobSpec{JobID: "job-1", AttemptID: "attempt-1", Kind: v1.JobKindBuild, FencingToken: 11})
	jr := &jobRunner{
		d:         &Daemon{cfg: config.Config{Provider: "codex"}, collectors: store, leases: capacityTestLease(job)},
		job:       job,
		sessionID: "session-after-exit",
	}
	lineage := &capacityRecoveryLineage{
		BranchRef: "job/job-1/attempt-1",
		CommitSHA: "0123456789abcdef0123456789abcdef01234567",
	}
	if ref := jr.persistCapacityCheckpoint(lineage); ref != job.AttemptID {
		t.Fatalf("checkpoint ref = %q", ref)
	}
	checkpoint, err := store.CheckpointForAttempt(job.JobID, job.AttemptID, job.FencingToken)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.ProviderSessionID != jr.sessionID || checkpoint.BranchState != lineage.BranchRef+"@"+lineage.CommitSHA {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
}

func TestProviderCapacityEventDefaultsAndOptionalCheckpointPaths(t *testing.T) {
	t.Parallel()
	store := collector.NewStore(t.TempDir())
	job := withTestAuthority(v1.JobSpec{JobID: "job-1", AttemptID: "attempt-1", Kind: v1.JobKindBuild, FencingToken: 13})
	jr := &jobRunner{
		d:         &Daemon{cfg: config.Config{Provider: "opencode", AgentModel: "openai/model"}, collectors: store, leases: capacityTestLease(job)},
		job:       job,
		sessionID: "provider-session",
	}
	retryAt := time.Date(2026, time.August, 14, 8, 0, 0, 0, time.FixedZone("test", 3600))
	event := jr.providerCapacityEvent(provider.CapacityFailure{
		Reason:  provider.CapacityReasonQuotaExhausted,
		RetryAt: retryAt,
	}, nil, "")
	if event.ResumePolicy != v1.CapacityResumeManual || event.Provider != "openai" {
		t.Fatalf("default event = %#v", event)
	}
	if event.RetryAt == nil || !event.RetryAt.Equal(retryAt) || event.RetryAt.Location() != time.UTC {
		t.Fatalf("retry_at was not normalized to UTC: %#v", event.RetryAt)
	}

	lineage := &capacityRecoveryLineage{
		BranchRef: "job/job-1/attempt-1",
		CommitSHA: "0123456789abcdef0123456789abcdef01234567",
	}
	jr.sessionID = ""
	if ref := jr.persistCapacityCheckpoint(lineage); ref != "" {
		t.Fatalf("missing checkpoint and session produced ref %q", ref)
	}
	checkpoint := collector.NewCheckpoint(job, "existing", "trusted", "resume")
	if err := store.SaveCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	if ref := jr.persistCapacityCheckpoint(lineage); ref != job.AttemptID {
		t.Fatalf("existing exact checkpoint ref = %q", ref)
	}
	persisted, err := store.CheckpointForAttempt(job.JobID, job.AttemptID, job.FencingToken)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.PlanStep != checkpoint.PlanStep || persisted.BranchState != lineage.BranchRef+"@"+lineage.CommitSHA {
		t.Fatalf("existing checkpoint was not bound to trusted Git lineage: %#v", persisted)
	}
}

func TestCapacityRecoveryLineageRejectsIncompleteAndMultiRepositoryBuilds(t *testing.T) {
	t.Parallel()
	jr := &jobRunner{
		job: v1.JobSpec{Kind: v1.JobKindBuild, JobID: "job-1", AttemptID: "attempt-1"},
		repositories: []checkedOutRepository{
			{ID: "repo-1", Primary: true},
			{ID: "repo-2"},
		},
	}
	if got := jr.capacityRecoveryLineage(map[string]any{
		"branch": "job/job-1/attempt-1", "head_sha": "0123456789abcdef0123456789abcdef01234567", "pushed": true,
	}); got != nil {
		t.Fatalf("multi-repository lineage must fail closed: %#v", got)
	}

	jr.repositories = jr.repositories[:1]
	for name, extra := range map[string]map[string]any{
		"not pushed":    {"branch": "job/job-1/attempt-1", "head_sha": "0123456789abcdef0123456789abcdef01234567"},
		"missing sha":   {"branch": "job/job-1/attempt-1", "pushed": true},
		"unsafe branch": {"branch": "../main", "head_sha": "0123456789abcdef0123456789abcdef01234567", "pushed": true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := jr.capacityRecoveryLineage(extra); got != nil {
				t.Fatalf("invalid finalized build gained recovery authority: %#v", got)
			}
		})
	}
}

func TestCapacityCheckpointRequiresCurrentLeaseFence(t *testing.T) {
	t.Parallel()
	store := collector.NewStore(t.TempDir())
	job := v1.JobSpec{JobID: "job-1", AttemptID: "attempt-1", FencingToken: 17}
	jr := &jobRunner{
		d:         &Daemon{cfg: config.Config{Provider: "claude"}, collectors: store, leases: lease.NewManager()},
		job:       job,
		sessionID: "provider-session",
	}
	lineage := &capacityRecoveryLineage{
		BranchRef: "job/job-1/attempt-1",
		CommitSHA: "0123456789abcdef0123456789abcdef01234567",
	}
	if ref := jr.persistCapacityCheckpoint(lineage); ref != "" {
		t.Fatalf("checkpoint persisted without a current lease: %q", ref)
	}
	if _, err := store.CheckpointForAttempt(job.JobID, job.AttemptID, job.FencingToken); err == nil {
		t.Fatal("stale runner wrote a recovery checkpoint")
	}
}
