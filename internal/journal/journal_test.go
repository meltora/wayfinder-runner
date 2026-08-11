package journal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/lease"
)

func openTest(t *testing.T) *Journal {
	t.Helper()
	j, err := Open(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

func TestAppendIdempotent(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	r := Record{ID: "e1", Kind: KindEvent, Payload: []byte(`{"x":1}`), CreatedAt: time.Now()}
	if err := j.Append(ctx, r); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := j.Append(ctx, r); err != nil {
		t.Fatalf("Append duplicate should be ignored: %v", err)
	}
	n, err := j.UnackedCount(ctx)
	if err != nil {
		t.Fatalf("UnackedCount: %v", err)
	}
	if n != 1 {
		t.Fatalf("unacked = %d, want 1", n)
	}
}

func TestAppendIfNewReportsDuplicate(t *testing.T) {
	t.Parallel()
	j := openTest(t)
	record := Record{ID: "cmd-1", Kind: KindCommand, Payload: []byte(`{"command":"pause"}`), CreatedAt: time.Now()}
	inserted, err := j.AppendIfNew(context.Background(), record)
	if err != nil || !inserted {
		t.Fatalf("first insert = %v, %v", inserted, err)
	}
	inserted, err = j.AppendIfNew(context.Background(), record)
	if err != nil || inserted {
		t.Fatalf("duplicate insert = %v, %v", inserted, err)
	}
}

func TestUnackedOrderingAndAck(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	base := time.Now()
	for i, id := range []string{"a", "b", "c"} {
		if err := j.Append(ctx, Record{ID: id, Kind: KindEvent, Payload: []byte(id), CreatedAt: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatalf("Append %s: %v", id, err)
		}
	}
	if err := j.Ack(ctx, "b"); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	recs, err := j.Unacked(ctx, KindEvent, 10)
	if err != nil {
		t.Fatalf("Unacked: %v", err)
	}
	if len(recs) != 2 || recs[0].ID != "a" || recs[1].ID != "c" {
		t.Fatalf("unacked = %+v", recs)
	}
	if err := j.Ack(ctx, "b"); err == nil {
		t.Fatal("double ack should error")
	}
	if err := j.Ack(ctx, "missing"); err == nil {
		t.Fatal("ack of unknown id should error")
	}
}

func TestKindsAreSeparate(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	now := time.Now()
	if err := j.Append(ctx, Record{ID: "e1", Kind: KindEvent, Payload: []byte("e"), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(ctx, Record{ID: "c1", Kind: KindCommand, Payload: []byte("c"), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	evts, _ := j.Unacked(ctx, KindEvent, 10)
	cmds, _ := j.Unacked(ctx, KindCommand, 10)
	if len(evts) != 1 || len(cmds) != 1 {
		t.Fatalf("kinds not separated: %d events, %d commands", len(evts), len(cmds))
	}
}

func TestAckKindCannotAcknowledgeDifferentRecordKind(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	if err := j.Append(ctx, Record{ID: "upload-1", Kind: KindUpload, Payload: []byte(`{}`), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := j.AckKind(ctx, "upload-1", KindEvent); err == nil {
		t.Fatal("event ack acknowledged an upload record")
	}
	records, err := j.Unacked(ctx, KindUpload, 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("upload record was lost: %v %v", records, err)
	}
}

func TestLeaseRoundTripAndClear(t *testing.T) {
	t.Parallel()
	j := openTest(t)
	ctx := context.Background()
	want := lease.Lease{
		JobID: "job-1", AttemptID: "attempt-1", Kind: v1.JobKindBuild,
		FencingToken: 42,
		AcquiredAt:   time.Unix(1_700_000_000, 0),
		ExpiresAt:    time.Unix(1_700_000_600, 0),
	}
	if err := j.SaveLease(ctx, want); err != nil {
		t.Fatalf("SaveLease: %v", err)
	}
	got, err := j.LoadLease(ctx)
	if err != nil {
		t.Fatalf("LoadLease: %v", err)
	}
	if got == nil || *got != want {
		t.Fatalf("LoadLease = %+v, want %+v", got, want)
	}
	if err := j.ClearLease(ctx, "wrong-attempt"); err != nil {
		t.Fatalf("ClearLease wrong attempt: %v", err)
	}
	if got, _ := j.LoadLease(ctx); got == nil {
		t.Fatal("wrong attempt cleared lease")
	}
	if err := j.ClearLease(ctx, want.AttemptID); err != nil {
		t.Fatalf("ClearLease: %v", err)
	}
	if got, err := j.LoadLease(ctx); err != nil || got != nil {
		t.Fatalf("LoadLease after clear = %+v, %v", got, err)
	}
}

func TestDurableToolCallLifecycle(t *testing.T) {
	t.Parallel()
	j := openTest(t)
	ctx := context.Background()
	state, _, err := j.BeginToolCall(ctx, "attempt-1", "platform_report_progress", "progress-1")
	if err != nil || state != ToolCallStarted {
		t.Fatalf("BeginToolCall = %q, %v", state, err)
	}
	state, _, err = j.BeginToolCall(ctx, "attempt-1", "platform_report_progress", "progress-1")
	if err != nil || state != ToolCallPending {
		t.Fatalf("pending BeginToolCall = %q, %v", state, err)
	}
	result := []byte(`{"ok":true}`)
	if err := j.CompleteToolCall(ctx, "attempt-1", "platform_report_progress", "progress-1", result); err != nil {
		t.Fatal(err)
	}
	state, got, err := j.BeginToolCall(ctx, "attempt-1", "platform_report_progress", "progress-1")
	if err != nil || state != ToolCallCompleted || string(got) != string(result) {
		t.Fatalf("completed BeginToolCall = %q, %s, %v", state, got, err)
	}

	state, _, err = j.BeginToolCall(ctx, "attempt-1", "platform_checkpoint", "checkpoint-1")
	if err != nil || state != ToolCallStarted {
		t.Fatalf("second BeginToolCall = %q, %v", state, err)
	}
	if err := j.AbandonToolCall(ctx, "attempt-1", "platform_checkpoint", "checkpoint-1"); err != nil {
		t.Fatal(err)
	}
	state, _, err = j.BeginToolCall(ctx, "attempt-1", "platform_checkpoint", "checkpoint-1")
	if err != nil || state != ToolCallStarted {
		t.Fatalf("BeginToolCall after abandon = %q, %v", state, err)
	}
}

func TestPruneRemovesOnlyExpiredCompletedData(t *testing.T) {
	t.Parallel()
	j := openTest(t)
	ctx := context.Background()
	old := time.Now().Add(-60 * 24 * time.Hour)
	for _, id := range []string{"old-acked", "recent-acked", "old-unacked"} {
		if err := j.Append(ctx, Record{ID: id, Kind: KindEvent, Payload: []byte(`{}`), CreatedAt: old}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Ack(ctx, "old-acked"); err != nil {
		t.Fatal(err)
	}
	if err := j.Ack(ctx, "recent-acked"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.db.Exec(`UPDATE records SET acked_at=? WHERE id='old-acked'`, old.Unix()); err != nil {
		t.Fatal(err)
	}
	if state, _, err := j.BeginToolCall(ctx, "attempt", "tool", "old"); err != nil || state != ToolCallStarted {
		t.Fatalf("begin tool call: %v, %v", state, err)
	}
	if err := j.CompleteToolCall(ctx, "attempt", "tool", "old", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := j.db.Exec(`UPDATE tool_calls SET updated_at=? WHERE idempotency_key='old'`, old.Unix()); err != nil {
		t.Fatal(err)
	}

	removed, err := j.Prune(ctx, time.Now().Add(-30*24*time.Hour), 100)
	if err != nil || removed != 2 {
		t.Fatalf("Prune = %d, %v", removed, err)
	}
	var remaining int
	if err := j.db.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&remaining); err != nil || remaining != 2 {
		t.Fatalf("remaining records = %d, %v", remaining, err)
	}
	if err := j.db.QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("remaining tool calls = %d, %v", remaining, err)
	}
}

func TestJournalCapacityFailsClosed(t *testing.T) {
	j := openTest(t)
	j.SetMaxBytes(1)
	if err := j.Append(context.Background(), Record{ID: "event", Kind: KindEvent, Payload: []byte(`{}`), CreatedAt: time.Now()}); err == nil {
		t.Fatal("journal accepted a record beyond its configured capacity")
	}
}

func TestOpenAndMaintenanceValidation(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir); err == nil {
		t.Fatal("directory journal accepted")
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "journal.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("symlink journal accepted")
	}

	j := openTest(t)
	if err := j.ensureCapacity(context.Background(), -1); err == nil {
		t.Fatal("negative capacity accepted")
	}
	for _, limit := range []int{0, 10_001} {
		if _, err := j.Prune(context.Background(), time.Now(), limit); err == nil {
			t.Fatalf("prune limit %d accepted", limit)
		}
	}
	if err := j.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteToolCall(context.Background(), "missing", "tool", "key", []byte(`{}`)); err == nil {
		t.Fatal("missing tool call completed")
	}
}

func TestUnackedPagination(t *testing.T) {
	j := openTest(t)
	for _, id := range []string{"a", "b", "c"} {
		if err := j.Append(context.Background(), Record{ID: id, Kind: KindEvent, Payload: []byte(id), CreatedAt: time.Unix(100, 0)}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := j.UnackedPage(context.Background(), KindEvent, 1, 1)
	if err != nil || len(page) != 1 || page[0].ID != "b" {
		t.Fatalf("page=%v err=%v", page, err)
	}
}

func TestClaimCheckExecutionBindsKeyToExactlyOneCheck(t *testing.T) {
	t.Parallel()
	j := openTest(t)
	ctx := context.Background()
	claimed, err := j.ClaimCheckExecution(ctx, "attempt-1", "key-1", "check-a", 5, 10)
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	claimed, err = j.ClaimCheckExecution(ctx, "attempt-1", "key-1", "check-a", 5, 10)
	if err != nil || claimed {
		t.Fatalf("same-check replay = %v, %v; must replay without a new claim", claimed, err)
	}
	if _, err = j.ClaimCheckExecution(ctx, "attempt-1", "key-1", "check-b", 5, 10); !errors.Is(err, ErrCheckClaimConflict) {
		t.Fatalf("cross-check claim = %v, want ErrCheckClaimConflict", err)
	}
	claimed, err = j.ClaimCheckExecution(ctx, "attempt-2", "key-1", "check-b", 5, 10)
	if err != nil || !claimed {
		t.Fatalf("a different attempt owns its own namespace: %v, %v", claimed, err)
	}
}

func TestClaimCheckExecutionEnforcesDurableBudgets(t *testing.T) {
	t.Parallel()
	j := openTest(t)
	ctx := context.Background()
	claim := func(key, checkID string) (bool, error) {
		return j.ClaimCheckExecution(ctx, "attempt-1", key, checkID, 2, 3)
	}
	if claimed, err := claim("k1", "check-a"); err != nil || !claimed {
		t.Fatalf("claim k1 = %v, %v", claimed, err)
	}
	if claimed, err := claim("k2", "check-a"); err != nil || !claimed {
		t.Fatalf("claim k2 = %v, %v", claimed, err)
	}
	if _, err := claim("k3", "check-a"); !errors.Is(err, ErrCheckBudgetExhausted) {
		t.Fatalf("per-check budget = %v, want ErrCheckBudgetExhausted", err)
	}
	if claimed, err := claim("k3", "check-b"); err != nil || !claimed {
		t.Fatalf("per-attempt slot = %v, %v", claimed, err)
	}
	if _, err := claim("k4", "check-b"); !errors.Is(err, ErrCheckBudgetExhausted) {
		t.Fatalf("per-attempt budget = %v, want ErrCheckBudgetExhausted", err)
	}
	if claimed, err := claim("k1", "check-a"); err != nil || claimed {
		t.Fatalf("same-key replay consumed a budget slot: %v, %v", claimed, err)
	}
	if _, err := j.ClaimCheckExecution(ctx, "attempt-1", "k9", "check-a", 0, 3); err == nil {
		t.Fatal("non-positive per-check limit accepted")
	}
	if _, err := j.ClaimCheckExecution(ctx, "attempt-1", "k9", "check-a", 2, 0); err == nil {
		t.Fatal("non-positive per-attempt limit accepted")
	}
}

func TestClaimCheckExecutionSurvivesRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "journal.db")
	ctx := context.Background()
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := j.ClaimCheckExecution(ctx, "attempt-1", "key-1", "check-a", 1, 5); err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if claimed, err := reopened.ClaimCheckExecution(ctx, "attempt-1", "key-1", "check-a", 1, 5); err != nil || claimed {
		t.Fatalf("post-restart replay = %v, %v", claimed, err)
	}
	if _, err := reopened.ClaimCheckExecution(ctx, "attempt-1", "key-1", "check-b", 1, 5); !errors.Is(err, ErrCheckClaimConflict) {
		t.Fatalf("post-restart conflict = %v, want ErrCheckClaimConflict", err)
	}
	if _, err := reopened.ClaimCheckExecution(ctx, "attempt-1", "key-2", "check-a", 1, 5); !errors.Is(err, ErrCheckBudgetExhausted) {
		t.Fatalf("post-restart budget = %v, want ErrCheckBudgetExhausted", err)
	}
}

func TestClaimCheckExecutionConcurrentClaimsConverge(t *testing.T) {
	t.Parallel()
	j := openTest(t)
	ctx := context.Background()
	const callers = 16
	barrier := make(chan struct{})
	var wg sync.WaitGroup
	claimed := make([]bool, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-barrier
			claimed[i], errs[i] = j.ClaimCheckExecution(ctx, "attempt-1", "shared-key", fmt.Sprintf("check-%d", i), 20, 20)
		}(i)
	}
	close(barrier)
	wg.Wait()
	winners := 0
	for i := range callers {
		if claimed[i] {
			winners++
			continue
		}
		if !errors.Is(errs[i], ErrCheckClaimConflict) {
			t.Fatalf("loser %d err = %v, want ErrCheckClaimConflict", i, errs[i])
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent cross-check claims produced %d owners, want exactly 1", winners)
	}
}

func TestHasRecord(t *testing.T) {
	t.Parallel()
	j := openTest(t)
	ctx := context.Background()
	exists, err := j.HasRecord(ctx, "missing")
	if err != nil || exists {
		t.Fatalf("HasRecord missing = %v, %v", exists, err)
	}
	if err := j.Append(ctx, Record{ID: "stable-1", Kind: KindEvent, Payload: []byte(`{}`), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	exists, err = j.HasRecord(ctx, "stable-1")
	if err != nil || !exists {
		t.Fatalf("HasRecord present = %v, %v", exists, err)
	}
	if err := j.Ack(ctx, "stable-1"); err != nil {
		t.Fatal(err)
	}
	exists, err = j.HasRecord(ctx, "stable-1")
	if err != nil || !exists {
		t.Fatalf("HasRecord acked = %v, %v", exists, err)
	}
}
