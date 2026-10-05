package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/mockcp"
)

const gatewayNotProvided = "The gateway address was not provided; pass --gateway URL to our-runner configure."

func captureStdout(t *testing.T, run func() error) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = writer
	captured := make(chan string)
	go func() {
		raw, _ := io.ReadAll(reader)
		captured <- string(raw)
	}()
	runErr := run()
	os.Stdout = old
	_ = writer.Close()
	return <-captured, runErr
}

func enrollWithDiscovery(t *testing.T, discovery *mockcp.ConfigurationResponse) (*configureHarness, string) {
	t.Helper()
	server, err := mockcp.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	if discovery != nil {
		server.SetDiscoveryResponse(*discovery)
	}
	server.AddEnrollmentCode("discovery-code")
	stateDir := filepath.Join(t.TempDir(), "state")
	codeFile := filepath.Join(t.TempDir(), "code")
	if err := os.WriteFile(codeFile, []byte("discovery-code"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := captureStdout(t, func() error {
		return cmdEnroll([]string{"--state-dir", stateDir, "--code-file", codeFile, "--url", server.EnrollURL(), "--allow-insecure"})
	})
	if err != nil {
		t.Fatalf("enroll: %v\n%s", err, output)
	}
	runnerID, err := daemon.ReadRunnerID(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if requests := server.DiscoveryRequests(); len(requests) != 1 || requests[0].Method != http.MethodGet || requests[0].RawQuery != "" {
		t.Fatalf("discovery requests = %+v", requests)
	}
	return &configureHarness{server: server, stateDir: stateDir, runnerID: runnerID, gateway: server.WSURL(), output: &bytes.Buffer{}}, output
}

func (h *configureHarness) runWithoutGateway(input string, terminal bool, args ...string) error {
	return cmdConfigureWithDependencies(append([]string{"--state-dir", h.stateDir}, args...), configureDependencies{
		input:    strings.NewReader(input),
		output:   h.output,
		terminal: terminal,
		newFetch: newRemoteFetch,
		sleep:    func(context.Context, time.Duration) error { return nil },
	})
}

func storedGateway(t *testing.T, stateDir string) (string, bool) {
	t.Helper()
	path := filepath.Join(stateDir, "gateway-url")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("gateway-url %v %v", info, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw)), true
}

func TestEnrollStoresTheDiscoveredGatewayAndConfigureUsesIt(t *testing.T) {
	h, output := enrollWithDiscovery(t, nil)
	if stored, ok := storedGateway(t, h.stateDir); !ok || stored != h.server.WSURL() {
		t.Fatalf("stored gateway = %q %v", stored, ok)
	}
	if strings.Contains(output, gatewayNotProvided) || !strings.Contains(output, "enrolled as runner "+h.runnerID) {
		t.Fatalf("output:\n%s", output)
	}
	key := newConfigurePackKey(t, "pack-key-1")
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker", key))
	if err := h.runWithoutGateway("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	if local := h.local(t); local.ControlPlaneURL != h.server.WSURL() {
		t.Fatalf("local = %+v", local)
	}
	if requests := h.server.ConfigurationRequests(); len(requests) != 1 || requests[0].ClientName != h.runnerID {
		t.Fatalf("configuration requests = %+v", requests)
	}
	out := h.output.String()
	if !strings.Contains(out, "Accept them with: our-runner configure --state-dir ") || strings.Contains(out, "--gateway") {
		t.Fatalf("hint:\n%s", out)
	}
	warnings := &bytes.Buffer{}
	if _, err := loadStateDirConfiguration(context.Background(), h.stateDir, warnings); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warnings.String(), "run our-runner configure --state-dir ") || strings.Contains(warnings.String(), "--gateway") {
		t.Fatalf("start warning:\n%s", warnings.String())
	}
}

func TestConfigureGatewayFlagWinsAndIsNotStored(t *testing.T) {
	h, _ := enrollWithDiscovery(t, nil)
	override := strings.TrimSuffix(h.server.WSURL(), "/v1/runners/stream")
	key := newConfigurePackKey(t, "pack-key-1")
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker", key))
	if err := h.runWithoutGateway("", false, "--non-interactive", "--gateway", override); err != nil {
		t.Fatal(err)
	}
	if local := h.local(t); local.ControlPlaneURL != override {
		t.Fatalf("local = %+v", local)
	}
	if stored, _ := storedGateway(t, h.stateDir); stored != h.server.WSURL() {
		t.Fatalf("flag written back: %q", stored)
	}
	if !strings.Contains(h.output.String(), "Accept them with: our-runner configure --gateway "+shellQuote(override)) {
		t.Fatalf("hint:\n%s", h.output.String())
	}
	warnings := &bytes.Buffer{}
	if _, err := loadStateDirConfiguration(context.Background(), h.stateDir, warnings); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warnings.String(), "run our-runner configure --gateway "+shellQuote(override)) {
		t.Fatalf("start warning:\n%s", warnings.String())
	}
}

