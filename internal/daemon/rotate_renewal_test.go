package daemon_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/mockcp"
)

const thirtyDays = 30 * 24 * time.Hour

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type rotationFixture struct {
	t        *testing.T
	cp       *mockcp.Server
	stateDir string
	logs     *logBuffer
	start    time.Time
}

func newRotationFixture(t *testing.T, lifetime time.Duration) *rotationFixture {
	t.Helper()
	cp, err := mockcp.NewServerWithCertificateLifetime(lifetime)
	if err != nil {
		t.Fatal(err)
	}
	startMockControlPlane(t, cp)
	f := &rotationFixture{t: t, cp: cp, stateDir: t.TempDir(), logs: &logBuffer{}, start: time.Now()}
	enrollRunner(t, cp, f.stateDir)
	return f
}

func (f *rotationFixture) daemon(offset time.Duration) *daemon.Daemon {
	f.t.Helper()
	d, err := daemon.New(directRunnerConfig(f.cp.WSURL(), f.stateDir), slog.New(slog.NewTextHandler(f.logs, nil)))
	if err != nil {
		f.t.Fatal(err)
	}
	d.SetClock(func() time.Time { return time.Now().Add(offset) })
	return d
}

func (f *rotationFixture) file(name string) string { return filepath.Join(f.stateDir, name) }

func (f *rotationFixture) exists(name string) bool {
	_, err := os.Lstat(f.file(name))
	return err == nil
}

func (f *rotationFixture) currentKeySPKI() string {
	f.t.Helper()
	key, err := identity.NewStore(f.stateDir).LoadKey()
	if err != nil {
		f.t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		f.t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func (f *rotationFixture) pending() *identity.StoredIdentity {
	f.t.Helper()
	pending, err := identity.NewStore(f.stateDir).LoadPending()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return pending
}

func spkiOf(t *testing.T, stored *identity.StoredIdentity) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&stored.Key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func certNotAfter(t *testing.T, certPEM []byte) time.Time {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert.NotAfter
}

func (f *rotationFixture) currentNotAfter() time.Time {
	f.t.Helper()
	certPEM, _, err := identity.NewStore(f.stateDir).LoadCert()
	if err != nil {
		f.t.Fatal(err)
	}
	return certNotAfter(f.t, certPEM)
}

func TestRotationSwitchPolicy(t *testing.T) {
	t.Parallel()
	notAfter := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		remaining   time.Duration
		activeLease bool
		immediate   bool
		want        bool
	}{
		{"half-life, idle", 15 * 24 * time.Hour, false, false, true},
		{"half-life, active lease", 15 * 24 * time.Hour, true, false, false},
		{"just above 72h, active lease", 72*time.Hour + time.Minute, true, false, false},
		{"just below 72h, idle", 72*time.Hour - time.Minute, false, false, true},
		{"just below 72h, active lease", 72*time.Hour - time.Minute, true, false, true},
		{"expired, idle", -time.Hour, false, false, true},
		{"expired, active lease", -time.Hour, true, false, true},
		{"operator command, active lease", 15 * 24 * time.Hour, true, true, true},
	}
	for _, tc := range cases {
		if got := daemon.RotationSwitchDue(notAfter.Add(-tc.remaining), notAfter, tc.activeLease, tc.immediate); got != tc.want {
			t.Errorf("%s: RotationSwitchDue = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRenewalDoesNothingBeforeHalfLife(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	d := f.daemon(24 * time.Hour)
	defer d.CloseForTest()
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.cp.RotateRequests()); n != 0 || f.exists("pending-identity.json") {
		t.Fatalf("renewal before half-life sent %d requests", n)
	}
}

func TestRenewalWaitsForNoActiveLeaseAboveTheFloor(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	originalSPKI := f.currentKeySPKI()
	d := f.daemon(16 * 24 * time.Hour)
	defer d.CloseForTest()
	if err := d.SetTestLeaseActive(true); err != nil {
		t.Fatal(err)
	}
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatalf("renewal: %v", err)
	}
	pending := f.pending()
	if pending == nil || !pending.HasCertificate() {
		t.Fatal("renewal did not store a pending certificate")
	}
	if f.currentKeySPKI() != originalSPKI || f.exists("previous-identity.json") {
		t.Fatal("runner switched certificates while a lease was active above the 72 h floor")
	}
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.currentKeySPKI() != originalSPKI {
		t.Fatal("runner switched on a later tick while the lease was still active")
	}
	if err := d.SetTestLeaseActive(false); err != nil {
		t.Fatal(err)
	}
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatalf("renewal after the lease ended: %v", err)
	}
	if f.currentKeySPKI() != spkiOf(t, pending) {
		t.Fatal("runner did not switch to the pending certificate once idle")
	}
	if !f.exists("previous-identity.json") || f.exists("pending-identity.json") {
		t.Fatal("switch did not leave previous-identity.json and remove pending-identity.json")
	}
	if n := len(f.cp.RotateRequests()); n != 1 {
		t.Fatalf("rotate requests = %d, want 1 (the pending certificate is reused)", n)
	}
}

func TestRenewalSwitchesWithActiveLeaseBelowTheFloor(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	originalSPKI := f.currentKeySPKI()
	offset := time.Until(f.currentNotAfter().Add(-71 * time.Hour))
	d := f.daemon(offset)
	defer d.CloseForTest()
	if err := d.SetTestLeaseActive(true); err != nil {
		t.Fatal(err)
	}
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatalf("renewal: %v", err)
	}
	if f.currentKeySPKI() == originalSPKI {
		t.Fatal("runner did not switch with less than 72 h remaining")
	}
}

