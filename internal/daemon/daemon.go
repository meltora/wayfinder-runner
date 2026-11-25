package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/capability"
	"github.com/example/app-runner/internal/collector"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/egresspolicy"
	"github.com/example/app-runner/internal/hookspool"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/lease"
	"github.com/example/app-runner/internal/previewhost"
	"github.com/example/app-runner/internal/previewlifecycle"
	"github.com/example/app-runner/internal/previewobserver"
	"github.com/example/app-runner/internal/repo"
	"github.com/example/app-runner/internal/runlock"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/sourcepublication"
	"github.com/example/app-runner/internal/supervisor"
	"github.com/example/app-runner/internal/transport"
	"github.com/example/app-runner/internal/updateipc"
	"github.com/example/app-runner/internal/validation"
)

var Version = "0.3.1"

const capabilityRefreshInterval = 5 * time.Minute

const daemonJobSlots = 1

type Daemon struct {
	agentConfigScanState
	cfg                  config.Config
	store                *identity.Store
	runnerID             string
	journal              *journal.Journal
	runLock              *runlock.Lock
	client               *transport.Client
	paused               atomic.Bool
	logger               *slog.Logger
	providerVersionCache *capability.ProviderVersionCache

	subscriptionConfigured func(string) bool

	leases         *lease.Manager
	recoveredLease atomic.Bool
	collectors     *collector.Store
	uploader       *artifactUploader
	supervisor     supervisor.Supervisor

	userMapping          isolation.UserMapping
	ownership            isolation.Ownership
	removeJobContainer   containerRemover
	verifiedProviderCLIs map[providerCLIKey]string

	jobNetworkAttestation isolation.DockerNetworkAttestation
	acquireJobEgressRules jobEgressRulesAcquirer

	jobMu                                                 sync.Mutex
	activeJob                                             *jobRunner
	workIntakeProposalActive                              bool
	updateDrain                                           *pendingUpdate
	updateWake                                            chan struct{}
	updateRunMu                                           sync.Mutex
	updateIPC                                             updateIPC
	updateRestored                                        bool
	updateExpiredAtStartup                                *pendingUpdate
	controlReady                                          atomic.Bool
	rotationMu                                            sync.Mutex
	now                                                   func() time.Time
	previewLeaseNow                                       func() time.Time
	leaseRenewalNow                                       func() time.Time
	leaseMonitorInterval                                  time.Duration
	previewEvidenceRedeployTimeout                        time.Duration
	rotationFault                                         func(point string) error
	configureRepositoryManager                            func(*repo.Manager)
	promotedTLS                                           atomic.Pointer[tls.Config]
	rotationCommands                                      sync.WaitGroup
	commandMu                                             sync.Mutex
	commandOwner                                          string
	sourcePublicationPublisher                            sourcePublicationPublisher
	sourcePublicationCommandMu                            sync.Mutex
	sourcePublicationCommandActive                        map[string]struct{}
	sourcePublicationCommands                             sync.WaitGroup
	previewObserver                                       previewObserver
	previewObservationCommandMu                           sync.Mutex
	previewObservationCommandActive                       map[string]struct{}
	previewObservationCommands                            sync.WaitGroup
	previewLifecycleController                            previewLifecycleController
	previewLifecycleCommandMu                             sync.Mutex
	previewLifecycleCommandActive                         map[string]struct{}
	previewLifecycleKeyActive                             map[string]struct{}
	previewLifecycleCommands                              sync.WaitGroup
	previewHostController                                 previewhost.Controller
	previewDeployCommandMu                                sync.Mutex
	previewDeployCommandActive                            map[string]struct{}
	previewDeployKeyActive                                map[string]struct{}
	previewDeployCommands                                 sync.WaitGroup
	previewJobDeploys                                     previewJobDeployRegistry
	previewHostMu                                         sync.Mutex
	previewHostReapMu                                     sync.Mutex
	previewHostEmergencyGeneration                        uint64
	previewHostDeployCancels                              map[string]context.CancelFunc
	previewHostSweeping                                   map[string]struct{}
	previewHostRecovering                                 map[string]uint64
	previewHostWake                                       chan struct{}
	hostMemory                                            func() uint64
	previewHostGOOS                                       func() string
	previewTunnel                                         previewTunnel
	previewTunnelAdvertised                               atomic.Bool
	previewTunnelMu                                       sync.Mutex
	previewTunnelCancel                                   context.CancelFunc
	previewTunnelDone                                     chan struct{}
	reviewerIdentityVerifier                              reviewerIdentityVerifier
	reviewerIdentityVerificationCommandMu                 sync.Mutex
	reviewerIdentityVerificationCommandActive             map[string]struct{}
	reviewerIdentityVerificationCommands                  sync.WaitGroup
	providerReviewerAssigner                              providerReviewerAssigner
	providerReviewerAssignmentCommandMu                   sync.Mutex
	providerReviewerAssignmentCommandActive               map[string]struct{}
	providerReviewerAssignmentCommands                    sync.WaitGroup
	providerReviewerAssignmentReconciler                  providerReviewerAssignmentReconciler
	providerReviewerAssignmentReconciliationCommandMu     sync.Mutex
	providerReviewerAssignmentReconciliationCommandActive map[string]struct{}
	providerReviewerAssignmentReconciliationCommands      sync.WaitGroup
	providerReviewObserver                                providerReviewObserver
	providerReviewObservationCommandMu                    sync.Mutex
	providerReviewObservationCommandActive                map[string]struct{}
	providerReviewObservationCommands                     sync.WaitGroup
	providerReviewFeedbackContentReader                   providerReviewFeedbackContentReader
	providerReviewFeedbackContentCommandMu                sync.Mutex
	providerReviewFeedbackContentCommandActive            map[string]struct{}
	providerReviewFeedbackContentCommands                 sync.WaitGroup
	providerPullRequestRunner                             providerPullRequestRunner
	providerPullRequestCommandMu                          sync.Mutex
	providerPullRequestCommandActive                      map[string]struct{}
	providerPullRequestCommands                           sync.WaitGroup
	workIntakeProposalGenerator                           workIntakeProposalGenerator
	workIntakeProposalCommandMu                           sync.Mutex
	workIntakeProposalCommandActive                       map[string]struct{}
	workIntakeProposalCommands                            sync.WaitGroup

	eventSink   func(ctx context.Context, payload any) error
	httpMu      sync.RWMutex
	controlHTTP *http.Client

	hotMu                       sync.RWMutex
	sourcePublicationHTTPClient *http.Client
	configurationReloader       ConfigurationReloader
	configurationReloadWait     func(failures int) time.Duration
	configurationReloadFailing  bool
	configurationApplyFailing   bool
	configurationRestartDigest  string
	pendingAgentPackEvictions   []string
	agentPackTrust              *agentPackTrust
}

// ExternalOperationDependencies narrows test transport injection to external
// provider operations and preview observation. Production callers use New,
// which keeps the hardened default HTTP client, resolver, and dialer.
type ExternalOperationDependencies struct {
	SourcePublicationHTTPClient *http.Client
	PreviewObserver             previewobserver.Dependencies
}

func New(cfg config.Config, logger *slog.Logger) (*Daemon, error) {
	return NewWithExternalOperationDependencies(cfg, logger, ExternalOperationDependencies{})
}

