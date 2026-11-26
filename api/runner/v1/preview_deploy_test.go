package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

const goldenPreviewBlueprintContent = `{"schema_version":1,"services":[{"name":"db","role":"database","image":"docker.io/library/postgres@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","env":{"POSTGRES_PASSWORD":{"kind":"generated","generator":"random_secret"}}}],"app":{"image":"ghcr.io/acme/node@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","working_directory":".","install":["pnpm install --frozen-lockfile"],"build":["pnpm build"],"migrate":["pnpm db:migrate:deploy"],"seed":["pnpm db:seed"],"start":"pnpm start","port":3000,"health_checks":[{"id":"home","path":"/","expected_status":200}],"env":{"DATABASE_URL":{"kind":"generated","generator":"postgres_url"},"STRIPE_SECRET_KEY":{"kind":"secret","note":"Test mode key"},"FEATURE_FLAG":{"kind":"fake","value":"enabled"},"SENTRY_DSN":{"kind":"disabled"}}},"limits":{"memory_mb":1024,"cpus":1,"max_lifetime_minutes":1440},"egress":{"mode":"none"}}`

func goldenPreviewDeployOperation() PreviewDeployOperation {
	return PreviewDeployOperation{
		PreviewDeployOperationAuthority: PreviewDeployOperationAuthority{
			SchemaVersion:               PreviewDeploySchemaVersion,
			PreviewDeployEffectID:       "preview-deploy-effect-1",
			PreviewDeployEffectRevision: 3,
			PreviewDeployOperationID:    "preview-deploy-operation-" + strings.Repeat("a", 64),
			PreviewID:                   "preview-1",
			OrganizationID:              "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1",
			ExecutionManifestID: "manifest-1", ExecutionManifestDigest: strings.Repeat("c", 64),
			IdempotencyKey: "preview-deploy-" + strings.Repeat("d", 64),
			LeaseID:        "lease-1", FencingToken: 4,
			LeaseExpiresAt: time.Date(2030, 8, 14, 12, 45, 0, 0, time.UTC),
		},
		Blueprint: PreviewDeployBlueprint{
			BlueprintID: "node-web", Version: "1",
			Digest:  "83ded9d685a2c763c73dd2ea0f56a054fe1e851b8b44c8307211ea518fa60563",
			Content: json.RawMessage(goldenPreviewBlueprintContent),
		},
		CommitSet: []PreviewObservationCommit{
			{RepositoryID: "api", CommitSHA: strings.Repeat("1", 40)},
			{RepositoryID: "web", CommitSHA: strings.Repeat("2", 40)},
		},
		PreviewKey: "abcdefghijklmnopqrstuvwxyz",
		ExpiresAt:  time.Date(2030, 8, 14, 18, 0, 0, 0, time.UTC),
	}
}

func TestPreviewDeployDigestsMatchPinnedCanonicalVectors(t *testing.T) {
	operation := goldenPreviewDeployOperation()
	operation.CommitSetDigest = PreviewLifecycleCommitSetDigest(operation.CommitSet)
	if operation.CommitSetDigest != "e830a1a3a3babcad9157ee58ea049d39d4c05e506af12d577e9230f200e03415" {
		t.Fatalf("commit_set_digest=%s", operation.CommitSetDigest)
	}
	operation.ResourceBindingDigest = PreviewDeployResourceBindingDigest(operation)
	if operation.ResourceBindingDigest != "542e6cc4a0f0a85a6cf8f994091420348a33d487c9a86346bcabc408290613a4" {
		t.Fatalf("resource_binding_digest=%s", operation.ResourceBindingDigest)
	}
	operation.OperationDigest = PreviewDeployOperationDigest(operation)
	if operation.OperationDigest != "fb2ca63656af8d85ee327bc3f6bda062e89efeba0656deb68210d064f0db390b" {
		t.Fatalf("operation_digest=%s", operation.OperationDigest)
	}
	operation.CommandDigest = PreviewDeployCommandDigest(operation)
	if operation.CommandDigest != "5d75cd44e9187528a0b0f2bcc2eae5871659997e414af130895ace0199416ccf" {
		t.Fatalf("command_digest=%s", operation.CommandDigest)
	}
}

