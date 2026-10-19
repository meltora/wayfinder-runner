package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/validation"
)

type artifactUploader struct {
	baseURL    string
	httpClient *http.Client
	mu         sync.RWMutex
}

func (u *artifactUploader) SetHTTPClient(client *http.Client) {
	if client == nil {
		return
	}
	u.mu.Lock()
	u.httpClient = client
	u.mu.Unlock()
}

func newArtifactUploader(baseURL string) *artifactUploader {
	return &artifactUploader{baseURL: strings.TrimSuffix(baseURL, "/"), httpClient: &http.Client{Timeout: 5 * time.Minute}}
}

func deriveHTTPURL(wsURL string) string {
	u := wsURL
	u = strings.Replace(u, "wss://", "https://", 1)
	u = strings.Replace(u, "ws://", "http://", 1)
	if i := strings.Index(u, "/v1/"); i >= 0 {
		u = u[:i]
	}
	return u
}

func (u *artifactUploader) Upload(ctx context.Context, pending pendingArtifactUpload, body io.Reader) error {
	endpoint := fmt.Sprintf("%s/v1/artifacts/%s/%s/%s", u.baseURL,
		url.PathEscape(pending.Artifact.JobID), url.PathEscape(pending.Artifact.AttemptID), url.PathEscape(pending.Artifact.Name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, body)
	if err != nil {
		return err
	}
	// The uploader owns the stream lifecycle. Prevent net/http from closing an
	// *os.File supplied as io.Reader so the caller can verify and close it.
	req.Body = io.NopCloser(body)
	req.ContentLength = pending.Artifact.SizeBytes
	if pending.Artifact.SizeBytes == 0 {
		req.Body = http.NoBody
	}
	setArtifactUploadHeaders(req, pending)
	u.mu.RLock()
	client := u.httpClient
	u.mu.RUnlock()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		rejection := artifactUploadStatusError{status: resp.StatusCode, body: string(body)}
		if definitiveArtifactUploadRejection(resp.StatusCode) {
			return permanentUpload(rejection)
		}
		return rejection
	}
	return verifyArtifactUploadReceipt(resp.Body, pending)
}

func setArtifactUploadHeaders(req *http.Request, pending pendingArtifactUpload) {
	req.Header.Set("Content-Type", pending.Artifact.ContentType)
	req.Header.Set("X-Artifact-Upload-ID", pending.RecordID)
	req.Header.Set("X-Artifact-Digest", pending.Artifact.DigestSHA256)
	req.Header.Set("X-Fencing-Token", strconv.FormatUint(pending.Artifact.FencingToken, 10))
	// Replay the immutable queued authority, including after the runtime lease
	// has been released. The gateway independently verifies this stored graph.
	req.Header.Set("X-Manifest-Kind", string(pending.Authority.Kind))
	req.Header.Set("X-Manifest-ID", pending.Authority.ManifestID)
	req.Header.Set("X-Manifest-Digest", pending.Authority.ManifestDigest)
	req.Header.Set("X-Lease-ID", pending.Authority.LeaseID)
	req.Header.Set("X-Workflow-Revision", strconv.Itoa(pending.Authority.WorkflowRevision))
	if pending.AgentRunAuthority != nil {
		req.Header.Set("X-Agent-Run-ID", pending.AgentRunAuthority.AgentRunID)
		req.Header.Set("X-Step-Run-ID", pending.AgentRunAuthority.StepRunID)
		req.Header.Set("X-Step-Sequence", strconv.FormatUint(pending.AgentRunAuthority.StepSequence, 10))
		req.Header.Set("X-Agent-Run-Schema-Version", strconv.Itoa(pending.AgentRunAuthority.SchemaVersion))
	}
}

type artifactUploadStatusError struct {
	status int
	body   string
}

func (e artifactUploadStatusError) Error() string {
	return fmt.Sprintf("artifact upload rejected: status %d: %s", e.status, e.body)
}

