package daemon

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/previewhost"
)

type previewHostingEvents struct {
	mu         sync.Mutex
	destroyed  []v1.PreviewHostedDestroyedEvent
	deploys    []v1.PreviewDeployResultEvent
	lifecycles []v1.PreviewLifecycleResultEvent
}

func capturePreviewHostingEvents(d *Daemon) *previewHostingEvents {
	events := &previewHostingEvents{}
	d.eventSink = func(_ context.Context, payload any) error {
		events.mu.Lock()
		defer events.mu.Unlock()
		switch event := payload.(type) {
		case v1.PreviewHostedDestroyedEvent:
			events.destroyed = append(events.destroyed, event)
		case v1.PreviewDeployResultEvent:
			events.deploys = append(events.deploys, event)
		case v1.PreviewLifecycleResultEvent:
			events.lifecycles = append(events.lifecycles, event)
		}
		return nil
	}
	return events
}

func (events *previewHostingEvents) snapshot() ([]v1.PreviewHostedDestroyedEvent, []v1.PreviewDeployResultEvent, []v1.PreviewLifecycleResultEvent) {
	events.mu.Lock()
	defer events.mu.Unlock()
	return slices.Clone(events.destroyed), slices.Clone(events.deploys), slices.Clone(events.lifecycles)
}

func allowPreviewHostingPause(t *testing.T, d *Daemon) {
	t.Helper()
	if err := os.Chmod(d.cfg.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func pausePreviewHostingDaemon(t *testing.T, d *Daemon) {
	t.Helper()
	allowPreviewHostingPause(t, d)
	if err := d.Pause(); err != nil {
		t.Fatal(err)
	}
}

func previewDeployCommandFor(previewID, keySuffix string) v1.CommandPayload {
	command := validPreviewDeployCommand()
	operation := command.PreviewDeploy
	operation.PreviewID = previewID
	operation.IdempotencyKey = "preview-deploy-" + strings.Repeat(keySuffix, 64)
	operation.PreviewDeployOperationID = "preview-deploy-operation-" + strings.Repeat(keySuffix, 64)
	sealTestPreviewDeploy(operation)
	return command
}

func insertPreviewHostRow(
	t *testing.T,
	d *Daemon,
	previewID, keySuffix string,
	state journal.PreviewHostState,
	expiresAt time.Time,
	reason v1.PreviewHostedDestroyReason,
) journal.PreviewHost {
	t.Helper()
	deploy := *previewDeployCommandFor(previewID, keySuffix).PreviewDeploy
	var stored journal.PreviewHost
	insertHostedPreviewHost(t, d, deploy, func(host *journal.PreviewHost) {
		host.State = state
		host.ExpiresAt = expiresAt
		host.DestroyReason = reason
		stored = *host
	})
	return stored
}

func previewHostState(t *testing.T, d *Daemon, idempotencyKey string) journal.PreviewHost {
	t.Helper()
	host, found, err := d.journal.PreviewHostByIdempotencyKey(context.Background(), idempotencyKey)
	if err != nil || !found {
		t.Fatalf("host %s found=%v err=%v", idempotencyKey, found, err)
	}
	return host
}

func recoveredPreviewIDs(controller *fakePreviewHostController) []string {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	ids := make([]string, 0, len(controller.recovered))
	for _, host := range controller.recovered {
		ids = append(ids, host.PreviewID)
	}
	slices.Sort(ids)
	return ids
}

type listingPreviewHostController struct {
	*fakePreviewHostController
	ids []string
}

func (controller listingPreviewHostController) HostedPreviewIDs(context.Context) ([]string, error) {
	return slices.Clone(controller.ids), nil
}

type cancellablePreviewHostController struct {
	*fakePreviewHostController
	cancelled chan struct{}
}

func (controller cancellablePreviewHostController) Deploy(ctx context.Context, _ previewhost.DeployRequest) previewhost.DeployResult {
	controller.mu.Lock()
	controller.deploys++
	controller.mu.Unlock()
	controller.started <- struct{}{}
	<-ctx.Done()
	close(controller.cancelled)
	return previewhost.DeployResult{Outcome: v1.PreviewDeployOutcome{Kind: v1.PreviewDeployFailed, Phase: v1.PreviewDeployPhaseInstall}}
}

func TestPreviewDeployRefusesWhenConcurrencyIsExhausted(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.cfg.PreviewHosting.MaxConcurrent = 2
	d.cfg.PreviewHosting.MemoryBudgetMB = 1 << 20
	future := time.Now().Add(time.Hour)
	insertPreviewHostRow(t, d, "preview-a", "a", journal.PreviewHostDeployed, future, "")
	insertPreviewHostRow(t, d, "preview-b", "b", journal.PreviewHostDeploying, future, "")
	insertPreviewHostRow(t, d, "preview-c", "c", journal.PreviewHostUncertain, future, "")
	command := validPreviewDeployCommand()
	trustPreviewDeployCommits(t, d, *command.PreviewDeploy)
	controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
	d.previewHostController = controller
	events := capturePreviewHostingEvents(d)
	if err := d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command); err != nil {
		t.Fatal(err)
	}
	_, deploys, _ := events.snapshot()
	if count, _, _ := controller.counts(); count != 0 || len(deploys) != 1 || deploys[0].Outcome.Kind != v1.PreviewDeployCapacityExhausted {
		t.Fatalf("deploys=%d events=%+v", count, deploys)
	}
	if host := previewHostState(t, d, command.PreviewDeploy.IdempotencyKey); host.State != journal.PreviewHostFailed {
		t.Fatalf("capacity refusal state=%s", host.State)
	}
	if err := d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("2")}, command); err != nil {
		t.Fatal(err)
	}
	if count, _, _ := controller.counts(); count != 0 {
		t.Fatal("replayed capacity refusal ran the deploy")
	}
}

