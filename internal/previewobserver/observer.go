package previewobserver

import (
	"context"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func (registry *Registry) Observe(
	ctx context.Context,
	operation v1.PreviewObservationOperation,
) v1.PreviewObservationResultOutcome {
	compiled, ok := registry.profileFor(operation)
	if !ok {
		return permanentOutcome(v1.PreviewProfileUnavailable)
	}
	target, err := registry.resolveTarget(ctx, compiled, operation.PreviewURL)
	if err != nil {
		return outcomeForError(err)
	}
	checks := make([]v1.PreviewObservationCheck, 0, len(operation.RequiredHealthCheckIDs))
	for _, checkID := range operation.RequiredHealthCheckIDs {
		result, err := registry.executeCheck(ctx, compiled, target, checkID)
		if err != nil {
			return outcomeForError(err)
		}
		checks = append(checks, result)
	}
	return v1.PreviewObservationResultOutcome{
		Kind: v1.PreviewObservationObserved,
		Observation: &v1.PreviewObservation{
			SchemaVersion: v1.PreviewObservationSchemaVersion,
			PreviewURL:    operation.PreviewURL,
			CommitSet:     append([]v1.PreviewObservationCommit(nil), operation.CommitSet...),
			Checks:        checks,
		},
	}
}

func outcomeForError(err error) v1.PreviewObservationResultOutcome {
	code := resultCode(err)
	if code == v1.PreviewProfileUnavailable || code == v1.PreviewTargetNotAllowed ||
		code == v1.PreviewInvalidObserverResponse {
		return permanentOutcome(code)
	}
	return v1.PreviewObservationResultOutcome{Kind: v1.PreviewObservationRetryable, Code: code}
}

func permanentOutcome(code v1.PreviewObservationErrorCode) v1.PreviewObservationResultOutcome {
	return v1.PreviewObservationResultOutcome{Kind: v1.PreviewObservationPermanent, Code: code}
}
