package lease

import (
	"testing"
	"time"
)

func testLease(token uint64) Lease {
	return Lease{JobID: "j1", AttemptID: "a1", FencingToken: token, ExpiresAt: time.Now().Add(time.Hour)}
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
	if err := m.Acquire(Lease{JobID: "j2", AttemptID: "a2", FencingToken: 1, ExpiresAt: time.Now().Add(time.Hour)}); err == nil {
		t.Fatal("lower fencing token must not preempt")
	}
	if err := m.Acquire(Lease{JobID: "j2", AttemptID: "a2", FencingToken: 3, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
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
	if err := m.Acquire(Lease{JobID: "j1", AttemptID: "a1", FencingToken: 1, ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := m.Validate("a1", 1); err == nil {
		t.Fatal("expired lease should fail validation")
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
	if err := m.RenewUntil("a1", 7, newExpiry); err != nil {
		t.Fatalf("RenewUntil: %v", err)
	}
	if got := m.Current(); got == nil || !got.ExpiresAt.Equal(newExpiry) {
		t.Fatalf("renewed lease = %+v, want expiry %v", got, newExpiry)
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
