package journal

import (
	"fmt"
	"strings"
)

func (j *Journal) ensureRecordSequenceSchema() error {
	rows, err := j.db.Query(`PRAGMA table_info(records)`)
	if err != nil {
		return err
	}
	sequencePrimaryKey := false
	hasSequence := false
	sequenceType := ""
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&index, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "sequence" {
			hasSequence = true
			sequencePrimaryKey = primaryKey == 1
			sequenceType = columnType
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if hasSequence {
		if !sequencePrimaryKey || !strings.EqualFold(sequenceType, "INTEGER") {
			return fmt.Errorf("records sequence column is not an integer primary key")
		}
		var definition string
		if err := j.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='records'`).Scan(&definition); err != nil {
			return err
		}
		normalized := strings.ToUpper(strings.Join(strings.Fields(definition), " "))
		if !strings.Contains(normalized, "SEQUENCE INTEGER PRIMARY KEY AUTOINCREMENT") {
			return fmt.Errorf("records sequence column is not monotonic AUTOINCREMENT authority")
		}
		return nil
	}

	tx, err := j.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	statements := []string{
		`DROP TABLE IF EXISTS records_sequence_migration`,
		`CREATE TABLE records_sequence_migration (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			id TEXT NOT NULL UNIQUE,
			kind TEXT NOT NULL,
			payload BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			acked_at INTEGER
		)`,
		`INSERT INTO records_sequence_migration (sequence, id, kind, payload, created_at, acked_at)
		 SELECT rowid, id, kind, payload, created_at, acked_at FROM records ORDER BY rowid ASC`,
		`DROP TABLE records`,
		`ALTER TABLE records_sequence_migration RENAME TO records`,
		`CREATE INDEX idx_records_unacked ON records(kind, sequence) WHERE acked_at IS NULL`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("migrate durable record sequence: %w", err)
		}
	}
	return tx.Commit()
}
