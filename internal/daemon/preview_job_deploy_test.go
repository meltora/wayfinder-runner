package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/lease"
)

type fakePreviewJobDeployTimer struct {
	at    time.Time
	ch    chan time.Time
	fired bool
}

type fakePreviewJobDeployClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakePreviewJobDeployTimer
	created chan struct{}
}

func newFakePreviewJobDeployClock(now time.Time) *fakePreviewJobDeployClock {
	return &fakePreviewJobDeployClock{now: now, created: make(chan struct{}, 64)}
}

func (clock *fakePreviewJobDeployClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakePreviewJobDeployClock) After(duration time.Duration) <-chan time.Time {
	clock.mu.Lock()
	timer := &fakePreviewJobDeployTimer{at: clock.now.Add(duration), ch: make(chan time.Time, 1)}
	clock.timers = append(clock.timers, timer)
	clock.mu.Unlock()
	clock.created <- struct{}{}
	return timer.ch
}

func (clock *fakePreviewJobDeployClock) waitTimers(t *testing.T, count int) {
	t.Helper()
	for range count {
		select {
		case <-clock.created:
		case <-time.After(5 * time.Second):
			t.Fatalf("await did not arm %d timers", count)
		}
	}
}

func (clock *fakePreviewJobDeployClock) advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
	for _, timer := range clock.timers {
		if !timer.fired && !timer.at.After(clock.now) {
			timer.fired = true
			timer.ch <- clock.now
		}
	}
}

func (clock *fakePreviewJobDeployClock) fire(index int) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	timer := clock.timers[index]
	if !timer.fired {
		timer.fired = true
		timer.ch <- clock.now
	}
}

func testPreviewJobDeployWaiter(jr *jobRunner, sequence uint64) (*previewJobDeployWaiter, v1.CommandPayload) {
	command := validPreviewDeployCommand()
	requestID := v1.PreviewDeployRequestID(jr.job.JobID, jr.job.LeaseID, sequence)
	command.PreviewDeploy.IdempotencyKey = v1.PreviewJobDeployIdempotencyKey(requestID)
	command.PreviewDeploy.EdgeHost = command.PreviewDeploy.PreviewKey + ".previews.example.test"
	sealTestPreviewDeploy(command.PreviewDeploy)
	commitSet := map[string]string{}
	for _, commit := range command.PreviewDeploy.CommitSet {
		commitSet[commit.RepositoryID] = commit.CommitSHA
	}
	w := newPreviewJobDeployWaiter(jr.job.AttemptID, jr.job.ManifestID, requestID, sequence,
		commitSet, command.PreviewDeploy.CommitSetDigest)
	return w, command
}

func registerTestPreviewJobDeploy(t *testing.T, jr *jobRunner, sequence uint64) (*previewJobDeployWaiter, v1.CommandPayload) {
	t.Helper()
	w, command := testPreviewJobDeployWaiter(jr, sequence)
	if err := jr.d.previewJobDeploys.register(w); err != nil {
		t.Fatal(err)
	}
	return w, command
}

func startTestPreviewJobDeployAwait(
	t *testing.T,
	jr *jobRunner,
	ctx context.Context,
	w *previewJobDeployWaiter,
	clock *fakePreviewJobDeployClock,
) <-chan previewJobDeployOutcome {
	t.Helper()
	result := make(chan previewJobDeployOutcome, 1)
	sentAt := clock.Now()
	go func() { result <- jr.awaitPreviewJobDeploy(ctx, w, sentAt, clock.Now, clock.After) }()
	clock.waitTimers(t, 3)
	return result
}

func receivePreviewJobDeployOutcome(t *testing.T, result <-chan previewJobDeployOutcome) previewJobDeployOutcome {
	t.Helper()
	select {
	case outcome := <-result:
		return outcome
	case <-time.After(5 * time.Second):
		t.Fatal("await did not return")
	}
	return previewJobDeployOutcome{}
}

func assertPreviewJobDeployOpen(t *testing.T, w *previewJobDeployWaiter) {
	t.Helper()
	if w.hasArrived() {
		t.Fatal("waiter arrived")
	}
	select {
	case <-w.done:
		t.Fatalf("waiter resolved: %+v", w.result)
	default:
	}
}

