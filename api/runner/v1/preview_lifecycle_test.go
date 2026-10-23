package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPreviewLifecycleCommandUsesDedicatedCapabilityPayload(t *testing.T) {
	desired := time.Date(2030, 8, 15, 12, 0, 0, 0, time.UTC)
	command := CommandPayload{
		Command: CmdPreviewLifecycle,
		PreviewLifecycle: &PreviewLifecycleOperation{
			PreviewLifecycleOperationAuthority: PreviewLifecycleOperationAuthority{
				SchemaVersion:                  PreviewLifecycleSchemaVersion,
				PreviewLifecycleEffectID:       "preview-lifecycle-effect-1",
				PreviewLifecycleEffectRevision: 1,
				PreviewLifecycleOperationID:    "preview-lifecycle-operation-" + strings.Repeat("a", 64),
				OrganizationID:                 "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1",
				PreviewEvidenceID: "preview-evidence-1", PreviewObservationID: "preview-observation-1",
				PreviewObservationDigest: strings.Repeat("b", 64),
				ExecutionManifestID:      "manifest-1", ExecutionManifestDigest: strings.Repeat("c", 64),
				Action: PreviewLifecycleExtend, IdempotencyKey: "preview-lifecycle-" + strings.Repeat("d", 64),
				ResourceBindingDigest: strings.Repeat("e", 64), OperationDigest: strings.Repeat("f", 64),
				CommandDigest: strings.Repeat("1", 64), LeaseID: "lease-1", FencingToken: 2,
				LeaseExpiresAt: time.Date(2030, 8, 14, 12, 5, 0, 0, time.UTC),
			},
			Blueprint:                 PreviewObservationBlueprint{BlueprintID: "node-web", Version: "1", Digest: strings.Repeat("2", 64)},
			PreviewURL:                "https://branch.preview.example.test/",
			CommitSet:                 []PreviewObservationCommit{{RepositoryID: "web", CommitSHA: strings.Repeat("3", 40)}},
			CommitSetDigest:           strings.Repeat("4", 64),
			OriginalExpiresAt:         time.Date(2030, 8, 14, 18, 0, 0, 0, time.UTC),
			CurrentEffectiveExpiresAt: time.Date(2030, 8, 14, 18, 0, 0, 0, time.UTC),
			DesiredExpiresAt:          &desired,
		},
	}
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"command":"preview_lifecycle"`, `"preview_lifecycle":`, `"action":"extend"`} {
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("wire command missing %s: %s", expected, raw)
		}
	}
	for _, forbidden := range []string{"controller_url", "authorization", "headers", "credential"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("wire command leaked local controller field %q: %s", forbidden, raw)
		}
	}
}

func TestPreviewLifecycleResultHasNoGenericBody(t *testing.T) {
	event := PreviewLifecycleResultEvent{
		Kind: EventPreviewLifecycleResult,
		PreviewLifecycleResultAuthority: PreviewLifecycleResultAuthority{PreviewLifecycleOperationAuthority: PreviewLifecycleOperationAuthority{
			SchemaVersion:            PreviewLifecycleSchemaVersion,
			PreviewLifecycleEffectID: "effect-1",
		}},
		CommandMessageID: "preview-lifecycle-command-" + strings.Repeat("a", 64),
		ObservedAt:       time.Date(2030, 8, 14, 12, 1, 0, 0, time.UTC),
		Outcome:          PreviewLifecycleResultOutcome{Kind: PreviewLifecycleUncertain, Code: PreviewLifecycleControllerTimeout},
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kind":"preview_lifecycle_result"`) || strings.Contains(string(raw), `"body"`) {
		t.Fatalf("unsafe preview lifecycle result shape: %s", raw)
	}
}