func TestPreviewDeployRefusesWhenMemoryBudgetIsExhausted(t *testing.T) {
	for name, tc := range map[string]struct {
		budget   int
		admitted bool
	}{
		"over budget":    {budget: 2047, admitted: false},
		"exactly budget": {budget: 2048, admitted: true},
	} {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			d := jr.d
			d.cfg.PreviewHosting.MemoryBudgetMB = tc.budget
			insertPreviewHostRow(t, d, "preview-a", "a", journal.PreviewHostDestroying, time.Now(), v1.PreviewHostedDestroyExpired)
			command := validPreviewDeployCommand()
			trustPreviewDeployCommits(t, d, *command.PreviewDeploy)
			controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
			d.previewHostController = controller
			events := capturePreviewHostingEvents(d)
			if err := d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command); err != nil {
				t.Fatal(err)
			}
			_, deploys, _ := events.snapshot()
			count, _, _ := controller.counts()
			if tc.admitted != (count == 1) || len(deploys) != 1 ||
				(deploys[0].Outcome.Kind == v1.PreviewDeployCapacityExhausted) == tc.admitted {
				t.Fatalf("deploys=%d events=%+v", count, deploys)
			}
		})
	}
}

func TestPreviewDeploysAreSerializedAgainstCapacity(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.cfg.PreviewHosting.MaxConcurrent = 1
	d.cfg.PreviewHosting.MemoryBudgetMB = 1 << 20
	first := previewDeployCommandFor("preview-a", "a")
	second := previewDeployCommandFor("preview-b", "b")
	trustPreviewDeployCommits(t, d, *first.PreviewDeploy)
	controller := &fakePreviewHostController{
		deployOutcome: deployedPreviewOutcome(), started: make(chan struct{}, 1), release: make(chan struct{}),
	}
	d.previewHostController = controller
	events := capturePreviewHostingEvents(d)
	done := make(chan error, 1)
	go func() {
		done <- d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, first)
	}()
	<-controller.started
	if err := d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("2")}, second); err != nil {
		t.Fatal(err)
	}
	close(controller.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, deploys, _ := events.snapshot()
	kinds := map[string]v1.PreviewDeployOutcomeKind{}
	for _, event := range deploys {
		kinds[event.IdempotencyKey] = event.Outcome.Kind
	}
	if count, _, _ := controller.counts(); count != 1 ||
		kinds[first.PreviewDeploy.IdempotencyKey] != v1.PreviewDeployDeployed ||
		kinds[second.PreviewDeploy.IdempotencyKey] != v1.PreviewDeployCapacityExhausted {
		t.Fatalf("deploys=%d kinds=%v", count, kinds)
	}
}

