package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/previewhost"
	"github.com/example/app-runner/internal/redact"
)

const testPreviewDeployBlueprintContent = `{"schema_version":1,"services":[{"name":"db","role":"database","image":"docker.io/library/postgres@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","env":{"POSTGRES_PASSWORD":{"kind":"generated","generator":"random_secret"}}}],"app":{"image":"ghcr.io/acme/node@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","working_directory":".","install":["pnpm install --frozen-lockfile"],"build":["pnpm build"],"migrate":["pnpm db:migrate:deploy"],"seed":["pnpm db:seed"],"start":"pnpm start","port":3000,"health_checks":[{"id":"home","path":"/","expected_status":200}],"env":{"DATABASE_URL":{"kind":"generated","generator":"postgres_url"},"STRIPE_SECRET_KEY":{"kind":"secret","note":"Test mode key"},"FEATURE_FLAG":{"kind":"fake","value":"enabled"},"SENTRY_DSN":{"kind":"disabled"}}},"limits":{"memory_mb":1024,"cpus":1,"max_lifetime_minutes":1440},"egress":{"mode":"none"}}`

const testPreviewDeployBlueprintDigest = "83ded9d685a2c763c73dd2ea0f56a054fe1e851b8b44c8307211ea518fa60563"

type fakePreviewHostController struct {
	mu                sync.Mutex
	deployOutcome     v1.PreviewDeployOutcome
	lifecycleOutcome  v1.PreviewLifecycleResultOutcome
	recoverErr        error
	deploys           int
	lifecycles        int
	recovered         []journal.PreviewHost
	lastDeploy        previewhost.DeployRequest
	deadline          time.Time
	lifecycleDeadline time.Time
	started           chan struct{}
	release           chan struct{}
	capability        *v1.PreviewHostingCapability
}

func (controller *fakePreviewHostController) Capability() v1.PreviewHostingCapability {
	if controller.capability != nil {
		return *controller.capability
	}
	maxExtension := uint64(7 * 24 * 60 * 60 * 1000)
	return v1.PreviewHostingCapability{
		ProtocolVersion: v1.PreviewHostingProtocolVersion,
		MaxConcurrent:   3,
		Actions:         []v1.PreviewLifecycleAction{"deploy", v1.PreviewLifecycleExtend, v1.PreviewLifecycleDestroy, "observe"},
		MaxExtensionMS:  &maxExtension,
	}
}

func (controller *fakePreviewHostController) Deploy(ctx context.Context, request previewhost.DeployRequest) previewhost.DeployResult {
	controller.mu.Lock()
	controller.deploys++
	controller.lastDeploy = request
	controller.deadline, _ = ctx.Deadline()
	started, release := controller.started, controller.release
	outcome := controller.deployOutcome
	controller.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	return previewhost.DeployResult{Outcome: outcome}
}

func (controller *fakePreviewHostController) ApplyLifecycle(ctx context.Context, _ previewhost.LifecycleRequest) previewhost.LifecycleResult {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.lifecycles++
	controller.lifecycleDeadline, _ = ctx.Deadline()
	return previewhost.LifecycleResult{Outcome: controller.lifecycleOutcome}
}

func (controller *fakePreviewHostController) Recover(_ context.Context, host journal.PreviewHost) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.recovered = append(controller.recovered, host)
	return controller.recoverErr
}

func (controller *fakePreviewHostController) counts() (int, int, int) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.deploys, controller.lifecycles, len(controller.recovered)
}

func deployedPreviewOutcome() v1.PreviewDeployOutcome {
	return v1.PreviewDeployOutcome{
		Kind:   v1.PreviewDeployDeployed,
		Checks: []v1.PreviewObservationCheck{{CheckID: "home", Status: v1.PreviewObservationCheckPassed, HTTPStatus: 200, DurationMS: 5}},
	}
}

func sealTestPreviewDeploy(operation *v1.PreviewDeployOperation) {
	operation.CommitSetDigest = v1.PreviewLifecycleCommitSetDigest(operation.CommitSet)
	operation.ResourceBindingDigest = v1.PreviewDeployResourceBindingDigest(*operation)
	operation.OperationDigest = v1.PreviewDeployOperationDigest(*operation)
	operation.CommandDigest = v1.PreviewDeployCommandDigest(*operation)
}

func validPreviewDeployCommand() v1.CommandPayload {
	now := time.Now().UTC().Truncate(time.Millisecond)
	operation := v1.PreviewDeployOperation{
		PreviewDeployOperationAuthority: v1.PreviewDeployOperationAuthority{
			SchemaVersion:         v1.PreviewDeploySchemaVersion,
			PreviewDeployEffectID: "preview-deploy-effect-1", PreviewDeployEffectRevision: 1,
			PreviewDeployOperationID: "preview-deploy-operation-" + strings.Repeat("a", 64),
			PreviewID:                "preview-1", OrganizationID: "organization-1", ProjectID: "project-1",
			WorkflowRunID: "workflow-1", ExecutionManifestID: "manifest-1", ExecutionManifestDigest: strings.Repeat("c", 64),
			IdempotencyKey: "preview-deploy-" + strings.Repeat("d", 64), LeaseID: "lease-1", FencingToken: 4,
			LeaseExpiresAt: now.Add(time.Minute),
		},
		Blueprint: v1.PreviewDeployBlueprint{
			BlueprintID: "node-web", Version: "1", Digest: testPreviewDeployBlueprintDigest,
			Content: json.RawMessage(testPreviewDeployBlueprintContent),
		},
		CommitSet: []v1.PreviewObservationCommit{
			{RepositoryID: "api", CommitSHA: strings.Repeat("1", 40)},
			{RepositoryID: "web", CommitSHA: strings.Repeat("2", 40)},
		},
		PreviewKey: "abcdefghijklmnopqrstuvwxyz",
		ExpiresAt:  now.Add(2 * time.Hour),
	}
	sealTestPreviewDeploy(&operation)
	return v1.CommandPayload{Command: v1.CmdPreviewDeploy, PreviewDeploy: &operation}
}

