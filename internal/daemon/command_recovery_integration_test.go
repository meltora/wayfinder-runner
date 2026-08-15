package daemon_test

import (
	"context"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/mockcp"
)

func TestCommandRedeliveryReplaysOriginalAckAndChangedReuseFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	d, err := daemon.New(directRunnerConfig(cp.WSURL(), stateDir), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Run(ctx) }()
	waitFor(t, "runner connected", 10*time.Second, func() bool { return cp.HelloCount() >= 1 })

	envelope := v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: "stable-resume-command",
		SentAt: time.Now(), Payload: v1.CommandPayload{Command: v1.CmdResumeRunner},
	}
	if err := cp.SendCommandEnvelope(ctx, envelope); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "original command acknowledgement", 10*time.Second, func() bool {
		return len(cp.CommandAcks()) == 1
	})
	envelope.SentAt = time.Now()
	if err := cp.SendCommandEnvelope(ctx, envelope); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replayed command acknowledgement", 10*time.Second, func() bool {
		return len(cp.CommandAcks()) == 2
	})
	for _, acknowledgement := range cp.CommandAcks() {
		payload := acknowledgement.Payload.(map[string]any)
		if acknowledgement.InReplyTo != envelope.ID || payload["status"] != "accepted" || payload["reason_code"] != nil || payload["error"] != nil {
			t.Fatalf("replayed acknowledgement = %+v", acknowledgement)
		}
	}

	changed := envelope
	changed.SentAt = time.Now()
	changed.Payload = v1.CommandPayload{Command: v1.CmdEmergencyStop}
	if err := cp.SendCommandEnvelope(ctx, changed); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "conflicting command acknowledgement", 10*time.Second, func() bool {
		return len(cp.CommandAcks()) == 3
	})
	conflict := cp.CommandAcks()[2].Payload.(map[string]any)
	if conflict["status"] != "rejected" || conflict["reason_code"] != "command_conflict" || conflict["error"] != nil {
		t.Fatalf("conflict acknowledgement = %v", conflict)
	}
	if d.Paused() {
		t.Fatal("conflicting command content applied emergency stop")
	}
}
