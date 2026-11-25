package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/repo"
	"github.com/example/app-runner/internal/validation"
)

const (
	previewJobDeployCodeCapabilityMissing    = "runner_capability_missing"
	previewJobDeployCodeNetworkUnsupported   = "job_network_unsupported"
	previewJobDeployCodeLimitReached         = "deploy_limit_reached"
	previewJobDeployCodeInFlight             = "request_in_flight"
	previewJobDeployCodeCommitSetUnavailable = "commit_set_unavailable"
	previewJobDeployCodeCommitImportFailed   = "commit_import_failed"
	previewJobDeployCodeEventSendFailed      = "event_send_failed"
)

type previewJobDeployState struct {
	mu       sync.Mutex
	sequence uint64
	inFlight bool
	last     *previewJobDeployOutcome
}

func (s *previewJobDeployState) reserve() (uint64, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sequence >= v1.PreviewJobDeployMaxRequests {
		return 0, previewJobDeployCodeLimitReached
	}
	if s.inFlight {
		return 0, previewJobDeployCodeInFlight
	}
	s.inFlight = true
	return s.sequence + 1, ""
}

func (s *previewJobDeployState) consume(sequence uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence = sequence
}

func (s *previewJobDeployState) finish(outcome previewJobDeployOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight = false
	if outcome.Status == v1.PreviewJobDeployStatusDeployed {
		latest := outcome
		latest.CommitSet = copyCommitSet(outcome.CommitSet)
		s.last = &latest
	}
}

func (s *previewJobDeployState) lastDeployed() *previewJobDeployOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return nil
	}
	latest := *s.last
	latest.CommitSet = copyCommitSet(s.last.CommitSet)
	return &latest
}

func copyCommitSet(commitSet map[string]string) map[string]string {
	if commitSet == nil {
		return nil
	}
	copied := make(map[string]string, len(commitSet))
	for repositoryID, commitSHA := range commitSet {
		copied[repositoryID] = commitSHA
	}
	return copied
}

type previewDeployToolReply struct {
	Status    string            `json:"status"`
	Code      string            `json:"code,omitempty"`
	URL       string            `json:"url,omitempty"`
	ExpiresAt string            `json:"expires_at,omitempty"`
	CommitSet map[string]string `json:"commit_set,omitempty"`
}

func newPreviewDeployToolReply(outcome previewJobDeployOutcome) previewDeployToolReply {
	if outcome.Status != v1.PreviewJobDeployStatusDeployed {
		return previewDeployToolReply{Status: outcome.Status, Code: outcome.Code}
	}
	return previewDeployToolReply{
		Status:    outcome.Status,
		URL:       v1.PreviewJobDeployInternalURL,
		ExpiresAt: v1.FormatPreviewJobDeployTime(outcome.ExpiresAt),
		CommitSet: copyCommitSet(outcome.CommitSet),
	}
}

func localPreviewJobDeployOutcome(status, code string) previewJobDeployOutcome {
	return previewJobDeployOutcome{Status: status, Code: code}
}

func (d *Daemon) previewDeployAdvertised() bool {
	return d != nil && d.previewHostingCapability() != nil && d.previewTunnelAdvertised.Load()
}

func (jr *jobRunner) previewJobNetworkSupported() bool {
	cfg := jr.d.cfg
	return cfg.Isolation == "docker" && cfg.JobNetwork == "restricted" && cfg.JobNetworkName != ""
}

func (jr *jobRunner) executeDeployPreviewTool(ctx context.Context) previewDeployToolReply {
	return newPreviewDeployToolReply(jr.requestPreviewJobDeploy(ctx, nil, true))
}

func (jr *jobRunner) requestPreviewJobDeploy(ctx context.Context, commitSet map[string]string, attach bool) previewJobDeployOutcome {
	if !jr.d.previewDeployAdvertised() {
		return localPreviewJobDeployOutcome(v1.PreviewJobDeployStatusRefused, previewJobDeployCodeCapabilityMissing)
	}
	if attach && !jr.previewJobNetworkSupported() {
		return localPreviewJobDeployOutcome(v1.PreviewJobDeployStatusRefused, previewJobDeployCodeNetworkUnsupported)
	}
	sequence, refusal := jr.previewDeploys.reserve()
	if refusal != "" {
		return localPreviewJobDeployOutcome(v1.PreviewJobDeployStatusRefused, refusal)
	}
	outcome := jr.sendPreviewJobDeployRequest(ctx, sequence, commitSet)
	jr.previewDeploys.finish(outcome)
	if outcome.Status == v1.PreviewJobDeployStatusDeployed && attach {
		jr.previewJobDeployed(ctx, outcome)
	}
	return outcome
}

func (jr *jobRunner) previewJobDeployed(context.Context, previewJobDeployOutcome) {}