func previewDeployCommandID(suffix string) string {
	return "preview-deploy-command-" + strings.Repeat(suffix, 64)
}

func trustPreviewDeployCommits(t *testing.T, d *Daemon, operation v1.PreviewDeployOperation) {
	t.Helper()
	if err := d.journal.RecordTrustedCommitSet(context.Background(), operation.ExecutionManifestID,
		operation.ExecutionManifestDigest, operation.CommitSet, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func capturePreviewDeployEvents(t *testing.T, d *Daemon) *[]v1.PreviewDeployResultEvent {
	t.Helper()
	var mu sync.Mutex
	events := &[]v1.PreviewDeployResultEvent{}
	d.eventSink = func(_ context.Context, payload any) error {
		mu.Lock()
		defer mu.Unlock()
		switch event := payload.(type) {
		case v1.PreviewDeployResultEvent:
			*events = append(*events, event)
		case json.RawMessage:
			var decoded v1.PreviewDeployResultEvent
			if err := json.Unmarshal(event, &decoded); err != nil {
				t.Fatalf("decode replayed result: %v", err)
			}
			*events = append(*events, decoded)
		default:
			t.Fatalf("event type=%T", payload)
		}
		return nil
	}
	return events
}

func TestPreviewDeployWithoutHostControllerIsUnsupported(t *testing.T) {
	jr, _ := testJobRunner(t)
	err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, validPreviewDeployCommand())
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonUnsupportedCommand {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
}

func TestPreviewDeployRejectsMalformedAndSmuggledPayload(t *testing.T) {
	tests := map[string]func(v1.CommandPayload) v1.CommandPayload{
		"missing": func(command v1.CommandPayload) v1.CommandPayload { command.PreviewDeploy = nil; return command },
		"digest": func(command v1.CommandPayload) v1.CommandPayload {
			command.PreviewDeploy.CommandDigest = strings.Repeat("9", 64)
			return command
		},
		"job smuggling": func(command v1.CommandPayload) v1.CommandPayload { command.JobID = "job-1"; return command },
		"lease smuggling": func(command v1.CommandPayload) v1.CommandPayload {
			command.LeaseExpiresAt = time.Now()
			return command
		},
		"control smuggling": func(command v1.CommandPayload) v1.CommandPayload { command.ControlID = "control-1"; return command },
		"lifecycle beside deploy": func(command v1.CommandPayload) v1.CommandPayload {
			command.PreviewLifecycle = validPreviewLifecycleCommand(v1.PreviewLifecycleDestroy).PreviewLifecycle
			return command
		},
		"observation beside deploy": func(command v1.CommandPayload) v1.CommandPayload {
			command.PreviewObservation = &v1.PreviewObservationOperation{}
			return command
		},
		"deploy under resume": func(command v1.CommandPayload) v1.CommandPayload {
			command.Command = v1.CmdResumeRunner
			return command
		},
		"deploy under lifecycle": func(command v1.CommandPayload) v1.CommandPayload {
			command.Command = v1.CmdPreviewLifecycle
			command.PreviewLifecycle = validPreviewLifecycleCommand(v1.PreviewLifecycleDestroy).PreviewLifecycle
			return command
		},
		"deploy under job control": func(command v1.CommandPayload) v1.CommandPayload {
			command.Command = v1.CmdControlJob
			return command
		},
		"invalid blueprint": func(command v1.CommandPayload) v1.CommandPayload {
			command.PreviewDeploy.Blueprint.Content = json.RawMessage(`{"schema_version":1}`)
			return command
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
			jr.d.previewHostController = controller
			jr.d.previewLifecycleController = &fakePreviewLifecycleController{}
			err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, mutate(validPreviewDeployCommand()))
			if reason := commandRejectionReason(err); reason != v1.CommandAckReasonInvalidCommand {
				t.Fatalf("reason=%q err=%v", reason, err)
			}
			if deploys, _, _ := controller.counts(); deploys != 0 {
				t.Fatal("invalid command reached controller")
			}
		})
	}
}

func TestPreviewDeployRefusesPreviewSecretsBeforeJournalling(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.d.previewHostController = &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
	envelope := commandEnvelope(previewDeployCommandID("1"), validPreviewDeployCommand())
	envelope.PreviewSecrets = v1.PreviewSecrets{"STRIPE_SECRET_KEY": "sk_test_preview_secret_value"}
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if exists, err := jr.d.journal.HasRecord(context.Background(), envelope.ID); err != nil || exists {
		t.Fatalf("command carrying preview secrets journalled exists=%v err=%v", exists, err)
	}
}

