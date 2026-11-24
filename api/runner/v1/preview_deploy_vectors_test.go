package v1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	Conventions   map[string]string            `json:"conventions"`
	Valid         []PreviewDeployValidVector   `json:"valid"`
	Invalid       []PreviewDeployInvalidVector `json:"invalid"`
	Stateful      []PreviewDeployInvalidVector `json:"stateful"`
	HostedResults []PreviewHostedResultVector  `json:"hosted_lifecycle_results"`
}

type PreviewHostedResultVector struct {
	Name            string                  `json:"name"`
	Description     string                  `json:"description"`
	Given           string                  `json:"given"`
	Controller      string                  `json:"controller"`
	CommandEnvelope json.RawMessage         `json:"command_envelope"`
	ResultEnvelope  json.RawMessage         `json:"result_envelope"`
	Expected        PreviewHostedResultWant `json:"expected"`
}

type PreviewHostedResultWant struct {
	ResultEventID           string                      `json:"result_event_id"`
	OutcomeKind             PreviewLifecycleOutcomeKind `json:"outcome_kind"`
	OutcomeCode             PreviewLifecycleErrorCode   `json:"outcome_code,omitempty"`
	ResourceBindingDigest   string                      `json:"resource_binding_digest"`
	OperationDigest         string                      `json:"operation_digest"`
	CommandDigest           string                      `json:"command_digest"`
	ControllerReceiptDigest string                      `json:"controller_receipt_digest,omitempty"`
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

func oversizeVectorBlueprint() string {
	decoder := json.NewDecoder(strings.NewReader(previewDeployVectorBlueprint))
	decoder.UseNumber()
	var blueprint map[string]any
	if err := decoder.Decode(&blueprint); err != nil {
		panic(err)
	}
	filler := map[string]any{"kind": "fake", "value": strings.Repeat("filler ", 146) + "ok"}
	envs := []map[string]any{
		blueprint["app"].(map[string]any)["env"].(map[string]any),
		blueprint["services"].([]any)[0].(map[string]any)["env"].(map[string]any),
	}
	for index, env := range envs {
		for key := 0; len(env) < 128; key++ {
			env[fmt.Sprintf("FILLER_%d_%03d", index, key)] = filler
		}
	}
	raw, err := json.Marshal(blueprint)
	if err != nil {
		panic(err)
	}
	if len(raw) <= 256<<10 {
		panic(fmt.Sprintf("oversize blueprint is only %d bytes", len(raw)))
	}
	return string(raw)
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
	return vectorEnvelopeSentAt(id, "2099-01-01T00:00:00Z", payload)
}

func vectorEnvelopeSentAt(id, sentAt string, payload CommandPayload) json.RawMessage {
	raw, err := json.Marshal(Envelope{
		Version: ProtocolVersion, Type: TypeCommand, ID: id,
		SentAt: vectorTime(sentAt), RunnerID: "runner-1", Payload: payload,
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
		want.HostedDestroyedEventID = PreviewHostedDestroyedEventID(operation.PreviewID,
			sealVectorDeploy(vectorDeployOperation()).IdempotencyKey, PreviewHostedDestroyOutcomeDestroyed)
	}
	return PreviewDeployValidVector{
		Name: name, Description: description, Given: "deployed:deploy_two_repositories",
		Envelope: vectorLifecycleEnvelope(id, operation), Expected: want,
	}
}

func hostedResultVector(
	name, description, given, controller, commandID, commandSentAt string,
	operation PreviewLifecycleOperation,
	observedAt, sentAt string,
	outcome PreviewLifecycleResultOutcome,
) PreviewHostedResultVector {
	if outcome.Kind == PreviewLifecycleSucceeded {
		outcome.ResourceBindingDigest = operation.ResourceBindingDigest
		outcome.ControllerReceiptDigest = PreviewHostedLifecycleReceiptDigest(operation, *outcome.ResourceState)
	}
	eventID := vectorLifecycleResultEventID(commandID)
	result, err := json.Marshal(Envelope{
		Version: ProtocolVersion, Type: TypeEvent, ID: eventID,
		SentAt: vectorTime(sentAt), RunnerID: "runner-1",
		Payload: PreviewLifecycleResultEvent{
			Kind:                            EventPreviewLifecycleResult,
			PreviewLifecycleResultAuthority: PreviewLifecycleResultAuthorityFromOperation(operation),
			CommandMessageID:                commandID,
			ObservedAt:                      vectorTime(observedAt),
			Outcome:                         outcome,
		},
	})
	if err != nil {
		panic(err)
	}
	return PreviewHostedResultVector{
		Name: name, Description: description, Given: given, Controller: controller,
		CommandEnvelope: vectorEnvelopeSentAt(commandID, commandSentAt, CommandPayload{Command: CmdPreviewLifecycle, PreviewLifecycle: &operation}),
		ResultEnvelope:  result,
		Expected: PreviewHostedResultWant{
			ResultEventID: eventID, OutcomeKind: outcome.Kind, OutcomeCode: outcome.Code,
			ResourceBindingDigest: operation.ResourceBindingDigest, OperationDigest: operation.OperationDigest,
			CommandDigest: operation.CommandDigest, ControllerReceiptDigest: outcome.ControllerReceiptDigest,
		},
	}
}

func hostedResultVectors(extend, destroy PreviewLifecycleOperation) []PreviewHostedResultVector {
	blueprint := extend.Blueprint
	overlong := mutatedLifecycle(PreviewLifecycleExtend, func(operation *PreviewLifecycleOperation) {
		operation.PreviewLifecycleOperationID = "preview-lifecycle-operation-" + strings.Repeat("f", 64)
		operation.IdempotencyKey = "preview-lifecycle-" + strings.Repeat("f", 64)
		desired := operation.CurrentEffectiveExpiresAt.Add(8 * 24 * time.Hour)
		operation.DesiredExpiresAt = &desired
	})
	return []PreviewHostedResultVector{
		hostedResultVector("hosted_extend_succeeded", "the command of hosted_extend_with_desired_expiry; the runner fills resource_binding_digest and computes controller_receipt_digest",
			"deployed:deploy_two_repositories", "extends", vectorLifecycleCommandID("1"), "2099-01-01T00:00:00Z", extend,
			"2099-01-01T00:00:01.234Z", "2099-01-01T00:00:01.240Z",
			PreviewLifecycleResultOutcome{
				Kind: PreviewLifecycleSucceeded,
				ResourceState: &PreviewLifecycleResourceState{
					Kind: PreviewLifecycleResourceActive, Blueprint: &blueprint,
					CommitSet: extend.CommitSet, ExpiresAt: extend.DesiredExpiresAt,
				},
			}),
		hostedResultVector("hosted_destroy_already_absent", "the command of hosted_destroy_without_desired_expiry for a preview the runner does not host",
			"none", "not_called", vectorLifecycleCommandID("2"), "2099-01-01T00:00:00Z", destroy,
			"2099-01-01T00:00:01.5Z", "2099-01-01T00:00:01.501Z",
			PreviewLifecycleResultOutcome{
				Kind: PreviewLifecycleSucceeded,
				ResourceState: &PreviewLifecycleResourceState{
					Kind: PreviewLifecycleResourceDestroyed, Disposition: PreviewLifecycleAlreadyAbsent,
				},
			}),
		hostedResultVector("hosted_extend_policy_denied", "extend to current_effective_expires_at plus 8 days, beyond the default 7 day max_extension_ms",
			"deployed:deploy_two_repositories", "denies", vectorLifecycleCommandID("6"), "2099-01-01T00:00:00Z", overlong,
			"2099-01-01T00:00:02Z", "2099-01-01T00:00:02.001Z",
			PreviewLifecycleResultOutcome{Kind: PreviewLifecyclePermanent, Code: PreviewLifecyclePolicyDenied}),
		hostedResultVector("hosted_destroy_controller_timeout", "the operation of hosted_destroy_without_desired_expiry under a new command id; the controller outlives the lease, so observed_at is clamped to lease_expires_at",
			"deployed:deploy_two_repositories", "outlives_lease", vectorLifecycleCommandID("7"), "2099-01-01T11:30:00Z", destroy,
			"2099-01-01T12:00:00Z", "2099-01-01T12:00:00.003Z",
			PreviewLifecycleResultOutcome{Kind: PreviewLifecycleUncertain, Code: PreviewLifecycleControllerTimeout}),
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
	operationDigestStale := deploy
	operationDigestStale.PreviewDeployEffectRevision = 7
	bindingDigestWrong := deploy
	bindingDigestWrong.ResourceBindingDigest = strings.Repeat("e", 64)
	bindingDigestWrong.OperationDigest = PreviewDeployOperationDigest(bindingDigestWrong)
	bindingDigestWrong.CommandDigest = PreviewDeployCommandDigest(bindingDigestWrong)
	commandDigestWrong := deploy
	commandDigestWrong.CommandDigest = strings.Repeat("e", 64)
	commitSetDigestWrong := deploy
	commitSetDigestWrong.CommitSetDigest = strings.Repeat("e", 64)
	commitSetDigestWrong.ResourceBindingDigest = PreviewDeployResourceBindingDigest(commitSetDigestWrong)
	commitSetDigestWrong.OperationDigest = PreviewDeployOperationDigest(commitSetDigestWrong)
	commitSetDigestWrong.CommandDigest = PreviewDeployCommandDigest(commitSetDigestWrong)
	unsorted := mutatedDeploy(func(operation *PreviewDeployOperation) {
		operation.CommitSet[0], operation.CommitSet[1] = operation.CommitSet[1], operation.CommitSet[0]
	})
	oversize := oversizeVectorBlueprint()
	manyRepositories := make([]PreviewObservationCommit, 17)
	for index := range manyRepositories {
		manyRepositories[index] = PreviewObservationCommit{RepositoryID: fmt.Sprintf("repo-%02d", index), CommitSHA: strings.Repeat("1", 40)}
	}
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
		Conventions: map[string]string{
			"envelope":                  "a full runner v1 envelope; compare it semantically, not byte-for-byte, because this file is indented and the wire omits zero milliseconds",
			"byte_limits":               "blueprint.content size is measured on Go encoding/json Marshal output of the decoded payload: compact, object keys sorted, and <, >, & escaped as \\u003c, \\u003e, \\u0026",
			"given":                     "none: no prior state; trusted_commit_set: the vector's own commit_set is recorded as trusted for its execution manifest; trusted_commit_set:<valid name>: that vector's commit_set is trusted; deployed:<valid name>: that deploy vector was accepted and its preview is live; the command message id used for the setup does not affect the outcome",
			"expected_outcome":          "invalid_command and command_conflict are command ack reason codes and publish no result; failed:<phase> means the command is accepted and one preview_deploy_result with outcome failed at that phase is published",
			"expected":                  "digests and event ids computed by the runner; each recomputes from the envelope, so a mismatch localises an encoder bug",
			"hosted_destroyed_event_id": "preview-hosted-destroyed- followed by hex sha256 of preview_id, NUL, the idempotency_key of the deploy named in given, NUL, and the outcome; vectors use outcome destroyed",
			"hosted_lifecycle_results":  "each vector is a hosted preview_lifecycle command_envelope and the preview_lifecycle_result event envelope the runner publishes for it given that state and controller; the result envelope id is result_event_id and its payload carries the command authority unchanged; sent_at and observed_at are runner wall-clock times, so compare them only as observed_at <= lease_expires_at, except that a controller_timeout sets observed_at to lease_expires_at; a real runner sets the result sent_at from its local clock in its local time zone with nanosecond precision, so its offset and sub-second digits can differ from the vector's and consumers must compare sent_at as an instant, never as a string; expected.resource_binding_digest, operation_digest and command_digest recompute from the command; on success the outcome repeats resource_binding_digest and controller_receipt_digest is PreviewCanonicalDigest: the lowercase hex sha256 of the canonical JSON (object keys sorted by UTF-16 code units, no whitespace, ECMAScript string escaping) of an object with operation_digest and resource_binding_digest of the command, resource_kind of resource_state, disposition of resource_state always present and the empty string for an active state, and expires_at only when resource_state has one, encoded by previewLifecycleCanonicalTime as UTC with exactly three fractional digits and a Z suffix (YYYY-MM-DDTHH:MM:SS.sssZ)",
			"controller":                "what the preview host controller does in a hosted_lifecycle_results vector: extends: returns an active resource_state at desired_expires_at without a receipt; not_called: the runner answers without the controller; denies: returns permanent policy_denied, as the runner's controller does for an extension beyond max_extension_ms; outlives_lease: does not answer before lease_expires_at, which the runner can observe only when the lease remaining at receipt is at most lease_max (default 45 minutes), because the runner caps the controller deadline at now plus lease_max and publishes the controller's answer if that cap ends first",
		},
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
			invalidVector("deploy_blueprint_content_over_256_kib", "blueprint.content is at most 262144 bytes of compact JSON; the content is otherwise schema-valid and digest-bound", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.Blueprint.Content = json.RawMessage(oversize)
				operation.Blueprint.Digest = vectorBlueprintDigest(oversize)
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
			invalidVector("deploy_operation_digest_not_resealed", "operation_digest binds preview_deploy_effect_revision; changing it without resealing is refused", vectorDeployEnvelope(vectorDeployCommandID("4"), operationDigestStale)),
			invalidVector("deploy_resource_binding_digest_mismatch", "resource_binding_digest is recomputed from the binding material", vectorDeployEnvelope(vectorDeployCommandID("4"), bindingDigestWrong)),
			invalidVector("deploy_command_digest_mismatch", "command_digest is recomputed from the command material", vectorDeployEnvelope(vectorDeployCommandID("4"), commandDigestWrong)),
			invalidVector("deploy_commit_set_digest_mismatch", "commit_set_digest is recomputed from commit_set", vectorDeployEnvelope(vectorDeployCommandID("4"), commitSetDigestWrong)),
			invalidVector("deploy_commit_set_duplicate_repository", "commit_set repository_id values are unique", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.CommitSet[1].RepositoryID = operation.CommitSet[0].RepositoryID
			}))),
			invalidVector("deploy_commit_set_over_16_repositories", "commit_set has at most 16 entries", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.CommitSet = manyRepositories
			}))),
			invalidVector("deploy_commit_set_empty", "commit_set has at least 1 entry", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.CommitSet = []PreviewObservationCommit{}
			}))),
			invalidVector("deploy_commit_sha_uppercase", "commit_sha is 40 lowercase hex characters", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.CommitSet[0].CommitSHA = strings.Repeat("A", 40)
			}))),
			invalidVector("deploy_blueprint_version_zero", "blueprint.version matches ^[1-9][0-9]{0,15}$", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.Blueprint.Version = "0"
			}))),
			invalidVector("deploy_blueprint_version_17_digits", "blueprint.version matches ^[1-9][0-9]{0,15}$", vectorDeployEnvelope(vectorDeployCommandID("4"), mutatedDeploy(func(operation *PreviewDeployOperation) {
				operation.Blueprint.Version = "10000000000000000"
			}))),
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
		HostedResults: hostedResultVectors(extend, destroy),
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

