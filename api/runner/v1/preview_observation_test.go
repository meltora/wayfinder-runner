package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPreviewObservationCommandWireShapeContainsNoNetworkAuthority(t *testing.T) {
	command := CommandPayload{
		Command: CmdPreviewObservation,
		PreviewObservation: &PreviewObservationOperation{
			PreviewObservationOperationAuthority: PreviewObservationOperationAuthority{
				SchemaVersion:              PreviewObservationOperationAuthorityVersion,
				PreviewObservationEffectID: "preview-effect-1", PreviewEvidenceID: "preview-evidence-1",
				ReviewReadyRequestID: "review-ready-1", ProjectID: "project-1", WorkflowRunID: "workflow-1",
				AttemptID: "attempt-1", ExecutionManifestID: "manifest-1", ExecutionManifestDigest: strings.Repeat("a", 64),
				LeaseID: "preview-operation-1", FencingToken: 3, LeaseExpiresAt: time.Date(2030, 8, 14, 12, 4, 0, 0, time.UTC),
			},
			Blueprint:  PreviewObservationBlueprint{BlueprintID: "blueprint-1", Version: "1", Digest: strings.Repeat("b", 64)},
			PreviewURL: "https://feature.preview.example.test/", PreviewExpiresAt: time.Date(2030, 8, 14, 13, 0, 0, 0, time.UTC),
			CommitSet:              []PreviewObservationCommit{{RepositoryID: "web", CommitSHA: strings.Repeat("c", 40)}},
			RequiredHealthCheckIDs: []string{"http-smoke"},
		},
	}
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"headers", "credential", "health_check_path", "request_body", "command_line"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("command leaked forbidden field %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(string(raw), `"command":"preview_observation"`) ||
		!strings.Contains(string(raw), `"preview_observation"`) {
		t.Fatalf("unexpected command wire shape: %s", raw)
	}
}

func TestPreviewObservationResultWireShapeIsNormalized(t *testing.T) {
	event := PreviewObservationResultEvent{
		Kind: EventPreviewObservationResult,
		PreviewObservationOperationAuthority: PreviewObservationOperationAuthority{
			SchemaVersion:              PreviewObservationOperationAuthorityVersion,
			PreviewObservationEffectID: "preview-effect-1", PreviewEvidenceID: "preview-evidence-1",
			ReviewReadyRequestID: "review-ready-1", ProjectID: "project-1", WorkflowRunID: "workflow-1",
			AttemptID: "attempt-1", ExecutionManifestID: "manifest-1", ExecutionManifestDigest: strings.Repeat("a", 64),
			LeaseID: "preview-operation-1", FencingToken: 3, LeaseExpiresAt: time.Date(2030, 8, 14, 12, 4, 0, 0, time.UTC),
		},
		CommandMessageID: "preview-command-1", ObservedAt: time.Date(2030, 8, 14, 12, 1, 0, 0, time.UTC),
		Outcome: PreviewObservationResultOutcome{
			Kind: PreviewObservationObserved,
			Observation: &PreviewObservation{
				SchemaVersion: PreviewObservationSchemaVersion,
				PreviewURL:    "https://feature.preview.example.test/",
				CommitSet:     []PreviewObservationCommit{{RepositoryID: "web", CommitSHA: strings.Repeat("c", 40)}},
				Checks:        []PreviewObservationCheck{{CheckID: "http-smoke", Status: PreviewObservationCheckPassed, HTTPStatus: 200, DurationMS: 42}},
			},
		},
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kind":"preview_observation_result"`) || strings.Contains(string(raw), "body") {
		t.Fatalf("unexpected result wire shape: %s", raw)
	}
}
