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
	"github.com/example/app-runner/internal/hookspool"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/lease"
	"github.com/example/app-runner/internal/runlock"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/supervisor"
	"github.com/example/app-runner/internal/transport"
	"github.com/example/app-runner/internal/updateipc"
	"github.com/example/app-runner/internal/validation"
)

var Version = "0.1.0"

const capabilityRefreshInterval = 5 * time.Minute

type Daemon struct {
	cfg      config.Config
	store    *identity.Store
	runnerID string
	journal  *journal.Journal
	runLock  *runlock.Lock
	client   *transport.Client
	paused   atomic.Bool
	logger   *slog.Logger

	leases         *lease.Manager
	recoveredLease atomic.Bool
	collectors     *collector.Store
	uploader       *artifactUploader
	supervisor     supervisor.Supervisor

	jobMu                  sync.Mutex
	activeJob              *jobRunner
	updateDrain            *pendingUpdate
	updateWake             chan struct{}
	updateRunMu            sync.Mutex
	updateIPC              updateIPC
	updateRestored         bool
	updateExpiredAtStartup *pendingUpdate
	controlReady           atomic.Bool
	rotationMu             sync.Mutex
	commandMu              sync.Mutex
	commandOwner           string

	eventSink   func(ctx context.Context, payload any) error
	httpMu      sync.RWMutex
	controlHTTP *http.Client
}

