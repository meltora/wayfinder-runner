package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
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
	PreviewID               string
	PreviewKey              string
	IdempotencyKey          string
	OperationDigest         string
	OrganizationID          string
	ProjectID               string
	ExecutionManifestID     string
	ExecutionManifestDigest string
	CommitSetDigest         string
	BlueprintID             string
	BlueprintVersion        string
	BlueprintDigest         string
	MemoryMB                int
	OriginalExpiresAt       time.Time
	ExpiresAt               time.Time
	State                   PreviewHostState
	Result                  []byte
	DestroyReason           v1.PreviewHostedDestroyReason
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type PreviewHostedLifecycleResult struct {
	IdempotencyKey  string
	OperationDigest string
	EventID         string
	Result          []byte
	CreatedAt       time.Time
}

type PreviewHostUpdate struct {
	IdempotencyKey string
	State          PreviewHostState
	Result         []byte
	ExpiresAt      *time.Time
	DestroyReason  v1.PreviewHostedDestroyReason
}

func validPreviewHostDestroyReason(reason v1.PreviewHostedDestroyReason) bool {
	switch reason {
	case "", v1.PreviewHostedDestroyExpired, v1.PreviewHostedDestroyEmergencyStop, v1.PreviewHostedDestroyReconcileExpired:
		return true
	}
	return false
}

func (j *Journal) ensurePreviewHostingSchema() error {
	_, err := j.db.Exec(`
		CREATE TABLE IF NOT EXISTS trusted_commit_sets (
			manifest_id TEXT NOT NULL,
			manifest_digest TEXT NOT NULL,
			commit_set_digest TEXT NOT NULL,
			repository_id TEXT NOT NULL,
			commit_sha TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (manifest_id, manifest_digest, commit_set_digest, repository_id)
		);
		CREATE TABLE IF NOT EXISTS preview_hosts (
			idempotency_key TEXT PRIMARY KEY,
			preview_id TEXT NOT NULL,
			preview_key TEXT NOT NULL,
			operation_digest TEXT NOT NULL,
			organization_id TEXT NOT NULL,
			project_id TEXT NOT NULL,
			execution_manifest_id TEXT NOT NULL,
			execution_manifest_digest TEXT NOT NULL,
			commit_set_digest TEXT NOT NULL,
			blueprint_id TEXT NOT NULL,
			blueprint_version TEXT NOT NULL,
			blueprint_digest TEXT NOT NULL,
			memory_mb INTEGER NOT NULL,
			original_expires_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('deploying', 'deployed', 'destroying', 'destroyed', 'failed', 'uncertain')),
			result BLOB,
			destroy_reason TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_preview_hosts_preview ON preview_hosts(preview_id, created_at);
		CREATE INDEX IF NOT EXISTS idx_preview_hosts_state ON preview_hosts(state);
		CREATE TABLE IF NOT EXISTS preview_hosted_lifecycle_results (
			idempotency_key TEXT PRIMARY KEY,
			operation_digest TEXT NOT NULL,
			event_id TEXT NOT NULL,
			result BLOB NOT NULL,
			created_at INTEGER NOT NULL
		);
	`)
	if err != nil {
		return err
	}
	return j.ensurePreviewHostDestroyReasonColumn()
}

func (j *Journal) ensurePreviewHostDestroyReasonColumn() error {
	rows, err := j.db.Query(`PRAGMA table_info(preview_hosts)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&index, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "destroy_reason" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	if _, err := j.db.Exec(`ALTER TABLE preview_hosts ADD COLUMN destroy_reason TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add preview host destroy_reason column: %w", err)
	}
	return nil
}

