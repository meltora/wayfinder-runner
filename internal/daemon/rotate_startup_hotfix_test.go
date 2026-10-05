package daemon_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/mockcp"
)

type hotfixLogs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *hotfixLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *hotfixLogs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func unreachableHTTPSURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return "https://" + addr
}

func hotfixDaemon(t *testing.T, offset time.Duration) (*mockcp.Server, *daemon.Daemon, *hotfixLogs) {
	t.Helper()
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cp.Close)
	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	cfg.ControlPlaneHTTPURL = unreachableHTTPSURL(t)
	logs := &hotfixLogs{}
	d, err := daemon.New(cfg, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	d.SetClock(func() time.Time { return time.Now().Add(offset) })
	return cp, d, logs
}

func TestHotfixStartupRenewalFailureKeepsTheCurrentCertificate(t *testing.T) {
	cp, d, logs := hotfixDaemon(t, 13*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	deadline := time.After(20 * time.Second)
	for cp.HelloCount() < 1 {
		select {
		case err := <-done:
			t.Fatalf("Run stopped after a startup renewal failure: %v\n%s", err, logs.String())
		case <-deadline:
			t.Fatalf("runner did not connect with its current certificate\n%s", logs.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if !strings.Contains(logs.String(), "startup certificate rotation failed; continuing with the current certificate") {
		t.Fatalf("startup renewal failure was not attempted and logged:\n%s", logs.String())
	}
}

func TestHotfixStartupRenewalFailureWithExpiredCertificateIsFatal(t *testing.T) {
	cp, d, logs := hotfixDaemon(t, 25*time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := d.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "startup certificate rotation") {
		t.Fatalf("Run = %v, want the startup certificate rotation failure\n%s", err, logs.String())
	}
	if cp.HelloCount() != 0 {
		t.Fatal("runner connected with an expired certificate")
	}
}
