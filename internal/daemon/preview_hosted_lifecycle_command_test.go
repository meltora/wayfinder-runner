package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
)

func sealHostedPreviewLifecycle(operation *v1.PreviewLifecycleOperation) {
	operation.CommitSetDigest = v1.PreviewLifecycleCommitSetDigest(operation.CommitSet)
	operation.ResourceBindingDigest = v1.PreviewHostedLifecycleResourceBindingDigest(*operation)
	operation.OperationDigest = v1.PreviewHostedLifecycleOperationDigest(*operation)
	operation.CommandDigest = v1.PreviewLifecycleCommandDigest(*operation)
}

func hostedExtendSucceeded(operation v1.PreviewLifecycleOperation) v1.PreviewLifecycleResultOutcome {
	blueprint := operation.Blueprint
	return v1.PreviewLifecycleResultOutcome{
		Kind: v1.PreviewLifecycleSucceeded,
		ResourceState: &v1.PreviewLifecycleResourceState{
			Kind: v1.PreviewLifecycleResourceActive, Blueprint: &blueprint,
			CommitSet: operation.CommitSet, ExpiresAt: operation.DesiredExpiresAt,
		},
	}
}

func insertHostedPreviewHost(t *testing.T, d *Daemon, deploy v1.PreviewDeployOperation, mutate func(*journal.PreviewHost)) {
	t.Helper()
	host := journal.PreviewHost{
		PreviewID: deploy.PreviewID, PreviewKey: deploy.PreviewKey, IdempotencyKey: deploy.IdempotencyKey,
		OperationDigest: deploy.OperationDigest, OrganizationID: deploy.OrganizationID, ProjectID: deploy.ProjectID,
		ExecutionManifestID: deploy.ExecutionManifestID, ExecutionManifestDigest: deploy.ExecutionManifestDigest,
		CommitSetDigest: deploy.CommitSetDigest, BlueprintID: deploy.Blueprint.BlueprintID,
		BlueprintVersion: deploy.Blueprint.Version, BlueprintDigest: deploy.Blueprint.Digest, MemoryMB: 1024,
		OriginalExpiresAt: deploy.ExpiresAt, ExpiresAt: deploy.ExpiresAt,
		State: journal.PreviewHostDeployed, Result: []byte(`{}`),
	}
	if mutate != nil {
		mutate(&host)
	}
	if _, inserted, err := d.journal.InsertPreviewHost(context.Background(), host); err != nil || !inserted {
		t.Fatalf("insert host inserted=%v err=%v", inserted, err)
	}
}

func hostedLifecycleCommandID(suffix string) string {
	return "preview-lifecycle-command-" + strings.Repeat(suffix, 64)
}

func TestHostedPreviewLifecycleReplaysTerminalResultForSameKey(t *testing.T) {
	jr, _ := testJobRunner(t)
	deploy := *validPreviewDeployCommand().PreviewDeploy
	command := validHostedPreviewLifecycleCommand(v1.PreviewLifecycleExtend, deploy)
	controller := &fakePreviewHostController{lifecycleOutcome: hostedExtendSucceeded(*command.PreviewLifecycle)}
	jr.d.previewHostController = controller
	insertHostedPreviewHost(t, jr.d, deploy, nil)
	events := captureLifecycleEvents(jr.d)
	for _, id := range []string{hostedLifecycleCommandID("3"), hostedLifecycleCommandID("4")} {
		if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: id}, command); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if _, lifecycles, _ := controller.counts(); lifecycles != 1 || len(*events) != 1 {
		t.Fatalf("lifecycles=%d events=%+v", lifecycles, *events)
	}
	ctx := context.Background()
	if exists, _ := jr.d.journal.HasRecord(ctx, previewLifecycleResultEventID(hostedLifecycleCommandID("3"))); !exists {
		t.Fatal("result is not journalled under its stable event ID")
	}
	if exists, _ := jr.d.journal.HasRecord(ctx, previewLifecycleResultEventID(hostedLifecycleCommandID("4"))); exists {
		t.Fatal("replay published a second result under a new event ID")
	}
	stored, found, err := jr.d.journal.PreviewHostedLifecycleResultByKey(ctx, command.PreviewLifecycle.IdempotencyKey)
	if err != nil || !found || stored.OperationDigest != command.PreviewLifecycle.OperationDigest ||
		stored.EventID != previewLifecycleResultEventID(hostedLifecycleCommandID("3")) {
		t.Fatalf("stored=%+v found=%v err=%v", stored, found, err)
	}
}

