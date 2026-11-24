package daemon

import (
	"context"
	"errors"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/capability"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/journal"
)

const previewHostExpiryInterval = 30 * time.Second

type previewHostIDLister interface {
	HostedPreviewIDs(ctx context.Context) ([]string, error)
}

type previewDeployAdmission struct {
	ctx        context.Context
	cancel     context.CancelFunc
	generation uint64
}

func (d *Daemon) previewHostMaxConcurrent() int {
	if d.cfg.PreviewHosting.MaxConcurrent > 0 {
		return d.cfg.PreviewHosting.MaxConcurrent
	}
	return config.DefaultPreviewHostingMaxConcurrent
}

func (d *Daemon) previewHostMemoryBudgetMB() int {
	if d.cfg.PreviewHosting.MemoryBudgetMB > 0 {
		return d.cfg.PreviewHosting.MemoryBudgetMB
	}
	total := capability.TotalMemory
	if d.hostMemory != nil {
		total = d.hostMemory
	}
	return int(total() / 2 / (1 << 20))
}

func (d *Daemon) admitPreviewDeploy(
	ctx context.Context,
	commandMessageID string,
	operation v1.PreviewDeployOperation,
	host journal.PreviewHost,
) (*previewDeployAdmission, error) {
	d.previewHostMu.Lock()
	if d.Paused() {
		d.previewHostMu.Unlock()
		return nil, rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
	}
	occupied, err := d.journal.PreviewHostsInStates(ctx,
		journal.PreviewHostDeploying, journal.PreviewHostDeployed, journal.PreviewHostDestroying)
	if err != nil {
		d.previewHostMu.Unlock()
		return nil, rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	memory := host.MemoryMB
	for _, existing := range occupied {
		memory += existing.MemoryMB
	}
	if len(occupied) >= d.previewHostMaxConcurrent() || memory > d.previewHostMemoryBudgetMB() {
		event := previewDeployResultEvent(operation, commandMessageID, previewDeployObservedAt(operation),
			v1.PreviewDeployOutcome{Kind: v1.PreviewDeployCapacityExhausted})
		raw, err := validatedPreviewDeployResult(event)
		if err != nil {
			d.previewHostMu.Unlock()
			return nil, rejectCommand(v1.CommandAckReasonInternalRejection, err)
		}
		host.State = journal.PreviewHostFailed
		host.Result = raw
		_, _, err = d.journal.InsertPreviewHost(ctx, host)
		d.previewHostMu.Unlock()
		if err != nil {
			return nil, previewHostInsertRejection(err)
		}
		return nil, d.sendPreviewDeployResult(ctx, event)
	}
	if _, inserted, err := d.journal.InsertPreviewHost(ctx, host); err != nil || !inserted {
		d.previewHostMu.Unlock()
		if err == nil {
			return nil, rejectCommand(v1.CommandAckReasonCommandConflict, nil)
		}
		return nil, previewHostInsertRejection(err)
	}
	deployCtx, cancel := context.WithDeadline(ctx, operation.LeaseExpiresAt)
	if d.previewHostDeployCancels == nil {
		d.previewHostDeployCancels = make(map[string]context.CancelFunc)
	}
	d.previewHostDeployCancels[operation.IdempotencyKey] = cancel
	admission := &previewDeployAdmission{ctx: deployCtx, cancel: cancel, generation: d.previewHostEmergencyGeneration}
	d.previewHostMu.Unlock()
	return admission, nil
}

func (d *Daemon) recordPreviewDeployOutcome(
	ctx context.Context,
	idempotencyKey string,
	admission *previewDeployAdmission,
	state journal.PreviewHostState,
	raw []byte,
) error {
	d.previewHostMu.Lock()
	delete(d.previewHostDeployCancels, idempotencyKey)
	update := journal.PreviewHostUpdate{IdempotencyKey: idempotencyKey, State: state, Result: raw}
	stopped := admission.generation != d.previewHostEmergencyGeneration &&
		(state == journal.PreviewHostDeployed || state == journal.PreviewHostUncertain)
	if stopped {
		update.State = journal.PreviewHostDestroying
		update.DestroyReason = v1.PreviewHostedDestroyEmergencyStop
	}
	err := d.journal.UpdatePreviewHost(ctx, update)
	d.previewHostMu.Unlock()
	if stopped {
		d.wakePreviewHostReaper()
	}
	return err
}

func (d *Daemon) wakePreviewHostReaper() {
	if d.previewHostWake == nil {
		return
	}
	select {
	case d.previewHostWake <- struct{}{}:
	default:
	}
}

func (d *Daemon) emergencyStopPreviewHosts(ctx context.Context) error {
	if d.previewHostController == nil {
		return nil
	}
	d.previewHostMu.Lock()
	d.previewHostEmergencyGeneration++
	for _, cancel := range d.previewHostDeployCancels {
		cancel()
	}
	err := d.markPreviewHostsDestroyingLocked(ctx, v1.PreviewHostedDestroyEmergencyStop, time.Time{})
	d.previewHostMu.Unlock()
	d.wakePreviewHostReaper()
	return err
}

func (d *Daemon) markPreviewHostsDestroyingLocked(
	ctx context.Context,
	reason v1.PreviewHostedDestroyReason,
	expiredBy time.Time,
) error {
	hosts, err := d.journal.PreviewHostsInStates(ctx, journal.PreviewHostDeployed)
	if err != nil {
		return err
	}
	var errs []error
	for _, host := range hosts {
		if !expiredBy.IsZero() && host.ExpiresAt.After(expiredBy) {
			continue
		}
		if err := d.journal.UpdatePreviewHost(ctx, journal.PreviewHostUpdate{
			IdempotencyKey: host.IdempotencyKey, State: journal.PreviewHostDestroying, DestroyReason: reason,
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *Daemon) expirePreviewHosts(ctx context.Context, reason v1.PreviewHostedDestroyReason) error {
	if d.previewHostController == nil {
		return nil
	}
	d.previewHostMu.Lock()
	markErr := d.markPreviewHostsDestroyingLocked(ctx, reason, time.Now())
	d.previewHostMu.Unlock()
	return errors.Join(markErr, d.reapPreviewHosts(ctx))
}

func (d *Daemon) reapPreviewHosts(ctx context.Context) error {
	if d.previewHostController == nil {
		return nil
	}
	d.previewHostReapMu.Lock()
	defer d.previewHostReapMu.Unlock()
	hosts, err := d.journal.PreviewHostsInStates(ctx, journal.PreviewHostDestroying)
	if err != nil {
		return err
	}
	var errs []error
	for _, host := range hosts {
		if err := d.finishPreviewHostDestroy(ctx, host); err != nil {
			errs = append(errs, err)
			if ctx.Err() != nil {
				break
			}
		}
	}
	return errors.Join(errs...)
}

func (d *Daemon) finishPreviewHostDestroy(ctx context.Context, host journal.PreviewHost) error {
	recoverErr := d.previewHostController.Recover(ctx, host)
	if recoverErr != nil && ctx.Err() != nil {
		return errors.Join(ctx.Err(), recoverErr)
	}
	outcome, state := v1.PreviewHostedDestroyOutcomeDestroyed, journal.PreviewHostDestroyed
	if recoverErr != nil {
		d.logger.Warn("hosted preview teardown unproven", "preview_id", host.PreviewID, "err", recoverErr)
		outcome, state = v1.PreviewHostedDestroyOutcomeUncertain, journal.PreviewHostUncertain
	}
	reason := host.DestroyReason
	if reason == "" {
		reason = v1.PreviewHostedDestroyExpired
	}
	event := v1.NewPreviewHostedDestroyedEvent(v1.PreviewHostedDestroyedSubject{
		PreviewID: host.PreviewID, OrganizationID: host.OrganizationID, ProjectID: host.ProjectID, PreviewKey: host.PreviewKey,
	}, time.Now(), reason, outcome)
	if _, err := d.sendEventWithStableID(ctx, v1.PreviewHostedDestroyedEventID(host.PreviewID), event); err != nil {
		return err
	}
	return d.journal.UpdatePreviewHost(ctx, journal.PreviewHostUpdate{IdempotencyKey: host.IdempotencyKey, State: state})
}

func (d *Daemon) reconcilePreviewHostsAtStartup(ctx context.Context) error {
	if d.previewHostController == nil {
		return nil
	}
	sweepErr := d.sweepOrphanPreviewResources(ctx)
	return errors.Join(sweepErr, d.expirePreviewHosts(ctx, v1.PreviewHostedDestroyReconcileExpired))
}

func (d *Daemon) sweepOrphanPreviewResources(ctx context.Context) error {
	lister, ok := d.previewHostController.(previewHostIDLister)
	if !ok {
		return nil
	}
	d.previewHostMu.Lock()
	defer d.previewHostMu.Unlock()
	ids, listErr := lister.HostedPreviewIDs(ctx)
	if listErr != nil && len(ids) == 0 {
		return listErr
	}
	tracked, err := d.journal.PreviewHostsInStates(ctx,
		journal.PreviewHostDeploying, journal.PreviewHostDeployed, journal.PreviewHostDestroying)
	if err != nil {
		return errors.Join(listErr, err)
	}
	live := make(map[string]bool, len(tracked))
	for _, host := range tracked {
		live[host.PreviewID] = true
	}
	errs := []error{listErr}
	for _, id := range ids {
		if live[id] {
			continue
		}
		if err := d.previewHostController.Recover(ctx, journal.PreviewHost{PreviewID: id}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *Daemon) previewHostExpiryLoop(ctx context.Context) {
	if d.previewHostController == nil {
		return
	}
	if err := d.reconcilePreviewHostsAtStartup(ctx); err != nil && ctx.Err() == nil {
		d.logger.Warn("reconcile hosted previews", "err", err)
	}
	ticker := time.NewTicker(previewHostExpiryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-d.previewHostWake:
		}
		if err := d.expirePreviewHosts(ctx, v1.PreviewHostedDestroyExpired); err != nil && ctx.Err() == nil {
			d.logger.Warn("expire hosted previews", "err", err)
		}
	}
}
