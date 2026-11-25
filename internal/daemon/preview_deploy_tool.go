package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/mcpbridge"
	"github.com/example/app-runner/internal/previewhost"
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
	previewJobDeployCodeInvalidResult        = "invalid_result"
	previewJobNetworkSweepTimeout            = 30 * time.Second
	previewJobNetworkAttachTimeout           = 60 * time.Second
)

type previewJobDeployState struct {
	mu       sync.Mutex
	sequence uint64
	inFlight bool
	last     *previewJobDeployOutcome
}

func (s *previewJobDeployState) reserve(used func(uint64) (bool, error)) (uint64, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sequence >= v1.PreviewJobDeployMaxRequests {
		return 0, previewJobDeployCodeLimitReached, nil
	}
	if s.inFlight {
		return 0, previewJobDeployCodeInFlight, nil
	}
	for s.sequence < v1.PreviewJobDeployMaxRequests {
		taken, err := used(s.sequence + 1)
		if err != nil {
			return 0, "", err
		}
		if !taken {
			break
		}
		s.sequence++
	}
	if s.sequence >= v1.PreviewJobDeployMaxRequests {
		return 0, previewJobDeployCodeLimitReached, nil
	}
	s.inFlight = true
	return s.sequence + 1, "", nil
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

func checkedPreviewJobDeployOutcome(outcome previewJobDeployOutcome) previewJobDeployOutcome {
	if outcome.Status == v1.PreviewJobDeployStatusDeployed && outcome.ExpiresAt.IsZero() {
		return previewJobDeployOutcome{
			Status: v1.PreviewJobDeployStatusFailed, Code: previewJobDeployCodeInvalidResult,
			CommitSet: outcome.CommitSet, CommitSetDigest: outcome.CommitSetDigest, Sequence: outcome.Sequence,
		}
	}
	return outcome
}

func newPreviewDeployToolReply(outcome previewJobDeployOutcome) previewDeployToolReply {
	outcome = checkedPreviewJobDeployOutcome(outcome)
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

func (jr *jobRunner) previewJobNetworkConfigured() bool {
	cfg := jr.d.cfg
	return cfg.Isolation == "docker" && cfg.JobNetwork == "restricted" && cfg.JobNetworkName != ""
}

func (jr *jobRunner) previewJobNetworkSupported() bool {
	return jr.previewReachOpen.Load() && jr.previewJobNetworkConfigured()
}

func (jr *jobRunner) executeDeployPreviewTool(ctx context.Context) previewDeployToolReply {
	return newPreviewDeployToolReply(jr.requestPreviewJobDeploy(ctx, nil, true))
}

func (jr *jobRunner) requestPreviewJobDeploy(ctx context.Context, commitSet map[string]string, attach bool) previewJobDeployOutcome {
	if !jr.d.previewDeployAdvertised() {
		return localPreviewJobDeployOutcome(v1.PreviewJobDeployStatusRefused, previewJobDeployCodeCapabilityMissing)
	}
	if attach && !jr.previewJobNetworkSupported() {
		code := previewJobDeployCodeNetworkUnsupported
		if jr.previewJobNetworkConfigured() && jr.previewReachProviderSupported() {
			code = previewJobDeployCodeCapabilityMissing
		}
		return localPreviewJobDeployOutcome(v1.PreviewJobDeployStatusRefused, code)
	}
	sequence, refusal, err := jr.previewDeploys.reserve(func(sequence uint64) (bool, error) {
		return jr.d.journal.HasRecord(ctx, jr.previewJobDeployEventID(sequence))
	})
	if err != nil {
		jr.logger.Warn("preview deploy request sequence unavailable", "err", err)
		return localPreviewJobDeployOutcome(v1.PreviewJobDeployStatusFailed, previewJobDeployCodeEventSendFailed)
	}
	if refusal != "" {
		return localPreviewJobDeployOutcome(v1.PreviewJobDeployStatusRefused, refusal)
	}
	outcome := checkedPreviewJobDeployOutcome(jr.sendPreviewJobDeployRequest(ctx, sequence, commitSet))
	if outcome.Status == v1.PreviewJobDeployStatusDeployed && attach {
		jr.previewJobDeployed(outcome)
	}
	jr.previewDeploys.finish(outcome)
	return outcome
}

func (jr *jobRunner) publishFinalPreviewEvidence(ctx context.Context, final map[string]string) {
	deployed := jr.previewDeploys.lastDeployed()
	if deployed == nil {
		return
	}
	commits, err := v1.PreviewCommitSetFromMap(final)
	if err != nil {
		jr.logger.Warn("final preview evidence skipped: invalid final commit set", "err", err)
		return
	}
	digest := v1.PreviewLifecycleCommitSetDigest(commits)
	if deployed.CommitSetDigest != digest {
		outcome := jr.requestPreviewJobDeploy(ctx, final, false)
		if outcome.Status != v1.PreviewJobDeployStatusDeployed {
			jr.logger.Warn("final preview evidence skipped: final commit set was not deployed",
				"status", outcome.Status, "code", outcome.Code)
			return
		}
		deployed = &outcome
	}
	if deployed.EdgeHost == "" {
		jr.logger.Warn("final preview evidence skipped: deploy result has no edge host")
		return
	}
	if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
		jr.logger.Warn("final preview evidence skipped: lease validation failed", "err", err)
		return
	}
	fields := map[string]any{
		"url": "https://" + deployed.EdgeHost, "health": "healthy",
		"expires_at":        deployed.ExpiresAt.UTC().Format(time.RFC3339Nano),
		"known_limitations": []string{}, "commit_set": copyCommitSet(final),
	}
	eventID := v1.PreviewJobEvidenceEventID(jr.job.JobID, jr.job.LeaseID, digest)
	payload := manifestBoundEvent(jr.job, "preview", fields, time.Now().UTC())
	if _, err := jr.d.sendEventWithStableID(ctx, eventID, payload); err != nil {
		jr.logger.Warn("final preview evidence not sent", "event_id", eventID, "err", err)
	}
}

func (jr *jobRunner) previewReachProviderSupported() bool {
	return jr.d.cfg.Provider != "codex"
}

func (jr *jobRunner) previewReachAllowed() bool {
	return jr.previewReachProviderSupported() && jr.previewJobNetworkConfigured() &&
		slices.Contains(jr.d.capabilitiesForJob(jr.job.Kind), mcpbridge.DeployPreviewTool)
}

func (jr *jobRunner) previewJobDeployed(outcome previewJobDeployOutcome) {
	attacher, ok := jr.d.previewHostController.(previewhost.JobNetworkAttacher)
	if !ok {
		jr.logger.Warn("preview job network skipped: hosting cannot attach job networks")
		return
	}
	if !jr.previewJobNetworkSupported() {
		jr.logger.Warn("preview job network skipped: the job has no restricted docker network")
		return
	}
	jr.previewNetworkMu.Lock()
	defer jr.previewNetworkMu.Unlock()
	if jr.previewNetworkClosed {
		jr.logger.Warn("preview job network skipped: the job has ended")
		return
	}
	container := isolation.ContainerName(jr.d.runnerID, jr.job.AttemptID)
	attachCtx, cancel := context.WithTimeout(jr.ctx, previewJobNetworkAttachTimeout)
	defer cancel()
	if err := attacher.AttachJob(attachCtx, outcome.PreviewID, jr.job.AttemptID, container); err != nil {
		jr.logger.Warn("preview job network attach failed", "preview_id", outcome.PreviewID, "err", err)
	}
}

func (jr *jobRunner) detachPreviewJobNetwork() {
	jr.previewNetworkMu.Lock()
	defer jr.previewNetworkMu.Unlock()
	jr.previewNetworkClosed = true
	attacher, ok := jr.d.previewHostController.(previewhost.JobNetworkAttacher)
	if !ok {
		return
	}
	if err := attacher.DetachJob(context.WithoutCancel(jr.ctx), jr.job.AttemptID); err != nil {
		jr.logger.Warn("preview job network detach failed", "err", err)
	}
}

func (d *Daemon) sweepJobPreviewNetworksAtStartup(ctx context.Context) {
	attacher, ok := d.previewHostController.(previewhost.JobNetworkAttacher)
	if !ok {
		return
	}
	live, err := d.journal.LoadLease(ctx)
	if err != nil {
		d.logger.Warn("preview job network sweep skipped: durable lease unavailable", "err", err)
		return
	}
	keep := func(attemptID string) bool { return live != nil && live.AttemptID == attemptID }
	sweepCtx, cancel := context.WithTimeout(ctx, previewJobNetworkSweepTimeout)
	defer cancel()
	if err := attacher.SweepJobNetworks(sweepCtx, keep); err != nil && ctx.Err() == nil {
		d.logger.Warn("preview job network sweep", "err", err)
	}
}

func (jr *jobRunner) previewJobDeployEventID(sequence uint64) string {
	return v1.PreviewDeployRequestedEventID(v1.PreviewDeployRequestID(jr.job.JobID, jr.job.LeaseID, sequence))
}

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
	callDeadline, _ := ctx.Deadline()
	return jr.awaitPreviewJobDeploy(jr.ctx, w, sentAt, callDeadline, time.Now, time.After)
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
		basis := startCommit(repository)
		if basis != "" && !manager.HasCommit(ctx, repository.URL, basis) {
			basis = ""
		}
		bundlePath, cleanup, err := jr.bundleTrustedCommit(ctx, repository, sha, basis)
		if err != nil {
			return fmt.Errorf("bundle preview commit for repository %q: %w", repository.ID, err)
		}
		if bundlePath == "" {
			continue
		}
		err = manager.ImportPreviewCommit(ctx, repository.URL, bundlePath, sha)
		cleanup()
		if err != nil {
			return fmt.Errorf("import preview commit for repository %q: %w", repository.ID, err)
		}
	}
	return nil
}