func TestRenewalLogsRotationCheckErrors(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	d := f.daemon(16 * 24 * time.Hour)
	defer d.CloseForTest()
	raw, err := os.ReadFile(f.file("identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.file("identity.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatalf("renewal returned %v for a check error; want it logged", err)
	}
	if !strings.Contains(f.logs.String(), "certificate rotation check failed") {
		t.Fatalf("ShouldRotate error was not logged: %s", f.logs.String())
	}
	if len(f.cp.RotateRequests()) != 0 {
		t.Fatal("renewal sent a request although the check failed")
	}
	if err := os.WriteFile(f.file("identity.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestObtainPersistsKeyBeforeRequestAndRetriesTransientFailuresWithTheSameKey(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	f.cp.FailNextRotation(http.StatusServiceUnavailable, "rotation_unavailable")
	f.cp.FailNextRotation(http.StatusBadGateway, "")
	d := f.daemon(16 * 24 * time.Hour)
	defer d.CloseForTest()
	for range 2 {
		if err := d.RenewCertificate(context.Background()); err == nil {
			t.Fatal("renewal succeeded against a failing endpoint")
		}
	}
	pending := f.pending()
	if pending == nil || pending.HasCertificate() {
		t.Fatal("pending key was not kept after a transient failure")
	}
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatalf("renewal: %v", err)
	}
	requests := f.cp.RotateRequests()
	if len(requests) != 3 {
		t.Fatalf("rotate requests = %d, want 3", len(requests))
	}
	for i, request := range requests {
		if request.SPKISHA256 != spkiOf(t, pending) {
			t.Fatalf("request %d used a different key than the persisted pending key", i)
		}
	}
	if f.currentKeySPKI() != spkiOf(t, pending) {
		t.Fatal("runner did not switch to the renewed certificate")
	}
}

func TestObtainDiscardsKeyOnlyWhenReused(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	f.cp.FailNextRotation(http.StatusConflict, "rotation_too_early")
	f.cp.FailNextRotation(http.StatusConflict, "rotation_key_reused")
	d := f.daemon(16 * 24 * time.Hour)
	defer d.CloseForTest()
	if err := d.SetTestLeaseActive(true); err != nil {
		t.Fatal(err)
	}
	err := d.RenewCertificate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rotation_too_early") {
		t.Fatalf("renewal error = %v, want the public code", err)
	}
	first := f.pending()
	if first == nil {
		t.Fatal("pending key was discarded after rotation_too_early")
	}
	if err := d.RenewCertificate(context.Background()); err == nil {
		t.Fatal("renewal succeeded against rotation_key_reused")
	}
	if f.pending() != nil {
		t.Fatal("pending key was kept after rotation_key_reused")
	}
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatalf("renewal with a new key: %v", err)
	}
	requests := f.cp.RotateRequests()
	if len(requests) != 3 || requests[0].SPKISHA256 != requests[1].SPKISHA256 || requests[2].SPKISHA256 == requests[1].SPKISHA256 {
		t.Fatalf("request keys = %+v; want the same key twice, then a new key", requests)
	}
	pending := f.pending()
	if pending == nil || !pending.HasCertificate() || spkiOf(t, pending) != requests[2].SPKISHA256 {
		t.Fatal("the new key's certificate was not stored as pending")
	}
	if issued, ok := f.cp.PendingCertificate(runnerIDOf(t, f.stateDir)); !ok || issued != string(pending.CertPEM) {
		t.Fatal("stored pending certificate differs from the issued certificate")
	}
}