func TestPreviewDeployOperationDigestBindsIntentButNotContentBytesOrDelivery(t *testing.T) {
	base := goldenPreviewDeployOperation()
	base.CommitSetDigest = PreviewLifecycleCommitSetDigest(base.CommitSet)
	base.ResourceBindingDigest = PreviewDeployResourceBindingDigest(base)
	digest := PreviewDeployOperationDigest(base)

	reformatted := base
	reformatted.Blueprint.Content = json.RawMessage(" " + goldenPreviewBlueprintContent + "\n")
	redelivered := base
	redelivered.LeaseID, redelivered.FencingToken = "lease-2", 9
	redelivered.LeaseExpiresAt = base.LeaseExpiresAt.Add(time.Minute)
	for name, candidate := range map[string]PreviewDeployOperation{"content bytes": reformatted, "delivery": redelivered} {
		if PreviewDeployOperationDigest(candidate) != digest {
			t.Fatalf("%s changed operation digest", name)
		}
	}
	if PreviewDeployCommandDigest(redelivered) == PreviewDeployCommandDigest(base) {
		t.Fatal("command digest ignored delivery fence")
	}
	changed := base
	changed.EdgeHost = changed.PreviewKey + ".preview.example.test"
	if PreviewDeployResourceBindingDigest(changed) == base.ResourceBindingDigest {
		t.Fatal("resource binding ignored edge_host")
	}
}

func TestPreviewDeployResultAuthorityEchoesIdentityWithoutContent(t *testing.T) {
	operation := goldenPreviewDeployOperation()
	event := PreviewDeployResultEvent{
		Kind:                         EventPreviewDeployResult,
		PreviewDeployResultAuthority: PreviewDeployResultAuthorityFromOperation(operation),
		CommandMessageID:             "preview-deploy-command-" + strings.Repeat("e", 64),
		ObservedAt:                   time.Date(2030, 8, 14, 12, 30, 0, 0, time.UTC),
		Outcome:                      PreviewDeployOutcome{Kind: PreviewDeployFailed, Phase: PreviewDeployPhaseCommitSet},
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"kind":"preview_deploy_result"`, `"preview_id":"preview-1"`, `"edge_host":""`,
		`"blueprint":{"blueprint_id":"node-web","version":"1","digest":"83ded9d685a2c763c73dd2ea0f56a054fe1e851b8b44c8307211ea518fa60563"}`,
		`"outcome":{"kind":"failed","phase":"commit_set"}`,
	} {
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("result missing %s: %s", expected, raw)
		}
	}
	if strings.Contains(string(raw), "content") || strings.Contains(string(raw), "services") {
		t.Fatalf("result echoed blueprint content: %s", raw)
	}
}

func TestPreviewDeployResultEchoesCommandTimesWithFixedMilliseconds(t *testing.T) {
	var operation PreviewDeployOperation
	if err := json.Unmarshal([]byte(`{"lease_expires_at":"2026-11-26T10:08:00.440Z","expires_at":"2026-11-26T10:22:40.000Z"}`), &operation); err != nil {
		t.Fatal(err)
	}
	event := PreviewDeployResultEvent{
		Kind:                         EventPreviewDeployResult,
		PreviewDeployResultAuthority: PreviewDeployResultAuthorityFromOperation(operation),
		ObservedAt:                   time.Date(2026, 11, 26, 9, 23, 3, 10_000_000, time.UTC),
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"lease_expires_at":"2026-11-26T10:08:00.440Z"`,
		`"expires_at":"2026-11-26T10:22:40.000Z"`,
		`"observed_at":"2026-11-26T09:23:03.010Z"`,
	} {
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("result missing %s: %s", expected, raw)
		}
	}
	var decoded PreviewDeployResultEvent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.LeaseExpiresAt.Equal(operation.LeaseExpiresAt) || !decoded.ExpiresAt.Equal(operation.ExpiresAt) || !decoded.ObservedAt.Equal(event.ObservedAt) {
		t.Fatalf("decoded times changed: %+v", decoded)
	}
}