func (jr *jobRunner) bundleTrustedCommit(ctx context.Context, repository checkedOutRepository, sha, basis string) (string, func(), error) {
	trusted := repository.Trusted
	if trusted == nil {
		return "", nil, fmt.Errorf("the trusted result repository is not available")
	}
	if err := validation.GitCommitSHA("commit_sha", sha); err != nil {
		return "", nil, err
	}
	if basis != "" && validation.GitCommitSHA("basis_sha", basis) != nil {
		basis = ""
	}
	trusted.mu.Lock()
	defer trusted.mu.Unlock()
	if basis != "" {
		if _, err := trusted.git(ctx, "cat-file", "-e", basis+"^{commit}"); err != nil {
			basis = ""
		}
	}
	if basis != "" {
		if _, err := trusted.git(ctx, "merge-base", "--is-ancestor", sha, basis); err == nil {
			return "", func() {}, nil
		}
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
	if _, err := trusted.git(ctx, "update-ref", ref, sha); err != nil {
		return "", nil, err
	}
	bundleArgs := []string{"bundle", "create", "--quiet", bundlePath, ref}
	if basis != "" {
		bundleArgs = append(bundleArgs, "^"+basis)
	}
	_, bundleErr := trusted.git(ctx, bundleArgs...)
	_, deleteErr := trusted.git(context.WithoutCancel(ctx), "update-ref", "-d", ref)
	if err := errors.Join(bundleErr, deleteErr); err != nil {
		cleanup()
		return "", nil, err
	}
	return bundlePath, cleanup, nil
}
