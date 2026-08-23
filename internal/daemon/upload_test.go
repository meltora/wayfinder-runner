package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
)

func TestQueuedArtifactUploadsWithFencingAndRegistersOnce(t *testing.T) {
	jr, cap := testJobRunner(t)
	authority := v1.AgentRunAuthority{SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 1}
	jr.job.AgentRunAuthority = &authority
	var uploads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		if r.Method != http.MethodPut || r.Header.Get("X-Fencing-Token") != "7" {
			t.Errorf("upload request = %s fencing=%q", r.Method, r.Header.Get("X-Fencing-Token"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "artifact body" {
			t.Errorf("upload body = %q, %v", body, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	jr.d.uploader = newArtifactUploader(server.URL)

	source := filepath.Join(jr.workDir, "result.txt")
	if err := os.WriteFile(source, []byte("artifact body"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := call(t, jr, "platform_publish_artifact", `{"name":"result.txt","path":"result.txt","content_type":"text/plain","idempotency_key":"upload-ok"}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["registered"] != true {
		t.Fatalf("result = %v", result)
	}
	if uploads != 1 || len(cap.byKind("artifact_registered")) != 1 {
		t.Fatalf("uploads=%d events=%d", uploads, len(cap.byKind("artifact_registered")))
	}
	event := cap.byKind("artifact_registered")[0]
	for field, expected := range map[string]any{
		"manifest_kind": "build", "lease_id": "lease-1", "manifest_id": "manifest-1",
		"manifest_digest": strings.Repeat("a", 64), "job_id": "j1", "attempt_id": "a1", "fencing_token": uint64(7),
	} {
		if event[field] != expected {
			t.Fatalf("artifact event %s = %v, want %v", field, event[field], expected)
		}
	}
	if event["name"] != "result.txt" || event["artifact"] != nil || event["observed_at"] == nil {
		t.Fatalf("artifact event does not match strict wire shape: %v", event)
	}
	if event["agent_run_authority"] != authority {
		t.Fatalf("artifact event agent authority=%v", event["agent_run_authority"])
	}
	if records, err := jr.d.journal.Unacked(context.Background(), "upload", 10); err != nil || len(records) != 0 {
		t.Fatalf("pending uploads = %v, %v", records, err)
	}
}

func TestCorruptUploadRecordIsRejectedWithoutPoisoningQueue(t *testing.T) {
	jr, cap := testJobRunner(t)
	ctx := context.Background()
	if err := jr.d.journal.Append(ctx, journal.Record{
		ID: "bad-upload", Kind: journal.KindUpload, Payload: []byte(`{"unexpected":true}`), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := jr.d.processArtifactUploadBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if len(cap.byKind("artifact_upload_rejected")) != 1 {
		t.Fatalf("rejection events = %v", cap.events)
	}
	records, err := jr.d.journal.Unacked(ctx, journal.KindUpload, 10)
	if err != nil || len(records) != 0 {
		t.Fatalf("corrupt upload remained pending: %v %v", records, err)
	}
}

func TestArtifactUploaderRejectsNonSuccessResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer server.Close()
	uploader := newArtifactUploader(server.URL + "/")
	uploader.SetHTTPClient(nil)
	err := uploader.Upload(context.Background(), "job-1", "attempt-1", "report.txt", "digest", 9, strings.NewReader("body"))
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("Upload=%v", err)
	}
	custom := &http.Client{Timeout: time.Second}
	uploader.SetHTTPClient(custom)
	if uploader.httpClient != custom {
		t.Fatal("custom HTTP client not installed")
	}
}

func TestPermanentUploadErrorAndInputValidation(t *testing.T) {
	baseErr := errors.New("permanent")
	wrapped := permanentUpload(baseErr)
	if wrapped == nil || wrapped.Error() != "permanent" || !errors.Is(wrapped, baseErr) {
		t.Fatalf("wrapped=%v", wrapped)
	}
	if permanentUpload(nil) != nil {
		t.Fatal("nil error was wrapped")
	}

	jr, _ := testJobRunner(t)
	valid := pendingArtifactUpload{
		RecordID: "upload-1",
		Authority: v1.ManifestAuthority{
			Kind: v1.JobKindBuild, LeaseID: "lease-1", ManifestID: "manifest-1",
			ManifestDigest: strings.Repeat("a", 64), WorkflowRevision: 2,
		},
		Artifact: v1.ArtifactRef{
			JobID: "job-1", AttemptID: "attempt-1", Name: "report.txt",
			DigestSHA256: strings.Repeat("a", 64), SizeBytes: 1, ContentType: "text/plain",
		},
		Path: "missing",
	}
	cases := map[string]func(*pendingArtifactUpload){
		"record id":     func(p *pendingArtifactUpload) { p.RecordID = "bad id" },
		"job id":        func(p *pendingArtifactUpload) { p.Artifact.JobID = "bad/id" },
		"attempt id":    func(p *pendingArtifactUpload) { p.Artifact.AttemptID = "bad id" },
		"name":          func(p *pendingArtifactUpload) { p.Artifact.Name = "../secret" },
		"digest length": func(p *pendingArtifactUpload) { p.Artifact.DigestSHA256 = "bad" },
		"digest case":   func(p *pendingArtifactUpload) { p.Artifact.DigestSHA256 = strings.Repeat("A", 64) },
		"negative size": func(p *pendingArtifactUpload) { p.Artifact.SizeBytes = -1 },
		"large size":    func(p *pendingArtifactUpload) { p.Artifact.SizeBytes = (256 << 20) + 1 },
		"content type":  func(p *pendingArtifactUpload) { p.Artifact.ContentType = "not media" },
		"long content":  func(p *pendingArtifactUpload) { p.Artifact.ContentType = strings.Repeat("x", 201) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			pending := valid
			mutate(&pending)
			err := jr.d.processArtifactUpload(context.Background(), pending)
			var permanent permanentUploadError
			if !errors.As(err, &permanent) {
				t.Fatalf("expected permanent error, got %v", err)
			}
		})
	}
	if err := jr.d.processArtifactUpload(context.Background(), valid); err == nil {
		t.Fatal("missing artifact file accepted")
	}
}

func TestUploadBatchRejectsRecordIdentityMismatch(t *testing.T) {
	jr, cap := testJobRunner(t)
	pending := pendingArtifactUpload{
		RecordID: "different-id",
		Authority: v1.ManifestAuthority{
			Kind: v1.JobKindBuild, LeaseID: "lease-1", ManifestID: "manifest-1",
			ManifestDigest: strings.Repeat("a", 64), WorkflowRevision: 2,
		},
		Artifact: v1.ArtifactRef{JobID: "job-1", AttemptID: "attempt-1", Name: "a", DigestSHA256: strings.Repeat("a", 64)},
		Path:     "missing",
	}
	raw, err := json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	if err := jr.d.journal.Append(context.Background(), journal.Record{ID: "upload-record", Kind: journal.KindUpload, Payload: raw, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := jr.d.processArtifactUploadBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cap.byKind("artifact_upload_rejected")) != 1 {
		t.Fatalf("events=%v", cap.events)
	}
}

func TestDrainArtifactUploadsStopsOnCancellation(t *testing.T) {
	jr, _ := testJobRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	jr.d.drainArtifactUploads(ctx)
}
