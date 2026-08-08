package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/updateipc"
	"github.com/example/app-runner/internal/validation"
)

const (
	pendingUpdateFile     = "pending-update.json"
	defaultUpdateDrain    = 30 * time.Minute
	maximumUpdateDrain    = 24 * time.Hour
	updatePhaseDraining   = "draining"
	updatePhasePreparing  = "preparing"
	updatePhasePrepared   = "prepared"
	updatePhaseActivating = "activating"
	updatePhaseHealthy    = "healthy"
	updatePhaseFailed     = "failed"
	updatePhaseExpired    = "expired"
	updatePhaseRolledBack = "rolled_back"
)

type updateIPC interface {
	Prepare(context.Context, string, string) (updateipc.Response, error)
	Activate(context.Context, string, string) (updateipc.Response, error)
	Health(context.Context, string, string, string) (updateipc.Response, error)
	Status(context.Context, string, string) (updateipc.Response, error)
}

type pendingUpdate struct {
	UpdateID      string    `json:"update_id"`
	CommandID     string    `json:"command_id"`
	DrainDeadline time.Time `json:"drain_deadline"`
	Phase         string    `json:"phase"`
	AckAttempted  bool      `json:"ack_attempted"`
}

func validateUpdateCommand(cmd v1.CommandPayload, now time.Time) error {
	if cmd.Command != v1.CmdUpdate {
		return fmt.Errorf("not an update command")
	}
	if err := validation.ID("update_id", cmd.UpdateID); err != nil {
		return err
	}
	if cmd.Job != nil || cmd.JobID != "" || cmd.AttemptID != "" || cmd.FencingToken != 0 ||
		!cmd.LeaseExpiresAt.IsZero() || cmd.Reason != "" || cmd.QuestionID != "" ||
		cmd.Decision != "" || cmd.DecidedBy != "" {
		return fmt.Errorf("update command contains fields outside the update capability boundary")
	}
	if cmd.DrainDeadline == nil {
		return nil
	}
	deadline := *cmd.DrainDeadline
	if deadline.IsZero() || !deadline.After(now) || deadline.After(now.Add(maximumUpdateDrain)) {
		return fmt.Errorf("drain_deadline must be within the next 24 hours")
	}
	return nil
}

func (d *Daemon) beginUpdateDrain(commandID string, cmd v1.CommandPayload, now time.Time) error {
	if err := validateUpdateCommand(cmd, now); err != nil {
		return err
	}
	if err := validation.ID("command_id", commandID); err != nil {
		return err
	}
	deadline := now.Add(defaultUpdateDrain)
	if cmd.DrainDeadline != nil {
		deadline = *cmd.DrainDeadline
	}
	pending := &pendingUpdate{
		UpdateID: cmd.UpdateID, CommandID: commandID, DrainDeadline: deadline.UTC(), Phase: updatePhaseDraining,
	}
	d.jobMu.Lock()
	defer d.jobMu.Unlock()
	if d.updateDrain != nil {
		if d.updateDrain.UpdateID == cmd.UpdateID {
			return nil
		}
		return fmt.Errorf("runner is already draining for update %s", d.updateDrain.UpdateID)
	}
	if err := persistPendingUpdate(d.cfg.StateDir, pending); err != nil {
		return fmt.Errorf("persist update drain: %w", err)
	}
	d.updateDrain = pending
	d.signalUpdateLocked()
	return nil
}

func (d *Daemon) markUpdateAckAttempted(updateID string) error {
	d.jobMu.Lock()
	defer d.jobMu.Unlock()
	if d.updateDrain == nil || d.updateDrain.UpdateID != updateID {
		return nil
	}
	if d.updateDrain.AckAttempted {
		return nil
	}
	next := *d.updateDrain
	next.AckAttempted = true
	if err := persistPendingUpdate(d.cfg.StateDir, &next); err != nil {
		return fmt.Errorf("persist update acknowledgement state: %w", err)
	}
	d.updateDrain = &next
	d.signalUpdateLocked()
	return nil
}

func (d *Daemon) transitionPendingUpdate(updateID, phase string) error {
	d.jobMu.Lock()
	defer d.jobMu.Unlock()
	if d.updateDrain == nil || d.updateDrain.UpdateID != updateID {
		return fmt.Errorf("update drain is no longer active")
	}
	next := *d.updateDrain
	next.Phase = phase
	if err := persistPendingUpdate(d.cfg.StateDir, &next); err != nil {
		return err
	}
	d.updateDrain = &next
	return nil
}