func definitiveArtifactUploadRejection(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		http.StatusGone, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

func continuesArtifactUploadBatch(err error) bool {
	var rejection artifactUploadStatusError
	if !errors.As(err, &rejection) {
		return false
	}
	switch rejection.status {
	case http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return false
	}
	return true
}

type pendingArtifactUpload struct {
	RecordID          string                `json:"record_id"`
	Authority         v1.ManifestAuthority  `json:"authority"`
	AgentRunAuthority *v1.AgentRunAuthority `json:"agent_run_authority,omitempty"`
	Artifact          v1.ArtifactRef        `json:"artifact"`
	Path              string                `json:"path"`
}

type permanentUploadError struct{ err error }

func (e permanentUploadError) Error() string { return e.err.Error() }
func (e permanentUploadError) Unwrap() error { return e.err }

func permanentUpload(err error) error {
	if err == nil {
		return nil
	}
	return permanentUploadError{err: err}
}

func (d *Daemon) queueArtifactUpload(ctx context.Context, authority v1.ManifestAuthority, agentRunAuthority *v1.AgentRunAuthority, ref v1.ArtifactRef, storedPath string) (*pendingArtifactUpload, error) {
	relative, err := filepath.Rel(d.collectors.Root, storedPath)
	if err != nil {
		return nil, err
	}
	var authorityCopy *v1.AgentRunAuthority
	if agentRunAuthority != nil {
		value := *agentRunAuthority
		authorityCopy = &value
	}
	pending := &pendingArtifactUpload{
		RecordID: "upload-" + newEventID(), Authority: authority, AgentRunAuthority: authorityCopy,
		Artifact: ref, Path: filepath.ToSlash(relative),
	}
	raw, err := json.Marshal(pending)
	if err != nil {
		return nil, err
	}
	if err := d.journal.Append(ctx, journal.Record{
		ID: pending.RecordID, Kind: journal.KindUpload, Payload: raw, CreatedAt: time.Now().UTC(),
	}); err != nil {
		return nil, err
	}
	return pending, nil
}

func (d *Daemon) processArtifactUpload(ctx context.Context, pending pendingArtifactUpload) error {
	if err := validation.ID("upload_record_id", pending.RecordID); err != nil {
		return permanentUpload(err)
	}
	if err := validation.ManifestAuthority(pending.Authority); err != nil {
		return permanentUpload(err)
	}
	if err := validation.ID("job_id", pending.Artifact.JobID); err != nil {
		return permanentUpload(err)
	}
	if err := validation.ID("attempt_id", pending.Artifact.AttemptID); err != nil {
		return permanentUpload(err)
	}
	if err := validation.DisplayName("artifact name", pending.Artifact.Name); err != nil {
		return permanentUpload(err)
	}
	if len(pending.Artifact.DigestSHA256) != 64 {
		return permanentUpload(fmt.Errorf("artifact digest is invalid"))
	}
	if _, err := hex.DecodeString(pending.Artifact.DigestSHA256); err != nil || pending.Artifact.DigestSHA256 != strings.ToLower(pending.Artifact.DigestSHA256) {
		return permanentUpload(fmt.Errorf("artifact digest is invalid"))
	}
	if pending.Artifact.SizeBytes < 0 || pending.Artifact.SizeBytes > 256<<20 {
		return permanentUpload(fmt.Errorf("artifact size is invalid"))
	}
	if (pending.AgentRunAuthority != nil && strings.TrimSpace(pending.Artifact.ContentType) == "") || len(pending.Artifact.ContentType) > 200 {
		return permanentUpload(fmt.Errorf("artifact content type is invalid"))
	}
	if pending.Artifact.ContentType != "" {
		if _, _, err := mime.ParseMediaType(pending.Artifact.ContentType); err != nil {
			return permanentUpload(fmt.Errorf("artifact content type is invalid"))
		}
	}
	if pending.AgentRunAuthority != nil {
		if err := validation.AgentRunAuthority(*pending.AgentRunAuthority); err != nil {
			return permanentUpload(err)
		}
	}
	file, err := safeio.OpenRegular(d.collectors.Root, pending.Path)
	if err != nil {
		return permanentUpload(err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return permanentUpload(err)
	}
	if info.Size() != pending.Artifact.SizeBytes {
		_ = file.Close()
		return permanentUpload(fmt.Errorf("artifact size changed while queued"))
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return permanentUpload(err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != pending.Artifact.DigestSHA256 {
		_ = file.Close()
		return permanentUpload(fmt.Errorf("artifact digest changed while queued"))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return permanentUpload(err)
	}
	if err := d.uploader.Upload(ctx, pending, file); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// The authenticated HTTP receipt already records durable content. A generic
	// metadata event could outlive its active lease and must not be replayed.
	if err := d.journal.Ack(ctx, pending.RecordID); err != nil {
		return err
	}
	if err := safeio.RemoveRegular(d.collectors.Root, pending.Path); err != nil && !os.IsNotExist(err) {
		d.logger.Warn("remove uploaded artifact", "record_id", pending.RecordID, "err", err)
	}
	return nil
}

func (d *Daemon) drainArtifactUploads(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if err := d.processArtifactUploadBatch(ctx); err != nil && ctx.Err() == nil {
			d.logger.Warn("artifact upload queue", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Daemon) processArtifactUploadBatch(ctx context.Context) error {
	boundary, err := d.journal.LatestSequence(ctx)
	if err != nil {
		return err
	}
	var cursor journal.Sequence
	var deferred error
	for {
		records, err := d.journal.UnackedAfter(ctx, journal.KindUpload, cursor, 100)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			return deferred
		}
		for _, record := range records {
			if record.Sequence > boundary {
				return deferred
			}
			cursor = record.Sequence
			if err := d.processQueuedArtifactUpload(ctx, record); err != nil {
				if !continuesArtifactUploadBatch(err) || ctx.Err() != nil {
					return err
				}
				if deferred == nil {
					deferred = err
				}
			}
		}
	}
}

func (d *Daemon) processQueuedArtifactUpload(ctx context.Context, record journal.Record) error {
	var pending pendingArtifactUpload
	decoder := json.NewDecoder(bytes.NewReader(record.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pending); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return d.rejectArtifactUpload(ctx, record.ID, "invalid durable upload record")
	}
	if pending.RecordID != record.ID {
		return d.rejectArtifactUpload(ctx, record.ID, "durable upload identity mismatch")
	}
	uploadCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	err := d.processArtifactUpload(uploadCtx, pending)
	cancel()
	var permanent permanentUploadError
	if err == nil || !errors.As(err, &permanent) {
		return err
	}
	d.logger.Warn("artifact upload rejected permanently", "record_id", record.ID,
		"job", pending.Artifact.JobID, "attempt", pending.Artifact.AttemptID, "err", err)
	return d.rejectArtifactUpload(ctx, record.ID, truncate(err.Error(), 500))
}

func (d *Daemon) rejectArtifactUpload(ctx context.Context, recordID, reason string) error {
	if err := validation.ID("upload_record_id", recordID); err != nil {
		return err
	}
	if err := d.sendEventWithID(ctx, "artifact-rejected-"+recordID, map[string]any{
		"kind": "artifact_upload_rejected", "record_id": recordID, "reason": reason,
	}); err != nil {
		return err
	}
	return d.journal.Ack(ctx, recordID)
}
