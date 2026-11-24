package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
)

func releaseOnCleanup(t *testing.T, ch chan struct{}) func() {
	t.Helper()
	var once sync.Once
	release := func() { once.Do(func() { close(ch) }) }
	t.Cleanup(release)
	return release
}

func previewHostRecoveryClaims(d *Daemon) int {
	d.previewHostMu.Lock()
	defer d.previewHostMu.Unlock()
	return len(d.previewHostRecovering)
}

func waitForSweepTeardown(t *testing.T, controller blockingSweepController, done <-chan error) {
	t.Helper()
	select {
	case <-controller.entered:
	case err := <-done:
		t.Fatalf("finished before reaching the teardown: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("never reached the teardown")
	}
}

func setRecoverErr(controller *fakePreviewHostController, err error) {
	controller.mu.Lock()
	controller.recoverErr = err
	controller.mu.Unlock()
}

func TestHostedLifecycleDestroyIsRetryableWhileAnUncertainProofRuns(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	fake := &fakePreviewHostController{}
	controller := blockingSweepController{
		fakePreviewHostController: fake, entered: make(chan struct{}), released: make(chan struct{}),
	}
	d.previewHostController = controller
	release := releaseOnCleanup(t, controller.released)
	events := capturePreviewHostingEvents(d)
	deploy := *validPreviewDeployCommand().PreviewDeploy
	insertHostedPreviewHost(t, d, deploy, func(host *journal.PreviewHost) {
		host.State = journal.PreviewHostUncertain
	})
	done := make(chan error, 1)
	go func() { done <- d.proveUncertainPreviewDeploys(context.Background()) }()
	waitForSweepTeardown(t, controller, done)
	command := validHostedPreviewLifecycleCommand(v1.PreviewLifecycleDestroy, deploy)
	if err := d.executeCommand(context.Background(), v1.Envelope{ID: hostedLifecycleCommandID("3")}, command); err != nil {
		t.Fatal(err)
	}
	if _, lifecycles, _ := fake.counts(); lifecycles != 0 {
		t.Fatalf("lifecycle teardown ran beside the proof: %d", lifecycles)
	}
	_, _, lifecycles := events.snapshot()
	if len(lifecycles) != 1 || lifecycles[0].Outcome.Kind != v1.PreviewLifecycleRetryable ||
		lifecycles[0].Outcome.Code != v1.PreviewLifecycleTransientResourceConflict {
		t.Fatalf("lifecycles=%+v", lifecycles)
	}
	if _, found, err := d.journal.PreviewHostedLifecycleResultByKey(context.Background(), command.PreviewLifecycle.IdempotencyKey); err != nil || found {
		t.Fatalf("retryable result was journalled: found=%v err=%v", found, err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if stored := previewHostState(t, d, deploy.IdempotencyKey); stored.State != journal.PreviewHostDestroyed {
		t.Fatalf("host=%+v", stored)
	}
	if err := d.executeCommand(context.Background(), v1.Envelope{ID: hostedLifecycleCommandID("4")}, command); err != nil {
		t.Fatal(err)
	}
	_, _, lifecycles = events.snapshot()
	if len(lifecycles) != 2 || lifecycles[1].Outcome.Kind != v1.PreviewLifecycleSucceeded ||
		lifecycles[1].Outcome.ResourceState == nil ||
		lifecycles[1].Outcome.ResourceState.Disposition != v1.PreviewLifecycleAlreadyAbsent {
		t.Fatalf("lifecycles=%+v", lifecycles)
	}
}

func TestEmergencyStopDuringAnUnprovenUncertainProofSchedulesEmergencyTeardown(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	allowPreviewHostingPause(t, d)
	fake := &fakePreviewHostController{recoverErr: errors.New("docker unavailable")}
	controller := blockingSweepController{
		fakePreviewHostController: fake, entered: make(chan struct{}), released: make(chan struct{}),
	}
	d.previewHostController = controller
	release := releaseOnCleanup(t, controller.released)
	events := capturePreviewHostingEvents(d)
	deploy := *validPreviewDeployCommand().PreviewDeploy
	insertHostedPreviewHost(t, d, deploy, func(host *journal.PreviewHost) {
		host.State = journal.PreviewHostUncertain
	})
	done := make(chan error, 1)
	go func() { done <- d.proveUncertainPreviewDeploys(context.Background()) }()
	waitForSweepTeardown(t, controller, done)
	if err := d.emergencyStopPreviewHosts(context.Background()); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if recovering := previewHostRecoveryClaims(d); recovering != 0 {
		t.Fatalf("recovery claims left behind: %d", recovering)
	}
	if stored := previewHostState(t, d, deploy.IdempotencyKey); stored.State != journal.PreviewHostDestroying ||
		stored.DestroyReason != v1.PreviewHostedDestroyEmergencyStop {
		t.Fatalf("host=%+v", stored)
	}
	d.previewHostController = fake
	setRecoverErr(fake, nil)
	if err := d.reapPreviewHosts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stored := previewHostState(t, d, deploy.IdempotencyKey); stored.State != journal.PreviewHostDestroyed {
		t.Fatalf("host=%+v", stored)
	}
	destroyed, _, _ := events.snapshot()
	if len(destroyed) != 1 || destroyed[0].Reason != v1.PreviewHostedDestroyEmergencyStop ||
		destroyed[0].Outcome != v1.PreviewHostedDestroyOutcomeDestroyed {
		t.Fatalf("destroyed=%+v", destroyed)
	}
}

func TestEmergencyStopDuringAnUnprovenInterruptedRecoverySchedulesEmergencyTeardown(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	allowPreviewHostingPause(t, d)
	fake := &fakePreviewHostController{recoverErr: errors.New("docker unavailable")}
	controller := blockingSweepController{
		fakePreviewHostController: fake, entered: make(chan struct{}), released: make(chan struct{}),
	}
	d.previewHostController = controller
	release := releaseOnCleanup(t, controller.released)
	events := capturePreviewHostingEvents(d)
	command := validPreviewDeployCommand()
	insertHostedPreviewHost(t, d, *command.PreviewDeploy, func(host *journal.PreviewHost) {
		host.State = journal.PreviewHostDeploying
		host.Result = nil
	})
	done := make(chan error, 1)
	go func() {
		done <- d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command)
	}()
	waitForSweepTeardown(t, controller, done)
	if err := d.emergencyStopPreviewHosts(context.Background()); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if recovering := previewHostRecoveryClaims(d); recovering != 0 {
		t.Fatalf("recovery claims left behind: %d", recovering)
	}
	key := command.PreviewDeploy.IdempotencyKey
	if stored := previewHostState(t, d, key); stored.State != journal.PreviewHostDestroying ||
		stored.DestroyReason != v1.PreviewHostedDestroyEmergencyStop || len(stored.Result) == 0 {
		t.Fatalf("host=%+v", stored)
	}
	d.previewHostController = fake
	setRecoverErr(fake, nil)
	if err := d.reapPreviewHosts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stored := previewHostState(t, d, key); stored.State != journal.PreviewHostDestroyed {
		t.Fatalf("host=%+v", stored)
	}
	destroyed, deploys, _ := events.snapshot()
	if len(deploys) != 1 || deploys[0].Outcome.Kind != v1.PreviewDeployUncertain {
		t.Fatalf("deploys=%+v", deploys)
	}
	if len(destroyed) != 1 || destroyed[0].Reason != v1.PreviewHostedDestroyEmergencyStop ||
		destroyed[0].IdempotencyKey != key {
		t.Fatalf("destroyed=%+v", destroyed)
	}
}

func TestPreviewHostLeaseMaxOnlyReapsDeployingRows(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	fake := &fakePreviewHostController{}
	d.previewHostController = fake
	capturePreviewHostingEvents(d)
	host := insertPreviewHostRow(t, d, "preview-a", "a", journal.PreviewHostDeployed, time.Now().Add(3*time.Hour), "")
	d.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if err := d.expirePreviewHosts(context.Background(), v1.PreviewHostedDestroyExpired); err != nil {
		t.Fatal(err)
	}
	if stored := previewHostState(t, d, host.IdempotencyKey); stored.State != journal.PreviewHostDeployed {
		t.Fatalf("an extended deployed preview older than the lease max was reaped: %+v", stored)
	}
	if _, _, recovered := fake.counts(); recovered != 0 {
		t.Fatalf("teardown attempts=%d", recovered)
	}
}
