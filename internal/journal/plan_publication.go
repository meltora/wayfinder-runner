package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/example/app-runner/internal/validation"
)

const maxPlanPublicationPayloadBytes = 1 << 20

var ErrPlanPublicationConflict = errors.New("planning attempt is already bound to a different publication")

type PlanPublicationClaim struct {
	AttemptID      string
	Tool           string
	IdempotencyKey string
	EventID        string
	InputDigest    string
	Payload        []byte
	CreatedAt      time.Time
}

// ClaimPlanPublication durably binds a planning attempt to exactly one event
// before the generic tool-call state is opened. That ordering closes both crash
// windows: a retry can reconstruct the event if the process died before send,
// and a different idempotency key cannot publish a competing plan.
func (j *Journal) ClaimPlanPublication(ctx context.Context, claim PlanPublicationClaim) (PlanPublicationClaim, bool, error) {
	if err := validatePlanPublicationClaim(claim); err != nil {
		return PlanPublicationClaim{}, false, err
	}
	existingBeforeLock, foundBeforeLock, err := loadPlanPublicationClaim(ctx, j.db, claim.AttemptID)
	if err != nil {
		return PlanPublicationClaim{}, false, err
	}
	if foundBeforeLock {
		if !samePlanPublicationClaim(existingBeforeLock, claim) {
			return PlanPublicationClaim{}, false, ErrPlanPublicationConflict
		}
	} else {
		if err := j.ensureCapacity(ctx, len(claim.Payload)+len(claim.AttemptID)+len(claim.Tool)+len(claim.IdempotencyKey)+256); err != nil {
			return PlanPublicationClaim{}, false, err
		}
	}
	conn, err := j.db.Conn(ctx)
	if err != nil {
		return PlanPublicationClaim{}, false, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return PlanPublicationClaim{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	existing, found, err := loadPlanPublicationClaim(ctx, conn, claim.AttemptID)
	if err != nil {
		return PlanPublicationClaim{}, false, err
	}
	if found {
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return PlanPublicationClaim{}, false, err
		}
		committed = true
		if !samePlanPublicationClaim(existing, claim) {
			return PlanPublicationClaim{}, false, ErrPlanPublicationConflict
		}
		return existing, false, nil
	}
	createdAt := claim.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO plan_publications
			(attempt_id, tool, idempotency_key, event_id, input_digest, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		claim.AttemptID, claim.Tool, claim.IdempotencyKey, claim.EventID,
		claim.InputDigest, claim.Payload, createdAt.Unix()); err != nil {
		return PlanPublicationClaim{}, false, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return PlanPublicationClaim{}, false, err
	}
	committed = true
	claim.Payload = append([]byte(nil), claim.Payload...)
	claim.CreatedAt = time.Unix(createdAt.Unix(), 0).UTC()
	return claim, true, nil
}

type planPublicationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadPlanPublicationClaim(ctx context.Context, queryer planPublicationQueryer, attemptID string) (PlanPublicationClaim, bool, error) {
	var claim PlanPublicationClaim
	var createdAt int64
	err := queryer.QueryRowContext(ctx, `
		SELECT attempt_id, tool, idempotency_key, event_id, input_digest, payload, created_at
		FROM plan_publications WHERE attempt_id=?`, attemptID).
		Scan(&claim.AttemptID, &claim.Tool, &claim.IdempotencyKey, &claim.EventID,
			&claim.InputDigest, &claim.Payload, &createdAt)
	if err == sql.ErrNoRows {
		return PlanPublicationClaim{}, false, nil
	}
	if err != nil {
		return PlanPublicationClaim{}, false, err
	}
	claim.Payload = append([]byte(nil), claim.Payload...)
	claim.CreatedAt = time.Unix(createdAt, 0).UTC()
	return claim, true, nil
}

func validatePlanPublicationClaim(claim PlanPublicationClaim) error {
	for field, value := range map[string]string{
		"attempt_id": claim.AttemptID, "tool": claim.Tool,
		"idempotency_key": claim.IdempotencyKey, "event_id": claim.EventID,
	} {
		if err := validation.ID(field, value); err != nil {
			return err
		}
	}
	if err := validation.SHA256Digest("input_digest", claim.InputDigest); err != nil {
		return err
	}
	if len(claim.Payload) == 0 || len(claim.Payload) > maxPlanPublicationPayloadBytes {
		return fmt.Errorf("plan publication payload has an invalid size")
	}
	return nil
}

func samePlanPublicationClaim(left, right PlanPublicationClaim) bool {
	return left.AttemptID == right.AttemptID && left.Tool == right.Tool &&
		left.IdempotencyKey == right.IdempotencyKey && left.EventID == right.EventID &&
		left.InputDigest == right.InputDigest
}

func (j *Journal) PlanPublicationQueued(ctx context.Context, attemptID string) (bool, error) {
	claim, found, err := loadPlanPublicationClaim(ctx, j.db, attemptID)
	if err != nil || !found {
		return false, err
	}
	record, found, err := j.LoadRecord(ctx, claim.EventID)
	if err != nil || !found {
		return false, err
	}
	return record.Kind == KindEvent, nil
}
