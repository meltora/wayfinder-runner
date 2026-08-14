package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack"
	"github.com/example/app-runner/internal/collector"
	"github.com/example/app-runner/internal/hookspool"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/lease"
	"github.com/example/app-runner/internal/mcpbridge"
	"github.com/example/app-runner/internal/provider"
	"github.com/example/app-runner/internal/repo"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/scanner"
	"github.com/example/app-runner/internal/supervisor"
	"github.com/example/app-runner/internal/validation"
)

type jobRunner struct {
	d        *Daemon
	job      v1.JobSpec
	jobToken string
	workDir  string
	proc     supervisor.Process
	logger   *slog.Logger

	ctx             context.Context
	cancel          context.CancelFunc
	procMu          sync.Mutex
	cancelled       bool
	leaseLost       bool
	sessionID       string
	providerError   string
	capacityFailure *provider.CapacityFailure

	decisionMu sync.Mutex
	decisions  map[string]chan v1.CommandPayload

	checkMu sync.Mutex

	idempotencyMu   sync.Mutex
	seenIdempotency map[string]*toolCallMemo
	repositoryURL   string
	repositoryID    string
	repositories    []checkedOutRepository
	packDir         string
}

type checkedOutRepository struct {
	ID      string
	URL     string
	Base    string
	Branch  string
	WorkDir string
	Primary bool
}

type toolCallMemo struct {
	done   chan struct{}
	result []byte
	err    error
}

func (d *Daemon) startJob(ctx context.Context, job v1.JobSpec) error {
	if err := validation.JobSpec(job); err != nil {
		return fmt.Errorf("invalid job: %w", err)
	}
	job, err := v1.CloneJobSpec(job)
	if err != nil {
		return fmt.Errorf("isolate job command: %w", err)
	}
	jobToken, err := newSecretToken()
	if err != nil {
		return err
	}
	d.jobMu.Lock()
	defer d.jobMu.Unlock()
	if d.updateDrain != nil {
		return fmt.Errorf("runner is draining for update %s", d.updateDrain.UpdateID)
	}
	if d.activeJob != nil {
		return fmt.Errorf("runner busy with job %s", d.activeJob.job.JobID)
	}
	expiresAt := job.LeaseExpiresAt
	if expiresAt.IsZero() && d.cfg.AllowUnsafeDirect {
		expiresAt = time.Now().Add(30 * time.Minute)
	}
	if !expiresAt.After(time.Now()) {
		return fmt.Errorf("job requires an unexpired control-plane lease")
	}
	if expiresAt.After(time.Now().Add(24 * time.Hour)) {
		return fmt.Errorf("job lease cannot exceed 24 hours")
	}
	now := time.Now()
	l := lease.Lease{
		LeaseID: job.LeaseID, JobID: job.JobID, AttemptID: job.AttemptID, Kind: job.Kind,
		ManifestID: job.ManifestID, ManifestDigest: job.ManifestDigest, WorkflowRevision: job.WorkflowRevision,
		FencingToken: job.FencingToken,
		AcquiredAt:   now,
		ExpiresAt:    expiresAt,
	}
	if err := d.leases.Acquire(l); err != nil {
		return err
	}
	if err := d.journal.SaveLease(ctx, l); err != nil {
		d.leases.Release(job.AttemptID)
		return fmt.Errorf("persist job lease: %w", err)
	}
	releaseLease := func() {
		d.leases.Release(job.AttemptID)
		if err := d.journal.ClearLease(context.Background(), job.AttemptID); err != nil {
			d.logger.Error("clear failed job lease", "attempt", job.AttemptID, "err", err)
		}
	}
	jobsRoot := filepath.Join(d.cfg.StateDir, "jobs")
	if err := os.MkdirAll(jobsRoot, 0o700); err != nil {
		releaseLease()
		return fmt.Errorf("create jobs root: %w", err)
	}
	workDir, err := os.MkdirTemp(jobsRoot, "job-")
	if err != nil {
		releaseLease()
		return fmt.Errorf("create job workspace: %w", err)
	}
	// #nosec G302 -- job workspace is a private directory; owner traversal requires 0700.
	if err := os.Chmod(workDir, 0o700); err != nil {
		releaseLease()
		_ = os.RemoveAll(workDir)
		return fmt.Errorf("secure job workspace: %w", err)
	}
	jr := &jobRunner{
		d:               d,
		job:             job,
		jobToken:        jobToken,
		workDir:         workDir,
		logger:          d.logger.With("job", job.JobID, "attempt", job.AttemptID),
		decisions:       map[string]chan v1.CommandPayload{},
		seenIdempotency: map[string]*toolCallMemo{},
	}
	jr.ctx, jr.cancel = context.WithCancel(context.Background())
	d.activeJob = jr
	go jr.run()
	return nil
}