func runnerIDOf(t *testing.T, stateDir string) string {
	t.Helper()
	id, err := daemon.ReadRunnerID(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

var errCrash = errors.New("simulated crash")

func crashAt(point string) func(string) error {
	return func(p string) error {
		if p == point {
			return errCrash
		}
		return nil
	}
}

func TestCrashAfterPendingKeyReusesTheKey(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	d := f.daemon(16 * 24 * time.Hour)
	d.SetRotationFault(crashAt(daemon.RotationFaultPendingKeySaved))
	if err := d.RenewCertificate(context.Background()); !errors.Is(err, errCrash) {
		t.Fatalf("renewal = %v, want the simulated crash", err)
	}
	d.CloseForTest()
	persisted := f.pending()
	if persisted == nil || len(f.cp.RotateRequests()) != 0 {
		t.Fatal("crash point 1 must leave a pending key and no request")
	}
	d = f.daemon(16 * 24 * time.Hour)
	defer d.CloseForTest()
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests := f.cp.RotateRequests()
	if len(requests) != 1 || requests[0].SPKISHA256 != spkiOf(t, persisted) {
		t.Fatal("restart did not reuse the persisted pending key")
	}
	if f.currentKeySPKI() != spkiOf(t, persisted) {
		t.Fatal("runner did not switch after resuming")
	}
}

func TestCrashAfterResponseReplaysTheSameCertificate(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	d := f.daemon(16 * 24 * time.Hour)
	d.SetRotationFault(crashAt(daemon.RotationFaultResponseReceived))
	if err := d.RenewCertificate(context.Background()); !errors.Is(err, errCrash) {
		t.Fatalf("renewal = %v, want the simulated crash", err)
	}
	d.CloseForTest()
	if pending := f.pending(); pending == nil || pending.HasCertificate() {
		t.Fatal("crash point 2 must leave a pending key without a certificate")
	}
	d = f.daemon(16 * 24 * time.Hour)
	defer d.CloseForTest()
	if err := d.SetTestLeaseActive(true); err != nil {
		t.Fatal(err)
	}
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests := f.cp.RotateRequests()
	if len(requests) != 2 || requests[0].SPKISHA256 != requests[1].SPKISHA256 || requests[0].Certificate != requests[1].Certificate {
		t.Fatal("restart did not resend the same key and receive the identical certificate")
	}
	if pending := f.pending(); pending == nil || string(pending.CertPEM) != requests[0].Certificate {
		t.Fatal("replayed certificate was not stored as pending")
	}
}

func TestCrashAfterCertificateSavedSwitchesOnPolicy(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	originalSPKI := f.currentKeySPKI()
	d := f.daemon(16 * 24 * time.Hour)
	d.SetRotationFault(crashAt(daemon.RotationFaultCertificateSaved))
	if err := d.RenewCertificate(context.Background()); !errors.Is(err, errCrash) {
		t.Fatalf("renewal = %v, want the simulated crash", err)
	}
	d.CloseForTest()
	d = f.daemon(16 * 24 * time.Hour)
	defer d.CloseForTest()
	if err := d.SetTestLeaseActive(true); err != nil {
		t.Fatal(err)
	}
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.currentKeySPKI() != originalSPKI {
		t.Fatal("resumed runner switched with an active lease above the floor")
	}
	if err := d.SetTestLeaseActive(false); err != nil {
		t.Fatal(err)
	}
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.currentKeySPKI() == originalSPKI || len(f.cp.RotateRequests()) != 1 {
		t.Fatal("resumed runner did not switch to the saved certificate without a new request")
	}
}

func runDaemon(t *testing.T, d *daemon.Daemon) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	t.Cleanup(stop)
	return stop
}

func TestCrashAfterPromotionStartsWithTheNewCertificate(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	d := f.daemon(16 * 24 * time.Hour)
	d.SetRotationFault(crashAt(daemon.RotationFaultPromoted))
	if err := d.RenewCertificate(context.Background()); !errors.Is(err, errCrash) {
		t.Fatalf("renewal = %v, want the simulated crash", err)
	}
	d.CloseForTest()
	promotedSPKI := f.currentKeySPKI()
	if !f.exists("previous-identity.json") {
		t.Fatal("crash point 4 must leave previous-identity.json")
	}
	d = f.daemon(0)
	runDaemon(t, d)
	waitFor(t, "activation with the new certificate", 15*time.Second, func() bool { return f.cp.Activations() == 1 })
	waitFor(t, "previous identity removed after hello_ack", 15*time.Second, func() bool { return !f.exists("previous-identity.json") })
	if f.currentKeySPKI() != promotedSPKI {
		t.Fatal("runner left the promoted certificate")
	}
}

func TestCrashAfterActivationRemovesPreviousOnlyAfterHelloAck(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	d := f.daemon(16 * 24 * time.Hour)
	d.SetRotationFault(crashAt(daemon.RotationFaultBeforeConfirm))
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.SetClock(time.Now)
	stop := runDaemon(t, d)
	waitFor(t, "activation", 15*time.Second, func() bool { return f.cp.Activations() == 1 && f.cp.HelloCount() >= 1 })
	time.Sleep(200 * time.Millisecond)
	stop()
	if !f.exists("previous-identity.json") {
		t.Fatal("crash point 5 must leave previous-identity.json after activation")
	}
	dropThrough := f.cp.HelloCount() + 2
	f.cp.SetDropOnHello(dropThrough)
	d = f.daemon(0)
	runDaemon(t, d)
	waitFor(t, "two dropped hellos", 15*time.Second, func() bool { return f.cp.HelloCount() >= dropThrough })
	if !f.exists("previous-identity.json") {
		t.Fatal("previous-identity.json was deleted before any hello_ack")
	}
	waitFor(t, "previous identity removed after hello_ack", 20*time.Second, func() bool { return !f.exists("previous-identity.json") })
}

func TestRefusedActivationFallsBackToThePreviousCertificate(t *testing.T) {
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	startMockControlPlane(t, cp)
	cp.SetRefuseActivation(true)
	f := &rotationFixture{t: t, cp: cp, stateDir: t.TempDir(), logs: &logBuffer{}}
	enrollRunner(t, cp, f.stateDir)
	originalSPKI := f.currentKeySPKI()
	d := f.daemon(13 * time.Hour)
	runDaemon(t, d)
	waitFor(t, "accepted session on the previous certificate", 20*time.Second, func() bool { return cp.HelloCount() >= 1 })
	if cp.StreamRejections() < 1 || len(cp.RotateRequests()) != 1 {
		t.Fatalf("rejections = %d, requests = %d; want the renewed certificate refused once", cp.StreamRejections(), len(cp.RotateRequests()))
	}
	if f.currentKeySPKI() != originalSPKI {
		t.Fatal("runner did not restore the previous certificate")
	}
	if f.exists("previous-identity.json") || f.exists("pending-identity.json") {
		t.Fatal("fallback left rotation files behind")
	}
	if !strings.Contains(f.logs.String(), "certificate rotation was not accepted; continuing with the previous certificate") {
		t.Fatalf("fallback was not logged: %s", f.logs.String())
	}
	cp.SetRefuseActivation(false)
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatalf("renewal on the next tick: %v", err)
	}
	requests := cp.RotateRequests()
	if len(requests) != 2 || requests[1].SPKISHA256 == requests[0].SPKISHA256 {
		t.Fatal("renewal after fallback did not request a certificate for a new key")
	}
	waitFor(t, "activation of the second renewal", 20*time.Second, func() bool { return cp.Activations() == 1 })
}

