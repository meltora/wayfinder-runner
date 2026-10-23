package validation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func controlPlanePreviewLifecycleCommandJSON(original, current, desired, lease string) string {
	return `{
		"schema_version": 1,
		"preview_lifecycle_effect_id": "preview-lifecycle-effect-1",
		"preview_lifecycle_effect_revision": 1,
		"preview_lifecycle_operation_id": "preview-lifecycle-operation-` + strings.Repeat("4", 64) + `",
		"organization_id": "organization-1",
		"project_id": "project-1",
		"workflow_run_id": "workflow-run-1",
		"preview_evidence_id": "preview-evidence-1",
		"preview_observation_id": "preview-observation-1",
		"preview_observation_digest": "` + strings.Repeat("2", 64) + `",
		"execution_manifest_id": "execution-manifest-1",
		"execution_manifest_digest": "` + strings.Repeat("3", 64) + `",
		"blueprint": {"blueprint_id": "blueprint-1", "version": "2026.08.1", "digest": "` + strings.Repeat("1", 64) + `"},
		"preview_url": "https://preview.example.test/",
		"commit_set": [
			{"repository_id": "repository-a", "commit_sha": "` + strings.Repeat("a", 40) + `"},
			{"repository_id": "repository-b", "commit_sha": "` + strings.Repeat("b", 40) + `"}
		],
		"commit_set_digest": "cf63268a5326a55193214033aab6908b3700dc9beea53426598b1fd929a8d361",
		"original_expires_at": "` + original + `",
		"current_effective_expires_at": "` + current + `",
		"action": "extend",
		"desired_expires_at": "` + desired + `",
		"idempotency_key": "preview-lifecycle-` + strings.Repeat("5", 64) + `",
		"resource_binding_digest": "f0c683d6f02ddb02982b0ac78795350f1b2943325e6f9eaeb2d1ed15e7a53e52",
		"operation_digest": "58c81e6395ef43f1e44385a95fe7a19534f7e36cbf94dd043a0d154b66e2c1b8",
		"command_digest": "da63a3c987b180eae64704e1e48f58d5b4ec623526751127d588cdfff3d77691",
		"lease_id": "preview-lifecycle-lease-1",
		"fencing_token": 7,
		"lease_expires_at": "` + lease + `"
	}`
}

func TestPreviewLifecycleOperationAcceptsControlPlaneMillisecondTimestamps(t *testing.T) {
	cases := map[string]struct {
		original, current, desired, lease string
		accepted                          bool
	}{
		"exact control-plane text": {
			"2030-08-21T12:00:00.520Z", "2030-08-22T12:00:00.500Z", "2030-08-23T12:00:00.100Z", "2030-08-22T12:05:00.520Z", true,
		},
		"lease one millisecond away": {
			"2030-08-21T12:00:00.520Z", "2030-08-22T12:00:00.500Z", "2030-08-23T12:00:00.100Z", "2030-08-22T12:05:00.519Z", false,
		},
		"original expiry one millisecond away": {
			"2030-08-21T12:00:00.521Z", "2030-08-22T12:00:00.500Z", "2030-08-23T12:00:00.100Z", "2030-08-22T12:05:00.520Z", false,
		},
		"desired expiry one millisecond away": {
			"2030-08-21T12:00:00.520Z", "2030-08-22T12:00:00.500Z", "2030-08-23T12:00:00.101Z", "2030-08-22T12:05:00.520Z", false,
		},
	}
	for name, command := range cases {
		t.Run(name, func(t *testing.T) {
			var operation v1.PreviewLifecycleOperation
			raw := controlPlanePreviewLifecycleCommandJSON(command.original, command.current, command.desired, command.lease)
			if err := json.Unmarshal([]byte(raw), &operation); err != nil {
				t.Fatalf("decode control-plane command: %v", err)
			}
			err := PreviewLifecycleOperation(operation)
			if command.accepted && err != nil {
				t.Fatalf("control-plane command rejected: %v", err)
			}
			if !command.accepted && err == nil {
				t.Fatal("tampered control-plane command accepted")
			}
		})
	}
}

