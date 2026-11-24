package daemon

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/example/app-runner/internal/validation"
)

const (
	maxPreviewLifetime        = 30 * 24 * time.Hour
	maxPreviewLimitations     = 128
	maxPreviewLimitationBytes = 4096
)

type previewPublication struct {
	url         string
	health      string
	expiresAt   time.Time
	limitations []string
}

func parsePreviewPublication(raw map[string]any, observedAt time.Time) (previewPublication, error) {
	allowed := map[string]struct{}{
		"url": {}, "health": {}, "expires_at": {}, "known_limitations": {}, "idempotency_key": {},
	}
	for field := range raw {
		if _, ok := allowed[field]; !ok {
			return previewPublication{}, fmt.Errorf("preview field %q is not allowed", field)
		}
	}

	previewURL, ok := raw["url"].(string)
	if !ok {
		return previewPublication{}, fmt.Errorf("preview url is required")
	}
	if err := validation.WebURL("preview url", previewURL); err != nil {
		return previewPublication{}, err
	}
	if strings.Contains(previewURL, "\\") {
		return previewPublication{}, fmt.Errorf("preview url must not contain backslashes")
	}
	parsedURL, err := url.Parse(previewURL)
	if err != nil || parsedURL.RawQuery != "" || parsedURL.ForceQuery {
		return previewPublication{}, fmt.Errorf("preview url must not contain a query")
	}

	health, ok := raw["health"].(string)
	if !ok || (health != "healthy" && health != "degraded" && health != "down") {
		return previewPublication{}, fmt.Errorf("preview health must be healthy, degraded, or down")
	}
	expiresText, ok := raw["expires_at"].(string)
	if !ok || expiresText == "" {
		return previewPublication{}, fmt.Errorf("preview expires_at is required")
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresText)
	if err != nil {
		return previewPublication{}, fmt.Errorf("preview expires_at must be an RFC3339 timestamp")
	}
	observedAt = observedAt.UTC()
	if !expiresAt.After(observedAt) {
		return previewPublication{}, fmt.Errorf("preview expires_at must be in the future")
	}
	if expiresAt.After(observedAt.Add(maxPreviewLifetime)) {
		return previewPublication{}, fmt.Errorf("preview expires_at exceeds the maximum lifetime")
	}
	limitations, err := parsePreviewLimitations(raw)
	if err != nil {
		return previewPublication{}, err
	}
	return previewPublication{
		url: previewURL, health: health, expiresAt: expiresAt.UTC(), limitations: limitations,
	}, nil
}

func parsePreviewLimitations(raw map[string]any) ([]string, error) {
	value, exists := raw["known_limitations"]
	if !exists {
		return []string{}, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("known_limitations must be an array of strings")
	}
	if len(items) > maxPreviewLimitations {
		return nil, fmt.Errorf("known_limitations must contain at most %d items", maxPreviewLimitations)
	}
	limitations := make([]string, 0, len(items))
	for index, item := range items {
		limitation, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("known_limitations[%d] must be a string", index)
		}
		if strings.TrimSpace(limitation) == "" || len(limitation) > maxPreviewLimitationBytes {
			return nil, fmt.Errorf("known_limitations[%d] is blank or exceeds its size limit", index)
		}
		for _, character := range limitation {
			if (character < 0x20 && character != '\t' && character != '\n' && character != '\r') || character == 0x7f {
				return nil, fmt.Errorf("known_limitations[%d] contains a control character", index)
			}
		}
		limitations = append(limitations, limitation)
	}
	return limitations, nil
}

func (jr *jobRunner) executePreviewPublication(ctx context.Context, raw map[string]any) (any, error) {
	observedAt := time.Now().UTC()
	preview, err := parsePreviewPublication(raw, observedAt)
	if err != nil {
		return nil, err
	}
	commitSet, err := jr.previewCommitSet(ctx)
	if err != nil {
		return nil, err
	}
	if err := jr.d.leases.Validate(jr.job.AttemptID, jr.job.FencingToken); err != nil {
		return nil, fmt.Errorf("lease validation before preview evidence: %w", err)
	}
	fields := map[string]any{
		"url": preview.url, "health": preview.health,
		"expires_at":        preview.expiresAt.Format(time.RFC3339Nano),
		"known_limitations": preview.limitations, "commit_set": commitSet,
	}
	if err := jr.d.sendEvent(ctx, manifestBoundEvent(jr.job, "preview", fields, observedAt)); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (jr *jobRunner) previewCommitSet(ctx context.Context) (map[string]string, error) {
	if len(jr.repositories) < 1 || len(jr.repositories) > 16 {
		return nil, fmt.Errorf("preview requires between 1 and 16 authoritative repositories")
	}
	commitSet := make(map[string]string, len(jr.repositories))
	for _, repository := range jr.repositories {
		if err := validation.ID("repository_id", repository.ID); err != nil {
			return nil, err
		}
		if _, duplicate := commitSet[repository.ID]; duplicate {
			return nil, fmt.Errorf("repository %q appears more than once", repository.ID)
		}
		result, err := jr.resolveAgentResult(ctx, repository, "preview evidence")
		if err != nil {
			return nil, fmt.Errorf("collect preview evidence for repository %q: %w", repository.ID, err)
		}
		if err := validation.GitCommitSHA("commit_sha", result.commit); err != nil {
			return nil, fmt.Errorf("repository %q: %w", repository.ID, err)
		}
		commitSet[repository.ID] = result.commit
	}
	jr.recordTrustedCommitSet(ctx, commitSet)
	return commitSet, nil
}