func TestHostedPreviewLifecycleReplaysStoredResultWhenEventWasPruned(t *testing.T) {
	jr, _ := testJobRunner(t)
	deploy := *validPreviewDeployCommand().PreviewDeploy
	command := validHostedPreviewLifecycleCommand(v1.PreviewLifecycleExtend, deploy)
	controller := &fakePreviewHostController{lifecycleOutcome: hostedExtendSucceeded(*command.PreviewLifecycle)}
	jr.d.previewHostController = controller
	insertHostedPreviewHost(t, jr.d, deploy, nil)
	outcome := hostedExtendSucceeded(*command.PreviewLifecycle)
	outcome.ResourceBindingDigest = command.PreviewLifecycle.ResourceBindingDigest
	outcome.ControllerReceiptDigest = v1.PreviewHostedLifecycleReceiptDigest(*command.PreviewLifecycle, *outcome.ResourceState)
	original := v1.PreviewLifecycleResultEvent{
		Kind:                            v1.EventPreviewLifecycleResult,
		PreviewLifecycleResultAuthority: v1.PreviewLifecycleResultAuthorityFromOperation(*command.PreviewLifecycle),
		CommandMessageID:                hostedLifecycleCommandID("3"),
		ObservedAt:                      time.Now().UTC().Truncate(time.Millisecond),
		Outcome:                         outcome,
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	eventID := previewLifecycleResultEventID(hostedLifecycleCommandID("3"))
	if _, _, err := jr.d.journal.RecordPreviewHostedLifecycleResult(context.Background(), journal.PreviewHostedLifecycleResult{
		IdempotencyKey: command.PreviewLifecycle.IdempotencyKey, OperationDigest: command.PreviewLifecycle.OperationDigest,
		EventID: eventID, Result: raw,
	}, nil); err != nil {
		t.Fatal(err)
	}
	events := captureLifecycleEvents(jr.d)
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: hostedLifecycleCommandID("4")}, command); err != nil {
		t.Fatal(err)
	}
	if _, lifecycles, _ := controller.counts(); lifecycles != 0 || len(*events) != 1 ||
		(*events)[0].CommandMessageID != hostedLifecycleCommandID("3") || (*events)[0].Outcome.Kind != v1.PreviewLifecycleSucceeded {
		t.Fatalf("lifecycles=%d events=%+v", lifecycles, *events)
	}
	if exists, _ := jr.d.journal.HasRecord(context.Background(), eventID); !exists {
		t.Fatal("replay did not use the stored stable event ID")
	}
}

