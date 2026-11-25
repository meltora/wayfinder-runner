package daemon

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/lease"
	"github.com/example/app-runner/internal/transport"
)

func ApprovedPlanFixture(revision int) *v1.ApprovedPlan { return testApprovedPlan(revision) }

func (d *Daemon) TestRotate(ctx context.Context) error { return d.rotateCert(ctx) }

func (d *Daemon) SetClock(now func() time.Time) { d.now = now }

func (d *Daemon) RenewCertificate(ctx context.Context) error { return d.renewCertificate(ctx, false) }

func (d *Daemon) FallBackAfterUnauthorizedUpgrade() {
	d.fallBackFromRejectedRotation(&transport.UpgradeRejectedError{StatusCode: http.StatusUnauthorized})
}

func (d *Daemon) AwaitingRotationConfirmation() bool { return d.promotedTLS.Load() != nil }

func (d *Daemon) SetRotationFault(fault func(point string) error) { d.rotationFault = fault }

func (d *Daemon) StartRequestedRotation(ctx context.Context) { d.startRequestedRotation(ctx) }

func (d *Daemon) WaitRequestedRotations() { d.rotationCommands.Wait() }

func (d *Daemon) JournalEventForTest(ctx context.Context, eventID string, payload any) ([]byte, error) {
	if _, err := d.sendEventWithStableID(ctx, eventID, payload); err != nil {
		return nil, err
	}
	record, found, err := d.journal.LoadRecord(ctx, eventID)
	if err != nil || !found {
		return nil, fmt.Errorf("journaled event %s found=%v: %w", eventID, found, err)
	}
	return record.Payload, nil
}

func (d *Daemon) LockCommandsForTest() func() {
	d.commandMu.Lock()
	return d.commandMu.Unlock
}

func (d *Daemon) CloseForTest() {
	_ = d.journal.Close()
	_ = d.runLock.Close()
}

func (d *Daemon) SetTestLeaseActive(active bool) error {
	if !active {
		d.leases.Release("attempt-rotation")
		return nil
	}
	return d.leases.Acquire(lease.Lease{
		LeaseID: "lease-rotation", JobID: "job-rotation", AttemptID: "attempt-rotation", Kind: v1.JobKindBuild,
		ManifestID: "manifest-rotation", ManifestDigest: strings.Repeat("a", 64), WorkflowRevision: 1,
		FencingToken: 1, ExpiresAt: time.Now().Add(time.Hour),
	})
}

var RotationSwitchDue = rotationSwitchDue

const (
	RotationFaultPendingKeySaved  = rotationFaultPendingKeySaved
	RotationFaultResponseReceived = rotationFaultResponseReceived
	RotationFaultCertificateSaved = rotationFaultCertificateSaved
	RotationFaultPromoted         = rotationFaultPromoted
	RotationFaultBeforeConfirm    = rotationFaultBeforeConfirm
)