func validPreviewLifecycleOperation(action v1.PreviewLifecycleAction) v1.PreviewLifecycleOperation {
	now := time.Date(2030, 8, 14, 12, 0, 0, 0, time.UTC)
	operation := v1.PreviewLifecycleOperation{
		PreviewLifecycleOperationAuthority: v1.PreviewLifecycleOperationAuthority{
			SchemaVersion:            v1.PreviewLifecycleSchemaVersion,
			PreviewLifecycleEffectID: "preview-lifecycle-effect-1", PreviewLifecycleEffectRevision: 3,
			PreviewLifecycleOperationID: "preview-lifecycle-operation-" + strings.Repeat("a", 64),
			OrganizationID:              "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1",
			PreviewEvidenceID: "preview-evidence-1", PreviewObservationID: "preview-observation-1",
			PreviewObservationDigest: strings.Repeat("b", 64),
			ExecutionManifestID:      "manifest-1", ExecutionManifestDigest: strings.Repeat("c", 64),
			Action: action, IdempotencyKey: "preview-lifecycle-" + strings.Repeat("d", 64),
			ResourceBindingDigest: strings.Repeat("e", 64), OperationDigest: strings.Repeat("f", 64),
			CommandDigest: strings.Repeat("1", 64), LeaseID: "lease-1", FencingToken: 7,
			LeaseExpiresAt: now.Add(5 * time.Minute),
		},
		Blueprint:  v1.PreviewObservationBlueprint{BlueprintID: "node-web", Version: "2026.08", Digest: strings.Repeat("2", 64)},
		PreviewURL: "https://branch.preview.example.test/",
		CommitSet: []v1.PreviewObservationCommit{
			{RepositoryID: "api", CommitSHA: strings.Repeat("3", 40)},
			{RepositoryID: "web", CommitSHA: strings.Repeat("4", 40)},
		},
		CommitSetDigest:   strings.Repeat("5", 64),
		OriginalExpiresAt: now.Add(6 * time.Hour), CurrentEffectiveExpiresAt: now.Add(6 * time.Hour),
	}
	if action == v1.PreviewLifecycleExtend {
		desired := now.Add(12 * time.Hour)
		operation.DesiredExpiresAt = &desired
	}
	operation.CommitSetDigest = v1.PreviewLifecycleCommitSetDigest(operation.CommitSet)
	operation.ResourceBindingDigest = v1.PreviewLifecycleResourceBindingDigest(operation)
	operation.OperationDigest = v1.PreviewLifecycleOperationDigest(operation)
	operation.CommandDigest = v1.PreviewLifecycleCommandDigest(operation)
	return operation
}

func TestPreviewLifecycleOperationAcceptsOnlyStrictActionShapes(t *testing.T) {
	for _, action := range []v1.PreviewLifecycleAction{v1.PreviewLifecycleExtend, v1.PreviewLifecycleDestroy} {
		if err := PreviewLifecycleOperation(validPreviewLifecycleOperation(action)); err != nil {
			t.Fatalf("valid %s operation rejected: %v", action, err)
		}
	}
	extendWithoutDesired := validPreviewLifecycleOperation(v1.PreviewLifecycleExtend)
	extendWithoutDesired.DesiredExpiresAt = nil
	if err := PreviewLifecycleOperation(extendWithoutDesired); err == nil {
		t.Fatal("extend without desired expiry accepted")
	}
	destroyWithDesired := validPreviewLifecycleOperation(v1.PreviewLifecycleDestroy)
	desired := destroyWithDesired.CurrentEffectiveExpiresAt.Add(time.Hour)
	destroyWithDesired.DesiredExpiresAt = &desired
	if err := PreviewLifecycleOperation(destroyWithDesired); err == nil {
		t.Fatal("destroy with desired expiry accepted")
	}
	tooFar := validPreviewLifecycleOperation(v1.PreviewLifecycleExtend)
	distant := tooFar.CurrentEffectiveExpiresAt.Add(30*24*time.Hour + time.Second)
	tooFar.DesiredExpiresAt = &distant
	if err := PreviewLifecycleOperation(tooFar); err == nil {
		t.Fatal("unbounded extension accepted")
	}
	regressed := validPreviewLifecycleOperation(v1.PreviewLifecycleDestroy)
	regressed.CurrentEffectiveExpiresAt = regressed.OriginalExpiresAt.Add(-time.Second)
	regressed.OperationDigest = v1.PreviewLifecycleOperationDigest(regressed)
	regressed.CommandDigest = v1.PreviewLifecycleCommandDigest(regressed)
	if err := PreviewLifecycleOperation(regressed); err == nil {
		t.Fatal("effective expiry before original expiry accepted")
	}
}

