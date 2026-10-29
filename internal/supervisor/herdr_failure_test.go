package supervisor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func assertNoHerdrRuntimeEntries(t *testing.T, runtimeDir string) {
	t.Helper()
	entries, err := os.ReadDir(runtimeDir)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		for _, prefix := range []string{".herdr-launch-", ".herdr-exit-", ".herdr-output-"} {
			if strings.HasPrefix(entry.Name(), prefix) {
				t.Fatalf("failed start left %s in the runtime directory", entry.Name())
			}
		}
	}
}

func TestHerdrStartFailuresCloseTheWorkspaceAndLeaveNoRuntimeFiles(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name           string
		failMethod     string
		results        map[string]any
		runtimeDir     func(t *testing.T) string
		wantWorkspace  bool
		wantErrContent string
	}{
		{name: "workspace.create error", failMethod: "workspace.create", wantErrContent: "workspace.create"},
		{name: "pane.list error", failMethod: "pane.list", wantWorkspace: true, wantErrContent: "pane.list"},
		{
			name: "empty pane list", results: map[string]any{"pane.list": map[string]any{"panes": []any{}}},
			wantWorkspace: true, wantErrContent: "no pane found",
		},
		{
			name: "missing runtime directory", runtimeDir: func(*testing.T) string { return "" },
			wantWorkspace: true, wantErrContent: "runtime directory",
		},
		{
			name: "runtime directory not creatable", runtimeDir: func(*testing.T) string { return filepath.Join(blocker, "runtime") },
			wantWorkspace: true,
		},
		{name: "pane.send_input error", failMethod: "pane.send_input", wantWorkspace: true, wantErrContent: "pane.send_input"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := startFakeHerdr(t)
			fake.mu.Lock()
			fake.failMethod, fake.results = tc.failMethod, tc.results
			fake.mu.Unlock()
			runtimeDir := t.TempDir()
			if tc.runtimeDir != nil {
				runtimeDir = tc.runtimeDir(t)
			}
			process, err := NewHerdr(fake.ln.Addr().String()).Start(context.Background(), Spec{
				Name: "failure", Command: "/bin/sh", Dir: t.TempDir(), RuntimeDir: runtimeDir,
			})
			if err == nil {
				_ = process.Kill()
				t.Fatal("Start succeeded")
			}
			if tc.wantErrContent != "" && !strings.Contains(err.Error(), tc.wantErrContent) {
				t.Fatalf("Start error = %v, want %q", err, tc.wantErrContent)
			}
			methods := fake.methodsCalled()
			if closed := slices.Contains(methods, "workspace.close"); closed != tc.wantWorkspace {
				t.Fatalf("workspace.close called = %v, want %v (methods %v)", closed, tc.wantWorkspace, methods)
			}
			if runtimeDir != "" {
				assertNoHerdrRuntimeEntries(t, runtimeDir)
			}
		})
	}
}

func TestHerdrRefusesAStdinConflictBeforeDialling(t *testing.T) {
	dialled := false
	sup := NewHerdr("/nonexistent/herdr.sock")
	sup.dial = func(string) (herdrConn, error) {
		dialled = true
		return nil, os.ErrNotExist
	}
	_, err := sup.Start(context.Background(), Spec{
		Name: "conflict", Command: "/bin/sh", StdinFile: writeStdinFixture(t, "prompt"), Stdin: strings.NewReader("prompt"),
	})
	if err == nil || dialled {
		t.Fatalf("Start error = %v, dialled = %v", err, dialled)
	}
}

func TestHerdrReportsADialFailure(t *testing.T) {
	sup := NewHerdr("/nonexistent/herdr.sock")
	sup.dial = func(string) (herdrConn, error) { return nil, os.ErrNotExist }
	if _, err := sup.Start(context.Background(), Spec{Name: "dial", Command: "/bin/sh"}); err == nil || !strings.Contains(err.Error(), "herdr connect") {
		t.Fatalf("Start error = %v", err)
	}
}

func TestHerdrFindsAPaneInTheSingularResponseShape(t *testing.T) {
	fake := startFakeHerdr(t)
	fake.mu.Lock()
	fake.results = map[string]any{"pane.list": map[string]any{"pane": map[string]any{"pane_id": "w9:p7"}}}
	fake.mu.Unlock()
	process, err := NewHerdr(fake.ln.Addr().String()).Start(context.Background(), Spec{
		Name: "singular", Command: "/bin/sh", Dir: t.TempDir(), RuntimeDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Kill() }()
	if paneID := process.(*herdrProc).paneID; paneID != "w9:p7" {
		t.Fatalf("pane = %q", paneID)
	}
}

func TestHerdrFallsBackToATopLevelWorkspaceID(t *testing.T) {
	fake := startFakeHerdr(t)
	fake.mu.Lock()
	fake.results = map[string]any{"workspace.create": map[string]any{"workspace_id": "w3"}}
	fake.mu.Unlock()
	process, err := NewHerdr(fake.ln.Addr().String()).Start(context.Background(), Spec{
		Name: "top-level", Command: "/bin/sh", Dir: t.TempDir(), RuntimeDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Kill() }()
	if workspace := process.(*herdrProc).workspace; workspace != "w3" {
		t.Fatalf("workspace = %q", workspace)
	}
}

func TestHerdrOutputFIFORequiresAWritableRuntimeDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := createHerdrOutputFIFO(dir); err == nil {
		t.Fatal("output FIFO created in a read-only directory")
	}
	if _, _, _, err := prepareHerdrOutput(filepath.Join(t.TempDir(), "missing", "stream.fifo"), nil); err == nil {
		t.Fatal("output prepared for a missing FIFO")
	}
}

func TestHerdrAwaitOutputTimesOutAndClosesTheReader(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	process := &herdrProc{outputDone: make(chan error), outputReader: reader}
	started := time.Now()
	err = process.awaitOutput()
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("awaitOutput = %v", err)
	}
	if elapsed := time.Since(started); elapsed < 5*time.Second {
		t.Fatalf("awaitOutput returned after %s, before its drain timeout", elapsed)
	}
	if _, err := reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("output reader is still open after the timeout")
	}
	if again := process.awaitOutput(); again != err {
		t.Fatalf("second awaitOutput = %v", again)
	}
}

func TestDefaultHerdrSocketPathFallsBackToTheHomeDirectory(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := defaultSocketPath(), filepath.Join(home, ".config", "herdr", "herdr.sock"); got != want {
		t.Fatalf("defaultSocketPath = %q, want %q", got, want)
	}
	t.Setenv("HOME", "")
	if got := defaultSocketPath(); got != "" {
		t.Fatalf("defaultSocketPath without a home = %q", got)
	}
	if NewHerdr("").Available() {
		t.Fatal("Herdr without a socket path reported available")
	}
}
