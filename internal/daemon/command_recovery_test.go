package daemon

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
)

func persistUnexecutedCommand(t *testing.T, d *Daemon, envelope v1.Envelope, owner string) {
	t.Helper()
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := commandContentDigest(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := d.journal.ReceiveCommand(context.Background(), journal.Record{
		ID: envelope.ID, Kind: journal.KindCommand, Payload: raw, CreatedAt: time.Now(),
	}, digest); err != nil || !created {
		t.Fatalf("receive command = %v, %v", created, err)
	}
	if owner != "" {
		if _, claimed, err := d.journal.ClaimCommand(context.Background(), envelope.ID, owner); err != nil || !claimed {
			t.Fatalf("claim command = %v, %v", claimed, err)
		}
	}
}

func commandReceipt(t *testing.T, d *Daemon, envelope v1.Envelope) journal.CommandReceipt {
	t.Helper()
	digest, err := commandContentDigest(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	receipt, _, err := d.journal.ReceiveCommand(context.Background(), journal.Record{
		ID: envelope.ID, Kind: journal.KindCommand, Payload: []byte("unused"), CreatedAt: time.Now(),
	}, digest)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestRedeliveredReceivedCommandExecutesInsteadOfBecomingDuplicate(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.commandOwner = "process-new"
	if err := os.Chmod(d.cfg.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	d.paused.Store(true)
	envelope := commandEnvelope("resume-after-crash", v1.CommandPayload{Command: v1.CmdResumeRunner})
	persistUnexecutedCommand(t, d, envelope, "")

	if err := d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if d.Paused() {
		t.Fatal("durably received command was not executed on redelivery")
	}
	receipt := commandReceipt(t, d, envelope)
	if receipt.State != journal.CommandAccepted || receipt.AckStatus != "accepted" {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestNewProcessReclaimsCommandAfterEffectBeforeCompletion(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.commandOwner = "process-new"
	if err := os.Chmod(d.cfg.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	d.paused.Store(false) // The prior process applied the resume effect.
	envelope := commandEnvelope("resume-after-effect", v1.CommandPayload{Command: v1.CmdResumeRunner})
	persistUnexecutedCommand(t, d, envelope, "process-old")

	if err := d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	receipt := commandReceipt(t, d, envelope)
	if receipt.State != journal.CommandAccepted || receipt.AckStatus != "accepted" {
		t.Fatalf("recovered receipt = %+v", receipt)
	}
}

func TestCompletedCommandReplaysOriginalRejectionAndRejectsChangedReuse(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.commandOwner = "process-new"
	rejected := commandEnvelope("rejected-command", v1.CommandPayload{Command: v1.CmdStartJob})
	if err := d.OnCommand(context.Background(), rejected); err != nil {
		t.Fatal(err)
	}
	first := commandReceipt(t, d, rejected)
	if first.State != journal.CommandRejected || first.AckStatus != "rejected" ||
		!strings.Contains(first.AckError, "missing job spec") {
		t.Fatalf("first rejection = %+v", first)
	}
	rejected.SentAt = time.Now().Add(time.Second)
	if err := d.OnCommand(context.Background(), rejected); err != nil {
		t.Fatal(err)
	}
	if replayed := commandReceipt(t, d, rejected); replayed != first {
		t.Fatalf("terminal replay = %+v, want %+v", replayed, first)
	}

	changed := rejected
	changed.SentAt = time.Now()
	changed.Payload = v1.CommandPayload{Command: v1.CmdEmergencyStop}
	if err := d.OnCommand(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	if d.Paused() {
		t.Fatal("changed command-ID reuse applied a different effect")
	}
}

func TestRenewalRecoveryAcceptsAlreadyPersistedExactExpiry(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.commandOwner = "process-new"
	current := d.leases.Current()
	if current == nil {
		t.Fatal("expected lease")
	}
	expiresAt := current.ExpiresAt.Add(time.Hour).Truncate(time.Second)
	if err := d.leases.RenewUntil(current.AttemptID, current.FencingToken, expiresAt); err != nil {
		t.Fatal(err)
	}
	if err := d.journal.SaveLease(context.Background(), *d.leases.Current()); err != nil {
		t.Fatal(err)
	}
	envelope := commandEnvelope("renew-after-effect", v1.CommandPayload{
		Command: v1.CmdRenewLease, AttemptID: current.AttemptID,
		FencingToken: current.FencingToken, LeaseExpiresAt: expiresAt,
	})
	persistUnexecutedCommand(t, d, envelope, "process-old")

	if err := d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if receipt := commandReceipt(t, d, envelope); receipt.State != journal.CommandAccepted {
		t.Fatalf("renewal recovery = %+v", receipt)
	}
}

func TestStartRecoveryRetainsReconciliationInsteadOfRejectingAppliedCommand(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.commandOwner = "process-new"
	current := d.leases.Current()
	if current == nil {
		t.Fatal("expected lease")
	}
	if err := d.journal.SaveLease(context.Background(), *current); err != nil {
		t.Fatal(err)
	}
	d.recoveredLease.Store(true)
	job := jr.job
	job.LeaseExpiresAt = current.ExpiresAt
	job.Repositories = []v1.JobRepository{{RepositoryID: "repo-1", Primary: true}}
	job.Ticket = v1.TicketContext{Key: "PROJ-1", Summary: "Recovered start"}
	envelope := commandEnvelope("start-after-effect", v1.CommandPayload{Command: v1.CmdStartJob, Job: &job})
	persistUnexecutedCommand(t, d, envelope, "process-old")

	if err := d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if receipt := commandReceipt(t, d, envelope); receipt.State != journal.CommandAccepted {
		t.Fatalf("start recovery = %+v", receipt)
	}
	if d.activeJob != nil || !d.recoveredLease.Load() {
		t.Fatal("recovered start incorrectly launched work or cleared reconciliation")
	}
}

func TestStartRecoveryRejectsMismatchedImmutableAuthority(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.commandOwner = "process-new"
	d.recoveredLease.Store(true)
	current := d.leases.Current()
	if current == nil {
		t.Fatal("expected lease")
	}
	job := jr.job
	job.LeaseExpiresAt = current.ExpiresAt
	job.ManifestDigest = strings.Repeat("b", 64)
	job.Repositories = []v1.JobRepository{{RepositoryID: "repo-1", Primary: true}}
	job.Ticket = v1.TicketContext{Key: "PROJ-1", Summary: "Mismatched start"}
	envelope := commandEnvelope("start-mismatched-authority", v1.CommandPayload{Command: v1.CmdStartJob, Job: &job})
	persistUnexecutedCommand(t, d, envelope, "process-old")

	if err := d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	receipt := commandReceipt(t, d, envelope)
	if receipt.State != journal.CommandRejected || !strings.Contains(receipt.AckError, "runner busy") {
		t.Fatalf("mismatched authority was mistaken for an applied command: %+v", receipt)
	}
}

func TestTerminalUpdateReplayRestoresAcknowledgementGate(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	d.commandOwner = "process-new"
	secureUpdateTestState(t, d)
	command := updateCommand("update-replayed")
	envelope := commandEnvelope("update-after-outcome", command)
	if err := d.beginUpdateDrain(envelope.ID, command, time.Now()); err != nil {
		t.Fatal(err)
	}
	persistUnexecutedCommand(t, d, envelope, "process-old")
	if _, err := d.journal.CompleteCommand(context.Background(), envelope.ID, "process-old", "accepted", ""); err != nil {
		t.Fatal(err)
	}

	if err := d.OnCommand(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	d.jobMu.Lock()
	pending := d.updateDrain
	d.jobMu.Unlock()
	if pending == nil || !pending.AckAttempted {
		t.Fatalf("terminal update replay did not restore acknowledgement gate: %+v", pending)
	}
}

func TestCommandJournalFailureClosesTransportForRedelivery(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	if err := d.journal.Close(); err != nil {
		t.Fatal(err)
	}
	err := d.OnCommand(context.Background(), commandEnvelope(
		"retry-after-storage-recovery",
		v1.CommandPayload{Command: v1.CmdResumeRunner},
	))
	if err == nil || !strings.Contains(err.Error(), "durably receive command") {
		t.Fatalf("journal failure did not request transport redelivery: %v", err)
	}
}
