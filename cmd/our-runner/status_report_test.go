package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/journal"
)

func appendStatusEvent(t *testing.T, j *journal.Journal, id string) {
	t.Helper()
	if err := j.Append(context.Background(), journal.Record{
		ID: id, Kind: journal.KindEvent, Payload: []byte(id), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Append %s: %v", id, err)
	}
}

func TestStatusReportFreshStateDirectoryCreatesNothing(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	lines, err := statusReport(stateDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"state dir: " + stateDir, "enrolled: false", "journal: not created yet"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("status created the state directory: %v", err)
	}
}

func TestStatusReportCountsRejectedEvents(t *testing.T) {
	stateDir := t.TempDir()
	journalPath := filepath.Join(stateDir, "journal.db")
	j, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"evt-1", "evt-2", "evt-3"} {
		appendStatusEvent(t, j, id)
	}
	if err := j.RecordEventRejection(ctx, "evt-1", "runner_event_rejected", now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordEventRejection(ctx, "evt-2", "unsupported_message", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordSessionRejection(ctx, "invalid_envelope", now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordSessionRejection(ctx, "invalid_envelope", now.Add(-90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordSessionRejection(ctx, "malformed_json", now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordSessionRejection(ctx, "envelope_too_large", now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}

	lines, err := statusReport(stateDir, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"state dir: " + stateDir,
		"enrolled: false",
		"unacknowledged events: 3",
		"rejected events: 2",
		"rejected for more than 24 hours (may never be accepted): 1",
		"last rejection: unsupported_message at 2026-09-29T11:00:00Z",
		"gateway closed the session: malformed_json ×1 (last 2026-09-29T11:30:00Z)",
		"gateway closed the session: invalid_envelope ×2 (last 2026-09-29T10:30:00Z)",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines =\n%q\nwant\n%q", lines, want)
	}
	after, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode() != info.Mode() || !after.ModTime().Equal(info.ModTime()) || after.Size() != info.Size() {
		t.Fatalf("status changed the journal: %v %v %d -> %v %v %d",
			info.Mode(), info.ModTime(), info.Size(), after.Mode(), after.ModTime(), after.Size())
	}

	j, err = journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.AckKind(ctx, "evt-2", journal.KindEvent); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	lines, err = statusReport(stateDir, now)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{
		"state dir: " + stateDir,
		"enrolled: false",
		"unacknowledged events: 2",
		"rejected events: 1",
		"rejected for more than 24 hours (may never be accepted): 1",
		"last rejection: runner_event_rejected at 2026-09-28T11:00:00Z",
		"gateway closed the session: malformed_json ×1 (last 2026-09-29T11:30:00Z)",
		"gateway closed the session: invalid_envelope ×2 (last 2026-09-29T10:30:00Z)",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines after ack =\n%q\nwant\n%q", lines, want)
	}
}

func TestStatusReportWithoutRejectionsOmitsLastRejection(t *testing.T) {
	stateDir := t.TempDir()
	if err := daemon.WriteRunnerID(stateDir, "runner-123"); err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(filepath.Join(stateDir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	appendStatusEvent(t, j, "evt-1")
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	lines, err := statusReport(stateDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 6 || lines[2] != "runner id: runner-123" {
		t.Fatalf("lines = %q", lines)
	}
	tail := lines[len(lines)-3:]
	want := []string{
		"unacknowledged events: 1",
		"rejected events: 0",
		"rejected for more than 24 hours (may never be accepted): 0",
	}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("lines = %q", lines)
	}
}

func TestStatusReportUnavailableJournalDoesNotFail(t *testing.T) {
	for name, prepare := range map[string]func(t *testing.T, path string) string{
		"directory": func(t *testing.T, path string) string {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			return "journal: unavailable (not a regular file)"
		},
		"corrupt": func(t *testing.T, path string) string {
			if err := os.WriteFile(path, []byte("this is not a sqlite database, only some bytes padding it out"), 0o600); err != nil {
				t.Fatal(err)
			}
			return "journal: unavailable (corrupt)"
		},
	} {
		t.Run(name, func(t *testing.T) {
			stateDir := t.TempDir()
			want := prepare(t, filepath.Join(stateDir, "journal.db"))
			lines, err := statusReport(stateDir, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if lines[len(lines)-1] != want {
				t.Fatalf("lines = %q, want last %q", lines, want)
			}
		})
	}
}

func TestCmdStatusSucceedsWithoutJournal(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := cmdStatus([]string{"--state-dir", stateDir}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("status created the state directory: %v", err)
	}
}

func TestStatusReportLeavesBroadEnrolledStateDirectoryUnchanged(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := identity.NewStore(stateDir).SaveCert([]byte("cert"), []byte("ca")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := statusReport(stateDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"state dir: " + stateDir, "enrolled: true", "journal: not created yet"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
	after, err := os.Stat(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("status changed the state directory from %v %v to %v %v",
			before.Mode(), before.ModTime(), after.Mode(), after.ModTime())
	}
}

func TestStatusReportWALWithoutIndexIsUnavailable(t *testing.T) {
	source := t.TempDir()
	j, err := journal.Open(filepath.Join(source, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	appendStatusEvent(t, j, "evt-1")
	stateDir := t.TempDir()
	for _, name := range []string{"journal.db", "journal.db-wal"} {
		raw, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stateDir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	journalPath := filepath.Join(stateDir, "journal.db")
	stats := func() []os.FileInfo {
		out := []os.FileInfo{}
		for _, name := range []string{journalPath, journalPath + "-wal"} {
			info, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, info)
		}
		return out
	}
	before := stats()
	if err := cmdStatus([]string{"--state-dir", stateDir}); err != nil {
		t.Fatalf("status: %v", err)
	}
	lines, err := statusReport(stateDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := lines[len(lines)-1]; got != "journal: unavailable (wal index missing)" {
		t.Fatalf("lines = %q", lines)
	}
	if _, err := os.Lstat(journalPath + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("status created the WAL index: %v", err)
	}
	for i, info := range stats() {
		if info.Mode() != before[i].Mode() || info.Size() != before[i].Size() || !info.ModTime().Equal(before[i].ModTime()) {
			t.Fatalf("status changed %s", info.Name())
		}
	}
}