func (d *Daemon) cancelActiveJob(reason string) {
	d.jobMu.Lock()
	jr := d.activeJob
	d.jobMu.Unlock()
	if jr == nil {
		return
	}
	jr.logger.Warn("cancelling job", "reason", reason)
	jr.cancel()
	jr.procMu.Lock()
	jr.cancelled = true
	proc := jr.proc
	jr.procMu.Unlock()
	if proc != nil {
		_ = proc.Kill()
	}
}

func (d *Daemon) cancelJob(ctx context.Context, attemptID, reason string) error {
	d.jobMu.Lock()
	jr := d.activeJob
	d.jobMu.Unlock()
	if jr != nil {
		if attemptID != "" && jr.job.AttemptID != attemptID {
			return fmt.Errorf("attempt %s is not active", attemptID)
		}
		d.cancelActiveJob(reason)
		return nil
	}
	current := d.leases.Current()
	if current == nil {
		return nil
	}
	if attemptID != "" && current.AttemptID != attemptID {
		return fmt.Errorf("attempt %s does not own the recovered lease", attemptID)
	}
	if d.cfg.Isolation == "docker" {
		cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := isolation.RemoveRunnerContainers(cleanupCtx, d.runnerID)
		cancel()
		if err != nil {
			return fmt.Errorf("stop recovered job container: %w", err)
		}
	}
	if err := d.journal.ClearLease(ctx, current.AttemptID); err != nil {
		return fmt.Errorf("clear recovered lease: %w", err)
	}
	d.leases.Release(current.AttemptID)
	d.recoveredLease.Store(false)
	return cleanupOrphanedJobState(d.cfg.StateDir)
}