func assertPreviewJobDeployRegistryEmpty(t *testing.T, d *Daemon) {
	t.Helper()
	d.previewJobDeploys.mu.Lock()
	defer d.previewJobDeploys.mu.Unlock()
	if len(d.previewJobDeploys.byKey) != 0 || len(d.previewJobDeploys.byEvent) != 0 {
		t.Fatalf("registry keys=%d events=%d", len(d.previewJobDeploys.byKey), len(d.previewJobDeploys.byEvent))
	}
}

func assertPreviewJobDeployOutcome(t *testing.T, outcome previewJobDeployOutcome, status, code string) {
	t.Helper()
	if outcome.Status != status || outcome.Code != code {
		t.Fatalf("outcome=%+v want %s/%s", outcome, status, code)
	}
}

func TestPreviewJobDeployResolvesOnDeployedResult(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, command := registerTestPreviewJobDeploy(t, jr, 1)
	trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
	jr.d.previewHostController = &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
	capturePreviewDeployEvents(t, jr.d)
	clock := newFakePreviewJobDeployClock(time.Now())
	result := startTestPreviewJobDeployAwait(t, jr, jr.ctx, w, clock)
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command); err != nil {
		t.Fatal(err)
	}
	outcome := receivePreviewJobDeployOutcome(t, result)
	assertPreviewJobDeployOutcome(t, outcome, v1.PreviewJobDeployStatusDeployed, "")
	if outcome.EdgeHost != command.PreviewDeploy.EdgeHost || !outcome.ExpiresAt.Equal(command.PreviewDeploy.ExpiresAt) ||
		outcome.CommitSetDigest != command.PreviewDeploy.CommitSetDigest || outcome.Sequence != 1 ||
		outcome.CommitSet["api"] != command.PreviewDeploy.CommitSet[0].CommitSHA || len(outcome.CommitSet) != 2 {
		t.Fatalf("outcome=%+v", outcome)
	}
	assertPreviewJobDeployRegistryEmpty(t, jr.d)
}

func TestPreviewJobDeployResolvesOnResentResult(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, command := testPreviewJobDeployWaiter(jr, 1)
	trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
	controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
	jr.d.previewHostController = controller
	events := capturePreviewDeployEvents(t, jr.d)
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command); err != nil {
		t.Fatal(err)
	}
	if err := jr.d.previewJobDeploys.register(w); err != nil {
		t.Fatal(err)
	}
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("2")}, command); err != nil {
		t.Fatal(err)
	}
	if deploys, _, _ := controller.counts(); deploys != 1 || len(*events) != 1 {
		t.Fatalf("deploys=%d events=%d", deploys, len(*events))
	}
	clock := newFakePreviewJobDeployClock(time.Now())
	outcome := receivePreviewJobDeployOutcome(t, startTestPreviewJobDeployAwait(t, jr, jr.ctx, w, clock))
	assertPreviewJobDeployOutcome(t, outcome, v1.PreviewJobDeployStatusDeployed, "")
	if outcome.EdgeHost != command.PreviewDeploy.EdgeHost {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestPreviewJobDeployIgnoresCommandWithMismatchedDigest(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, command := registerTestPreviewJobDeploy(t, jr, 1)
	command.PreviewDeploy.CommitSet[1].CommitSHA = "3333333333333333333333333333333333333333"
	sealTestPreviewDeploy(command.PreviewDeploy)
	trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
	controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
	jr.d.previewHostController = controller
	events := capturePreviewDeployEvents(t, jr.d)
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command); err != nil {
		t.Fatal(err)
	}
	if deploys, _, _ := controller.counts(); deploys != 1 || len(*events) != 1 {
		t.Fatalf("mismatched command did not run through P5d deploys=%d events=%d", deploys, len(*events))
	}
	assertPreviewJobDeployOpen(t, w)
	clock := newFakePreviewJobDeployClock(time.Now())
	result := startTestPreviewJobDeployAwait(t, jr, jr.ctx, w, clock)
	clock.advance(v1.PreviewJobDeployCommandTimeout)
	assertPreviewJobDeployOutcome(t, receivePreviewJobDeployOutcome(t, result),
		v1.PreviewJobDeployStatusTimeout, previewJobDeployCodeCommandTimeout)
}

