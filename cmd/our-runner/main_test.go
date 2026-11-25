package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/mcpbridge"
	"github.com/example/app-runner/internal/mockcp"
)

func TestParseFlagsStrict(t *testing.T) {
	t.Parallel()
	f, err := parseFlags([]string{"--url", "https://example.test", "--state-dir", "/tmp/runner"})
	if err != nil {
		t.Fatal(err)
	}
	if f.url != "https://example.test" || f.stateDir != "/tmp/runner" {
		t.Fatalf("flags = %+v", f)
	}
	for _, args := range [][]string{
		{"--unknown", "x"},
		{"--url"},
		{"positional"},
		{"--url", "one", "--url", "two"},
		{"--url", ""},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("parseFlags(%q) unexpectedly succeeded", args)
		}
	}
}

func TestReadEnrollmentCodeAndTokenRequirePrivateRegularFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte(" value \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readEnrollmentCode(path); err != nil || got != "value" {
		t.Fatalf("readEnrollmentCode() = %q, %v", got, err)
	}
	if got, err := readPrivateTokenFile(path); err != nil || got != "value" {
		t.Fatalf("readPrivateTokenFile() = %q, %v", got, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readEnrollmentCode(path); err == nil {
		t.Fatal("public enrollment code accepted")
	}
	if _, err := readPrivateTokenFile(path); err == nil {
		t.Fatal("public MCP token accepted")
	}
}

func TestParseInternalFlagsStrict(t *testing.T) {
	t.Parallel()
	values, err := parseInternalFlags([]string{"--socket", "/tmp/a.sock", "--type", "stop"}, "--socket", "--type")
	if err != nil || values["--socket"] != "/tmp/a.sock" || values["--type"] != "stop" {
		t.Fatalf("values = %v, err = %v", values, err)
	}
	for _, args := range [][]string{
		{"--unknown", "x"},
		{"--socket"},
		{"--socket", ""},
		{"--socket", "one", "--socket", "two"},
		{"positional"},
	} {
		if _, err := parseInternalFlags(args, "--socket"); err == nil {
			t.Fatalf("parseInternalFlags(%q) unexpectedly succeeded", args)
		}
	}
}

func TestInternalCommandsRejectUnsafeOrMalformedFlags(t *testing.T) {
	t.Parallel()
	if err := cmdMCPServe([]string{"--token", "secret"}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("raw token error = %v", err)
	}
	for _, call := range []func() error{
		func() error { return cmdMCPServe([]string{"--socket", "/tmp/x", "--bogus", "x"}) },
		func() error { return cmdHookEvent([]string{"--socket", "/tmp/x", "--socket", "/tmp/y"}) },
	} {
		if err := call(); err == nil {
			t.Fatal("malformed internal flags were accepted")
		}
	}
}

func TestAdministrativeCommandsAndRequiredArguments(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := cmdStatus([]string{"--state-dir", stateDir}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if err := cmdPause([]string{"--state-dir", stateDir}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	paused, err := daemon.ReadPausedState(stateDir)
	if err != nil || !paused {
		t.Fatalf("paused = %t, err = %v", paused, err)
	}
	if err := cmdResume([]string{"--state-dir", stateDir}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	paused, err = daemon.ReadPausedState(stateDir)
	if err != nil || paused {
		t.Fatalf("paused = %t, err = %v", paused, err)
	}

	for name, call := range map[string]func() error{
		"enroll raw secret": func() error { return cmdEnroll([]string{"--code", "secret", "--url", "https://cp.test"}) },
		"enroll missing":    func() error { return cmdEnroll(nil) },
		"doctor missing":    func() error { return cmdDoctor(nil) },
		"start missing":     func() error { return cmdStart(nil) },
		"MCP missing":       func() error { return cmdMCPServe(nil) },
		"hook missing":      func() error { return cmdHookEvent(nil) },
		"register missing":  func() error { return cmdRegisterRepo(nil) },
	} {
		if err := call(); err == nil {
			t.Fatalf("%s unexpectedly succeeded", name)
		}
	}
}

func TestScanAgentConfigCommand(t *testing.T) {
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, "AGENTS.md"), []byte("# Instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdScanConfig([]string{"--config", repository}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if err := cmdScanConfig([]string{"--config", filepath.Join(repository, "missing")}); err == nil {
		t.Fatal("missing scan directory accepted")
	}
}

func TestRunCLICommonDispatch(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"help"}, 0},
		{[]string{"--help"}, 0},
		{[]string{"version"}, 0},
		{[]string{"does-not-exist"}, 2},
		{[]string{"doctor"}, 1},
		{[]string{"status", "--unknown", "x"}, 1},
	} {
		if got := runCLI(tc.args); got != tc.code {
			t.Fatalf("runCLI(%v)=%d want %d", tc.args, got, tc.code)
		}
	}
}