func TestPreviewHostExpiryDestroysExpiredHostsWhilePausedAndEmitsOnce(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	pausePreviewHostingDaemon(t, d)
	controller := &fakePreviewHostController{}
	d.previewHostController = controller
	events := capturePreviewHostingEvents(d)
	expired := insertPreviewHostRow(t, d, "preview-a", "a", journal.PreviewHostDeployed, time.Now().Add(-time.Second), "")
	live := insertPreviewHostRow(t, d, "preview-b", "b", journal.PreviewHostDeployed, time.Now().Add(time.Hour), "")
	for range 2 {
		if err := d.expirePreviewHosts(context.Background(), v1.PreviewHostedDestroyExpired); err != nil {
			t.Fatal(err)
		}
	}
	destroyed, _, _ := events.snapshot()
	if len(destroyed) != 1 || destroyed[0].PreviewID != "preview-a" || destroyed[0].Reason != v1.PreviewHostedDestroyExpired ||
		destroyed[0].Outcome != v1.PreviewHostedDestroyOutcomeDestroyed || destroyed[0].PreviewKey != expired.PreviewKey ||
		destroyed[0].OrganizationID != expired.OrganizationID || destroyed[0].ProjectID != expired.ProjectID {
		t.Fatalf("destroyed=%+v", destroyed)
	}
	if ids := recoveredPreviewIDs(controller); !slices.Equal(ids, []string{"preview-a"}) {
		t.Fatalf("recovered=%v", ids)
	}
	if host := previewHostState(t, d, expired.IdempotencyKey); host.State != journal.PreviewHostDestroyed || host.DestroyReason != v1.PreviewHostedDestroyExpired {
		t.Fatalf("expired host=%+v", host)
	}
	if host := previewHostState(t, d, live.IdempotencyKey); host.State != journal.PreviewHostDeployed {
		t.Fatalf("live host=%+v", host)
	}
	if exists, _ := d.journal.HasRecord(context.Background(), v1.PreviewHostedDestroyedEventID("preview-a")); !exists {
		t.Fatal("destroyed event is not journalled under its stable ID")
	}
}

func TestPreviewHostDestroyResumesAfterRestartWithoutASecondEvent(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	controller := &fakePreviewHostController{}
	d.previewHostController = controller
	host := insertPreviewHostRow(t, d, "preview-a", "a", journal.PreviewHostDestroying, time.Now().Add(-time.Minute), v1.PreviewHostedDestroyEmergencyStop)
	earlier := v1.NewPreviewHostedDestroyedEvent(v1.PreviewHostedDestroyedSubject{
		PreviewID: host.PreviewID, OrganizationID: host.OrganizationID, ProjectID: host.ProjectID, PreviewKey: host.PreviewKey,
	}, time.Now(), v1.PreviewHostedDestroyEmergencyStop, v1.PreviewHostedDestroyOutcomeDestroyed)
	if _, err := d.sendEventWithStableID(context.Background(), v1.PreviewHostedDestroyedEventID(host.PreviewID), earlier); err != nil {
		t.Fatal(err)
	}
	events := capturePreviewHostingEvents(d)
	if err := d.reapPreviewHosts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if destroyed, _, _ := events.snapshot(); len(destroyed) != 0 {
		t.Fatalf("restart emitted a second destroyed event: %+v", destroyed)
	}
	if stored := previewHostState(t, d, host.IdempotencyKey); stored.State != journal.PreviewHostDestroyed {
		t.Fatalf("host=%+v", stored)
	}
}

