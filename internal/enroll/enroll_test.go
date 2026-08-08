package enroll

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnrollSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runners/enroll" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if req.EnrollmentCode != "code-123" || req.CSR == "" {
			t.Errorf("bad request: %+v", req)
		}
		_ = json.NewEncoder(w).Encode(Response{RunnerID: "r-1", Certificate: "cert", CACert: "ca"})
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, AllowInsecure: true}
	resp, err := c.Enroll(context.Background(), Request{EnrollmentCode: "code-123", CSR: "csr-pem", RunnerVersion: "0.1.0"})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if resp.RunnerID != "r-1" {
		t.Fatalf("RunnerID = %q", resp.RunnerID)
	}
}

func TestEnrollRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"code_expired"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, AllowInsecure: true}
	_, err := c.Enroll(context.Background(), Request{EnrollmentCode: "bad", CSR: "csr"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEnrollMissingFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Response{RunnerID: "r-1"})
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, AllowInsecure: true}
	if _, err := c.Enroll(context.Background(), Request{EnrollmentCode: "c", CSR: "csr"}); err == nil {
		t.Fatal("expected missing-fields error")
	}
}

func TestEnrollRejectsInsecureTransportByDefault(t *testing.T) {
	t.Parallel()
	c := &Client{BaseURL: "http://control-plane.example.com"}
	if _, err := c.Enroll(context.Background(), Request{EnrollmentCode: "code", CSR: "csr"}); err == nil {
		t.Fatal("unencrypted enrollment was accepted")
	}
}

func TestEnrollDoesNotForwardCodeAcrossRedirect(t *testing.T) {
	var redirected bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected = true
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	c := &Client{BaseURL: source.URL, AllowInsecure: true}
	if _, err := c.Enroll(context.Background(), Request{EnrollmentCode: "secret-code", CSR: "csr"}); err == nil {
		t.Fatal("redirect unexpectedly followed")
	}
	if redirected {
		t.Fatal("enrollment code was forwarded to redirect target")
	}
}
