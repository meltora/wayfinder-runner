package journal

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/lease"

	_ "modernc.org/sqlite"
)

type Kind string

const (
	KindCommand Kind = "command"
	KindEvent   Kind = "event"
	KindUpload  Kind = "upload"
)

type Record struct {
	ID        string
	Kind      Kind
	Payload   []byte
	CreatedAt time.Time
	AckedAt   *time.Time
}

type Journal struct {
	db       *sql.DB
	path     string
	maxBytes int64
}

func Open(path string) (*Journal, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("journal path must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}
	j := &Journal{db: db, path: path, maxBytes: 512 << 20}
	if err := j.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure journal: %w", err)
	}
	return j, nil
}

func (j *Journal) SetMaxBytes(limit int64) { j.maxBytes = limit }

func (j *Journal) ensureCapacity(ctx context.Context, incoming int) error {
	if incoming < 0 || int64(incoming) > j.maxBytes {
		return fmt.Errorf("journal record exceeds capacity")
	}
	var pages, freePages, pageSize int64
	if err := j.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		return err
	}
	if err := j.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
		return err
	}
	if err := j.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return err
	}
	used := (pages - freePages) * pageSize
	for _, suffix := range []string{"-wal", "-shm"} {
		if info, err := os.Stat(j.path + suffix); err == nil {
			used += info.Size()
		}
	}
	if used+int64(incoming) > j.maxBytes {
		return fmt.Errorf("journal capacity of %d bytes is exhausted", j.maxBytes)
	}
	return nil
}

func (j *Journal) migrate() error {
	_, err := j.db.Exec(`
		CREATE TABLE IF NOT EXISTS records (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			payload BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			acked_at INTEGER
		);
			CREATE INDEX IF NOT EXISTS idx_records_unacked ON records(kind) WHERE acked_at IS NULL;
			CREATE TABLE IF NOT EXISTS active_lease (
				singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
				job_id TEXT NOT NULL,
				attempt_id TEXT NOT NULL,
				kind TEXT NOT NULL,
				fencing_token INTEGER NOT NULL,
				acquired_at INTEGER NOT NULL,
				expires_at INTEGER NOT NULL
			);
			CREATE TABLE IF NOT EXISTS tool_calls (
				attempt_id TEXT NOT NULL,
				tool TEXT NOT NULL,
				idempotency_key TEXT NOT NULL,
				status TEXT NOT NULL CHECK (status IN ('pending', 'completed')),
				result BLOB,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL,
				PRIMARY KEY (attempt_id, tool, idempotency_key)
			);
	`)
	return err
}

func (j *Journal) Append(ctx context.Context, r Record) error {
	if exists, err := j.recordExists(ctx, r.ID); err != nil {
		return err
	} else if exists {
		return nil
	}
	if err := j.ensureCapacity(ctx, len(r.Payload)+len(r.ID)+64); err != nil {
		return err
	}
	_, err := j.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO records (id, kind, payload, created_at) VALUES (?, ?, ?, ?)`,
		r.ID, string(r.Kind), r.Payload, r.CreatedAt.Unix())
	return err
}

func (j *Journal) AppendIfNew(ctx context.Context, r Record) (bool, error) {
	if exists, err := j.recordExists(ctx, r.ID); err != nil {
		return false, err
	} else if exists {
		return false, nil
	}
	if err := j.ensureCapacity(ctx, len(r.Payload)+len(r.ID)+64); err != nil {
		return false, err
	}
	res, err := j.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO records (id, kind, payload, created_at) VALUES (?, ?, ?, ?)`,
		r.ID, string(r.Kind), r.Payload, r.CreatedAt.Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (j *Journal) recordExists(ctx context.Context, id string) (bool, error) {
	var exists int
	err := j.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM records WHERE id=?)`, id).Scan(&exists)
	return exists == 1, err
}

func (j *Journal) Ack(ctx context.Context, id string) error {
	res, err := j.db.ExecContext(ctx,
		`UPDATE records SET acked_at = ? WHERE id = ? AND acked_at IS NULL`, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("record %s not found or already acked", id)
	}
	return nil
}

func (j *Journal) AckKind(ctx context.Context, id string, kind Kind) error {
	res, err := j.db.ExecContext(ctx,
		`UPDATE records SET acked_at = ? WHERE id = ? AND kind = ? AND acked_at IS NULL`, time.Now().Unix(), id, string(kind))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%s record %s not found or already acked", kind, id)
	}
	return nil
}

func (j *Journal) Unacked(ctx context.Context, kind Kind, limit int) ([]Record, error) {
	return j.UnackedPage(ctx, kind, limit, 0)
}

func (j *Journal) UnackedPage(ctx context.Context, kind Kind, limit, offset int) ([]Record, error) {
	rows, err := j.db.QueryContext(ctx,
		`SELECT id, kind, payload, created_at FROM records WHERE kind = ? AND acked_at IS NULL ORDER BY created_at ASC, id ASC LIMIT ? OFFSET ?`,
		string(kind), limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Record
	for rows.Next() {
		var r Record
		var created int64
		var k string
		if err := rows.Scan(&r.ID, &k, &r.Payload, &created); err != nil {
			return nil, err
		}
		r.Kind = Kind(k)
		r.CreatedAt = time.Unix(created, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (j *Journal) UnackedCount(ctx context.Context) (int, error) {
	var n int
	err := j.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM records WHERE acked_at IS NULL`).Scan(&n)
	return n, err
}