func TestPreviewHostDestroyFinishesWithItsStoredReason(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.previewHostController = &fakePreviewHostController{}
	events := capturePreviewHostingEvents(d)
	insertPreviewHostRow(t, d, "preview-a", "a", journal.PreviewHostDestroying, time.Now().Add(time.Hour), v1.PreviewHostedDestroyEmergencyStop)
	if err := d.expirePreviewHosts(context.Background(), v1.PreviewHostedDestroyExpired); err != nil {
		t.Fatal(err)
	}
	if destroyed, _, _ := events.snapshot(); len(destroyed) != 1 || destroyed[0].Reason != v1.PreviewHostedDestroyEmergencyStop {
		t.Fatalf("destroyed=%+v", destroyed)
	}
}

func TestPreviewHostDestroyReportsUncertainTeardown(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.previewHostController = &fakePreviewHostController{recoverErr: errors.New("docker unavailable")}
	events := capturePreviewHostingEvents(d)
	host := insertPreviewHostRow(t, d, "preview-a", "a", journal.PreviewHostDeployed, time.Now().Add(-time.Second), "")
	if err := d.expirePreviewHosts(context.Background(), v1.PreviewHostedDestroyExpired); err != nil {
		t.Fatal(err)
	}
	if destroyed, _, _ := events.snapshot(); len(destroyed) != 1 || destroyed[0].Outcome != v1.PreviewHostedDestroyOutcomeUncertain {
		t.Fatalf("destroyed=%+v", destroyed)
	}
	if stored := previewHostState(t, d, host.IdempotencyKey); stored.State != journal.PreviewHostUncertain {
		t.Fatalf("host=%+v", stored)
	}
}

func TestPreviewHostDestroyInterruptedByShutdownStaysPending(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.previewHostController = &fakePreviewHostController{recoverErr: context.Canceled}
	events := capturePreviewHostingEvents(d)
	host := insertPreviewHostRow(t, d, "preview-a", "a", journal.PreviewHostDestroying, time.Now(), v1.PreviewHostedDestroyExpired)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.reapPreviewHosts(ctx); err == nil {
		t.Fatal("interrupted teardown reported success")
	}
	if destroyed, _, _ := events.snapshot(); len(destroyed) != 0 {
		t.Fatalf("interrupted teardown emitted %+v", destroyed)
	}
	if stored := previewHostState(t, d, host.IdempotencyKey); stored.State != journal.PreviewHostDestroying {
		t.Fatalf("host=%+v", stored)
	}
}

func TestEmergencyStopDestroysEveryLivePreview(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.cfg.PreviewHosting.MemoryBudgetMB = 1 << 20
	allowPreviewHostingPause(t, d)
	controller := cancellablePreviewHostController{
		fakePreviewHostController: &fakePreviewHostController{started: make(chan struct{}, 1)},
		cancelled:                 make(chan struct{}),
	}
	d.previewHostController = controller
	events := capturePreviewHostingEvents(d)
	insertPreviewHostRow(t, d, "preview-a", "a", journal.PreviewHostDeployed, time.Now().Add(time.Hour), "")
	insertPreviewHostRow(t, d, "preview-b", "b", journal.PreviewHostDeployed, time.Now().Add(time.Hour), "")
	deploying := previewDeployCommandFor("preview-c", "c")
	trustPreviewDeployCommits(t, d, *deploying.PreviewDeploy)
	done := make(chan error, 1)
	go func() {
		done <- d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, deploying)
	}()
	<-controller.started
	if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdEmergencyStop}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-controller.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("emergency stop did not cancel the in-flight deploy")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !d.Paused() {
		t.Fatal("emergency stop did not pause")
	}
	if err := d.reapPreviewHosts(context.Background()); err != nil {
		t.Fatal(err)
	}
	destroyed, deploys, _ := events.snapshot()
	ids := []string{}
	for _, event := range destroyed {
		if event.Reason != v1.PreviewHostedDestroyEmergencyStop {
			t.Fatalf("reason=%s", event.Reason)
		}
		ids = append(ids, event.PreviewID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"preview-a", "preview-b"}) || len(deploys) != 1 || deploys[0].Outcome.Kind != v1.PreviewDeployFailed {
		t.Fatalf("destroyed=%v deploys=%+v", ids, deploys)
	}
	live, err := d.journal.LivePreviewHosts(context.Background())
	if err != nil || len(live) != 0 {
		t.Fatalf("live after emergency stop=%+v err=%v", live, err)
	}
}