func (d *Daemon) clearPendingUpdate(updateID string) error {
	d.jobMu.Lock()
	defer d.jobMu.Unlock()
	if d.updateDrain == nil || d.updateDrain.UpdateID != updateID {
		return nil
	}
	if err := removePendingUpdate(d.cfg.StateDir); err != nil {
		return err
	}
	d.updateDrain = nil
	d.updateRestored = false
	return nil
}

func (d *Daemon) signalUpdateLocked() {
	if d.updateWake == nil {
		return
	}
	select {
	case d.updateWake <- struct{}{}:
	default:
	}
}

func (d *Daemon) signalUpdate() {
	d.jobMu.Lock()
	d.signalUpdateLocked()
	d.jobMu.Unlock()
}

func (d *Daemon) updateLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-d.updateWake:
		}
		if !d.controlReady.Load() {
			continue
		}
		if err := d.processPendingUpdate(ctx); err != nil && ctx.Err() == nil {
			d.logger.Warn("process pending update", "err", err)
		}
	}
}

func (d *Daemon) processPendingUpdate(ctx context.Context) error {
	d.updateRunMu.Lock()
	defer d.updateRunMu.Unlock()

	d.jobMu.Lock()
	if d.updateDrain == nil {
		d.jobMu.Unlock()
		return nil
	}
	pending := *d.updateDrain
	busy := d.activeJob != nil
	d.jobMu.Unlock()
	if pending.Phase == updatePhaseDraining && time.Now().After(pending.DrainDeadline) {
		return d.failPendingUpdate(ctx, pending.UpdateID, updatePhaseExpired, "drain_deadline_exceeded", errors.New("update drain deadline expired"))
	}
	if !pending.AckAttempted || busy {
		return nil
	}
	updateCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if d.updateIPC == nil {
		return d.failPendingUpdate(updateCtx, pending.UpdateID, updatePhaseFailed, "updater_unavailable", errors.New("updater IPC is unavailable"))
	}
	if pending.Phase == updatePhaseActivating {
		d.jobMu.Lock()
		restored := d.updateRestored
		d.jobMu.Unlock()
		if restored {
			d.notifyStartupUpdateHealth(updateCtx)
			d.jobMu.Lock()
			cleared := d.updateDrain == nil
			d.jobMu.Unlock()
			if cleared {
				return nil
			}
		}
		return d.reconcileActivatingUpdate(updateCtx, pending)
	}

	if pending.Phase != updatePhasePrepared {
		if err := d.transitionPendingUpdate(pending.UpdateID, updatePhasePreparing); err != nil {
			return err
		}
		if err := d.emitUpdatePhase(updateCtx, pending.UpdateID, updatePhasePreparing, ""); err != nil {
			return d.failPendingUpdate(updateCtx, pending.UpdateID, updatePhaseFailed, "event_journal_failed", err)
		}
		status, statusErr := d.updateIPC.Status(updateCtx, updateRequestID(d.runnerID, pending.UpdateID, updateipc.ActionStatus), pending.UpdateID)
		if statusErr == nil && status.OK && status.UpdateID == pending.UpdateID &&
			(status.Phase == updatePhaseHealthy || status.Phase == "committed") {
			if err := d.emitUpdatePhase(updateCtx, pending.UpdateID, updatePhaseHealthy, ""); err != nil {
				return err
			}
			return d.clearPendingUpdate(pending.UpdateID)
		}
		alreadyPrepared := statusErr == nil && status.OK && status.Phase == updatePhasePrepared && status.UpdateID == pending.UpdateID
		if !alreadyPrepared {
			response, err := d.updateIPC.Prepare(updateCtx, updateRequestID(d.runnerID, pending.UpdateID, updateipc.ActionPrepare), pending.UpdateID)
			if err != nil {
				return d.failPendingUpdate(updateCtx, pending.UpdateID, updatePhaseFailed, "updater_unavailable", err)
			}
			if !response.OK || response.Phase != updatePhasePrepared {
				code := response.ErrorCode
				if code == "" {
					code = "prepare_rejected"
				}
				return d.failPendingUpdate(updateCtx, pending.UpdateID, updatePhaseFailed, code, errors.New("updater rejected preparation"))
			}
		}
		if err := d.transitionPendingUpdate(pending.UpdateID, updatePhasePrepared); err != nil {
			return err
		}
		if err := d.emitUpdatePhase(updateCtx, pending.UpdateID, updatePhasePrepared, ""); err != nil {
			return d.failPendingUpdate(updateCtx, pending.UpdateID, updatePhaseFailed, "event_journal_failed", err)
		}
	}

	if err := d.transitionPendingUpdate(pending.UpdateID, updatePhaseActivating); err != nil {
		return err
	}
	// The activating event is durably recorded before the privileged updater is
	// allowed to stop this service.
	if err := d.emitUpdatePhase(updateCtx, pending.UpdateID, updatePhaseActivating, ""); err != nil {
		return d.failPendingUpdate(updateCtx, pending.UpdateID, updatePhaseFailed, "event_journal_failed", err)
	}
	response, err := d.updateIPC.Activate(updateCtx, updateRequestID(d.runnerID, pending.UpdateID, updateipc.ActionActivate), pending.UpdateID)
	if err != nil {
		// Delivery may have succeeded even if the response was lost. Preserve the
		// activating state so a restarted candidate can still prove health.
		return fmt.Errorf("activation outcome is pending reconciliation: %w", err)
	}
	if !response.OK || response.Phase != updatePhaseActivating {
		code := response.ErrorCode
		if code == "" {
			code = "activation_rejected"
		}
		return d.failPendingUpdate(updateCtx, pending.UpdateID, updatePhaseFailed, code, errors.New("updater rejected activation"))
	}
	return nil
}

