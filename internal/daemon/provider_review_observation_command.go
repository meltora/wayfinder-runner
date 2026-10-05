package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
	"time"
)

type providerReviewObserver interface {
	ReadyProviderReviewObservationInstallations() []v1.ProviderReviewObservationInstallationCapability
	ObserveProviderReviews(context.Context, v1.ProviderReviewObservationOperation, *v1.ProviderCredential) v1.ProviderReviewObservationResultOutcome
}

func validateProviderReviewObservationCommandBoundary(c v1.CommandPayload) error {
	if c.Command != v1.CmdProviderReviewObservation {
		if c.ProviderReviewObservation != nil {
			return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
		}
		return nil
	}
	if c.ProviderReviewObservation == nil || c.ProviderReviewFeedbackContent != nil || c.ProviderReviewerAssignment != nil || c.ProviderReviewerAssignmentReconciliation != nil || c.SourcePublication != nil || c.PreviewObservation != nil || c.ReviewerIdentityVerification != nil || c.Job != nil || c.JobID != "" || c.AttemptID != "" || c.FencingToken != 0 || !c.LeaseExpiresAt.IsZero() || c.Reason != "" || c.QuestionID != "" || c.Decision != "" || c.DecidedBy != "" || c.UpdateID != "" || c.DrainDeadline != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, nil)
	}
	if err := validation.ProviderReviewObservationOperation(*c.ProviderReviewObservation); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	return nil
}
func (d *Daemon) executeProviderReviewObservation(ctx context.Context, id string, op v1.ProviderReviewObservationOperation, providerCredential *v1.ProviderCredential) error {
	observer := d.sourcePublicationServices().providerReviewObserver
	if observer == nil {
		return rejectCommand(v1.CommandAckReasonUnsupportedCommand, nil)
	}
	if err := validation.ProviderReviewObservationCommandMessageID(id); err != nil {
		return rejectCommand(v1.CommandAckReasonInvalidCommand, err)
	}
	if !op.LeaseExpiresAt.After(time.Now()) {
		return rejectCommand(v1.CommandAckReasonLeaseRejected, nil)
	}
	observeCtx, cancel := context.WithDeadline(ctx, op.LeaseExpiresAt)
	defer cancel()
	outcome := v1.ProviderReviewObservationResultOutcome{}
	now := time.Now()
	if code := expiredReviewObservationSubject(op, now); code != "" {
		outcome = v1.ProviderReviewObservationResultOutcome{Kind: v1.ProviderReviewObservationPermanent, Code: code}
	} else {
		outcome = observer.ObserveProviderReviews(observeCtx, op, providerCredential)
	}
	observed := time.Now().UTC()
	if observed.After(op.LeaseExpiresAt) {
		observed = op.LeaseExpiresAt
		outcome = v1.ProviderReviewObservationResultOutcome{Kind: v1.ProviderReviewObservationRetryable, Code: v1.ProviderReviewObservationProviderUnavailable}
	}
	event := v1.ProviderReviewObservationResultEvent{Kind: v1.EventProviderReviewObservationResult, ProviderReviewObservationAuthority: op.ProviderReviewObservationAuthority, CommandMessageID: id, ObservedAt: observed, Outcome: outcome}
	if err := validation.ProviderReviewObservationResultEvent(event); err != nil {
		return rejectCommand(v1.CommandAckReasonInternalRejection, err)
	}
	_, err := d.sendEventWithStableID(ctx, providerReviewObservationResultEventID(id), event)
	return err
}
func expiredReviewObservationSubject(op v1.ProviderReviewObservationOperation, now time.Time) v1.ProviderReviewObservationErrorCode {
	for _, s := range op.Subjects {
		if !s.ProviderAccountProofExpiresAt.After(now) {
			return v1.ProviderReviewObservationProviderAccountProofExpired
		}
		if !s.IdentityVerificationExpiresAt.After(now) {
			return v1.ProviderReviewObservationSubjectIdentityExpired
		}
	}
	return ""
}
func providerReviewObservationResultEventID(id string) string {
	digest := sha256.Sum256([]byte(id))
	return "provider-review-observation-result-" + hex.EncodeToString(digest[:])
}
func (d *Daemon) completeProviderReviewObservationCommand(ctx context.Context, e v1.Envelope, c v1.CommandPayload, owner string, effect bool) {
	defer d.providerReviewObservationCommands.Done()
	defer d.providerReviewObservationCommandFinish(e.ID)
	if err := d.completeClaimedCommand(ctx, e, c, nil, owner, effect); err != nil {
		d.logger.Error("complete provider review observation command", "id", e.ID, "err", err)
	}
}
func (d *Daemon) providerReviewObservationCommandIsActive(id string) bool {
	d.providerReviewObservationCommandMu.Lock()
	defer d.providerReviewObservationCommandMu.Unlock()
	_, ok := d.providerReviewObservationCommandActive[id]
	return ok
}
func (d *Daemon) providerReviewObservationCommandStart(id string) bool {
	d.providerReviewObservationCommandMu.Lock()
	defer d.providerReviewObservationCommandMu.Unlock()
	if d.providerReviewObservationCommandActive == nil {
		d.providerReviewObservationCommandActive = map[string]struct{}{}
	}
	if _, ok := d.providerReviewObservationCommandActive[id]; ok {
		return false
	}
	d.providerReviewObservationCommandActive[id] = struct{}{}
	return true
}
func (d *Daemon) providerReviewObservationCommandFinish(id string) {
	d.providerReviewObservationCommandMu.Lock()
	delete(d.providerReviewObservationCommandActive, id)
	d.providerReviewObservationCommandMu.Unlock()
}
