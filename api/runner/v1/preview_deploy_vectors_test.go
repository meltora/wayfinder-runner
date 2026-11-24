package v1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	previewDeployVectorsPath      = "testdata/preview_deploy_v1_vectors.json"
	previewDeployVectorsUpdateEnv = "PREVIEW_DEPLOY_VECTORS_UPDATE"
	previewDeployVectorBlueprint  = `{"schema_version":1,"services":[{"name":"db","role":"database","image":"docker.io/library/postgres@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","env":{"POSTGRES_PASSWORD":{"kind":"generated","generator":"random_secret"}}}],"app":{"image":"ghcr.io/acme/node@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","working_directory":".","install":["pnpm install --frozen-lockfile"],"build":["pnpm build"],"migrate":["pnpm db:migrate:deploy"],"seed":["pnpm db:seed"],"start":"pnpm start","port":3000,"health_checks":[{"id":"home","path":"/","expected_status":200}],"env":{"DATABASE_URL":{"kind":"generated","generator":"postgres_url"},"STRIPE_SECRET_KEY":{"kind":"secret","note":"Test mode key"},"FEATURE_FLAG":{"kind":"fake","value":"enabled"},"SENTRY_DSN":{"kind":"disabled"}}},"limits":{"memory_mb":1024,"cpus":1,"max_lifetime_minutes":1440},"egress":{"mode":"none"}}`
)

type PreviewDeployVectorFile struct {
	SchemaVersion int                          `json:"schema_version"`
	Generator     string                       `json:"generator"`
	Valid         []PreviewDeployValidVector   `json:"valid"`
	Invalid       []PreviewDeployInvalidVector `json:"invalid"`
	Stateful      []PreviewDeployInvalidVector `json:"stateful"`
}

type PreviewDeployValidVector struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Given       string                  `json:"given"`
	Envelope    json.RawMessage         `json:"envelope"`
	Expected    PreviewDeployVectorWant `json:"expected"`
}

type PreviewDeployVectorWant struct {
	Command                string `json:"command"`
	BlueprintDigest        string `json:"blueprint_digest"`
	CommitSetDigest        string `json:"commit_set_digest"`
	ResourceBindingDigest  string `json:"resource_binding_digest"`
	OperationDigest        string `json:"operation_digest"`
	CommandDigest          string `json:"command_digest"`
	ResultEventID          string `json:"result_event_id"`
	HostedDestroyedEventID string `json:"hosted_destroyed_event_id,omitempty"`
}

type PreviewDeployInvalidVector struct {
	Name            string          `json:"name"`
	Rule            string          `json:"rule"`
	Given           string          `json:"given"`
	Envelope        json.RawMessage `json:"envelope"`
	ExpectedOutcome string          `json:"expected_outcome"`
}

func vectorTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func vectorBlueprintDigest(content string) string {
	digest, err := PreviewBlueprintDigestV1([]byte(content))
	if err != nil {
		panic(err)
	}
	return digest
}

func vectorDeployOperation() PreviewDeployOperation {
	return PreviewDeployOperation{
		PreviewDeployOperationAuthority: PreviewDeployOperationAuthority{
			SchemaVersion:               PreviewDeploySchemaVersion,
			PreviewDeployEffectID:       "preview-deploy-effect-1",
			PreviewDeployEffectRevision: 1,
			PreviewDeployOperationID:    "preview-deploy-operation-" + strings.Repeat("a", 64),
			PreviewID:                   "preview-1",
			OrganizationID:              "organization-1",
			ProjectID:                   "project-1",
			WorkflowRunID:               "workflow-1",
			ExecutionManifestID:         "manifest-1",
			ExecutionManifestDigest:     strings.Repeat("c", 64),
			IdempotencyKey:              "preview-deploy-" + strings.Repeat("d", 64),
			LeaseID:                     "lease-1",
			FencingToken:                4,
			LeaseExpiresAt:              vectorTime("2099-01-01T00:45:00Z"),
		},
		Blueprint: PreviewDeployBlueprint{
			BlueprintID: "blueprint-version-1",
			Version:     "3",
			Digest:      vectorBlueprintDigest(previewDeployVectorBlueprint),
			Content:     json.RawMessage(previewDeployVectorBlueprint),
		},
		CommitSet: []PreviewObservationCommit{
			{RepositoryID: "api", CommitSHA: strings.Repeat("1", 40)},
			{RepositoryID: "web", CommitSHA: strings.Repeat("2", 40)},
		},
		PreviewKey: "abcdefghijklmnopqrstuvwxyz",
		ExpiresAt:  vectorTime("2099-01-02T00:00:00.123Z"),
	}
}