func cleanupOrphanedJobState(stateDir string) error {
	var errs []error
	for _, target := range []struct {
		dir, prefix string
	}{
		{filepath.Join(stateDir, "jobs"), "job-"},
		{filepath.Join(stateDir, "job-homes"), "home-"},
		{filepath.Join(stateDir, "sockets"), "job-"},
		{filepath.Join(stateDir, "tmux"), "job-"},
		{filepath.Join(stateDir, "supervisor"), ".herdr-exit-"},
	} {
		entries, err := os.ReadDir(target.dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), target.prefix) {
				continue
			}
			path := filepath.Join(target.dir, entry.Name())
			if err := os.RemoveAll(path); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (jr *jobRunner) run() {
	d := jr.d
	ctx := jr.ctx
	job := jr.job
	leaseCtx, stopLeaseMonitor := context.WithCancel(ctx)
	defer stopLeaseMonitor()
	go jr.monitorLease(leaseCtx)
	finish := func(state string, payload map[string]any) {
		stopLeaseMonitor()
		finishCtx, finishCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer finishCancel()
		capacityWait := state == "waiting_for_provider_capacity"
		if payload == nil {
			payload = map[string]any{}
		}
		if err := d.leases.Validate(job.AttemptID, job.FencingToken); err != nil {
			state = "quarantined"
			payload["lease_error"] = err.Error()
		}
		jr.procMu.Lock()
		leaseLost := jr.leaseLost
		jr.procMu.Unlock()
		if leaseLost {
			state = "quarantined"
		}
		payload["kind"] = "job_finished"
		payload["state"] = state
		payload["job_id"] = job.JobID
		payload["attempt_id"] = job.AttemptID
		payload["fencing_token"] = job.FencingToken
		if sid := jr.currentSessionID(); sid != "" && !capacityWait {
			cp := collector.NewCheckpoint(job, "finished", "", "")
			cp.ProviderSessionID = sid
			if err := d.collectors.SaveCheckpoint(cp); err != nil {
				jr.logger.Warn("save final checkpoint", "err", err)
			}
		}
		if d.cfg.Isolation == "docker" {
			if err := isolation.RemoveRunnerContainers(finishCtx, d.runnerID); err != nil {
				jr.logger.Error("remove job container", "err", err)
				state = "quarantined"
				payload["state"] = state
				payload["cleanup_error"] = "job container cleanup failed"
			}
		}
		finishRecorded := true
		if err := d.sendEvent(finishCtx, payload); err != nil {
			finishRecorded = false
			d.recoveredLease.Store(true)
			jr.logger.Error("durably record finish event; lease retained for reconciliation", "err", err)
		}
		if finishRecorded {
			if err := d.journal.ClearLease(finishCtx, job.AttemptID); err != nil {
				d.recoveredLease.Store(true)
				jr.logger.Error("clear durable lease; lease retained for reconciliation", "err", err)
			} else {
				d.leases.Release(job.AttemptID)
				d.recoveredLease.Store(false)
			}
		}
		d.jobMu.Lock()
		d.activeJob = nil
		d.signalUpdateLocked()
		d.jobMu.Unlock()
		if err := safeio.MakeTreeRemovable(jr.workDir); err != nil {
			jr.logger.Warn("prepare workdir cleanup", "err", err)
		}
		if err := os.RemoveAll(jr.workDir); err != nil {
			jr.logger.Warn("workdir cleanup", "err", err)
		}
	}

	if err := os.MkdirAll(jr.workDir, 0o700); err != nil {
		finish("failed", map[string]any{"error": err.Error()})
		return
	}

	jr.publishProgress(ctx, "provisioning", "preparing workspace", "")
	workDir, cleanup, err := jr.prepareWorkspace(ctx)
	if err != nil {
		jr.logger.Error("workspace preparation failed", "err", err)
		finish("failed", map[string]any{"error": err.Error()})
		return
	}
	defer cleanup()

	isDocker := d.cfg.Isolation == "docker"
	socketsRoot := filepath.Join(d.cfg.StateDir, "sockets")
	if !isDocker && len(filepath.Join(socketsRoot, "job-1234567890", "mcp.sock")) > 100 {
		socketsRoot = os.TempDir()
	}
	if err := os.MkdirAll(socketsRoot, 0o700); err != nil {
		finish("failed", map[string]any{"error": err.Error()})
		return
	}
	socketDir, err := os.MkdirTemp(socketsRoot, "job-")
	if err != nil {
		finish("failed", map[string]any{"error": err.Error()})
		return
	}
	defer func() { _ = os.RemoveAll(socketDir) }()
	sockPath := filepath.Join(socketDir, "mcp.sock")
	tokenPath := filepath.Join(socketDir, "job.token")
	if err := os.WriteFile(tokenPath, []byte(jr.jobToken+"\n"), 0o400); err != nil {
		finish("failed", map[string]any{"error": "create MCP token file: " + err.Error()})
		return
	}
	mcpSrv := &mcpbridge.UnixServer{SocketPath: sockPath, Token: jr.jobToken, Handler: jr.handleToolCall}
	mcpCtx, stopMCP := context.WithCancel(ctx)
	defer stopMCP()
	if err := mcpSrv.Start(mcpCtx); err != nil {
		finish("failed", map[string]any{"error": err.Error()})
		return
	}
	agentSockPath := mcpSrv.SocketPath

	adapterName := d.cfg.Provider
	adapter, err := provider.For(adapterName)
	if err != nil {
		finish("failed", map[string]any{"error": err.Error()})
		return
	}
	spec, err := adapter.BuildSpec(provider.StartParams{
		Job:             job,
		WorkDir:         workDir,
		ConfigDir:       socketDir,
		PackDir:         jr.packDir,
		MCPSock:         agentSockPath,
		MCPTokenFile:    tokenPath,
		Prompt:          jr.buildPrompt(),
		BinaryPath:      d.cfg.ProviderBinaries[adapterName],
		Model:           d.cfg.AgentModel,
		ResumeSessionID: job.ResumeSessionID,
		ReadOnly:        job.Kind == v1.JobKindPlan,
	})
	if err != nil {
		finish("failed", map[string]any{"error": err.Error()})
		return
	}
	iso, err := isolation.Select(d.cfg.Isolation, d.cfg.AllowUnsafeDirect)
	if err != nil {
		finish("failed", map[string]any{"error": err.Error()})
		return
	}
	isoPolicy := isolation.Policy{
		WorkDir:         workDir,
		WorkspaceRoot:   jr.workDir,
		AgentPackDir:    jr.packDir,
		MCPSock:         sockPath,
		MCPTokenFile:    tokenPath,
		MemoryMB:        d.cfg.JobMemoryMB,
		CPUs:            d.cfg.JobCPUs,
		PIDs:            d.cfg.JobPIDs,
		NetworkNone:     d.cfg.JobNetwork == "none",
		NetworkName:     d.cfg.JobNetworkName,
		ReadOnlyFS:      isDocker,
		ReadOnlyWorkDir: job.Kind == v1.JobKindPlan,
		Image:           d.cfg.JobImage,
		RunnerID:        d.runnerID,
		AttemptID:       job.AttemptID,
		NestedSandbox:   isDocker && adapterName != "opencode",
	}
	if iso.Name() == "docker" {
		spec, err = adapter.BuildSpec(provider.StartParams{
			Job:             job,
			WorkDir:         workDir,
			ConfigDir:       socketDir,
			PackDir:         jr.packDir,
			MCPSock:         agentSockPath,
			MCPTokenFile:    tokenPath,
			MCPBinary:       "/usr/local/bin/our-runner",
			Prompt:          jr.buildPrompt(),
			BinaryPath:      d.cfg.ProviderBinaries[adapterName],
			Model:           d.cfg.AgentModel,
			ResumeSessionID: job.ResumeSessionID,
			ReadOnly:        job.Kind == v1.JobKindPlan,
			HardenedRuntime: true,
		})
		if err != nil {
			finish("failed", map[string]any{"error": err.Error()})
			return
		}
		isoPolicy.Environment = provider.ContainerEnvironment(spec.Env)
		jobHome, err := isolation.PrepareHome(d.cfg.StateDir, job.JobID, job.AttemptID, adapterName)
		if err != nil {
			finish("failed", map[string]any{"error": "prepare job home: " + err.Error()})
			return
		}
		defer func() {
			if err := isolation.CleanupHome(jobHome); err != nil {
				jr.logger.Warn("job home cleanup", "err", err)
			}
		}()
		isoPolicy.JobHome = jobHome
	}
	spec.Command, spec.Args, err = iso.Wrap(spec.Command, spec.Args, isoPolicy)
	if err != nil {
		finish("failed", map[string]any{"error": err.Error()})
		return
	}
	spec.Env = append(spec.Env,
		"PLATFORM_MCP_SOCKET="+agentSockPath,
		"PLATFORM_MCP_TOKEN_FILE="+tokenPath,
	)
	supervisorRoot := filepath.Join(d.cfg.StateDir, "supervisor")
	if err := os.MkdirAll(supervisorRoot, 0o700); err != nil {
		finish("failed", map[string]any{"error": "create supervisor runtime: " + err.Error()})
		return
	}
	spec.RuntimeDir = supervisorRoot

	jr.publishProgress(ctx, "agent_started", "provider process starting", "")
	outR, outW := io.Pipe()
	spec.Stdout = outW
	spec.Stderr = outW

	jr.procMu.Lock()
	if jr.cancelled {
		jr.procMu.Unlock()
		finish("cancelled", nil)
		return
	}
	proc, err := d.supervisor.Start(ctx, spec)
	if err != nil {
		jr.procMu.Unlock()
		finish("failed", map[string]any{"error": err.Error()})
		return
	}
	jr.proc = proc
	jr.procMu.Unlock()
	outputConsumed := make(chan struct{})
	go func() {
		defer close(outputConsumed)
		jr.consumeOutput(outR)
	}()

	exitErr := proc.Wait()
	_ = outW.Close()
	<-outputConsumed
	if reporter, ok := proc.(supervisor.SessionReporter); ok {
		if ref, err := reporter.SessionRef(); err == nil && ref != "" {
			jr.procMu.Lock()
			jr.sessionID = ref
			jr.procMu.Unlock()
		}
	}
	capacityFailure, capacityExhausted := jr.currentCapacityFailure()
	if exitErr != nil || capacityExhausted {
		jr.procMu.Lock()
		cancelled := jr.cancelled
		leaseLost := jr.leaseLost
		jr.procMu.Unlock()
		state := "failed"
		if leaseLost {
			state = "quarantined"
		} else if cancelled {
			state = "cancelled"
		} else if capacityExhausted {
			state = "waiting_for_provider_capacity"
		}
		jr.logger.Error("agent exited", "err", exitErr, "capacity_exhausted", capacityExhausted)
		agentError := "provider capacity exhausted"
		if exitErr != nil {
			agentError = "agent exited: " + exitErr.Error()
		}
		if providerError := jr.currentProviderError(); providerError != "" {
			agentError += "; provider: " + providerError
		}
		extra := map[string]any{"error": agentError}
		if job.Kind == v1.JobKindBuild && (state == "failed" || state == "waiting_for_provider_capacity") {
			rm := repo.NewManager(filepath.Join(d.cfg.StateDir, "repos"))
			rm.AllowLocal = d.cfg.AllowUnsafeDirect
			_, buildExtra, finalizationErr := jr.finalizeBuild(ctx, rm, false)
			if buildExtra != nil {
				for key, value := range buildExtra {
					if key == "error" {
						extra["checks_error"] = value
						continue
					}
					extra[key] = value
				}
			}
			if finalizationErr != nil {
				extra["finalization_error"] = finalizationErr.Error()
			}
		}
		if state == "waiting_for_provider_capacity" {
			extra["failure_class"] = "provider_capacity_exhausted"
			extra["resume_policy"] = jr.effectiveCapacityResumePolicy()
			lineage := jr.capacityRecoveryLineage(extra)
			checkpointRef := jr.persistCapacityCheckpoint(lineage)
			capacityEvent := jr.providerCapacityEvent(capacityFailure, lineage, checkpointRef)
			if err := validation.ProviderCapacityExhaustedEvent(capacityEvent); err != nil {
				extra["capacity_event_error"] = "provider capacity event failed local validation"
				jr.logger.Error("validate provider capacity event", "err", err)
			} else if err := jr.d.sendEvent(ctx, capacityEvent); err != nil {
				extra["capacity_event_error"] = "provider capacity event could not be recorded"
				jr.logger.Error("record provider capacity event", "err", err)
			}
		}
		finish(state, extra)
		return
	}

	if job.Kind == v1.JobKindBuild {
		rm := repo.NewManager(filepath.Join(d.cfg.StateDir, "repos"))
		rm.AllowLocal = d.cfg.AllowUnsafeDirect
		state, extra, err := jr.finalizeBuild(ctx, rm, true)
		if err != nil {
			finish("failed", map[string]any{"error": err.Error()})
			return
		}
		finish(state, extra)
		return
	}
	finish("succeeded", nil)
}

func (jr *jobRunner) monitorLease(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
				jr.procMu.Lock()
				if jr.leaseLost {
					jr.procMu.Unlock()
					return
				}
				jr.leaseLost = true
				jr.cancelled = true
				proc := jr.proc
				jr.procMu.Unlock()
				jr.logger.Error("authoritative lease lost; quarantining job", "err", err)
				jr.cancel()
				if proc != nil {
					_ = proc.Kill()
				}
				return
			}
		}
	}
}