func New(cfg config.Config, logger *slog.Logger) (*Daemon, error) {
	cfg = config.Normalize(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid runner configuration: %w", err)
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
	httpURL := cfg.ControlPlaneHTTPURL
	if httpURL == "" {
		httpURL = deriveHTTPURL(cfg.ControlPlaneURL)
	}
	leaseManager := lease.NewManager()
	recoveredLease := false
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
		} else if err := j.ClearLease(context.Background(), persistedLease.AttemptID); err != nil {
			_ = j.Close()
			_ = lock.Close()
			return nil, fmt.Errorf("clear expired durable lease: %w", err)
		} else if err := cleanupOrphanedJobState(cfg.StateDir); err != nil {
			_ = j.Close()
			_ = lock.Close()
			return nil, fmt.Errorf("cleanup expired job state: %w", err)
		}
	} else if err := cleanupOrphanedJobState(cfg.StateDir); err != nil {
		_ = j.Close()
		_ = lock.Close()
		return nil, fmt.Errorf("cleanup orphaned job state: %w", err)
	}
	if cfg.Isolation == "docker" {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		info := isolation.ProbeDocker(cleanupCtx)
		if !info.Available || (runtime.GOOS == "linux" && !info.Rootless && !info.UsernsRemap) {
			cleanupCancel()
			_ = j.Close()
			_ = lock.Close()
			return nil, fmt.Errorf("production Docker isolation requires an available rootless or userns-remapped daemon")
		}
		if cfg.JobNetwork == "restricted" {
			if err := isolation.VerifyDockerNetwork(cleanupCtx, cfg.JobNetworkName, cfg.EgressPolicyID); err != nil {
				cleanupCancel()
				_ = j.Close()
				_ = lock.Close()
				return nil, err
			}
		}
		cleanupErr := isolation.RemoveRunnerContainers(cleanupCtx, runnerID)
		cleanupCancel()
		if cleanupErr != nil {
			_ = j.Close()
			_ = lock.Close()
			return nil, fmt.Errorf("cleanup runner containers: %w", cleanupErr)
		}
	}
	updateClient := updateipc.NewClient(cfg.UpdateSocketPath)
	updateClient.ExpectedServerUID = 0
	d := &Daemon{
		cfg: cfg, store: store, runnerID: runnerID, journal: j, runLock: lock, logger: logger,
		commandOwner: "process-" + newEventID(),
		leases:       leaseManager,
		collectors:   collector.NewStore(cfg.StateDir),
		uploader:     newArtifactUploader(httpURL),
		supervisor:   nil,
		updateWake:   make(chan struct{}, 1),
		updateIPC:    updateClient,
	}
	d.collectors.SetMaxSpoolBytes(int64(cfg.ArtifactSpoolMB) << 20)
	d.recoveredLease.Store(recoveredLease)
	d.supervisor, err = selectSupervisor(cfg, logger)
	if err != nil {
		_ = j.Close()
		_ = lock.Close()
		return nil, err
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
		_ = d.journal.Close()
		_ = d.runLock.Close()
	}()

	if shouldRotate, err := d.store.ShouldRotate(time.Now()); err == nil && shouldRotate {
		rotateCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err = d.rotateCert(rotateCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("startup certificate rotation: %w", err)
		}
	}
	certPEM, caPEM, err := d.store.LoadCert()
	if err != nil {
		return err
	}
	key, err := d.store.LoadKey()
	if err != nil {
		return err
	}
	keyDER, err := marshalKey(key)
	if err != nil {
		return err
	}
	tlsCfg, err := transport.TLSConfigFromPEM(certPEM, keyDER, caPEM)
	if err != nil {
		return err
	}
	d.setControlTLS(tlsCfg)

	d.client = transport.NewClient(d.cfg.ControlPlaneURL, d.runnerID, Version, tlsCfg, d)
	d.client.ReplayUnacked = d.replayUnacked
	d.client.HeartbeatPayload = d.heartbeatPayload

	spoolCtx, stopSpool := context.WithCancel(ctx)
	defer stopSpool()
	go d.drainHookSpool(spoolCtx)
	go d.drainArtifactUploads(spoolCtx)
	go d.journalMaintenance(spoolCtx)
	go d.pauseStateLoop(spoolCtx)
	go d.rotationLoop(spoolCtx)
	go d.updateLoop(spoolCtx)
	go d.capabilityRefreshLoop(spoolCtx, capabilityRefreshInterval)

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
	if err := d.publishCapability(ctx); err != nil {
		d.logger.Error("publish capability manifest", "err", err)
	}
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
	det := capability.New(d.runnerID, Version, d.cfg.Labels, d.cfg.AgentPackPublicKeys)
	m := det.Collect(ctx)
	key, err := d.store.LoadKey()
	if err != nil {
		return fmt.Errorf("load runner signing key: %w", err)
	}
	if err := det.Sign(&m, key); err != nil {
		return fmt.Errorf("sign capability manifest: %w", err)
	}
	return d.sendEvent(ctx, map[string]any{
		"kind":     "capability_manifest",
		"manifest": m,
	})
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
	d.logger.Warn("disconnected", "err", err)
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
	return v1.HeartbeatPayload{State: state, ActiveJobs: activeJobs, UnackedCount: unacked}
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
	payload, err := json.Marshal(env)
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

	cmd, decodeErr := decodeCommandPayload(env.Payload)
	execErr := decodeErr
	if execErr == nil {
		alreadyApplied := false
		if effectMayHaveRun {
			alreadyApplied = d.commandEffectAlreadyApplied(cmd)
		}
		if !alreadyApplied {
			execErr = d.executeCommand(ctx, env, cmd)
		}
	}
	status := v1.CommandAckAccepted
	if execErr != nil {
		status = v1.CommandAckRejected
	}
	reasonCode := commandRejectionReason(execErr)
	receipt, err = d.journal.CompleteCommand(ctx, env.ID, claimOwner, string(status), reasonCode)
	if err != nil {
		d.logger.Error("persist command outcome", "id", env.ID, "err", err)
		return fmt.Errorf("persist command outcome %s: %w", env.ID, err)
	}
	d.sendDurableCommandAck(ctx, receipt)
	d.recordCommandAckAttempt(env.Payload, receipt)
	return nil
}

func (d *Daemon) executeCommand(ctx context.Context, env v1.Envelope, cmd v1.CommandPayload) error {
	switch cmd.Command {
	case v1.CmdStartJob:
		if d.Paused() {
			return rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
		}
		if cmd.Job == nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return d.startJob(ctx, *cmd.Job)
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
		return d.startJob(ctx, *cmd.Job)
	case v1.CmdCancelJob:
		return d.cancelJob(ctx, cmd.AttemptID, cmd.Reason)
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
		if err := d.Pause(); err != nil {
			return err
		}
		if err := d.cancelJob(ctx, "", "emergency_stop"); err != nil {
			d.logger.Error("clear lease during emergency stop", "err", err)
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
		rotateCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err := d.rotateCert(rotateCtx); err != nil {
			return fmt.Errorf("rotation: %w", err)
		}
		return d.sendEvent(ctx, map[string]any{"kind": "cert_rotated"})
	case v1.CmdUpdate:
		return d.beginUpdateDrain(env.ID, cmd, time.Now())
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
