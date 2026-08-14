package journal

import (
	"context"
	"fmt"
	"time"
)

// Sequence is an opaque, monotonically increasing journal position. Callers
// may compare it and pass it back as a replay cursor, but must not persist it as
// a business identifier or infer record identity from its value.
type Sequence int64

func (j *Journal) Unacked(ctx context.Context, kind Kind, limit int) ([]Record, error) {
	return j.UnackedAfter(ctx, kind, 0, limit)
}

// LatestSequence returns a snapshot boundary for a finite replay pass.
func (j *Journal) LatestSequence(ctx context.Context) (Sequence, error) {
	var sequence int64
	err := j.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) FROM records`).Scan(&sequence)
	return Sequence(sequence), err
}

// UnackedAfter returns unacknowledged records after an opaque cursor in durable
// insertion order. Keyset pagination remains stable when earlier rows are
// acknowledged while a replay is in progress.
func (j *Journal) UnackedAfter(ctx context.Context, kind Kind, after Sequence, limit int) ([]Record, error) {
	if after < 0 {
		return nil, fmt.Errorf("journal sequence cannot be negative")
	}
	if limit < 1 || limit > 10_000 {
		return nil, fmt.Errorf("unacked limit must be between 1 and 10000")
	}
	rows, err := j.db.QueryContext(ctx,
		`SELECT sequence, id, kind, payload, created_at FROM records WHERE kind = ? AND acked_at IS NULL AND sequence > ? ORDER BY sequence ASC LIMIT ?`,
		string(kind), int64(after), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Record
	for rows.Next() {
		var record Record
		var sequence int64
		var createdAt int64
		var kindValue string
		if err := rows.Scan(&sequence, &record.ID, &kindValue, &record.Payload, &createdAt); err != nil {
			return nil, err
		}
		record.Sequence = Sequence(sequence)
		record.Kind = Kind(kindValue)
		record.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, record)
	}
	return out, rows.Err()
}