func TestEnrollProceedsWhenDiscoveryIsUnavailableOrInvalid(t *testing.T) {
	for name, response := range map[string]mockcp.ConfigurationResponse{
		"older app":        {Status: http.StatusNotFound, Body: []byte("404 page not found")},
		"not configured":   {Status: http.StatusNotFound, Body: []byte(`{"code":"runner_enrollment_unavailable"}`)},
		"unknown field":    {Status: http.StatusOK, Body: []byte(`{"schema_version":1,"gateway_url":"wss://g.example","organization":"o"}`)},
		"not wss":          {Status: http.StatusOK, Body: []byte(`{"schema_version":1,"gateway_url":"https://g.example"}`)},
		"query in gateway": {Status: http.StatusOK, Body: []byte(`{"schema_version":1,"gateway_url":"wss://g.example/?x=1"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			h, output := enrollWithDiscovery(t, &response)
			if !strings.Contains(output, "enrolled as runner "+h.runnerID) || !strings.Contains(output, gatewayNotProvided) {
				t.Fatalf("output:\n%s", output)
			}
			if _, ok := storedGateway(t, h.stateDir); ok {
				t.Fatal("an unusable gateway was stored")
			}
		})
	}
}

func TestConfigureWithoutAnyKnownGatewayFailsBeforeFetching(t *testing.T) {
	h, _ := enrollWithDiscovery(t, &mockcp.ConfigurationResponse{Status: http.StatusNotFound, Body: []byte("404 page not found")})
	err := h.runWithoutGateway("", false, "--non-interactive")
	if err == nil || err.Error() != "No gateway address is known for this runner; pass --gateway URL." {
		t.Fatalf("err = %v", err)
	}
	if requests := h.server.ConfigurationRequests(); len(requests) != 0 {
		t.Fatalf("fetched without a gateway: %+v", requests)
	}
	assertNoConfigurationFiles(t, h.stateDir)
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker", newConfigurePackKey(t, "pack-key-1")))
	if err := h.run("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.output.String(), "Accept them with: our-runner configure --gateway ") {
		t.Fatalf("pre-discovery runner lost the --gateway hint:\n%s", h.output.String())
	}
}

func TestConfigureRejectsAStoredGatewayThatIsNotUsable(t *testing.T) {
	h, _ := enrollWithDiscovery(t, nil)
	if err := os.WriteFile(filepath.Join(h.stateDir, "gateway-url"), []byte("https://not-a-gateway.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.runWithoutGateway("", false, "--non-interactive"); err == nil || !strings.Contains(err.Error(), "gateway-url") {
		t.Fatalf("err = %v", err)
	}
	if err := os.Chmod(filepath.Join(h.stateDir, "gateway-url"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.runWithoutGateway("", false, "--non-interactive"); err == nil || !strings.Contains(err.Error(), "gateway-url") {
		t.Fatalf("err = %v", err)
	}
	if len(h.server.ConfigurationRequests()) != 0 {
		t.Fatal("fetched with an unusable stored gateway")
	}
}

func TestEnrollmentRecoveryStaysOfflineAndStoresNoGateway(t *testing.T) {
	dir := savedRecoveryIdentity(t, "runner-recovered-42", false)
	transport := &rejectRecoveryNetwork{}
	withDefaultTransport(t, transport)
	output, err := captureStdout(t, func() error {
		return cmdEnroll([]string{"--state-dir", dir, "--code-file", filepath.Join(t.TempDir(), "missing-code"), "--url", "https://must-not-contact.invalid"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if transport.calls != 0 {
		t.Fatal("recovery contacted the server")
	}
	if _, ok := storedGateway(t, dir); ok {
		t.Fatal("recovery stored a gateway")
	}
	if !strings.Contains(output, gatewayNotProvided) {
		t.Fatalf("output:\n%s", output)
	}
}
