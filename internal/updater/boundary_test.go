package updater

import (
	"context"
	"errors"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/app-runner/internal/updateipc"
)

func TestRunProcessAndBoundedOutput(t *testing.T) {
	t.Parallel()
	if err := runProcess(context.Background(), ProcessCall{Path: "/usr/bin/true", Env: minimalEnvironment}); err != nil {
		t.Fatal(err)
	}
	if err := runProcess(context.Background(), ProcessCall{Path: "/usr/bin/false", Env: minimalEnvironment}); err == nil {
		t.Fatal("false succeeded")
	}
	if err := runProcess(context.Background(), ProcessCall{Path: "/usr/bin/true", DropPrivileges: true}); err == nil {
		t.Fatal("root credential drop accepted")
	}
	out := &boundedOutput{remaining: 2}
	if n, err := out.Write([]byte("abcd")); err != nil || n != 4 || string(out.data) != "ab" {
		t.Fatalf("bounded write n=%d data=%q err=%v", n, out.data, err)
	}
}

func TestSystemdControllerFixedCommands(t *testing.T) {
	t.Parallel()
	var calls []ProcessCall
	controller := &SystemdController{Timeout: time.Second, Run: func(_ context.Context, call ProcessCall) error { calls = append(calls, call); return nil }}
	if err := controller.Stop(context.Background(), DefaultRunnerService); err != nil {
		t.Fatal(err)
	}
	if err := controller.Start(context.Background(), DefaultRunnerService); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].Args[2] != "stop" || calls[1].Args[2] != "start" {
		t.Fatalf("calls=%+v", calls)
	}
	if err := controller.Start(context.Background(), "attacker.service"); err == nil {
		t.Fatal("arbitrary service accepted")
	}
	if _, err := controller.MainPID(context.Background(), "attacker.service"); err == nil {
		t.Fatal("arbitrary MainPID service accepted")
	}
	controller.Output = func(context.Context, string, []string, []string) ([]byte, error) { return []byte("42\n"), nil }
	if pid, err := controller.MainPID(context.Background(), DefaultRunnerService); err != nil || pid != 42 {
		t.Fatalf("pid=%d err=%v", pid, err)
	}
	controller.Output = func(context.Context, string, []string, []string) ([]byte, error) { return []byte("bad"), nil }
	if _, err := controller.MainPID(context.Background(), DefaultRunnerService); err == nil {
		t.Fatal("invalid pid accepted")
	}
}