func TestDurableCommandRecordStripsPreviewSecrets(t *testing.T) {
	envelope := commandEnvelope(previewDeployCommandID("1"), validPreviewDeployCommand())
	envelope.PreviewSecrets = v1.PreviewSecrets{"STRIPE_SECRET_KEY": "sk_test_preview_secret_value"}
	digest, err := commandContentDigest(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := durableCommandRecordPayload(envelope, digest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "preview_secrets") || strings.Contains(string(raw), "sk_test_preview_secret_value") {
		t.Fatalf("durable record carries preview secrets: %s", raw)
	}
}

func TestPreviewDeployPublishesOneResultAndReplaysByIdempotencyKey(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewDeployCommand()
	trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
	controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
	jr.d.previewHostController = controller
	events := capturePreviewDeployEvents(t, jr.d)
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command); err != nil {
		t.Fatal(err)
	}
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("2")}, command); err != nil {
		t.Fatal(err)
	}
	deploys, _, _ := controller.counts()
	if deploys != 1 || len(*events) != 1 || (*events)[0].Outcome.Kind != v1.PreviewDeployDeployed ||
		(*events)[0].CommandMessageID != previewDeployCommandID("1") {
		t.Fatalf("deploys=%d events=%+v", deploys, *events)
	}
	if controller.lastDeploy.Content.Limits.MemoryMB != 1024 || !controller.deadline.Equal(command.PreviewDeploy.LeaseExpiresAt) {
		t.Fatalf("deploy request=%+v deadline=%s", controller.lastDeploy.Content.Limits, controller.deadline)
	}
	host, found, err := jr.d.journal.PreviewHostByIdempotencyKey(context.Background(), command.PreviewDeploy.IdempotencyKey)
	if err != nil || !found || host.State != journal.PreviewHostDeployed || host.MemoryMB != 1024 || len(host.Result) == 0 {
		t.Fatalf("host=%+v found=%v err=%v", host, found, err)
	}
	if exists, _ := jr.d.journal.HasRecord(context.Background(), v1.PreviewDeployResultEventID(command.PreviewDeploy.IdempotencyKey)); !exists {
		t.Fatal("result event is not journalled under its stable ID")
	}
}

func TestPreviewDeployStoresTheFullDeployAuthorityOnTheHostRow(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewDeployCommand()
	operation := *command.PreviewDeploy
	trustPreviewDeployCommits(t, jr.d, operation)
	jr.d.previewHostController = &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
	capturePreviewDeployEvents(t, jr.d)
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command); err != nil {
		t.Fatal(err)
	}
	host, found, err := jr.d.journal.PreviewHostByPreviewID(context.Background(), operation.PreviewID)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if host.ExecutionManifestID != operation.ExecutionManifestID ||
		host.ExecutionManifestDigest != operation.ExecutionManifestDigest ||
		host.CommitSetDigest != operation.CommitSetDigest ||
		!host.OriginalExpiresAt.Equal(operation.ExpiresAt) || !host.ExpiresAt.Equal(operation.ExpiresAt) ||
		host.PreviewKey != operation.PreviewKey || host.OrganizationID != operation.OrganizationID ||
		host.ProjectID != operation.ProjectID || host.BlueprintID != operation.Blueprint.BlueprintID ||
		host.BlueprintVersion != operation.Blueprint.Version || host.BlueprintDigest != operation.Blueprint.Digest {
		t.Fatalf("host=%+v operation=%+v", host, operation)
	}
	lifecycle := validHostedPreviewLifecycleCommand(v1.PreviewLifecycleDestroy, operation)
	if !hostedPreviewHostBindingMatches(*lifecycle.PreviewLifecycle, host) {
		t.Fatal("the stored row does not bind a lifecycle operation derived from the same deploy")
	}
}

func TestPreviewDeployReplaysStoredResultWhenEventWasPruned(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewDeployCommand()
	trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
	controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
	jr.d.previewHostController = controller
	stored := v1.PreviewDeployResultEvent{
		Kind:                         v1.EventPreviewDeployResult,
		PreviewDeployResultAuthority: v1.PreviewDeployResultAuthorityFromOperation(*command.PreviewDeploy),
		CommandMessageID:             previewDeployCommandID("1"),
		ObservedAt:                   time.Now().UTC().Truncate(time.Millisecond),
		Outcome:                      deployedPreviewOutcome(),
	}
	raw, _ := json.Marshal(stored)
	insertTestPreviewHost(t, jr.d, *command.PreviewDeploy, journal.PreviewHostDeployed, raw)
	events := capturePreviewDeployEvents(t, jr.d)
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("2")}, command); err != nil {
		t.Fatal(err)
	}
	deploys, _, _ := controller.counts()
	if deploys != 0 || len(*events) != 1 || (*events)[0].CommandMessageID != previewDeployCommandID("1") {
		t.Fatalf("deploys=%d events=%+v", deploys, *events)
	}
}

func insertTestPreviewHost(t *testing.T, d *Daemon, operation v1.PreviewDeployOperation, state journal.PreviewHostState, result []byte) {
	t.Helper()
	if _, inserted, err := d.journal.InsertPreviewHost(context.Background(), journal.PreviewHost{
		PreviewID: operation.PreviewID, PreviewKey: operation.PreviewKey, IdempotencyKey: operation.IdempotencyKey,
		OperationDigest: operation.OperationDigest, OrganizationID: operation.OrganizationID, ProjectID: operation.ProjectID,
		ExecutionManifestID: operation.ExecutionManifestID, ExecutionManifestDigest: operation.ExecutionManifestDigest,
		CommitSetDigest: operation.CommitSetDigest, BlueprintID: operation.Blueprint.BlueprintID,
		BlueprintVersion: operation.Blueprint.Version, BlueprintDigest: operation.Blueprint.Digest, MemoryMB: 1024,
		OriginalExpiresAt: operation.ExpiresAt, ExpiresAt: operation.ExpiresAt,
		State: state, Result: result,
	}); err != nil || !inserted {
		t.Fatalf("insert host inserted=%v err=%v", inserted, err)
	}
}