func TestPreviewDeployCommandUsesDedicatedPayload(t *testing.T) {
	operation := goldenPreviewDeployOperation()
	raw, err := json.Marshal(CommandPayload{Command: CmdPreviewDeploy, PreviewDeploy: &operation})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"command":"preview_deploy"`) || !strings.Contains(string(raw), `"preview_deploy":{`) {
		t.Fatalf("wire=%s", raw)
	}
	var decoded CommandPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.PreviewDeploy == nil || decoded.PreviewDeploy.PreviewKey != operation.PreviewKey {
		t.Fatalf("decoded=%+v", decoded.PreviewDeploy)
	}
}

func TestPreviewSecretsNeverFormatTheirValues(t *testing.T) {
	secrets := PreviewSecrets{"STRIPE_SECRET_KEY": "sk_test_value_that_must_not_leak"}
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("secrets", "value", secrets)
	encoded, err := json.Marshal(secrets)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(Envelope{Version: 1, Type: TypeCommand, ID: "c", PreviewSecrets: secrets})
	if err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{
		fmt.Sprint(secrets), fmt.Sprintf("%v", secrets), fmt.Sprintf("%+v", secrets), fmt.Sprintf("%#v", secrets),
		fmt.Sprintf("%s", secrets), fmt.Sprintf("%q", secrets), secrets.String(), secrets.GoString(),
		string(encoded), string(envelope), logged.String(),
	} {
		if strings.Contains(rendered, "sk_test_value") || strings.Contains(rendered, "STRIPE") {
			t.Fatalf("secret leaked: %s", rendered)
		}
	}
	if !strings.Contains(string(envelope), `"preview_secrets":"[redacted]"`) {
		t.Fatalf("envelope=%s", envelope)
	}
}

func TestEnvelopeDecodesPreviewSecretsSibling(t *testing.T) {
	var envelope Envelope
	if err := json.Unmarshal([]byte(`{"version":1,"type":"command","id":"c","sent_at":"2030-01-01T00:00:00Z","preview_secrets":{"A":"b"}}`), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.PreviewSecrets) != 1 || envelope.PreviewSecrets["A"] != "b" {
		t.Fatalf("secrets=%d", len(envelope.PreviewSecrets))
	}
	var absent Envelope
	if err := json.Unmarshal([]byte(`{"version":1,"type":"command","id":"c","sent_at":"2030-01-01T00:00:00Z"}`), &absent); err != nil {
		t.Fatal(err)
	}
	if absent.PreviewSecrets != nil {
		t.Fatal("absent sibling decoded as present")
	}
}

func TestPreviewHostedDestroyedEventBuilder(t *testing.T) {
	destroyedAt := time.Date(2030, 8, 14, 18, 0, 0, 123456789, time.FixedZone("x", 3600))
	event := NewPreviewHostedDestroyedEvent(PreviewHostedDestroyedSubject{
		PreviewID: "preview-1", OrganizationID: "organization-1", ProjectID: "project-1",
		PreviewKey: "abcdefghijklmnopqrstuvwxyz", IdempotencyKey: "preview-deploy-" + strings.Repeat("d", 64),
	}, destroyedAt, PreviewHostedDestroyExpired, PreviewHostedDestroyOutcomeDestroyed)
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"kind":"preview_hosted_destroyed","schema_version":1,"preview_id":"preview-1","organization_id":"organization-1","project_id":"project-1","preview_key":"abcdefghijklmnopqrstuvwxyz","idempotency_key":"preview-deploy-dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","destroyed_at":"2030-08-14T17:00:00.123Z","reason":"expired","outcome":"destroyed"}`
	if string(raw) != expected {
		t.Fatalf("event=%s", raw)
	}
}

func TestPreviewHostedDestroyedEventIDCoversPreviewKeyAndOutcome(t *testing.T) {
	key := "preview-deploy-" + strings.Repeat("d", 64)
	vectors := map[PreviewHostedDestroyOutcome]string{
		PreviewHostedDestroyOutcomeDestroyed: "preview-hosted-destroyed-9ce9e90218c07f0f7938bc08dd1cd6eb6b94e2ae0d558a973eb7e9dbf30b0be6",
		PreviewHostedDestroyOutcomeUncertain: "preview-hosted-destroyed-269324349cd5e444f686744a4e0dcf371345dba92dba0043a887116f88a2766d",
	}
	for outcome, want := range vectors {
		if got := PreviewHostedDestroyedEventID("preview-1", key, outcome); got != want {
			t.Fatalf("%s id=%s want %s", outcome, got, want)
		}
	}
	base := PreviewHostedDestroyedEventID("preview-1", key, PreviewHostedDestroyOutcomeDestroyed)
	if base == PreviewHostedDestroyedEventID("preview-2", key, PreviewHostedDestroyOutcomeDestroyed) ||
		base == PreviewHostedDestroyedEventID("preview-1", "preview-deploy-"+strings.Repeat("e", 64), PreviewHostedDestroyOutcomeDestroyed) {
		t.Fatal("destroyed event ID does not separate previews and deploy keys")
	}
}

func goldenHostedLifecycleOperation() PreviewLifecycleOperation {
	desired := time.Date(2030, 8, 15, 18, 0, 0, 0, time.UTC)
	return PreviewLifecycleOperation{
		PreviewLifecycleOperationAuthority: PreviewLifecycleOperationAuthority{
			SchemaVersion:                  PreviewLifecycleSchemaVersion,
			PreviewLifecycleEffectID:       "preview-lifecycle-effect-1",
			PreviewLifecycleEffectRevision: 2,
			PreviewLifecycleOperationID:    "preview-lifecycle-operation-" + strings.Repeat("a", 64),
			OrganizationID:                 "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1",
			ExecutionManifestID: "manifest-1", ExecutionManifestDigest: strings.Repeat("c", 64),
			Action: PreviewLifecycleExtend, IdempotencyKey: "preview-lifecycle-" + strings.Repeat("d", 64),
			LeaseID: "lease-1", FencingToken: 4, LeaseExpiresAt: time.Date(2030, 8, 14, 12, 45, 0, 0, time.UTC),
		},
		PreviewID:  "preview-1",
		PreviewKey: "abcdefghijklmnopqrstuvwxyz",
		Blueprint: PreviewObservationBlueprint{
			BlueprintID: "node-web", Version: "1",
			Digest: "83ded9d685a2c763c73dd2ea0f56a054fe1e851b8b44c8307211ea518fa60563",
		},
		CommitSet: []PreviewObservationCommit{
			{RepositoryID: "api", CommitSHA: strings.Repeat("1", 40)},
			{RepositoryID: "web", CommitSHA: strings.Repeat("2", 40)},
		},
		OriginalExpiresAt:         time.Date(2030, 8, 14, 18, 0, 0, 0, time.UTC),
		CurrentEffectiveExpiresAt: time.Date(2030, 8, 14, 18, 0, 0, 0, time.UTC),
		DesiredExpiresAt:          &desired,
	}
}

func TestPreviewHostedLifecycleDigestsMatchPinnedCanonicalVectors(t *testing.T) {
	operation := goldenHostedLifecycleOperation()
	if !operation.Hosted() {
		t.Fatal("hosted variant not recognized")
	}
	operation.CommitSetDigest = PreviewLifecycleCommitSetDigest(operation.CommitSet)
	operation.ResourceBindingDigest = PreviewHostedLifecycleResourceBindingDigest(operation)
	if operation.ResourceBindingDigest != "fa6022784ad8b18c621db969095219151fb30a5d2d9f49d145df38a1f7c440d5" {
		t.Fatalf("resource_binding_digest=%s", operation.ResourceBindingDigest)
	}
	operation.OperationDigest = PreviewHostedLifecycleOperationDigest(operation)
	if operation.OperationDigest != "e635849caf055bc2315faf38c28268a55af81d7dbccb0c3431399e6defb90657" {
		t.Fatalf("operation_digest=%s", operation.OperationDigest)
	}
	operation.CommandDigest = PreviewLifecycleCommandDigest(operation)
	if operation.CommandDigest != "dea703521c6d7c5dc7f209b1fa3715e91e922ee4db14bbb58152207e4b5d3136" {
		t.Fatalf("command_digest=%s", operation.CommandDigest)
	}
}

func TestPreviewHostedLifecycleWireOmitsExternalTargetFields(t *testing.T) {
	operation := goldenHostedLifecycleOperation()
	raw, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"preview_evidence_id", "preview_observation_id", "preview_observation_digest", "preview_url"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("hosted operation carried %s: %s", forbidden, raw)
		}
	}
	if !strings.Contains(string(raw), `"preview_id":"preview-1"`) || !strings.Contains(string(raw), `"preview_key":"abcdefghijklmnopqrstuvwxyz"`) {
		t.Fatalf("hosted operation=%s", raw)
	}
	var decoded PreviewLifecycleOperation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil || decoded.PreviewID != "preview-1" || decoded.DesiredExpiresAt == nil {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	event, err := json.Marshal(PreviewLifecycleResultEvent{
		Kind:                            EventPreviewLifecycleResult,
		PreviewLifecycleResultAuthority: PreviewLifecycleResultAuthorityFromOperation(operation),
		Outcome:                         PreviewLifecycleResultOutcome{Kind: PreviewLifecycleUncertain, Code: PreviewLifecycleControllerTimeout},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"preview_evidence_id", "preview_observation_id", "preview_observation_digest", "preview_url"} {
		if strings.Contains(string(event), forbidden) {
			t.Fatalf("hosted result carried %s: %s", forbidden, event)
		}
	}
	if !strings.Contains(string(event), `"kind":"preview_lifecycle_result"`) || !strings.Contains(string(event), `"preview_id":"preview-1"`) ||
		!strings.Contains(string(event), `"outcome":{"kind":"uncertain","code":"controller_timeout"}`) {
		t.Fatalf("hosted result=%s", event)
	}
}

func TestPreviewExternalLifecycleWireIsUnchanged(t *testing.T) {
	operation := goldenHostedLifecycleOperation()
	operation.PreviewID, operation.PreviewKey = "", ""
	operation.PreviewEvidenceID, operation.PreviewObservationID = "preview-evidence-1", "preview-observation-1"
	operation.PreviewObservationDigest = strings.Repeat("b", 64)
	operation.PreviewURL = "https://branch.preview.example.test/"
	operation.DesiredExpiresAt = nil
	operation.Action = PreviewLifecycleDestroy
	raw, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"schema_version":1,"preview_lifecycle_effect_id":"preview-lifecycle-effect-1","preview_lifecycle_effect_revision":2,"preview_lifecycle_operation_id":"preview-lifecycle-operation-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","organization_id":"organization-1","project_id":"project-1","workflow_run_id":"workflow-1","preview_evidence_id":"preview-evidence-1","preview_observation_id":"preview-observation-1","preview_observation_digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","execution_manifest_id":"manifest-1","execution_manifest_digest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","action":"destroy","idempotency_key":"preview-lifecycle-dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","resource_binding_digest":"","operation_digest":"","command_digest":"","lease_id":"lease-1","fencing_token":4,"lease_expires_at":"2030-08-14T12:45:00Z","blueprint":{"blueprint_id":"node-web","version":"1","digest":"83ded9d685a2c763c73dd2ea0f56a054fe1e851b8b44c8307211ea518fa60563"},"preview_url":"https://branch.preview.example.test/","commit_set":[{"repository_id":"api","commit_sha":"1111111111111111111111111111111111111111"},{"repository_id":"web","commit_sha":"2222222222222222222222222222222222222222"}],"commit_set_digest":"","original_expires_at":"2030-08-14T18:00:00Z","current_effective_expires_at":"2030-08-14T18:00:00Z"}`
	if string(raw) != expected {
		t.Fatalf("external wire changed:\n%s", raw)
	}
	if operation.Hosted() {
		t.Fatal("external variant recognized as hosted")
	}
	event, err := json.Marshal(PreviewLifecycleResultEvent{
		Kind:                            EventPreviewLifecycleResult,
		PreviewLifecycleResultAuthority: PreviewLifecycleResultAuthorityFromOperation(operation),
		Outcome:                         PreviewLifecycleResultOutcome{Kind: PreviewLifecycleUncertain, Code: PreviewLifecycleControllerTimeout},
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedEvent := `{"kind":"preview_lifecycle_result",` + strings.TrimPrefix(expected, "{")
	expectedEvent = strings.TrimSuffix(expectedEvent, "}") + `,"command_message_id":"","observed_at":"0001-01-01T00:00:00Z","outcome":{"kind":"uncertain","code":"controller_timeout"}}`
	if string(event) != expectedEvent {
		t.Fatalf("external result wire changed:\n%s", event)
	}
}