func (d *Daemon) reconcileActivatingUpdate(ctx context.Context, pending pendingUpdate) error {
	status, err := d.updateIPC.Status(ctx, updateRequestID(d.runnerID, pending.UpdateID, updateipc.ActionStatus), pending.UpdateID)
	if err != nil {
		return fmt.Errorf("query activating update: %w", err)
	}
	if !status.OK {
		return fmt.Errorf("updater status rejected activating reconciliation")
	}
	if status.UpdateID != "" && status.UpdateID != pending.UpdateID {
		return fmt.Errorf("updater status identifies another transaction; update drain retained")
	}
	switch status.Phase {
	case updatePhasePrepared:
		response, err := d.updateIPC.Activate(ctx, updateRequestID(d.runnerID, pending.UpdateID, updateipc.ActionActivate), pending.UpdateID)
		if err != nil {
			return fmt.Errorf("retry activation: %w", err)
		}
		if !response.OK || response.Phase != updatePhaseActivating {
			return fmt.Errorf("retry activation was not accepted")
		}
		return nil
	case updatePhaseActivating, "stopping", "switching", "starting", "awaiting_health":
		return nil
	case updatePhaseHealthy, "committed":
		if status.UpdateID != pending.UpdateID {
			return fmt.Errorf("committed status did not identify the requested update")
		}
		if err := d.emitUpdatePhase(ctx, pending.UpdateID, updatePhaseHealthy, ""); err != nil {
			return err
		}
		return d.clearPendingUpdate(pending.UpdateID)
	case updatePhaseRolledBack:
		if status.UpdateID != pending.UpdateID {
			return fmt.Errorf("rollback status did not identify the requested update; drain retained")
		}
		return d.completeTerminalUpdate(ctx, pending.UpdateID, updatePhaseRolledBack, "activation_rolled_back")
	case "quarantined":
		if status.UpdateID != pending.UpdateID {
			return fmt.Errorf("quarantine status did not identify the requested update; drain retained")
		}
		return d.completeTerminalUpdate(ctx, pending.UpdateID, updatePhaseFailed, "release_quarantined")
	case "idle":
		return fmt.Errorf("updater activation outcome is ambiguous; update drain retained")
	default:
		return fmt.Errorf("updater returned an unknown activation phase")
	}
}

func (d *Daemon) completeTerminalUpdate(ctx context.Context, updateID, phase, code string) error {
	if validation.ID("error_code", code) != nil {
		return fmt.Errorf("invalid terminal update error code")
	}
	eventCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	// Keep the drain if the terminal outcome cannot be durably recorded. This
	// prevents scheduling from resuming with an unreported rollback.
	if err := d.emitUpdatePhase(eventCtx, updateID, phase, code); err != nil {
		return err
	}
	return d.clearPendingUpdate(updateID)
}

func (d *Daemon) failPendingUpdate(ctx context.Context, updateID, phase, code string, cause error) error {
	if validation.ID("error_code", code) != nil {
		code = "update_failed"
	}
	eventCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	// Resume scheduling only after the terminal outcome is durably journaled.
	// If journaling fails, retaining the drain makes the operation retryable and
	// prevents an unreported failure from disappearing during a crash.
	if eventErr := d.emitUpdatePhase(eventCtx, updateID, phase, code); eventErr != nil {
		return errors.Join(cause, eventErr)
	}
	return errors.Join(cause, d.clearPendingUpdate(updateID))
}

func (d *Daemon) emitUpdatePhase(ctx context.Context, updateID, phase, errorCode string) error {
	payload := map[string]any{"kind": "runner_update", "update_id": updateID, "phase": phase}
	if errorCode != "" {
		payload["error_code"] = errorCode
	}
	return d.sendEventWithID(ctx, updateEventID(d.runnerID, updateID, phase), payload)
}

