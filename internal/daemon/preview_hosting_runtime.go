package daemon

import (
	"context"
	"errors"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/capability"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/validation"
)

const previewHostExpiryInterval = 30 * time.Second

var previewHostOccupiedStates = []journal.PreviewHostState{
	journal.PreviewHostDeploying, journal.PreviewHostDeployed, journal.PreviewHostDestroying, journal.PreviewHostUncertain,
}

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

func (d *Daemon) previewHostNow() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

func (d *Daemon) previewHostLeaseMax() time.Duration {
	seconds := d.cfg.PreviewHosting.LeaseMaxSeconds
	if seconds <= 0 {
		seconds = config.DefaultPreviewHostingLeaseMaxSeconds
	}
	return time.Duration(seconds) * time.Second
}

func (d *Daemon) previewHostDeadline(lease time.Time) time.Time {
	limit := time.Now().Add(d.previewHostLeaseMax())
	if lease.After(limit) {
		return limit
	}
	return lease
}

func previewHostExpired(host journal.PreviewHost, now time.Time) bool {
	if host.State == journal.PreviewHostUncertain && host.DestroyReason != "" {
		return true
	}
	return !host.ExpiresAt.After(now)
}

func (d *Daemon) previewHostInFlightLocked(idempotencyKey string) bool {
	if _, deploying := d.previewHostDeployCancels[idempotencyKey]; deploying {
		return true
	}
	_, recovering := d.previewHostRecovering[idempotencyKey]
	return recovering
}

func (d *Daemon) claimPreviewHostRecoveryLocked(idempotencyKey string) {
	if d.previewHostRecovering == nil {
		d.previewHostRecovering = make(map[string]uint64)
	}
	d.previewHostRecovering[idempotencyKey] = d.previewHostEmergencyGeneration
}

func (d *Daemon) releasePreviewHostRecoveryLocked(idempotencyKey string) bool {
	generation, claimed := d.previewHostRecovering[idempotencyKey]
	delete(d.previewHostRecovering, idempotencyKey)
	return claimed && generation != d.previewHostEmergencyGeneration
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
	if _, sweeping := d.previewHostSweeping[operation.PreviewID]; sweeping {
		d.previewHostMu.Unlock()
		return nil, rejectCommand(v1.CommandAckReasonRunnerBusy, nil)
	}
	occupied, err := d.journal.PreviewHostsInStates(ctx, previewHostOccupiedStates...)
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
	deployCtx, cancel := context.WithDeadline(ctx, d.previewHostDeadline(operation.LeaseExpiresAt))
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
	err := d.markPreviewHostsDestroyingLocked(ctx, v1.PreviewHostedDestroyEmergencyStop,
		func(journal.PreviewHost) bool { return true })
	d.previewHostMu.Unlock()
	d.wakePreviewHostReaper()
	return err
}