func TestPreviewDeployRefusesConflictingIntentUnderSameKeyOrPreview(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewDeployCommand()
	trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
	controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
	jr.d.previewHostController = controller
	capturePreviewDeployEvents(t, jr.d)
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command); err != nil {
		t.Fatal(err)
	}
	changed := validPreviewDeployCommand()
	changed.PreviewDeploy.ExpiresAt = changed.PreviewDeploy.ExpiresAt.Add(time.Hour)
	sealTestPreviewDeploy(changed.PreviewDeploy)
	err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("2")}, changed)
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonCommandConflict {
		t.Fatalf("same key reason=%q err=%v", reason, err)
	}
	otherKey := validPreviewDeployCommand()
	otherKey.PreviewDeploy.IdempotencyKey = "preview-deploy-" + strings.Repeat("e", 64)
	sealTestPreviewDeploy(otherKey.PreviewDeploy)
	err = jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("3")}, otherKey)
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonCommandConflict {
		t.Fatalf("live preview reason=%q err=%v", reason, err)
	}
	if deploys, _, _ := controller.counts(); deploys != 1 {
		t.Fatalf("deploys=%d", deploys)
	}
}

func TestPreviewDeployRefusesUntrustedCommitSet(t *testing.T) {
	tests := map[string]func(*testing.T, *Daemon, v1.PreviewDeployOperation){
		"unknown manifest": func(*testing.T, *Daemon, v1.PreviewDeployOperation) {},
		"different manifest digest": func(t *testing.T, d *Daemon, operation v1.PreviewDeployOperation) {
			operation.ExecutionManifestDigest = strings.Repeat("9", 64)
			trustPreviewDeployCommits(t, d, operation)
		},
		"missing repository": func(t *testing.T, d *Daemon, operation v1.PreviewDeployOperation) {
			operation.CommitSet = append(append([]v1.PreviewObservationCommit(nil), operation.CommitSet...),
				v1.PreviewObservationCommit{RepositoryID: "worker", CommitSHA: strings.Repeat("3", 40)})
			trustPreviewDeployCommits(t, d, operation)
		},
		"extra repository": func(t *testing.T, d *Daemon, operation v1.PreviewDeployOperation) {
			operation.CommitSet = operation.CommitSet[:1]
			trustPreviewDeployCommits(t, d, operation)
		},
		"sha mismatch": func(t *testing.T, d *Daemon, operation v1.PreviewDeployOperation) {
			operation.CommitSet = []v1.PreviewObservationCommit{operation.CommitSet[0], {RepositoryID: "web", CommitSHA: strings.Repeat("4", 40)}}
			trustPreviewDeployCommits(t, d, operation)
		},
	}
	for name, seed := range tests {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			command := validPreviewDeployCommand()
			seed(t, jr.d, *command.PreviewDeploy)
			controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
			jr.d.previewHostController = controller
			events := capturePreviewDeployEvents(t, jr.d)
			if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command); err != nil {
				t.Fatal(err)
			}
			deploys, _, _ := controller.counts()
			if deploys != 0 || len(*events) != 1 || (*events)[0].Outcome.Kind != v1.PreviewDeployFailed ||
				(*events)[0].Outcome.Phase != v1.PreviewDeployPhaseCommitSet {
				t.Fatalf("deploys=%d events=%+v", deploys, *events)
			}
			host, found, _ := jr.d.journal.PreviewHostByIdempotencyKey(context.Background(), command.PreviewDeploy.IdempotencyKey)
			if !found || host.State != journal.PreviewHostFailed {
				t.Fatalf("host=%+v", host)
			}
		})
	}
}

func TestPreviewDeployRejectsPausedRunnerAndExpiredLease(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewDeployCommand()
	trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
	controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
	jr.d.previewHostController = controller
	jr.d.paused.Store(true)
	err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command)
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonRunnerPaused {
		t.Fatalf("paused reason=%q err=%v", reason, err)
	}
	jr.d.paused.Store(false)
	command.PreviewDeploy.LeaseExpiresAt = time.Now().Add(-time.Second).UTC().Truncate(time.Millisecond)
	sealTestPreviewDeploy(command.PreviewDeploy)
	err = jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command)
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonLeaseRejected {
		t.Fatalf("lease reason=%q err=%v", reason, err)
	}
	if deploys, _, _ := controller.counts(); deploys != 0 {
		t.Fatal("rejected deploy reached controller")
	}
	if _, found, _ := jr.d.journal.PreviewHostByIdempotencyKey(context.Background(), command.PreviewDeploy.IdempotencyKey); found {
		t.Fatal("rejected deploy registered a host")
	}
}

