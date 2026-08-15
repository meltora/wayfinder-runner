package journal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func commandRecord(id string) Record {
	return Record{
		ID: id, Kind: KindCommand, Payload: []byte(`{"command":"resume_runner"}`), CreatedAt: time.Now(),
	}
}

func legacyCommandRecord(t *testing.T, id string, payload any) (Record, string) {
	t.Helper()
	envelope := v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: id, SentAt: time.Now(), Payload: payload,
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	return Record{ID: id, Kind: KindCommand, Payload: raw, CreatedAt: time.Now()}, hex.EncodeToString(digest[:])
}

func TestCommandReceiptClaimsRecoverAndReplayTerminalOutcome(t *testing.T) {
	t.Parallel()
	j := openTest(t)
	ctx := context.Background()
	digest := strings.Repeat("a", 64)
	receipt, created, err := j.ReceiveCommand(ctx, commandRecord("command-1"), digest)
	if err != nil || !created || receipt.State != CommandReceived {
		t.Fatalf("receive = %+v, %v, %v", receipt, created, err)
	}
	receipt, created, err = j.ReceiveCommand(ctx, commandRecord("command-1"), digest)
	if err != nil || created || receipt.State != CommandReceived {
		t.Fatalf("duplicate receive = %+v, %v, %v", receipt, created, err)
	}
	if _, _, err := j.ReceiveCommand(ctx, commandRecord("command-1"), strings.Repeat("b", 64)); !errors.Is(err, ErrCommandContentConflict) {
		t.Fatalf("changed content = %v, want ErrCommandContentConflict", err)
	}

	receipt, claimed, err := j.ClaimCommand(ctx, "command-1", "process-1")
	if err != nil || !claimed || receipt.State != CommandProcessing || receipt.ClaimOwner != "process-1" {
		t.Fatalf("first claim = %+v, %v, %v", receipt, claimed, err)
	}
	if receipt, claimed, err = j.ClaimCommand(ctx, "command-1", "process-1"); err != nil || claimed || receipt.ClaimOwner != "process-1" {
		t.Fatalf("concurrent same-process claim = %+v, %v, %v", receipt, claimed, err)
	}
	receipt, claimed, err = j.ClaimCommand(ctx, "command-1", "process-2")
	if err != nil || !claimed || receipt.ClaimOwner != "process-2" {
		t.Fatalf("restart claim = %+v, %v, %v", receipt, claimed, err)
	}
	if _, err := j.CompleteCommand(ctx, "command-1", "process-1", "accepted", ""); !errors.Is(err, ErrCommandClaimConflict) {
		t.Fatalf("stale owner completion = %v, want ErrCommandClaimConflict", err)
	}
	receipt, err = j.CompleteCommand(ctx, "command-1", "process-2", "rejected", v1.CommandAckReasonRunnerPaused)
	if err != nil || receipt.State != CommandRejected || receipt.AckStatus != "rejected" || receipt.AckReasonCode != v1.CommandAckReasonRunnerPaused {
		t.Fatalf("completion = %+v, %v", receipt, err)
	}
	if records, err := j.Unacked(ctx, KindCommand, 10); err != nil || len(records) != 0 {
		t.Fatalf("completed command remained unacked: %+v, %v", records, err)
	}
	receipt, created, err = j.ReceiveCommand(ctx, commandRecord("command-1"), digest)
	if err != nil || created || receipt.State != CommandRejected || receipt.AckReasonCode != v1.CommandAckReasonRunnerPaused {
		t.Fatalf("terminal replay = %+v, %v, %v", receipt, created, err)
	}
}

func TestReceiveCommandAdoptsLegacyIncompleteAndCompletedRecords(t *testing.T) {
	t.Parallel()
	j := openTest(t)
	ctx := context.Background()
	incompleteRecord, incompleteDigest := legacyCommandRecord(t, "legacy-incomplete", map[string]any{"command": "resume_runner"})
	if err := j.Append(ctx, incompleteRecord); err != nil {
		t.Fatal(err)
	}
	incomplete, created, err := j.ReceiveCommand(ctx, commandRecord("legacy-incomplete"), incompleteDigest)
	if err != nil || !created || incomplete.State != CommandReceived {
		t.Fatalf("legacy incomplete = %+v, %v, %v", incomplete, created, err)
	}

	completedRecord, completedDigest := legacyCommandRecord(t, "legacy-completed", map[string]any{"command": "resume_runner"})
	if err := j.Append(ctx, completedRecord); err != nil {
		t.Fatal(err)
	}
	if err := j.AckKind(ctx, "legacy-completed", KindCommand); err != nil {
		t.Fatal(err)
	}
	completed, created, err := j.ReceiveCommand(ctx, commandRecord("legacy-completed"), completedDigest)
	if err != nil || !created || completed.State != CommandAccepted || completed.AckStatus != "duplicate" {
		t.Fatalf("legacy completed = %+v, %v, %v", completed, created, err)
	}
}

