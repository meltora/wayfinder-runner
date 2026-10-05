package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const (
	maxSessionRejectionCodes  = 32
	otherSessionRejectionCode = "other"
	invalidRejectionCode      = "invalid_code"
	sessionRejectionWindow    = 7 * 24 * time.Hour
	longRejectionAge          = 24 * time.Hour
)

var ErrWALIndexMissing = errors.New("journal WAL file exists without its index")

var rejectionCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type EventRejection struct {
	Code string
	At   time.Time
}

type SessionRejection struct {
	Code           string
	Count          int
	LastRejectedAt time.Time
}

type RejectionSummary struct {
	UnacknowledgedEvents int
	RejectedEvents       int
	LongRejectedEvents   int
	LastRejection        *EventRejection
	SessionRejections    []SessionRejection
}

type summaryQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func NormalizeRejectionCode(code string) string {
	if rejectionCodePattern.MatchString(code) {
		return code
	}
	return invalidRejectionCode
}

func (j *Journal) ensureEventRejectionSchema() error {
	_, err := j.db.Exec(`
		CREATE TABLE IF NOT EXISTS event_rejections (
			record_id TEXT PRIMARY KEY REFERENCES records(id) ON DELETE CASCADE,
			code TEXT NOT NULL,
			first_rejected_at INTEGER NOT NULL,
			last_rejected_at INTEGER NOT NULL,
			count INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS session_rejections (
			code TEXT PRIMARY KEY,
			count INTEGER NOT NULL,
			last_rejected_at INTEGER NOT NULL
		);
	`)
	return err
}

func (j *Journal) RecordEventRejection(ctx context.Context, id, code string, at time.Time) error {
	_, err := j.db.ExecContext(ctx, `
		INSERT INTO event_rejections (record_id, code, first_rejected_at, last_rejected_at, count)
		SELECT ?1, ?2, ?3, ?3, 1
		WHERE EXISTS (SELECT 1 FROM records WHERE id = ?1 AND kind = 'event' AND acked_at IS NULL)
		ON CONFLICT(record_id) DO UPDATE SET
			code = excluded.code,
			last_rejected_at = excluded.last_rejected_at,
			count = event_rejections.count + 1`,
		id, NormalizeRejectionCode(code), at.Unix())
	return err
}

func (j *Journal) RecordSessionRejection(ctx context.Context, code string, at time.Time) error {
	_, err := j.db.ExecContext(ctx, `
		INSERT INTO session_rejections (code, count, last_rejected_at)
		VALUES (
			CASE
				WHEN EXISTS (SELECT 1 FROM session_rejections WHERE code = ?1) THEN ?1
				WHEN (SELECT COUNT(*) FROM session_rejections WHERE code <> ?2) < ?3 THEN ?1
				ELSE ?2
			END,
			1, ?4)
		ON CONFLICT(code) DO UPDATE SET
			count = session_rejections.count + 1,
			last_rejected_at = excluded.last_rejected_at`,
		NormalizeRejectionCode(code), otherSessionRejectionCode, maxSessionRejectionCodes, at.Unix())
	return err
}

func (j *Journal) RejectionSummary(ctx context.Context, now time.Time) (RejectionSummary, error) {
	return readRejectionSummary(ctx, j.db, now)
}

func ReadRejectionSummary(ctx context.Context, path string, now time.Time) (RejectionSummary, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return RejectionSummary{}, err
	}
	if !info.Mode().IsRegular() {
		return RejectionSummary{}, fmt.Errorf("journal path must be a regular file")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return RejectionSummary{}, err
	}
	if !walSidecarsExist(absolute) {
		summary, err := readRejectionSummaryFile(ctx, absolute, true, now)
		if err == nil && !walSidecarsExist(absolute) {
			return summary, nil
		}
	}
	if walIndexMissing(absolute) {
		return RejectionSummary{}, ErrWALIndexMissing
	}
	return readRejectionSummaryFile(ctx, absolute, false, now)
}

func walIndexMissing(path string) bool {
	if _, err := os.Lstat(path + "-wal"); err != nil {
		return false
	}
	_, err := os.Lstat(path + "-shm")
	return os.IsNotExist(err)
}

func walSidecarsExist(path string) bool {
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(path + suffix); err == nil || !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

func readRejectionSummaryFile(ctx context.Context, absolute string, immutable bool, now time.Time) (RejectionSummary, error) {
	query := "mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)"
	if immutable {
		query = "mode=ro&immutable=1&_pragma=query_only(1)"
	}
	dsn := url.URL{Scheme: "file", Path: absolute, RawQuery: query}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return RejectionSummary{}, err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RejectionSummary{}, err
	}
	defer func() { _ = tx.Rollback() }()
	return readRejectionSummary(ctx, tx, now)
}

func readRejectionSummary(ctx context.Context, q summaryQuerier, now time.Time) (RejectionSummary, error) {
	var summary RejectionSummary
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM records WHERE kind = 'event' AND acked_at IS NULL`).
		Scan(&summary.UnacknowledgedEvents); err != nil {
		return RejectionSummary{}, err
	}
	var tables int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('event_rejections', 'session_rejections')`).
		Scan(&tables); err != nil {
		return RejectionSummary{}, err
	}
	if tables != 2 {
		return summary, nil
	}
	if err := q.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(rejection.first_rejected_at <= ?), 0)
		FROM event_rejections AS rejection
		JOIN records AS record ON record.id = rejection.record_id
		WHERE record.kind = 'event' AND record.acked_at IS NULL`,
		now.Add(-longRejectionAge).Unix()).
		Scan(&summary.RejectedEvents, &summary.LongRejectedEvents); err != nil {
		return RejectionSummary{}, err
	}
	var lastCode string
	var lastAt int64
	err := q.QueryRowContext(ctx, `
		SELECT rejection.code, rejection.last_rejected_at
		FROM event_rejections AS rejection
		JOIN records AS record ON record.id = rejection.record_id
		WHERE record.kind = 'event' AND record.acked_at IS NULL
		ORDER BY rejection.last_rejected_at DESC, record.sequence DESC
		LIMIT 1`).Scan(&lastCode, &lastAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return RejectionSummary{}, err
	default:
		summary.LastRejection = &EventRejection{Code: lastCode, At: time.Unix(lastAt, 0).UTC()}
	}
	rows, err := q.QueryContext(ctx, `
		SELECT code, count, last_rejected_at FROM session_rejections
		WHERE last_rejected_at >= ?
		ORDER BY last_rejected_at DESC, code ASC`,
		now.Add(-sessionRejectionWindow).Unix())
	if err != nil {
		return RejectionSummary{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var rejection SessionRejection
		var at int64
		if err := rows.Scan(&rejection.Code, &rejection.Count, &at); err != nil {
			return RejectionSummary{}, err
		}
		rejection.LastRejectedAt = time.Unix(at, 0).UTC()
		summary.SessionRejections = append(summary.SessionRejections, rejection)
	}
	if err := rows.Err(); err != nil {
		return RejectionSummary{}, err
	}
	return summary, nil
}