func TestPreviewJobDeployIgnoresCommandWithOtherManifest(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, command := registerTestPreviewJobDeploy(t, jr, 1)
	command.PreviewDeploy.ExecutionManifestID = "manifest-2"
	sealTestPreviewDeploy(command.PreviewDeploy)
	trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
	controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
	jr.d.previewHostController = controller
	capturePreviewDeployEvents(t, jr.d)
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command); err != nil {
		t.Fatal(err)
	}
	if deploys, _, _ := controller.counts(); deploys != 1 {
		t.Fatalf("deploys=%d", deploys)
	}
	assertPreviewJobDeployOpen(t, w)
}

func TestPreviewJobDeployTimesOutWithoutCommand(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, _ := registerTestPreviewJobDeploy(t, jr, 1)
	clock := newFakePreviewJobDeployClock(time.Now())
	result := startTestPreviewJobDeployAwait(t, jr, jr.ctx, w, clock)
	clock.advance(v1.PreviewJobDeployCommandTimeout - time.Millisecond)
	assertPreviewJobDeployOpen(t, w)
	clock.advance(time.Millisecond)
	outcome := receivePreviewJobDeployOutcome(t, result)
	assertPreviewJobDeployOutcome(t, outcome, v1.PreviewJobDeployStatusTimeout, previewJobDeployCodeCommandTimeout)
	if outcome.Sequence != 1 || outcome.CommitSetDigest != w.digest {
		t.Fatalf("outcome=%+v", outcome)
	}
	assertPreviewJobDeployRegistryEmpty(t, jr.d)
}

func TestPreviewJobDeployArrivalDisarmsCommandTimer(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, command := registerTestPreviewJobDeploy(t, jr, 1)
	trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
	controller := &fakePreviewHostController{
		deployOutcome: deployedPreviewOutcome(), started: make(chan struct{}, 1), release: make(chan struct{}),
	}
	jr.d.previewHostController = controller
	capturePreviewDeployEvents(t, jr.d)
	clock := newFakePreviewJobDeployClock(time.Now())
	result := startTestPreviewJobDeployAwait(t, jr, jr.ctx, w, clock)
	clock.advance(v1.PreviewJobDeployCommandTimeout - time.Second)
	executed := make(chan error, 1)
	go func() {
		executed <- jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command)
	}()
	select {
	case <-controller.started:
	case <-time.After(5 * time.Second):
		t.Fatal("deploy did not start")
	}
	clock.advance(10 * time.Minute)
	select {
	case outcome := <-result:
		t.Fatalf("await ended during deploy: %+v", outcome)
	default:
	}
	close(controller.release)
	if err := <-executed; err != nil {
		t.Fatal(err)
	}
	assertPreviewJobDeployOutcome(t, receivePreviewJobDeployOutcome(t, result), v1.PreviewJobDeployStatusDeployed, "")
}

func TestPreviewJobDeployEndsOnJobCancel(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, _ := registerTestPreviewJobDeploy(t, jr, 1)
	clock := newFakePreviewJobDeployClock(time.Now())
	result := startTestPreviewJobDeployAwait(t, jr, jr.ctx, w, clock)
	jr.cancel()
	assertPreviewJobDeployOutcome(t, receivePreviewJobDeployOutcome(t, result),
		v1.PreviewJobDeployStatusFailed, previewJobDeployCodeJobCancelled)
	assertPreviewJobDeployRegistryEmpty(t, jr.d)
}

func TestPreviewJobDeployEndsOnJobRelease(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, _ := registerTestPreviewJobDeploy(t, jr, 1)
	clock := newFakePreviewJobDeployClock(time.Now())
	result := startTestPreviewJobDeployAwait(t, jr, context.Background(), w, clock)
	jr.d.previewJobDeploys.releaseAttempt(jr.job.AttemptID)
	assertPreviewJobDeployOutcome(t, receivePreviewJobDeployOutcome(t, result),
		v1.PreviewJobDeployStatusFailed, previewJobDeployCodeJobCancelled)
	assertPreviewJobDeployRegistryEmpty(t, jr.d)
}