func NewWithExternalOperationDependencies(
	cfg config.Config,
	logger *slog.Logger,
	dependencies ExternalOperationDependencies,
) (*Daemon, error) {
	cfg = config.Normalize(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid runner configuration: %w", err)
	}
	var publicationPublisher sourcePublicationPublisher
	var identityVerifier reviewerIdentityVerifier
	var reviewerAssigner providerReviewerAssigner
	var reviewerAssignmentReconciler providerReviewerAssignmentReconciler
	var reviewObserver providerReviewObserver
	var feedbackContentReader providerReviewFeedbackContentReader
	var pullRequestRunner providerPullRequestRunner
	if len(cfg.SourcePublicationInstallations) != 0 {
		registry, err := sourcepublication.NewRegistry(cfg.SourcePublicationInstallations, dependencies.SourcePublicationHTTPClient)
		if err != nil {
			return nil, fmt.Errorf("initialize source publication credentials: %w", err)
		}
		publicationPublisher = registry
		identityVerifier = registry
		reviewerAssigner = registry
		reviewerAssignmentReconciler = registry
		reviewObserver = registry
		feedbackContentReader = registry
		pullRequestRunner = registry
	}
	var localPreviewObserver previewObserver
	if len(cfg.PreviewObservationProfiles) != 0 {
		registry, err := previewobserver.NewRegistry(cfg.PreviewObservationProfiles, dependencies.PreviewObserver)
		if err != nil {
			return nil, fmt.Errorf("initialize preview observer: %w", err)
		}
		localPreviewObserver = registry
	}
	var localPreviewLifecycleController previewLifecycleController
	if len(cfg.PreviewLifecycleControllers) != 0 {
		registry, err := previewlifecycle.NewRegistry(cfg.PreviewLifecycleControllers, previewlifecycle.Dependencies{})
		if err != nil {
			return nil, fmt.Errorf("initialize preview lifecycle controller: %w", err)
		}
		localPreviewLifecycleController = registry
	}
	if logger == nil {
		logger = slog.Default()
	}
	store := identity.NewStore(cfg.StateDir)
	lock, err := runlock.Acquire(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	if !store.Enrolled() {
		_ = lock.Close()
		return nil, fmt.Errorf("runner not enrolled; run `our-runner enroll` first")
	}
	runnerID, err := loadRunnerID(cfg.StateDir)
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("load runner ID: %w", err)
	}
	j, err := journal.Open(filepath.Join(cfg.StateDir, "journal.db"))
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	j.SetMaxBytes(int64(cfg.JournalMaxMB) << 20)
	// Artifact receipts are authenticated by the runner gateway. A separate
	// control-plane HTTP endpoint may serve Agent Packs without owning intake.
	httpURL := deriveHTTPURL(cfg.ControlPlaneURL)
	leaseManager := lease.NewManager()
	recoveredLease := false
	cleanupJobs := true
	persistedLease, err := j.LoadLease(context.Background())
	if err != nil {
		_ = j.Close()
		_ = lock.Close()
		return nil, fmt.Errorf("load durable lease: %w", err)
	}
	if persistedLease != nil {
		if persistedLease.ExpiresAt.After(time.Now()) {
			if err := leaseManager.Acquire(*persistedLease); err != nil {
				_ = j.Close()
				_ = lock.Close()
				return nil, fmt.Errorf("restore durable lease: %w", err)
			}
			recoveredLease = true
			cleanupJobs = false
		} else if err := j.ClearLease(context.Background(), persistedLease.AttemptID); err != nil {
			_ = j.Close()
			_ = lock.Close()
			return nil, fmt.Errorf("clear expired durable lease: %w", err)
		}
	}
	var workIntakeNetworkAttestation isolation.DockerNetworkAttestation
	var workIntakeEffectivePolicy egresspolicy.Binding
	var userMapping isolation.UserMapping
	var ownership isolation.Ownership
	var verifiedProviderCLIs map[providerCLIKey]string
	if cfg.Isolation == "docker" {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		info := isolation.ProbeDocker(cleanupCtx)
		if refusal := dockerDaemonRefusal(info, runtime.GOOS); refusal != nil {
			cleanupCancel()
			_ = j.Close()
			_ = lock.Close()
			return nil, refusal
		}
		if cfg.JobNetwork == "restricted" {
			attestation, attestErr := isolation.AttestDockerNetwork(cleanupCtx, cfg.JobNetworkName, cfg.EgressPolicyID)
			if attestErr != nil {
				cleanupCancel()
				_ = j.Close()
				_ = lock.Close()
				return nil, attestErr
			}
			workIntakeNetworkAttestation = attestation
			if cfg.WorkIntakeProposal.Enabled {
				policyClient := egresspolicy.NewClient(cfg.EgressPolicyVerifierSocket)
				binding, policyErr := policyClient.Attest(
					cleanupCtx, attestation.ID, attestation.Name, attestation.PolicyID,
				)
				if policyErr != nil {
					cleanupCancel()
					_ = j.Close()
					_ = lock.Close()
					return nil, fmt.Errorf("attest effective consultation egress policy: %w", policyErr)
				}
				workIntakeEffectivePolicy = binding
			}
		}
		cleanupErr := isolation.RemoveRunnerContainers(cleanupCtx, runnerID)
		cleanupCancel()
		if cleanupErr != nil {
			_ = j.Close()
			_ = lock.Close()
			return nil, fmt.Errorf("cleanup runner containers: %w", cleanupErr)
		}
		userMapping, ownership, err = resolveDockerJobUser(cfg, runnerID)
		if err != nil {
			_ = j.Close()
			_ = lock.Close()
			return nil, err
		}
		verifiedProviderCLIs, err = verifyDockerProviderCLIs(cfg, runnerID, userMapping, logger)
		if err != nil {
			_ = j.Close()
			_ = lock.Close()
			return nil, err
		}
		if err := verifyDockerCheckImages(context.Background(), cfg.Checks); err != nil {
			_ = j.Close()
			_ = lock.Close()
			return nil, err
		}
	} else {
		warnUnusedCheckImages(cfg.Checks, logger)
	}
	if err := recoverStartupJobState(cfg.StateDir, cleanupJobs, ownershipReclaimer(ownership, runnerID)); err != nil {
		_ = j.Close()
		_ = lock.Close()
		return nil, err
	}
	trust, err := openAgentPackTrust(cfg)
	if err != nil {
		_ = j.Close()
		_ = lock.Close()
		return nil, fmt.Errorf("platform trust: %w", err)
	}
	updateClient := updateipc.NewClient(cfg.UpdateSocketPath)
	updateClient.ExpectedServerUID = 0
	d := &Daemon{
		cfg: cfg, store: store, runnerID: runnerID, journal: j, runLock: lock, logger: logger,
		commandOwner:                         "process-" + newEventID(),
		providerVersionCache:                 capability.NewProviderVersionCache(),
		now:                                  time.Now,
		leases:                               leaseManager,
		collectors:                           collector.NewStore(cfg.StateDir),
		uploader:                             newArtifactUploader(httpURL),
		supervisor:                           nil,
		userMapping:                          userMapping,
		ownership:                            ownership,
		verifiedProviderCLIs:                 verifiedProviderCLIs,
		updateWake:                           make(chan struct{}, 1),
		updateIPC:                            updateClient,
		sourcePublicationPublisher:           publicationPublisher,
		sourcePublicationHTTPClient:          dependencies.SourcePublicationHTTPClient,
		previewObserver:                      localPreviewObserver,
		previewLifecycleController:           localPreviewLifecycleController,
		reviewerIdentityVerifier:             identityVerifier,
		providerReviewerAssigner:             reviewerAssigner,
		providerReviewerAssignmentReconciler: reviewerAssignmentReconciler,
		providerReviewObserver:               reviewObserver,
		providerReviewFeedbackContentReader:  feedbackContentReader,
		providerPullRequestRunner:            pullRequestRunner,
		agentPackTrust:                       trust,
	}
	if workIntakeNetworkAttestation.ID != "" {
		d.jobNetworkAttestation = workIntakeNetworkAttestation
		d.acquireJobEgressRules = newJobEgressRulesAcquirer(cfg, workIntakeNetworkAttestation)
	}
	d.applyConfiguredAgentPackTrust(context.Background(), cfg.AgentPackTrustedKeySet)
	d.collectors.SetMaxSpoolBytes(int64(cfg.ArtifactSpoolMB) << 20)
	d.recoveredLease.Store(recoveredLease)
	d.supervisor, err = selectSupervisor(cfg, logger)
	if err != nil {
		_ = j.Close()
		_ = lock.Close()
		return nil, err
	}
	if cfg.WorkIntakeProposal.Enabled {
		// Docker is the hardened execution boundary for consultation. Starting
		// the Docker CLI directly also keeps the raw prompt exclusively on stdin
		// instead of persisting it in a tmux/herdr command specification.
		d.workIntakeProposalGenerator = newProviderWorkIntakeProposalGenerator(
			cfg, runnerID, supervisor.NewDirectProcess(), workIntakeNetworkAttestation, workIntakeEffectivePolicy,
			userMapping, ownership,
		)
	}
	paused, err := ReadPausedState(cfg.StateDir)
	if err != nil {
		_ = j.Close()
		_ = lock.Close()
		return nil, fmt.Errorf("inspect persistent pause state: %w", err)
	}
	d.paused.Store(paused)
	pending, expired, err := restorePendingUpdate(cfg.StateDir, time.Now())
	if err != nil {
		_ = j.Close()
		_ = lock.Close()
		return nil, fmt.Errorf("restore pending update: %w", err)
	}
	d.updateDrain = pending
	d.updateRestored = pending != nil
	d.updateExpiredAtStartup = expired
	d.previewHostWake = make(chan struct{}, 1)
	if cfg.PreviewHosting.Enabled {
		controller, err := d.newPreviewHostController()
		if err != nil {
			_ = j.Close()
			_ = lock.Close()
			return nil, fmt.Errorf("initialize preview host controller: %w", err)
		}
		d.previewHostController = controller
	}
	leaseManager.SetClock(d.leaseRenewalClock)
	return d, nil
}

