package daemon_test

import (
	"context"
	"testing"
	"time"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/enroll"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/mockcp"
)

func directRunnerConfig(controlPlaneURL, stateDir string) config.Config {
	return config.Config{
		ControlPlaneURL:   controlPlaneURL,
		StateDir:          stateDir,
		Transport:         "websocket",
		Isolation:         "direct",
		AllowUnsafeDirect: true,
		Supervisor:        "direct",
		Provider:          "codex",
	}
}

func enrollRunner(t *testing.T, cp *mockcp.Server, stateDir string) {
	t.Helper()
	cp.AddEnrollmentCode("code-1")
	store := identity.NewStore(stateDir)
	key, err := store.EnsureKey()
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	csr, err := identity.NewCSR(key, "test-host")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}
	resp, err := (&enroll.Client{BaseURL: cp.EnrollURL(), AllowInsecure: true}).Enroll(context.Background(), enroll.Request{
		EnrollmentCode: "code-1",
		CSR:            string(csr),
		RunnerVersion:  daemon.Version,
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if err := identity.ValidateIssued([]byte(resp.Certificate), []byte(resp.CACert), key, resp.RunnerID, time.Now()); err != nil {
		t.Fatalf("ValidateIssued: %v", err)
	}
	if err := store.SaveIdentity(key, []byte(resp.Certificate), []byte(resp.CACert)); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
	if err := daemon.WriteRunnerID(stateDir, resp.RunnerID); err != nil {
		t.Fatalf("WriteRunnerID: %v", err)
	}
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestEndToEndEnrollConnectManifestReconnect(t *testing.T) {
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := cp.Start(context.Background()); err != nil {
		t.Fatalf("cp.Start: %v", err)
	}
	defer cp.Close()
	cp.DropOnHello = 1

	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)

	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	d, err := daemon.New(cfg, nil)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()

	waitFor(t, "reconnect after drop", 10*time.Second, func() bool {
		return cp.HelloCount() >= 2
	})
	waitFor(t, "capability manifest event", 10*time.Second, func() bool {
		for _, e := range cp.Events() {
			if payload, ok := e.Payload.(map[string]any); ok && payload["kind"] == "capability_manifest" {
				return true
			}
		}
		return false
	})
	waitFor(t, "heartbeats", 5*time.Second, func() bool {
		return cp.Heartbeats() >= 1
	})

	cancel()
	if err := <-errCh; err != context.Canceled {
		t.Fatalf("daemon exit err = %v", err)
	}
}

func TestDaemonRefusesWithoutEnrollment(t *testing.T) {
	cfg := directRunnerConfig("wss://127.0.0.1:1/stream", t.TempDir())
	if _, err := daemon.New(cfg, nil); err == nil {
		t.Fatal("expected not-enrolled error")
	}
}

func TestEnrollmentCodeSingleUse(t *testing.T) {
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := cp.Start(context.Background()); err != nil {
		t.Fatalf("cp.Start: %v", err)
	}
	defer cp.Close()

	cp.AddEnrollmentCode("code-once")
	dir := t.TempDir()
	store := identity.NewStore(dir)
	key, _ := store.EnsureKey()
	csr, _ := identity.NewCSR(key, "h")
	req := enroll.Request{EnrollmentCode: "code-once", CSR: string(csr), RunnerVersion: "0.1.0"}
	c := &enroll.Client{BaseURL: cp.EnrollURL(), AllowInsecure: true}
	if _, err := c.Enroll(context.Background(), req); err != nil {
		t.Fatalf("first enroll: %v", err)
	}
	if _, err := c.Enroll(context.Background(), req); err == nil {
		t.Fatal("second enroll with same code should fail")
	}
}
