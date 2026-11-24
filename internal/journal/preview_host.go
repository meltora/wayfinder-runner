package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

const maxPreviewHostResultBytes = 64 << 10

var (
	ErrPreviewHostConflict = errors.New("preview is already bound to a different live deploy")
	ErrPreviewHostNotFound = errors.New("preview host is not registered")
)

type PreviewHostState string

const (
	PreviewHostDeploying  PreviewHostState = "deploying"
	PreviewHostDeployed   PreviewHostState = "deployed"
	PreviewHostDestroying PreviewHostState = "destroying"
	PreviewHostDestroyed  PreviewHostState = "destroyed"
	PreviewHostFailed     PreviewHostState = "failed"
	PreviewHostUncertain  PreviewHostState = "uncertain"
)

func (state PreviewHostState) valid() bool {
	switch state {
	case PreviewHostDeploying, PreviewHostDeployed, PreviewHostDestroying, PreviewHostDestroyed, PreviewHostFailed, PreviewHostUncertain:
		return true
	}
	return false
}

func (state PreviewHostState) Live() bool {
	return state == PreviewHostDeploying || state == PreviewHostDeployed
}

type PreviewHost struct {
	PreviewID        string
	PreviewKey       string
	IdempotencyKey   string
	OperationDigest  string
	OrganizationID   string
	ProjectID        string
	BlueprintID      string
	BlueprintVersion string
	BlueprintDigest  string
	MemoryMB         int
	ExpiresAt        time.Time
	State            PreviewHostState
	Result           []byte
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type PreviewHostUpdate struct {
	IdempotencyKey string
	State          PreviewHostState
	Result         []byte
	ExpiresAt      *time.Time
}

func (j *Journal) ensurePreviewHostingSchema() error {
	_, err := j.db.Exec(`
		CREATE TABLE IF NOT EXISTS trusted_commit_sets (
			manifest_id TEXT NOT NULL,
			manifest_digest TEXT NOT NULL,
			repository_id TEXT NOT NULL,
			commit_sha TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (manifest_id, manifest_digest, repository_id)
		);
		CREATE TABLE IF NOT EXISTS preview_hosts (
			idempotency_key TEXT PRIMARY KEY,
			preview_id TEXT NOT NULL,
			preview_key TEXT NOT NULL,
			operation_digest TEXT NOT NULL,
			organization_id TEXT NOT NULL,
			project_id TEXT NOT NULL,
			blueprint_id TEXT NOT NULL,
			blueprint_version TEXT NOT NULL,
			blueprint_digest TEXT NOT NULL,
			memory_mb INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('deploying', 'deployed', 'destroying', 'destroyed', 'failed', 'uncertain')),
			result BLOB,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_preview_hosts_preview ON preview_hosts(preview_id, created_at);
		CREATE INDEX IF NOT EXISTS idx_preview_hosts_state ON preview_hosts(state);
	`)
	return err
}

func (j *Journal) RecordTrustedCommitSet(ctx context.Context, manifestID, manifestDigest string, commits []v1.PreviewObservationCommit, at time.Time) error {
	if err := validation.ID("manifest_id", manifestID); err != nil {
		return err
	}
	if err := validation.SHA256Digest("manifest_digest", manifestDigest); err != nil {
		return err
	}
	if len(commits) == 0 {
		return fmt.Errorf("trusted commit set is empty")
	}
	seen := map[string]bool{}
	for _, commit := range commits {
		if err := validation.ID("repository_id", commit.RepositoryID); err != nil {
			return err
		}
		if err := validation.GitCommitSHA("commit_sha", commit.CommitSHA); err != nil {
			return err
		}
		if seen[commit.RepositoryID] {
			return fmt.Errorf("trusted commit set repeats repository %s", commit.RepositoryID)
		}
		seen[commit.RepositoryID] = true
	}
	if err := j.ensureCapacity(ctx, len(commits)*(len(manifestID)+len(manifestDigest)+128)); err != nil {
		return err
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM trusted_commit_sets WHERE manifest_id=? AND manifest_digest=?`, manifestID, manifestDigest); err != nil {
		return err
	}
	for _, commit := range commits {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO trusted_commit_sets (manifest_id, manifest_digest, repository_id, commit_sha, created_at)
			VALUES (?, ?, ?, ?, ?)`, manifestID, manifestDigest, commit.RepositoryID, commit.CommitSHA, at.UTC().Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (j *Journal) TrustedCommitSet(ctx context.Context, manifestID, manifestDigest string) ([]v1.PreviewObservationCommit, bool, error) {
	rows, err := j.db.QueryContext(ctx, `
		SELECT repository_id, commit_sha FROM trusted_commit_sets
		WHERE manifest_id=? AND manifest_digest=?`, manifestID, manifestDigest)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	var commits []v1.PreviewObservationCommit
	for rows.Next() {
		var commit v1.PreviewObservationCommit
		if err := rows.Scan(&commit.RepositoryID, &commit.CommitSHA); err != nil {
			return nil, false, err
		}
		commits = append(commits, commit)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	sort.Slice(commits, func(a, b int) bool { return commits[a].RepositoryID < commits[b].RepositoryID })
	return commits, len(commits) > 0, nil
}

func validatePreviewHost(host PreviewHost) error {
	for field, value := range map[string]string{
		"preview_id": host.PreviewID, "organization_id": host.OrganizationID, "project_id": host.ProjectID,
		"idempotency_key": host.IdempotencyKey, "blueprint_id": host.BlueprintID, "blueprint_version": host.BlueprintVersion,
	} {
		if err := validation.ID(field, value); err != nil {
			return err
		}
	}
	if err := validation.PreviewKey(host.PreviewKey); err != nil {
		return err
	}
	for field, value := range map[string]string{"operation_digest": host.OperationDigest, "blueprint_digest": host.BlueprintDigest} {
		if err := validation.SHA256Digest(field, value); err != nil {
			return err
		}
	}
	if host.MemoryMB <= 0 {
		return fmt.Errorf("preview host memory_mb is invalid")
	}
	if host.ExpiresAt.IsZero() {
		return fmt.Errorf("preview host expires_at is required")
	}
	if !host.State.valid() {
		return fmt.Errorf("preview host state is invalid")
	}
	if len(host.Result) > maxPreviewHostResultBytes {
		return fmt.Errorf("preview host result is too large")
	}
	return nil
}

func (j *Journal) InsertPreviewHost(ctx context.Context, host PreviewHost) (PreviewHost, bool, error) {
	if err := validatePreviewHost(host); err != nil {
		return PreviewHost{}, false, err
	}
	if err := j.ensureCapacity(ctx, len(host.Result)+1024); err != nil {
		return PreviewHost{}, false, err
	}
	conn, err := j.db.Conn(ctx)
	if err != nil {
		return PreviewHost{}, false, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return PreviewHost{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	if existing, found, err := loadPreviewHost(ctx, conn, `WHERE idempotency_key=?`, host.IdempotencyKey); err != nil {
		return PreviewHost{}, false, err
	} else if found {
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return PreviewHost{}, false, err
		}
		committed = true
		return existing, false, nil
	}
	var competing int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM preview_hosts WHERE preview_id=? AND state IN ('deploying', 'deployed', 'destroying', 'uncertain')`,
		host.PreviewID).Scan(&competing); err != nil {
		return PreviewHost{}, false, err
	}
	if competing > 0 {
		return PreviewHost{}, false, ErrPreviewHostConflict
	}
	now := time.Now().UTC()
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO preview_hosts
			(idempotency_key, preview_id, preview_key, operation_digest, organization_id, project_id,
			 blueprint_id, blueprint_version, blueprint_digest, memory_mb, expires_at, state, result, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		host.IdempotencyKey, host.PreviewID, host.PreviewKey, host.OperationDigest, host.OrganizationID, host.ProjectID,
		host.BlueprintID, host.BlueprintVersion, host.BlueprintDigest, host.MemoryMB, host.ExpiresAt.UTC().UnixMilli(),
		string(host.State), host.Result, now.UnixNano(), now.UnixNano()); err != nil {
		return PreviewHost{}, false, err
	}
	stored, _, err := loadPreviewHost(ctx, conn, `WHERE idempotency_key=?`, host.IdempotencyKey)
	if err != nil {
		return PreviewHost{}, false, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return PreviewHost{}, false, err
	}
	committed = true
	return stored, true, nil
}

func (j *Journal) UpdatePreviewHost(ctx context.Context, update PreviewHostUpdate) error {
	if !update.State.valid() {
		return fmt.Errorf("preview host state is invalid")
	}
	if len(update.Result) > maxPreviewHostResultBytes {
		return fmt.Errorf("preview host result is too large")
	}
	if update.ExpiresAt != nil && update.ExpiresAt.IsZero() {
		return fmt.Errorf("preview host expires_at is required")
	}
	if err := j.ensureCapacity(ctx, len(update.Result)+256); err != nil {
		return err
	}
	var expiresAt any
	if update.ExpiresAt != nil {
		expiresAt = update.ExpiresAt.UTC().UnixMilli()
	}
	var result any
	if update.Result != nil {
		result = update.Result
	}
	outcome, err := j.db.ExecContext(ctx, `
		UPDATE preview_hosts SET state=?, result=COALESCE(?, result), expires_at=COALESCE(?, expires_at), updated_at=?
		WHERE idempotency_key=?`,
		string(update.State), result, expiresAt, time.Now().UTC().UnixNano(), update.IdempotencyKey)
	if err != nil {
		return err
	}
	changed, err := outcome.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrPreviewHostNotFound
	}
	return nil
}

func (j *Journal) PreviewHostByIdempotencyKey(ctx context.Context, idempotencyKey string) (PreviewHost, bool, error) {
	return loadPreviewHost(ctx, j.db, `WHERE idempotency_key=?`, idempotencyKey)
}

func (j *Journal) PreviewHostByPreviewID(ctx context.Context, previewID string) (PreviewHost, bool, error) {
	return loadPreviewHost(ctx, j.db, `WHERE preview_id=? ORDER BY created_at DESC, rowid DESC LIMIT 1`, previewID)
}

func (j *Journal) LivePreviewHosts(ctx context.Context) ([]PreviewHost, error) {
	rows, err := j.db.QueryContext(ctx, previewHostColumns+` WHERE state IN ('deploying', 'deployed') ORDER BY created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var hosts []PreviewHost
	for rows.Next() {
		host, err := scanPreviewHost(rows)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	return hosts, rows.Err()
}

const previewHostColumns = `
	SELECT idempotency_key, preview_id, preview_key, operation_digest, organization_id, project_id,
		blueprint_id, blueprint_version, blueprint_digest, memory_mb, expires_at, state, result, created_at, updated_at
	FROM preview_hosts`

type previewHostScanner interface {
	Scan(...any) error
}

func scanPreviewHost(row previewHostScanner) (PreviewHost, error) {
	var host PreviewHost
	var state string
	var expiresAt, createdAt, updatedAt int64
	var result []byte
	if err := row.Scan(&host.IdempotencyKey, &host.PreviewID, &host.PreviewKey, &host.OperationDigest,
		&host.OrganizationID, &host.ProjectID, &host.BlueprintID, &host.BlueprintVersion, &host.BlueprintDigest,
		&host.MemoryMB, &expiresAt, &state, &result, &createdAt, &updatedAt); err != nil {
		return PreviewHost{}, err
	}
	host.State = PreviewHostState(state)
	host.ExpiresAt = time.UnixMilli(expiresAt).UTC()
	host.CreatedAt = time.Unix(0, createdAt).UTC()
	host.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if result != nil {
		host.Result = append([]byte(nil), result...)
	}
	return host, nil
}

func loadPreviewHost(ctx context.Context, queryer planPublicationQueryer, clause string, arg string) (PreviewHost, bool, error) {
	host, err := scanPreviewHost(queryer.QueryRowContext(ctx, previewHostColumns+` `+clause, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return PreviewHost{}, false, nil
	}
	if err != nil {
		return PreviewHost{}, false, err
	}
	return host, true, nil
}
