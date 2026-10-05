package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validSourcePublicationOperation() v1.SourcePublicationOperation {
	return v1.SourcePublicationOperation{
		SourcePublicationOperationAuthority: v1.SourcePublicationOperationAuthority{
			SchemaVersion:             v1.SourcePublicationOperationAuthorityVersion,
			SourcePublicationEffectID: "publication-effect-1",
			ReviewReadyRequestID:      "review-ready-1",
			ProjectID:                 "project-1",
			WorkflowRunID:             "workflow-1",
			AttemptID:                 "attempt-1",
			ExecutionManifestID:       "manifest-1",
			ExecutionManifestDigest:   strings.Repeat("a", 64),
			LeaseID:                   "publication-lease-1",
			FencingToken:              12,
			LeaseExpiresAt:            time.Date(2026, 8, 20, 16, 30, 0, 0, time.UTC),
		},
		Publication: v1.SourcePublicationCommand{
			SchemaVersion: v1.SourcePublicationSchemaVersion, Provider: "github", InstallationID: "installation-1",
			RepositoryID: "repository-1", RepositoryExternalID: "octo/example",
			BaseBranch: "main", HeadBranch: "job/workflow-1/attempt-1",
			HeadSHA:        strings.Repeat("b", 40),
			IdempotencyKey: "source-publish-" + strings.Repeat("c", 64),
			Title:          "Implement ABC-123",
		},
	}
}

func TestSourcePublicationOperationRejectsEveryAuthorityBoundary(t *testing.T) {
	if err := SourcePublicationOperation(validSourcePublicationOperation()); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*v1.SourcePublicationOperation){
		"schema":              func(value *v1.SourcePublicationOperation) { value.SchemaVersion = 2 },
		"effect":              func(value *v1.SourcePublicationOperation) { value.SourcePublicationEffectID = "bad/id" },
		"review request":      func(value *v1.SourcePublicationOperation) { value.ReviewReadyRequestID = "" },
		"project":             func(value *v1.SourcePublicationOperation) { value.ProjectID = "bad id" },
		"workflow":            func(value *v1.SourcePublicationOperation) { value.WorkflowRunID = "" },
		"attempt":             func(value *v1.SourcePublicationOperation) { value.AttemptID = "../attempt" },
		"manifest":            func(value *v1.SourcePublicationOperation) { value.ExecutionManifestID = "" },
		"manifest digest":     func(value *v1.SourcePublicationOperation) { value.ExecutionManifestDigest = strings.Repeat("A", 64) },
		"lease":               func(value *v1.SourcePublicationOperation) { value.LeaseID = "" },
		"fence":               func(value *v1.SourcePublicationOperation) { value.FencingToken = 0 },
		"large fence":         func(value *v1.SourcePublicationOperation) { value.FencingToken = 1 << 53 },
		"lease expiry":        func(value *v1.SourcePublicationOperation) { value.LeaseExpiresAt = time.Time{} },
		"provider":            func(value *v1.SourcePublicationOperation) { value.Publication.Provider = "GitHub" },
		"installation":        func(value *v1.SourcePublicationOperation) { value.Publication.InstallationID = "" },
		"repository":          func(value *v1.SourcePublicationOperation) { value.Publication.RepositoryID = "bad/repo" },
		"external repository": func(value *v1.SourcePublicationOperation) { value.Publication.RepositoryExternalID = "bad\x00repo" },
		"base branch":         func(value *v1.SourcePublicationOperation) { value.Publication.BaseBranch = "../main" },
		"head branch":         func(value *v1.SourcePublicationOperation) { value.Publication.HeadBranch = "bad ref" },
		"head sha":            func(value *v1.SourcePublicationOperation) { value.Publication.HeadSHA = strings.Repeat("A", 40) },
		"idempotency":         func(value *v1.SourcePublicationOperation) { value.Publication.IdempotencyKey = "arbitrary" },
		"title":               func(value *v1.SourcePublicationOperation) { value.Publication.Title = "line one\nline two" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := validSourcePublicationOperation()
			mutate(&candidate)
			if err := SourcePublicationOperation(candidate); err == nil {
				t.Fatal("invalid source publication operation accepted")
			}
		})
	}
}