func TestDeployThatFinishesAfterEmergencyStopIsDestroyed(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.cfg.PreviewHosting.MemoryBudgetMB = 1 << 20
	allowPreviewHostingPause(t, d)
	controller := &fakePreviewHostController{
		deployOutcome: deployedPreviewOutcome(), started: make(chan struct{}, 1), release: make(chan struct{}),
	}
	d.previewHostController = controller
	events := capturePreviewHostingEvents(d)
	command := validPreviewDeployCommand()
	trustPreviewDeployCommits(t, d, *command.PreviewDeploy)
	done := make(chan error, 1)
	go func() {
		done <- d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command)
	}()
	<-controller.started
	if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdEmergencyStop}); err != nil {
		t.Fatal(err)
	}
	close(controller.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	host := previewHostState(t, d, command.PreviewDeploy.IdempotencyKey)
	if host.State != journal.PreviewHostDestroying || host.DestroyReason != v1.PreviewHostedDestroyEmergencyStop {
		t.Fatalf("host=%+v", host)
	}
	if err := d.reapPreviewHosts(context.Background()); err != nil {
		t.Fatal(err)
	}
	destroyed, _, _ := events.snapshot()
	if len(destroyed) != 1 || destroyed[0].PreviewID != command.PreviewDeploy.PreviewID || destroyed[0].Reason != v1.PreviewHostedDestroyEmergencyStop {
		t.Fatalf("destroyed=%+v", destroyed)
	}
}

func TestHostedPreviewDestroyRunsWhilePausedButExtendIsRefused(t *testing.T) {
	deploy := *validPreviewDeployCommand().PreviewDeploy
	jr, _ := testJobRunner(t)
	d := jr.d
	pausePreviewHostingDaemon(t, d)
	controller := &fakePreviewHostController{lifecycleOutcome: v1.PreviewLifecycleResultOutcome{
		Kind: v1.PreviewLifecycleSucceeded,
		ResourceState: &v1.PreviewLifecycleResourceState{
			Kind: v1.PreviewLifecycleResourceDestroyed, Disposition: v1.PreviewLifecycleDestroyed,
		},
	}}
	d.previewHostController = controller
	insertTestPreviewHost(t, d, deploy, journal.PreviewHostDeployed, []byte(`{}`))
	events := capturePreviewHostingEvents(d)
	extend := validHostedPreviewLifecycleCommand(v1.PreviewLifecycleExtend, deploy)
	err := d.executeCommand(context.Background(), v1.Envelope{ID: hostedLifecycleCommandID("5")}, extend)
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonRunnerPaused {
		t.Fatalf("paused extend reason=%q err=%v", reason, err)
	}
	external := validPreviewLifecycleCommand(v1.PreviewLifecycleDestroy)
	if reason := commandRejectionReason(d.executeCommand(context.Background(), v1.Envelope{ID: previewLifecycleCommandID()}, external)); reason != v1.CommandAckReasonRunnerPaused {
		t.Fatalf("paused external lifecycle reason=%q", reason)
	}
	destroy := validHostedPreviewLifecycleCommand(v1.PreviewLifecycleDestroy, deploy)
	if err := d.executeCommand(context.Background(), v1.Envelope{ID: hostedLifecycleCommandID("6")}, destroy); err != nil {
		t.Fatal(err)
	}
	_, _, lifecycles := events.snapshot()
	if _, count, _ := controller.counts(); count != 1 || len(lifecycles) != 1 || lifecycles[0].Outcome.Kind != v1.PreviewLifecycleSucceeded {
		t.Fatalf("lifecycles=%d events=%+v", count, lifecycles)
	}
	if host := previewHostState(t, d, deploy.IdempotencyKey); host.State != journal.PreviewHostDestroyed {
		t.Fatalf("host=%+v", host)
	}
}

