package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/lease"
	"github.com/example/app-runner/internal/mockcp"
	"github.com/example/app-runner/internal/runlock"
)

func enrolledDirectConfig(t *testing.T) config.Config {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	ca, err := mockcp.NewTestCA()
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := identity.NewCSR(key, "runner-1")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.SignCSR(csr, "runner-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := identity.NewStore(stateDir).SaveIdentity(key, cert, ca.CertPEM); err != nil {
		t.Fatal(err)
	}
	if err := WriteRunnerID(stateDir, "runner-1"); err != nil {
		t.Fatal(err)
	}
	return config.Normalize(config.Config{
		ControlPlaneURL: "wss://control.example.test/stream",
		StateDir:        stateDir, Transport: "websocket", Isolation: "direct",
		AllowUnsafeDirect: true, Supervisor: "direct", Provider: "codex",
	})
}

func closeConstructedDaemon(d *Daemon) {
	if d == nil {
		return
	}
	_ = d.journal.Close()
	_ = d.runLock.Close()
}

func TestNewRestoresDurableLeaseAndPersistentPause(t *testing.T) {
	cfg := enrolledDirectConfig(t)
	j, err := journal.Open(filepath.Join(cfg.StateDir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	want := lease.Lease{JobID: "job-1", AttemptID: "attempt-1", FencingToken: 4, ExpiresAt: time.Now().Add(time.Hour)}
	if err := j.SaveLease(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := SetPausedState(cfg.StateDir, true); err != nil {
		t.Fatal(err)
	}
	d, err := New(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer closeConstructedDaemon(d)
	if !d.recoveredLease.Load() || d.leases.Current() == nil || !d.Paused() {
		t.Fatalf("recovered=%v lease=%+v paused=%v", d.recoveredLease.Load(), d.leases.Current(), d.Paused())
	}
}

func TestNewRestoresPendingUpdateDrain(t *testing.T) {
	cfg := enrolledDirectConfig(t)
	pending := &pendingUpdate{
		UpdateID: "update-restart", CommandID: "command-restart", DrainDeadline: time.Now().Add(time.Hour),
		Phase: updatePhasePrepared, AckAttempted: true,
	}
	if err := persistPendingUpdate(cfg.StateDir, pending); err != nil {
		t.Fatal(err)
	}
	d, err := New(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer closeConstructedDaemon(d)
	d.jobMu.Lock()
	restored := d.updateDrain
	d.jobMu.Unlock()
	if restored == nil || restored.UpdateID != pending.UpdateID || !d.updateRestored {
		t.Fatalf("restored=%+v updateRestored=%v", restored, d.updateRestored)
	}
}

func TestNewClearsExpiredLeaseAndOrphanedRuntime(t *testing.T) {
	cfg := enrolledDirectConfig(t)
	j, err := journal.Open(filepath.Join(cfg.StateDir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.SaveLease(context.Background(), lease.Lease{JobID: "job-1", AttemptID: "attempt-1", FencingToken: 4, ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	orphan := filepath.Join(cfg.StateDir, "jobs", "job-old")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := New(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer closeConstructedDaemon(d)
	if d.recoveredLease.Load() || d.leases.Current() != nil {
		t.Fatalf("expired lease restored: %+v", d.leases.Current())
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan remains: %v", err)
	}
}

func TestNewFailureGatesCloseResources(t *testing.T) {
	cfg := enrolledDirectConfig(t)
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "missing.sock"))
	cfg.Supervisor = "herdr"
	if _, err := New(cfg, slog.Default()); err == nil {
		t.Fatal("missing Herdr socket accepted")
	}
	cfg.Supervisor = "direct"
	if err := os.Remove(filepath.Join(cfg.StateDir, "runner-id")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg, slog.Default()); err == nil {
		t.Fatal("missing runner ID accepted")
	}
	if err := WriteRunnerID(cfg.StateDir, "runner-1"); err != nil {
		t.Fatal(err)
	}
	lock, err := runlock.Acquire(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if _, err := New(cfg, slog.Default()); err == nil {
		t.Fatal("second daemon lock accepted")
	}
}

func TestSelectSupervisorModes(t *testing.T) {
	cfg := config.Config{StateDir: t.TempDir(), Supervisor: "direct"}
	sup, err := selectSupervisor(cfg, slog.Default())
	if err != nil || sup.Name() != "direct" {
		t.Fatalf("direct=%v err=%v", sup, err)
	}

	dir, err := os.MkdirTemp("/tmp", "daemon-herdr-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "herdr.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	t.Setenv("HERDR_SOCKET_PATH", socket)
	cfg.Supervisor = "auto"
	sup, err = selectSupervisor(cfg, slog.Default())
	if err != nil || sup.Name() != "herdr" {
		t.Fatalf("auto Herdr=%v err=%v", sup, err)
	}

	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(dir, "missing.sock"))
	t.Setenv("PATH", t.TempDir())
	cfg.Supervisor = "herdr"
	if _, err := selectSupervisor(cfg, slog.Default()); err == nil {
		t.Fatal("unavailable Herdr accepted")
	}
	cfg.Supervisor = "tmux"
	if _, err := selectSupervisor(cfg, slog.Default()); err == nil {
		t.Fatal("unavailable tmux accepted")
	}
	cfg.Supervisor = "auto"
	sup, err = selectSupervisor(cfg, slog.Default())
	if err != nil || sup.Name() != "direct" {
		t.Fatalf("auto fallback=%v err=%v", sup, err)
	}
}