func TestHostedPreviewLifecycleRefusesSameKeyWithDifferentOperation(t *testing.T) {
	jr, _ := testJobRunner(t)
	deploy := *validPreviewDeployCommand().PreviewDeploy
	first := validHostedPreviewLifecycleCommand(v1.PreviewLifecycleExtend, deploy)
	controller := &fakePreviewHostController{lifecycleOutcome: hostedExtendSucceeded(*first.PreviewLifecycle)}
	jr.d.previewHostController = controller
	insertHostedPreviewHost(t, jr.d, deploy, nil)
	events := captureLifecycleEvents(jr.d)
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: hostedLifecycleCommandID("3")}, first); err != nil {
		t.Fatal(err)
	}
	second := validHostedPreviewLifecycleCommand(v1.PreviewLifecycleExtend, deploy)
	desired := second.PreviewLifecycle.DesiredExpiresAt.Add(time.Hour)
	second.PreviewLifecycle.DesiredExpiresAt = &desired
	sealHostedPreviewLifecycle(second.PreviewLifecycle)
	if second.PreviewLifecycle.IdempotencyKey != first.PreviewLifecycle.IdempotencyKey ||
		second.PreviewLifecycle.OperationDigest == first.PreviewLifecycle.OperationDigest {
		t.Fatal("test commands must share the key and differ in operation digest")
	}
	err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: hostedLifecycleCommandID("4")}, second)
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonCommandConflict {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
	host, _, _ := jr.d.journal.PreviewHostByPreviewID(context.Background(), deploy.PreviewID)
	if _, lifecycles, _ := controller.counts(); lifecycles != 1 || len(*events) != 1 ||
		!host.ExpiresAt.Equal(*first.PreviewLifecycle.DesiredExpiresAt) {
		t.Fatalf("lifecycles=%d events=%d expires_at=%s", lifecycles, len(*events), host.ExpiresAt)
	}
}

func TestHostedPreviewLifecycleReplaysAfterRestart(t *testing.T) {
	ctx := context.Background()
	deploy := *validPreviewDeployCommand().PreviewDeploy
	command := validHostedPreviewLifecycleCommand(v1.PreviewLifecycleExtend, deploy)
	before, _ := testJobRunner(t)
	path := filepath.Join(before.d.cfg.StateDir, "journal.db")
	before.d.previewHostController = &fakePreviewHostController{lifecycleOutcome: hostedExtendSucceeded(*command.PreviewLifecycle)}
	insertHostedPreviewHost(t, before.d, deploy, nil)
	captureLifecycleEvents(before.d)
	if err := before.d.executeCommand(ctx, v1.Envelope{ID: hostedLifecycleCommandID("3")}, command); err != nil {
		t.Fatal(err)
	}
	if err := before.d.journal.Close(); err != nil {
		t.Fatal(err)
	}

	after, _ := testJobRunner(t)
	_ = after.d.journal.Close()
	reopened, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	after.d.journal = reopened
	controller := &fakePreviewHostController{lifecycleOutcome: hostedExtendSucceeded(*command.PreviewLifecycle)}
	after.d.previewHostController = controller
	events := captureLifecycleEvents(after.d)
	if err := after.d.executeCommand(ctx, v1.Envelope{ID: hostedLifecycleCommandID("4")}, command); err != nil {
		t.Fatal(err)
	}
	if _, lifecycles, _ := controller.counts(); lifecycles != 0 || len(*events) != 0 {
		t.Fatalf("lifecycles=%d events=%+v", lifecycles, *events)
	}
	if exists, _ := reopened.HasRecord(ctx, previewLifecycleResultEventID(hostedLifecycleCommandID("4"))); exists {
		t.Fatal("restart replay published under a new event ID")
	}
	changed := validHostedPreviewLifecycleCommand(v1.PreviewLifecycleExtend, deploy)
	desired := changed.PreviewLifecycle.DesiredExpiresAt.Add(time.Hour)
	changed.PreviewLifecycle.DesiredExpiresAt = &desired
	sealHostedPreviewLifecycle(changed.PreviewLifecycle)
	err = after.d.executeCommand(ctx, v1.Envelope{ID: hostedLifecycleCommandID("5")}, changed)
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonCommandConflict {
		t.Fatalf("conflict after restart reason=%q err=%v", reason, err)
	}
}