func TestOperatorRotationRetriesATransientFailureOnTheNextTickBeforeHalfLife(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	f.cp.FailNextRotation(http.StatusServiceUnavailable, "rotation_unavailable")
	d := f.daemon(24 * time.Hour)
	defer d.CloseForTest()
	if err := d.TestRotate(context.Background()); err == nil {
		t.Fatal("operator rotation succeeded against a 503")
	}
	pending := f.pending()
	if pending == nil || pending.HasCertificate() {
		t.Fatal("operator rotation did not keep the pending key after a 503")
	}
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatalf("next tick: %v", err)
	}
	requests := f.cp.RotateRequests()
	if len(requests) != 2 {
		t.Fatalf("rotate requests = %d, want the operator rotation retried on the next tick", len(requests))
	}
	for i, request := range requests {
		if request.SPKISHA256 != spkiOf(t, pending) {
			t.Fatalf("request %d used a different key than the persisted pending key", i)
		}
	}
	if f.currentKeySPKI() != spkiOf(t, pending) {
		t.Fatal("runner did not switch to the renewed certificate after the retry")
	}
}

func TestOperatorRotationCrashAfterPendingKeyResumesBeforeHalfLife(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	d := f.daemon(24 * time.Hour)
	d.SetRotationFault(crashAt(daemon.RotationFaultPendingKeySaved))
	if err := d.TestRotate(context.Background()); !errors.Is(err, errCrash) {
		t.Fatalf("operator rotation = %v, want the simulated crash", err)
	}
	d.CloseForTest()
	persisted := f.pending()
	if persisted == nil || len(f.cp.RotateRequests()) != 0 {
		t.Fatal("crash point 1 must leave a pending key and no request")
	}
	d = f.daemon(24 * time.Hour)
	defer d.CloseForTest()
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests := f.cp.RotateRequests()
	if len(requests) != 1 || requests[0].SPKISHA256 != spkiOf(t, persisted) {
		t.Fatalf("rotate requests = %d; want the interrupted operator rotation resumed with the persisted key", len(requests))
	}
	if f.currentKeySPKI() != spkiOf(t, persisted) {
		t.Fatal("runner did not switch after resuming the operator rotation")
	}
}

