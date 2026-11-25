package daemon

import (
	"context"
	"encoding/json"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/previewhost"
	"github.com/example/app-runner/internal/validation"
)

func (d *Daemon) executeHostedPreviewLifecycle(
	ctx context.Context,
	commandMessageID string,
	operation v1.PreviewLifecycleOperation,
) error {
	if d.previewHostController == nil {
		return rejectCommand(v1.CommandAckReasonUnsupportedCommand, nil)
	}
	if err := validation.PreviewLifecycleCommandMessageID(commandMessageID); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	if !d.previewLifecycleKeyStart(operation.IdempotencyKey) {
		return nil
	}
	defer d.previewLifecycleKeyFinish(operation.IdempotencyKey)
	stored, found, err := d.journal.PreviewHostedLifecycleResultByKey(ctx, operation.IdempotencyKey)
	if err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	if found {
		return d.replayHostedPreviewLifecycle(ctx, operation, stored)
	}
	d.previewHostMu.Lock()
	defer d.previewHostMu.Unlock()
	if d.Paused() && operation.Action != v1.PreviewLifecycleDestroy {
		return rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
	}
	now := time.Now()
	if !operation.LeaseExpiresAt.After(now) ||
		(operation.Action == v1.PreviewLifecycleExtend && !operation.CurrentEffectiveExpiresAt.After(now)) {
		return rejectCommand(v1.CommandAckReasonLeaseRejected, nil)
	}
	host, found, err := d.journal.PreviewHostByPreviewID(ctx, operation.PreviewID)
	if err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	outcome, apply, err := hostedPreviewLifecyclePrecheck(operation, host, found)
	if err != nil {
		return err
	}
	if apply && d.previewHostInFlightLocked(host.IdempotencyKey) {
		outcome, apply = v1.PreviewLifecycleResultOutcome{
			Kind: v1.PreviewLifecycleRetryable, Code: v1.PreviewLifecycleTransientResourceConflict,
		}, false
	}
	if apply {
		destroying := operation.Action == v1.PreviewLifecycleDestroy
		if destroying {
			d.resetPreviewTunnel(operation.PreviewID)
		}
		lifecycleCtx, cancel := context.WithDeadline(ctx, d.previewHostDeadline(operation.LeaseExpiresAt))
		outcome = d.previewHostController.ApplyLifecycle(lifecycleCtx, previewhost.LifecycleRequest{
			Operation: operation, Host: host,
		}).Outcome
		cancel()
		if destroying {
			d.resetPreviewTunnel(operation.PreviewID)
		}
	}
	observedAt := time.Now().UTC().Truncate(time.Millisecond)
	if observedAt.After(operation.LeaseExpiresAt) {
		observedAt = operation.LeaseExpiresAt
		if apply {
			outcome = v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecycleUncertain, Code: v1.PreviewLifecycleControllerTimeout}
		}
	}
	if outcome.Kind == v1.PreviewLifecycleSucceeded && outcome.ResourceState != nil {
		outcome.ResourceBindingDigest = operation.ResourceBindingDigest
		if outcome.ControllerReceiptDigest == "" {
			outcome.ControllerReceiptDigest = v1.PreviewHostedLifecycleReceiptDigest(operation, *outcome.ResourceState)
		}
	}
	event := v1.PreviewLifecycleResultEvent{
		Kind:                            v1.EventPreviewLifecycleResult,
		PreviewLifecycleResultAuthority: v1.PreviewLifecycleResultAuthorityFromOperation(operation),
		CommandMessageID:                commandMessageID,
		ObservedAt:                      observedAt,
		Outcome:                         outcome,
	}
	if err := validation.PreviewHostedLifecycleResultEvent(event); err != nil {
		event.Outcome = v1.PreviewLifecycleResultOutcome{
			Kind: v1.PreviewLifecyclePermanent, Code: v1.PreviewLifecycleInvalidControllerResponse,
		}
		if apply && operation.Action == v1.PreviewLifecycleDestroy {
			event.Outcome = v1.PreviewLifecycleResultOutcome{
				Kind: v1.PreviewLifecycleUncertain, Code: v1.PreviewLifecycleReceiptUnavailable,
			}
		}
		if err := validation.PreviewHostedLifecycleResultEvent(event); err != nil {
			return rejectCommand(v1.CommandAckReasonInternalRejection, err)
		}
	}
	eventID := previewLifecycleResultEventID(commandMessageID)
	if event.Outcome.Kind != v1.PreviewLifecycleRetryable {
		raw, err := json.Marshal(event)
		if err != nil {
			return rejectCommand(v1.CommandAckReasonInternalRejection, err)
		}
		var update *journal.PreviewHostUpdate
		if apply {
			update = hostedPreviewLifecycleHostUpdate(operation, host, event.Outcome)
		}
		record := journal.PreviewHostedLifecycleResult{
			IdempotencyKey: operation.IdempotencyKey, OperationDigest: operation.OperationDigest,
			EventID: eventID, Result: raw,
		}
		if _, _, err := d.journal.RecordPreviewHostedLifecycleResult(ctx, record, update); err != nil {
			return rejectCommand(v1.CommandAckReasonInternalRejection, err)
		}
	}
	_, err = d.sendEventWithStableID(ctx, eventID, event)
	return err
}