func TestPreviewDeployV1VectorDigestsArePinned(t *testing.T) {
	file := BuildPreviewDeployVectorFile()
	pins := map[string][2]string{
		"deploy_two_repositories":           {"2b0db84f9636698f552cd3fa32db5164968a57e444e670c6fa63e70307b05287", "052edcff07c6abcb371254c23b656afed14e38623352e8af4c59e02fb8b2a55f"},
		"hosted_extend_with_desired_expiry": {"47cb24167ced4299408856c175b7a1a2b562fa28cc567ab438766cb4328cf1de", ""},
	}
	for _, vector := range file.Valid {
		pin, ok := pins[vector.Name]
		if !ok {
			continue
		}
		if vector.Expected.OperationDigest != pin[0] || (pin[1] != "" && vector.Expected.CommandDigest != pin[1]) {
			t.Fatalf("%s digests moved: operation=%s command=%s", vector.Name, vector.Expected.OperationDigest, vector.Expected.CommandDigest)
		}
		delete(pins, vector.Name)
	}
	if len(pins) != 0 {
		t.Fatalf("pinned vectors missing: %v", pins)
	}
	hostedPins := map[string]PreviewHostedResultWant{
		"hosted_extend_succeeded":       {ControllerReceiptDigest: "c9d91d8440426aa7bf985f9e7141fe337cf27cf462fa064bd400222a77c02979"},
		"hosted_destroy_already_absent": {ControllerReceiptDigest: "9dc059ef45a2f0f2e056e0b6d9205d4c52c5f10f8631fb3eabc6f305f25e0676"},
		"hosted_extend_policy_denied": {
			OperationDigest: "84d62af5188e9f07c9455fce489e8715b01d08d2ea060da473000fdf3aee17a5",
			CommandDigest:   "9c0014f805764012a2da67290e5b29fde1eaea9bf52028ccfe34c2ad7880823c",
		},
	}
	for _, vector := range file.HostedResults {
		pin, ok := hostedPins[vector.Name]
		if !ok {
			continue
		}
		if (pin.ControllerReceiptDigest != "" && vector.Expected.ControllerReceiptDigest != pin.ControllerReceiptDigest) ||
			(pin.OperationDigest != "" && vector.Expected.OperationDigest != pin.OperationDigest) ||
			(pin.CommandDigest != "" && vector.Expected.CommandDigest != pin.CommandDigest) {
			t.Fatalf("%s digests moved: operation=%s command=%s receipt=%s", vector.Name, vector.Expected.OperationDigest, vector.Expected.CommandDigest, vector.Expected.ControllerReceiptDigest)
		}
		delete(hostedPins, vector.Name)
	}
	if len(hostedPins) != 0 {
		t.Fatalf("pinned hosted vectors missing: %v", hostedPins)
	}
}

