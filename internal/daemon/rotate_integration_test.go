package daemon_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/enroll"
	"github.com/example/app-runner/internal/mockcp"
)

func rotateCommand(id string) v1.Envelope {
	return v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: id,
		SentAt: time.Now(), Payload: v1.CommandPayload{Command: v1.CmdRotateCert},
	}
}

func TestRotationViaCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	startMockControlPlane(t, cp)
	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	f := &rotationFixture{t: t, cp: cp, stateDir: stateDir, logs: &logBuffer{}}
	originalSPKI := f.currentKeySPKI()
	d := f.daemon(0)
	if err := d.SetTestLeaseActive(true); err != nil {
		t.Fatal(err)
	}
	runDaemon(t, d)

	waitFor(t, "runner connected", 10*time.Second, func() bool { return cp.HelloCount() >= 1 })
	hellosBefore := cp.HelloCount()
	command := rotateCommand("cmd-rotate-1")
	if err := cp.SendCommandEnvelope(context.Background(), command); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "activation of the new certificate", 15*time.Second, func() bool { return cp.Activations() == 1 })
	waitFor(t, "reconnect with new cert", 15*time.Second, func() bool { return cp.HelloCount() > hellosBefore })
	if f.currentKeySPKI() == originalSPKI {
		t.Fatal("operator rotation did not switch while a lease was active")
	}
	acks := cp.CommandAcks()
	if len(acks) != 1 || acks[0].InReplyTo != command.ID {
		t.Fatalf("command acks = %+v, want one ack for the rotation command", acks)
	}
	payload, ok := acks[0].Payload.(map[string]any)
	if !ok || payload["status"] != string(v1.CommandAckAccepted) {
		t.Fatalf("rotation ack payload = %#v, want accepted", acks[0].Payload)
	}
	if events := cp.EventsByKind("cert_rotated"); len(events) != 0 {
		t.Fatalf("runner sent %d cert_rotated events", len(events))
	}
	waitFor(t, "previous identity removed", 15*time.Second, func() bool { return !f.exists("previous-identity.json") })

	log := cp.StreamLog()
	sent := slices.IndexFunc(log, func(entry string) bool { return strings.HasSuffix(entry, ":command:"+command.ID) })
	if sent < 0 {
		t.Fatalf("stream log has no command entry: %v", log)
	}
	seq := strings.SplitN(log[sent], ":", 2)[0]
	ack := slices.Index(log, seq+":command_ack:"+command.ID)
	closed := slices.Index(log, seq+":closed")
	if ack < 0 || closed < 0 || ack > closed {
		t.Fatalf("rotation ack was not delivered on the commanding session before it closed: %v", log)
	}
}

func TestDuplicateRotationCommandIsAJournalReplay(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	startMockControlPlane(t, cp)
	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	f := &rotationFixture{t: t, cp: cp, stateDir: stateDir, logs: &logBuffer{}}
	runDaemon(t, f.daemon(0))
	waitFor(t, "runner connected", 10*time.Second, func() bool { return cp.HelloCount() >= 1 })
	command := rotateCommand("cmd-rotate-duplicate")
	if err := cp.SendCommandEnvelope(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "activation", 15*time.Second, func() bool { return cp.Activations() == 1 })
	waitFor(t, "reconnected session", 15*time.Second, func() bool {
		return cp.SendCommandEnvelope(context.Background(), rotateCommand(command.ID)) == nil
	})
	waitFor(t, "second ack", 15*time.Second, func() bool { return len(cp.CommandAcks()) >= 2 })
	time.Sleep(500 * time.Millisecond)
	if n := len(cp.RotateRequests()); n != 1 {
		t.Fatalf("duplicate delivery sent %d rotate requests, want 1", n)
	}
	if cp.Activations() != 1 {
		t.Fatal("duplicate delivery rotated again")
	}
}

func TestRotateRequiresTheActivatedCertificate(t *testing.T) {
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	startMockControlPlane(t, cp)
	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)

	d, err := daemon.New(directRunnerConfig(cp.WSURL(), stateDir), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.CloseForTest()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.TestRotate(ctx); err != nil {
		t.Fatalf("first rotation: %v", err)
	}
	err = d.TestRotate(ctx)
	var rejected *enroll.RotateRejectedError
	if !errors.As(err, &rejected) || rejected.Status != http.StatusUnauthorized {
		t.Fatalf("rotation with a certificate that never activated = %v, want 401", err)
	}
}

func TestRequestedRotationStartsOnlyAfterTheCommandIsAcknowledged(t *testing.T) {
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	startMockControlPlane(t, cp)
	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	f := &rotationFixture{t: t, cp: cp, stateDir: stateDir, logs: &logBuffer{}}
	d := f.daemon(0)
	defer d.CloseForTest()
	unlock := d.LockCommandsForTest()
	d.StartRequestedRotation(context.Background())
	time.Sleep(500 * time.Millisecond)
	requestsWhileHeld := len(cp.RotateRequests())
	unlock()
	if requestsWhileHeld != 0 {
		t.Fatalf("rotation sent %d requests before the command handler released", requestsWhileHeld)
	}
	d.WaitRequestedRotations()
	if n := len(cp.RotateRequests()); n != 1 || !f.exists("previous-identity.json") {
		t.Fatalf("rotation after the acknowledgement sent %d requests and did not switch", n)
	}
}

func TestSessionOnTheOldCertificateDoesNotConfirmTheSwitch(t *testing.T) {
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	startMockControlPlane(t, cp)
	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	f := &rotationFixture{t: t, cp: cp, stateDir: stateDir, logs: &logBuffer{}}
	gate := make(chan struct{})
	cp.SetStreamGate(gate)
	d := f.daemon(0)
	runDaemon(t, d)
	waitFor(t, "stream upgrade held with the old certificate", 10*time.Second, func() bool {
		return slices.Contains(cp.StreamLog(), "gate:waiting")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.TestRotate(ctx); err != nil {
		t.Fatalf("rotation: %v", err)
	}
	cp.SetStreamGate(nil)
	close(gate)
	waitFor(t, "session accepted on the old certificate", 10*time.Second, func() bool { return cp.HelloCount() >= 1 })
	time.Sleep(300 * time.Millisecond)
	if cp.Activations() != 0 {
		t.Fatal("test setup: the renewed certificate was already used")
	}
	if !f.exists("previous-identity.json") {
		t.Fatalf("a session on the old certificate confirmed the switch and removed previous-identity.json: %s %v", f.logs.String(), cp.StreamLog())
	}
}
