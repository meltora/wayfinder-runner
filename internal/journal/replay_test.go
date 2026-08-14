package journal

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestUnackedKeysetPaginationSurvivesAcknowledgedEarlierPage(t *testing.T) {
	j := openTest(t)
	for _, id := range []string{"a", "b", "c"} {
		if err := j.Append(context.Background(), Record{ID: id, Kind: KindEvent, Payload: []byte(id), CreatedAt: time.Unix(100, 0)}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := j.UnackedAfter(context.Background(), KindEvent, 0, 2)
	if err != nil || len(first) != 2 || first[0].ID != "a" || first[1].ID != "b" {
		t.Fatalf("first page=%v err=%v", first, err)
	}
	if first[0].Sequence == 0 || first[1].Sequence <= first[0].Sequence {
		t.Fatalf("non-monotonic journal sequences: %+v", first)
	}
	if err := j.AckKind(context.Background(), "a", KindEvent); err != nil {
		t.Fatal(err)
	}
	if err := j.AckKind(context.Background(), "b", KindEvent); err != nil {
		t.Fatal(err)
	}
	second, err := j.UnackedAfter(context.Background(), KindEvent, first[1].Sequence, 2)
	if err != nil || len(second) != 1 || second[0].ID != "c" {
		t.Fatalf("second page=%v err=%v", second, err)
	}
	if second[0].Sequence <= first[1].Sequence {
		t.Fatalf("cursor did not advance: first=%+v second=%+v", first, second)
	}
}

func TestUnackedAfterValidatesCursorAndBound(t *testing.T) {
	t.Parallel()
	j := openTest(t)
	ctx := context.Background()
	for _, test := range []struct {
		after Sequence
		limit int
	}{
		{after: -1, limit: 1},
		{after: 0, limit: 0},
		{after: 0, limit: 10_001},
	} {
		if _, err := j.UnackedAfter(ctx, KindEvent, test.after, test.limit); err == nil {
			t.Fatalf("accepted cursor=%d limit=%d", test.after, test.limit)
		}
	}
}

func TestJournalSequenceNeverReusesAPrunedPosition(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	createdAt := time.Now().Add(-48 * time.Hour)
	if err := j.Append(ctx, Record{ID: "old", Kind: KindEvent, Payload: []byte("old"), CreatedAt: createdAt}); err != nil {
		t.Fatal(err)
	}
	old, err := j.Unacked(ctx, KindEvent, 1)
	if err != nil || len(old) != 1 {
		t.Fatalf("old record = %+v, %v", old, err)
	}
	if err := j.AckKind(ctx, "old", KindEvent); err != nil {
		t.Fatal(err)
	}
	if pruned, err := j.Prune(ctx, time.Now().Add(time.Hour), 10); err != nil || pruned != 1 {
		t.Fatalf("pruned = %d, %v", pruned, err)
	}
	if err := j.Append(ctx, Record{ID: "new", Kind: KindEvent, Payload: []byte("new"), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	newRecords, err := j.Unacked(ctx, KindEvent, 1)
	if err != nil || len(newRecords) != 1 {
		t.Fatalf("new record = %+v, %v", newRecords, err)
	}
	if newRecords[0].Sequence <= old[0].Sequence {
		t.Fatalf("journal sequence reused after prune: old=%d new=%d", old[0].Sequence, newRecords[0].Sequence)
	}
}

func TestOpenMigratesLegacyRecordRowIDsToDurableSequences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE records (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			payload BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			acked_at INTEGER
		);
		CREATE INDEX idx_records_unacked ON records(kind) WHERE acked_at IS NULL;
		INSERT INTO records (id, kind, payload, created_at) VALUES
			('legacy-a', 'event', 'a', 100),
			('legacy-b', 'event', 'b', 100);
	`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	records, err := j.Unacked(context.Background(), KindEvent, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ID != "legacy-a" || records[0].Sequence != 1 ||
		records[1].ID != "legacy-b" || records[1].Sequence != 2 {
		t.Fatalf("migrated records = %+v", records)
	}
	if err := j.Append(context.Background(), Record{
		ID: "post-migration", Kind: KindEvent, Payload: []byte("c"), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	boundary, err := j.LatestSequence(context.Background())
	if err != nil || boundary <= records[1].Sequence {
		t.Fatalf("post-migration sequence = %d, %v", boundary, err)
	}
}

func TestOpenRejectsReusableSequenceSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reusable.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE records (
		sequence INTEGER PRIMARY KEY,
		id TEXT NOT NULL UNIQUE,
		kind TEXT NOT NULL,
		payload BLOB NOT NULL,
		created_at INTEGER NOT NULL,
		acked_at INTEGER
	)`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("journal accepted a sequence schema that can reuse pruned positions")
	}
}
