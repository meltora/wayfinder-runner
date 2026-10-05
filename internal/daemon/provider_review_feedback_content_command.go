package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

type providerReviewFeedbackContentReader interface {
	ReadyProviderReviewFeedbackContentInstallations() []v1.ProviderReviewFeedbackContentInstallationCapability
	FetchProviderReviewFeedbackContent(context.Context, v1.ProviderReviewFeedbackContentOperation, *v1.ProviderCredential) v1.ProviderReviewFeedbackContentResultOutcome
}

func validateProviderReviewFeedbackContentCommandBoundary(c v1.CommandPayload) error {
	if c.Command != v1.CmdProviderReviewFeedbackContent {
		if c.ProviderReviewFeedbackContent != nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return nil
	}
	if c.ProviderReviewFeedbackContent == nil || c.ProviderReviewObservation != nil || c.ProviderReviewerAssignment != nil || c.ProviderReviewerAssignmentReconciliation != nil || c.SourcePublication != nil || c.PreviewObservation != nil || c.ReviewerIdentityVerification != nil || c.Job != nil || c.JobID != "" || c.AttemptID != "" || c.FencingToken != 0 || !c.LeaseExpiresAt.IsZero() || c.Reason != "" || c.QuestionID != "" || c.Decision != "" || c.DecidedBy != "" || c.UpdateID != "" || c.DrainDeadline != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	if err := validation.ProviderReviewFeedbackContentOperation(*c.ProviderReviewFeedbackContent); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	return nil
}

func (d *Daemon) executeProviderReviewFeedbackContent(ctx context.Context, id string, op v1.ProviderReviewFeedbackContentOperation, providerCredential *v1.ProviderCredential) error {
	reader := d.sourcePublicationServices().providerReviewFeedbackContentReader
	if reader == nil {
		return rejectCommand(v1.CommandAckReasonUnsupportedCommand, nil)
	}
	if err := validation.ProviderReviewFeedbackContentCommandMessageID(id); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	if !op.LeaseExpiresAt.After(time.Now()) {
		return rejectCommand(v1.CommandAckReasonLeaseRejected, nil)
	}
	fetchCtx, cancel := context.WithDeadline(ctx, op.LeaseExpiresAt)
	defer cancel()
	now := time.Now()
	outcome := v1.ProviderReviewFeedbackContentResultOutcome{}
	if code := expiredProviderReviewFeedbackAuthority(op, now); code != "" {
		outcome = feedbackContentPermanent(code)
	} else {
		outcome = reader.FetchProviderReviewFeedbackContent(fetchCtx, op, providerCredential)
	}
	observed := time.Now().UTC()
	if observed.Before(op.EvidenceObservedAt) {
		observed = op.EvidenceObservedAt
	}
	if observed.After(op.LeaseExpiresAt) {
		observed = op.LeaseExpiresAt
		outcome = v1.ProviderReviewFeedbackContentResultOutcome{Kind: v1.ProviderReviewFeedbackContentRetryable, Code: v1.ProviderReviewFeedbackContentProviderUnavailable}
	}
	event := v1.ProviderReviewFeedbackContentResultEvent{Kind: v1.EventProviderReviewFeedbackContentResult, ProviderReviewFeedbackContentAuthority: op.ProviderReviewFeedbackContentAuthority, CommandMessageID: id, ObservedAt: observed, Outcome: outcome}
	if err := validation.ProviderReviewFeedbackContentResultEvent(event); err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	_, err := d.sendEventWithStableID(ctx, providerReviewFeedbackContentResultEventID(id), event)
	return err
}

func feedbackContentPermanent(code v1.ProviderReviewFeedbackContentErrorCode) v1.ProviderReviewFeedbackContentResultOutcome {
	return v1.ProviderReviewFeedbackContentResultOutcome{Kind: v1.ProviderReviewFeedbackContentPermanent, Code: code}
}
func expiredProviderReviewFeedbackAuthority(op v1.ProviderReviewFeedbackContentOperation, now time.Time) v1.ProviderReviewFeedbackContentErrorCode {
	if !op.ProviderAccountProofExpiresAt.After(now) {
		return v1.ProviderReviewFeedbackContentProviderAccountProofExpired
	}
	if !op.IdentityVerificationExpiresAt.After(now) {
		return v1.ProviderReviewFeedbackContentReviewerIdentityExpired
	}
	if op.CodeReviewAuthorityExpiresAt != nil && !op.CodeReviewAuthorityExpiresAt.After(now) {
		return v1.ProviderReviewFeedbackContentCodeReviewAuthorityChanged
	}
	return ""
}
func providerReviewFeedbackContentResultEventID(id string) string {
	digest := sha256.Sum256([]byte(id))
	return "provider-review-feedback-content-result-" + hex.EncodeToString(digest[:])
}
func (d *Daemon) completeProviderReviewFeedbackContentCommand(ctx context.Context, e v1.Envelope, c v1.CommandPayload, owner string, effect bool) {
	defer d.providerReviewFeedbackContentCommands.Done()
	defer d.providerReviewFeedbackContentCommandFinish(e.ID)
	if err := d.completeClaimedCommand(ctx, e, c, nil, owner, effect); err != nil {
		d.logger.Error("complete provider review feedback content command", "id", e.ID, "err", err)
	}
}
func (d *Daemon) providerReviewFeedbackContentCommandIsActive(id string) bool {
	d.providerReviewFeedbackContentCommandMu.Lock()
	defer d.providerReviewFeedbackContentCommandMu.Unlock()
	_, ok := d.providerReviewFeedbackContentCommandActive[id]
	return ok
}
func (d *Daemon) providerReviewFeedbackContentCommandStart(id string) bool {
	d.providerReviewFeedbackContentCommandMu.Lock()
	defer d.providerReviewFeedbackContentCommandMu.Unlock()
	if d.providerReviewFeedbackContentCommandActive == nil {
		d.providerReviewFeedbackContentCommandActive = map[string]struct{}{}
	}
	if _, ok := d.providerReviewFeedbackContentCommandActive[id]; ok {
		return false
	}
	d.providerReviewFeedbackContentCommandActive[id] = struct{}{}
	return true
}
func (d *Daemon) providerReviewFeedbackContentCommandFinish(id string) {
	d.providerReviewFeedbackContentCommandMu.Lock()
	delete(d.providerReviewFeedbackContentCommandActive, id)
	d.providerReviewFeedbackContentCommandMu.Unlock()
}
