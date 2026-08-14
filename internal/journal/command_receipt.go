package journal

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type CommandState string

const (
	CommandReceived   CommandState = "received"
	CommandProcessing CommandState = "processing"
	CommandAccepted   CommandState = "accepted"
	CommandRejected   CommandState = "rejected"
)

var (
	ErrCommandContentConflict = errors.New("command content conflicts with durable receipt")
	ErrCommandClaimConflict   = errors.New("command is not owned by this execution claim")
)

type CommandReceipt struct {
	ID            string
	ContentDigest string
	State         CommandState
	ClaimOwner    string
	AckStatus     string
	AckError      string
}

type commandReceiptQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (j *Journal) ensureCommandReceiptSchema() error {
	_, err := j.db.Exec(`
		CREATE TABLE IF NOT EXISTS command_receipts (
			command_id TEXT PRIMARY KEY,
			content_digest TEXT NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('received', 'processing', 'accepted', 'rejected')),
			claim_owner TEXT,
			ack_status TEXT CHECK (ack_status IS NULL OR ack_status IN ('accepted', 'rejected', 'duplicate')),
			ack_error TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY (command_id) REFERENCES records(id) ON DELETE CASCADE
		)
	`)
	return err
}

func validCommandDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == 32
}

func loadCommandReceipt(ctx context.Context, query commandReceiptQuery, id string) (CommandReceipt, bool, error) {
	var receipt CommandReceipt
	var claimOwner, ackStatus, ackError sql.NullString
	err := query.QueryRowContext(ctx, `
		SELECT command_id, content_digest, state, claim_owner, ack_status, ack_error
		FROM command_receipts WHERE command_id=?`, id).
		Scan(&receipt.ID, &receipt.ContentDigest, &receipt.State, &claimOwner, &ackStatus, &ackError)
	if errors.Is(err, sql.ErrNoRows) {
		return CommandReceipt{}, false, nil
	}
	if err != nil {
		return CommandReceipt{}, false, err
	}
	receipt.ClaimOwner = claimOwner.String
	receipt.AckStatus = ackStatus.String
	receipt.AckError = ackError.String
	return receipt, true, nil
}

func validateCommandRecord(record Record, digest string) error {
	if record.Kind != KindCommand || record.ID == "" || len(record.ID) > 128 || record.CreatedAt.IsZero() {
		return fmt.Errorf("invalid durable command record")
	}
	if !validCommandDigest(digest) {
		return fmt.Errorf("invalid command content digest")
	}
	return nil
}

func storedCommandContentDigest(raw []byte, expectedID string) (string, error) {
	var envelope v1.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", fmt.Errorf("%w: invalid stored command envelope", ErrCommandContentConflict)
	}
	if envelope.Version != v1.ProtocolVersion || envelope.Type != v1.TypeCommand || envelope.ID != expectedID {
		return "", fmt.Errorf("%w: stored command envelope identity mismatch", ErrCommandContentConflict)
	}
	digest, err := v1.CommandContentDigest(envelope.Payload)
	if err != nil {
		return "", fmt.Errorf("%w: invalid stored command content", ErrCommandContentConflict)
	}
	return digest, nil
}

func (j *Journal) ReceiveCommand(ctx context.Context, record Record, digest string) (CommandReceipt, bool, error) {
	j.commandMu.Lock()
	defer j.commandMu.Unlock()
	if err := validateCommandRecord(record, digest); err != nil {
		return CommandReceipt{}, false, err
	}
	if receipt, ok, err := loadCommandReceipt(ctx, j.db, record.ID); err != nil {
		return CommandReceipt{}, false, err
	} else if ok {
		if receipt.ContentDigest != digest {
			return CommandReceipt{}, false, ErrCommandContentConflict
		}
		return receipt, false, nil
	}
	if err := j.ensureCapacity(ctx, len(record.Payload)+len(record.ID)+256); err != nil {
		return CommandReceipt{}, false, err
	}

	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return CommandReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if receipt, ok, err := loadCommandReceipt(ctx, tx, record.ID); err != nil {
		return CommandReceipt{}, false, err
	} else if ok {
		if receipt.ContentDigest != digest {
			return CommandReceipt{}, false, ErrCommandContentConflict
		}
		return receipt, false, nil
	}

	result, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO records (id, kind, payload, created_at) VALUES (?, ?, ?, ?)`,
		record.ID, string(record.Kind), record.Payload, record.CreatedAt.Unix())
	if err != nil {
		return CommandReceipt{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return CommandReceipt{}, false, err
	}
	var kind string
	var storedPayload []byte
	var ackedAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT kind, payload, acked_at FROM records WHERE id=?`, record.ID).
		Scan(&kind, &storedPayload, &ackedAt); err != nil {
		return CommandReceipt{}, false, err
	}
	if Kind(kind) != KindCommand {
		return CommandReceipt{}, false, ErrCommandContentConflict
	}
	if inserted == 0 {
		storedDigest, err := storedCommandContentDigest(storedPayload, record.ID)
		if err != nil {
			return CommandReceipt{}, false, err
		}
		if storedDigest != digest {
			return CommandReceipt{}, false, ErrCommandContentConflict
		}
	}
	state := CommandReceived
	var ackStatus any
	if inserted == 0 && ackedAt.Valid {
		state = CommandAccepted
		// Older runners did not retain accepted/rejected outcomes. Preserve their
		// explicit duplicate acknowledgement instead of inventing an outcome.
		ackStatus = "duplicate"
	}
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO command_receipts
			(command_id, content_digest, state, ack_status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		record.ID, digest, string(state), ackStatus, now, now); err != nil {
		return CommandReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return CommandReceipt{}, false, err
	}
	return CommandReceipt{
		ID: record.ID, ContentDigest: digest, State: state, AckStatus: nullableString(ackStatus),
	}, true, nil
}