func TestStartupReconcileExpiresStaleHostsAndSweepsOrphans(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	fake := &fakePreviewHostController{}
	d.previewHostController = listingPreviewHostController{
		fakePreviewHostController: fake,
		ids:                       []string{"preview-expired", "preview-live", "preview-deploying", "preview-orphan", "preview-failed"},
	}
	events := capturePreviewHostingEvents(d)
	insertPreviewHostRow(t, d, "preview-expired", "a", journal.PreviewHostDeployed, time.Now().Add(-time.Minute), "")
	insertPreviewHostRow(t, d, "preview-live", "b", journal.PreviewHostDeployed, time.Now().Add(time.Hour), "")
	insertPreviewHostRow(t, d, "preview-deploying", "c", journal.PreviewHostDeploying, time.Now().Add(-time.Minute), "")
	insertPreviewHostRow(t, d, "preview-failed", "e", journal.PreviewHostFailed, time.Now().Add(time.Hour), "")
	if err := d.reconcilePreviewHostsAtStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	destroyed, _, _ := events.snapshot()
	if len(destroyed) != 1 || destroyed[0].PreviewID != "preview-expired" || destroyed[0].Reason != v1.PreviewHostedDestroyReconcileExpired {
		t.Fatalf("destroyed=%+v", destroyed)
	}
	if ids := recoveredPreviewIDs(fake); !slices.Equal(ids, []string{"preview-expired", "preview-failed", "preview-orphan"}) {
		t.Fatalf("recovered=%v", ids)
	}
	for _, id := range []string{"preview-orphan", "preview-failed"} {
		if exists, _ := d.journal.HasRecord(context.Background(), v1.PreviewHostedDestroyedEventID(id)); exists {
			t.Fatalf("orphan sweep emitted an event for %s", id)
		}
	}
}

func TestPreviewHostExpiryLoopReconcilesAndWakesOnEmergencyStopWhilePaused(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.previewHostWake = make(chan struct{}, 1)
	fake := &fakePreviewHostController{}
	d.previewHostController = fake
	events := capturePreviewHostingEvents(d)
	pausePreviewHostingDaemon(t, d)
	insertPreviewHostRow(t, d, "preview-a", "a", journal.PreviewHostDeployed, time.Now().Add(-time.Minute), "")
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		d.previewHostExpiryLoop(ctx)
		close(stopped)
	}()
	defer func() {
		cancel()
		<-stopped
	}()
	waitForDestroyed := func(count int) []v1.PreviewHostedDestroyedEvent {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if destroyed, _, _ := events.snapshot(); len(destroyed) >= count {
				return destroyed
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %d destroyed events", count)
		return nil
	}
	if destroyed := waitForDestroyed(1); destroyed[0].Reason != v1.PreviewHostedDestroyReconcileExpired {
		t.Fatalf("startup reason=%s", destroyed[0].Reason)
	}
	insertPreviewHostRow(t, d, "preview-b", "b", journal.PreviewHostDeployed, time.Now().Add(time.Hour), "")
	if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdEmergencyStop}); err != nil {
		t.Fatal(err)
	}
	if destroyed := waitForDestroyed(2); destroyed[1].PreviewID != "preview-b" || destroyed[1].Reason != v1.PreviewHostedDestroyEmergencyStop {
		t.Fatalf("emergency destroyed=%+v", destroyed)
	}
}
