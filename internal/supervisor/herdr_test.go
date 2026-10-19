package supervisor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type fakeHerdr struct {
	ln         net.Listener
	mu         sync.Mutex
	requests   []herdrRequest
	sentText   string
	sentKeys   []string
	failMethod string
}

func startFakeHerdr(t *testing.T) *fakeHerdr {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "herdr-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "herdr.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeHerdr{ln: ln}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeHerdr) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakeHerdr) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	sc := bufio.NewScanner(conn)
	enc := json.NewEncoder(conn)
	for sc.Scan() {
		var req herdrRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		f.mu.Lock()
		f.requests = append(f.requests, req)
		failMethod := f.failMethod
		f.mu.Unlock()
		if req.Method == failMethod {
			_ = enc.Encode(herdrResponse{ID: req.ID, Error: &herdrError{Code: "failed", Message: "injected"}})
			continue
		}
		var result any
		switch req.Method {
		case "workspace.create":
			result = map[string]any{"workspace": map[string]any{"workspace_id": "w9"}}
		case "pane.list":
			result = map[string]any{"panes": []map[string]any{{"pane_id": "w9:p1"}}}
		case "pane.send_text", "pane.send_input":
			params, _ := req.Params.(map[string]any)
			f.mu.Lock()
			f.sentText, _ = params["text"].(string)
			if rawKeys, ok := params["keys"].([]any); ok {
				f.sentKeys = make([]string, 0, len(rawKeys))
				for _, rawKey := range rawKeys {
					if key, ok := rawKey.(string); ok {
						f.sentKeys = append(f.sentKeys, key)
					}
				}
			}
			f.mu.Unlock()
			result = map[string]any{"type": "ok"}
		case "pane.get":
			result = map[string]any{"pane": map[string]any{"agent_session": map[string]any{"value": "session-1"}}}
		default:
			result = map[string]any{"type": "ok"}
		}
		raw, _ := json.Marshal(result)
		_ = enc.Encode(herdrResponse{ID: req.ID, Result: raw})
	}
}

func (f *fakeHerdr) methodsCalled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		out = append(out, r.Method)
	}
	return out
}

func TestHerdrStartSendsCommand(t *testing.T) {
	fake := startFakeHerdr(t)
	sockPath := fake.ln.Addr().String()
	sup := NewHerdr(sockPath)
	spec := Spec{
		Name:       "job-1",
		Command:    "/bin/echo",
		Args:       []string{"hello world"},
		Dir:        t.TempDir(),
		Env:        []string{"PLATFORM_MCP_TOKEN=abc", "HOME=/x", "UNAPPROVED_SECRET=hidden"},
		RuntimeDir: t.TempDir(),
	}
	proc, err := sup.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	hp := proc.(*herdrProc)
	if hp.paneID != "w9:p1" || hp.workspace != "w9" {
		t.Fatalf("pane=%s workspace=%s", hp.paneID, hp.workspace)
	}
	fake.mu.Lock()
	text := fake.sentText
	keys := append([]string(nil), fake.sentKeys...)
	fake.mu.Unlock()
	launch, err := os.ReadFile(hp.launchFile)
	if err != nil {
		t.Fatalf("read launch script: %v", err)
	}
	launchText := string(launch)
	if !strings.Contains(launchText, "PLATFORM_MCP_TOKEN=abc") {
		t.Fatalf("platform env not forwarded: %q", launchText)
	}
	if !strings.Contains(launchText, "HOME=/x") {
		t.Fatalf("required process env not forwarded: %q", launchText)
	}
	if strings.Contains(launchText, "UNAPPROVED_SECRET") {
		t.Fatalf("non-platform env leaked: %q", launchText)
	}
	if !strings.Contains(launchText, "'/bin/echo' 'hello world'") {
		t.Fatalf("command not quoted: %q", launchText)
	}
	if strings.Contains(text, "PLATFORM_MCP_TOKEN") || strings.Contains(text, "hello world") {
		t.Fatalf("sensitive launch details pasted into terminal: %q", text)
	}
	if text != shellQuote(hp.launchFile) {
		t.Fatalf("submitted command = %q, want launch script %q", text, hp.launchFile)
	}
	if len(keys) != 1 || keys[0] != "enter" {
		t.Fatalf("submission keys = %v", keys)
	}
	methods := fake.methodsCalled()
	want := []string{"workspace.create", "pane.list", "pane.send_input"}
	if len(methods) != len(want) {
		t.Fatalf("methods = %v", methods)
	}
	for i := range want {
		if methods[i] != want[i] {
			t.Fatalf("methods = %v, want %v", methods, want)
		}
	}
}

