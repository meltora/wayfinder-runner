package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
)

func TestArtifactUploaderClassifiesDefinitiveRejectionsAsPermanent(t *testing.T) {
	permanent := []int{
		http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		http.StatusGone, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity,
	}
	transient := []int{
		http.StatusUnauthorized, http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout,
	}
	statuses := map[int]bool{}
	for _, status := range permanent {
		statuses[status] = true
	}
	for _, status := range transient {
		statuses[status] = false
	}
	for status, permanent := range statuses {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()
			err := newArtifactUploader(server.URL).Upload(context.Background(), pendingArtifactUpload{Artifact: v1.ArtifactRef{
				JobID: "job-1", AttemptID: "attempt-1", Name: "report.txt", DigestSHA256: "digest", SizeBytes: 4,
			}}, strings.NewReader("body"))
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("status %d", status)) {
				t.Fatalf("Upload=%v", err)
			}
			var classified permanentUploadError
			if errors.As(err, &classified) != permanent {
				t.Fatalf("status %d permanent=%v, want %v", status, errors.As(err, &classified), permanent)
			}
		})
	}
}

type scriptedUploadServer struct {
	mu       sync.Mutex
	statuses map[string]int
	attempts []string
}

func (s *scriptedUploadServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get("X-Artifact-Upload-ID")
	s.mu.Lock()
	s.attempts = append(s.attempts, id)
	status, scripted := s.statuses[id]
	s.mu.Unlock()
	if scripted {
		w.WriteHeader(status)
		return
	}
	writeArtifactReceipt(w, r)
}

func queueTestArtifactUploads(t *testing.T, jr *jobRunner, count int) []string {
	t.Helper()
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		ref, path, err := jr.d.collectors.SaveArtifact(jr.job.JobID, jr.job.AttemptID, jr.job.FencingToken, fmt.Sprintf("proof-%d.txt", i), "text/plain", strings.NewReader(fmt.Sprintf("bytes %d", i)))
		if err != nil {
			t.Fatal(err)
		}
		pending, err := jr.d.queueArtifactUpload(context.Background(), jr.job.ManifestAuthority(), nil, *ref, path)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, pending.RecordID)
	}
	return ids
}

func unackedUploadIDs(t *testing.T, jr *jobRunner) []string {
	t.Helper()
	records, err := jr.d.journal.Unacked(context.Background(), journal.KindUpload, 100)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}

func TestUploadBatchRetiresDefinitivelyRejectedUploadAndContinues(t *testing.T) {
	jr, cap := testJobRunner(t)
	ids := queueTestArtifactUploads(t, jr, 2)
	server := &scriptedUploadServer{statuses: map[string]int{ids[0]: http.StatusForbidden}}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	jr.d.uploader = newArtifactUploader(httpServer.URL)

	if err := jr.d.processArtifactUploadBatch(context.Background()); err != nil {
		t.Fatalf("batch=%v", err)
	}
	rejections := cap.byKind("artifact_upload_rejected")
	if len(rejections) != 1 || rejections[0]["record_id"] != ids[0] || !strings.Contains(fmt.Sprint(rejections[0]["reason"]), "status 403") {
		t.Fatalf("rejections=%v", rejections)
	}
	if pending := unackedUploadIDs(t, jr); len(pending) != 0 {
		t.Fatalf("pending uploads=%v", pending)
	}
	if err := jr.d.processArtifactUploadBatch(context.Background()); err != nil {
		t.Fatalf("second batch=%v", err)
	}
	if len(server.attempts) != 2 || len(cap.byKind("artifact_upload_rejected")) != 1 {
		t.Fatalf("attempts=%v rejections=%v", server.attempts, cap.byKind("artifact_upload_rejected"))
	}
}

func TestUploadBatchTransientRecordFailureDoesNotStarveLaterRecords(t *testing.T) {
	jr, cap := testJobRunner(t)
	ids := queueTestArtifactUploads(t, jr, 3)
	server := &scriptedUploadServer{statuses: map[string]int{ids[0]: http.StatusInternalServerError}}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	jr.d.uploader = newArtifactUploader(httpServer.URL)

	err := jr.d.processArtifactUploadBatch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("batch=%v", err)
	}
	if pending := unackedUploadIDs(t, jr); len(pending) != 1 || pending[0] != ids[0] {
		t.Fatalf("pending uploads=%v", pending)
	}
	if len(server.attempts) != 3 || len(cap.byKind("artifact_upload_rejected")) != 0 {
		t.Fatalf("attempts=%v rejections=%v", server.attempts, cap.byKind("artifact_upload_rejected"))
	}
}

func TestUploadBatchStopsOnControlPlaneBackpressure(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			jr, cap := testJobRunner(t)
			ids := queueTestArtifactUploads(t, jr, 2)
			server := &scriptedUploadServer{statuses: map[string]int{ids[0]: status}}
			httpServer := httptest.NewServer(server)
			defer httpServer.Close()
			jr.d.uploader = newArtifactUploader(httpServer.URL)

			if err := jr.d.processArtifactUploadBatch(context.Background()); err == nil {
				t.Fatal("backpressure accepted")
			}
			if len(server.attempts) != 1 || len(unackedUploadIDs(t, jr)) != 2 || len(cap.byKind("artifact_upload_rejected")) != 0 {
				t.Fatalf("attempts=%v pending=%v", server.attempts, unackedUploadIDs(t, jr))
			}
		})
	}
}

func TestUploadBatchStopsOnTransportFailure(t *testing.T) {
	jr, _ := testJobRunner(t)
	queueTestArtifactUploads(t, jr, 2)
	httpServer := httptest.NewServer(http.NotFoundHandler())
	httpServer.Close()
	jr.d.uploader = newArtifactUploader(httpServer.URL)

	if err := jr.d.processArtifactUploadBatch(context.Background()); err == nil {
		t.Fatal("transport failure accepted")
	}
	if pending := unackedUploadIDs(t, jr); len(pending) != 2 {
		t.Fatalf("pending uploads=%v", pending)
	}
}