func TestPreviewDeployResultAfterLeaseBecomesUncertain(t *testing.T) {
	for name, outcome := range map[string]v1.PreviewDeployOutcome{
		"late deployed":      deployedPreviewOutcome(),
		"late lease expired": {Kind: v1.PreviewDeployFailed, Phase: v1.PreviewDeployPhaseLeaseExpired},
	} {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			var clockMu sync.Mutex
			clock := time.Now().UTC().Truncate(time.Millisecond)
			jr.d.previewLeaseNow = func() time.Time {
				clockMu.Lock()
				defer clockMu.Unlock()
				return clock
			}
			command := validPreviewDeployCommand()
			command.PreviewDeploy.LeaseExpiresAt = clock.Add(time.Minute)
			sealTestPreviewDeploy(command.PreviewDeploy)
			trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
			controller := &fakePreviewHostController{
				deployOutcome: outcome, started: make(chan struct{}, 1), release: make(chan struct{}),
			}
			jr.d.previewHostController = controller
			events := capturePreviewDeployEvents(t, jr.d)
			go func() {
				<-controller.started
				clockMu.Lock()
				clock = clock.Add(2 * time.Minute)
				clockMu.Unlock()
				close(controller.release)
			}()
			if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command); err != nil {
				t.Fatal(err)
			}
			if len(*events) != 1 || !(*events)[0].ObservedAt.Equal(command.PreviewDeploy.LeaseExpiresAt) {
				t.Fatalf("events=%+v", *events)
			}
			expected := v1.PreviewDeployUncertain
			if outcome.Phase == v1.PreviewDeployPhaseLeaseExpired {
				expected = v1.PreviewDeployFailed
			}
			host, _, _ := jr.d.journal.PreviewHostByIdempotencyKey(context.Background(), command.PreviewDeploy.IdempotencyKey)
			if (*events)[0].Outcome.Kind != expected || host.State == journal.PreviewHostDeployed {
				t.Fatalf("outcome=%+v host=%s", (*events)[0].Outcome, host.State)
			}
		})
	}
}

func TestPreviewDeployRedactsLogTailAndRefusesInvalidControllerOutcome(t *testing.T) {
	redact.ForgetSecretsForTest()
	t.Cleanup(redact.ForgetSecretsForTest)
	secret := "remembered-preview-secret-value-1234567890"
	redact.RememberSecret(secret)
	jr, _ := testJobRunner(t)
	command := validPreviewDeployCommand()
	trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
	jr.d.previewHostController = &fakePreviewHostController{deployOutcome: v1.PreviewDeployOutcome{
		Kind: v1.PreviewDeployFailed, Phase: v1.PreviewDeployPhaseBuild, LogTail: "build failed: " + secret + "\n",
	}}
	events := capturePreviewDeployEvents(t, jr.d)
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 1 || strings.Contains((*events)[0].Outcome.LogTail, secret) || (*events)[0].Outcome.LogTail == "" {
		t.Fatalf("events=%+v", *events)
	}

	jr2, _ := testJobRunner(t)
	trustPreviewDeployCommits(t, jr2.d, *command.PreviewDeploy)
	jr2.d.previewHostController = &fakePreviewHostController{deployOutcome: v1.PreviewDeployOutcome{
		Kind: v1.PreviewDeployDeployed, Checks: []v1.PreviewObservationCheck{{CheckID: "other", Status: v1.PreviewObservationCheckPassed, HTTPStatus: 200}},
	}}
	invalid := capturePreviewDeployEvents(t, jr2.d)
	if err := jr2.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command); err != nil {
		t.Fatal(err)
	}
	if len(*invalid) != 1 || (*invalid)[0].Outcome.Kind != v1.PreviewDeployUncertain {
		t.Fatalf("checks that do not match the blueprint must become uncertain: %+v", *invalid)
	}
}

func TestPreviewDeployChecksMatchUnsortedBlueprintByID(t *testing.T) {
	content := v1.PreviewBlueprintContentV1{App: v1.PreviewBlueprintAppV1{HealthChecks: []v1.PreviewBlueprintHealthCheckV1{
		{ID: "root", Path: "/", ExpectedStatus: 200},
		{ID: "api", Path: "/api", ExpectedStatus: 204},
	}}}
	passed := func(ids ...string) []v1.PreviewObservationCheck {
		checks := make([]v1.PreviewObservationCheck, 0, len(ids))
		for _, id := range ids {
			checks = append(checks, v1.PreviewObservationCheck{CheckID: id, Status: v1.PreviewObservationCheckPassed, HTTPStatus: 200})
		}
		return checks
	}
	sorted := sanitizePreviewDeployOutcome(v1.PreviewDeployOutcome{Kind: v1.PreviewDeployDeployed, Checks: passed("api", "root")}, content, "")
	if sorted.Kind != v1.PreviewDeployDeployed || len(sorted.Checks) != 2 {
		t.Fatalf("id-sorted checks of an unsorted blueprint = %+v", sorted)
	}
	for name, checks := range map[string][]v1.PreviewObservationCheck{
		"blueprint order": passed("root", "api"),
		"missing":         passed("api"),
		"duplicate":       passed("api", "api"),
		"extra":           passed("api", "other", "root"),
		"trailing extra":  passed("api", "root", "zzz"),
		"foreign":         passed("api", "other"),
	} {
		outcome := sanitizePreviewDeployOutcome(v1.PreviewDeployOutcome{Kind: v1.PreviewDeployDeployed, Checks: checks}, content, "")
		if outcome.Kind != v1.PreviewDeployUncertain {
			t.Fatalf("%s: outcome = %+v", name, outcome)
		}
	}
	if !slices.Equal([]string{content.App.HealthChecks[0].ID, content.App.HealthChecks[1].ID}, []string{"root", "api"}) {
		t.Fatalf("blueprint checks were reordered: %+v", content.App.HealthChecks)
	}
}

