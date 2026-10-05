package journal

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func appendEvent(t *testing.T, j *Journal, id string) {
	t.Helper()
	if err := j.Append(context.Background(), Record{
		ID: id, Kind: KindEvent, Payload: []byte(`{"id":"` + id + `"}`), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Append %s: %v", id, err)
	}
}

type eventRejectionRow struct {
	code  string
	first int64
	last  int64
	count int
}

func readEventRejection(t *testing.T, j *Journal, id string) (eventRejectionRow, bool) {
	t.Helper()
	var row eventRejectionRow
	err := j.db.QueryRow(`SELECT code, first_rejected_at, last_rejected_at, count FROM event_rejections WHERE record_id=?`, id).
		Scan(&row.code, &row.first, &row.last, &row.count)
	if err == sql.ErrNoRows {
		return row, false
	}
	if err != nil {
		t.Fatalf("read rejection %s: %v", id, err)
	}
	return row, true
}

func TestRecordEventRejectionCountsRepeatedRejections(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	appendEvent(t, j, "evt-1")
	first := time.Unix(1_000, 0)
	for index, code := range []string{"runner_event_rejected", "runner_event_rejected", "unsupported_message"} {
		if err := j.RecordEventRejection(ctx, "evt-1", code, first.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatalf("RecordEventRejection: %v", err)
		}
	}
	row, ok := readEventRejection(t, j, "evt-1")
	if !ok {
		t.Fatal("rejection row missing")
	}
	if row.count != 3 || row.first != 1_000 || row.last != 1_120 || row.code != "unsupported_message" {
		t.Fatalf("rejection row = %+v", row)
	}
}

func TestRecordEventRejectionIgnoresUnknownCommandAndAcknowledgedRecords(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	appendEvent(t, j, "evt-acked")
	if err := j.AckKind(ctx, "evt-acked", KindEvent); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(ctx, Record{ID: "cmd-1", Kind: KindCommand, Payload: []byte("c"), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"evt-unknown", "cmd-1", "evt-acked"} {
		if err := j.RecordEventRejection(ctx, id, "runner_event_rejected", time.Now()); err != nil {
			t.Fatalf("RecordEventRejection %s: %v", id, err)
		}
		if _, ok := readEventRejection(t, j, id); ok {
			t.Fatalf("rejection recorded for %s", id)
		}
	}
	var rows int
	if err := j.db.QueryRow(`SELECT COUNT(*) FROM event_rejections`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("event_rejections rows = %d, %v", rows, err)
	}
}

func TestAcknowledgedRejectedEventIsExcludedAndCascadesOnPrune(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	appendEvent(t, j, "evt-1")
	appendEvent(t, j, "evt-2")
	now := time.Now()
	for _, id := range []string{"evt-1", "evt-2"} {
		if err := j.RecordEventRejection(ctx, id, "runner_event_rejected", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.AckKind(ctx, "evt-1", KindEvent); err != nil {
		t.Fatal(err)
	}
	summary, err := j.RejectionSummary(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if summary.UnacknowledgedEvents != 1 || summary.RejectedEvents != 1 {
		t.Fatalf("summary after ack = %+v", summary)
	}
	if _, ok := readEventRejection(t, j, "evt-1"); !ok {
		t.Fatal("rejection row dropped before prune")
	}
	if _, err := j.Prune(ctx, time.Now().Add(time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if _, ok := readEventRejection(t, j, "evt-1"); ok {
		t.Fatal("rejection row survived prune of its acknowledged record")
	}
	if _, ok := readEventRejection(t, j, "evt-2"); !ok {
		t.Fatal("rejection row of an unacknowledged record was removed")
	}
}

func TestRecordSessionRejectionCapsDistinctCodes(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	at := time.Unix(5_000, 0)
	for index := range maxSessionRejectionCodes {
		if err := j.RecordSessionRejection(ctx, fmt.Sprintf("code_%02d", index), at); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.RecordSessionRejection(ctx, "code_00", at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"code_extra_a", "code_extra_b"} {
		if err := j.RecordSessionRejection(ctx, code, at.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	counts := map[string]int{}
	rows, err := j.db.Query(`SELECT code, count FROM session_rejections`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var code string
		var count int
		if err := rows.Scan(&code, &count); err != nil {
			t.Fatal(err)
		}
		counts[code] = count
	}
	if len(counts) != maxSessionRejectionCodes+1 || counts["code_00"] != 2 || counts["other"] != 2 ||
		counts["code_extra_a"] != 0 {
		t.Fatalf("session rejection counts = %v", counts)
	}
}

func TestRejectionCodesAreNormalized(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	appendEvent(t, j, "evt-1")
	if err := j.RecordEventRejection(ctx, "evt-1", "Bad Code!", time.Now()); err != nil {
		t.Fatal(err)
	}
	if row, _ := readEventRejection(t, j, "evt-1"); row.code != "invalid_code" {
		t.Fatalf("event code = %q", row.code)
	}
	if err := j.RecordSessionRejection(ctx, "Bad Code!", time.Now()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := j.db.QueryRow(`SELECT count FROM session_rejections WHERE code='invalid_code'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("invalid_code count = %d, %v", count, err)
	}
	for code, want := range map[string]string{
		"runner_event_rejected": "runner_event_rejected",
		"a":                     "a",
		"":                      "invalid_code",
		"1abc":                  "invalid_code",
		"abc-def":               "invalid_code",
		"a" + string(bytes.Repeat([]byte("b"), 63)): "a" + string(bytes.Repeat([]byte("b"), 63)),
		"a" + string(bytes.Repeat([]byte("b"), 64)): "invalid_code",
	} {
		if got := NormalizeRejectionCode(code); got != want {
			t.Fatalf("NormalizeRejectionCode(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestRejectionWritesFailOnClosedJournal(t *testing.T) {
	j, err := Open(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	if err := j.RecordEventRejection(context.Background(), "evt-1", "runner_event_rejected", time.Now()); err == nil {
		t.Fatal("event rejection write on a closed journal succeeded")
	}
	if err := j.RecordSessionRejection(context.Background(), "invalid_envelope", time.Now()); err == nil {
		t.Fatal("session rejection write on a closed journal succeeded")
	}
}

func TestRejectedEventSurvivesPruneRestartAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	appendEvent(t, j, "evt-before")
	appendEvent(t, j, "evt-rejected")
	appendEvent(t, j, "evt-after")
	original, ok, err := j.LoadRecord(ctx, "evt-rejected")
	if err != nil || !ok {
		t.Fatalf("LoadRecord: %v %v", ok, err)
	}
	for range 5 {
		if err := j.RecordEventRejection(ctx, "evt-rejected", "runner_event_rejected", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.Prune(ctx, time.Now().AddDate(1, 0, 0), 10_000); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	records, err := reopened.Unacked(ctx, KindEvent, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[0].ID != "evt-before" || records[1].ID != "evt-rejected" || records[2].ID != "evt-after" {
		t.Fatalf("replay order = %+v", records)
	}
	if records[1].Sequence != original.Sequence || !bytes.Equal(records[1].Payload, original.Payload) {
		t.Fatalf("rejected record changed: %+v vs %+v", records[1], original)
	}
	if row, ok := readEventRejection(t, reopened, "evt-rejected"); !ok || row.count != 5 {
		t.Fatalf("rejection row after restart = %+v, %v", row, ok)
	}
}

func TestRejectionSummaryCountsAndOrdersSessionCodes(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	now := time.Unix(10_000_000, 0)
	for _, id := range []string{"evt-1", "evt-2", "evt-3"} {
		appendEvent(t, j, id)
	}
	if err := j.RecordEventRejection(ctx, "evt-1", "runner_event_rejected", now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordEventRejection(ctx, "evt-2", "unsupported_message", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for code, at := range map[string]time.Time{
		"invalid_envelope":   now.Add(-2 * time.Hour),
		"malformed_json":     now.Add(-time.Hour),
		"envelope_too_large": now.Add(-8 * 24 * time.Hour),
	} {
		if err := j.RecordSessionRejection(ctx, code, at); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := j.RejectionSummary(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if summary.UnacknowledgedEvents != 3 || summary.RejectedEvents != 2 || summary.LongRejectedEvents != 1 {
		t.Fatalf("summary counts = %+v", summary)
	}
	if summary.LastRejection == nil || summary.LastRejection.Code != "unsupported_message" ||
		!summary.LastRejection.At.Equal(now.Add(-time.Hour)) {
		t.Fatalf("last rejection = %+v", summary.LastRejection)
	}
	if len(summary.SessionRejections) != 2 || summary.SessionRejections[0].Code != "malformed_json" ||
		summary.SessionRejections[1].Code != "invalid_envelope" || summary.SessionRejections[0].Count != 1 {
		t.Fatalf("session rejections = %+v", summary.SessionRejections)
	}
}

func createPreS23Journal(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		CREATE TABLE records (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			id TEXT NOT NULL UNIQUE,
			kind TEXT NOT NULL,
			payload BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			acked_at INTEGER
		);
		INSERT INTO records (id, kind, payload, created_at) VALUES ('evt-old', 'event', 'x', 100);
	`); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlySummaryOfPreS23JournalReportsZeroRejections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	createPreS23Journal(t, path)
	summary, err := ReadRejectionSummary(context.Background(), path, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if summary.UnacknowledgedEvents != 1 || summary.RejectedEvents != 0 || summary.LastRejection != nil ||
		len(summary.SessionRejections) != 0 {
		t.Fatalf("pre-S23 summary = %+v", summary)
	}
	var tables int
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('event_rejections', 'session_rejections')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("read-only summary created tables: %d, %v", tables, err)
	}
}

func TestOpenAddsRejectionTablesToPreS23Journal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	createPreS23Journal(t, path)
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	if err := j.RecordEventRejection(context.Background(), "evt-old", "runner_event_rejected", time.Now()); err != nil {
		t.Fatal(err)
	}
	if row, ok := readEventRejection(t, j, "evt-old"); !ok || row.count != 1 {
		t.Fatalf("rejection on migrated journal = %+v, %v", row, ok)
	}
}

func TestPreS23SchemaReaderIgnoresRejectionTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(t, j, "evt-1")
	if err := j.RecordEventRejection(context.Background(), "evt-1", "runner_event_rejected", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordSessionRejection(context.Background(), "invalid_envelope", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS records (sequence INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, kind TEXT NOT NULL, payload BLOB NOT NULL, created_at INTEGER NOT NULL, acked_at INTEGER)`,
		`INSERT OR IGNORE INTO records (id, kind, payload, created_at) VALUES ('evt-2', 'event', 'y', 200)`,
		`UPDATE records SET acked_at = 300 WHERE id = 'evt-1' AND kind = 'event' AND acked_at IS NULL`,
		`DELETE FROM records WHERE acked_at IS NOT NULL AND acked_at < 400`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("pre-S23 statement %q: %v", statement, err)
		}
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_rejections`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("cascade after pre-S23 prune = %d, %v", remaining, err)
	}
}

func TestReadOnlySummaryWhileWriterHoldsWALTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	ctx := context.Background()
	appendEvent(t, j, "evt-1")
	appendEvent(t, j, "evt-2")
	if err := j.RecordEventRejection(ctx, "evt-1", "runner_event_rejected", time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := j.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE records SET acked_at = 1 WHERE id = 'evt-2'`); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	summary, readErr := ReadRejectionSummary(ctx, path, time.Now())
	elapsed := time.Since(started)
	if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if readErr != nil {
		t.Fatalf("read-only summary during a held write transaction: %v", readErr)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("read-only summary waited %s", elapsed)
	}
	if summary.UnacknowledgedEvents != 2 || summary.RejectedEvents != 1 {
		t.Fatalf("summary during write = %+v", summary)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode() != before.Mode() {
		t.Fatalf("journal mode changed from %v to %v", before.Mode(), after.Mode())
	}
	var check string
	if err := j.db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check = %q, %v", check, err)
	}
	appendEvent(t, j, "evt-3")
}

func TestReadOnlySummaryOfClosedWALJournalCreatesNoFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.db")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(t, j, "evt-1")
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	entriesBefore, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := ReadRejectionSummary(context.Background(), path, time.Now())
	if err != nil {
		t.Fatalf("read-only summary of a closed journal: %v", err)
	}
	if summary.UnacknowledgedEvents != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	entriesAfter, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesAfter) != len(entriesBefore) {
		names := []string{}
		for _, entry := range entriesAfter {
			names = append(names, entry.Name())
		}
		t.Fatalf("read-only summary left files behind: %v", names)
	}
}

func TestReadRejectionSummaryRejectsMissingAndNonRegularPaths(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadRejectionSummary(context.Background(), filepath.Join(dir, "missing.db"), time.Now()); !os.IsNotExist(err) {
		t.Fatalf("missing journal error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "missing.db")); !os.IsNotExist(err) {
		t.Fatal("read-only summary created the journal")
	}
	target := filepath.Join(dir, "target.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRejectionSummary(context.Background(), link, time.Now()); err == nil {
		t.Fatal("symlinked journal was read")
	}
}

func copyWALWithoutIndex(t *testing.T) string {
	t.Helper()
	source := t.TempDir()
	j, err := Open(filepath.Join(source, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	appendEvent(t, j, "evt-1")
	appendEvent(t, j, "evt-2")
	dir := t.TempDir()
	for _, name := range []string{"journal.db", "journal.db-wal"} {
		raw, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "journal.db")
}

func TestReadOnlySummaryReportsMissingWALIndexWithoutCreatingIt(t *testing.T) {
	path := copyWALWithoutIndex(t)
	walInfo, err := os.Stat(path + "-wal")
	if err != nil || walInfo.Size() == 0 {
		t.Fatalf("fixture has no committed WAL content: %v, %v", walInfo, err)
	}
	stats := func() []os.FileInfo {
		out := []os.FileInfo{}
		for _, name := range []string{path, path + "-wal"} {
			info, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, info)
		}
		return out
	}
	before := stats()
	_, err = ReadRejectionSummary(context.Background(), path, time.Now())
	if !errors.Is(err, ErrWALIndexMissing) {
		t.Fatalf("ReadRejectionSummary error = %v, want ErrWALIndexMissing", err)
	}
	if _, err := os.Lstat(path + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("read-only summary created the WAL index: %v", err)
	}
	for i, info := range stats() {
		if info.Mode() != before[i].Mode() || info.Size() != before[i].Size() || !info.ModTime().Equal(before[i].ModTime()) {
			t.Fatalf("read-only summary changed %s", info.Name())
		}
	}
}

func TestReadOnlySummaryWithBothSidecarsReadsNormally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	appendEvent(t, j, "evt-1")
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(path + suffix); err != nil {
			t.Fatalf("open journal has no %s: %v", suffix, err)
		}
	}
	summary, err := ReadRejectionSummary(context.Background(), path, time.Now())
	if err != nil || summary.UnacknowledgedEvents != 1 {
		t.Fatalf("summary = %+v, %v", summary, err)
	}
}
