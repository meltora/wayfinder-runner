package daemon_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/mockcp"
)

func startMockControlPlane(t *testing.T, cp *mockcp.Server) {
	t.Helper()
	if err := cp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cp.Close)
}

func TestStartupRotationFailureIsNotFatal(t *testing.T) {
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	startMockControlPlane(t, cp)
	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	cp.FailNextRotation(http.StatusNotFound, "")

	d, err := daemon.New(directRunnerConfig(cp.WSURL(), stateDir), nil)
	if err != nil {
		t.Fatal(err)
	}
	d.SetClock(func() time.Time { return time.Now().Add(13 * time.Hour) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	deadline := time.After(15 * time.Second)
	for cp.HelloCount() < 1 {
		select {
		case err := <-done:
			t.Fatalf("Run stopped after a startup rotation failure: %v", err)
		case <-deadline:
			t.Fatal("runner did not connect with its current certificate")
		case <-time.After(50 * time.Millisecond):
		}
	}
	requests := cp.RotateRequests()
	if len(requests) != 1 || requests[0].Status != http.StatusNotFound {
		t.Fatalf("rotate requests = %+v, want one refused request", requests)
	}
	cancel()
	<-done
}

func TestStartupRotationFailureWithExpiredCertificateStaysFatal(t *testing.T) {
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	startMockControlPlane(t, cp)
	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	cp.FailNextRotation(http.StatusNotFound, "")

	d, err := daemon.New(directRunnerConfig(cp.WSURL(), stateDir), nil)
	if err != nil {
		t.Fatal(err)
	}
	d.SetClock(func() time.Time { return time.Now().Add(48 * time.Hour) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = d.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "startup certificate rotation") {
		t.Fatalf("Run = %v, want the startup certificate rotation failure", err)
	}
	if cp.HelloCount() != 0 {
		t.Fatalf("runner connected with an expired certificate")
	}
}