func selectSupervisor(cfg config.Config, logger *slog.Logger) (supervisor.Supervisor, error) {
	herdrSup := func() supervisor.Supervisor {
		h := supervisor.NewHerdr("")
		if h.Available() {
			return h
		}
		return nil
	}
	tmuxSup := func() supervisor.Supervisor {
		t := supervisor.NewTmux(filepath.Join(cfg.StateDir, "tmux"))
		if t.Available() {
			return t
		}
		return nil
	}
	switch cfg.Supervisor {
	case "herdr":
		if h := herdrSup(); h != nil {
			return h, nil
		}
		return nil, fmt.Errorf("herdr supervisor configured but its Unix socket is unavailable")
	case "tmux":
		if t := tmuxSup(); t != nil {
			return t, nil
		}
		return nil, fmt.Errorf("tmux supervisor configured but tmux is unavailable")
	case "", "auto":
		if h := herdrSup(); h != nil {
			logger.Info("supervisor: herdr")
			return h, nil
		}
		if t := tmuxSup(); t != nil {
			logger.Info("supervisor: tmux")
			return t, nil
		}
		logger.Info("supervisor: direct")
	}
	return supervisor.NewDirectProcess(), nil
}

func (d *Daemon) Run(ctx context.Context) error {
	defer func() {
		d.stopAgentConfigScan(context.Canceled)
		d.agentConfigScanCommands.Wait()
		d.sourcePublicationCommands.Wait()
		d.previewObservationCommands.Wait()
		d.previewLifecycleCommands.Wait()
		d.previewDeployCommands.Wait()
		d.reviewerIdentityVerificationCommands.Wait()
		d.providerReviewerAssignmentCommands.Wait()
		d.providerReviewerAssignmentReconciliationCommands.Wait()
		d.providerReviewObservationCommands.Wait()
		d.providerReviewFeedbackContentCommands.Wait()
		d.providerPullRequestCommands.Wait()
		d.workIntakeProposalCommands.Wait()
		d.rotationCommands.Wait()
		_ = d.journal.Close()
		_ = d.runLock.Close()
	}()

	d.applyAgentPackRevocations()
	if err := d.startupRotation(ctx); err != nil {
		return err
	}
	tlsCfg, err := d.currentTLSConfig()
	if err != nil {
		return err
	}
	d.resumePromotedIdentity(tlsCfg)
	d.setControlTLS(tlsCfg)

	d.client = transport.NewClient(d.cfg.ControlPlaneURL, d.runnerID, Version, tlsCfg, d)
	d.client.ReplayUnacked = d.replayUnacked
	d.client.HeartbeatPayload = d.heartbeatPayload
	d.previewTunnel = d.newPreviewTunnel(tlsCfg)
	defer d.stopPreviewTunnel()

	spoolCtx, stopSpool := context.WithCancel(ctx)
	defer stopSpool()
	d.sweepJobPreviewNetworksAtStartup(spoolCtx)
	go d.drainHookSpool(spoolCtx)
	go d.drainArtifactUploads(spoolCtx)
	go d.journalMaintenance(spoolCtx)
	go d.pauseStateLoop(spoolCtx)
	go d.rotationLoop(spoolCtx)
	go d.updateLoop(spoolCtx)
	go d.capabilityRefreshLoop(spoolCtx, capabilityRefreshInterval)
	go d.previewHostExpiryLoop(spoolCtx)
	if d.configurationReloader != nil {
		go d.configurationReloadLoop(spoolCtx)
	}

	d.logger.Info("starting daemon", "runner_id", d.runnerID, "control_plane", d.cfg.ControlPlaneURL)
	return d.client.Run(ctx)
}

func (d *Daemon) journalMaintenance(ctx context.Context) {
	run := func() {
		cutoff := time.Now().Add(-time.Duration(d.cfg.JournalRetentionDays) * 24 * time.Hour)
		for range 10 {
			removed, err := d.journal.Prune(ctx, cutoff, 1000)
			if err != nil {
				if ctx.Err() == nil {
					d.logger.Warn("journal retention", "err", err)
				}
				return
			}
			if removed < 2000 {
				break
			}
		}
		if err := d.journal.Checkpoint(ctx); err != nil && ctx.Err() == nil {
			d.logger.Warn("journal checkpoint", "err", err)
		}
	}
	run()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (d *Daemon) drainHookSpool(ctx context.Context) {
	spool, err := hookspool.NewSpool(filepath.Join(d.cfg.StateDir, "hook-spool"))
	if err != nil {
		d.logger.Error("hook spool init", "err", err)
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := spool.Process(100, func(ev hookspool.Event) error {
				return d.sendEvent(ctx, hookEventPayload(ev))
			}); err != nil {
				d.logger.Warn("hook spool delivery", "err", err)
			}
		}
	}
}