func TestPreviewLifecycleOperationRejectsEveryAuthorityBoundary(t *testing.T) {
	mutations := map[string]func(*v1.PreviewLifecycleOperation){
		"schema":             func(value *v1.PreviewLifecycleOperation) { value.SchemaVersion++ },
		"revision":           func(value *v1.PreviewLifecycleOperation) { value.PreviewLifecycleEffectRevision = 0 },
		"operation id":       func(value *v1.PreviewLifecycleOperation) { value.PreviewLifecycleOperationID = "operation-1" },
		"tenant":             func(value *v1.PreviewLifecycleOperation) { value.OrganizationID = "../foreign" },
		"observation digest": func(value *v1.PreviewLifecycleOperation) { value.PreviewObservationDigest = strings.Repeat("A", 64) },
		"url credentials": func(value *v1.PreviewLifecycleOperation) {
			value.PreviewURL = "https://user:secret@preview.example.test/"
		},
		"commit order": func(value *v1.PreviewLifecycleOperation) {
			value.CommitSet[0], value.CommitSet[1] = value.CommitSet[1], value.CommitSet[0]
		},
		"idempotency": func(value *v1.PreviewLifecycleOperation) { value.IdempotencyKey = "other" },
		"zero fence":  func(value *v1.PreviewLifecycleOperation) { value.FencingToken = 0 },
		"lease":       func(value *v1.PreviewLifecycleOperation) { value.LeaseExpiresAt = time.Time{} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := validPreviewLifecycleOperation(v1.PreviewLifecycleExtend)
			mutate(&candidate)
			if err := PreviewLifecycleOperation(candidate); err == nil {
				t.Fatal("invalid preview lifecycle operation accepted")
			}
		})
	}
}

func validPreviewLifecycleResultEvent(action v1.PreviewLifecycleAction) v1.PreviewLifecycleResultEvent {
	operation := validPreviewLifecycleOperation(action)
	outcome := v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecycleSucceeded}
	if action == v1.PreviewLifecycleExtend {
		outcome.ControllerReceiptDigest = strings.Repeat("6", 64)
		outcome.ResourceBindingDigest = operation.ResourceBindingDigest
		outcome.ResourceState = &v1.PreviewLifecycleResourceState{
			Kind: v1.PreviewLifecycleResourceActive, PreviewURL: operation.PreviewURL,
			Blueprint: &operation.Blueprint, CommitSet: append([]v1.PreviewObservationCommit(nil), operation.CommitSet...),
			ExpiresAt: operation.DesiredExpiresAt,
		}
	} else {
		outcome.ControllerReceiptDigest = strings.Repeat("6", 64)
		outcome.ResourceBindingDigest = operation.ResourceBindingDigest
		outcome.ResourceState = &v1.PreviewLifecycleResourceState{
			Kind: v1.PreviewLifecycleResourceDestroyed, Disposition: v1.PreviewLifecycleDestroyed,
		}
	}
	return v1.PreviewLifecycleResultEvent{
		Kind:                            v1.EventPreviewLifecycleResult,
		PreviewLifecycleResultAuthority: v1.PreviewLifecycleResultAuthorityFromOperation(operation),
		CommandMessageID:                "preview-lifecycle-command-" + strings.Repeat("7", 64),
		ObservedAt:                      operation.LeaseExpiresAt.Add(-time.Minute), Outcome: outcome,
	}
}

func TestPreviewLifecycleResultAcceptsOnlyNormalizedDiscriminants(t *testing.T) {
	for _, action := range []v1.PreviewLifecycleAction{v1.PreviewLifecycleExtend, v1.PreviewLifecycleDestroy} {
		if err := PreviewLifecycleResultEvent(validPreviewLifecycleResultEvent(action)); err != nil {
			t.Fatalf("valid %s result rejected: %v", action, err)
		}
	}
	retryAfter := uint64(1000)
	retryable := validPreviewLifecycleResultEvent(v1.PreviewLifecycleExtend)
	retryable.Outcome = v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecycleRetryable, Code: v1.PreviewLifecycleRateLimited, RetryAfterMS: &retryAfter}
	if err := PreviewLifecycleResultEvent(retryable); err != nil {
		t.Fatal(err)
	}
	permanent := validPreviewLifecycleResultEvent(v1.PreviewLifecycleDestroy)
	permanent.Outcome = v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecyclePermanent, Code: v1.PreviewLifecycleResourceBindingMismatch}
	if err := PreviewLifecycleResultEvent(permanent); err != nil {
		t.Fatal(err)
	}
	uncertain := validPreviewLifecycleResultEvent(v1.PreviewLifecycleDestroy)
	uncertain.Outcome = v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecycleUncertain, Code: v1.PreviewLifecycleControllerTimeout}
	if err := PreviewLifecycleResultEvent(uncertain); err != nil {
		t.Fatal(err)
	}

	bad := validPreviewLifecycleResultEvent(v1.PreviewLifecycleExtend)
	bad.Outcome.ResourceState.ExpiresAt = &bad.CurrentEffectiveExpiresAt
	if err := PreviewLifecycleResultEvent(bad); err == nil {
		t.Fatal("extend result with non-desired expiry accepted")
	}
	bad = validPreviewLifecycleResultEvent(v1.PreviewLifecycleDestroy)
	bad.Outcome.Code = v1.PreviewLifecyclePolicyDenied
	if err := PreviewLifecycleResultEvent(bad); err == nil {
		t.Fatal("success with error code accepted")
	}
}