func (jr *jobRunner) prepareWorkspace(ctx context.Context) (string, func(), error) {
	d := jr.d
	job := jr.job
	if len(job.Repositories) == 0 {
		return "", func() {}, fmt.Errorf("job has no registered repositories")
	}
	if len(job.Repositories) > 16 {
		return "", func() {}, fmt.Errorf("job exceeds 16-repository limit")
	}
	refs := append([]v1.JobRepository(nil), job.Repositories...)
	primaryCount := 0
	for _, ref := range refs {
		if ref.Primary {
			primaryCount++
		}
	}
	if primaryCount != 1 {
		return "", func() {}, fmt.Errorf("job must define exactly one primary repository")
	}

	rm := repo.NewManager(filepath.Join(d.cfg.StateDir, "repos"))
	rm.AllowLocal = d.cfg.AllowUnsafeDirect
	cleanup := func() {
		for i := len(jr.repositories) - 1; i >= 0; i-- {
			repository := jr.repositories[i]
			if err := rm.RemoveWorkspace(repository.WorkDir); err != nil {
				jr.logger.Warn("workspace cleanup", "repository", repository.ID, "err", err)
			}
		}
	}

	seen := map[string]bool{}
	primaryDir := ""
	for _, ref := range refs {
		if err := validation.ID("repository_id", ref.RepositoryID); err != nil {
			cleanup()
			return "", func() {}, err
		}
		if seen[ref.RepositoryID] {
			cleanup()
			return "", func() {}, fmt.Errorf("repository %q appears more than once", ref.RepositoryID)
		}
		seen[ref.RepositoryID] = true
		repository, ok := d.cfg.Repositories[ref.RepositoryID]
		if !ok {
			cleanup()
			return "", func() {}, fmt.Errorf("repository %q is not registered on this runner", ref.RepositoryID)
		}
		if _, err := rm.Register(ctx, repository.URL); err != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("register repository %s: %w", ref.RepositoryID, err)
		}
		base := ref.BaseBranch
		if base == "" {
			var err error
			base, err = rm.DefaultBranch(ctx, repository.URL)
			if err != nil {
				cleanup()
				return "", func() {}, err
			}
		}
		wtDir := filepath.Join(jr.workDir, "repositories", ref.RepositoryID)
		branch := "job/" + job.JobID + "/" + job.AttemptID
		if err := rm.CreateWorkspace(ctx, repository.URL, branch, base, wtDir); err != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("workspace %s: %w", ref.RepositoryID, err)
		}
		runtimeRepository := checkedOutRepository{
			ID: ref.RepositoryID, URL: repository.URL, Base: base,
			Branch: branch, WorkDir: wtDir, Primary: ref.Primary,
		}
		jr.repositories = append(jr.repositories, runtimeRepository)
		if ref.Primary {
			jr.repositoryID = ref.RepositoryID
			jr.repositoryURL = repository.URL
			primaryDir = wtDir
		}

		inv, err := scanner.Scan(wtDir)
		if err != nil {
			jr.logger.Warn("config scan failed", "repository", ref.RepositoryID, "err", err)
		} else if err := d.sendEvent(ctx, map[string]any{
			"kind": "config_inventory", "job_id": job.JobID,
			"attempt_id": job.AttemptID, "fencing_token": job.FencingToken,
			"repository_id": ref.RepositoryID, "inventory": inv,
		}); err != nil {
			jr.logger.Warn("send config inventory", "repository", ref.RepositoryID, "err", err)
		}
	}

	if job.AgentPack != nil {
		packDir := filepath.Join(jr.workDir, "pack")
		if err := jr.fetchAndMountPack(job.AgentPack, packDir); err != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("agent pack: %w", err)
		}
		jr.packDir = packDir
	}
	return primaryDir, cleanup, nil
}