func (d *Daemon) OnConnected(ctx context.Context) {
	d.controlReady.Store(true)
	d.logger.Info("connected to control plane")
	d.confirmRotatedIdentity()
	if err := d.publishCapability(ctx); err != nil {
		d.logger.Error("publish capability manifest", "err", err)
	}
	d.startPreviewTunnel(ctx)
	if d.recoveredLease.Load() {
		if current := d.leases.Current(); current != nil {
			if err := d.sendEvent(ctx, map[string]any{
				"kind": "lease_reconciliation_required", "job_id": current.JobID,
				"attempt_id": current.AttemptID, "fencing_token": current.FencingToken,
				"lease_expires_at": current.ExpiresAt,
			}); err != nil {
				d.logger.Error("send lease reconciliation event", "err", err)
			}
		}
	}
	if d.updateExpiredAtStartup != nil {
		expired := d.updateExpiredAtStartup
		if err := d.sendEventWithID(ctx, updateEventID(d.runnerID, expired.UpdateID, updatePhaseExpired), map[string]any{
			"kind": "runner_update", "update_id": expired.UpdateID, "phase": updatePhaseExpired, "error_code": "drain_deadline_exceeded",
		}); err != nil {
			d.logger.Warn("record expired startup update", "err", err)
		} else {
			d.updateExpiredAtStartup = nil
		}
	}
	d.jobMu.Lock()
	pending := d.updateDrain
	d.jobMu.Unlock()
	if pending != nil && !pending.AckAttempted {
		_ = d.sendCommandAck(ctx, pending.CommandID, v1.CommandAckAccepted, "")
		if err := d.markUpdateAckAttempted(pending.UpdateID); err != nil {
			d.logger.Warn("persist recovered update acknowledgement", "err", err)
		}
	}
	d.notifyStartupUpdateHealth(ctx)
	d.signalUpdate()
}

func (d *Daemon) publishCapability(ctx context.Context) error {
	d.hotMu.RLock()
	hot := hotConfiguration{
		repositories: d.cfg.Repositories, agentPackPublicKeys: d.cfg.AgentPackPublicKeys,
		labels: d.cfg.Labels, digest: d.cfg.RunnerConfigurationDigest,
	}
	services := d.sourcePublicationServicesLocked()
	d.hotMu.RUnlock()
	det := capability.New(d.runnerID, Version, hot.labels, hot.agentPackPublicKeys).
		WithProviderBinaries(d.cfg.ProviderBinaries).
		WithProviderEnvironment(d.cfg.ProviderEnvironment).
		WithProviderAPIKeys(d.cfg.ProviderAPIKeys).
		WithProviderVersionCache(d.providerVersionCache)
	if d.agentPackTrust != nil {
		det = det.WithAgentPackTrust(d.agentPackTrustCapability(hot.agentPackPublicKeys))
	}
	det.AgentConfigScanRepositoryIDs = d.agentConfigScanRepositoriesFrom(hot.repositories)
	det.RunnerConfigurationDigest = hot.digest
	if d.cfg.WorkIntakeProposal.Enabled && d.cfg.Isolation == "docker" && d.cfg.JobNetwork == "restricted" {
		det.WorkIntakeProposalHardened = true
		det.WorkIntakeProposalRestrictedEgress = true
		det.WorkIntakeProposalAdapter = d.cfg.WorkIntakeProposal.Adapter
		det.WorkIntakeProposalModel = d.cfg.WorkIntakeProposal.Model
		det.WorkIntakeProposalPromptTemplateDigest = workIntakeProposalPromptTemplateDigest()
		det.WorkIntakeProposalPolicyDigest = workIntakeProposalPolicyDigest()
		if d.workIntakeProposalGenerator != nil {
			digest, err := d.workIntakeProposalGenerator.ModelExecutionConfigDigest(ctx)
			if err != nil {
				d.logger.Warn("omit work intake proposal capability: execution binding unavailable", "err", err)
			} else {
				det.WorkIntakeProposalModelExecutionConfigDigest = digest
			}
		}
	}
	if services.publisher != nil {
		det.SourcePublicationProviders = services.publisher.ReadyProviders()
	}
	if d.previewObserver != nil {
		det.PreviewObservationProfiles = d.previewObserver.ReadyProfiles()
	}
	if d.previewLifecycleController != nil {
		det.PreviewLifecycleProfiles = d.previewLifecycleController.ReadyProfiles()
	}
	det.PreviewHosting = d.previewHostingCapability()
	det.PreviewTunnel = det.PreviewHosting != nil && d.previewTunnel != nil
	if services.reviewerIdentityVerifier != nil {
		det.ReviewerIdentityVerificationInstallations = services.reviewerIdentityVerifier.ReadyReviewerIdentityInstallations()
	}
	if services.providerReviewerAssigner != nil {
		det.ProviderReviewerAssignmentInstallations = services.providerReviewerAssigner.ReadyProviderReviewerAssignmentInstallations()
	}
	if services.providerReviewerAssignmentReconciler != nil {
		det.ProviderReviewerAssignmentReconciliationInstallations = services.providerReviewerAssignmentReconciler.ReadyProviderReviewerAssignmentReconciliationInstallations()
	}
	if services.providerReviewObserver != nil {
		det.ProviderReviewObservationInstallations = services.providerReviewObserver.ReadyProviderReviewObservationInstallations()
	}
	if services.providerReviewFeedbackContentReader != nil {
		det.ProviderReviewFeedbackContentInstallations = services.providerReviewFeedbackContentReader.ReadyProviderReviewFeedbackContentInstallations()
	}
	if services.providerPullRequestRunner != nil {
		det.ProviderPullRequestInstallations = services.providerPullRequestRunner.ReadyProviderPullRequestInstallations()
	}
	m := det.Collect(ctx)
	key, err := d.store.LoadKey()
	if err != nil {
		return fmt.Errorf("load runner signing key: %w", err)
	}
	if err := det.Sign(&m, key); err != nil {
		return fmt.Errorf("sign capability manifest: %w", err)
	}
	if err := d.sendEvent(ctx, map[string]any{
		"kind":     "capability_manifest",
		"manifest": m,
	}); err != nil {
		d.previewTunnelAdvertised.Store(false)
		return err
	}
	d.previewTunnelAdvertised.Store(det.PreviewTunnel)
	return nil
}

func (d *Daemon) capabilityRefreshLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !d.controlReady.Load() {
				continue
			}
			if err := d.publishCapability(ctx); err != nil && ctx.Err() == nil {
				d.logger.Warn("refresh capability manifest", "err", err)
			}
		}
	}
}

func (d *Daemon) OnEventAck(ctx context.Context, eventID string) {
	if err := validation.ID("event_id", eventID); err != nil {
		d.logger.Warn("invalid event ack", "err", err)
		return
	}
	if err := d.journal.AckKind(ctx, eventID, journal.KindEvent); err != nil {
		d.logger.Debug("event ack", "id", eventID, "err", err)
	}
}