func TestFallbackKeepsTheRenewedCertificateWhenThePreviousHasExpired(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	originalSPKI := f.currentKeySPKI()
	d := f.daemon(16 * 24 * time.Hour)
	defer d.CloseForTest()
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatal(err)
	}
	renewedSPKI := f.currentKeySPKI()
	if renewedSPKI == originalSPKI || !d.AwaitingRotationConfirmation() {
		t.Fatal("renewal did not switch to a certificate awaiting confirmation")
	}
	d.SetClock(func() time.Time { return time.Now().Add(31 * 24 * time.Hour) })
	d.FallBackAfterUnauthorizedUpgrade()
	if f.currentKeySPKI() != renewedSPKI {
		t.Fatal("runner restored a previous certificate that has expired")
	}
	logs := f.logs.String()
	if !strings.Contains(logs, "renewed certificate was refused and the previous certificate is no longer valid") {
		t.Fatalf("expired previous certificate was not logged: %s", logs)
	}
	if strings.Contains(logs, "continuing with the previous certificate") {
		t.Fatal("fallback reported continuing with an expired previous certificate")
	}
}

func TestFallbackHappensAtMostOnce(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	originalSPKI := f.currentKeySPKI()
	d := f.daemon(16 * 24 * time.Hour)
	defer d.CloseForTest()
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.currentKeySPKI() == originalSPKI || !d.AwaitingRotationConfirmation() {
		t.Fatal("renewal did not switch to a certificate awaiting confirmation")
	}
	d.FallBackAfterUnauthorizedUpgrade()
	if f.currentKeySPKI() != originalSPKI || f.exists("previous-identity.json") {
		t.Fatal("first 401 did not restore the previous certificate")
	}
	if d.AwaitingRotationConfirmation() {
		t.Fatal("fallback left the renewed certificate armed for another fallback")
	}
	d.FallBackAfterUnauthorizedUpgrade()
	if f.currentKeySPKI() != originalSPKI {
		t.Fatal("second 401 changed the restored certificate")
	}
	logs := f.logs.String()
	if n := strings.Count(logs, "certificate rotation was not accepted; continuing with the previous certificate"); n != 1 {
		t.Fatalf("fallback logged %d times, want once", n)
	}
	if strings.Contains(logs, "no previous certificate is available") {
		t.Fatal("second 401 attempted another fallback")
	}
}

func TestInvalidPendingCertificateIsDiscarded(t *testing.T) {
	f := newRotationFixture(t, thirtyDays)
	originalSPKI := f.currentKeySPKI()
	d := f.daemon(16 * 24 * time.Hour)
	defer d.CloseForTest()
	if err := d.SetTestLeaseActive(true); err != nil {
		t.Fatal(err)
	}
	if err := d.RenewCertificate(context.Background()); err != nil {
		t.Fatal(err)
	}
	stale := f.pending()
	if stale == nil || !stale.HasCertificate() {
		t.Fatal("renewal did not store a pending certificate")
	}
	d.SetClock(func() time.Time { return certNotAfter(t, stale.CertPEM).Add(time.Hour) })
	_ = d.RenewCertificate(context.Background())
	if f.currentKeySPKI() != originalSPKI {
		t.Fatal("runner switched to an expired pending certificate")
	}
	if pending := f.pending(); pending != nil && spkiOf(t, pending) == spkiOf(t, stale) {
		t.Fatal("expired pending certificate was kept")
	}
	if !strings.Contains(f.logs.String(), "pending runner certificate is no longer valid") {
		t.Fatalf("discarding the expired pending certificate was not logged: %s", f.logs.String())
	}
}