func TestHerdrKillClosesPane(t *testing.T) {
	fake := startFakeHerdr(t)
	sup := NewHerdr(fake.ln.Addr().String())
	proc, err := sup.Start(context.Background(), Spec{Name: "j", Command: "/bin/sh", Dir: t.TempDir(), RuntimeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	methods := fake.methodsCalled()
	last := methods[len(methods)-2:]
	if last[0] != "pane.close" || last[1] != "workspace.close" {
		t.Fatalf("last methods = %v", methods)
	}
}

func TestHerdrKillPropagatesCleanupFailure(t *testing.T) {
	fake := startFakeHerdr(t)
	sup := NewHerdr(fake.ln.Addr().String())
	proc, err := sup.Start(context.Background(), Spec{
		Name: "cleanup-error", Command: "/bin/sh", Dir: t.TempDir(), RuntimeDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.failMethod = "workspace.close"
	fake.mu.Unlock()
	if err := proc.Kill(); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("Kill=%v", err)
	}
}

func TestHerdrProcessMetadataInterruptAndSessionReporting(t *testing.T) {
	fake := startFakeHerdr(t)
	sup := NewHerdr(fake.ln.Addr().String())
	if sup.Name() != "herdr" || !sup.Available() {
		t.Fatalf("name=%q available=%v", sup.Name(), sup.Available())
	}
	proc, err := sup.Start(context.Background(), Spec{Name: "metadata", Command: "/bin/sh", Dir: t.TempDir(), RuntimeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	hp := proc.(*herdrProc)
	if hp.PID() != 0 {
		t.Fatalf("PID=%d", hp.PID())
	}
	if err := hp.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if err := hp.ReportAgentSession("codex", "session-1"); err != nil {
		t.Fatal(err)
	}
	ref, err := hp.SessionRef()
	if err != nil || ref != "session-1" {
		t.Fatalf("SessionRef=%q err=%v", ref, err)
	}
	before := len(fake.methodsCalled())
	ref, err = hp.SessionRef()
	if err != nil || ref != "session-1" || len(fake.methodsCalled()) != before {
		t.Fatalf("cached SessionRef=%q err=%v", ref, err)
	}
	if err := hp.Kill(); err != nil {
		t.Fatal(err)
	}
	methods := strings.Join(fake.methodsCalled(), ",")
	for _, want := range []string{"pane.send_keys", "pane.report_agent_session", "pane.get"} {
		if !strings.Contains(methods, want) {
			t.Fatalf("methods %q missing %q", methods, want)
		}
	}
}

func TestHerdrWaitExitCode(t *testing.T) {
	fake := startFakeHerdr(t)
	sup := NewHerdr(fake.ln.Addr().String())
	proc, err := sup.Start(context.Background(), Spec{Name: "j2", Command: "/bin/sh", Dir: t.TempDir(), RuntimeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	hp := proc.(*herdrProc)
	if err := os.WriteFile(hp.exitFile, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return")
	}
}

func TestHerdrLaunchScriptWritesPrivateExitFileUnderPermissiveUmask(t *testing.T) {
	runtimeDir := t.TempDir()
	exitFile := filepath.Join(runtimeDir, ".herdr-exit-test")
	outputPath := filepath.Join(runtimeDir, "output.log")
	launch, err := writeHerdrLaunchScript(Spec{Command: "/bin/sh", Args: []string{"-c", "exit 3"}, Dir: t.TempDir(), RuntimeDir: runtimeDir}, exitFile, outputPath)
	if err != nil {
		t.Fatal(err)
	}
	previous := syscall.Umask(0o002)
	runErr := exec.Command(launch).Run()
	syscall.Umask(previous)
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("launch script result = %v, want exit 3", runErr)
	}
	data, err := readExitCode(exitFile)
	if err != nil {
		t.Fatalf("readExitCode: %v", err)
	}
	if strings.TrimSpace(string(data)) != "3" {
		t.Fatalf("exit code = %q, want 3", data)
	}
}

func TestHerdrStreamsProviderOutput(t *testing.T) {
	fake := startFakeHerdr(t)
	var output bytes.Buffer
	proc, err := NewHerdr(fake.ln.Addr().String()).Start(context.Background(), Spec{
		Name:       "output",
		Command:    "/bin/sh",
		Dir:        t.TempDir(),
		RuntimeDir: t.TempDir(),
		Stdout:     &output,
		Stderr:     &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	hp := proc.(*herdrProc)
	providerEvent := []byte(`{"type":"error","error":{"data":{"message":"usage limit reached"}}}` + "\n")
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- os.WriteFile(hp.outputPath, providerEvent, 0o600)
	}()
	if err := <-writeDone; err != nil {
		t.Fatalf("write provider output: %v", err)
	}
	if err := os.WriteFile(hp.exitFile, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := proc.Wait(); err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("Wait=%v", err)
	}
	if got := output.String(); got != string(providerEvent) {
		t.Fatalf("output=%q, want %q", got, providerEvent)
	}
	if _, err := os.Lstat(hp.outputPath); !os.IsNotExist(err) {
		t.Fatalf("output fifo was not removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(hp.outputPath)); !os.IsNotExist(err) {
		t.Fatalf("private output directory was not removed: %v", err)
	}
}

func TestCombineHerdrOutputWritersPreservesDistinctSinks(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	writer := combineHerdrOutputWriters(&stdout, &stderr)
	if _, err := writer.Write([]byte("event\n")); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "event\n" || stderr.String() != "event\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	writer = combineHerdrOutputWriters(&stdout, &stdout)
	if _, err := writer.Write([]byte("once\n")); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "once\n" {
		t.Fatalf("same writer received duplicate output: %q", stdout.String())
	}
}

func TestHerdrWaitPollsUntilExitFileExists(t *testing.T) {
	fake := startFakeHerdr(t)
	proc, err := NewHerdr(fake.ln.Addr().String()).Start(context.Background(), Spec{
		Name: "delayed", Command: "/bin/sh", Dir: t.TempDir(), RuntimeDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	hp := proc.(*herdrProc)
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()

	select {
	case err := <-done:
		t.Fatalf("Wait returned before the process wrote its exit status: %v", err)
	case <-time.After(350 * time.Millisecond):
	}
	if err := os.WriteFile(hp.exitFile, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not observe the delayed exit status")
	}
}

func TestHerdrWaitReportsFailureAndCancellation(t *testing.T) {
	t.Run("nonzero", func(t *testing.T) {
		fake := startFakeHerdr(t)
		proc, err := NewHerdr(fake.ln.Addr().String()).Start(context.Background(), Spec{Name: "failing", Command: "/bin/false", Dir: t.TempDir(), RuntimeDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		hp := proc.(*herdrProc)
		if err := os.WriteFile(hp.exitFile, []byte("17\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := proc.Wait(); err == nil || !strings.Contains(err.Error(), "17") {
			t.Fatalf("Wait=%v", err)
		}
	})
	t.Run("context", func(t *testing.T) {
		fake := startFakeHerdr(t)
		ctx, cancel := context.WithCancel(context.Background())
		proc, err := NewHerdr(fake.ln.Addr().String()).Start(ctx, Spec{Name: "cancelled", Command: "/bin/sh", Dir: t.TempDir(), RuntimeDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := proc.Wait(); err != context.Canceled {
			t.Fatalf("Wait=%v", err)
		}
	})
}

func TestDefaultHerdrSocketPathHonorsEnvironment(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/custom-herdr.sock")
	if got := defaultSocketPath(); got != "/tmp/custom-herdr.sock" {
		t.Fatalf("defaultSocketPath=%q", got)
	}
	if got := NewHerdr("").SocketPath; got != "/tmp/custom-herdr.sock" {
		t.Fatalf("NewHerdr socket=%q", got)
	}
}

func TestHerdrErrorFormatting(t *testing.T) {
	err := (&herdrError{Code: "forbidden", Message: "denied"}).Error()
	if err != "forbidden: denied" {
		t.Fatalf("error=%q", err)
	}
}

func TestHerdrAvailability(t *testing.T) {
	sup := NewHerdr("/nonexistent/herdr.sock")
	if sup.Available() {
		t.Fatal("should not be available")
	}
}
