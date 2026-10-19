package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/example/app-runner/internal/validation"
)

const maxReviewReadySummaryBytes = 16 * 1024

type reviewReadyIntent struct {
	summary        string
	limitations    []string
	idempotencyKey string
}

func parseReviewReadyIntent(raw map[string]any) (reviewReadyIntent, error) {
	allowed := map[string]struct{}{
		"summary": {}, "known_limitations": {}, "idempotency_key": {},
	}
	for field := range raw {
		if _, ok := allowed[field]; !ok {
			return reviewReadyIntent{}, fmt.Errorf("review readiness field %q is not allowed", field)
		}
	}
	summary, ok := raw["summary"].(string)
	if !ok {
		return reviewReadyIntent{}, fmt.Errorf("review readiness summary is required")
	}
	if err := validateManualInstruction("review readiness summary", summary, maxReviewReadySummaryBytes, false); err != nil {
		return reviewReadyIntent{}, err
	}
	limitations, err := parsePreviewLimitations(raw)
	if err != nil {
		return reviewReadyIntent{}, err
	}
	return reviewReadyIntent{
		summary: summary, limitations: limitations,
		idempotencyKey: str(raw, "idempotency_key"),
	}, nil
}

func (jr *jobRunner) recordReviewReadyIntent(raw map[string]any) (any, error) {
	if !jr.job.PushBranch {
		return nil, fmt.Errorf("review readiness requires signed branch publication authority")
	}
	intent, err := parseReviewReadyIntent(raw)
	if err != nil {
		return nil, err
	}
	jr.reviewReadyMu.Lock()
	defer jr.reviewReadyMu.Unlock()
	if jr.reviewReadyIntent != nil {
		return nil, fmt.Errorf("a review readiness intent is already recorded for this attempt")
	}
	intent.limitations = append([]string{}, intent.limitations...)
	jr.reviewReadyIntent = &intent
	return map[string]any{
		"ok": true, "status": "pending_runner_finalization",
	}, nil
}

func (jr *jobRunner) reviewReadyIntentSnapshot() (reviewReadyIntent, bool) {
	jr.reviewReadyMu.Lock()
	defer jr.reviewReadyMu.Unlock()
	if jr.reviewReadyIntent == nil {
		return reviewReadyIntent{}, false
	}
	copy := *jr.reviewReadyIntent
	copy.limitations = append([]string{}, copy.limitations...)
	return copy, true
}

func (jr *jobRunner) publishFinalReviewReady(
	ctx context.Context,
	commitSet map[string]string,
) error {
	intent, ok := jr.reviewReadyIntentSnapshot()
	if !ok {
		return nil
	}
	validatedCommitSet, err := jr.validateFinalCommitSet(commitSet)
	if err != nil {
		return err
	}
	if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
		return fmt.Errorf("lease validation before review readiness: %w", err)
	}
	observedAt := time.Now().UTC()
	event := manifestBoundEvent(jr.job, "review_ready", map[string]any{
		"summary": intent.summary, "known_limitations": intent.limitations,
		"commit_set": validatedCommitSet,
	}, observedAt)
	_, err = jr.d.sendEventWithStableID(ctx, reviewReadyEventID(jr, intent), event)
	return err
}

func (jr *jobRunner) validateFinalCommitSet(commitSet map[string]string) (map[string]string, error) {
	if len(jr.repositories) < 1 || len(jr.repositories) > 16 || len(commitSet) != len(jr.repositories) {
		return nil, fmt.Errorf("review readiness requires the exact authoritative repository set")
	}
	validated := make(map[string]string, len(commitSet))
	for _, repository := range jr.repositories {
		if err := validation.ID("repository_id", repository.ID); err != nil {
			return nil, err
		}
		if _, duplicate := validated[repository.ID]; duplicate {
			return nil, fmt.Errorf("repository %q appears more than once", repository.ID)
		}
		commitSHA, exists := commitSet[repository.ID]
		if !exists {
			return nil, fmt.Errorf("review readiness is missing repository %q", repository.ID)
		}
		if err := validation.GitCommitSHA("commit_sha", commitSHA); err != nil {
			return nil, fmt.Errorf("repository %q: %w", repository.ID, err)
		}
		validated[repository.ID] = commitSHA
	}
	return validated, nil
}

func reviewReadyEventID(jr *jobRunner, intent reviewReadyIntent) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		jr.job.AttemptID,
		jr.job.ManifestDigest,
		intent.idempotencyKey,
	}, "\x00")))
	return "review-ready-" + hex.EncodeToString(digest[:])
}
