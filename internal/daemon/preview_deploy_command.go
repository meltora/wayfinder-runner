package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/previewhost"
	"github.com/example/app-runner/internal/redact"
	"github.com/example/app-runner/internal/validation"
)

func validatePreviewDeployCommandBoundary(command v1.CommandPayload) error {
	if command.Command != v1.CmdPreviewDeploy {
		if command.PreviewDeploy != nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return nil
	}
	if command.PreviewDeploy == nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	rest := command
	rest.Command = ""
	rest.PreviewDeploy = nil
	if rest != (v1.CommandPayload{}) {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	if err := validation.PreviewDeployOperation(*command.PreviewDeploy); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	if _, err := validation.PreviewDeployBlueprintContent(command.PreviewDeploy.Blueprint); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	return nil
}

func (d *Daemon) executePreviewDeploy(
	ctx context.Context,
	commandMessageID string,
	operation v1.PreviewDeployOperation,
	credential *v1.ProviderCredential,
) error {
	credentialToken := ""
	if credential != nil {
		credentialToken = credential.Token
		redact.RememberSecret(credentialToken)
	}
	if d.previewHostController == nil {
		return rejectCommand(v1.CommandAckReasonUnsupportedCommand, nil)
	}
	if err := validation.PreviewDeployCommandMessageID(commandMessageID); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	if !d.previewDeployKeyStart(operation.IdempotencyKey) {
		return nil
	}
	defer d.previewDeployKeyFinish(operation.IdempotencyKey)
	existing, found, err := d.journal.PreviewHostByIdempotencyKey(ctx, operation.IdempotencyKey)
	if err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	if found {
		if existing.OperationDigest != operation.OperationDigest {
			return rejectCommand(v1.CommandAckReasonCommandConflict, nil)
		}
		return d.resumePreviewDeploy(ctx, commandMessageID, operation, existing)
	}
	if d.Paused() {
		return rejectCommand(v1.CommandAckReasonRunnerPaused, nil)
	}
	if !operation.LeaseExpiresAt.After(time.Now()) {
		return rejectCommand(v1.CommandAckReasonLeaseRejected, nil)
	}
	content, err := validation.PreviewDeployBlueprintContent(operation.Blueprint)
	if err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	host := journal.PreviewHost{
		PreviewID: operation.PreviewID, PreviewKey: operation.PreviewKey, IdempotencyKey: operation.IdempotencyKey,
		OperationDigest: operation.OperationDigest, OrganizationID: operation.OrganizationID, ProjectID: operation.ProjectID,
		ExecutionManifestID: operation.ExecutionManifestID, ExecutionManifestDigest: operation.ExecutionManifestDigest,
		CommitSetDigest: operation.CommitSetDigest, BlueprintID: operation.Blueprint.BlueprintID,
		BlueprintVersion: operation.Blueprint.Version, BlueprintDigest: operation.Blueprint.Digest, MemoryMB: content.Limits.MemoryMB,
		OriginalExpiresAt: operation.ExpiresAt, ExpiresAt: operation.ExpiresAt,
		State: journal.PreviewHostDeploying,
	}
	trusted, err := d.previewDeployCommitSetTrusted(ctx, operation)
	if err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	if !trusted {
		event := previewDeployResultEvent(operation, commandMessageID, previewDeployObservedAt(operation),
			v1.PreviewDeployOutcome{Kind: v1.PreviewDeployFailed, Phase: v1.PreviewDeployPhaseCommitSet})
		raw, err := validatedPreviewDeployResult(event)
		if err != nil {
			return rejectCommand(v1.CommandAckReasonInternalRejection, err)
		}
		host.State = journal.PreviewHostFailed
		host.Result = raw
		if _, _, err := d.journal.InsertPreviewHost(ctx, host); err != nil {
			return previewHostInsertRejection(err)
		}
		return d.sendPreviewDeployResult(ctx, event)
	}
	admission, err := d.admitPreviewDeploy(ctx, commandMessageID, operation, host)
	if admission == nil {
		return err
	}
	result := d.previewHostController.Deploy(admission.ctx, previewhost.DeployRequest{
		Operation: operation, Content: content, Credential: credential,
	})
	admission.cancel()
	observedAt := time.Now().UTC().Truncate(time.Millisecond)
	outcome := result.Outcome
	if observedAt.After(operation.LeaseExpiresAt) {
		observedAt = operation.LeaseExpiresAt
		if outcome.Kind != v1.PreviewDeployFailed || outcome.Phase != v1.PreviewDeployPhaseLeaseExpired {
			outcome = v1.PreviewDeployOutcome{Kind: v1.PreviewDeployUncertain}
		}
	}
	outcome = sanitizePreviewDeployOutcome(outcome, content, credentialToken)
	event := previewDeployResultEvent(operation, commandMessageID, observedAt, outcome)
	raw, err := validatedPreviewDeployResult(event)
	if err != nil {
		event.Outcome = v1.PreviewDeployOutcome{Kind: v1.PreviewDeployUncertain}
		if raw, err = validatedPreviewDeployResult(event); err != nil {
			return rejectCommand(v1.CommandAckReasonInternalRejection, err)
		}
	}
	if err := d.recordPreviewDeployOutcome(ctx, operation.IdempotencyKey, admission,
		previewHostStateForOutcome(event.Outcome.Kind), raw); err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	return d.sendPreviewDeployResult(ctx, event)
}

func (d *Daemon) resumePreviewDeploy(
	ctx context.Context,
	commandMessageID string,
	operation v1.PreviewDeployOperation,
	host journal.PreviewHost,
) error {
	if host.State == journal.PreviewHostDeploying {
		return d.recoverInterruptedPreviewDeploy(ctx, commandMessageID, operation, host)
	}
	if len(host.Result) == 0 {
		return d.reportUncertainPreviewDeploy(ctx, commandMessageID, operation, false, false)
	}
	return d.resendStoredPreviewDeployResult(ctx, host.Result)
}

func (d *Daemon) reportUncertainPreviewDeploy(
	ctx context.Context,
	commandMessageID string,
	operation v1.PreviewDeployOperation,
	proven bool,
	claimed bool,
) error {
	event := previewDeployResultEvent(operation, commandMessageID, previewDeployObservedAt(operation),
		v1.PreviewDeployOutcome{Kind: v1.PreviewDeployUncertain})
	raw, err := validatedPreviewDeployResult(event)
	d.previewHostMu.Lock()
	stopped := false
	if claimed {
		stopped = d.releasePreviewHostRecoveryLocked(operation.IdempotencyKey)
	}
	if err != nil {
		d.previewHostMu.Unlock()
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	current, found, err := d.journal.PreviewHostByIdempotencyKey(ctx, operation.IdempotencyKey)
	if err == nil && !found {
		err = journal.ErrPreviewHostNotFound
	}
	if err != nil {
		d.previewHostMu.Unlock()
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	stored := current.Result
	update := journal.PreviewHostUpdate{IdempotencyKey: operation.IdempotencyKey, State: current.State}
	if current.State == journal.PreviewHostDeploying {
		update.State = journal.PreviewHostUncertain
		if proven {
			update.State = journal.PreviewHostDestroyed
		} else if stopped {
			update.State = journal.PreviewHostDestroying
			update.DestroyReason = v1.PreviewHostedDestroyEmergencyStop
		}
	}
	if len(stored) == 0 {
		update.Result = raw
	}
	if update.State != current.State || len(update.Result) > 0 {
		err = d.journal.UpdatePreviewHost(ctx, update)
	}
	d.previewHostMu.Unlock()
	if err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	if update.State == journal.PreviewHostDestroying {
		d.wakePreviewHostReaper()
	}
	if len(stored) > 0 {
		return d.resendStoredPreviewDeployResult(ctx, stored)
	}
	return d.sendPreviewDeployResult(ctx, event)
}

func (d *Daemon) resendStoredPreviewDeployResult(ctx context.Context, raw []byte) error {
	var stored v1.PreviewDeployResultEvent
	if err := json.Unmarshal(raw, &stored); err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	if err := validation.PreviewDeployResultEvent(stored); err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	return d.sendPreviewDeployResult(ctx, stored)
}

func (d *Daemon) previewDeployCommitSetTrusted(ctx context.Context, operation v1.PreviewDeployOperation) (bool, error) {
	return d.journal.TrustedCommitSetRecorded(ctx, operation.ExecutionManifestID, operation.ExecutionManifestDigest,
		operation.CommitSetDigest, operation.CommitSet)
}

func previewHostInsertRejection(err error) error {
	if errors.Is(err, journal.ErrPreviewHostConflict) {
		return rejectCommand(v1.CommandAckReasonCommandConflict, err)
	}
	return rejectCommand(v1.CommandAckReasonInternalRejection, err)
}

func previewDeployObservedAt(operation v1.PreviewDeployOperation) time.Time {
	observedAt := time.Now().UTC().Truncate(time.Millisecond)
	if observedAt.After(operation.LeaseExpiresAt) {
		return operation.LeaseExpiresAt
	}
	return observedAt
}

func previewDeployResultEvent(
	operation v1.PreviewDeployOperation,
	commandMessageID string,
	observedAt time.Time,
	outcome v1.PreviewDeployOutcome,
) v1.PreviewDeployResultEvent {
	return v1.PreviewDeployResultEvent{
		Kind:                         v1.EventPreviewDeployResult,
		PreviewDeployResultAuthority: v1.PreviewDeployResultAuthorityFromOperation(operation),
		CommandMessageID:             commandMessageID,
		ObservedAt:                   observedAt,
		Outcome:                      outcome,
	}
}

func validatedPreviewDeployResult(event v1.PreviewDeployResultEvent) ([]byte, error) {
	if err := validation.PreviewDeployResultEvent(event); err != nil {
		return nil, err
	}
	return json.Marshal(event)
}

func (d *Daemon) sendPreviewDeployResult(ctx context.Context, event v1.PreviewDeployResultEvent) error {
	_, err := d.sendEventWithStableID(ctx, v1.PreviewDeployResultEventID(event.IdempotencyKey), event)
	return err
}

func sanitizePreviewDeployOutcome(
	outcome v1.PreviewDeployOutcome,
	content v1.PreviewBlueprintContentV1,
	credentialToken string,
) v1.PreviewDeployOutcome {
	switch outcome.Kind {
	case v1.PreviewDeployDeployed:
		if !previewDeployChecksMatchBlueprint(outcome.Checks, content) {
			return v1.PreviewDeployOutcome{Kind: v1.PreviewDeployUncertain}
		}
	case v1.PreviewDeployFailed:
	case v1.PreviewDeployCapacityExhausted, v1.PreviewDeployUncertain:
		return v1.PreviewDeployOutcome{Kind: outcome.Kind}
	default:
		return v1.PreviewDeployOutcome{Kind: v1.PreviewDeployUncertain}
	}
	outcome.LogTail = boundPreviewDeployLogTail(redact.Token(redact.KnownSecrets(outcome.LogTail), credentialToken))
	return outcome
}

func previewDeployChecksMatchBlueprint(checks []v1.PreviewObservationCheck, content v1.PreviewBlueprintContentV1) bool {
	if len(checks) != len(content.App.HealthChecks) {
		return false
	}
	ids := make([]string, 0, len(content.App.HealthChecks))
	for _, check := range content.App.HealthChecks {
		ids = append(ids, check.ID)
	}
	slices.Sort(ids)
	for index, id := range ids {
		if checks[index].CheckID != id {
			return false
		}
	}
	return true
}

func boundPreviewDeployLogTail(value string) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) > v1.PreviewDeployLogTailMaxBytes {
		value = value[len(value)-v1.PreviewDeployLogTailMaxBytes:]
		for len(value) > 0 && !utf8.RuneStart(value[0]) {
			value = value[1:]
		}
	}
	lines := strings.SplitAfter(value, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > v1.PreviewDeployLogTailMaxLines {
		value = strings.Join(lines[len(lines)-v1.PreviewDeployLogTailMaxLines:], "")
	}
	return value
}

func previewHostStateForOutcome(kind v1.PreviewDeployOutcomeKind) journal.PreviewHostState {
	switch kind {
	case v1.PreviewDeployDeployed:
		return journal.PreviewHostDeployed
	case v1.PreviewDeployFailed, v1.PreviewDeployCapacityExhausted:
		return journal.PreviewHostFailed
	}
	return journal.PreviewHostUncertain
}

func (d *Daemon) completePreviewDeployCommand(
	ctx context.Context,
	envelope v1.Envelope,
	command v1.CommandPayload,
	claimOwner string,
	effectMayHaveRun bool,
) {
	defer d.previewDeployCommands.Done()
	defer d.previewDeployCommandFinish(envelope.ID)
	if err := d.completeClaimedCommand(ctx, envelope, command, nil, claimOwner, effectMayHaveRun); err != nil {
		d.logger.Error("complete preview deploy command", "id", envelope.ID, "err", err)
	}
}

func (d *Daemon) previewDeployCommandIsActive(commandID string) bool {
	d.previewDeployCommandMu.Lock()
	defer d.previewDeployCommandMu.Unlock()
	_, active := d.previewDeployCommandActive[commandID]
	return active
}

func (d *Daemon) previewDeployCommandStart(commandID string) bool {
	d.previewDeployCommandMu.Lock()
	defer d.previewDeployCommandMu.Unlock()
	if d.previewDeployCommandActive == nil {
		d.previewDeployCommandActive = make(map[string]struct{})
	}
	if _, active := d.previewDeployCommandActive[commandID]; active {
		return false
	}
	d.previewDeployCommandActive[commandID] = struct{}{}
	return true
}

func (d *Daemon) previewDeployCommandFinish(commandID string) {
	d.previewDeployCommandMu.Lock()
	delete(d.previewDeployCommandActive, commandID)
	d.previewDeployCommandMu.Unlock()
}

func (d *Daemon) previewDeployKeyStart(idempotencyKey string) bool {
	d.previewDeployCommandMu.Lock()
	defer d.previewDeployCommandMu.Unlock()
	if d.previewDeployKeyActive == nil {
		d.previewDeployKeyActive = make(map[string]struct{})
	}
	if _, active := d.previewDeployKeyActive[idempotencyKey]; active {
		return false
	}
	d.previewDeployKeyActive[idempotencyKey] = struct{}{}
	return true
}

func (d *Daemon) previewDeployKeyFinish(idempotencyKey string) {
	d.previewDeployCommandMu.Lock()
	delete(d.previewDeployKeyActive, idempotencyKey)
	d.previewDeployCommandMu.Unlock()
}