func TestSupportBundleCommand(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := daemon.WriteRunnerID(stateDir, "runner-1"); err != nil {
		t.Fatal(err)
	}
	if err := cmdSupportBundle([]string{"--state-dir", stateDir}); err != nil {
		t.Fatalf("cmdSupportBundle: %v", err)
	}
}

func TestRegisterRepositoryCommand(t *testing.T) {
	binDir := t.TempDir()
	fakeGit := filepath.Join(binDir, "git")
	script := `#!/bin/sh
set -eu
case " $* " in
  *" clone --mirror "*)
    for last do :; done
    /bin/mkdir -p "$last"
    ;;
  *" symbolic-ref --short HEAD "*) printf 'main\n' ;;
  *) exit 9 ;;
esac
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := cmdRegisterRepo([]string{"--state-dir", stateDir, "--url", "https://example.test/repository.git"}); err != nil {
		t.Fatalf("cmdRegisterRepo: %v", err)
	}
}

func TestHookEventCommandRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "job.sock")
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("token-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	received := make(chan json.RawMessage, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &mcpbridge.UnixServer{
		SocketPath: socket,
		Token:      "token-1",
		Handler: func(_ context.Context, tool string, args json.RawMessage) (any, error) {
			if tool != mcpbridge.InternalHookEventTool {
				t.Errorf("tool=%q", tool)
			}
			received <- append(json.RawMessage(nil), args...)
			return map[string]any{"ok": true}, nil
		},
	}
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}

	oldStdin := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = reader.Close()
	})
	if _, err := writer.WriteString(`{"tool":"bash","authorization":"redact-me"}`); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	if err := cmdHookEvent([]string{"--socket", socket, "--token-file", tokenFile, "--type", "post-tool"}); err != nil {
		t.Fatalf("cmdHookEvent: %v", err)
	}
	select {
	case raw := <-received:
		if !strings.Contains(string(raw), `"event_type":"post-tool"`) {
			t.Fatalf("args=%s", raw)
		}
		if strings.Contains(string(raw), "authorization") || strings.Contains(string(raw), `"data"`) {
			t.Fatalf("hook forwarded provider-controlled payload: %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("hook event was not received")
	}
	if err := cmdHookEvent([]string{"--socket", socket, "--token-file", tokenFile, "--type", "repository-contents"}); err == nil {
		t.Fatal("hook command accepted an unregistered event type")
	}
}

func TestHookEventCommandHasABoundedDaemonCall(t *testing.T) {
	dir, err := os.MkdirTemp("", "hook-deadline-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "job.sock")
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("token-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &mcpbridge.UnixServer{
		SocketPath: socket,
		Token:      "token-1",
		Handler: func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}

	oldStdin := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = reader.Close()
	})
	started := time.Now()
	if err := cmdHookEvent([]string{"--socket", socket, "--token-file", tokenFile, "--type", "stop"}); err == nil {
		t.Fatal("timed-out daemon call unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed < time.Second || elapsed > 3*time.Second {
		t.Fatalf("hook deadline elapsed=%s", elapsed)
	}
}

func TestMCPServeCommandRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "job.sock")
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("token-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &mcpbridge.UnixServer{
		SocketPath: socket,
		Token:      "token-1",
		Handler: func(_ context.Context, tool string, _ json.RawMessage) (any, error) {
			if tool == mcpbridge.InternalListToolsTool {
				return []string{"platform_get_job_context"}, nil
			}
			return map[string]any{"tool": tool}, nil
		},
	}
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}

	oldStdin, oldStdout := os.Stdin, os.Stdout
	inReader, inWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outReader, outWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin, os.Stdout = inReader, outWriter
	t.Cleanup(func() {
		os.Stdin, os.Stdout = oldStdin, oldStdout
		_ = inReader.Close()
		_ = outReader.Close()
		_ = outWriter.Close()
	})
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"platform_get_job_context","arguments":{}}}` + "\n"
	if _, err := inWriter.WriteString(request); err != nil {
		t.Fatal(err)
	}
	_ = inWriter.Close()
	if err := cmdMCPServe([]string{"--socket", socket, "--token-file", tokenFile}); err != nil {
		t.Fatalf("cmdMCPServe: %v", err)
	}
	_ = outWriter.Close()
	buf := make([]byte, 4096)
	n, err := outReader.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf[:n]), `"jsonrpc":"2.0"`) {
		t.Fatalf("response=%s", buf[:n])
	}
}