func (jr *jobRunner) fetchAndMountPack(ref *v1.AgentPackRef, destDir string) error {
	if ref.KeyID == "" {
		return fmt.Errorf("agent pack is missing signing key ID")
	}
	keyB64, ok := jr.d.cfg.AgentPackPublicKeys[ref.KeyID]
	if !ok || keyB64 == "" {
		return fmt.Errorf("agent pack signing key %q is not trusted by this runner", ref.KeyID)
	}
	if err := jr.d.validateArtifactURL(ref.URL); err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(jr.ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, ref.URL, nil)
	if err != nil {
		return err
	}
	resp, err := jr.d.httpClientForURL(ref.URL, 2*time.Minute).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pack fetch: status %d", resp.StatusCode)
	}
	if resp.ContentLength > 64<<20 {
		return fmt.Errorf("agent pack archive exceeds size limit")
	}
	pub, err := agentpack.PublicKeyFromBase64(keyB64)
	if err != nil {
		return err
	}
	pack, err := agentpack.VerifyAndMount(io.LimitReader(resp.Body, 64<<20), ref.Digest, pub, destDir)
	if err != nil {
		return err
	}
	if pack.Manifest.Version != ref.Version {
		return fmt.Errorf("agent pack version mismatch: got %q want %q", pack.Manifest.Version, ref.Version)
	}
	if pack.Manifest.Provider != "" && pack.Manifest.Provider != jr.d.cfg.Provider {
		return fmt.Errorf("agent pack targets provider %q, runner uses %q", pack.Manifest.Provider, jr.d.cfg.Provider)
	}
	return nil
}

