package journal

import (
	"context"
	"database/sql"
	"errors"
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
	Sequence  Sequence
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
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			id TEXT NOT NULL UNIQUE,
			kind TEXT NOT NULL,
			payload BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			acked_at INTEGER
		);
			CREATE TABLE IF NOT EXISTS active_lease (
				singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
				lease_id TEXT NOT NULL,
				job_id TEXT NOT NULL,
				attempt_id TEXT NOT NULL,
				kind TEXT NOT NULL,
				manifest_id TEXT NOT NULL,
				manifest_digest TEXT NOT NULL,
				workflow_revision INTEGER NOT NULL,
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
		CREATE TABLE IF NOT EXISTS check_claims (
			attempt_id TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			check_id TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (attempt_id, idempotency_key)
		);
		CREATE INDEX IF NOT EXISTS idx_check_claims_check ON check_claims(attempt_id, check_id);
		CREATE TABLE IF NOT EXISTS plan_publications (
			attempt_id TEXT PRIMARY KEY,
			tool TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			event_id TEXT NOT NULL UNIQUE,
			input_digest TEXT NOT NULL,
			payload BLOB NOT NULL,
			created_at INTEGER NOT NULL
		);
`)
	if err != nil {
		return err
	}
	if err := j.ensureRecordSequenceSchema(); err != nil {
		return err
	}
	if _, err := j.db.Exec(`CREATE INDEX IF NOT EXISTS idx_records_unacked ON records(kind, sequence) WHERE acked_at IS NULL`); err != nil {
		return err
	}
	return j.ensureActiveLeaseAuthorityColumns()
}

func (j *Journal) ensureActiveLeaseAuthorityColumns() error {
	rows, err := j.db.Query(`PRAGMA table_info(active_lease)`)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&index, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	columns := []struct{ name, definition string }{
		{"lease_id", `TEXT NOT NULL DEFAULT ''`},
		{"manifest_id", `TEXT NOT NULL DEFAULT ''`},
		{"manifest_digest", `TEXT NOT NULL DEFAULT ''`},
		{"workflow_revision", `INTEGER NOT NULL DEFAULT -1`},
	}
	for _, column := range columns {
		if existing[column.name] {
			continue
		}
		if _, err := j.db.Exec(`ALTER TABLE active_lease ADD COLUMN ` + column.name + ` ` + column.definition); err != nil {
			return fmt.Errorf("add active lease authority column %s: %w", column.name, err)
		}
	}
	return nil
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

// HasRecord reports whether a record with the given ID is already durably
// journaled, regardless of kind or acknowledgement state.
func (j *Journal) HasRecord(ctx context.Context, id string) (bool, error) {
	return j.recordExists(ctx, id)
}

// LoadRecord returns the full record with the given ID regardless of
// acknowledgement state, so a caller can reconstruct durable outcomes after a
// restart. ok is false when no record exists under the ID.
func (j *Journal) LoadRecord(ctx context.Context, id string) (Record, bool, error) {
	var r Record
	var kind string
	var sequence int64
	var created int64
	err := j.db.QueryRowContext(ctx,
		`SELECT sequence, id, kind, payload, created_at FROM records WHERE id=?`, id).
		Scan(&sequence, &r.ID, &kind, &r.Payload, &created)
	if err == sql.ErrNoRows {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	r.Sequence = Sequence(sequence)
	r.Kind = Kind(kind)
	r.CreatedAt = time.Unix(created, 0)
	return r, true, nil
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

func (j *Journal) UnackedCount(ctx context.Context) (int, error) {
	var n int
	err := j.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM records WHERE acked_at IS NULL`).Scan(&n)
	return n, err
}

func (j *Journal) SaveLease(ctx context.Context, l lease.Lease) error {
	if !l.HasManifestAuthority() {
		return fmt.Errorf("refuse to persist lease without immutable manifest authority")
	}
	_, err := j.db.ExecContext(ctx, `
		INSERT INTO active_lease (singleton, lease_id, job_id, attempt_id, kind, manifest_id, manifest_digest, workflow_revision, fencing_token, acquired_at, expires_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
			lease_id=excluded.lease_id, job_id=excluded.job_id, attempt_id=excluded.attempt_id, kind=excluded.kind,
			manifest_id=excluded.manifest_id, manifest_digest=excluded.manifest_digest, workflow_revision=excluded.workflow_revision,
			fencing_token=excluded.fencing_token, acquired_at=excluded.acquired_at, expires_at=excluded.expires_at`,
		l.LeaseID, l.JobID, l.AttemptID, string(l.Kind), l.ManifestID, l.ManifestDigest, l.WorkflowRevision,
		l.FencingToken, l.AcquiredAt.Unix(), l.ExpiresAt.Unix())
	return err
}

