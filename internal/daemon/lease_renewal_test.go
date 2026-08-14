package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/lease"
)

func TestLeaseRenewalDueUsesTheLatterThirdOfTheCurrentWindow(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	current := lease.Lease{AcquiredAt: start, ExpiresAt: start.Add(60 * time.Second)}
	if leaseRenewalDue(start.Add(39*time.Second), current) {
		t.Fatal("renewal was due before the latter third")
	}
	if !leaseRenewalDue(start.Add(40*time.Second), current) {
		t.Fatal("renewal was not due at the latter-third boundary")
	}
	if leaseRenewalDue(current.ExpiresAt, current) {
		t.Fatal("expired lease was considered renewable")
	}
}

func TestLeaseRenewalRequestIsEmittedOncePerAuthorityWindow(t *testing.T) {
	jr, events := testJobRunner(t)
	current := jr.d.leases.Current()
	if current == nil {
		t.Fatal("expected active lease")
	}
	observedAt := time.Now().UTC()
	if err := jr.requestLeaseRenewal(context.Background(), *current, observedAt); err != nil {
		t.Fatal(err)
	}
	if err := jr.requestLeaseRenewal(context.Background(), *current, observedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := len(events.byKind("lease_renewal")); got != 1 {
		t.Fatalf("same authority window emitted %d renewal requests, want 1", got)
	}

	if err := jr.d.leases.RenewUntil(
		current.AttemptID,
		current.FencingToken,
		current.ExpiresAt.Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	next := jr.d.leases.Current()
	if next == nil {
		t.Fatal("expected renewed lease")
	}
	if err := jr.requestLeaseRenewal(context.Background(), *next, observedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := len(events.byKind("lease_renewal")); got != 2 {
		t.Fatalf("new authority window emitted %d total renewal requests, want 2", got)
	}
}

func TestLeaseRenewalEventAndStableIDBindTheCurrentWindow(t *testing.T) {
	t.Parallel()
	job := withTestAuthority(v1.JobSpec{
		JobID: "job-renew", AttemptID: "attempt-renew", Kind: v1.JobKindBuild, FencingToken: 17,
	})
	jr := &jobRunner{job: job}
	observedAt := time.Date(2026, time.August, 14, 12, 0, 40, 0, time.UTC)
	current := lease.Lease{
		LeaseID: job.LeaseID, JobID: job.JobID, AttemptID: job.AttemptID, Kind: job.Kind,
		ManifestID: job.ManifestID, ManifestDigest: job.ManifestDigest,
		FencingToken: job.FencingToken, AcquiredAt: observedAt.Add(-40 * time.Second),
		ExpiresAt: observedAt.Add(20 * time.Second),
	}
	event := jr.leaseRenewalEvent(observedAt)
	if event.LeaseID != current.LeaseID || event.ManifestID != current.ManifestID ||
		event.ManifestDigest != strings.Repeat("a", 64) || event.ObservedAt != observedAt {
		t.Fatalf("renewal event = %#v", event)
	}
	first := leaseRenewalEventID(current)
	if first != leaseRenewalEventID(current) {
		t.Fatal("same lease window produced a different event ID")
	}
	current.ExpiresAt = current.ExpiresAt.Add(time.Minute)
	if first == leaseRenewalEventID(current) {
		t.Fatal("new lease window reused the prior event ID")
	}
}