func (d *Daemon) OnEventRejected(ctx context.Context, eventID, code string) error {
	if err := validation.ID("event_id", eventID); err != nil {
		return err
	}
	code = journal.NormalizeRejectionCode(code)
	d.logger.Debug("event rejected by control plane", "id", eventID, "code", code)
	d.previewJobDeployEventRejected(eventID, code)
	return d.journal.RecordEventRejection(ctx, eventID, code, time.Now())
}

func (d *Daemon) OnSessionRejected(ctx context.Context, code string) error {
	code = journal.NormalizeRejectionCode(code)
	d.logger.Debug("session rejected by control plane", "code", code)
	return d.journal.RecordSessionRejection(ctx, code, time.Now())
}

func (d *Daemon) sendEvent(ctx context.Context, payload any) error {
	return d.sendEventWithID(ctx, newEventID(), payload)
}

func (d *Daemon) sendEventWithID(ctx context.Context, eventID string, payload any) error {
	if err := validation.ID("event_id", eventID); err != nil {
		return err
	}
	if d.eventSink != nil {
		return d.eventSink(ctx, payload)
	}
	env := v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeEvent, ID: eventID,
		SentAt: time.Now(), RunnerID: d.runnerID, Payload: payload,
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if err := d.journal.Append(ctx, journal.Record{
		ID: env.ID, Kind: journal.KindEvent, Payload: raw, CreatedAt: time.Now(),
	}); err != nil {
		return err
	}
	if d.client == nil {
		return nil
	}
	if err := d.client.SendEvent(ctx, env); err != nil {
		d.logger.Debug("event durably queued for replay", "id", env.ID, "err", err)
	}
	return nil
}

// sendEventWithStableID durably records an event under a caller-derived stable
// ID before delivering it. An ID that is already journaled is neither appended
// again nor transmitted, so a retry after a crash between event publication
// and tool-result commit cannot create a second logical event.
func (d *Daemon) sendEventWithStableID(ctx context.Context, eventID string, payload any) (bool, error) {
	if err := validation.ID("event_id", eventID); err != nil {
		return false, err
	}
	env := v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeEvent, ID: eventID,
		SentAt: time.Now(), RunnerID: d.runnerID, Payload: payload,
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return false, err
	}
	inserted, err := d.journal.AppendIfNew(ctx, journal.Record{
		ID: env.ID, Kind: journal.KindEvent, Payload: raw, CreatedAt: time.Now(),
	})
	if err != nil {
		return false, err
	}
	if !inserted {
		return false, nil
	}
	if d.eventSink != nil {
		return true, d.eventSink(ctx, payload)
	}
	if d.client == nil {
		return true, nil
	}
	if err := d.client.SendEvent(ctx, env); err != nil {
		d.logger.Debug("event durably queued for replay", "id", env.ID, "err", err)
	}
	return true, nil
}

func (d *Daemon) OnDisconnected(err error) {
	d.controlReady.Store(false)
	d.stopPreviewTunnel()
	d.logger.Warn("disconnected", "err", err)
	d.fallBackFromRejectedRotation(err)
}

func (d *Daemon) heartbeatPayload(ctx context.Context) v1.HeartbeatPayload {
	state := "idle"
	activeJobs := 0
	if d.Paused() {
		state = "paused"
	}
	d.jobMu.Lock()
	if d.activeJob != nil {
		state = "busy"
		activeJobs = 1
	}
	if d.updateDrain != nil && !d.Paused() {
		state = "draining_for_update"
	}
	d.jobMu.Unlock()
	if d.recoveredLease.Load() && activeJobs == 0 {
		state = "reconciliation_required"
	}
	unacked, err := d.journal.UnackedCount(ctx)
	if err != nil {
		d.logger.Debug("heartbeat journal count", "err", err)
	}
	return v1.HeartbeatPayload{State: state, ActiveJobs: activeJobs, UnackedCount: unacked, JobSlots: daemonJobSlots}
}

func (d *Daemon) setControlTLS(tlsConfig *tls.Config) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true,
		TLSClientConfig: tlsConfig.Clone(),
	}
	client := &http.Client{
		Timeout:       5 * time.Minute,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	d.httpMu.Lock()
	d.controlHTTP = client
	d.httpMu.Unlock()
	d.uploader.SetHTTPClient(client)
}