func TestPreviewJobDeployEndsAtLeaseExpiry(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, _ := registerTestPreviewJobDeploy(t, jr, 1)
	clock := newFakePreviewJobDeployClock(jr.d.leases.Current().ExpiresAt.Add(-time.Minute))
	result := startTestPreviewJobDeployAwait(t, jr, jr.ctx, w, clock)
	clock.advance(time.Minute)
	assertPreviewJobDeployOutcome(t, receivePreviewJobDeployOutcome(t, result),
		v1.PreviewJobDeployStatusFailed, previewJobDeployCodeLeaseExpired)
	assertPreviewJobDeployRegistryEmpty(t, jr.d)
}

func TestPreviewJobDeployRearmsAfterLeaseRenewal(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, _ := registerTestPreviewJobDeploy(t, jr, 1)
	current := jr.d.leases.Current()
	clock := newFakePreviewJobDeployClock(current.ExpiresAt.Add(-time.Minute))
	result := startTestPreviewJobDeployAwait(t, jr, jr.ctx, w, clock)
	if err := jr.d.leases.RenewUntil(current.AttemptID, current.FencingToken, current.ExpiresAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	clock.waitTimers(t, 1)
	clock.advance(time.Minute)
	assertPreviewJobDeployOutcome(t, receivePreviewJobDeployOutcome(t, result),
		v1.PreviewJobDeployStatusTimeout, previewJobDeployCodeCommandTimeout)
}

func TestPreviewJobDeployRejectedEventFinalCode(t *testing.T) {
	for code, want := range map[string]string{
		"deploy_limit_reached": "deploy_limit_reached",
		"lease_expired":        "lease_expired",
		"weird":                v1.PreviewJobDeployRefusalRejected,
	} {
		t.Run(code, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			w, _ := registerTestPreviewJobDeploy(t, jr, 1)
			if _, err := jr.d.sendEventWithStableID(context.Background(), w.eventID, map[string]string{"kind": "test"}); err != nil {
				t.Fatal(err)
			}
			if err := jr.d.OnEventRejected(context.Background(), w.eventID, code); err != nil {
				t.Fatal(err)
			}
			clock := newFakePreviewJobDeployClock(time.Now())
			assertPreviewJobDeployOutcome(t, receivePreviewJobDeployOutcome(t, startTestPreviewJobDeployAwait(t, jr, jr.ctx, w, clock)),
				v1.PreviewJobDeployStatusRefused, want)
			assertPreviewJobDeployRegistryEmpty(t, jr.d)
		})
	}
}