func vectorGivenDeployKey(t *testing.T, file PreviewDeployVectorFile, given string) string {
	t.Helper()
	name, ok := strings.CutPrefix(given, "deployed:")
	if !ok {
		t.Fatalf("hosted destroy vector given=%q names no deploy", given)
	}
	for _, vector := range file.Valid {
		if vector.Name != name {
			continue
		}
		var envelope struct {
			Payload CommandPayload `json:"payload"`
		}
		if err := json.Unmarshal(vector.Envelope, &envelope); err != nil || envelope.Payload.PreviewDeploy == nil {
			t.Fatalf("given deploy %s: %v", name, err)
		}
		return envelope.Payload.PreviewDeploy.IdempotencyKey
	}
	t.Fatalf("given deploy %s is not a valid vector", name)
	return ""
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
	if len(file.Valid) != 5 || len(file.Invalid) != 27 || len(file.Stateful) != 2 {
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
					got.HostedDestroyedEventID = PreviewHostedDestroyedEventID(operation.PreviewID,
						vectorGivenDeployKey(t, file, vector.Given), PreviewHostedDestroyOutcomeDestroyed)
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

func TestPreviewHostedLifecycleResultVectorsRecomputeFromTheWire(t *testing.T) {
	var file PreviewDeployVectorFile
	raw, err := os.ReadFile(previewDeployVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.HostedResults) != 4 {
		t.Fatalf("hosted lifecycle result vectors = %d", len(file.HostedResults))
	}
	for _, vector := range file.HostedResults {
		t.Run(vector.Name, func(t *testing.T) {
			var command struct {
				ID      string         `json:"id"`
				Payload CommandPayload `json:"payload"`
			}
			if err := json.Unmarshal(vector.CommandEnvelope, &command); err != nil || command.Payload.PreviewLifecycle == nil {
				t.Fatalf("command envelope: %v", err)
			}
			var result struct {
				Version  int                         `json:"version"`
				Type     MessageType                 `json:"type"`
				ID       string                      `json:"id"`
				RunnerID string                      `json:"runner_id"`
				Payload  PreviewLifecycleResultEvent `json:"payload"`
			}
			if err := json.Unmarshal(vector.ResultEnvelope, &result); err != nil {
				t.Fatal(err)
			}
			operation := *command.Payload.PreviewLifecycle
			event := result.Payload
			if result.Version != ProtocolVersion || result.Type != TypeEvent || result.RunnerID == "" ||
				result.ID != vectorLifecycleResultEventID(command.ID) || event.Kind != EventPreviewLifecycleResult ||
				event.CommandMessageID != command.ID || event.ObservedAt.After(event.LeaseExpiresAt) {
				t.Fatalf("result envelope %+v does not answer command %s", result, command.ID)
			}
			gotAuthority, _ := json.Marshal(event.PreviewLifecycleResultAuthority)
			wantAuthority, _ := json.Marshal(PreviewLifecycleResultAuthorityFromOperation(operation))
			if !bytes.Equal(gotAuthority, wantAuthority) {
				t.Fatalf("result authority %s, want %s", gotAuthority, wantAuthority)
			}
			got := PreviewHostedResultWant{
				ResultEventID: vectorLifecycleResultEventID(command.ID),
				OutcomeKind:   event.Outcome.Kind, OutcomeCode: event.Outcome.Code,
				ResourceBindingDigest: PreviewHostedLifecycleResourceBindingDigest(operation),
				OperationDigest:       PreviewHostedLifecycleOperationDigest(operation),
				CommandDigest:         PreviewLifecycleCommandDigest(operation),
			}
			if event.Outcome.Kind == PreviewLifecycleSucceeded {
				if event.Outcome.ResourceState == nil || event.Outcome.ResourceBindingDigest != got.ResourceBindingDigest {
					t.Fatalf("success outcome %+v does not bind %s", event.Outcome, got.ResourceBindingDigest)
				}
				got.ControllerReceiptDigest = PreviewHostedLifecycleReceiptDigest(operation, *event.Outcome.ResourceState)
				if event.Outcome.ControllerReceiptDigest != got.ControllerReceiptDigest {
					t.Fatalf("controller_receipt_digest=%s, recomputed %s", event.Outcome.ControllerReceiptDigest, got.ControllerReceiptDigest)
				}
			}
			if got != vector.Expected {
				t.Fatalf("recomputed %+v, want %+v", got, vector.Expected)
			}
		})
	}
}
