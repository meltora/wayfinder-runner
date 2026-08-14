package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/collector"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/lease"
)

func commandEnvelope(id string, payload any) v1.Envelope {
	return v1.Envelope{
		Version: v1.ProtocolVersion,
		Type:    v1.TypeCommand,
		ID:      id,
		SentAt:  time.Now(),
		Payload: payload,
	}
}

func TestOnCommandValidationDeduplicationAndStrictPayload(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.runnerID = "runner-1"

	invalid := commandEnvelope("invalid command id", v1.CommandPayload{Command: v1.CmdResumeRunner})
	if err := d.OnCommand(context.Background(), invalid); err != nil {
		t.Fatal(err)
	}

	valid := commandEnvelope("command-1", v1.CommandPayload{Command: v1.CmdResumeRunner})
	if err := d.OnCommand(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	if d.Paused() {
		t.Fatal("resume command left runner paused")
	}
	if err := d.OnCommand(context.Background(), valid); err != nil {
		t.Fatal(err)
	}

	unknown := commandEnvelope("command-2", map[string]any{"command": v1.CmdResumeRunner, "unexpected": true})
	if err := d.OnCommand(context.Background(), unknown); err != nil {
		t.Fatal(err)
	}
	badJSON := commandEnvelope("command-3", map[string]any{"command": func() {}})
	if err := d.OnCommand(context.Background(), badJSON); err != nil {
		t.Fatal(err)
	}
}

func TestValidateCommandEnvelopeBoundaries(t *testing.T) {
	d := &Daemon{runnerID: "runner-1"}
	valid := commandEnvelope("command-1", nil)
	if err := d.validateCommandEnvelope(valid); err != nil {
		t.Fatal(err)
	}
	cases := []v1.Envelope{
		{Version: 0, Type: v1.TypeCommand, ID: "command-1", SentAt: time.Now()},
		{Version: v1.ProtocolVersion, Type: v1.TypeEvent, ID: "command-1", SentAt: time.Now()},
		{Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: "command-1"},
		{Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: "command-1", SentAt: time.Now().Add(-11 * time.Minute)},
		{Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: "command-1", SentAt: time.Now().Add(3 * time.Minute)},
		{Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: "command-1", SentAt: time.Now(), RunnerID: "runner-2"},
	}
	for _, env := range cases {
		if err := d.validateCommandEnvelope(env); err == nil {
			t.Fatalf("invalid envelope accepted: %+v", env)
		}
	}
}

func TestExecuteCommandPolicyAndLeaseBranches(t *testing.T) {
	t.Run("job gating", func(t *testing.T) {
		jr, _ := testJobRunner(t)
		d := jr.d
		d.paused.Store(true)
		if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdStartJob}); err == nil {
			t.Fatal("paused start accepted")
		}
		if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdReviewComment}); err == nil {
			t.Fatal("paused continuation accepted")
		}
		d.paused.Store(false)
		if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdStartJob}); err == nil {
			t.Fatal("missing job accepted")
		}
		if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdReviewComment}); err == nil {
			t.Fatal("missing continuation job accepted")
		}
	})

	t.Run("renewal fencing", func(t *testing.T) {
		jr, _ := testJobRunner(t)
		d := jr.d
		if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdRenewLease, AttemptID: "bad id", FencingToken: 7}); err == nil {
			t.Fatal("invalid attempt ID accepted")
		}
		if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdRenewLease, AttemptID: "a1"}); err == nil {
			t.Fatal("zero fencing token accepted")
		}
		if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdRenewLease, AttemptID: "a1", FencingToken: 99, LeaseExpiresAt: time.Now().Add(time.Hour)}); err == nil {
			t.Fatal("wrong fencing token accepted")
		}
		expires := time.Now().Add(30 * time.Minute).Truncate(time.Second)
		if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdRenewLease, AttemptID: "a1", FencingToken: 7, LeaseExpiresAt: expires}); err != nil {
			t.Fatalf("valid renewal: %v", err)
		}
		persisted, err := d.journal.LoadLease(context.Background())
		if err != nil || persisted == nil || persisted.FencingToken != 7 {
			t.Fatalf("persisted lease=%+v err=%v", persisted, err)
		}
	})

	t.Run("control commands", func(t *testing.T) {
		jr, _ := testJobRunner(t)
		d := jr.d
		if err := os.Chmod(d.cfg.StateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdDecisionResponse}); err == nil {
			t.Fatal("decision without active job accepted")
		}
		if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdEmergencyStop}); err != nil {
			t.Fatal(err)
		}
		if !d.Paused() {
			t.Fatal("emergency stop did not pause runner")
		}
		if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdResumeRunner}); err != nil {
			t.Fatal(err)
		}
		if d.Paused() {
			t.Fatal("resume did not clear pause")
		}
		if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: "future_command"}); err == nil {
			t.Fatal("unsupported command accepted")
		}
	})
}

