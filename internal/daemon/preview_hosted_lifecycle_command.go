package daemon

import (
	"context"
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
	now := time.Now()
	if !operation.LeaseExpiresAt.After(now) ||
		(operation.Action == v1.PreviewLifecycleExtend && !operation.CurrentEffectiveExpiresAt.After(now)) {
		return rejectCommand(v1.CommandAckReasonLeaseRejected, nil)
	}
	host, found, err := d.journal.PreviewHostByPreviewID(ctx, operation.PreviewID)
	if err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	outcome, apply := hostedPreviewLifecyclePrecheck(operation, host, found)
	if apply {
		lifecycleCtx, cancel := context.WithDeadline(ctx, operation.LeaseExpiresAt)
		outcome = d.previewHostController.ApplyLifecycle(lifecycleCtx, previewhost.LifecycleRequest{
			Operation: operation, Host: host,
		}).Outcome
		cancel()
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
	if apply {
		if err := d.recordHostedPreviewLifecycle(ctx, operation, host, event.Outcome); err != nil {
			return rejectCommand(v1.CommandAckReasonInternalRejection, err)
		}
	}
	_, err = d.sendEventWithStableID(ctx, previewLifecycleResultEventID(commandMessageID), event)
	return err
}

func hostedPreviewLifecyclePrecheck(
	operation v1.PreviewLifecycleOperation,
	host journal.PreviewHost,
	found bool,
) (v1.PreviewLifecycleResultOutcome, bool) {
	absent := !found || host.State == journal.PreviewHostDestroyed || host.State == journal.PreviewHostFailed
	if absent {
		if operation.Action == v1.PreviewLifecycleDestroy {
			return v1.PreviewLifecycleResultOutcome{
				Kind: v1.PreviewLifecycleSucceeded,
				ResourceState: &v1.PreviewLifecycleResourceState{
					Kind: v1.PreviewLifecycleResourceDestroyed, Disposition: v1.PreviewLifecycleAlreadyAbsent,
				},
			}, false
		}
		return v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecyclePermanent, Code: v1.PreviewLifecycleResourceNotFound}, false
	}
	if host.PreviewKey != operation.PreviewKey || host.OrganizationID != operation.OrganizationID ||
		host.ProjectID != operation.ProjectID || host.BlueprintID != operation.Blueprint.BlueprintID ||
		host.BlueprintVersion != operation.Blueprint.Version || host.BlueprintDigest != operation.Blueprint.Digest {
		return v1.PreviewLifecycleResultOutcome{
			Kind: v1.PreviewLifecyclePermanent, Code: v1.PreviewLifecycleResourceBindingMismatch,
		}, false
	}
	switch host.State {
	case journal.PreviewHostDeployed:
		return v1.PreviewLifecycleResultOutcome{}, true
	case journal.PreviewHostUncertain:
		if operation.Action == v1.PreviewLifecycleDestroy {
			return v1.PreviewLifecycleResultOutcome{}, true
		}
		return v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecyclePermanent, Code: v1.PreviewLifecycleResourceNotFound}, false
	}
	return v1.PreviewLifecycleResultOutcome{
		Kind: v1.PreviewLifecycleRetryable, Code: v1.PreviewLifecycleTransientResourceConflict,
	}, false
}

func (d *Daemon) recordHostedPreviewLifecycle(
	ctx context.Context,
	operation v1.PreviewLifecycleOperation,
	host journal.PreviewHost,
	outcome v1.PreviewLifecycleResultOutcome,
) error {
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
	return d.journal.UpdatePreviewHost(ctx, update)
}
