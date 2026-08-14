package lease

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func testLease(token uint64) Lease {
	return Lease{
		LeaseID: "lease-1", JobID: "j1", AttemptID: "a1", Kind: v1.JobKindBuild,
		ManifestID: "manifest-1", ManifestDigest: strings.Repeat("a", 64), WorkflowRevision: 1,
		FencingToken: token, ExpiresAt: time.Now().Add(time.Hour),
	}
}

func TestAcquireValidates(t *testing.T) {
	m := NewManager()
	if err := m.Acquire(testLease(1)); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := m.Validate("a1", 1); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := m.Validate("a1", 2); err == nil {
		t.Fatal("stale fencing should fail")
	}
	if err := m.Validate("other", 1); err == nil {
		t.Fatal("wrong attempt should fail")
	}
}

func TestAcquireBusyAndFencingPreemption(t *testing.T) {
	m := NewManager()
	if err := m.Acquire(testLease(2)); err != nil {
		t.Fatal(err)
	}
	stale := testLease(1)
	stale.JobID, stale.AttemptID, stale.LeaseID, stale.ManifestID = "j2", "a2", "lease-2", "manifest-2"
	if err := m.Acquire(stale); err == nil {
		t.Fatal("lower fencing token must not preempt")
	}
	fresher := stale
	fresher.FencingToken = 3
	if err := m.Acquire(fresher); err != nil {
		t.Fatalf("higher fencing token should preempt: %v", err)
	}
	if m.Current().JobID != "j2" {
		t.Fatal("lease not preempted")
	}
}

func TestRelease(t *testing.T) {
	m := NewManager()
	if err := m.Acquire(testLease(1)); err != nil {
		t.Fatal(err)
	}
	if m.Release("other") {
		t.Fatal("release by wrong attempt should fail")
	}
	if !m.Release("a1") {
		t.Fatal("release failed")
	}
	if err := m.Validate("a1", 1); err == nil {
		t.Fatal("validate after release should fail")
	}
}

func TestExpiredLease(t *testing.T) {
	m := NewManager()
	expired := testLease(1)
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	if err := m.Acquire(expired); err != nil {
		t.Fatal(err)
	}
	if err := m.Validate("a1", 1); err == nil {
		t.Fatal("expired lease should fail validation")
	}
}

func TestAcquireRejectsLeaseWithoutManifestAuthority(t *testing.T) {
	m := NewManager()
	if err := m.Acquire(Lease{JobID: "j1", AttemptID: "a1", FencingToken: 1, ExpiresAt: time.Now().Add(time.Hour)}); err == nil {
		t.Fatal("lease without immutable manifest authority was accepted")
	}
}

func TestRenewUntilRequiresOwnerAndFencingToken(t *testing.T) {
	m := NewManager()
	if err := m.Acquire(testLease(7)); err != nil {
		t.Fatal(err)
	}
	newExpiry := time.Now().Add(2 * time.Hour)
	if err := m.RenewUntil("other", 7, newExpiry); err == nil {
		t.Fatal("wrong attempt renewed lease")
	}
	if err := m.RenewUntil("a1", 6, newExpiry); err == nil {
		t.Fatal("stale fencing token renewed lease")
	}
	if err := m.RenewUntil("a1", 7, time.Now().Add(-time.Second)); err == nil {
		t.Fatal("past expiration renewed lease")
	}
	current := m.Current()
	if err := m.RenewUntil("a1", 7, current.ExpiresAt.Add(-time.Second)); err == nil {
		t.Fatal("decreasing expiration renewed lease")
	}
	if err := m.RenewUntil("a1", 7, current.ExpiresAt); err != nil {
		t.Fatalf("exact renewal replay was not idempotent: %v", err)
	}
	if got := m.Current(); !got.AcquiredAt.Equal(current.AcquiredAt) {
		t.Fatalf("exact renewal replay changed its window: before=%+v after=%+v", current, got)
	}
	beforeWindow := time.Now()
	if err := m.RenewUntil("a1", 7, newExpiry); err != nil {
		t.Fatalf("RenewUntil: %v", err)
	}
	if got := m.Current(); got == nil || !got.ExpiresAt.Equal(newExpiry) {
		t.Fatalf("renewed lease = %+v, want expiry %v", got, newExpiry)
	}
	if got := m.Current(); got.AcquiredAt.Before(beforeWindow) || got.AcquiredAt.After(time.Now()) {
		t.Fatalf("renewal window did not advance: %+v", got)
	}
}

func TestCurrentReturnsCopy(t *testing.T) {
	m := NewManager()
	if err := m.Acquire(testLease(1)); err != nil {
		t.Fatal(err)
	}
	current := m.Current()
	current.AttemptID = "tampered"
	if got := m.Current().AttemptID; got != "a1" {
		t.Fatalf("Current exposed mutable state: %q", got)
	}
}

func TestRenewDurationAndMaximumExpiry(t *testing.T) {
	m := NewManager()
	if m.Renew(time.Minute) {
		t.Fatal("missing lease renewed")
	}
	if err := m.Acquire(testLease(1)); err != nil {
		t.Fatal(err)
	}
	before := m.Current().ExpiresAt
	if !m.Renew(2*time.Hour) || !m.Current().ExpiresAt.After(before) {
		t.Fatal("duration renewal failed")
	}
	if err := m.RenewUntil("a1", 1, time.Now().Add(25*time.Hour)); err == nil {
		t.Fatal("renewal beyond 24 hours accepted")
	}
}