func TestPreviewLifecycleCanonicalDigestVectorMatchesControlPlane(t *testing.T) {
	ms := int(time.Millisecond)
	cases := map[string]struct {
		original, current, desired, lease   time.Time
		resourceBinding, operation, command string
	}{
		"whole seconds": {
			time.Date(2030, 8, 21, 12, 0, 0, 0, time.UTC), time.Date(2030, 8, 22, 12, 0, 0, 0, time.UTC),
			time.Date(2030, 8, 23, 12, 0, 0, 0, time.UTC), time.Date(2030, 8, 22, 12, 5, 0, 0, time.UTC),
			"c70b27b9663ff6c907ee163addb841d7528cba987fe3731d6aa1c96702260415",
			"9247a48020c5ce346dc703a4af10cc1675c78abf26ea015d011f7b65a67dafa6",
			"ea0fc3571f79a8ebad6a883a2427079448fe5235c202091452e6b67420d35748",
		},
		"zero-ending millisecond fractions": {
			time.Date(2030, 8, 21, 12, 0, 0, 520*ms, time.UTC), time.Date(2030, 8, 22, 12, 0, 0, 500*ms, time.UTC),
			time.Date(2030, 8, 23, 12, 0, 0, 100*ms, time.UTC), time.Date(2030, 8, 22, 12, 5, 0, 520*ms, time.UTC),
			"f0c683d6f02ddb02982b0ac78795350f1b2943325e6f9eaeb2d1ed15e7a53e52",
			"58c81e6395ef43f1e44385a95fe7a19534f7e36cbf94dd043a0d154b66e2c1b8",
			"da63a3c987b180eae64704e1e48f58d5b4ec623526751127d588cdfff3d77691",
		},
	}
	for name, vector := range cases {
		t.Run(name, func(t *testing.T) {
			desired := vector.desired
			operation := PreviewLifecycleOperation{
				PreviewLifecycleOperationAuthority: PreviewLifecycleOperationAuthority{
					SchemaVersion: 1, PreviewLifecycleEffectID: "preview-lifecycle-effect-1", PreviewLifecycleEffectRevision: 1,
					PreviewLifecycleOperationID: "preview-lifecycle-operation-" + strings.Repeat("4", 64),
					OrganizationID:              "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-run-1",
					PreviewEvidenceID: "preview-evidence-1", PreviewObservationID: "preview-observation-1",
					PreviewObservationDigest: strings.Repeat("2", 64), ExecutionManifestID: "execution-manifest-1",
					ExecutionManifestDigest: strings.Repeat("3", 64), Action: PreviewLifecycleExtend,
					IdempotencyKey: "preview-lifecycle-" + strings.Repeat("5", 64),
					LeaseID:        "preview-lifecycle-lease-1", FencingToken: 7,
					LeaseExpiresAt: vector.lease,
				},
				Blueprint:  PreviewObservationBlueprint{BlueprintID: "blueprint-1", Version: "2026.08.1", Digest: strings.Repeat("1", 64)},
				PreviewURL: "https://preview.example.test/",
				CommitSet: []PreviewObservationCommit{
					{RepositoryID: "repository-a", CommitSHA: strings.Repeat("a", 40)},
					{RepositoryID: "repository-b", CommitSHA: strings.Repeat("b", 40)},
				},
				OriginalExpiresAt:         vector.original,
				CurrentEffectiveExpiresAt: vector.current,
				DesiredExpiresAt:          &desired,
			}
			operation.CommitSetDigest = PreviewLifecycleCommitSetDigest(operation.CommitSet)
			operation.ResourceBindingDigest = PreviewLifecycleResourceBindingDigest(operation)
			operation.OperationDigest = PreviewLifecycleOperationDigest(operation)
			operation.CommandDigest = PreviewLifecycleCommandDigest(operation)
			if operation.CommitSetDigest != "cf63268a5326a55193214033aab6908b3700dc9beea53426598b1fd929a8d361" ||
				operation.ResourceBindingDigest != vector.resourceBinding ||
				operation.OperationDigest != vector.operation ||
				operation.CommandDigest != vector.command {
				t.Fatalf("digest vector mismatch: %+v", operation)
			}
			shifted := operation
			shifted.LeaseExpiresAt = operation.LeaseExpiresAt.Add(time.Millisecond)
			if PreviewLifecycleCommandDigest(shifted) == vector.command {
				t.Fatal("a lease one millisecond away kept the command digest")
			}
			shifted = operation
			shifted.OriginalExpiresAt = operation.OriginalExpiresAt.Add(time.Millisecond)
			if PreviewLifecycleResourceBindingDigest(shifted) == vector.resourceBinding {
				t.Fatal("an original expiry one millisecond away kept the resource binding digest")
			}
		})
	}
}
