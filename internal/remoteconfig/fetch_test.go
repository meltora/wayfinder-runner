package remoteconfig

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/enroll"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/mockcp"
)

func startMockControlPlane(t *testing.T) *mockcp.Server {
	t.Helper()
	server, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	return server
}

func enrolledStateDir(t *testing.T, server *mockcp.Server) (string, string) {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	store := identity.NewStore(stateDir)
	key, err := store.EnsureKey()
	if err != nil {
		t.Fatal(err)
	}
	csr, err := identity.NewCSR(key, "configure-test")
	if err != nil {
		t.Fatal(err)
	}
	server.AddEnrollmentCode("code-" + t.Name())
	client := &enroll.Client{BaseURL: server.EnrollURL(), AllowInsecure: true}
	response, err := client.Enroll(context.Background(), enroll.Request{EnrollmentCode: "code-" + t.Name(), CSR: string(csr), RunnerVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveIdentity(key, []byte(response.Certificate), []byte(response.CACert)); err != nil {
		t.Fatal(err)
	}
	return stateDir, response.RunnerID
}

func TestFetcherUsesTheRunnerIdentityOverMutualTLS(t *testing.T) {
	server := startMockControlPlane(t)
	stateDir, runnerID := enrolledStateDir(t, server)
	server.SetConfigurationResponses(mockcp.ConfigurationResponse{Status: http.StatusOK, Body: []byte(`{"ok":true}`)})
	fetcher, err := NewFetcher(stateDir, server.WSURL())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"ok":true}` {
		t.Fatalf("raw = %s", raw)
	}
	requests := server.ConfigurationRequests()
	if len(requests) != 1 || requests[0].Method != http.MethodGet || requests[0].RawQuery != "" || requests[0].ClientName != runnerID {
		t.Fatalf("requests = %+v", requests)
	}
}

func TestFetcherClassifiesGatewayAnswers(t *testing.T) {
	server := startMockControlPlane(t)
	stateDir, _ := enrolledStateDir(t, server)
	fetcher, err := NewFetcher(stateDir, server.WSURL())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		response  mockcp.ConfigurationResponse
		status    int
		code      string
		transient bool
		message   string
	}{
		{mockcp.ConfigurationResponse{Status: 401, Body: []byte(`{"code":"unauthorized"}`)}, 401, "unauthorized", false, "identity"},
		{mockcp.ConfigurationResponse{Status: 404, Body: []byte(`{"code":"configuration_not_ready"}`)}, 404, "configuration_not_ready", false, "has not saved"},
		{mockcp.ConfigurationResponse{Status: 409, Body: []byte(`{"code":"direct_isolation_unavailable"}`)}, 409, "direct_isolation_unavailable", false, "direct isolation"},
		{mockcp.ConfigurationResponse{Status: 409, Body: []byte(`{"code":"configuration_conflict"}`)}, 409, "configuration_conflict", false, "Agent Pack key"},
		{mockcp.ConfigurationResponse{Status: 409, Body: []byte(`{"code":"configuration_too_large"}`)}, 409, "configuration_too_large", false, "too large"},
		{mockcp.ConfigurationResponse{Status: 409, Body: []byte(`{"code":"configuration_changed"}`)}, 409, "configuration_changed", false, "changed"},
		{mockcp.ConfigurationResponse{Status: 429, Body: []byte(`{"code":"rate_limited"}`)}, 429, "rate_limited", false, "too many"},
		{mockcp.ConfigurationResponse{Status: 503, Body: []byte(`{"code":"unavailable"}`)}, 503, "unavailable", true, "unavailable"},
		{mockcp.ConfigurationResponse{Status: 500, Body: []byte(`not json`)}, 500, "", true, "HTTP 500"},
		{mockcp.ConfigurationResponse{Status: 302, Body: nil}, 302, "", false, "HTTP 302"},
	}
	for _, tc := range cases {
		server.SetConfigurationResponses(tc.response)
		_, err := fetcher.Fetch(context.Background())
		var statusErr *StatusError
		if !errors.As(err, &statusErr) || statusErr.Status != tc.status || statusErr.Code != tc.code {
			t.Fatalf("%d: err = %v", tc.status, err)
		}
		if IsTransient(err) != tc.transient {
			t.Fatalf("%d: transient = %v", tc.status, IsTransient(err))
		}
		if !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("%d: message = %v", tc.status, err)
		}
	}
}

func TestFetcherRejectsOversizedBodiesAndReportsNetworkFailures(t *testing.T) {
	server := startMockControlPlane(t)
	stateDir, _ := enrolledStateDir(t, server)
	fetcher, err := NewFetcher(stateDir, server.WSURL())
	if err != nil {
		t.Fatal(err)
	}
	server.SetConfigurationResponses(mockcp.ConfigurationResponse{Status: 200, Body: bytes.Repeat([]byte("a"), MaxDeliveryBytes+1)})
	if _, err := fetcher.Fetch(context.Background()); err == nil || IsTransient(err) {
		t.Fatalf("oversized err = %v", err)
	}
	server.Close()
	_, err = fetcher.Fetch(context.Background())
	var networkErr *NetworkError
	if !errors.As(err, &networkErr) || !IsTransient(err) {
		t.Fatalf("network err = %v", err)
	}
}

func TestFetcherRejectsAnUntrustedGateway(t *testing.T) {
	server := startMockControlPlane(t)
	other := startMockControlPlane(t)
	stateDir, _ := enrolledStateDir(t, server)
	fetcher, err := NewFetcher(stateDir, other.WSURL())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fetcher.Fetch(context.Background()); err == nil || IsTransient(err) {
		t.Fatalf("untrusted gateway err = %v", err)
	}
}

func TestNewFetcherRequiresAnEnrolledIdentity(t *testing.T) {
	if _, err := NewFetcher(filepath.Join(t.TempDir(), "state"), "wss://gateway.example.test"); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewFetcher(t.TempDir(), "https://gateway.example.test"); err == nil {
		t.Fatal("accepted a non-wss gateway")
	}
}