func TestPreviewJobDeployIgnoresTimestampOutOfWindow(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, _ := registerTestPreviewJobDeploy(t, jr, 1)
	if _, err := jr.d.sendEventWithStableID(context.Background(), w.eventID, map[string]string{"kind": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := jr.d.OnEventRejected(context.Background(), w.eventID, v1.PreviewJobDeployRefusalTimestampOutOfWindow); err != nil {
		t.Fatal(err)
	}
	if jr.d.previewJobDeployEventRejected(w.eventID, v1.PreviewJobDeployRefusalTimestampOutOfWindow) {
		t.Fatal("retryable rejection resolved the waiter")
	}
	assertPreviewJobDeployOpen(t, w)
	if jr.d.previewJobDeploys.forEvent(w.eventID) != w {
		t.Fatal("waiter is no longer registered")
	}
}

func TestPreviewJobDeployCommandRejectionFails(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, command := registerTestPreviewJobDeploy(t, jr, 1)
	trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
	jr.d.previewHostController = &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
	capturePreviewDeployEvents(t, jr.d)
	jr.d.paused.Store(true)
	err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command)
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonRunnerPaused {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
	clock := newFakePreviewJobDeployClock(time.Now())
	assertPreviewJobDeployOutcome(t, receivePreviewJobDeployOutcome(t, startTestPreviewJobDeployAwait(t, jr, jr.ctx, w, clock)),
		v1.PreviewJobDeployStatusFailed, string(v1.CommandAckReasonRunnerPaused))
}

func TestPreviewJobDeployRegistryOneInFlightPerAttempt(t *testing.T) {
	jr, _ := testJobRunner(t)
	first, _ := testPreviewJobDeployWaiter(jr, 1)
	second, _ := testPreviewJobDeployWaiter(jr, 2)
	duplicate, _ := testPreviewJobDeployWaiter(jr, 1)
	other := newPreviewJobDeployWaiter("a2", "manifest-2", v1.PreviewDeployRequestID("j2", "lease-2", 1), 1,
		map[string]string{"api": "1111111111111111111111111111111111111111"}, first.digest)
	registry := &jr.d.previewJobDeploys
	if err := registry.register(first); err != nil {
		t.Fatal(err)
	}
	if err := registry.register(second); !errors.Is(err, errPreviewJobDeployInFlight) {
		t.Fatalf("second err=%v", err)
	}
	if err := registry.register(duplicate); !errors.Is(err, errPreviewJobDeployInFlight) {
		t.Fatalf("duplicate err=%v", err)
	}
	if err := registry.register(other); err != nil {
		t.Fatal(err)
	}
	registry.release(first.key)
	if err := registry.register(second); err != nil {
		t.Fatal(err)
	}
	if registry.forKey(first.key) != nil || registry.forEvent(first.eventID) != nil || registry.forKey(second.key) != second {
		t.Fatal("registry index is stale")
	}
}

func TestPreviewJobDeployMapsResultOutcomes(t *testing.T) {
	tests := map[string]struct {
		outcome v1.PreviewDeployOutcome
		code    string
	}{
		"failed with phase":  {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployFailed, Phase: v1.PreviewDeployPhaseBuild}, "build"},
		"failed empty phase": {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployFailed}, "failed"},
		"capacity exhausted": {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployCapacityExhausted}, "capacity_exhausted"},
		"uncertain":          {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployUncertain}, "uncertain"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			w, command := registerTestPreviewJobDeploy(t, jr, 1)
			jr.d.previewJobDeployResultSent(previewDeployResultEvent(*command.PreviewDeploy, previewDeployCommandID("1"),
				time.Now().UTC().Truncate(time.Millisecond), test.outcome))
			outcome, ok := w.resolved()
			if !ok {
				t.Fatal("waiter did not resolve")
			}
			assertPreviewJobDeployOutcome(t, outcome, v1.PreviewJobDeployStatusFailed, test.code)
			if outcome.EdgeHost != "" || !outcome.ExpiresAt.IsZero() {
				t.Fatalf("failed outcome carries deploy fields: %+v", outcome)
			}
		})
	}
}

func TestPreviewJobDeployFailsOnUntrustedCommitSet(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, command := registerTestPreviewJobDeploy(t, jr, 1)
	controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
	jr.d.previewHostController = controller
	events := capturePreviewDeployEvents(t, jr.d)
	clock := newFakePreviewJobDeployClock(time.Now())
	result := startTestPreviewJobDeployAwait(t, jr, jr.ctx, w, clock)
	if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command); err != nil {
		t.Fatal(err)
	}
	assertPreviewJobDeployOutcome(t, receivePreviewJobDeployOutcome(t, result),
		v1.PreviewJobDeployStatusFailed, string(v1.PreviewDeployPhaseCommitSet))
	if deploys, _, _ := controller.counts(); deploys != 0 || len(*events) != 1 {
		t.Fatalf("deploys=%d events=%d", deploys, len(*events))
	}
	assertPreviewJobDeployRegistryEmpty(t, jr.d)
}

func TestPreviewJobDeployCapBeforeArrivalIsCommandTimeout(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, _ := registerTestPreviewJobDeploy(t, jr, 1)
	clock := newFakePreviewJobDeployClock(time.Now())
	result := startTestPreviewJobDeployAwait(t, jr, jr.ctx, w, clock)
	clock.fire(1)
	assertPreviewJobDeployOutcome(t, receivePreviewJobDeployOutcome(t, result),
		v1.PreviewJobDeployStatusTimeout, previewJobDeployCodeCommandTimeout)
	assertPreviewJobDeployRegistryEmpty(t, jr.d)
}

func TestPreviewJobDeployCapAfterArrivalIsDeployTimeout(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, command := registerTestPreviewJobDeploy(t, jr, 1)
	trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
	controller := &fakePreviewHostController{
		deployOutcome: deployedPreviewOutcome(), started: make(chan struct{}, 1), release: make(chan struct{}),
	}
	jr.d.previewHostController = controller
	capturePreviewDeployEvents(t, jr.d)
	clock := newFakePreviewJobDeployClock(time.Now())
	result := startTestPreviewJobDeployAwait(t, jr, jr.ctx, w, clock)
	executed := make(chan error, 1)
	go func() {
		executed <- jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command)
	}()
	select {
	case <-controller.started:
	case <-time.After(5 * time.Second):
		t.Fatal("deploy did not start")
	}
	clock.advance(previewJobDeployWaitCap())
	assertPreviewJobDeployOutcome(t, receivePreviewJobDeployOutcome(t, result),
		v1.PreviewJobDeployStatusFailed, previewJobDeployCodeDeployTimeout)
	assertPreviewJobDeployRegistryEmpty(t, jr.d)
	close(controller.release)
	if err := <-executed; err != nil {
		t.Fatal(err)
	}
}