func validSourcePublicationResultEvent() v1.SourcePublicationResultEvent {
	operation := validSourcePublicationOperation()
	return v1.SourcePublicationResultEvent{
		Kind:                                v1.EventSourcePublicationResult,
		SourcePublicationOperationAuthority: operation.SourcePublicationOperationAuthority,
		CommandMessageID:                    "publication-command-1",
		ObservedAt:                          time.Date(2026, 8, 20, 16, 20, 0, 0, time.UTC),
		Outcome: v1.SourcePublicationResultOutcome{
			Kind: v1.SourcePublicationSucceeded,
			Result: &v1.SourcePublicationResult{
				SchemaVersion: operation.Publication.SchemaVersion,
				Provider:      operation.Publication.Provider, InstallationID: operation.Publication.InstallationID,
				RepositoryID: operation.Publication.RepositoryID, RepositoryExternalID: operation.Publication.RepositoryExternalID,
				BaseBranch: operation.Publication.BaseBranch, HeadBranch: operation.Publication.HeadBranch,
				HeadSHA: operation.Publication.HeadSHA, IdempotencyKey: operation.Publication.IdempotencyKey,
				ExternalRequestID: "pull-request-42", URL: "https://github.com/octo/example/pull/42", State: "open",
			},
		},
	}
}

func TestSourcePublicationResultEventAllowsOnlyNormalizedOutcomes(t *testing.T) {
	if err := SourcePublicationResultEvent(validSourcePublicationResultEvent()); err != nil {
		t.Fatal(err)
	}

	retryAfter := uint64(60_000)
	retryable := validSourcePublicationResultEvent()
	retryable.Outcome = v1.SourcePublicationResultOutcome{
		Kind: v1.SourcePublicationRetryable, Code: v1.SourceControlRateLimited,
		RetryAfterMS: &retryAfter,
	}
	if err := SourcePublicationResultEvent(retryable); err != nil {
		t.Fatalf("valid retryable result rejected: %v", err)
	}

	permanent := validSourcePublicationResultEvent()
	permanent.Outcome = v1.SourcePublicationResultOutcome{
		Kind: v1.SourcePublicationPermanent, Code: v1.SourceControlPermissionDenied,
	}
	if err := SourcePublicationResultEvent(permanent); err != nil {
		t.Fatalf("valid permanent result rejected: %v", err)
	}

	mutations := map[string]func(*v1.SourcePublicationResultEvent){
		"kind":                   func(value *v1.SourcePublicationResultEvent) { value.Kind = "provider_result" },
		"command":                func(value *v1.SourcePublicationResultEvent) { value.CommandMessageID = "bad/id" },
		"observed":               func(value *v1.SourcePublicationResultEvent) { value.ObservedAt = time.Time{} },
		"success without result": func(value *v1.SourcePublicationResultEvent) { value.Outcome.Result = nil },
		"success with code":      func(value *v1.SourcePublicationResultEvent) { value.Outcome.Code = v1.SourceControlPermissionDenied },
		"noncanonical result URL": func(value *v1.SourcePublicationResultEvent) {
			value.Outcome.Result.URL = "https://github.com/octo/example/pull/42?token=secret"
		},
		"retryable permanent code": func(value *v1.SourcePublicationResultEvent) {
			value.Outcome = v1.SourcePublicationResultOutcome{Kind: v1.SourcePublicationRetryable, Code: v1.SourceControlPermissionDenied}
		},
		"permanent retry hint": func(value *v1.SourcePublicationResultEvent) {
			value.Outcome = v1.SourcePublicationResultOutcome{Kind: v1.SourcePublicationPermanent, Code: v1.SourceControlPermissionDenied, RetryAfterMS: &retryAfter}
		},
		"unknown credential code": func(value *v1.SourcePublicationResultEvent) {
			value.Outcome = v1.SourcePublicationResultOutcome{Kind: v1.SourcePublicationRetryable, Code: "provider_credential_unavailable"}
		},
		"raw error code": func(value *v1.SourcePublicationResultEvent) {
			value.Outcome = v1.SourcePublicationResultOutcome{Kind: v1.SourcePublicationRetryable, Code: "access_token=secret"}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := validSourcePublicationResultEvent()
			mutate(&candidate)
			if err := SourcePublicationResultEvent(candidate); err == nil {
				t.Fatal("invalid source publication result accepted")
			}
		})
	}
}
