package updater

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/app-runner/internal/updateipc"
)

type allowPeer struct {
	seen []updateipc.Action
	err  error
}

func (a *allowPeer) Authorize(_ context.Context, _ Peer, action updateipc.Action) error {
	a.seen = append(a.seen, action)
	return a.err
}

type fixedPeer struct {
	peer Peer
	err  error
}

func (f fixedPeer) Peer(net.Conn) (Peer, error) { return f.peer, f.err }

func TestServerUsesSharedBoundedProtocolAndAsyncActivation(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "updater-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	rel := releaseFor(archive, 1, "1.0.0")
	svc := &fakeService{}
	clock := &fakeClock{now: time.Now()}
	c := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, svc, clock)
	socket := filepath.Join(dir, "control.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &allowPeer{}
	server := &Server{Coordinator: c, Peers: fixedPeer{peer: Peer{UID: 1000, PID: 42}}, Authorizer: authorizer, IOTimeout: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, ln) }()
	client := updateipc.NewClient(socket)
	prepared, err := client.Prepare(ctx, "request-1", rel.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.OK || prepared.Phase != string(PhasePrepared) {
		t.Fatalf("prepare=%+v", prepared)
	}
	activated, err := client.Activate(ctx, "request-2", rel.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	if !activated.OK || activated.Phase != "activating" {
		t.Fatalf("activate=%+v", activated)
	}
	deadline := time.Now().Add(time.Second)
	for len(svc.Calls()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(svc.Calls()) < 2 {
		t.Fatal("activation did not run asynchronously")
	}
	health, err := client.Health(ctx, "request-3", rel.ReleaseID, rel.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !health.OK || health.Phase != "healthy" {
		t.Fatalf("health=%+v", health)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not exit after terminal transaction")
	}
	_ = os.Remove(socket)
	ln2, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- server.Serve(ctx2, ln2) }()
	reconciled, err := client.Status(ctx2, "request-4", rel.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	if !reconciled.OK || reconciled.Phase != "healthy" || reconciled.UpdateID != rel.ReleaseID {
		t.Fatalf("reconcile=%+v", reconciled)
	}
	cancel2()
	select {
	case <-done2:
	case <-time.After(time.Second):
		t.Fatal("reconcile server did not stop")
	}
}

func TestRunnerAuthorizerRequiresUIDAndExactSystemdMainPID(t *testing.T) {
	t.Parallel()
	a := &RunnerAuthorizer{ExpectedUID: 123, MainPID: func(context.Context) (int, error) { return 456, nil }}
	for _, peer := range []Peer{{UID: 1, PID: 456}, {UID: 123, PID: 1}} {
		if err := a.Authorize(context.Background(), peer, updateipc.ActionHealth); err == nil {
			t.Fatalf("authorized %+v", peer)
		}
	}
	if err := a.Authorize(context.Background(), Peer{UID: 123, PID: 456}, updateipc.ActionHealth); err != nil {
		t.Fatal(err)
	}
	if err := (&RunnerAuthorizer{}).Authorize(context.Background(), Peer{}, updateipc.ActionStatus); err == nil {
		t.Fatal("unconfigured authorizer accepted")
	}
	broken := &RunnerAuthorizer{ExpectedUID: 123, MainPID: func(context.Context) (int, error) { return 0, errors.New("systemd") }}
	if err := broken.Authorize(context.Background(), Peer{UID: 123, PID: 1}, updateipc.ActionStatus); err == nil {
		t.Fatal("MainPID failure ignored")
	}
}

func TestServerReturnsAsyncActivationFailure(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "updater-fail-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	rel := releaseFor(archive, 7, "7.0.0")
	svc := &fakeService{startErr: errors.New("start failed")}
	core := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, svc, &fakeClock{now: time.Now()})
	socket := filepath.Join(dir, "control.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Coordinator: core, Peers: fixedPeer{peer: Peer{UID: 1, PID: 2}}, Authorizer: &allowPeer{}}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), ln) }()
	client := updateipc.NewClient(socket)
	if _, err := client.Prepare(context.Background(), "request-a", rel.ReleaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Activate(context.Background(), "request-b", rel.ReleaseID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("async activation failure returned nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not report activation failure")
	}
}