func withDefaultTransport(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = old })
}

func TestEnrollCommandOverHTTPS(t *testing.T) {
	ca, err := mockcp.NewTestCA()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runners/enroll" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var request struct {
			EnrollmentCode string `json:"enrollment_code"`
			CSR            string `json:"csr"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.EnrollmentCode != "enroll-once" {
			http.Error(w, "bad enrollment", http.StatusBadRequest)
			return
		}
		cert, err := ca.SignCSR([]byte(request.CSR), "runner-42")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"runner_id": "runner-42", "certificate": string(cert), "ca_certificate": string(ca.CertPEM),
		})
	}))
	defer server.Close()
	withDefaultTransport(t, server.Client().Transport)

	stateDir := filepath.Join(t.TempDir(), "state")
	codeFile := filepath.Join(t.TempDir(), "enrollment-code")
	if err := os.WriteFile(codeFile, []byte("enroll-once\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdEnroll([]string{"--state-dir", stateDir, "--code-file", codeFile, "--url", server.URL}); err != nil {
		t.Fatalf("cmdEnroll: %v", err)
	}
	if id, err := daemon.ReadRunnerID(stateDir); err != nil || id != "runner-42" {
		t.Fatalf("runner ID=%q err=%v", id, err)
	}
	if err := identity.NewStore(stateDir).ValidateIdentity("runner-42", time.Now()); err != nil {
		t.Fatalf("stored identity: %v", err)
	}
	if err := cmdEnroll([]string{"--state-dir", stateDir, "--code-file", codeFile, "--url", server.URL}); err == nil {
		t.Fatal("duplicate enrollment accepted")
	}
}

func TestStartCommandRefusesAStateDirTooLongForTheJobSocket(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), strings.Repeat("s", 110))
	configPath := filepath.Join(t.TempDir(), "runner.yaml")
	raw := fmt.Sprintf(`control_plane_url: wss://control.example.test/stream
state_dir: %q
transport: websocket
isolation: docker
job_image: registry.example.com/runner/job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
supervisor: direct
provider: codex
`, stateDir)
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdStart([]string{"--config", configPath}); err == nil || !strings.Contains(err.Error(), "state_dir is too long for docker isolation") {
		t.Fatalf("cmdStart=%v", err)
	}
}

func TestStartCommandLoadsConfigurationBeforeEnrollmentGate(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	configPath := filepath.Join(t.TempDir(), "runner.yaml")
	raw := fmt.Sprintf(`control_plane_url: wss://control.example.test/stream
state_dir: %q
transport: websocket
isolation: direct
allow_unsafe_direct: true
supervisor: direct
provider: codex
`, stateDir)
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdStart([]string{"--config", configPath}); err == nil || !strings.Contains(err.Error(), "not enrolled") {
		t.Fatalf("cmdStart=%v", err)
	}
}
