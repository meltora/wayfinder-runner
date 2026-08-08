package daemon_test

import (
	"context"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/mockcp"
)

func TestRotationViaCommand(t *testing.T) {
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

	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	d, err := daemon.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	waitFor(t, "runner connected", 10*time.Second, func() bool { return cp.HelloCount() >= 1 })
	hellosBefore := cp.HelloCount()

	if err := cp.SendCommand(ctx, v1.CommandPayload{Command: v1.CmdRotateCert}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "cert_rotated event", 15*time.Second, func() bool {
		return len(cp.EventsByKind("cert_rotated")) >= 1
	})
	waitFor(t, "reconnect with new cert", 15*time.Second, func() bool {
		return cp.HelloCount() > hellosBefore
	})
	cancel()
}

func TestRotateRejectsUnknownRunner(t *testing.T) {
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

	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	d, err := daemon.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.TestRotate(ctx); err != nil {
		t.Fatalf("first rotation: %v", err)
	}
	if err := d.TestRotate(ctx); err != nil {
		t.Fatalf("second rotation with rotated cert: %v", err)
	}
}
