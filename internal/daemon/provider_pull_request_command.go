package daemon

import (
	"context"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

type providerPullRequestRunner interface {
	ReadyProviderPullRequestInstallations() []v1.ProviderPullRequestInstallationCapability
	RunProviderPullRequest(context.Context, v1.ProviderPullRequestAuthority, *v1.ProviderCredential) v1.ProviderPullRequestResultOutcome
}

func isProviderPullRequestCommand(command string) bool {
	return command == v1.CmdProviderPullRequestObservation || command == v1.CmdProviderPullRequestMerge
}

func validateProviderPullRequestCommandBoundary(c v1.CommandPayload) error {
	if !isProviderPullRequestCommand(c.Command) {
		if c.ProviderPullRequest != nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return nil
	}
	if c.ProviderPullRequest == nil || c.ProviderReviewFeedbackContent != nil || c.ProviderReviewObservation != nil || c.ProviderReviewerAssignment != nil || c.ProviderReviewerAssignmentReconciliation != nil || c.ReviewFeedbackContinuation != nil || c.SourcePublication != nil || c.PreviewObservation != nil || c.PreviewLifecycle != nil || c.ReviewerIdentityVerification != nil || c.WorkIntakeProposal != nil || c.AgentConfigScan != nil || c.AgentRunAuthority != nil || c.Job != nil || c.JobID != "" || c.AttemptID != "" || c.LeaseID != "" || c.ManifestID != "" || c.ManifestDigest != "" || c.FencingToken != 0 || !c.LeaseExpiresAt.IsZero() || c.Reason != "" || c.QuestionID != "" || c.Decision != "" || c.DecidedBy != "" || c.UpdateID != "" || c.DrainDeadline != nil || c.ControlID != "" || c.Action != "" || c.RequestedAt != "" || c.ExpiresAt != "" || c.CommandDigest != "" {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	if err := validation.ProviderPullRequestCommand(c.Command, *c.ProviderPullRequest); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	return nil
}

func (d *Daemon) executeProviderPullRequest(ctx context.Context, id string, op v1.ProviderPullRequestAuthority, providerCredential *v1.ProviderCredential) error {
	runner := d.sourcePublicationServices().providerPullRequestRunner
	if runner == nil {
		return rejectCommand(v1.CommandAckReasonUnsupportedCommand, nil)
	}
	if err := validation.ProviderPullRequestCommandMessageID(id, op); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	if !op.LeaseExpiresAt.After(time.Now()) {
		return rejectCommand(v1.CommandAckReasonLeaseRejected, nil)
	}
	runCtx, cancel := context.WithDeadline(ctx, op.LeaseExpiresAt)
	defer cancel()
	outcome := runner.RunProviderPullRequest(runCtx, op, providerCredential)
	now := time.Now()
	observed := v1.ProviderPullRequestTime(now)
	if !now.Before(op.LeaseExpiresAt) {
		observed = op.LeaseExpiresAt
		if outcome.Kind != v1.ProviderPullRequestOutcomeMerged {
			outcome = v1.ProviderPullRequestResultOutcome{Kind: v1.ProviderPullRequestOutcomeFailed, Code: v1.ProviderPullRequestDeadlineExceeded}
		}
	}
	event := v1.ProviderPullRequestResultEvent{Kind: v1.EventProviderPullRequestResult, ProviderPullRequestAuthority: op, CommandMessageID: id, ObservedAt: observed, Outcome: outcome}
	if err := validation.ProviderPullRequestResultEvent(event); err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	_, err := d.sendEventWithStableID(ctx, v1.ProviderPullRequestResultEventID(op.OperationID), event)
	return err
}

func (d *Daemon) completeProviderPullRequestCommand(ctx context.Context, e v1.Envelope, c v1.CommandPayload, owner string, effect bool) {
	defer d.providerPullRequestCommands.Done()
	defer d.providerPullRequestCommandFinish(e.ID)
	if err := d.completeClaimedCommand(ctx, e, c, nil, owner, effect); err != nil {
		d.logger.Error("complete provider pull request command", "id", e.ID, "err", err)
	}
}

func (d *Daemon) providerPullRequestCommandIsActive(id string) bool {
	d.providerPullRequestCommandMu.Lock()
	defer d.providerPullRequestCommandMu.Unlock()
	_, ok := d.providerPullRequestCommandActive[id]
	return ok
}

func (d *Daemon) providerPullRequestCommandStart(id string) bool {
	d.providerPullRequestCommandMu.Lock()
	defer d.providerPullRequestCommandMu.Unlock()
	if d.providerPullRequestCommandActive == nil {
		d.providerPullRequestCommandActive = map[string]struct{}{}
	}
	if _, ok := d.providerPullRequestCommandActive[id]; ok {
		return false
	}
	d.providerPullRequestCommandActive[id] = struct{}{}
	return true
}

func (d *Daemon) providerPullRequestCommandFinish(id string) {
	d.providerPullRequestCommandMu.Lock()
	delete(d.providerPullRequestCommandActive, id)
	d.providerPullRequestCommandMu.Unlock()
}