func (d *Daemon) httpClientForURL(raw string, timeout time.Duration) *http.Client {
	base := d.cfg.ControlPlaneHTTPURL
	if base == "" {
		base = deriveHTTPURL(d.cfg.ControlPlaneURL)
	}
	targetURL, targetErr := url.Parse(raw)
	baseURL, baseErr := url.Parse(base)
	if targetErr == nil && baseErr == nil && targetURL.Scheme == baseURL.Scheme && targetURL.Host == baseURL.Host {
		d.httpMu.RLock()
		client := d.controlHTTP
		d.httpMu.RUnlock()
		if client != nil {
			return client
		}
	}
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (d *Daemon) OnCommand(ctx context.Context, env v1.Envelope) error {
	d.logger.Info("command received", "type", env.Type, "id", env.ID)
	if err := d.validateCommandEnvelope(env); err != nil {
		d.logger.Warn("command envelope rejected", "id", env.ID, "err", err)
		_ = d.sendCommandAck(ctx, env.ID, v1.CommandAckRejected, v1.CommandAckReasonInvalidCommand)
		return nil
	}
	d.commandMu.Lock()
	defer d.commandMu.Unlock()

	contentDigest, err := commandContentDigest(env.Payload)
	if err != nil {
		_ = d.sendCommandAck(ctx, env.ID, v1.CommandAckRejected, v1.CommandAckReasonInvalidCommand)
		return nil
	}
	payload, err := durableCommandRecordPayload(env, contentDigest)
	if err != nil {
		_ = d.sendCommandAck(ctx, env.ID, v1.CommandAckRejected, v1.CommandAckReasonInternalRejection)
		return nil
	}
	receipt, _, err := d.journal.ReceiveCommand(ctx, journal.Record{
		ID: env.ID, Kind: journal.KindCommand, Payload: payload, CreatedAt: time.Now(),
	}, contentDigest)
	if errors.Is(err, journal.ErrCommandContentConflict) {
		d.logger.Error("command ID reused with different content", "id", env.ID)
		_ = d.sendCommandAck(ctx, env.ID, v1.CommandAckRejected, v1.CommandAckReasonCommandConflict)
		return nil
	}
	if err != nil {
		d.logger.Error("journal command", "err", err)
		return fmt.Errorf("durably receive command %s: %w", env.ID, err)
	}
	if commandReceiptTerminal(receipt) {
		d.sendDurableCommandAck(ctx, receipt)
		d.recordCommandAckAttempt(env.Payload, receipt)
		return nil
	}
	effectMayHaveRun := receipt.State == journal.CommandProcessing
	cmd, decodeErr := decodeCommandPayload(env.Payload)
	asyncAgentConfigScan := decodeErr == nil && cmd.Command == v1.CmdAgentConfigScan
	if asyncAgentConfigScan && d.agentConfigScanCommandIsActive(env.ID) {
		return nil
	}
	asyncSourcePublication :=
		decodeErr == nil && cmd.Command == v1.CmdSourcePublication
	asyncPreviewObservation :=
		decodeErr == nil && cmd.Command == v1.CmdPreviewObservation
	asyncPreviewLifecycle := decodeErr == nil && cmd.Command == v1.CmdPreviewLifecycle
	asyncPreviewDeploy := decodeErr == nil && cmd.Command == v1.CmdPreviewDeploy
	asyncReviewerIdentityVerification :=
		decodeErr == nil && cmd.Command == v1.CmdReviewerIdentityVerification
	asyncProviderReviewerAssignment :=
		decodeErr == nil && cmd.Command == v1.CmdProviderReviewerAssignment
	asyncProviderReviewerAssignmentReconciliation := decodeErr == nil && cmd.Command == v1.CmdProviderReviewerAssignmentReconciliation
	asyncProviderReviewObservation := decodeErr == nil && cmd.Command == v1.CmdProviderReviewObservation
	asyncProviderReviewFeedbackContent := decodeErr == nil && cmd.Command == v1.CmdProviderReviewFeedbackContent
	asyncProviderPullRequest := decodeErr == nil && isProviderPullRequestCommand(cmd.Command)
	asyncWorkIntakeProposal := decodeErr == nil && cmd.Command == v1.CmdWorkIntakeProposal
	if asyncSourcePublication && d.sourcePublicationCommandIsActive(env.ID) {
		return nil
	}
	if asyncPreviewObservation && d.previewObservationCommandIsActive(env.ID) {
		return nil
	}
	if asyncPreviewLifecycle && d.previewLifecycleCommandIsActive(env.ID) {
		return nil
	}
	if asyncPreviewDeploy && d.previewDeployCommandIsActive(env.ID) {
		return nil
	}
	if asyncReviewerIdentityVerification && d.reviewerIdentityVerificationCommandIsActive(env.ID) {
		return nil
	}
	if asyncProviderReviewerAssignment && d.providerReviewerAssignmentCommandIsActive(env.ID) {
		return nil
	}
	if asyncProviderReviewerAssignmentReconciliation && d.providerReviewerAssignmentReconciliationCommandIsActive(env.ID) {
		return nil
	}
	if asyncProviderReviewObservation && d.providerReviewObservationCommandIsActive(env.ID) {
		return nil
	}
	if asyncProviderReviewFeedbackContent && d.providerReviewFeedbackContentCommandIsActive(env.ID) {
		return nil
	}
	if asyncProviderPullRequest && d.providerPullRequestCommandIsActive(env.ID) {
		return nil
	}
	if asyncWorkIntakeProposal && d.workIntakeProposalCommandIsActive(env.ID) {
		return nil
	}
	claimOwner := d.commandOwner + "-" + newEventID()
	receipt, claimed, err := d.journal.ClaimCommand(ctx, env.ID, claimOwner)
	if err != nil {
		d.logger.Error("claim durable command", "id", env.ID, "err", err)
		return fmt.Errorf("claim durable command %s: %w", env.ID, err)
	}
	if !claimed {
		if commandReceiptTerminal(receipt) {
			d.sendDurableCommandAck(ctx, receipt)
		}
		return nil
	}
	if err := validateCommandProviderCredential(env); err != nil {
		if effectMayHaveRun {
			d.logger.Warn("dropping unusable provider credential from recovered command", "id", env.ID, "err", err)
			env.ProviderCredential = nil
		} else {
			d.logger.Warn("command envelope rejected", "id", env.ID, "err", err)
			rejected, completeErr := d.journal.CompleteCommand(
				ctx, env.ID, claimOwner, string(v1.CommandAckRejected), v1.CommandAckReasonInvalidCommand,
			)
			if completeErr != nil {
				d.logger.Error("persist command outcome", "id", env.ID, "err", completeErr)
				return fmt.Errorf("persist command outcome %s: %w", env.ID, completeErr)
			}
			d.sendDurableCommandAck(ctx, rejected)
			return nil
		}
	}
	if asyncAgentConfigScan {
		d.agentConfigScanCommandStart(env.ID)
		go d.completeAgentConfigScanCommand(ctx, env, cmd, claimOwner, effectMayHaveRun)
		return nil
	}
	if asyncSourcePublication {
		if !d.sourcePublicationCommandStart(env.ID) {
			return nil
		}
		d.sourcePublicationCommands.Add(1)
		go d.completeSourcePublicationCommand(
			ctx,
			env,
			cmd,
			claimOwner,
			effectMayHaveRun,
		)
		return nil
	}
	if asyncPreviewObservation {
		if !d.previewObservationCommandStart(env.ID) {
			return nil
		}
		d.previewObservationCommands.Add(1)
		go d.completePreviewObservationCommand(
			ctx,
			env,
			cmd,
			claimOwner,
			effectMayHaveRun,
		)
		return nil
	}
	if asyncPreviewLifecycle {
		if !d.previewLifecycleCommandStart(env.ID) {
			return nil
		}
		d.previewLifecycleCommands.Add(1)
		go d.completePreviewLifecycleCommand(ctx, env, cmd, claimOwner, effectMayHaveRun)
		return nil
	}
	if asyncPreviewDeploy {
		if !d.previewDeployCommandStart(env.ID) {
			return nil
		}
		d.previewDeployCommands.Add(1)
		go d.completePreviewDeployCommand(ctx, env, cmd, claimOwner, effectMayHaveRun)
		return nil
	}
	if asyncReviewerIdentityVerification {
		if !d.reviewerIdentityVerificationCommandStart(env.ID) {
			return nil
		}
		d.reviewerIdentityVerificationCommands.Add(1)
		go d.completeReviewerIdentityVerificationCommand(ctx, env, cmd, claimOwner, effectMayHaveRun)
		return nil
	}
	if asyncProviderReviewerAssignment {
		if !d.providerReviewerAssignmentCommandStart(env.ID) {
			return nil
		}
		d.providerReviewerAssignmentCommands.Add(1)
		go d.completeProviderReviewerAssignmentCommand(ctx, env, cmd, claimOwner, effectMayHaveRun)
		return nil
	}
	if asyncProviderReviewerAssignmentReconciliation {
		if !d.providerReviewerAssignmentReconciliationCommandStart(env.ID) {
			return nil
		}
		d.providerReviewerAssignmentReconciliationCommands.Add(1)
		go d.completeProviderReviewerAssignmentReconciliationCommand(ctx, env, cmd, claimOwner, effectMayHaveRun)
		return nil
	}
	if asyncProviderReviewObservation {
		if !d.providerReviewObservationCommandStart(env.ID) {
			return nil
		}
		d.providerReviewObservationCommands.Add(1)
		go d.completeProviderReviewObservationCommand(ctx, env, cmd, claimOwner, effectMayHaveRun)
		return nil
	}
	if asyncProviderReviewFeedbackContent {
		if !d.providerReviewFeedbackContentCommandStart(env.ID) {
			return nil
		}
		d.providerReviewFeedbackContentCommands.Add(1)
		go d.completeProviderReviewFeedbackContentCommand(ctx, env, cmd, claimOwner, effectMayHaveRun)
		return nil
	}
	if asyncProviderPullRequest {
		if !d.providerPullRequestCommandStart(env.ID) {
			return nil
		}
		d.providerPullRequestCommands.Add(1)
		go d.completeProviderPullRequestCommand(ctx, env, cmd, claimOwner, effectMayHaveRun)
		return nil
	}
	if asyncWorkIntakeProposal {
		if !d.workIntakeProposalCommandStart(env.ID) {
			return nil
		}
		d.workIntakeProposalCommands.Add(1)
		go d.completeWorkIntakeProposalCommand(ctx, env, cmd, claimOwner, effectMayHaveRun)
		return nil
	}
	return d.completeClaimedCommand(
		ctx,
		env,
		cmd,
		decodeErr,
		claimOwner,
		effectMayHaveRun,
	)
}

func (d *Daemon) completeClaimedCommand(
	ctx context.Context,
	env v1.Envelope,
	cmd v1.CommandPayload,
	decodeErr error,
	claimOwner string,
	effectMayHaveRun bool,
) error {
	execErr := decodeErr
	if execErr == nil &&
		(!effectMayHaveRun || !d.commandEffectAlreadyApplied(cmd)) {
		execErr = d.executeCommand(ctx, env, cmd)
	}
	status := v1.CommandAckAccepted
	if execErr != nil {
		status = v1.CommandAckRejected
	}
	reasonCode := commandRejectionReason(execErr)
	receipt, err := d.journal.CompleteCommand(ctx, env.ID, claimOwner, string(status), reasonCode)
	if err != nil {
		d.logger.Error("persist command outcome", "id", env.ID, "err", err)
		return fmt.Errorf("persist command outcome %s: %w", env.ID, err)
	}
	d.sendDurableCommandAck(ctx, receipt)
	d.recordCommandAckAttempt(env.Payload, receipt)
	return nil
}

func (d *Daemon) completeSourcePublicationCommand(
	ctx context.Context,
	env v1.Envelope,
	cmd v1.CommandPayload,
	claimOwner string,
	effectMayHaveRun bool,
) {
	defer d.sourcePublicationCommands.Done()
	defer d.sourcePublicationCommandFinish(env.ID)
	if err := d.completeClaimedCommand(
		ctx,
		env,
		cmd,
		nil,
		claimOwner,
		effectMayHaveRun,
	); err != nil {
		d.logger.Error(
			"complete source publication command",
			"id",
			env.ID,
			"err",
			err,
		)
	}
}

func (d *Daemon) sourcePublicationCommandIsActive(commandID string) bool {
	d.sourcePublicationCommandMu.Lock()
	defer d.sourcePublicationCommandMu.Unlock()
	_, active := d.sourcePublicationCommandActive[commandID]
	return active
}

func (d *Daemon) sourcePublicationCommandStart(commandID string) bool {
	d.sourcePublicationCommandMu.Lock()
	defer d.sourcePublicationCommandMu.Unlock()
	if d.sourcePublicationCommandActive == nil {
		d.sourcePublicationCommandActive = make(map[string]struct{})
	}
	if _, active := d.sourcePublicationCommandActive[commandID]; active {
		return false
	}
	d.sourcePublicationCommandActive[commandID] = struct{}{}
	return true
}

func (d *Daemon) sourcePublicationCommandFinish(commandID string) {
	d.sourcePublicationCommandMu.Lock()
	delete(d.sourcePublicationCommandActive, commandID)
	d.sourcePublicationCommandMu.Unlock()
}

func (d *Daemon) executeCommand(ctx context.Context, env v1.Envelope, cmd v1.CommandPayload) error {
	if err := validateAgentConfigScanCommandBoundary(cmd); err != nil {
		return err
	}
	if err := validateJobControlCommandBoundary(cmd, time.Now()); err != nil {
		return err
	}
	if err := validateSourcePublicationCommandBoundary(cmd); err != nil {
		return err
	}
	if err := validateProviderReviewerAssignmentCommandBoundary(cmd); err != nil {
		return err
	}
	if err := validateProviderReviewerAssignmentReconciliationCommandBoundary(cmd); err != nil {
		return err
	}
	if err := validateProviderReviewObservationCommandBoundary(cmd); err != nil {
		return err
	}
	if err := validateProviderReviewFeedbackContentCommandBoundary(cmd); err != nil {
		return err
	}
	if err := validateProviderPullRequestCommandBoundary(cmd); err != nil {
		return err
	}
	if err := validateReviewFeedbackContinuationCommandBoundary(cmd); err != nil {
		return err
	}
	if err := validatePreviewObservationCommandBoundary(cmd); err != nil {
		return err
	}
	if err := validatePreviewLifecycleCommandBoundary(cmd); err != nil {
		return err
	}
	if err := validatePreviewDeployCommandBoundary(cmd); err != nil {
		return err
	}
	if err := validateReviewerIdentityVerificationCommandBoundary(cmd); err != nil {
		return err
	}
	if err := validateWorkIntakeProposalCommandBoundary(cmd); err != nil {
		return err
	}

	switch cmd.Command {
	case v1.CmdStartJob:
		if d.Paused() {
			return rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
		}
		if cmd.Job == nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return d.startJobWithProviderCredential(ctx, *cmd.Job, env.ProviderCredential)
	case v1.CmdReviewComment:
		if d.Paused() {
			return rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
		}
		if cmd.Job == nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		if cmd.Job.ResumeSessionID == "" {
			if cp, err := d.collectors.LatestCheckpoint(cmd.Job.JobID); err == nil && cp.ProviderSessionID != "" {
				cmd.Job.ResumeSessionID = cp.ProviderSessionID
			}
		}
		return d.startJobWithProviderCredential(ctx, *cmd.Job, env.ProviderCredential)
	case v1.CmdReviewFeedbackContinuation:
		if d.Paused() {
			return rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
		}
		return d.startReviewFeedbackContinuation(ctx, *cmd.Job, *cmd.ReviewFeedbackContinuation, env.ProviderCredential)
	case v1.CmdCancelJob:
		return d.cancelJob(ctx, cmd.AttemptID, cmd.Reason)
	case v1.CmdControlJob:
		return d.executeJobControl(ctx, env.ID, cmd.JobControlOperation())
	case v1.CmdRenewLease:
		if err := validation.ID("attempt_id", cmd.AttemptID); err != nil {
			return err
		}
		if cmd.FencingToken == 0 {
			return fmt.Errorf("fencing_token must be non-zero")
		}
		if err := d.leases.RenewUntil(cmd.AttemptID, cmd.FencingToken, cmd.LeaseExpiresAt); err != nil {
			return err
		}
		current := d.leases.Current()
		if current == nil {
			return fmt.Errorf("lease disappeared during renewal")
		}
		if err := d.journal.SaveLease(ctx, *current); err != nil {
			d.cancelActiveJob("lease renewal could not be persisted")
			return fmt.Errorf("persist lease renewal: %w", err)
		}
		return nil
	case v1.CmdDecisionResponse:
		return d.executeDecisionResponse(cmd)
	case v1.CmdEmergencyStop:
		d.stopAgentConfigScan(errAgentConfigScanStopped)
		if err := d.Pause(); err != nil {
			return err
		}
		if err := d.cancelJob(ctx, "", "emergency_stop"); err != nil {
			d.logger.Error("clear lease during emergency stop", "err", err)
		}
		if err := d.emergencyStopPreviewHosts(ctx); err != nil {
			d.logger.Error("destroy hosted previews during emergency stop", "err", err)
		}
		d.logger.Error("emergency stop received; jobs cancelled and runner paused")
		return nil
	case v1.CmdResumeRunner:
		if err := d.Resume(); err != nil {
			return err
		}
		d.logger.Info("runner resumed by control plane")
		return nil
	case v1.CmdRotateCert:
		d.startRequestedRotation(ctx)
		return nil
	case v1.CmdUpdate:
		return d.beginUpdateDrain(env.ID, cmd, time.Now())
	case v1.CmdSourcePublication:
		if d.Paused() {
			return rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
		}
		return d.executeSourcePublication(ctx, env.ID, *cmd.SourcePublication, env.ProviderCredential)
	case v1.CmdProviderReviewerAssignment:
		if d.Paused() {
			return rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
		}
		return d.executeProviderReviewerAssignment(ctx, env.ID, *cmd.ProviderReviewerAssignment, env.ProviderCredential)
	case v1.CmdProviderReviewerAssignmentReconciliation:
		if d.Paused() {
			return rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
		}
		return d.executeProviderReviewerAssignmentReconciliation(ctx, env.ID, *cmd.ProviderReviewerAssignmentReconciliation, env.ProviderCredential)
	case v1.CmdProviderReviewObservation:
		if d.Paused() {
			return rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
		}
		return d.executeProviderReviewObservation(ctx, env.ID, *cmd.ProviderReviewObservation, env.ProviderCredential)
	case v1.CmdProviderReviewFeedbackContent:
		if d.Paused() {
			return rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
		}
		return d.executeProviderReviewFeedbackContent(ctx, env.ID, *cmd.ProviderReviewFeedbackContent, env.ProviderCredential)
	case v1.CmdProviderPullRequestObservation, v1.CmdProviderPullRequestMerge:
		if d.Paused() {
			return rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
		}
		return d.executeProviderPullRequest(ctx, env.ID, *cmd.ProviderPullRequest, env.ProviderCredential)
	case v1.CmdPreviewObservation:
		if d.Paused() {
			return rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
		}
		return d.executePreviewObservation(ctx, env.ID, *cmd.PreviewObservation)
	case v1.CmdPreviewLifecycle:
		if d.Paused() && (!cmd.PreviewLifecycle.Hosted() || cmd.PreviewLifecycle.Action != v1.PreviewLifecycleDestroy) {
			return rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
		}
		return d.executePreviewLifecycle(ctx, env.ID, *cmd.PreviewLifecycle)
	case v1.CmdPreviewDeploy:
		return d.executePreviewDeploy(ctx, env.ID, *cmd.PreviewDeploy, env.ProviderCredential)
	case v1.CmdReviewerIdentityVerification:
		if d.Paused() {
			return rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
		}
		return d.executeReviewerIdentityVerification(ctx, env.ID, *cmd.ReviewerIdentityVerification, env.ProviderCredential)
	case v1.CmdWorkIntakeProposal:
		return d.executeWorkIntakeProposal(ctx, env.ID, *cmd.WorkIntakeProposal)
	}
	return rejectCommand(v1.CommandAckReasonUnsupportedCommand, nil)
}

func (d *Daemon) validateCommandEnvelope(env v1.Envelope) error {
	if env.Version != v1.ProtocolVersion || env.Type != v1.TypeCommand {
		return fmt.Errorf("unsupported command envelope")
	}
	if err := validation.ID("command_id", env.ID); err != nil {
		return err
	}
	if env.SentAt.IsZero() || time.Since(env.SentAt) > 10*time.Minute || time.Until(env.SentAt) > 2*time.Minute {
		return fmt.Errorf("command timestamp is outside the accepted window")
	}
	if env.RunnerID != "" && env.RunnerID != d.runnerID {
		return fmt.Errorf("command is addressed to a different runner")
	}
	if env.InReplyTo != "" {
		return fmt.Errorf("command cannot reply to another message")
	}
	if env.PreviewSecrets != nil {
		return fmt.Errorf("preview secrets are not accepted before P5c")
	}
	return nil
}

func validateCommandProviderCredential(env v1.Envelope) error {
	if env.ProviderCredential == nil {
		return nil
	}
	if err := env.ProviderCredential.Validate(time.Now()); err != nil {
		return v1.ErrInvalidProviderCredential
	}
	return nil
}

func (d *Daemon) sendCommandAck(
	ctx context.Context,
	commandID string,
	status v1.CommandAckStatus,
	reasonCode v1.CommandAckReasonCode,
) error {
	if d.client == nil || commandID == "" {
		return nil
	}
	payload := v1.CommandAckPayload{Status: status, ReasonCode: reasonCode}
	if err := v1.ValidateCommandAckPayload(payload); err != nil {
		return fmt.Errorf("validate command acknowledgement: %w", err)
	}
	ackCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return d.client.SendCommandAck(ackCtx, commandID, payload)
}

func (d *Daemon) Paused() bool { return d.paused.Load() }

func (d *Daemon) Pause() error {
	if err := SetPausedState(d.cfg.StateDir, true); err != nil {
		return err
	}
	d.paused.Store(true)
	d.stopAgentConfigScan(errAgentConfigScanStopped)
	return nil
}

func (d *Daemon) Resume() error {
	if err := SetPausedState(d.cfg.StateDir, false); err != nil {
		return err
	}
	d.paused.Store(false)
	return nil
}

func (d *Daemon) pauseStateLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			paused, err := ReadPausedState(d.cfg.StateDir)
			if err != nil {
				d.logger.Warn("read pause state", "err", err)
				continue
			}
			d.paused.Store(paused)
			if paused {
				d.stopAgentConfigScan(errAgentConfigScanStopped)
			}
		}
	}
}

