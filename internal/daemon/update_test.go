package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/updateipc"
)

type fakeUpdateIPC struct {
	mu          sync.Mutex
	requests    []updateipc.Request
	prepareErr  error
	activateErr error
	prepareOK   bool
	activateOK  bool
	healthOK    bool
	healthPhase string
	healthErr   error
	statusOK    bool
	statusPhase string
	ackSeen     func() bool
}

func (f *fakeUpdateIPC) Prepare(_ context.Context, requestID, updateID string) (updateipc.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, updateipc.Request{SchemaVersion: 1, RequestID: requestID, Action: updateipc.ActionPrepare, UpdateID: updateID})
	return updateipc.Response{SchemaVersion: 1, OK: f.prepareOK, Phase: "prepared", UpdateID: updateID, ErrorCode: failureCode(f.prepareOK)}, f.prepareErr
}

func (f *fakeUpdateIPC) Activate(_ context.Context, requestID, updateID string) (updateipc.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ackSeen != nil && !f.ackSeen() {
		return updateipc.Response{}, errors.New("activation preceded command acknowledgement attempt")
	}
	f.requests = append(f.requests, updateipc.Request{SchemaVersion: 1, RequestID: requestID, Action: updateipc.ActionActivate, UpdateID: updateID})
	return updateipc.Response{SchemaVersion: 1, OK: f.activateOK, Phase: "activating", UpdateID: updateID, ErrorCode: failureCode(f.activateOK)}, f.activateErr
}

func (f *fakeUpdateIPC) Health(_ context.Context, requestID, updateID, runnerVersion string) (updateipc.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, updateipc.Request{SchemaVersion: 1, RequestID: requestID, Action: updateipc.ActionHealth, UpdateID: updateID, RunnerVersion: runnerVersion})
	phase := f.healthPhase
	if phase == "" {
		phase = "healthy"
	}
	ok := f.healthOK
	if !ok && f.healthPhase == "" && f.healthErr == nil {
		ok = true
	}
	return updateipc.Response{SchemaVersion: 1, OK: ok, Phase: phase, UpdateID: updateID, ErrorCode: failureCode(ok)}, f.healthErr
}

func (f *fakeUpdateIPC) Status(_ context.Context, requestID, updateID string) (updateipc.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, updateipc.Request{SchemaVersion: 1, RequestID: requestID, Action: updateipc.ActionStatus, UpdateID: updateID})
	phase := f.statusPhase
	if phase == "" {
		phase = "prepared"
	}
	return updateipc.Response{SchemaVersion: 1, OK: f.statusOK, Phase: phase, UpdateID: updateID, ErrorCode: failureCode(f.statusOK)}, nil
}

func failureCode(ok bool) string {
	if ok {
		return ""
	}
	return "test_failure"
}

func updateCommand(id string) v1.CommandPayload {
	deadline := time.Now().Add(time.Hour)
	return v1.CommandPayload{Command: v1.CmdUpdate, UpdateID: id, DrainDeadline: &deadline}
}