func (jr *jobRunner) sendPreviewJobDeployRequest(ctx context.Context, sequence uint64, commitSet map[string]string) previewJobDeployOutcome {
	failed := func(code string, err error) previewJobDeployOutcome {
		jr.logger.Warn("preview deploy request not sent", "code", code, "sequence", sequence, "err", err)
		return localPreviewJobDeployOutcome(v1.PreviewJobDeployStatusFailed, code)
	}
	if commitSet == nil {
		resolved, err := jr.previewCommitSet(ctx)
		if err != nil {
			return failed(previewJobDeployCodeCommitSetUnavailable, err)
		}
		commitSet = resolved
	} else {
		commitSet = copyCommitSet(commitSet)
	}
	commits, err := v1.PreviewCommitSetFromMap(commitSet)
	if err != nil {
		return failed(previewJobDeployCodeCommitSetUnavailable, err)
	}
	requestedAt := time.Now().UTC().Truncate(time.Millisecond)
	event := v1.PreviewDeployRequestedEvent{
		SchemaVersion: v1.PreviewDeployRequestedSchemaVersion,
		JobID:         jr.job.JobID, LeaseID: jr.job.LeaseID, FencingToken: jr.job.FencingToken,
		RequestID:       v1.PreviewDeployRequestID(jr.job.JobID, jr.job.LeaseID, sequence),
		CommitSet:       commits,
		CommitSetDigest: v1.PreviewLifecycleCommitSetDigest(commits),
		RequestedAt:     v1.FormatPreviewJobDeployTime(requestedAt),
	}
	if err := validation.PreviewDeployRequestedEvent(event); err != nil {
		return failed(previewJobDeployCodeCommitSetUnavailable, err)
	}
	if err := jr.recordTrustedCommitSetChecked(ctx, commitSet); err != nil {
		return failed(previewJobDeployCodeCommitSetUnavailable, err)
	}
	if err := jr.importPreviewCommits(ctx, commitSet); err != nil {
		return failed(previewJobDeployCodeCommitImportFailed, err)
	}
	w := newPreviewJobDeployWaiter(jr.job.AttemptID, jr.job.ManifestID, event.RequestID, sequence, commitSet, event.CommitSetDigest)
	if err := jr.d.previewJobDeploys.register(w); err != nil {
		if errors.Is(err, errPreviewJobDeployInFlight) {
			return localPreviewJobDeployOutcome(v1.PreviewJobDeployStatusRefused, previewJobDeployCodeInFlight)
		}
		return failed(previewJobDeployCodeEventSendFailed, err)
	}
	jr.previewDeploys.consume(sequence)
	fields := map[string]any{
		"schema_version":    event.SchemaVersion,
		"job_id":            event.JobID,
		"lease_id":          event.LeaseID,
		"fencing_token":     event.FencingToken,
		"request_id":        event.RequestID,
		"commit_set":        event.CommitSet,
		"commit_set_digest": event.CommitSetDigest,
		"requested_at":      event.RequestedAt,
	}
	payload := manifestBoundEvent(jr.job, v1.EventPreviewDeployRequested, fields, requestedAt)
	sentAt := time.Now()
	inserted, err := jr.d.sendEventWithStableID(ctx, w.eventID, payload)
	if err == nil && !inserted {
		err = fmt.Errorf("preview deploy request event %s was already journaled", w.eventID)
	}
	if err != nil {
		jr.d.previewJobDeploys.release(w.key)
		return failed(previewJobDeployCodeEventSendFailed, err)
	}
	return jr.awaitPreviewJobDeploy(jr.ctx, w, sentAt, time.Now, time.After)
}

func (jr *jobRunner) importPreviewCommits(ctx context.Context, commitSet map[string]string) error {
	manager := jr.d.previewHostRepositoryManager()
	for _, repository := range jr.repositories {
		sha, ok := commitSet[repository.ID]
		if !ok {
			return fmt.Errorf("repository %q is missing from the commit set", repository.ID)
		}
		if manager.HasCommit(ctx, repository.URL, sha) {
			continue
		}
		bundlePath, cleanup, err := jr.bundleTrustedCommit(ctx, repository, sha)
		if err != nil {
			return fmt.Errorf("bundle preview commit for repository %q: %w", repository.ID, err)
		}
		err = manager.ImportPreviewCommit(ctx, repository.URL, bundlePath, sha)
		cleanup()
		if err != nil {
			return fmt.Errorf("import preview commit for repository %q: %w", repository.ID, err)
		}
	}
	return nil
}

func (jr *jobRunner) bundleTrustedCommit(ctx context.Context, repository checkedOutRepository, sha string) (string, func(), error) {
	trusted := repository.Trusted
	if trusted == nil {
		return "", nil, fmt.Errorf("the trusted result repository is not available")
	}
	if err := validation.GitCommitSHA("commit_sha", sha); err != nil {
		return "", nil, err
	}
	bundle, err := os.CreateTemp(trusted.root, ".git-preview-*.bundle")
	if err != nil {
		return "", nil, err
	}
	bundlePath := bundle.Name()
	if err := bundle.Close(); err != nil {
		_ = os.Remove(bundlePath)
		return "", nil, err
	}
	if err := os.Remove(bundlePath); err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(bundlePath) }
	ref := repo.PreviewExportRefPrefix + sha
	trusted.mu.Lock()
	defer trusted.mu.Unlock()
	if _, err := trusted.git(ctx, "update-ref", ref, sha); err != nil {
		return "", nil, err
	}
	_, bundleErr := trusted.git(ctx, "bundle", "create", "--quiet", bundlePath, ref)
	_, deleteErr := trusted.git(context.WithoutCancel(ctx), "update-ref", "-d", ref)
	if err := errors.Join(bundleErr, deleteErr); err != nil {
		cleanup()
		return "", nil, err
	}
	return bundlePath, cleanup, nil
}