func sealVectorDeploy(operation PreviewDeployOperation) PreviewDeployOperation {
	operation.CommitSet = append([]PreviewObservationCommit(nil), operation.CommitSet...)
	operation.CommitSetDigest = PreviewLifecycleCommitSetDigest(operation.CommitSet)
	operation.ResourceBindingDigest = PreviewDeployResourceBindingDigest(operation)
	operation.OperationDigest = PreviewDeployOperationDigest(operation)
	operation.CommandDigest = PreviewDeployCommandDigest(operation)
	return operation
}

func vectorLifecycleOperation(action PreviewLifecycleAction, deploy PreviewDeployOperation) PreviewLifecycleOperation {
	operation := PreviewLifecycleOperation{
		PreviewLifecycleOperationAuthority: PreviewLifecycleOperationAuthority{
			SchemaVersion:                  PreviewLifecycleSchemaVersion,
			PreviewLifecycleEffectID:       "preview-lifecycle-effect-1",
			PreviewLifecycleEffectRevision: 1,
			PreviewLifecycleOperationID:    "preview-lifecycle-operation-" + strings.Repeat("a", 64),
			OrganizationID:                 deploy.OrganizationID,
			ProjectID:                      deploy.ProjectID,
			WorkflowRunID:                  deploy.WorkflowRunID,
			ExecutionManifestID:            deploy.ExecutionManifestID,
			ExecutionManifestDigest:        deploy.ExecutionManifestDigest,
			Action:                         action,
			IdempotencyKey:                 "preview-lifecycle-" + strings.Repeat("e", 64),
			LeaseID:                        "lease-2",
			FencingToken:                   5,
			LeaseExpiresAt:                 vectorTime("2099-01-01T12:00:00Z"),
		},
		PreviewID:                 deploy.PreviewID,
		PreviewKey:                deploy.PreviewKey,
		Blueprint:                 deploy.Blueprint.Identity(),
		CommitSet:                 append([]PreviewObservationCommit(nil), deploy.CommitSet...),
		OriginalExpiresAt:         deploy.ExpiresAt,
		CurrentEffectiveExpiresAt: deploy.ExpiresAt,
	}
	if action == PreviewLifecycleExtend {
		desired := deploy.ExpiresAt.Add(24 * time.Hour)
		operation.DesiredExpiresAt = &desired
	}
	return operation
}

func sealVectorLifecycle(operation PreviewLifecycleOperation) PreviewLifecycleOperation {
	operation.CommitSet = append([]PreviewObservationCommit(nil), operation.CommitSet...)
	operation.CommitSetDigest = PreviewLifecycleCommitSetDigest(operation.CommitSet)
	operation.ResourceBindingDigest = PreviewHostedLifecycleResourceBindingDigest(operation)
	operation.OperationDigest = PreviewHostedLifecycleOperationDigest(operation)
	operation.CommandDigest = PreviewLifecycleCommandDigest(operation)
	return operation
}