func TestReceiveCommandRejectsChangedLegacyIncompleteContent(t *testing.T) {
	t.Parallel()
	j := openTest(t)
	ctx := context.Background()
	record, _ := legacyCommandRecord(t, "legacy-conflict", map[string]any{"command": "resume_runner"})
	if err := j.Append(ctx, record); err != nil {
		t.Fatal(err)
	}
	_, changedDigest := legacyCommandRecord(t, "legacy-conflict", map[string]any{"command": "emergency_stop"})
	if _, _, err := j.ReceiveCommand(ctx, commandRecord("legacy-conflict"), changedDigest); !errors.Is(err, ErrCommandContentConflict) {
		t.Fatalf("changed legacy content = %v, want ErrCommandContentConflict", err)
	}
}

func TestReceiveCommandRejectsInvalidBoundaryValues(t *testing.T) {
	t.Parallel()
	j := openTest(t)
	ctx := context.Background()
	if _, _, err := j.ReceiveCommand(ctx, Record{ID: "event", Kind: KindEvent}, strings.Repeat("a", 64)); err == nil {
		t.Fatal("event record accepted as command")
	}
	if _, _, err := j.ReceiveCommand(ctx, commandRecord("bad-digest"), "bad"); err == nil {
		t.Fatal("invalid digest accepted")
	}
	if _, _, err := j.ClaimCommand(ctx, "missing", "process-1"); err == nil {
		t.Fatal("missing command claimed")
	}
	if _, err := j.CompleteCommand(ctx, "missing", "process-1", "accepted", ""); err == nil {
		t.Fatal("missing command completed")
	}
}

func TestCommandReceiptMigrationRedactsLegacyRawDiagnostics(t *testing.T) {
	t.Parallel()
	j := openTest(t)
	ctx := context.Background()
	digest := strings.Repeat("f", 64)
	if _, _, err := j.ReceiveCommand(ctx, commandRecord("legacy-raw-rejection"), digest); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := j.ClaimCommand(ctx, "legacy-raw-rejection", "process-1"); err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if _, err := j.CompleteCommand(
		ctx,
		"legacy-raw-rejection",
		"process-1",
		"rejected",
		v1.CommandAckReasonInternalRejection,
	); err != nil {
		t.Fatal(err)
	}
	const rawDiagnostic = "https://token:secret@example.test/private"
	if _, err := j.db.ExecContext(
		ctx,
		`UPDATE command_receipts SET ack_error=? WHERE command_id=?`,
		rawDiagnostic,
		"legacy-raw-rejection",
	); err != nil {
		t.Fatal(err)
	}

	if err := j.ensureCommandReceiptSchema(); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := j.db.QueryRowContext(
		ctx,
		`SELECT ack_error FROM command_receipts WHERE command_id=?`,
		"legacy-raw-rejection",
	).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != string(v1.CommandAckReasonInternalRejection) || strings.Contains(stored, "secret") {
		t.Fatalf("legacy diagnostic was not redacted: %q", stored)
	}
}

func TestConcurrentCommandDeliveryConvergesOnOneReceiptAndClaim(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	digest := strings.Repeat("d", 64)
	var createdCount atomic.Int64
	var claimedCount atomic.Int64
	errorsByWorker := make(chan error, 16)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, created, err := j.ReceiveCommand(ctx, commandRecord("concurrent-command"), digest)
			if err != nil {
				errorsByWorker <- err
				return
			}
			if created {
				createdCount.Add(1)
			}
			_, claimed, err := j.ClaimCommand(ctx, "concurrent-command", "same-process")
			if err != nil {
				errorsByWorker <- err
				return
			}
			if claimed {
				claimedCount.Add(1)
			}
		}()
	}
	close(start)
	workers.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		t.Errorf("concurrent delivery: %v", err)
	}
	if got := createdCount.Load(); got != 1 {
		t.Fatalf("created receipts = %d, want 1", got)
	}
	if got := claimedCount.Load(); got != 1 {
		t.Fatalf("claimed executions = %d, want 1", got)
	}
}

func TestPruneCascadesCompletedCommandReceipt(t *testing.T) {
	t.Parallel()
	j := openTest(t)
	ctx := context.Background()
	digest := strings.Repeat("e", 64)
	if _, _, err := j.ReceiveCommand(ctx, commandRecord("pruned-command"), digest); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := j.ClaimCommand(ctx, "pruned-command", "process-1"); err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if _, err := j.CompleteCommand(ctx, "pruned-command", "process-1", "accepted", ""); err != nil {
		t.Fatal(err)
	}
	if pruned, err := j.Prune(ctx, time.Now().Add(2*time.Second), 10); err != nil || pruned != 1 {
		t.Fatalf("prune = %d, %v", pruned, err)
	}
	if _, found, err := loadCommandReceipt(ctx, j.db, "pruned-command"); err != nil || found {
		t.Fatalf("receipt survived record prune: found=%v err=%v", found, err)
	}
}