func nullableString(value any) string {
	text, _ := value.(string)
	return text
}

func validClaimOwner(owner string) bool {
	return owner != "" && len(owner) <= 128
}

func (j *Journal) ClaimCommand(ctx context.Context, id, owner string) (CommandReceipt, bool, error) {
	j.commandMu.Lock()
	defer j.commandMu.Unlock()
	if id == "" || len(id) > 128 || !validClaimOwner(owner) {
		return CommandReceipt{}, false, fmt.Errorf("invalid command claim")
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return CommandReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	receipt, ok, err := loadCommandReceipt(ctx, tx, id)
	if err != nil {
		return CommandReceipt{}, false, err
	}
	if !ok {
		return CommandReceipt{}, false, fmt.Errorf("command receipt %s not found", id)
	}
	if receipt.State == CommandAccepted || receipt.State == CommandRejected ||
		(receipt.State == CommandProcessing && receipt.ClaimOwner == owner) {
		return receipt, false, nil
	}
	if receipt.State != CommandReceived && receipt.State != CommandProcessing {
		return CommandReceipt{}, false, fmt.Errorf("invalid command state %q", receipt.State)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE command_receipts
		SET state='processing', claim_owner=?, updated_at=?
		WHERE command_id=? AND state=? AND COALESCE(claim_owner, '')=?`,
		owner, time.Now().Unix(), id, string(receipt.State), receipt.ClaimOwner)
	if err != nil {
		return CommandReceipt{}, false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return CommandReceipt{}, false, err
	}
	if updated != 1 {
		return CommandReceipt{}, false, ErrCommandClaimConflict
	}
	receipt.State = CommandProcessing
	receipt.ClaimOwner = owner
	if err := tx.Commit(); err != nil {
		return CommandReceipt{}, false, err
	}
	return receipt, true, nil
}

func validateCommandCompletion(status, errorText string) error {
	if status != "accepted" && status != "rejected" {
		return fmt.Errorf("invalid command acknowledgement status")
	}
	if len(errorText) > 500 || (status == "accepted" && errorText != "") {
		return fmt.Errorf("invalid command acknowledgement error")
	}
	return nil
}

func (j *Journal) CompleteCommand(ctx context.Context, id, owner, status, errorText string) (CommandReceipt, error) {
	j.commandMu.Lock()
	defer j.commandMu.Unlock()
	if id == "" || len(id) > 128 || !validClaimOwner(owner) {
		return CommandReceipt{}, fmt.Errorf("invalid command completion")
	}
	if err := validateCommandCompletion(status, errorText); err != nil {
		return CommandReceipt{}, err
	}
	state := CommandAccepted
	if status == "rejected" {
		state = CommandRejected
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return CommandReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE command_receipts
		SET state=?, ack_status=?, ack_error=?, updated_at=?
		WHERE command_id=? AND state='processing' AND claim_owner=?`,
		string(state), status, nullableDBText(errorText), time.Now().Unix(), id, owner)
	if err != nil {
		return CommandReceipt{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return CommandReceipt{}, err
	}
	if updated != 1 {
		receipt, ok, loadErr := loadCommandReceipt(ctx, tx, id)
		if loadErr != nil {
			return CommandReceipt{}, loadErr
		}
		if ok && receipt.State == state && receipt.AckStatus == status && receipt.AckError == errorText {
			return receipt, nil
		}
		return CommandReceipt{}, ErrCommandClaimConflict
	}
	acked, err := tx.ExecContext(ctx,
		`UPDATE records SET acked_at=? WHERE id=? AND kind=? AND acked_at IS NULL`,
		time.Now().Unix(), id, string(KindCommand))
	if err != nil {
		return CommandReceipt{}, err
	}
	ackedCount, err := acked.RowsAffected()
	if err != nil {
		return CommandReceipt{}, err
	}
	if ackedCount != 1 {
		return CommandReceipt{}, fmt.Errorf("durable command record was not pending")
	}
	if err := tx.Commit(); err != nil {
		return CommandReceipt{}, err
	}
	return CommandReceipt{
		ID: id, State: state, ClaimOwner: owner, AckStatus: status, AckError: errorText,
	}, nil
}

func nullableDBText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