func vectorEnvelope(id string, payload CommandPayload) json.RawMessage {
	raw, err := json.Marshal(Envelope{
		Version: ProtocolVersion, Type: TypeCommand, ID: id,
		SentAt: vectorTime("2099-01-01T00:00:00Z"), RunnerID: "runner-1", Payload: payload,
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func vectorDeployEnvelope(id string, operation PreviewDeployOperation) json.RawMessage {
	return vectorEnvelope(id, CommandPayload{Command: CmdPreviewDeploy, PreviewDeploy: &operation})
}

func vectorLifecycleEnvelope(id string, operation PreviewLifecycleOperation) json.RawMessage {
	return vectorEnvelope(id, CommandPayload{Command: CmdPreviewLifecycle, PreviewLifecycle: &operation})
}

func vectorDeployCommandID(suffix string) string {
	return "preview-deploy-command-" + strings.Repeat(suffix, 64)
}

func vectorLifecycleCommandID(suffix string) string {
	return "preview-lifecycle-command-" + strings.Repeat(suffix, 64)
}

func vectorLifecycleResultEventID(commandMessageID string) string {
	digest := sha256.Sum256([]byte(commandMessageID))
	return "preview-lifecycle-result-" + hex.EncodeToString(digest[:])
}

func validDeployVector(name, description, id string, operation PreviewDeployOperation) PreviewDeployValidVector {
	return PreviewDeployValidVector{
		Name: name, Description: description, Given: "trusted_commit_set",
		Envelope: vectorDeployEnvelope(id, operation),
		Expected: PreviewDeployVectorWant{
			Command: CmdPreviewDeploy, BlueprintDigest: operation.Blueprint.Digest,
			CommitSetDigest: operation.CommitSetDigest, ResourceBindingDigest: operation.ResourceBindingDigest,
			OperationDigest: operation.OperationDigest, CommandDigest: operation.CommandDigest,
			ResultEventID: PreviewDeployResultEventID(operation.IdempotencyKey),
		},
	}
}

func validLifecycleVector(name, description, id string, operation PreviewLifecycleOperation) PreviewDeployValidVector {
	want := PreviewDeployVectorWant{
		Command: CmdPreviewLifecycle, BlueprintDigest: operation.Blueprint.Digest,
		CommitSetDigest: operation.CommitSetDigest, ResourceBindingDigest: operation.ResourceBindingDigest,
		OperationDigest: operation.OperationDigest, CommandDigest: operation.CommandDigest,
		ResultEventID: vectorLifecycleResultEventID(id),
	}
	if operation.Action == PreviewLifecycleDestroy {
		want.HostedDestroyedEventID = PreviewHostedDestroyedEventID(operation.PreviewID)
	}
	return PreviewDeployValidVector{
		Name: name, Description: description, Given: "deployed:deploy_two_repositories",
		Envelope: vectorLifecycleEnvelope(id, operation), Expected: want,
	}
}

func invalidVector(name, rule string, envelope json.RawMessage) PreviewDeployInvalidVector {
	return PreviewDeployInvalidVector{Name: name, Rule: rule, Given: "none", Envelope: envelope, ExpectedOutcome: "invalid_command"}
}

func mutatedDeploy(mutate func(*PreviewDeployOperation)) PreviewDeployOperation {
	operation := vectorDeployOperation()
	operation.CommitSet = append([]PreviewObservationCommit(nil), operation.CommitSet...)
	mutate(&operation)
	return sealVectorDeploy(operation)
}

func mutatedLifecycle(action PreviewLifecycleAction, mutate func(*PreviewLifecycleOperation)) PreviewLifecycleOperation {
	operation := vectorLifecycleOperation(action, sealVectorDeploy(vectorDeployOperation()))
	mutate(&operation)
	return sealVectorLifecycle(operation)
}

func BuildPreviewDeployVectorFile() PreviewDeployVectorFile {
	deploy := sealVectorDeploy(vectorDeployOperation())
	single := mutatedDeploy(func(operation *PreviewDeployOperation) {
		operation.PreviewDeployEffectRevision = 2
		operation.PreviewDeployOperationID = "preview-deploy-operation-" + strings.Repeat("b", 64)
		operation.PreviewID = "preview-2"
		operation.PreviewKey = "zyxwvutsrqponmlkjihgfedcba"
		operation.IdempotencyKey = "preview-deploy-" + strings.Repeat("f", 64)
		operation.Blueprint.Version = "9007199254740991"
		operation.CommitSet = []PreviewObservationCommit{{RepositoryID: "monorepo", CommitSHA: strings.Repeat("ab", 20)}}
		operation.ExpiresAt = vectorTime("2099-01-01T00:45:00.001Z")
	})
	withEdgeHost := mutatedDeploy(func(operation *PreviewDeployOperation) {
		operation.IdempotencyKey = "preview-deploy-" + strings.Repeat("0", 64)
		operation.PreviewDeployOperationID = "preview-deploy-operation-" + strings.Repeat("0", 64)
		operation.PreviewID = "preview-3"
		operation.EdgeHost = operation.PreviewKey + ".previews.example.com"
	})
	extend := sealVectorLifecycle(vectorLifecycleOperation(PreviewLifecycleExtend, deploy))
	destroy := sealVectorLifecycle(vectorLifecycleOperation(PreviewLifecycleDestroy, deploy))
	stale := vectorDeployOperation()
	stale.ExpiresAt = stale.ExpiresAt.Add(time.Hour)
	stale = sealVectorDeploy(stale)
	stale.CommitSet = deploy.CommitSet
	tampered := deploy
	tampered.ExpiresAt = tampered.ExpiresAt.Add(time.Hour)
	unsorted := mutatedDeploy(func(operation *PreviewDeployOperation) {
		operation.CommitSet[0], operation.CommitSet[1] = operation.CommitSet[1], operation.CommitSet[0]
	})
	oversize := `{"schema_version":1,"padding":"` + strings.Repeat("a", 256<<10) + `"}`
	untrusted := mutatedDeploy(func(operation *PreviewDeployOperation) {
		operation.IdempotencyKey = "preview-deploy-" + strings.Repeat("9", 64)
		operation.PreviewDeployOperationID = "preview-deploy-operation-" + strings.Repeat("9", 64)
		operation.PreviewID = "preview-9"
		operation.CommitSet = []PreviewObservationCommit{
			{RepositoryID: "api", CommitSHA: strings.Repeat("3", 40)},
			{RepositoryID: "web", CommitSHA: strings.Repeat("2", 40)},
		}
	})
	return PreviewDeployVectorFile{
		SchemaVersion: 1,
		Generator:     previewDeployVectorsUpdateEnv + "=1 go test ./api/runner/v1 -run TestPreviewDeployV1VectorsAreCurrent",
		Valid: []PreviewDeployValidVector{
			validDeployVector("deploy_two_repositories", "two repositories, millisecond expires_at, empty edge_host", vectorDeployCommandID("1"), deploy),
			validDeployVector("deploy_single_repository_max_version", "one repository, largest safe blueprint version, expires_at one millisecond after the lease", vectorDeployCommandID("2"), single),
			validDeployVector("deploy_with_edge_host", "edge_host is preview_key followed by the preview base domain", vectorDeployCommandID("3"), withEdgeHost),
			validLifecycleVector("hosted_extend_with_desired_expiry", "extend sets desired_expires_at to current_effective_expires_at plus 24 hours", vectorLifecycleCommandID("1"), extend),
			validLifecycleVector("hosted_destroy_without_desired_expiry", "destroy omits desired_expires_at", vectorLifecycleCommandID("2"), destroy),
		},
		Invalid: []PreviewDeployInvalidVector{
			invalidVector("deploy_bad_command_id", "envelope id must match ^preview-deploy-command-[0-9a-f]{64}$", vectorDeployEnvelope("preview-deploy-command-"+strings.Repeat("A", 64), deploy)),
			invalidVector("deploy_expires_at_sub_millisecond", "expires_at has at most millisecond precision", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.ExpiresAt = operation.ExpiresAt.Add(456 * time.Microsecond)
			}))),
			invalidVector("deploy_expires_at_equals_lease", "expires_at must be after lease_expires_at", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.ExpiresAt = operation.LeaseExpiresAt
			}))),
			invalidVector("deploy_expires_at_before_lease", "expires_at must be after lease_expires_at", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.ExpiresAt = operation.LeaseExpiresAt.Add(-time.Millisecond)
			}))),
			invalidVector("deploy_blueprint_content_over_256_kib", "blueprint.content is at most 262144 bytes", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.Blueprint.Content = json.RawMessage(oversize)
			}))),
			invalidVector("deploy_blueprint_digest_mismatch", "blueprint.digest is the canonical digest of blueprint.content", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.Blueprint.Digest = strings.Repeat("e", 64)
			}))),
			invalidVector("deploy_blueprint_version_leading_zero", "blueprint.version matches ^[1-9][0-9]{0,15}$", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.Blueprint.Version = "03"
			}))),
			invalidVector("deploy_blueprint_version_not_decimal", "blueprint.version matches ^[1-9][0-9]{0,15}$", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.Blueprint.Version = "2026.08.1"
			}))),
			invalidVector("deploy_commit_set_unsorted", "commit_set is sorted by repository_id", vectorDeployEnvelope(vectorDeployCommandID("4"), unsorted)),
			invalidVector("deploy_preview_key_invalid", "preview_key matches ^[a-z2-7]{26}$", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.PreviewKey = "abcdefghijklmnopqrstuvwxy1"
			}))),
			invalidVector("deploy_idempotency_key_invalid", "idempotency_key matches ^preview-deploy-[0-9a-f]{64}$", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.IdempotencyKey = "preview-deploy-" + strings.Repeat("D", 64)
			}))),
			invalidVector("deploy_operation_id_invalid", "preview_deploy_operation_id matches ^preview-deploy-operation-[0-9a-f]{64}$", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.PreviewDeployOperationID = "preview-deploy-operation-1"
			}))),
			invalidVector("deploy_edge_host_foreign_key", "edge_host starts with preview_key and a dot", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.EdgeHost = "zyxwvutsrqponmlkjihgfedcba.previews.example.com"
			}))),
			invalidVector("deploy_operation_digest_not_resealed", "operation_digest binds stable intent; a changed expires_at without resealing is refused", vectorDeployEnvelope(vectorDeployCommandID("4"), tampered)),
			invalidVector("hosted_extend_without_desired_expiry", "extend requires desired_expires_at", vectorLifecycleEnvelope(vectorLifecycleCommandID("3"), mutatedLifecycle(PreviewLifecycleExtend, func(operation *PreviewLifecycleOperation) {
				operation.DesiredExpiresAt = nil
			}))),
			invalidVector("hosted_destroy_with_desired_expiry", "destroy forbids desired_expires_at", vectorLifecycleEnvelope(vectorLifecycleCommandID("3"), mutatedLifecycle(PreviewLifecycleDestroy, func(operation *PreviewLifecycleOperation) {
				desired := operation.CurrentEffectiveExpiresAt.Add(24 * time.Hour)
				operation.DesiredExpiresAt = &desired
			}))),
			invalidVector("hosted_lifecycle_bad_command_id", "envelope id must match ^preview-lifecycle-command-[0-9a-f]{64}$", vectorLifecycleEnvelope("preview-lifecycle-command-1", destroy)),
			invalidVector("hosted_lifecycle_blueprint_version_not_decimal", "blueprint.version matches ^[1-9][0-9]{0,15}$", vectorLifecycleEnvelope(vectorLifecycleCommandID("3"), mutatedLifecycle(PreviewLifecycleDestroy, func(operation *PreviewLifecycleOperation) {
				operation.Blueprint.Version = "v3"
			}))),
		},
		Stateful: []PreviewDeployInvalidVector{
			{
				Name: "deploy_untrusted_commit_set", Rule: "a commit_set the runner never recorded for the execution manifest fails the commit_set phase",
				Given: "trusted_commit_set:deploy_two_repositories", Envelope: vectorDeployEnvelope(vectorDeployCommandID("5"), untrusted),
				ExpectedOutcome: "failed:commit_set",
			},
			{
				Name: "deploy_same_key_different_operation", Rule: "a resealed operation under an accepted idempotency_key is a conflict",
				Given: "deployed:deploy_two_repositories", Envelope: vectorDeployEnvelope(vectorDeployCommandID("6"), stale),
				ExpectedOutcome: "command_conflict",
			},
		},
	}
}