func (j *Journal) LoadLease(ctx context.Context) (*lease.Lease, error) {
	var l lease.Lease
	var kind string
	var acquired, expires int64
	err := j.db.QueryRowContext(ctx, `SELECT lease_id, job_id, attempt_id, kind, manifest_id, manifest_digest, workflow_revision, fencing_token, acquired_at, expires_at FROM active_lease WHERE singleton=1`).
		Scan(&l.LeaseID, &l.JobID, &l.AttemptID, &kind, &l.ManifestID, &l.ManifestDigest, &l.WorkflowRevision,
			&l.FencingToken, &acquired, &expires)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	l.Kind = v1.JobKind(kind)
	l.AcquiredAt = time.Unix(acquired, 0)
	l.ExpiresAt = time.Unix(expires, 0)
	if !l.HasManifestAuthority() {
		return nil, fmt.Errorf("active lease is missing immutable manifest authority; drain the runner before upgrading")
	}
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

var (
	ErrCheckClaimConflict   = errors.New("idempotency key is bound to a different check")
	ErrCheckBudgetExhausted = errors.New("check execution budget exhausted")
)

// ClaimCheckExecution atomically binds (attemptID, idempotencyKey) within the
// platform_run_check namespace to exactly one check ID and enforces durable
// per-check and per-attempt execution budgets in the same transaction.
// A same-check replay returns claimed=false and consumes no budget slot; a
// cross-check replay fails with ErrCheckClaimConflict. Claims survive daemon
// restarts so budgets cannot be reset by restarting.
func (j *Journal) ClaimCheckExecution(ctx context.Context, attemptID, key, checkID string, perCheckLimit, perAttemptLimit int) (bool, error) {
	if perCheckLimit < 1 || perAttemptLimit < 1 {
		return false, fmt.Errorf("claim limits must be positive")
	}
	if err := j.ensureCapacity(ctx, len(attemptID)+len(key)+len(checkID)+64); err != nil {
		return false, err
	}
	conn, err := j.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	var owner string
	err = conn.QueryRowContext(ctx,
		`SELECT check_id FROM check_claims WHERE attempt_id=? AND idempotency_key=?`, attemptID, key).Scan(&owner)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err == nil {
		if _, cerr := conn.ExecContext(ctx, `COMMIT`); cerr != nil {
			return false, cerr
		}
		committed = true
		if owner != checkID {
			return false, ErrCheckClaimConflict
		}
		return false, nil
	}
	var perCheck int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM check_claims WHERE attempt_id=? AND check_id=?`, attemptID, checkID).Scan(&perCheck); err != nil {
		return false, err
	}
	if perCheck >= perCheckLimit {
		return false, ErrCheckBudgetExhausted
	}
	var perAttempt int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM check_claims WHERE attempt_id=?`, attemptID).Scan(&perAttempt); err != nil {
		return false, err
	}
	if perAttempt >= perAttemptLimit {
		return false, ErrCheckBudgetExhausted
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO check_claims (attempt_id, idempotency_key, check_id, created_at) VALUES (?, ?, ?, ?)`,
		attemptID, key, checkID, time.Now().Unix()); err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return false, err
	}
	committed = true
	return true, nil
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
		DELETE FROM records WHERE sequence IN (
			SELECT sequence FROM records
			WHERE acked_at IS NOT NULL AND acked_at < ?
			ORDER BY acked_at ASC LIMIT ?
		)`, before.Unix(), limit)
	if err != nil {
		return 0, err
	}
	publications, err := tx.ExecContext(ctx, `
		DELETE FROM plan_publications WHERE rowid IN (
			SELECT publication.rowid
			FROM plan_publications AS publication
			JOIN tool_calls AS tool_call
			  ON tool_call.attempt_id = publication.attempt_id
			 AND tool_call.tool = publication.tool
			 AND tool_call.idempotency_key = publication.idempotency_key
			WHERE tool_call.status='completed' AND tool_call.updated_at < ?
			ORDER BY tool_call.updated_at ASC LIMIT ?
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
	publicationCount, err := publications.RowsAffected()
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
	return recordCount + publicationCount + toolCount, nil
}

func (j *Journal) Checkpoint(ctx context.Context) error {
	_, err := j.db.ExecContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`)
	return err
}

func (j *Journal) Close() error { return j.db.Close() }
