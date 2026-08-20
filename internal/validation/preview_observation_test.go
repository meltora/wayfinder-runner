package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validPreviewObservationOperation() v1.PreviewObservationOperation {
	return v1.PreviewObservationOperation{
		PreviewObservationOperationAuthority: v1.PreviewObservationOperationAuthority{
			SchemaVersion:              v1.PreviewObservationOperationAuthorityVersion,
			PreviewObservationEffectID: "preview-effect-1", PreviewEvidenceID: "preview-evidence-1",
			ReviewReadyRequestID: "review-ready-1", ProjectID: "project-1", WorkflowRunID: "workflow-1",
			AttemptID: "attempt-1", ExecutionManifestID: "manifest-1", ExecutionManifestDigest: strings.Repeat("a", 64),
			LeaseID: "preview-operation-1", FencingToken: 3, LeaseExpiresAt: time.Date(2030, 8, 14, 12, 4, 0, 0, time.UTC),
		},
		Blueprint:  v1.PreviewObservationBlueprint{BlueprintID: "blueprint-1", Version: "1", Digest: strings.Repeat("b", 64)},
		PreviewURL: "https://feature.preview.example.test/", PreviewExpiresAt: time.Date(2030, 8, 14, 13, 0, 0, 0, time.UTC),
		CommitSet: []v1.PreviewObservationCommit{
			{RepositoryID: "api", CommitSHA: strings.Repeat("c", 40)},
			{RepositoryID: "web", CommitSHA: strings.Repeat("d", 40)},
		},
		RequiredHealthCheckIDs: []string{"api-health", "http-smoke"},
	}
}

func TestPreviewObservationOperationRejectsEveryAuthorityBoundary(t *testing.T) {
	if err := PreviewObservationOperation(validPreviewObservationOperation()); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*v1.PreviewObservationOperation){
		"schema":         func(value *v1.PreviewObservationOperation) { value.SchemaVersion = 2 },
		"effect":         func(value *v1.PreviewObservationOperation) { value.PreviewObservationEffectID = "../bad" },
		"digest":         func(value *v1.PreviewObservationOperation) { value.ExecutionManifestDigest = strings.Repeat("A", 64) },
		"fence":          func(value *v1.PreviewObservationOperation) { value.FencingToken = 0 },
		"blueprint":      func(value *v1.PreviewObservationOperation) { value.Blueprint.BlueprintID = "" },
		"blueprint hash": func(value *v1.PreviewObservationOperation) { value.Blueprint.Digest = strings.Repeat("z", 64) },
		"query":          func(value *v1.PreviewObservationOperation) { value.PreviewURL += "?token=secret" },
		"preview expiry": func(value *v1.PreviewObservationOperation) { value.PreviewExpiresAt = value.LeaseExpiresAt },
		"commit order": func(value *v1.PreviewObservationOperation) {
			value.CommitSet[0], value.CommitSet[1] = value.CommitSet[1], value.CommitSet[0]
		},
		"check order": func(value *v1.PreviewObservationOperation) {
			value.RequiredHealthCheckIDs[0], value.RequiredHealthCheckIDs[1] = value.RequiredHealthCheckIDs[1], value.RequiredHealthCheckIDs[0]
		},
		"no checks": func(value *v1.PreviewObservationOperation) { value.RequiredHealthCheckIDs = nil },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := validPreviewObservationOperation()
			mutate(&candidate)
			if err := PreviewObservationOperation(candidate); err == nil {
				t.Fatal("invalid preview observation operation was accepted")
			}
		})
	}
}

func validPreviewObservationResultEvent() v1.PreviewObservationResultEvent {
	operation := validPreviewObservationOperation()
	return v1.PreviewObservationResultEvent{
		Kind:                                 v1.EventPreviewObservationResult,
		PreviewObservationOperationAuthority: operation.PreviewObservationOperationAuthority,
		CommandMessageID:                     "preview-command-1", ObservedAt: time.Date(2030, 8, 14, 12, 1, 0, 0, time.UTC),
		Outcome: v1.PreviewObservationResultOutcome{
			Kind: v1.PreviewObservationObserved,
			Observation: &v1.PreviewObservation{
				SchemaVersion: v1.PreviewObservationSchemaVersion, PreviewURL: operation.PreviewURL,
				CommitSet: operation.CommitSet,
				Checks: []v1.PreviewObservationCheck{
					{CheckID: "api-health", Status: v1.PreviewObservationCheckPassed, HTTPStatus: 200, DurationMS: 10},
					{CheckID: "http-smoke", Status: v1.PreviewObservationCheckFailed, HTTPStatus: 503, DurationMS: 20},
				},
			},
		},
	}
}

func TestPreviewObservationResultEventAllowsOnlyNormalizedOutcomes(t *testing.T) {
	if err := PreviewObservationResultEvent(validPreviewObservationResultEvent()); err != nil {
		t.Fatal(err)
	}
	retryAfter := uint64(1000)
	retryable := validPreviewObservationResultEvent()
	retryable.Outcome = v1.PreviewObservationResultOutcome{
		Kind: v1.PreviewObservationRetryable, Code: v1.PreviewUnreachable, RetryAfterMS: &retryAfter,
	}
	if err := PreviewObservationResultEvent(retryable); err != nil {
		t.Fatal(err)
	}
	permanent := validPreviewObservationResultEvent()
	permanent.Outcome = v1.PreviewObservationResultOutcome{
		Kind: v1.PreviewObservationPermanent, Code: v1.PreviewTargetNotAllowed,
	}
	if err := PreviewObservationResultEvent(permanent); err != nil {
		t.Fatal(err)
	}

	mutations := map[string]func(*v1.PreviewObservationResultEvent){
		"kind":    func(value *v1.PreviewObservationResultEvent) { value.Kind = "preview" },
		"command": func(value *v1.PreviewObservationResultEvent) { value.CommandMessageID = "" },
		"check order": func(value *v1.PreviewObservationResultEvent) {
			value.Outcome.Observation.Checks[0], value.Outcome.Observation.Checks[1] = value.Outcome.Observation.Checks[1], value.Outcome.Observation.Checks[0]
		},
		"bad status": func(value *v1.PreviewObservationResultEvent) { value.Outcome.Observation.Checks[0].Status = "unknown" },
		"bad http":   func(value *v1.PreviewObservationResultEvent) { value.Outcome.Observation.Checks[0].HTTPStatus = 99 },
		"retry class": func(value *v1.PreviewObservationResultEvent) {
			value.Outcome = v1.PreviewObservationResultOutcome{Kind: v1.PreviewObservationRetryable, Code: v1.PreviewTargetNotAllowed}
		},
		"raw code": func(value *v1.PreviewObservationResultEvent) {
			value.Outcome = v1.PreviewObservationResultOutcome{Kind: v1.PreviewObservationPermanent, Code: "token=secret"}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := validPreviewObservationResultEvent()
			mutate(&candidate)
			if err := PreviewObservationResultEvent(candidate); err == nil {
				t.Fatal("invalid preview observation result was accepted")
			}
		})
	}
}