func (d *Daemon) markPreviewHostsDestroyingLocked(
	ctx context.Context,
	reason v1.PreviewHostedDestroyReason,
	selected func(journal.PreviewHost) bool,
) error {
	hosts, err := d.journal.PreviewHostsInStates(ctx,
		journal.PreviewHostDeploying, journal.PreviewHostDeployed, journal.PreviewHostUncertain)
	if err != nil {
		return err
	}
	var errs []error
	for _, host := range hosts {
		if d.previewHostInFlightLocked(host.IdempotencyKey) || !selected(host) {
			continue
		}
		update := journal.PreviewHostUpdate{
			IdempotencyKey: host.IdempotencyKey, State: journal.PreviewHostDestroying, DestroyReason: reason,
		}
		if host.State == journal.PreviewHostUncertain && host.DestroyReason != "" {
			update.DestroyReason = ""
		}
		if err := d.journal.UpdatePreviewHost(ctx, update); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *Daemon) expirePreviewHosts(ctx context.Context, reason v1.PreviewHostedDestroyReason) error {
	if d.previewHostController == nil {
		return nil
	}
	now, leaseMax := d.previewHostNow(), d.previewHostLeaseMax()
	destroyErr := d.destroySelectedPreviewHosts(ctx, reason, func(host journal.PreviewHost) bool {
		if previewHostExpired(host, now) {
			return true
		}
		return host.State == journal.PreviewHostDeploying && !host.CreatedAt.Add(leaseMax).After(now)
	})
	return errors.Join(destroyErr, d.proveUncertainPreviewDeploys(ctx))
}

func (d *Daemon) proveUncertainPreviewDeploys(ctx context.Context) error {
	d.previewHostReapMu.Lock()
	defer d.previewHostReapMu.Unlock()
	d.previewHostMu.Lock()
	hosts, err := d.journal.PreviewHostsInStates(ctx, journal.PreviewHostUncertain)
	var claimed []journal.PreviewHost
	for _, host := range hosts {
		if host.DestroyReason != "" || d.previewHostInFlightLocked(host.IdempotencyKey) {
			continue
		}
		d.claimPreviewHostRecoveryLocked(host.IdempotencyKey)
		claimed = append(claimed, host)
	}
	d.previewHostMu.Unlock()
	if err != nil {
		return err
	}
	var errs []error
	wake := false
	for _, host := range claimed {
		recoverErr := d.previewHostController.Recover(ctx, host)
		if recoverErr != nil {
			d.logger.Warn("uncertain preview deploy teardown unproven", "preview_id", host.PreviewID, "err", recoverErr)
		}
		d.previewHostMu.Lock()
		stopped := d.releasePreviewHostRecoveryLocked(host.IdempotencyKey)
		var update *journal.PreviewHostUpdate
		switch {
		case recoverErr == nil:
			update = &journal.PreviewHostUpdate{IdempotencyKey: host.IdempotencyKey, State: journal.PreviewHostDestroyed}
		case stopped:
			update = &journal.PreviewHostUpdate{
				IdempotencyKey: host.IdempotencyKey, State: journal.PreviewHostDestroying,
				DestroyReason: v1.PreviewHostedDestroyEmergencyStop,
			}
			wake = true
		}
		if update != nil {
			current, found, err := d.journal.PreviewHostByIdempotencyKey(ctx, host.IdempotencyKey)
			if err == nil && found && current.State == journal.PreviewHostUncertain && current.DestroyReason == "" {
				err = d.journal.UpdatePreviewHost(ctx, *update)
			}
			if err != nil {
				errs = append(errs, err)
			}
		}
		d.previewHostMu.Unlock()
	}
	if wake {
		d.wakePreviewHostReaper()
	}
	return errors.Join(errs...)
}

func (d *Daemon) destroySelectedPreviewHosts(
	ctx context.Context,
	reason v1.PreviewHostedDestroyReason,
	selected func(journal.PreviewHost) bool,
) error {
	d.previewHostMu.Lock()
	markErr := d.markPreviewHostsDestroyingLocked(ctx, reason, selected)
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
	alreadyDestroyed, err := d.journal.HasRecord(ctx,
		v1.PreviewHostedDestroyedEventID(host.PreviewID, host.IdempotencyKey, v1.PreviewHostedDestroyOutcomeDestroyed))
	if err != nil {
		return err
	}
	if alreadyDestroyed {
		return d.journal.UpdatePreviewHost(ctx, journal.PreviewHostUpdate{
			IdempotencyKey: host.IdempotencyKey, State: journal.PreviewHostDestroyed,
		})
	}
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
		IdempotencyKey: host.IdempotencyKey,
	}, time.Now(), reason, outcome)
	if err := validation.PreviewHostedDestroyedEvent(event); err != nil {
		return err
	}
	if _, err := d.sendEventWithStableID(ctx,
		v1.PreviewHostedDestroyedEventID(host.PreviewID, host.IdempotencyKey, outcome), event); err != nil {
		return err
	}
	return d.journal.UpdatePreviewHost(ctx, journal.PreviewHostUpdate{IdempotencyKey: host.IdempotencyKey, State: state})
}

func (d *Daemon) reconcilePreviewHostsAtStartup(ctx context.Context) error {
	if d.previewHostController == nil {
		return nil
	}
	sweepErr := d.sweepOrphanPreviewResources(ctx)
	now := d.previewHostNow()
	destroyErr := d.destroySelectedPreviewHosts(ctx, v1.PreviewHostedDestroyReconcileExpired,
		func(host journal.PreviewHost) bool {
			return host.State == journal.PreviewHostDeploying || previewHostExpired(host, now)
		})
	return errors.Join(sweepErr, destroyErr, d.proveUncertainPreviewDeploys(ctx))
}

func (d *Daemon) sweepOrphanPreviewResources(ctx context.Context) error {
	lister, ok := d.previewHostController.(previewHostIDLister)
	if !ok {
		return nil
	}
	ids, listErr := lister.HostedPreviewIDs(ctx)
	if listErr != nil && len(ids) == 0 {
		return listErr
	}
	errs := []error{listErr}
	for _, id := range ids {
		claimed, err := d.claimOrphanPreviewSweep(ctx, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !claimed {
			continue
		}
		if err := d.previewHostController.Recover(ctx, journal.PreviewHost{PreviewID: id}); err != nil {
			errs = append(errs, err)
		}
		d.previewHostMu.Lock()
		delete(d.previewHostSweeping, id)
		d.previewHostMu.Unlock()
	}
	return errors.Join(errs...)
}

func (d *Daemon) claimOrphanPreviewSweep(ctx context.Context, previewID string) (bool, error) {
	d.previewHostMu.Lock()
	defer d.previewHostMu.Unlock()
	tracked, err := d.journal.PreviewHostsInStates(ctx, previewHostOccupiedStates...)
	if err != nil {
		return false, err
	}
	for _, host := range tracked {
		if host.PreviewID == previewID {
			return false, nil
		}
	}
	if d.previewHostSweeping == nil {
		d.previewHostSweeping = make(map[string]struct{})
	}
	d.previewHostSweeping[previewID] = struct{}{}
	return true, nil
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