func secureUpdateTestState(t *testing.T, d *Daemon) {
	t.Helper()
	if err := os.Chmod(d.cfg.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateCommandBoundaryAndDuplicateCoalescing(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	secureUpdateTestState(t, d)
	cmd := updateCommand("update-1")
	if err := d.executeCommand(context.Background(), v1.Envelope{ID: "command-1"}, cmd); err != nil {
		t.Fatal(err)
	}
	if err := d.executeCommand(context.Background(), v1.Envelope{ID: "command-2"}, cmd); err != nil {
		t.Fatalf("duplicate update did not coalesce: %v", err)
	}
	if err := d.executeCommand(context.Background(), v1.Envelope{ID: "command-3"}, updateCommand("update-2")); err == nil {
		t.Fatal("different update replaced an active drain")
	}
	bad := cmd
	bad.Reason = "also-install-this"
	if err := validateUpdateCommand(bad, time.Now()); err == nil {
		t.Fatal("update accepted an unrelated command field")
	}
	bad = cmd
	bad.UpdateID = "bad/id"
	if err := validateUpdateCommand(bad, time.Now()); err == nil {
		t.Fatal("unsafe update ID accepted")
	}
	bad = cmd
	bad.DrainDeadline = nil
	if err := validateUpdateCommand(bad, time.Now()); err != nil {
		t.Fatalf("optional drain deadline rejected: %v", err)
	}
}

func TestUpdatePayloadRejectsPrivilegedParametersBeforeDrain(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	secureUpdateTestState(t, d)
	d.runnerID = "runner-1"
	payload := map[string]any{
		"command": v1.CmdUpdate, "update_id": "update-1",
		"url": "https://attacker.invalid/payload", "target_path": "/usr/bin/our-runner", "force": true,
	}
	if err := d.OnCommand(context.Background(), commandEnvelope("command-boundary", payload)); err != nil {
		t.Fatal(err)
	}
	d.jobMu.Lock()
	pending := d.updateDrain
	d.jobMu.Unlock()
	if pending != nil {
		t.Fatalf("privileged update payload created drain state: %+v", pending)
	}
}

func TestConcurrentDuplicateUpdateIDsCoalesceRaceFree(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	secureUpdateTestState(t, d)
	cmd := updateCommand("update-race")
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- d.beginUpdateDrain(fmt.Sprintf("command-race-%d", i), cmd, time.Now())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	d.jobMu.Lock()
	pending := d.updateDrain
	d.jobMu.Unlock()
	if pending == nil || pending.UpdateID != "update-race" {
		t.Fatalf("pending=%+v", pending)
	}
}

func TestUpdateDrainingRejectsNewJobsAndWaitsForActiveJob(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	secureUpdateTestState(t, d)
	fake := &fakeUpdateIPC{prepareOK: true, activateOK: true}
	d.updateIPC = fake
	d.jobMu.Lock()
	d.activeJob = jr
	d.jobMu.Unlock()
	if err := d.beginUpdateDrain("command-1", updateCommand("update-1"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := d.markUpdateAckAttempted("update-1"); err != nil {
		t.Fatal(err)
	}
	if err := d.processPendingUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 0 {
		t.Fatal("updater contacted while job was active")
	}
	if err := d.startJob(context.Background(), validJobForUpdateTest()); err == nil || !strings.Contains(err.Error(), "draining") {
		t.Fatalf("new job was not rejected by update drain: %v", err)
	}
	d.jobMu.Lock()
	d.activeJob = nil
	d.jobMu.Unlock()
	if err := d.processPendingUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 3 || fake.requests[0].Action != updateipc.ActionStatus || fake.requests[1].Action != updateipc.ActionPrepare || fake.requests[2].Action != updateipc.ActionActivate {
		t.Fatalf("requests=%+v", fake.requests)
	}
}

func TestUpdateActivationOccursOnlyAfterAckAttemptAndFailurePreservesPause(t *testing.T) {
	jr, events := testJobRunner(t)
	d := jr.d
	secureUpdateTestState(t, d)
	d.paused.Store(true)
	fake := &fakeUpdateIPC{prepareOK: true, activateOK: true}
	d.updateIPC = fake
	fake.ackSeen = func() bool {
		d.jobMu.Lock()
		defer d.jobMu.Unlock()
		return d.updateDrain != nil && d.updateDrain.AckAttempted
	}
	cmd := updateCommand("update-ack")
	if err := d.OnCommand(context.Background(), commandEnvelope("command-update", cmd)); err != nil {
		t.Fatal(err)
	}
	if err := d.processPendingUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 3 {
		t.Fatalf("requests=%+v", fake.requests)
	}

	// A preparation failure clears only the update drain, not an independent pause.
	d.updateIPC = &fakeUpdateIPC{prepareErr: errors.New("socket unavailable")}
	if err := d.clearPendingUpdate("update-ack"); err != nil {
		t.Fatal(err)
	}
	if err := d.beginUpdateDrain("command-fail", updateCommand("update-fail"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := d.markUpdateAckAttempted("update-fail"); err != nil {
		t.Fatal(err)
	}
	if err := d.processPendingUpdate(context.Background()); err == nil {
		t.Fatal("preparation failure was hidden")
	}
	if !d.Paused() {
		t.Fatal("preparation failure cleared manual pause")
	}
	d.jobMu.Lock()
	pending := d.updateDrain
	d.jobMu.Unlock()
	if pending != nil {
		t.Fatalf("failed update drain remains: %+v", pending)
	}
	if len(events.byKind("runner_update")) == 0 {
		t.Fatal("update phases were not emitted")
	}
}

func TestUpdateReconcilesAlreadyPreparedRootTransaction(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	secureUpdateTestState(t, d)
	fake := &fakeUpdateIPC{statusOK: true, statusPhase: "prepared", activateOK: true}
	d.updateIPC = fake
	if err := d.beginUpdateDrain("command-prepared", updateCommand("update-prepared"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := d.markUpdateAckAttempted("update-prepared"); err != nil {
		t.Fatal(err)
	}
	if err := d.processPendingUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 2 || fake.requests[0].Action != updateipc.ActionStatus || fake.requests[1].Action != updateipc.ActionActivate {
		t.Fatalf("requests=%+v", fake.requests)
	}
}

func TestAlreadyCommittedUpdateClearsWithoutPreparingAgain(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	secureUpdateTestState(t, d)
	fake := &fakeUpdateIPC{statusOK: true, statusPhase: "healthy"}
	d.updateIPC = fake
	if err := d.beginUpdateDrain("command-healthy", updateCommand("update-healthy"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := d.markUpdateAckAttempted("update-healthy"); err != nil {
		t.Fatal(err)
	}
	if err := d.processPendingUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.jobMu.Lock()
	pending := d.updateDrain
	d.jobMu.Unlock()
	if pending != nil || len(fake.requests) != 1 || fake.requests[0].Action != updateipc.ActionStatus {
		t.Fatalf("pending=%+v requests=%+v", pending, fake.requests)
	}
}

func TestLostActivationResponsePreservesPendingHealthState(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	secureUpdateTestState(t, d)
	fake := &fakeUpdateIPC{
		prepareOK: true, activateErr: errors.New("response lost"),
		statusOK: true, statusPhase: "awaiting_health",
	}
	d.updateIPC = fake
	if err := d.beginUpdateDrain("command-lost", updateCommand("update-lost"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := d.markUpdateAckAttempted("update-lost"); err != nil {
		t.Fatal(err)
	}
	if err := d.processPendingUpdate(context.Background()); err == nil {
		t.Fatal("lost response should remain visible as a reconciliation error")
	}
	d.jobMu.Lock()
	pending := d.updateDrain
	d.jobMu.Unlock()
	if pending == nil || pending.Phase != updatePhaseActivating {
		t.Fatalf("activation uncertainty was cleared: %+v", pending)
	}
	if err := d.processPendingUpdate(context.Background()); err != nil {
		t.Fatalf("awaiting-health reconciliation: %v", err)
	}
	d.jobMu.Lock()
	pending = d.updateDrain
	d.jobMu.Unlock()
	if pending == nil {
		t.Fatal("awaiting-health transaction was cleared before candidate health")
	}
}

func TestPendingUpdatePersistsAndDeadlineCancels(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	secureUpdateTestState(t, d)
	cmd := updateCommand("update-restart")
	if err := d.beginUpdateDrain("command-restart", cmd, time.Now()); err != nil {
		t.Fatal(err)
	}
	restored, err := loadPendingUpdate(d.cfg.StateDir)
	if err != nil || restored == nil || restored.UpdateID != "update-restart" {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
	restored.DrainDeadline = time.Now().Add(-time.Second)
	if err := persistPendingUpdate(d.cfg.StateDir, restored); err != nil {
		t.Fatal(err)
	}
	loaded, expired, err := restorePendingUpdate(d.cfg.StateDir, time.Now())
	if err != nil || loaded != nil || expired == nil || expired.UpdateID != "update-restart" {
		t.Fatalf("loaded=%+v expired=%v err=%v", loaded, expired, err)
	}
}

func TestUpdateEventIDsAreDeterministicAndPhaseSpecific(t *testing.T) {
	a := updateEventID("runner-1", "update-1", "prepared")
	b := updateEventID("runner-1", "update-1", "prepared")
	c := updateEventID("runner-1", "update-1", "failed")
	if a != b || a == c {
		t.Fatalf("ids a=%q b=%q c=%q", a, b, c)
	}
}

func TestUpdatePhaseEventIsDurableAndSanitized(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.eventSink = nil
	d.runnerID = "runner-1"
	if err := d.emitUpdatePhase(context.Background(), "update-1", updatePhaseFailed, "prepare_rejected"); err != nil {
		t.Fatal(err)
	}
	records, err := d.journal.Unacked(context.Background(), journal.KindEvent, 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%v err=%v", records, err)
	}
	if records[0].ID != updateEventID("runner-1", "update-1", updatePhaseFailed) ||
		strings.Contains(string(records[0].Payload), "attacker.invalid") {
		t.Fatalf("record=%+v", records[0])
	}
}

func TestFailedUpdateRetainsDrainUntilTerminalEventIsDurable(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	secureUpdateTestState(t, d)
	d.eventSink = nil
	d.runnerID = "runner-1"
	if err := d.beginUpdateDrain("command-event-retry", updateCommand("update-event-retry"), time.Now()); err != nil {
		t.Fatal(err)
	}

	// Force journal capacity failure before the terminal event can be stored.
	d.journal.SetMaxBytes(1)
	cause := errors.New("preparation failed")
	if err := d.failPendingUpdate(context.Background(), "update-event-retry", updatePhaseFailed, "prepare_rejected", cause); err == nil {
		t.Fatal("terminal event failure was hidden")
	}
	d.jobMu.Lock()
	pending := d.updateDrain
	d.jobMu.Unlock()
	if pending == nil || pending.UpdateID != "update-event-retry" {
		t.Fatalf("failed event recording released the update drain: %+v", pending)
	}
	if loaded, err := loadPendingUpdate(d.cfg.StateDir); err != nil || loaded == nil {
		t.Fatalf("durable pending drain was removed: %+v err=%v", loaded, err)
	}
	if err := d.startJob(context.Background(), validJobForUpdateTest()); err == nil || !strings.Contains(err.Error(), "draining") {
		t.Fatalf("scheduling was not blocked after terminal event failure: %v", err)
	}

	// Once the event is durable, the same terminal transition may release the
	// drain. The original operation error remains visible to the caller.
	d.journal.SetMaxBytes(512 << 20)
	if err := d.failPendingUpdate(context.Background(), "update-event-retry", updatePhaseFailed, "prepare_rejected", cause); !errors.Is(err, cause) {
		t.Fatalf("retry did not preserve original cause: %v", err)
	}
	d.jobMu.Lock()
	pending = d.updateDrain
	d.jobMu.Unlock()
	if pending != nil {
		t.Fatalf("durably recorded failure did not release drain: %+v", pending)
	}
	if loaded, err := loadPendingUpdate(d.cfg.StateDir); err != nil || loaded != nil {
		t.Fatalf("pending state remains after durable event: %+v err=%v", loaded, err)
	}
	records, err := d.journal.Unacked(context.Background(), journal.KindEvent, 10)
	if err != nil || len(records) != 1 || records[0].ID != updateEventID("runner-1", "update-event-retry", updatePhaseFailed) {
		t.Fatalf("terminal event was not durably recorded: records=%v err=%v", records, err)
	}
}

func TestRestoredActivatingUpdateReportsHealthAfterAuthenticatedReadiness(t *testing.T) {
	jr, events := testJobRunner(t)
	d := jr.d
	secureUpdateTestState(t, d)
	d.runnerID = "runner-1"
	d.updateIPC = &fakeUpdateIPC{prepareOK: true, activateOK: true}
	d.updateDrain = &pendingUpdate{
		UpdateID: "update-health", CommandID: "command-health", DrainDeadline: time.Now().Add(time.Hour),
		Phase: updatePhaseActivating, AckAttempted: true,
	}
	d.updateRestored = true
	if err := persistPendingUpdate(d.cfg.StateDir, d.updateDrain); err != nil {
		t.Fatal(err)
	}
	d.notifyStartupUpdateHealth(context.Background())
	d.jobMu.Lock()
	pending := d.updateDrain
	d.jobMu.Unlock()
	if pending != nil {
		t.Fatalf("healthy restored update was not cleared: %+v", pending)
	}
	phases := events.byKind("runner_update")
	if len(phases) != 1 || phases[0]["phase"] != updatePhaseHealthy {
		t.Fatalf("events=%v", phases)
	}
}

func TestRestoredUpdateRejectsIdleAndReconcilesCommittedStatus(t *testing.T) {
	for _, tc := range []struct {
		name        string
		fake        *fakeUpdateIPC
		wantPending bool
	}{
		{name: "idle is not proof of commit", fake: &fakeUpdateIPC{healthPhase: "idle"}, wantPending: true},
		{name: "committed status recovers lost health response", fake: &fakeUpdateIPC{healthErr: errors.New("lost response"), statusOK: true, statusPhase: "healthy"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			d := jr.d
			secureUpdateTestState(t, d)
			d.runnerID = "runner-1"
			d.updateIPC = tc.fake
			d.updateDrain = &pendingUpdate{UpdateID: "update-reconcile", CommandID: "command-reconcile", DrainDeadline: time.Now().Add(time.Hour), Phase: updatePhaseActivating, AckAttempted: true}
			d.updateRestored = true
			if err := persistPendingUpdate(d.cfg.StateDir, d.updateDrain); err != nil {
				t.Fatal(err)
			}
			d.notifyStartupUpdateHealth(context.Background())
			d.jobMu.Lock()
			pending := d.updateDrain
			d.jobMu.Unlock()
			if (pending != nil) != tc.wantPending {
				t.Fatalf("pending=%+v wantPending=%v", pending, tc.wantPending)
			}
			if tc.wantPending {
				if err := d.processPendingUpdate(context.Background()); err == nil {
					t.Fatal("ambiguous idle status did not require reconciliation")
				}
				d.jobMu.Lock()
				stillPending := d.updateDrain
				d.jobMu.Unlock()
				if stillPending == nil {
					t.Fatal("ambiguous idle status cleared the update drain")
				}
			}
		})
	}
}

func TestRestartAfterExactRollbackOrQuarantineResumesScheduling(t *testing.T) {
	for _, tc := range []struct {
		statusPhase string
		eventPhase  string
		errorCode   string
	}{
		{statusPhase: updatePhaseRolledBack, eventPhase: updatePhaseRolledBack, errorCode: "activation_rolled_back"},
		{statusPhase: "quarantined", eventPhase: updatePhaseFailed, errorCode: "release_quarantined"},
	} {
		t.Run(tc.statusPhase, func(t *testing.T) {
			jr, events := testJobRunner(t)
			d := jr.d
			secureUpdateTestState(t, d)
			d.runnerID = "runner-1"
			d.paused.Store(true)
			d.updateIPC = &fakeUpdateIPC{
				healthErr: errors.New("candidate did not become healthy"),
				statusOK:  true, statusPhase: tc.statusPhase,
			}
			persisted := &pendingUpdate{
				UpdateID: "update-rollback", CommandID: "command-rollback", DrainDeadline: time.Now().Add(time.Hour),
				Phase: updatePhaseActivating, AckAttempted: true,
			}
			if err := persistPendingUpdate(d.cfg.StateDir, persisted); err != nil {
				t.Fatal(err)
			}
			restored, _, err := restorePendingUpdate(d.cfg.StateDir, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			d.updateDrain = restored
			d.updateRestored = true
			if err := d.processPendingUpdate(context.Background()); err != nil {
				t.Fatalf("reconcile terminal status: %v", err)
			}
			d.jobMu.Lock()
			pending := d.updateDrain
			d.jobMu.Unlock()
			if pending != nil {
				t.Fatalf("terminal update still blocks scheduling: %+v", pending)
			}
			if !d.Paused() {
				t.Fatal("rollback reconciliation cleared independent manual pause")
			}
			phases := events.byKind("runner_update")
			if len(phases) != 1 || phases[0]["phase"] != tc.eventPhase || phases[0]["error_code"] != tc.errorCode {
				t.Fatalf("events=%v", phases)
			}
			if loaded, err := loadPendingUpdate(d.cfg.StateDir); err != nil || loaded != nil {
				t.Fatalf("pending state survived terminal reconciliation: %+v err=%v", loaded, err)
			}
		})
	}
}

func validJobForUpdateTest() v1.JobSpec {
	return withTestAuthority(v1.JobSpec{
		JobID: "job-2", AttemptID: "attempt-2", Kind: v1.JobKindBuild,
		FencingToken: 8, LeaseExpiresAt: time.Now().Add(time.Hour),
		ApprovedPlanRevision: 1, ApprovedPlan: testApprovedPlan(1),
		Repositories: []v1.JobRepository{{RepositoryID: "repo-1", Primary: true}},
		Ticket:       v1.TicketContext{Key: "T-2", Summary: "summary", Description: "description"},
	})
}