func sortedTrustedCommits(commits []v1.PreviewObservationCommit) []v1.PreviewObservationCommit {
	sorted := append([]v1.PreviewObservationCommit(nil), commits...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].RepositoryID < sorted[b].RepositoryID })
	return sorted
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
	sorted := sortedTrustedCommits(commits)
	commitSetDigest := v1.PreviewLifecycleCommitSetDigest(sorted)
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, commit := range sorted {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO trusted_commit_sets
				(manifest_id, manifest_digest, commit_set_digest, repository_id, commit_sha, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, manifestID, manifestDigest, commitSetDigest,
			commit.RepositoryID, commit.CommitSHA, at.UTC().Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (j *Journal) TrustedCommitSetRecorded(
	ctx context.Context,
	manifestID, manifestDigest, commitSetDigest string,
	commits []v1.PreviewObservationCommit,
) (bool, error) {
	requested := sortedTrustedCommits(commits)
	if len(requested) == 0 || commitSetDigest != v1.PreviewLifecycleCommitSetDigest(requested) {
		return false, nil
	}
	rows, err := j.db.QueryContext(ctx, `
		SELECT repository_id, commit_sha FROM trusted_commit_sets
		WHERE manifest_id=? AND manifest_digest=? AND commit_set_digest=?`, manifestID, manifestDigest, commitSetDigest)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	var recorded []v1.PreviewObservationCommit
	for rows.Next() {
		var commit v1.PreviewObservationCommit
		if err := rows.Scan(&commit.RepositoryID, &commit.CommitSHA); err != nil {
			return false, err
		}
		recorded = append(recorded, commit)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return slices.Equal(sortedTrustedCommits(recorded), requested), nil
}

func validatePreviewHost(host PreviewHost) error {
	for field, value := range map[string]string{
		"preview_id": host.PreviewID, "organization_id": host.OrganizationID, "project_id": host.ProjectID,
		"idempotency_key": host.IdempotencyKey, "blueprint_id": host.BlueprintID, "blueprint_version": host.BlueprintVersion,
		"execution_manifest_id": host.ExecutionManifestID,
	} {
		if err := validation.ID(field, value); err != nil {
			return err
		}
	}
	if err := validation.PreviewKey(host.PreviewKey); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"operation_digest": host.OperationDigest, "blueprint_digest": host.BlueprintDigest,
		"execution_manifest_digest": host.ExecutionManifestDigest, "commit_set_digest": host.CommitSetDigest,
	} {
		if err := validation.SHA256Digest(field, value); err != nil {
			return err
		}
	}
	if host.MemoryMB <= 0 {
		return fmt.Errorf("preview host memory_mb is invalid")
	}
	if host.ExpiresAt.IsZero() || host.OriginalExpiresAt.IsZero() {
		return fmt.Errorf("preview host expires_at and original_expires_at are required")
	}
	if !host.State.valid() {
		return fmt.Errorf("preview host state is invalid")
	}
	if !validPreviewHostDestroyReason(host.DestroyReason) {
		return fmt.Errorf("preview host destroy reason is invalid")
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
			 execution_manifest_id, execution_manifest_digest, commit_set_digest,
			 blueprint_id, blueprint_version, blueprint_digest, memory_mb, original_expires_at, expires_at,
			 state, result, destroy_reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		host.IdempotencyKey, host.PreviewID, host.PreviewKey, host.OperationDigest, host.OrganizationID, host.ProjectID,
		host.ExecutionManifestID, host.ExecutionManifestDigest, host.CommitSetDigest,
		host.BlueprintID, host.BlueprintVersion, host.BlueprintDigest, host.MemoryMB,
		host.OriginalExpiresAt.UTC().UnixMilli(), host.ExpiresAt.UTC().UnixMilli(),
		string(host.State), host.Result, string(host.DestroyReason), now.UnixNano(), now.UnixNano()); err != nil {
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

func validatePreviewHostUpdate(update PreviewHostUpdate) error {
	if !update.State.valid() {
		return fmt.Errorf("preview host state is invalid")
	}
	if !validPreviewHostDestroyReason(update.DestroyReason) {
		return fmt.Errorf("preview host destroy reason is invalid")
	}
	if len(update.Result) > maxPreviewHostResultBytes {
		return fmt.Errorf("preview host result is too large")
	}
	if update.ExpiresAt != nil && update.ExpiresAt.IsZero() {
		return fmt.Errorf("preview host expires_at is required")
	}
	return nil
}

func (j *Journal) UpdatePreviewHost(ctx context.Context, update PreviewHostUpdate) error {
	if err := validatePreviewHostUpdate(update); err != nil {
		return err
	}
	if err := j.ensureCapacity(ctx, len(update.Result)+256); err != nil {
		return err
	}
	return applyPreviewHostUpdate(ctx, j.db, update)
}

type previewHostExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func applyPreviewHostUpdate(ctx context.Context, execer previewHostExecer, update PreviewHostUpdate) error {
	var expiresAt any
	if update.ExpiresAt != nil {
		expiresAt = update.ExpiresAt.UTC().UnixMilli()
	}
	var result any
	if update.Result != nil {
		result = update.Result
	}
	outcome, err := execer.ExecContext(ctx, `
		UPDATE preview_hosts SET state=?, result=COALESCE(?, result), expires_at=COALESCE(?, expires_at),
			destroy_reason=COALESCE(NULLIF(?, ''), destroy_reason), updated_at=?
		WHERE idempotency_key=?`,
		string(update.State), result, expiresAt, string(update.DestroyReason), time.Now().UTC().UnixNano(), update.IdempotencyKey)
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

func validatePreviewHostedLifecycleResult(record PreviewHostedLifecycleResult) error {
	if err := validation.ID("idempotency_key", record.IdempotencyKey); err != nil {
		return err
	}
	if err := validation.ID("event_id", record.EventID); err != nil {
		return err
	}
	if err := validation.SHA256Digest("operation_digest", record.OperationDigest); err != nil {
		return err
	}
	if len(record.Result) == 0 || len(record.Result) > maxPreviewHostResultBytes {
		return fmt.Errorf("preview hosted lifecycle result is invalid")
	}
	return nil
}

func (j *Journal) RecordPreviewHostedLifecycleResult(
	ctx context.Context,
	record PreviewHostedLifecycleResult,
	hostUpdate *PreviewHostUpdate,
) (PreviewHostedLifecycleResult, bool, error) {
	if err := validatePreviewHostedLifecycleResult(record); err != nil {
		return PreviewHostedLifecycleResult{}, false, err
	}
	if hostUpdate != nil {
		if err := validatePreviewHostUpdate(*hostUpdate); err != nil {
			return PreviewHostedLifecycleResult{}, false, err
		}
	}
	if err := j.ensureCapacity(ctx, len(record.Result)+1024); err != nil {
		return PreviewHostedLifecycleResult{}, false, err
	}
	conn, err := j.db.Conn(ctx)
	if err != nil {
		return PreviewHostedLifecycleResult{}, false, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return PreviewHostedLifecycleResult{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	existing, found, err := loadPreviewHostedLifecycleResult(ctx, conn, record.IdempotencyKey)
	if err != nil {
		return PreviewHostedLifecycleResult{}, false, err
	}
	if found {
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return PreviewHostedLifecycleResult{}, false, err
		}
		committed = true
		return existing, false, nil
	}
	now := time.Now().UTC()
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO preview_hosted_lifecycle_results (idempotency_key, operation_digest, event_id, result, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		record.IdempotencyKey, record.OperationDigest, record.EventID, record.Result, now.UnixNano()); err != nil {
		return PreviewHostedLifecycleResult{}, false, err
	}
	if hostUpdate != nil {
		if err := applyPreviewHostUpdate(ctx, conn, *hostUpdate); err != nil {
			return PreviewHostedLifecycleResult{}, false, err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return PreviewHostedLifecycleResult{}, false, err
	}
	committed = true
	record.Result = append([]byte(nil), record.Result...)
	record.CreatedAt = time.Unix(0, now.UnixNano()).UTC()
	return record, true, nil
}

func (j *Journal) PreviewHostedLifecycleResultByKey(ctx context.Context, idempotencyKey string) (PreviewHostedLifecycleResult, bool, error) {
	return loadPreviewHostedLifecycleResult(ctx, j.db, idempotencyKey)
}

func loadPreviewHostedLifecycleResult(
	ctx context.Context,
	queryer planPublicationQueryer,
	idempotencyKey string,
) (PreviewHostedLifecycleResult, bool, error) {
	var record PreviewHostedLifecycleResult
	var createdAt int64
	err := queryer.QueryRowContext(ctx, `
		SELECT idempotency_key, operation_digest, event_id, result, created_at
		FROM preview_hosted_lifecycle_results WHERE idempotency_key=?`, idempotencyKey).
		Scan(&record.IdempotencyKey, &record.OperationDigest, &record.EventID, &record.Result, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PreviewHostedLifecycleResult{}, false, nil
	}
	if err != nil {
		return PreviewHostedLifecycleResult{}, false, err
	}
	record.CreatedAt = time.Unix(0, createdAt).UTC()
	return record, true, nil
}

func (j *Journal) PreviewHostByIdempotencyKey(ctx context.Context, idempotencyKey string) (PreviewHost, bool, error) {
	return loadPreviewHost(ctx, j.db, `WHERE idempotency_key=?`, idempotencyKey)
}

func (j *Journal) PreviewHostByPreviewID(ctx context.Context, previewID string) (PreviewHost, bool, error) {
	return loadPreviewHost(ctx, j.db, `WHERE preview_id=? ORDER BY created_at DESC, rowid DESC LIMIT 1`, previewID)
}

func (j *Journal) LivePreviewHosts(ctx context.Context) ([]PreviewHost, error) {
	return j.PreviewHostsInStates(ctx, PreviewHostDeploying, PreviewHostDeployed)
}

func (j *Journal) PreviewHostsInStates(ctx context.Context, states ...PreviewHostState) ([]PreviewHost, error) {
	if len(states) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(states))
	args := make([]any, len(states))
	for index, state := range states {
		if !state.valid() {
			return nil, fmt.Errorf("preview host state is invalid")
		}
		placeholders[index] = "?"
		args[index] = string(state)
	}
	rows, err := j.db.QueryContext(ctx, previewHostColumns+` WHERE state IN (`+strings.Join(placeholders, ", ")+`) ORDER BY created_at, rowid`, args...)
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
		execution_manifest_id, execution_manifest_digest, commit_set_digest,
		blueprint_id, blueprint_version, blueprint_digest, memory_mb, original_expires_at, expires_at,
		state, result, destroy_reason, created_at, updated_at
	FROM preview_hosts`

type previewHostScanner interface {
	Scan(...any) error
}

func scanPreviewHost(row previewHostScanner) (PreviewHost, error) {
	var host PreviewHost
	var state, destroyReason string
	var originalExpiresAt, expiresAt, createdAt, updatedAt int64
	var result []byte
	if err := row.Scan(&host.IdempotencyKey, &host.PreviewID, &host.PreviewKey, &host.OperationDigest,
		&host.OrganizationID, &host.ProjectID, &host.ExecutionManifestID, &host.ExecutionManifestDigest, &host.CommitSetDigest,
		&host.BlueprintID, &host.BlueprintVersion, &host.BlueprintDigest,
		&host.MemoryMB, &originalExpiresAt, &expiresAt, &state, &result, &destroyReason, &createdAt, &updatedAt); err != nil {
		return PreviewHost{}, err
	}
	host.State = PreviewHostState(state)
	host.DestroyReason = v1.PreviewHostedDestroyReason(destroyReason)
	host.OriginalExpiresAt = time.UnixMilli(originalExpiresAt).UTC()
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