func (d *Daemon) validateArtifactURL(raw string) error {
	candidate, err := url.Parse(raw)
	if err != nil || candidate.Host == "" {
		return fmt.Errorf("invalid artifact URL")
	}
	if candidate.Scheme != "https" && (!d.cfg.AllowUnsafeDirect || candidate.Scheme != "http") {
		return fmt.Errorf("artifact URL must use HTTPS")
	}
	origins := append([]string(nil), d.cfg.AllowedArtifactOrigins...)
	if len(origins) == 0 {
		httpURL := d.cfg.ControlPlaneHTTPURL
		if httpURL == "" {
			httpURL = deriveHTTPURL(d.cfg.ControlPlaneURL)
		}
		origins = append(origins, httpURL)
	}
	for _, rawOrigin := range origins {
		origin, parseErr := url.Parse(rawOrigin)
		if parseErr == nil && origin.Scheme == candidate.Scheme && origin.Host == candidate.Host {
			return nil
		}
	}
	return fmt.Errorf("artifact URL origin is not approved")
}

func (jr *jobRunner) buildPrompt() string {
	t := jr.job.Ticket
	var b strings.Builder
	fmt.Fprintf(&b, "You are working on ticket %s: %s\n\n%s\n", t.Key, t.Summary, t.Description)
	if t.AcceptanceCriteria != "" {
		fmt.Fprintf(&b, "\nAcceptance criteria:\n%s\n", t.AcceptanceCriteria)
	}
	b.WriteString("\nFirst call the platform_get_job_context MCP tool for full context. ")
	if jr.packDir != "" {
		fmt.Fprintf(&b, "A signed, approved Agent Pack is mounted read-only at %s; load its instructions and relevant skills before acting. ", jr.packDir)
	}
	switch jr.job.Kind {
	case v1.JobKindPlan:
		if context := jr.job.PlanRevisionContext; context != nil {
			fmt.Fprintf(&b, "This is revision request %s for reviewed plan %s. Read the exact immutable base plan and human feedback from platform_get_job_context, then publish the replacement using platform_publish_plan_revision. Do not modify any files.", context.RequestID, context.BasePlan.RevisionID)
		} else {
			b.WriteString("Analyze the repository and publish an implementation plan using platform_publish_plan. Do not modify any files.")
		}
	case v1.JobKindBuild:
		fmt.Fprintf(&b, "Implement the approved plan (revision %d) in this repository. ", jr.job.ApprovedPlanRevision)
		if len(jr.job.RequiredChecks) > 0 {
			fmt.Fprintf(&b, "The following checks must pass before review: %s. ", strings.Join(jr.job.RequiredChecks, "; "))
		}
		b.WriteString("Publish progress, test results, and any blockers through the platform MCP tools.")
		if jr.job.ReviewComment != "" {
			fmt.Fprintf(&b, "\n\nA reviewer left this comment on your previous work; address it:\n%s\n", jr.job.ReviewComment)
		}
	}
	return b.String()
}