func TestPreviewDeployRedactsProviderCredentialFromLogTail(t *testing.T) {
	redact.ForgetSecretsForTest()
	t.Cleanup(redact.ForgetSecretsForTest)
	token := "ghs_previewdeployprovidercredential0123456789"
	jr, _ := testJobRunner(t)
	command := validPreviewDeployCommand()
	trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
	jr.d.previewHostController = &fakePreviewHostController{deployOutcome: v1.PreviewDeployOutcome{
		Kind: v1.PreviewDeployFailed, Phase: v1.PreviewDeployPhaseBuild,
		LogTail: "clone https://x-access-token:" + token + "@github.com/acme/web\nauth " + token + "\n",
	}}
	events := capturePreviewDeployEvents(t, jr.d)
	envelope := v1.Envelope{ID: previewDeployCommandID("1"), ProviderCredential: &v1.ProviderCredential{
		Kind: v1.ProviderCredentialGitHubInstallationToken, Token: token,
		ExpiresAt: time.Now().Add(time.Hour), RepositoryIDs: []string{"web"},
	}}
	if err := jr.d.executeCommand(context.Background(), envelope, command); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 1 || (*events)[0].Outcome.LogTail == "" {
		t.Fatalf("events=%+v", *events)
	}
	raw, _ := json.Marshal((*events)[0])
	if strings.Contains(string(raw), token) {
		t.Fatalf("result carries the provider credential: %s", raw)
	}
	host, _, _ := jr.d.journal.PreviewHostByIdempotencyKey(context.Background(), command.PreviewDeploy.IdempotencyKey)
	if strings.Contains(string(host.Result), token) {
		t.Fatal("stored result carries the provider credential")
	}
	if !slices.Contains(redact.RememberedSecrets(), token) {
		t.Fatal("provider credential was not registered for redaction")
	}
}

func TestPreviewDeployUsesNonBlockingLaneAndDeduplicatesInflight(t *testing.T) {
	jr, _ := testJobRunner(t)
	command := validPreviewDeployCommand()
	trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
	controller := &fakePreviewHostController{
		deployOutcome: deployedPreviewOutcome(), started: make(chan struct{}, 1), release: make(chan struct{}),
	}
	jr.d.previewHostController = controller
	events := capturePreviewDeployEvents(t, jr.d)
	envelope := commandEnvelope(previewDeployCommandID("1"), command)
	returned := make(chan error, 1)
	go func() { returned <- jr.d.OnCommand(context.Background(), envelope) }()
	select {
	case <-controller.started:
	case <-time.After(time.Second):
		t.Fatal("controller did not start")
	}
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("deploy blocked command loop")
	}
	if err := jr.d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if err := jr.d.OnCommand(context.Background(), commandEnvelope(previewDeployCommandID("2"), command)); err != nil {
		t.Fatal(err)
	}
	jr.d.previewDeployCommandMu.Lock()
	jr.d.previewDeployCommandMu.Unlock()
	close(controller.release)
	jr.d.previewDeployCommands.Wait()
	if deploys, _, recovered := controller.counts(); deploys != 1 || recovered != 0 || len(*events) != 1 {
		t.Fatalf("deploys=%d recovered=%d events=%d", deploys, recovered, len(*events))
	}
}

func TestPreviewDeployCrashRedeliveryNeverRerunsTheEffect(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.commandOwner = "preview-deploy-process-new"
	command := validPreviewDeployCommand()
	trustPreviewDeployCommits(t, d, *command.PreviewDeploy)
	insertTestPreviewHost(t, d, *command.PreviewDeploy, journal.PreviewHostDeploying, nil)
	controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome(), recoverErr: errors.New("teardown unproven")}
	d.previewHostController = controller
	events := capturePreviewDeployEvents(t, d)
	envelope := commandEnvelope(previewDeployCommandID("1"), command)
	persistUnexecutedCommand(t, d, envelope, "preview-deploy-process-old")
	if err := d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	d.previewDeployCommands.Wait()
	deploys, _, recovered := controller.counts()
	if deploys != 0 || recovered != 1 || len(*events) != 1 || (*events)[0].Outcome.Kind != v1.PreviewDeployUncertain {
		t.Fatalf("deploys=%d recovered=%d events=%+v", deploys, recovered, *events)
	}
	host, _, _ := d.journal.PreviewHostByIdempotencyKey(context.Background(), command.PreviewDeploy.IdempotencyKey)
	if host.State != journal.PreviewHostUncertain {
		t.Fatalf("host state=%s", host.State)
	}
	if receipt := commandReceipt(t, d, envelope); receipt.AckStatus != string(v1.CommandAckAccepted) {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestPreviewDeployCrashRedeliveryWithProvenTeardownMarksTheHostDestroyed(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	command := validPreviewDeployCommand()
	insertTestPreviewHost(t, d, *command.PreviewDeploy, journal.PreviewHostDeploying, nil)
	controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
	d.previewHostController = controller
	events := capturePreviewDeployEvents(t, d)
	if err := d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command); err != nil {
		t.Fatal(err)
	}
	deploys, _, recovered := controller.counts()
	if deploys != 0 || recovered != 1 || len(*events) != 1 || (*events)[0].Outcome.Kind != v1.PreviewDeployUncertain {
		t.Fatalf("deploys=%d recovered=%d events=%+v", deploys, recovered, *events)
	}
	host, _, _ := d.journal.PreviewHostByIdempotencyKey(context.Background(), command.PreviewDeploy.IdempotencyKey)
	if host.State != journal.PreviewHostDestroyed {
		t.Fatalf("host state=%s", host.State)
	}
	var result v1.PreviewDeployResultEvent
	if err := json.Unmarshal(host.Result, &result); err != nil || result.Outcome.Kind != v1.PreviewDeployUncertain {
		t.Fatalf("stored result=%s err=%v", host.Result, err)
	}
}

