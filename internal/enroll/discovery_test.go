package enroll

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func discoveryServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/runners/discovery" || r.URL.RawQuery != "" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoverGatewayReturnsTheAdvertisedURL(t *testing.T) {
	srv := discoveryServer(t, http.StatusOK, `{"schema_version":1,"gateway_url":"wss://gateway.example/v1/runners/stream"}`)
	got, err := (&Client{BaseURL: srv.URL + "/", AllowInsecure: true}).DiscoverGateway(context.Background())
	if err != nil || got != "wss://gateway.example/v1/runners/stream" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestDiscoverGatewayRejectsUnexpectedResponses(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"older app":      {http.StatusNotFound, `{"code":"not_found"}`},
		"not configured": {http.StatusNotFound, `{"code":"runner_enrollment_unavailable"}`},
		"unknown field":  {http.StatusOK, `{"schema_version":1,"gateway_url":"wss://g.example","organization":"org-1"}`},
		"schema version": {http.StatusOK, `{"schema_version":2,"gateway_url":"wss://g.example"}`},
		"missing url":    {http.StatusOK, `{"schema_version":1}`},
		"trailing json":  {http.StatusOK, `{"schema_version":1,"gateway_url":"wss://g.example"}{}`},
		"not json":       {http.StatusOK, `<html></html>`},
		"too large":      {http.StatusOK, `{"schema_version":1,"gateway_url":"wss://g.example/` + strings.Repeat("a", 1<<20) + `"}`},
	} {
		t.Run(name, func(t *testing.T) {
			srv := discoveryServer(t, tc.status, tc.body)
			if got, err := (&Client{BaseURL: srv.URL, AllowInsecure: true}).DiscoverGateway(context.Background()); err == nil {
				t.Fatalf("accepted %q", got)
			}
		})
	}
}

func TestDiscoverGatewayUsesTheEnrollmentTransportRules(t *testing.T) {
	if _, err := (&Client{BaseURL: "http://control-plane.example.com"}).DiscoverGateway(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("unencrypted discovery err = %v", err)
	}
	if _, err := (&Client{BaseURL: "https://control-plane.example.com?x=1"}).DiscoverGateway(context.Background()); err == nil {
		t.Fatal("control-plane URL with a query accepted")
	}
	var redirected bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected = true
		_, _ = w.Write([]byte(`{"schema_version":1,"gateway_url":"wss://attacker.example"}`))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/runners/discovery", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	if got, err := (&Client{BaseURL: source.URL, AllowInsecure: true}).DiscoverGateway(context.Background()); err == nil || redirected {
		t.Fatalf("redirect followed: got %q redirected=%v", got, redirected)
	}
}
