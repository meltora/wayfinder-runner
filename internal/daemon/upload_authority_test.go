package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
)

func TestQueuedArtifactRetryPreservesOriginalAuthorityAfterLeaseRelease(t *testing.T) {
	jr, cap := testJobRunner(t)
	agentAuthority := v1.AgentRunAuthority{SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 3}
	jr.job.AgentRunAuthority = &agentAuthority
	var requests []http.Header
	jr.d.uploader = newInMemoryArtifactUploader(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Header.Clone())
		if r.URL.Path != "/v1/artifacts/j1/a1/result.txt" || r.Method != http.MethodPut {
			t.Errorf("unexpected upload route: %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "artifact body" {
			t.Errorf("body=%q err=%v", body, err)
		}
		if len(requests) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeArtifactReceipt(w, r)
	}))
	if err := os.WriteFile(filepath.Join(jr.workDir, "result.txt"), []byte("artifact body"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := call(t, jr, "platform_publish_artifact", `{"name":"result.txt","path":"result.txt","content_type":"text/plain","idempotency_key":"retry-authority"}`)
	if err != nil || result.(map[string]any)["queued"] != true {
		t.Fatalf("result=%v err=%v", result, err)
	}
	records, err := jr.d.journal.Unacked(context.Background(), journal.KindUpload, 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("pending=%v err=%v", records, err)
	}
	var pending pendingArtifactUpload
	if err := json.Unmarshal(records[0].Payload, &pending); err != nil {
		t.Fatal(err)
	}
	if !jr.d.leases.Release(jr.job.AttemptID) {
		t.Fatal("original lease not released")
	}
	jr.job = v1.JobSpec{JobID: "different-job", AttemptID: "different-attempt", FencingToken: 999}
	if err := jr.d.processArtifactUploadBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatalf("retry changed request authority: %v", requests)
	}
	for header, want := range map[string]string{
		"X-Artifact-Upload-ID": pending.RecordID,
		"X-Artifact-Digest":    pending.Artifact.DigestSHA256,
		"X-Fencing-Token":      "7", "Content-Type": "text/plain", "Content-Length": strconv.Itoa(len("artifact body")),
		"X-Manifest-Kind": "build", "X-Manifest-ID": "manifest-1", "X-Manifest-Digest": strings.Repeat("a", 64),
		"X-Lease-ID": "lease-1", "X-Workflow-Revision": "2",
		"X-Agent-Run-ID": "agent-run-1", "X-Step-Run-ID": "step-run-1", "X-Step-Sequence": "3", "X-Agent-Run-Schema-Version": "1",
	} {
		if got := requests[1].Get(header); got != want {
			t.Errorf("%s=%q want %q", header, got, want)
		}
	}
	if records, err := jr.d.journal.Unacked(context.Background(), journal.KindUpload, 10); err != nil || len(records) != 0 {
		t.Fatalf("completed upload stayed queued: %v %v", records, err)
	}
	if _, err := os.Stat(filepath.Join(jr.d.collectors.Root, pending.Path)); !os.IsNotExist(err) {
		t.Fatalf("uploaded collector bytes were not removed: %v", err)
	}
	if len(cap.byKind("artifact_registered")) != 0 {
		t.Fatalf("registration events=%v", cap.events)
	}
}

func TestArtifactUploaderUsesGatewayOriginWithSeparatePackEndpoint(t *testing.T) {
	cfg := enrolledDirectConfig(t)
	cfg.ControlPlaneURL = "wss://gateway.example.test/v1/runner/connect"
	cfg.ControlPlaneHTTPURL = "https://packs.example.test"
	d, err := New(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer closeConstructedDaemon(d)
	if d.uploader.baseURL != "https://gateway.example.test" {
		t.Fatalf("uploader destination=%q", d.uploader.baseURL)
	}
	if d.cfg.ControlPlaneHTTPURL != cfg.ControlPlaneHTTPURL {
		t.Fatal("pack endpoint configuration changed")
	}
}

func TestArtifactUploaderEmptyLegacyArtifactOmitsAgentAuthority(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != 0 || r.Header.Get("Content-Length") != "0" || len(r.TransferEncoding) != 0 {
			t.Errorf("empty artifact was not explicitly bounded: length=%d transfer=%v", r.ContentLength, r.TransferEncoding)
		}
		for _, header := range []string{"X-Agent-Run-ID", "X-Step-Run-ID", "X-Step-Sequence", "X-Agent-Run-Schema-Version"} {
			if r.Header.Get(header) != "" {
				t.Errorf("legacy upload fabricated %s", header)
			}
		}
		if r.URL.EscapedPath() != "/v1/artifacts/job-1/attempt-1/report%20one.txt" {
			t.Errorf("artifact display name escaped incorrectly: %s", r.URL.EscapedPath())
		}
		writeArtifactReceipt(w, r)
	}))
	defer server.Close()
	pending := pendingArtifactUpload{Artifact: v1.ArtifactRef{JobID: "job-1", AttemptID: "attempt-1", Name: "report one.txt"}}
	if err := newArtifactUploader(server.URL).Upload(context.Background(), pending, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactUploaderTransportErrorsDoNotBecomeReceipts(t *testing.T) {
	for _, baseURL := range []string{":bad-url", "http://127.0.0.1:1"} {
		t.Run(baseURL, func(t *testing.T) {
			if err := newArtifactUploader(baseURL).Upload(context.Background(), pendingArtifactUpload{}, strings.NewReader("")); err == nil {
				t.Fatal("failed transport accepted as durable receipt")
			}
		})
	}
}