func TestPreviewDeployCrashRedeliveryWithoutRegistryRowRunsOnce(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.commandOwner = "preview-deploy-process-new"
	command := validPreviewDeployCommand()
	trustPreviewDeployCommits(t, d, *command.PreviewDeploy)
	controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
	d.previewHostController = controller
	events := capturePreviewDeployEvents(t, d)
	envelope := commandEnvelope(previewDeployCommandID("1"), command)
	persistUnexecutedCommand(t, d, envelope, "preview-deploy-process-old")
	if err := d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	d.previewDeployCommands.Wait()
	if deploys, _, _ := controller.counts(); deploys != 1 || len(*events) != 1 {
		t.Fatalf("deploys=%d events=%d", deploys, len(*events))
	}
}

func validHostedPreviewLifecycleCommand(action v1.PreviewLifecycleAction, deploy v1.PreviewDeployOperation) v1.CommandPayload {
	now := time.Now().UTC().Truncate(time.Millisecond)
	operation := v1.PreviewLifecycleOperation{
		PreviewLifecycleOperationAuthority: v1.PreviewLifecycleOperationAuthority{
			SchemaVersion:            v1.PreviewLifecycleSchemaVersion,
			PreviewLifecycleEffectID: "preview-lifecycle-effect-1", PreviewLifecycleEffectRevision: 1,
			PreviewLifecycleOperationID: "preview-lifecycle-operation-" + strings.Repeat("a", 64),
			OrganizationID:              deploy.OrganizationID, ProjectID: deploy.ProjectID, WorkflowRunID: deploy.WorkflowRunID,
			ExecutionManifestID: deploy.ExecutionManifestID, ExecutionManifestDigest: deploy.ExecutionManifestDigest,
			Action: action, IdempotencyKey: "preview-lifecycle-" + strings.Repeat("d", 64), LeaseID: "lease-1", FencingToken: 4,
			LeaseExpiresAt: now.Add(time.Minute),
		},
		PreviewID: deploy.PreviewID, PreviewKey: deploy.PreviewKey,
		Blueprint: deploy.Blueprint.Identity(), CommitSet: deploy.CommitSet,
		OriginalExpiresAt: deploy.ExpiresAt, CurrentEffectiveExpiresAt: deploy.ExpiresAt,
	}
	if action == v1.PreviewLifecycleExtend {
		desired := deploy.ExpiresAt.Add(time.Hour)
		operation.DesiredExpiresAt = &desired
	}
	operation.CommitSetDigest = v1.PreviewLifecycleCommitSetDigest(operation.CommitSet)
	operation.ResourceBindingDigest = v1.PreviewHostedLifecycleResourceBindingDigest(operation)
	operation.OperationDigest = v1.PreviewHostedLifecycleOperationDigest(operation)
	operation.CommandDigest = v1.PreviewLifecycleCommandDigest(operation)
	return v1.CommandPayload{Command: v1.CmdPreviewLifecycle, PreviewLifecycle: &operation}
}

func captureLifecycleEvents(d *Daemon) *[]v1.PreviewLifecycleResultEvent {
	events := &[]v1.PreviewLifecycleResultEvent{}
	d.eventSink = func(_ context.Context, payload any) error {
		*events = append(*events, payload.(v1.PreviewLifecycleResultEvent))
		return nil
	}
	return events
}

