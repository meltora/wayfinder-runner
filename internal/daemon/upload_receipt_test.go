package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/journal"
)

func writeArtifactReceipt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"receiptId": "receipt-1", "duplicate": false,
		"uploadId": r.Header.Get("X-Artifact-Upload-ID"), "digestSha256": r.Header.Get("X-Artifact-Digest"),
		"sizeBytes": r.ContentLength,
	})
}

func TestInvalidSuccessReceiptRetainsQueuedArtifactWithoutMetadataEvent(t *testing.T) {
	cases := []string{"empty", "malformed", "unknown", "missing", "null", "receipt id", "upload id", "digest", "size", "fractional", "duplicate type", "oversized", "trailing", "duplicate key", "case alias", "array"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			jr, cap := testJobRunner(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeInvalidArtifactReceipt(w, r, name)
			}))
			defer server.Close()
			jr.d.uploader = newArtifactUploader(server.URL)
			ref, path, err := jr.d.collectors.SaveArtifact(jr.job.JobID, jr.job.AttemptID, jr.job.FencingToken, "proof.txt", "text/plain", strings.NewReader("retained bytes"))
			if err != nil {
				t.Fatal(err)
			}
			pending, err := jr.d.queueArtifactUpload(context.Background(), jr.job.ManifestAuthority(), nil, *ref, path)
			if err != nil {
				t.Fatal(err)
			}
			if err := jr.d.processArtifactUploadBatch(context.Background()); err == nil {
				t.Fatal("invalid success receipt accepted")
			}
			records, err := jr.d.journal.Unacked(context.Background(), journal.KindUpload, 10)
			if err != nil || len(records) != 1 || records[0].ID != pending.RecordID {
				t.Fatalf("invalid receipt retired upload: %v %v", records, err)
			}
			body, err := os.ReadFile(filepath.Join(jr.d.collectors.Root, pending.Path))
			if err != nil || string(body) != "retained bytes" {
				t.Fatalf("invalid receipt discarded bytes: %q %v", body, err)
			}
			if len(cap.byKind("artifact_registered")) != 0 {
				t.Fatal("invalid receipt emitted metadata registration")
			}
		})
	}
}

func writeInvalidArtifactReceipt(w http.ResponseWriter, r *http.Request, name string) {
	receipt := map[string]any{"receiptId": "receipt-1", "duplicate": false, "uploadId": r.Header.Get("X-Artifact-Upload-ID"), "digestSha256": r.Header.Get("X-Artifact-Digest"), "sizeBytes": r.ContentLength}
	switch name {
	case "empty":
		return
	case "malformed":
		_, _ = w.Write([]byte("{"))
		return
	case "array":
		_, _ = w.Write([]byte("[]"))
		return
	case "oversized":
		_, _ = w.Write([]byte(strings.Repeat(" ", 4097)))
		return
	case "unknown":
		receipt["extra"] = true
	case "missing":
		delete(receipt, "duplicate")
	case "null":
		receipt["sizeBytes"] = nil
	case "receipt id":
		receipt["receiptId"] = "bad/id"
	case "upload id":
		receipt["uploadId"] = "different-upload"
	case "digest":
		receipt["digestSha256"] = strings.Repeat("0", 64)
	case "size":
		receipt["sizeBytes"] = r.ContentLength + 1
	case "fractional":
		receipt["sizeBytes"] = 1.5
	case "duplicate type":
		receipt["duplicate"] = "false"
	case "case alias":
		delete(receipt, "receiptId")
		receipt["ReceiptId"] = "receipt-1"
	}
	raw, _ := json.Marshal(receipt)
	if name == "trailing" {
		raw = append(raw, []byte(` {}`)...)
	}
	if name == "duplicate key" {
		raw = append([]byte(`{"duplicate":true,`), raw[1:]...)
	}
	_, _ = w.Write(raw)
}

func TestArtifactReceiptDuplicateAcknowledgementIsValid(t *testing.T) {
	pending := pendingArtifactUpload{RecordID: "upload-1"}
	pending.Artifact.DigestSHA256 = strings.Repeat("a", 64)
	for _, duplicate := range []bool{false, true} {
		raw, err := json.Marshal(map[string]any{
			"receiptId": "receipt-1", "duplicate": duplicate, "uploadId": pending.RecordID,
			"digestSha256": pending.Artifact.DigestSHA256, "sizeBytes": 0,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyArtifactUploadReceipt(strings.NewReader(string(raw)), pending); err != nil {
			t.Fatalf("duplicate=%t receipt=%v", duplicate, err)
		}
	}
}
