package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/lease"
	"github.com/example/app-runner/internal/validation"
)

func (d *Daemon) leaseRenewalClock() time.Time {
	if d.leaseRenewalNow != nil {
		return d.leaseRenewalNow()
	}
	return time.Now()
}

func (d *Daemon) leaseMonitorTick() time.Duration {
	if d.leaseMonitorInterval > 0 {
		return d.leaseMonitorInterval
	}
	return time.Second
}

func leaseRenewalDue(now time.Time, current lease.Lease) bool {
	window := current.ExpiresAt.Sub(current.AcquiredAt)
	if window <= 0 || !now.Before(current.ExpiresAt) {
		return false
	}
	return !now.Before(current.AcquiredAt.Add(window * 2 / 3))
}

func leaseRenewalEventID(current lease.Lease) string {
	material := fmt.Sprintf(
		"lease-renewal-v1\x00%s\x00%s\x00%s\x00%d\x00%d",
		current.LeaseID,
		current.AttemptID,
		current.ManifestDigest,
		current.FencingToken,
		current.ExpiresAt.UTC().UnixNano(),
	)
	digest := sha256.Sum256([]byte(material))
	return "lease-renewal-" + hex.EncodeToString(digest[:16])
}

func (jr *jobRunner) leaseRenewalEvent(observedAt time.Time) v1.LeaseRenewalEvent {
	return v1.LeaseRenewalEvent{
		Kind:           "lease_renewal",
		ManifestKind:   jr.job.Kind,
		LeaseID:        jr.job.LeaseID,
		JobID:          jr.job.JobID,
		AttemptID:      jr.job.AttemptID,
		ManifestID:     jr.job.ManifestID,
		ManifestDigest: jr.job.ManifestDigest,
		FencingToken:   jr.job.FencingToken,
		ObservedAt:     observedAt.UTC(),
	}
}

func (jr *jobRunner) requestLeaseRenewal(ctx context.Context, current lease.Lease, observedAt time.Time) error {
	event := jr.leaseRenewalEvent(observedAt)
	if current.LeaseID != event.LeaseID || current.JobID != event.JobID ||
		current.AttemptID != event.AttemptID || current.Kind != event.ManifestKind ||
		current.ManifestID != event.ManifestID || current.ManifestDigest != event.ManifestDigest ||
		current.FencingToken != event.FencingToken {
		return fmt.Errorf("current lease does not match immutable job authority")
	}
	if err := validation.LeaseRenewalEvent(event); err != nil {
		return err
	}
	_, err := jr.d.sendEventWithStableID(ctx, leaseRenewalEventID(current), event)
	return err
}