func TestHostedPreviewLifecycleBindingMismatchCoversDeployAuthority(t *testing.T) {
	deploy := *validPreviewDeployCommand().PreviewDeploy
	mutations := map[string]func(*journal.PreviewHost){
		"execution manifest id":     func(host *journal.PreviewHost) { host.ExecutionManifestID = "manifest-2" },
		"execution manifest digest": func(host *journal.PreviewHost) { host.ExecutionManifestDigest = strings.Repeat("9", 64) },
		"commit set digest":         func(host *journal.PreviewHost) { host.CommitSetDigest = strings.Repeat("8", 64) },
		"original expires at": func(host *journal.PreviewHost) {
			host.OriginalExpiresAt = host.OriginalExpiresAt.Add(-time.Minute)
		},
	}
	for name, mutate := range mutations {
		for _, action := range []v1.PreviewLifecycleAction{v1.PreviewLifecycleDestroy, v1.PreviewLifecycleExtend} {
			t.Run(name+" "+string(action), func(t *testing.T) {
				jr, _ := testJobRunner(t)
				command := validHostedPreviewLifecycleCommand(action, deploy)
				controller := &fakePreviewHostController{lifecycleOutcome: hostedExtendSucceeded(*command.PreviewLifecycle)}
				jr.d.previewHostController = controller
				insertHostedPreviewHost(t, jr.d, deploy, mutate)
				events := captureLifecycleEvents(jr.d)
				if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: hostedLifecycleCommandID("3")}, command); err != nil {
					t.Fatal(err)
				}
				if _, lifecycles, _ := controller.counts(); lifecycles != 0 || len(*events) != 1 ||
					(*events)[0].Outcome.Code != v1.PreviewLifecycleResourceBindingMismatch {
					t.Fatalf("lifecycles=%d events=%+v", lifecycles, *events)
				}
			})
		}
	}
}

func TestHostedPreviewExtendRefusesStaleCurrentEffectiveExpiry(t *testing.T) {
	deploy := *validPreviewDeployCommand().PreviewDeploy
	cases := map[string]struct{ commandAhead, rowAhead time.Duration }{
		"command ahead of registry": {commandAhead: time.Minute},
		"registry ahead of command": {rowAhead: time.Hour},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			command := validHostedPreviewLifecycleCommand(v1.PreviewLifecycleExtend, deploy)
			command.PreviewLifecycle.CurrentEffectiveExpiresAt = command.PreviewLifecycle.CurrentEffectiveExpiresAt.Add(test.commandAhead)
			sealHostedPreviewLifecycle(command.PreviewLifecycle)
			controller := &fakePreviewHostController{lifecycleOutcome: hostedExtendSucceeded(*command.PreviewLifecycle)}
			jr.d.previewHostController = controller
			rowExpiresAt := deploy.ExpiresAt.Add(test.rowAhead)
			insertHostedPreviewHost(t, jr.d, deploy, func(host *journal.PreviewHost) { host.ExpiresAt = rowExpiresAt })
			events := captureLifecycleEvents(jr.d)
			err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: hostedLifecycleCommandID("3")}, command)
			if err != nil {
				t.Fatal(err)
			}
			host, _, _ := jr.d.journal.PreviewHostByPreviewID(context.Background(), deploy.PreviewID)
			if _, lifecycles, _ := controller.counts(); lifecycles != 0 || len(*events) != 1 || !host.ExpiresAt.Equal(rowExpiresAt) {
				t.Fatalf("lifecycles=%d events=%d expires_at=%s", lifecycles, len(*events), host.ExpiresAt)
			}
			outcome := (*events)[0].Outcome
			if outcome.Kind != v1.PreviewLifecyclePermanent || outcome.Code != v1.PreviewLifecycleResourceBindingMismatch {
				t.Fatalf("outcome=%+v", outcome)
			}
			if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: hostedLifecycleCommandID("4")}, command); err != nil {
				t.Fatal(err)
			}
			stored, found, err := jr.d.journal.PreviewHostedLifecycleResultByKey(context.Background(), command.PreviewLifecycle.IdempotencyKey)
			if _, lifecycles, _ := controller.counts(); err != nil || !found || lifecycles != 0 || len(*events) != 1 ||
				stored.EventID != previewLifecycleResultEventID(hostedLifecycleCommandID("3")) {
				t.Fatalf("replay lifecycles=%d events=%d stored=%+v found=%v err=%v", lifecycles, len(*events), stored, found, err)
			}
		})
	}
}