func SetPausedState(stateDir string, paused bool) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	info, err := os.Lstat(stateDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("state directory must be a private real directory")
	}
	pausePath := filepath.Join(stateDir, "paused")
	if !paused {
		if err := os.Remove(pausePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove pause state: %w", err)
		}
		return safeio.SyncDirectory(stateDir)
	}
	tmp, err := os.CreateTemp(stateDir, ".paused-*")
	if err != nil {
		return fmt.Errorf("create pause state: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure pause state: %w", err)
	}
	if _, err := tmp.WriteString("1\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write pause state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync pause state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close pause state: %w", err)
	}
	if err := os.Rename(tmpPath, pausePath); err != nil {
		return fmt.Errorf("commit pause state: %w", err)
	}
	return safeio.SyncDirectory(stateDir)
}

func ReadPausedState(stateDir string) (bool, error) {
	info, err := os.Lstat(filepath.Join(stateDir, "paused"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() > 16 {
		return false, fmt.Errorf("pause state must be a private regular file")
	}
	return true, nil
}

func loadRunnerID(stateDir string) (string, error) {
	file, err := safeio.OpenRegular(stateDir, "runner-id")
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return "", err
	}
	if len(data) > 4096 {
		return "", fmt.Errorf("runner ID file is too large")
	}
	id := strings.TrimSpace(string(data))
	if err := validation.ID("runner_id", id); err != nil {
		return "", err
	}
	return id, nil
}

func ReadRunnerID(stateDir string) (string, error) { return loadRunnerID(stateDir) }

func WriteRunnerID(stateDir, id string) error {
	if err := validation.ID("runner_id", id); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(stateDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("state directory must be a real directory")
	}
	tmp, err := os.CreateTemp(stateDir, ".runner-id-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, filepath.Join(stateDir, "runner-id"))
}