func TestListenUnixStrictLifecycle(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("/tmp", "listen-updater-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "control.sock")
	ln, err := ListenUnix(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket info=%v err=%v", info, err)
	}
	ln.Close()
	_ = os.Remove(path)
	first, err := ListenUnix(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ListenUnix(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	second.Close()
	_ = os.Remove(path)
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenUnix(path, 0o600); err == nil {
		t.Fatal("regular socket target replaced")
	}
	if _, err := ListenUnix("/"+strings.Repeat("x", 101), 0o600); err == nil {
		t.Fatal("long socket path accepted")
	}
}

func TestServerInvalidRequestAndSanitizedReporting(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "server-boundary-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	rel := releaseFor(archive, 1, "1.0.0")
	core := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, &fakeService{}, &fakeClock{now: time.Now()})
	socket := filepath.Join(dir, "control.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var report string
	server := &Server{Coordinator: core, Peers: fixedPeer{peer: Peer{UID: 1, PID: 2}}, Authorizer: &allowPeer{}, Report: func(_ string, message string) { report = message }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, ln) }()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("not json"))
	_ = conn.(*net.UnixConn).CloseWrite()
	response, err := updateipc.DecodeResponse(conn)
	if err != nil {
		t.Fatal(err)
	}
	if response.OK || response.ErrorCode != "invalid_request" {
		t.Fatalf("response=%+v", response)
	}
	conn.Close()
	server.report("download", errors.New("Get https://example.test/a?secret=token failed"))
	if strings.Contains(report, "secret") || strings.Contains(report, "token") {
		t.Fatalf("report leaked query: %q", report)
	}
	cancel()
	<-done
	for err, want := range map[error]string{ErrAlreadyInstalled: "already_installed", ErrEquivocation: "equivocation", ErrTransactionMismatch: "transaction_mismatch", ErrNoPreparedUpdate: "not_prepared", ErrSuppressed: "release_suppressed", ErrRateLimited: "rate_limited", errors.New("x"): "update_failed"} {
		if got := errorCode(err); got != want {
			t.Fatalf("errorCode(%v)=%q", err, got)
		}
	}
	if firstError(nil, ErrNoPreparedUpdate) != ErrNoPreparedUpdate || firstError(ErrSuppressed, nil) != ErrSuppressed {
		t.Fatal("firstError")
	}
	_ = realClock{}.Now()
}

func TestServerStatusActivateAndHealthErrorResponses(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "server-actions-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	rel := releaseFor(archive, 1, "1.0.0")
	core := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, &fakeService{}, &fakeClock{now: time.Now()})
	socket := filepath.Join(dir, "control.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (&Server{Coordinator: core, Peers: fixedPeer{peer: Peer{UID: 1, PID: 2}}, Authorizer: &allowPeer{}}).Serve(ctx, ln)
	}()
	client := updateipc.NewClient(socket)
	status, err := client.Status(ctx, "status-1", "unknown-release")
	if err != nil || !status.OK || status.Phase != "idle" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	activation, err := client.Activate(ctx, "activate-1", "unknown-release")
	if err != nil || activation.OK || activation.ErrorCode != "not_prepared" {
		t.Fatalf("activation=%+v err=%v", activation, err)
	}
	health, err := client.Health(ctx, "health-1", "unknown-release", "1.0.0")
	if err != nil || health.OK || health.ErrorCode != "transaction_mismatch" {
		t.Fatalf("health=%+v err=%v", health, err)
	}
	cancel()
	<-done
}

func TestServerHealthDeadlineTimerRollsBackAndExits(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "server-timer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	rel := releaseFor(archive, 6, "6.0.0")
	activatedAt := time.Now()
	clock := &serveStartupClock{pending: activatedAt, expired: activatedAt.Add(time.Hour), pendingReads: math.MaxInt}
	core := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, &fakeService{}, clock)
	core.cfg.HealthTimeout = 30 * time.Millisecond
	if _, err := core.Prepare(context.Background(), rel.ReleaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Activate(context.Background(), rel.ReleaseID); err != nil {
		t.Fatal(err)
	}
	clock.expireAfter(serveStartupDeadlineReads)
	socket := filepath.Join(dir, "control.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- (&Server{Coordinator: core, Peers: fixedPeer{peer: Peer{UID: 1, PID: 2}}, Authorizer: &allowPeer{}}).Serve(context.Background(), ln)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	// Repository-wide race and integration suites can saturate filesystem sync
	// on shared CI hosts. The production deadline remains 30 ms in this test;
	// this bound only gives the resulting rollback enough time to persist.
	case <-time.After(10 * time.Second):
		t.Fatal("deadline timer did not exit")
	}
	state, err := core.loadState()
	if err != nil || state.Phase != PhaseIdle || state.FailedReleaseID != rel.ReleaseID || state.LastError != "health deadline expired" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if !clock.servedExpired() {
		t.Fatal("rollback happened before the deadline timer observed expiry")
	}
}

const serveStartupDeadlineReads = 2

type serveStartupClock struct {
	mu           sync.Mutex
	pending      time.Time
	expired      time.Time
	reads        int
	pendingReads int
}

func (c *serveStartupClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	if c.reads <= c.pendingReads {
		return c.pending
	}
	return c.expired
}

func (c *serveStartupClock) expireAfter(reads int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pendingReads = c.reads + reads
}

func (c *serveStartupClock) servedExpired() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads > c.pendingReads
}