func TestHostedPreviewLifecycleRoutesToHostController(t *testing.T) {
	deploy := *validPreviewDeployCommand().PreviewDeploy
	t.Run("unsupported without host controller", func(t *testing.T) {
		jr, _ := testJobRunner(t)
		jr.d.previewLifecycleController = &fakePreviewLifecycleController{}
		err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewLifecycleCommandID()}, validHostedPreviewLifecycleCommand(v1.PreviewLifecycleDestroy, deploy))
		if reason := commandRejectionReason(err); reason != v1.CommandAckReasonUnsupportedCommand {
			t.Fatalf("reason=%q err=%v", reason, err)
		}
	})
	t.Run("destroy of unknown preview is already absent", func(t *testing.T) {
		jr, _ := testJobRunner(t)
		external := &fakePreviewLifecycleController{}
		jr.d.previewLifecycleController = external
		controller := &fakePreviewHostController{}
		jr.d.previewHostController = controller
		events := captureLifecycleEvents(jr.d)
		if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewLifecycleCommandID()}, validHostedPreviewLifecycleCommand(v1.PreviewLifecycleDestroy, deploy)); err != nil {
			t.Fatal(err)
		}
		if _, lifecycles, _ := controller.counts(); lifecycles != 0 || external.callCount() != 0 || len(*events) != 1 ||
			(*events)[0].Outcome.Kind != v1.PreviewLifecycleSucceeded ||
			(*events)[0].Outcome.ResourceState.Disposition != v1.PreviewLifecycleAlreadyAbsent {
			t.Fatalf("lifecycles=%d events=%+v", lifecycles, *events)
		}
		raw, _ := json.Marshal((*events)[0])
		if strings.Contains(string(raw), "preview_url") || strings.Contains(string(raw), "preview_evidence_id") {
			t.Fatalf("hosted result carried external fields: %s", raw)
		}
	})
	t.Run("extend of deployed preview updates the registry", func(t *testing.T) {
		jr, _ := testJobRunner(t)
		command := validHostedPreviewLifecycleCommand(v1.PreviewLifecycleExtend, deploy)
		blueprint := command.PreviewLifecycle.Blueprint
		controller := &fakePreviewHostController{lifecycleOutcome: v1.PreviewLifecycleResultOutcome{
			Kind: v1.PreviewLifecycleSucceeded,
			ResourceState: &v1.PreviewLifecycleResourceState{
				Kind: v1.PreviewLifecycleResourceActive, Blueprint: &blueprint,
				CommitSet: command.PreviewLifecycle.CommitSet, ExpiresAt: command.PreviewLifecycle.DesiredExpiresAt,
			},
		}}
		jr.d.previewHostController = controller
		insertTestPreviewHost(t, jr.d, deploy, journal.PreviewHostDeployed, []byte(`{}`))
		events := captureLifecycleEvents(jr.d)
		if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewLifecycleCommandID()}, command); err != nil {
			t.Fatal(err)
		}
		host, _, _ := jr.d.journal.PreviewHostByPreviewID(context.Background(), deploy.PreviewID)
		if _, lifecycles, _ := controller.counts(); lifecycles != 1 || len(*events) != 1 ||
			(*events)[0].Outcome.Kind != v1.PreviewLifecycleSucceeded || !host.ExpiresAt.Equal(*command.PreviewLifecycle.DesiredExpiresAt) {
			t.Fatalf("lifecycles=%d events=%+v host=%+v", lifecycles, *events, host)
		}
	})
	t.Run("destroy of deployed preview marks it destroyed", func(t *testing.T) {
		jr, _ := testJobRunner(t)
		controller := &fakePreviewHostController{lifecycleOutcome: v1.PreviewLifecycleResultOutcome{
			Kind:          v1.PreviewLifecycleSucceeded,
			ResourceState: &v1.PreviewLifecycleResourceState{Kind: v1.PreviewLifecycleResourceDestroyed, Disposition: v1.PreviewLifecycleDestroyed},
		}}
		jr.d.previewHostController = controller
		insertTestPreviewHost(t, jr.d, deploy, journal.PreviewHostDeployed, []byte(`{}`))
		events := captureLifecycleEvents(jr.d)
		if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewLifecycleCommandID()}, validHostedPreviewLifecycleCommand(v1.PreviewLifecycleDestroy, deploy)); err != nil {
			t.Fatal(err)
		}
		host, _, _ := jr.d.journal.PreviewHostByPreviewID(context.Background(), deploy.PreviewID)
		if len(*events) != 1 || (*events)[0].Outcome.Kind != v1.PreviewLifecycleSucceeded || host.State != journal.PreviewHostDestroyed {
			t.Fatalf("events=%+v host=%s", *events, host.State)
		}
	})
	t.Run("binding mismatch is permanent", func(t *testing.T) {
		jr, _ := testJobRunner(t)
		controller := &fakePreviewHostController{}
		jr.d.previewHostController = controller
		other := deploy
		other.PreviewKey = "zzzzzzzzzzzzzzzzzzzzzzzzzz"
		insertTestPreviewHost(t, jr.d, other, journal.PreviewHostDeployed, []byte(`{}`))
		events := captureLifecycleEvents(jr.d)
		if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewLifecycleCommandID()}, validHostedPreviewLifecycleCommand(v1.PreviewLifecycleDestroy, deploy)); err != nil {
			t.Fatal(err)
		}
		if _, lifecycles, _ := controller.counts(); lifecycles != 0 || len(*events) != 1 ||
			(*events)[0].Outcome.Code != v1.PreviewLifecycleResourceBindingMismatch {
			t.Fatalf("lifecycles=%d events=%+v", lifecycles, *events)
		}
	})
	t.Run("extend of unknown preview is not found", func(t *testing.T) {
		jr, _ := testJobRunner(t)
		jr.d.previewHostController = &fakePreviewHostController{}
		events := captureLifecycleEvents(jr.d)
		if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewLifecycleCommandID()}, validHostedPreviewLifecycleCommand(v1.PreviewLifecycleExtend, deploy)); err != nil {
			t.Fatal(err)
		}
		if len(*events) != 1 || (*events)[0].Outcome.Code != v1.PreviewLifecycleResourceNotFound {
			t.Fatalf("events=%+v", *events)
		}
	})
	t.Run("hosted digests are required", func(t *testing.T) {
		jr, _ := testJobRunner(t)
		jr.d.previewHostController = &fakePreviewHostController{}
		command := validHostedPreviewLifecycleCommand(v1.PreviewLifecycleDestroy, deploy)
		command.PreviewLifecycle.ResourceBindingDigest = v1.PreviewLifecycleResourceBindingDigest(*command.PreviewLifecycle)
		err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewLifecycleCommandID()}, command)
		if reason := commandRejectionReason(err); reason != v1.CommandAckReasonInvalidCommand {
			t.Fatalf("reason=%q err=%v", reason, err)
		}
	})
}