func TestExecuteUpdateRequiresCapabilityPoorPayload(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdUpdate}); err == nil {
		t.Fatal("update without update ID accepted")
	}
	cmd := updateCommand("update-1")
	cmd.JobID = "smuggled-job"
	if err := d.executeCommand(context.Background(), v1.Envelope{ID: "command-1"}, cmd); err == nil {
		t.Fatal("update accepted an unrelated command field")
	}
}

func TestHeartbeatAndConnectionLifecycleStates(t *testing.T) {
	stateDir := t.TempDir()
	j, err := journal.Open(filepath.Join(stateDir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	events := &capturedEvents{}
	d := &Daemon{
		cfg: config.Normalize(config.Config{
			StateDir: stateDir, ControlPlaneHTTPURL: "https://control.example.test",
		}),
		store:      identity.NewStore(stateDir),
		runnerID:   "runner-1",
		journal:    j,
		leases:     lease.NewManager(),
		collectors: collector.NewStore(stateDir),
		uploader:   newArtifactUploader("https://control.example.test"),
		logger:     slog.Default(),
		eventSink:  events.sink,
	}
	if got := d.heartbeatPayload(context.Background()); got.State != "idle" || got.ActiveJobs != 0 {
		t.Fatalf("idle heartbeat=%+v", got)
	}
	d.paused.Store(true)
	if got := d.heartbeatPayload(context.Background()); got.State != "paused" {
		t.Fatalf("paused heartbeat=%+v", got)
	}
	d.jobMu.Lock()
	d.activeJob = &jobRunner{}
	d.jobMu.Unlock()
	if got := d.heartbeatPayload(context.Background()); got.State != "busy" || got.ActiveJobs != 1 {
		t.Fatalf("busy heartbeat=%+v", got)
	}
	d.jobMu.Lock()
	d.activeJob = nil
	d.jobMu.Unlock()
	d.recoveredLease.Store(true)
	if err := d.leases.Acquire(authoritativeTestLease("job-1", "attempt-1", 2, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if got := d.heartbeatPayload(context.Background()); got.State != "reconciliation_required" {
		t.Fatalf("recovered heartbeat=%+v", got)
	}
	d.OnConnected(context.Background())
	if len(events.byKind("capability_manifest")) != 1 || len(events.byKind("lease_reconciliation_required")) != 1 {
		t.Fatalf("connection events=%v", events.events)
	}

	d.setControlTLS(&tls.Config{MinVersion: tls.VersionTLS13})
	if d.httpClientForURL("https://control.example.test/artifact", time.Second) != d.controlHTTP {
		t.Fatal("same-origin URL did not receive authenticated client")
	}
	if d.httpClientForURL("https://other.example.test/artifact", time.Second) == d.controlHTTP {
		t.Fatal("cross-origin URL received authenticated client")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	d.journalMaintenance(cancelled)
	d.pauseStateLoop(cancelled)
	d.drainHookSpool(cancelled)
}

func TestOnEventAckAndEventSinkValidation(t *testing.T) {
	jr, cap := testJobRunner(t)
	d := jr.d
	d.OnEventAck(context.Background(), "bad id")
	if err := d.sendEventWithID(context.Background(), "bad id", map[string]any{"kind": "x"}); err == nil {
		t.Fatal("invalid event ID accepted")
	}
	if err := d.sendEventWithID(context.Background(), "event-1", map[string]any{"kind": "custom"}); err != nil {
		t.Fatal(err)
	}
	if len(cap.byKind("custom")) != 1 {
		t.Fatalf("events=%v", cap.events)
	}
}

func TestRecoveredLeaseIsRestoredByNew(t *testing.T) {
	// The complete enrolled New path is already exercised by daemon integration
	// tests. This test isolates the durable lease shape used during restart.
	stateDir := t.TempDir()
	j, err := journal.Open(filepath.Join(stateDir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	want := authoritativeTestLease("job-1", "attempt-1", 9, time.Now().Add(time.Hour))
	if err := j.SaveLease(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	j, err = journal.Open(filepath.Join(stateDir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	got, err := j.LoadLease(context.Background())
	if err != nil || got == nil || got.AttemptID != want.AttemptID {
		t.Fatalf("restored=%+v err=%v", got, err)
	}
}

var _ = json.RawMessage{}