func (d *Daemon) replayHostedPreviewLifecycle(
	ctx context.Context,
	operation v1.PreviewLifecycleOperation,
	stored journal.PreviewHostedLifecycleResult,
) error {
	if stored.OperationDigest != operation.OperationDigest {
		return rejectCommand(v1.CommandAckReasonCommandConflict, nil)
	}
	var event v1.PreviewLifecycleResultEvent
	if err := json.Unmarshal(stored.Result, &event); err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	if err := validation.PreviewHostedLifecycleResultEvent(event); err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	_, err := d.sendEventWithStableID(ctx, stored.EventID, event)
	return err
}

func hostedPreviewLifecyclePrecheck(
	operation v1.PreviewLifecycleOperation,
	host journal.PreviewHost,
	found bool,
) (v1.PreviewLifecycleResultOutcome, bool, error) {
	absent := !found || host.State == journal.PreviewHostDestroyed || host.State == journal.PreviewHostFailed
	if absent {
		if operation.Action == v1.PreviewLifecycleDestroy {
			return v1.PreviewLifecycleResultOutcome{
				Kind: v1.PreviewLifecycleSucceeded,
				ResourceState: &v1.PreviewLifecycleResourceState{
					Kind: v1.PreviewLifecycleResourceDestroyed, Disposition: v1.PreviewLifecycleAlreadyAbsent,
				},
			}, false, nil
		}
		return v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecyclePermanent, Code: v1.PreviewLifecycleResourceNotFound}, false, nil
	}
	if !hostedPreviewHostBindingMatches(operation, host) {
		return v1.PreviewLifecycleResultOutcome{
			Kind: v1.PreviewLifecyclePermanent, Code: v1.PreviewLifecycleResourceBindingMismatch,
		}, false, nil
	}
	switch host.State {
	case journal.PreviewHostDeployed:
		if operation.Action == v1.PreviewLifecycleExtend && !operation.CurrentEffectiveExpiresAt.Equal(host.ExpiresAt) {
			return v1.PreviewLifecycleResultOutcome{
				Kind: v1.PreviewLifecyclePermanent, Code: v1.PreviewLifecycleResourceBindingMismatch,
			}, false, nil
		}
		return v1.PreviewLifecycleResultOutcome{}, true, nil
	case journal.PreviewHostUncertain:
		if operation.Action == v1.PreviewLifecycleDestroy {
			return v1.PreviewLifecycleResultOutcome{}, true, nil
		}
		return v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecyclePermanent, Code: v1.PreviewLifecycleResourceNotFound}, false, nil
	}
	return v1.PreviewLifecycleResultOutcome{
		Kind: v1.PreviewLifecycleRetryable, Code: v1.PreviewLifecycleTransientResourceConflict,
	}, false, nil
}

func hostedPreviewHostBindingMatches(operation v1.PreviewLifecycleOperation, host journal.PreviewHost) bool {
	return host.PreviewKey == operation.PreviewKey && host.OrganizationID == operation.OrganizationID &&
		host.ProjectID == operation.ProjectID && host.BlueprintID == operation.Blueprint.BlueprintID &&
		host.BlueprintVersion == operation.Blueprint.Version && host.BlueprintDigest == operation.Blueprint.Digest &&
		host.ExecutionManifestID == operation.ExecutionManifestID &&
		host.ExecutionManifestDigest == operation.ExecutionManifestDigest &&
		host.CommitSetDigest == operation.CommitSetDigest &&
		host.OriginalExpiresAt.Equal(operation.OriginalExpiresAt)
}

func hostedPreviewLifecycleHostUpdate(
	operation v1.PreviewLifecycleOperation,
	host journal.PreviewHost,
	outcome v1.PreviewLifecycleResultOutcome,
) *journal.PreviewHostUpdate {
	update := journal.PreviewHostUpdate{IdempotencyKey: host.IdempotencyKey}
	switch {
	case outcome.Kind == v1.PreviewLifecycleSucceeded && operation.Action == v1.PreviewLifecycleDestroy:
		update.State = journal.PreviewHostDestroyed
	case outcome.Kind == v1.PreviewLifecycleSucceeded && operation.Action == v1.PreviewLifecycleExtend:
		update.State = host.State
		update.ExpiresAt = outcome.ResourceState.ExpiresAt
	case outcome.Kind == v1.PreviewLifecycleUncertain && operation.Action == v1.PreviewLifecycleDestroy:
		update.State = journal.PreviewHostUncertain
	default:
		return nil
	}
	return &update
}

func (d *Daemon) previewLifecycleKeyStart(idempotencyKey string) bool {
	d.previewLifecycleCommandMu.Lock()
	defer d.previewLifecycleCommandMu.Unlock()
	if d.previewLifecycleKeyActive == nil {
		d.previewLifecycleKeyActive = make(map[string]struct{})
	}
	if _, active := d.previewLifecycleKeyActive[idempotencyKey]; active {
		return false
	}
	d.previewLifecycleKeyActive[idempotencyKey] = struct{}{}
	return true
}

func (d *Daemon) previewLifecycleKeyFinish(idempotencyKey string) {
	d.previewLifecycleCommandMu.Lock()
	delete(d.previewLifecycleKeyActive, idempotencyKey)
	d.previewLifecycleCommandMu.Unlock()
}
