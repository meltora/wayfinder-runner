package mockcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPArtifactPackAndAdminEndpoints(t *testing.T) {
	s, err := NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	baseURL := s.enrollSrv.URL

	packBody := []byte("signed test pack")
	packPath := s.ServePack("pack.tar", packBody)
	resp, err := http.Get(baseURL + packPath)
	if err != nil {
		t.Fatal(err)
	}
	gotPack, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(gotPack, packBody) {
		t.Fatalf("pack status = %d, body = %q", resp.StatusCode, gotPack)
	}
	resp, err = http.Get(baseURL + "/packs/missing")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing pack status = %d", resp.StatusCode)
	}

	artifactBody := []byte("artifact")
	digest := sha256.Sum256(artifactBody)
	req, err := http.NewRequest(http.MethodPut, baseURL+"/v1/artifacts/job-1/attempt-1/log.txt", bytes.NewReader(artifactBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Artifact-Digest", hex.EncodeToString(digest[:]))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("artifact status = %d", resp.StatusCode)
	}
	stored, ok := s.Artifact("job-1", "attempt-1", "log.txt")
	if !ok || !bytes.Equal(stored, artifactBody) {
		t.Fatalf("stored artifact = %q, found = %t", stored, ok)
	}

	req, _ = http.NewRequest(http.MethodPut, baseURL+"/v1/artifacts/job-1/attempt-1/bad.txt", strings.NewReader("changed"))
	req.Header.Set("X-Artifact-Digest", strings.Repeat("0", 64))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad digest status = %d", resp.StatusCode)
	}

	resp, err = http.Get(baseURL + "/admin/events")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", resp.StatusCode)
	}
}

func TestHTTPAdminEndpointsRejectMalformedOrDisconnectedRequests(t *testing.T) {
	s, err := NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	baseURL := s.enrollSrv.URL

	cases := []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{http.MethodGet, "/admin/commands", "", http.StatusMethodNotAllowed},
		{http.MethodPost, "/admin/commands", "not-json", http.StatusBadRequest},
		{http.MethodPost, "/admin/commands", `{"command":"pause"}`, http.StatusConflict},
		{http.MethodGet, "/admin/decisions", "", http.StatusMethodNotAllowed},
		{http.MethodPost, "/admin/decisions", "not-json", http.StatusBadRequest},
		{http.MethodPost, "/admin/decisions", `{}`, http.StatusBadRequest},
		{http.MethodPost, "/admin/decisions", `{"question_id":"q1","decision":"yes"}`, http.StatusConflict},
		{http.MethodGet, "/v1/artifacts/job/attempt/name", "", http.StatusMethodNotAllowed},
		{http.MethodPut, "/v1/artifacts/bad", "x", http.StatusBadRequest},
	}
	for _, tc := range cases {
		req, err := http.NewRequest(tc.method, baseURL+tc.path, strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("%s %s status = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}
}