func (j *Journal) SaveLease(ctx context.Context, l lease.Lease) error {
	_, err := j.db.ExecContext(ctx, `
		INSERT INTO active_lease (singleton, job_id, attempt_id, kind, fencing_token, acquired_at, expires_at)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
			job_id=excluded.job_id, attempt_id=excluded.attempt_id, kind=excluded.kind,
			fencing_token=excluded.fencing_token, acquired_at=excluded.acquired_at, expires_at=excluded.expires_at`,
		l.JobID, l.AttemptID, string(l.Kind), l.FencingToken, l.AcquiredAt.Unix(), l.ExpiresAt.Unix())
	return err
}

func (j *Journal) LoadLease(ctx context.Context) (*lease.Lease, error) {
	var l lease.Lease
	var kind string
	var acquired, expires int64
	err := j.db.QueryRowContext(ctx, `SELECT job_id, attempt_id, kind, fencing_token, acquired_at, expires_at FROM active_lease WHERE singleton=1`).
		Scan(&l.JobID, &l.AttemptID, &kind, &l.FencingToken, &acquired, &expires)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	l.Kind = v1.JobKind(kind)
	l.AcquiredAt = time.Unix(acquired, 0)
	l.ExpiresAt = time.Unix(expires, 0)
	return &l, nil
}

func (j *Journal) ClearLease(ctx context.Context, attemptID string) error {
	_, err := j.db.ExecContext(ctx, `DELETE FROM active_lease WHERE singleton=1 AND attempt_id=?`, attemptID)
	return err
}

type ToolCallState string

const (
	ToolCallStarted   ToolCallState = "started"
	ToolCallPending   ToolCallState = "pending"
	ToolCallCompleted ToolCallState = "completed"
)

func (j *Journal) BeginToolCall(ctx context.Context, attemptID, tool, key string) (ToolCallState, []byte, error) {
	var existingStatus string
	var existingResult []byte
	err := j.db.QueryRowContext(ctx, `
		SELECT status, result FROM tool_calls
		WHERE attempt_id=? AND tool=? AND idempotency_key=?`, attemptID, tool, key).Scan(&existingStatus, &existingResult)
	if err == nil {
		if existingStatus == string(ToolCallCompleted) {
			return ToolCallCompleted, existingResult, nil
		}
		return ToolCallPending, nil, nil
	}
	if err != sql.ErrNoRows {
		return "", nil, err
	}
	if err := j.ensureCapacity(ctx, len(attemptID)+len(tool)+len(key)+128); err != nil {
		return "", nil, err
	}
	now := time.Now().Unix()
	res, err := j.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO tool_calls
		(attempt_id, tool, idempotency_key, status, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?)`, attemptID, tool, key, now, now)
	if err != nil {
		return "", nil, err
	}
	if rows, err := res.RowsAffected(); err != nil {
		return "", nil, err
	} else if rows == 1 {
		return ToolCallStarted, nil, nil
	}
	var status string
	var result []byte
	if err := j.db.QueryRowContext(ctx, `
		SELECT status, result FROM tool_calls
		WHERE attempt_id=? AND tool=? AND idempotency_key=?`, attemptID, tool, key).
		Scan(&status, &result); err != nil {
		return "", nil, err
	}
	if status == string(ToolCallCompleted) {
		return ToolCallCompleted, result, nil
	}
	return ToolCallPending, nil, nil
}

// ToolCallKeyOwner returns the recorded tool name for an idempotency key
// within a tool namespace prefix. It lets scoped tools fail closed when a key
// is already bound to a different scope of the same tool.
func (j *Journal) ToolCallKeyOwner(ctx context.Context, attemptID, toolPrefix, key string) (string, bool, error) {
	var tool string
	err := j.db.QueryRowContext(ctx, `
		SELECT tool FROM tool_calls
		WHERE attempt_id=? AND idempotency_key=? AND tool GLOB ?
		ORDER BY created_at ASC LIMIT 1`, attemptID, key, toolPrefix+"*").Scan(&tool)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return tool, true, nil
}

func (j *Journal) CompleteToolCall(ctx context.Context, attemptID, tool, key string, result []byte) error {
	res, err := j.db.ExecContext(ctx, `
		UPDATE tool_calls SET status='completed', result=?, updated_at=?
		WHERE attempt_id=? AND tool=? AND idempotency_key=? AND status='pending'`,
		result, time.Now().Unix(), attemptID, tool, key)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("tool call was not pending")
	}
	return nil
}

func (j *Journal) AbandonToolCall(ctx context.Context, attemptID, tool, key string) error {
	_, err := j.db.ExecContext(ctx, `
		DELETE FROM tool_calls
		WHERE attempt_id=? AND tool=? AND idempotency_key=? AND status='pending'`, attemptID, tool, key)
	return err
}

// Prune removes acknowledged records and completed idempotency entries older
// than before. Pending work is never deleted.
func (j *Journal) Prune(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit < 1 || limit > 10_000 {
		return 0, fmt.Errorf("prune limit must be between 1 and 10000")
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	records, err := tx.ExecContext(ctx, `
		DELETE FROM records WHERE rowid IN (
			SELECT rowid FROM records
			WHERE acked_at IS NOT NULL AND acked_at < ?
			ORDER BY acked_at ASC LIMIT ?
		)`, before.Unix(), limit)
	if err != nil {
		return 0, err
	}
	tools, err := tx.ExecContext(ctx, `
		DELETE FROM tool_calls WHERE rowid IN (
			SELECT rowid FROM tool_calls
			WHERE status='completed' AND updated_at < ?
			ORDER BY updated_at ASC LIMIT ?
		)`, before.Unix(), limit)
	if err != nil {
		return 0, err
	}
	recordCount, err := records.RowsAffected()
	if err != nil {
		return 0, err
	}
	toolCount, err := tools.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return recordCount + toolCount, nil
}

func (j *Journal) Checkpoint(ctx context.Context) error {
	_, err := j.db.ExecContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`)
	return err
}

func (j *Journal) Close() error { return j.db.Close() }