func TestPreviewJobDeployEndsWhenLeaseIdentityChanges(t *testing.T) {
	tests := map[string]func(t *testing.T, jr *jobRunner, current lease.Lease){
		"other fencing token": func(t *testing.T, jr *jobRunner, current lease.Lease) {
			current.FencingToken++
			current.ExpiresAt = current.ExpiresAt.Add(time.Hour)
			if err := jr.d.leases.Acquire(current); err != nil {
				t.Fatal(err)
			}
		},
		"other attempt": func(t *testing.T, jr *jobRunner, current lease.Lease) {
			if !jr.d.leases.Release(current.AttemptID) {
				t.Fatal("lease was not released")
			}
			current.AttemptID = "a2"
			current.ExpiresAt = current.ExpiresAt.Add(time.Hour)
			if err := jr.d.leases.Acquire(current); err != nil {
				t.Fatal(err)
			}
		},
		"no lease": func(t *testing.T, jr *jobRunner, current lease.Lease) {
			if !jr.d.leases.Release(current.AttemptID) {
				t.Fatal("lease was not released")
			}
		},
	}
	for name, replace := range tests {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			w, _ := registerTestPreviewJobDeploy(t, jr, 1)
			current := *jr.d.leases.Current()
			clock := newFakePreviewJobDeployClock(current.ExpiresAt.Add(-time.Minute))
			result := startTestPreviewJobDeployAwait(t, jr, jr.ctx, w, clock)
			replace(t, jr, current)
			clock.fire(2)
			assertPreviewJobDeployOutcome(t, receivePreviewJobDeployOutcome(t, result),
				v1.PreviewJobDeployStatusFailed, previewJobDeployCodeLeaseExpired)
			assertPreviewJobDeployRegistryEmpty(t, jr.d)
		})
	}
}

func TestPreviewJobDeployStaysOpenWhenResultSendFails(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, command := registerTestPreviewJobDeploy(t, jr, 1)
	jr.d.eventSink = func(context.Context, any) error { return errors.New("send failed") }
	event := previewDeployResultEvent(*command.PreviewDeploy, previewDeployCommandID("1"),
		time.Now().UTC().Truncate(time.Millisecond), deployedPreviewOutcome())
	if err := jr.d.sendPreviewDeployResult(context.Background(), event); err == nil {
		t.Fatal("send did not fail")
	}
	assertPreviewJobDeployOpen(t, w)
	if err := jr.d.journal.Close(); err != nil {
		t.Fatal(err)
	}
	jr.d.eventSink = nil
	if err := jr.d.sendPreviewDeployResult(context.Background(), event); err == nil {
		t.Fatal("journal append did not fail")
	}
	assertPreviewJobDeployOpen(t, w)
}

func TestPreviewJobDeployAwaitTwiceReturnsBothTimes(t *testing.T) {
	jr, _ := testJobRunner(t)
	w, _ := registerTestPreviewJobDeploy(t, jr, 1)
	clock := newFakePreviewJobDeployClock(time.Now())
	first := startTestPreviewJobDeployAwait(t, jr, jr.ctx, w, clock)
	jr.cancel()
	assertPreviewJobDeployOutcome(t, receivePreviewJobDeployOutcome(t, first),
		v1.PreviewJobDeployStatusFailed, previewJobDeployCodeJobCancelled)
	second := startTestPreviewJobDeployAwait(t, jr, context.Background(), w, clock)
	assertPreviewJobDeployOutcome(t, receivePreviewJobDeployOutcome(t, second),
		v1.PreviewJobDeployStatusFailed, previewJobDeployCodeJobCancelled)
	assertPreviewJobDeployRegistryEmpty(t, jr.d)
}