func (jr *jobRunner) publishProgress(ctx context.Context, milestone, task, next string) {
	_ = jr.sendJobEvent(ctx, "progress", map[string]any{
		"milestone":    milestone,
		"current_task": task,
		"next_action":  next,
	})
}

func (jr *jobRunner) consumeOutput(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var m map[string]any
		if err := json.Unmarshal(line, &m); err == nil {
			if sid := providerSessionID(m); sid != "" && validation.ID("provider_session_id", sid) == nil {
				jr.procMu.Lock()
				jr.sessionID = sid
				jr.procMu.Unlock()
			}
			if providerError := providerErrorMessage(m); providerError != "" {
				jr.procMu.Lock()
				jr.providerError = providerError
				jr.procMu.Unlock()
			}
			if capacityFailure, ok := provider.ClassifyCapacityFailure(jr.d.cfg.Provider, m, time.Now()); ok {
				jr.procMu.Lock()
				failureCopy := capacityFailure
				jr.capacityFailure = &failureCopy
				jr.procMu.Unlock()
			}
			jr.logger.Info("agent event", "type", safeLogValue(m["type"]), "status", safeLogValue(m["status"]))
		} else {
			jr.logger.Debug("unstructured agent output suppressed", "bytes", len(line))
		}
	}
	if err := sc.Err(); err != nil {
		jr.logger.Warn("agent output stream", "err", err)
		// A provider can emit a line larger than our parser boundary. Keep the
		// boundary fail-closed and close the pipe so the supervised writer cannot
		// wedge job finalization while emitting unbounded output.
		if closer, ok := r.(io.Closer); ok {
			_ = closer.Close()
		}
	}
}