func encodePreviewDeployVectorFile(t *testing.T, file PreviewDeployVectorFile) []byte {
	t.Helper()
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(file); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestPreviewDeployV1VectorsAreCurrent(t *testing.T) {
	want := encodePreviewDeployVectorFile(t, BuildPreviewDeployVectorFile())
	if os.Getenv(previewDeployVectorsUpdateEnv) == "1" {
		if err := os.WriteFile(previewDeployVectorsPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(previewDeployVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; regenerate with %s=1", previewDeployVectorsPath, previewDeployVectorsUpdateEnv)
	}
}

func TestPreviewDeployV1VectorDigestsRecomputeFromTheWire(t *testing.T) {
	var file PreviewDeployVectorFile
	raw, err := os.ReadFile(previewDeployVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Valid) != 5 || len(file.Invalid) != 18 || len(file.Stateful) != 2 {
		t.Fatalf("vector counts = %d valid, %d invalid, %d stateful", len(file.Valid), len(file.Invalid), len(file.Stateful))
	}
	for _, vector := range file.Valid {
		t.Run(vector.Name, func(t *testing.T) {
			var envelope struct {
				ID      string         `json:"id"`
				Payload CommandPayload `json:"payload"`
			}
			if err := json.Unmarshal(vector.Envelope, &envelope); err != nil {
				t.Fatal(err)
			}
			var got PreviewDeployVectorWant
			switch envelope.Payload.Command {
			case CmdPreviewDeploy:
				operation := *envelope.Payload.PreviewDeploy
				digest, err := PreviewBlueprintDigestV1(operation.Blueprint.Content)
				if err != nil {
					t.Fatal(err)
				}
				got = PreviewDeployVectorWant{
					Command: CmdPreviewDeploy, BlueprintDigest: digest,
					CommitSetDigest:       PreviewLifecycleCommitSetDigest(operation.CommitSet),
					ResourceBindingDigest: PreviewDeployResourceBindingDigest(operation),
					OperationDigest:       PreviewDeployOperationDigest(operation),
					CommandDigest:         PreviewDeployCommandDigest(operation),
					ResultEventID:         PreviewDeployResultEventID(operation.IdempotencyKey),
				}
			case CmdPreviewLifecycle:
				operation := *envelope.Payload.PreviewLifecycle
				got = PreviewDeployVectorWant{
					Command: CmdPreviewLifecycle, BlueprintDigest: operation.Blueprint.Digest,
					CommitSetDigest:       PreviewLifecycleCommitSetDigest(operation.CommitSet),
					ResourceBindingDigest: PreviewHostedLifecycleResourceBindingDigest(operation),
					OperationDigest:       PreviewHostedLifecycleOperationDigest(operation),
					CommandDigest:         PreviewLifecycleCommandDigest(operation),
					ResultEventID:         vectorLifecycleResultEventID(envelope.ID),
				}
				if operation.Action == PreviewLifecycleDestroy {
					got.HostedDestroyedEventID = PreviewHostedDestroyedEventID(operation.PreviewID)
				}
			default:
				t.Fatalf("command = %q", envelope.Payload.Command)
			}
			if got != vector.Expected {
				t.Fatalf("recomputed %+v, want %+v", got, vector.Expected)
			}
		})
	}
}