func updateEventID(runnerID, updateID, phase string) string {
	digest := sha256.Sum256([]byte("our-runner-update-event-v1\x00" + runnerID + "\x00" + updateID + "\x00" + phase))
	return "upd-" + hex.EncodeToString(digest[:])
}

func updateRequestID(runnerID, updateID string, action updateipc.Action) string {
	digest := sha256.Sum256([]byte("our-runner-update-ipc-v1\x00" + runnerID + "\x00" + updateID + "\x00" + string(action)))
	return "req-" + hex.EncodeToString(digest[:])
}

func (d *Daemon) notifyStartupUpdateHealth(ctx context.Context) {
	d.jobMu.Lock()
	if !d.updateRestored || d.updateDrain == nil || d.updateDrain.Phase != updatePhaseActivating {
		d.jobMu.Unlock()
		return
	}
	pending := *d.updateDrain
	d.jobMu.Unlock()
	response, err := d.updateIPC.Health(ctx, updateRequestID(d.runnerID, pending.UpdateID, updateipc.ActionHealth), pending.UpdateID, Version)
	healthy := err == nil && response.OK && (response.Phase == updatePhaseHealthy || response.Phase == "committed")
	if !healthy {
		// Health can commit successfully while its response is lost. Reconcile
		// against root-owned durable status before retrying or clearing state.
		status, statusErr := d.updateIPC.Status(ctx, updateRequestID(d.runnerID, pending.UpdateID, updateipc.ActionStatus), pending.UpdateID)
		healthy = statusErr == nil && status.OK && status.UpdateID == pending.UpdateID &&
			(status.Phase == updatePhaseHealthy || status.Phase == "committed")
	}
	if !healthy {
		d.logger.Warn("updater health notification was not accepted", "update_id", pending.UpdateID)
		return
	}
	if err := d.emitUpdatePhase(ctx, pending.UpdateID, updatePhaseHealthy, ""); err != nil {
		d.logger.Warn("record update health", "update_id", pending.UpdateID, "err", err)
		return
	}
	if err := d.clearPendingUpdate(pending.UpdateID); err != nil {
		d.logger.Warn("clear healthy update state", "update_id", pending.UpdateID, "err", err)
	}
}

func persistPendingUpdate(stateDir string, pending *pendingUpdate) error {
	if err := validatePendingUpdate(pending); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(stateDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("state directory must be a private real directory")
	}
	raw, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, ".pending-update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
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
	if err := os.Rename(tmpPath, filepath.Join(stateDir, pendingUpdateFile)); err != nil {
		return err
	}
	return safeio.SyncDirectory(stateDir)
}

func loadPendingUpdate(stateDir string) (*pendingUpdate, error) {
	path := filepath.Join(stateDir, pendingUpdateFile)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	raw, err := safeio.ReadRegularFile(path, 4096, 0o077)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var pending pendingUpdate
	if err := decoder.Decode(&pending); err != nil {
		return nil, fmt.Errorf("decode pending update: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("decode pending update: trailing JSON")
	}
	if err := validatePendingUpdate(&pending); err != nil {
		return nil, err
	}
	return &pending, nil
}

func restorePendingUpdate(stateDir string, now time.Time) (*pendingUpdate, *pendingUpdate, error) {
	pending, err := loadPendingUpdate(stateDir)
	if err != nil || pending == nil {
		return pending, nil, err
	}
	if pending.Phase == updatePhaseDraining && !pending.DrainDeadline.After(now) {
		if err := removePendingUpdate(stateDir); err != nil {
			return nil, nil, err
		}
		return nil, pending, nil
	}
	if pending.Phase == updatePhaseDraining && pending.DrainDeadline.After(now.Add(maximumUpdateDrain)) {
		return nil, nil, fmt.Errorf("pending update deadline exceeds maximum drain window")
	}
	return pending, nil, nil
}

func validatePendingUpdate(pending *pendingUpdate) error {
	if pending == nil {
		return fmt.Errorf("pending update is required")
	}
	if err := validation.ID("update_id", pending.UpdateID); err != nil {
		return err
	}
	if err := validation.ID("command_id", pending.CommandID); err != nil {
		return err
	}
	if pending.DrainDeadline.IsZero() {
		return fmt.Errorf("pending update drain deadline is required")
	}
	switch pending.Phase {
	case updatePhaseDraining, updatePhasePreparing, updatePhasePrepared, updatePhaseActivating:
	default:
		return fmt.Errorf("pending update phase is invalid")
	}
	return nil
}

func removePendingUpdate(stateDir string) error {
	err := os.Remove(filepath.Join(stateDir, pendingUpdateFile))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return safeio.SyncDirectory(stateDir)
}