func providerSessionID(event map[string]any) string {
	for _, key := range []string{"session_id", "sessionID"} {
		if value, ok := event[key].(string); ok {
			return value
		}
	}
	return ""
}

func providerErrorMessage(event map[string]any) string {
	message := provider.ErrorMessage(event)
	message = strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(message)
	redacted := hookspool.Redact(map[string]any{"detail": message})["detail"]
	message, _ = redacted.(string)
	return truncate(strings.TrimSpace(message), 512)
}

func safeLogValue(value any) string {
	s, _ := value.(string)
	s = strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(s)
	return truncate(s, 128)
}

func (jr *jobRunner) currentSessionID() string {
	jr.procMu.Lock()
	defer jr.procMu.Unlock()
	return jr.sessionID
}

func (jr *jobRunner) currentProviderError() string {
	jr.procMu.Lock()
	defer jr.procMu.Unlock()
	return jr.providerError
}

func (jr *jobRunner) currentCapacityFailure() (provider.CapacityFailure, bool) {
	jr.procMu.Lock()
	defer jr.procMu.Unlock()
	if jr.capacityFailure == nil {
		return provider.CapacityFailure{}, false
	}
	return *jr.capacityFailure, true
}

func (jr *jobRunner) effectiveCapacityResumePolicy() string {
	if jr.job.ProviderCapacityResumePolicy == v1.CapacityResumeAutomatic {
		return string(v1.CapacityResumeAutomatic)
	}
	return string(v1.CapacityResumeManual)
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
