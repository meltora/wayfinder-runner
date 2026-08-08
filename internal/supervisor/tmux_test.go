package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTmuxAvailable(t *testing.T) {
	tm := NewTmux(t.TempDir())
	tm.LookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
	if !tm.Available() {
		t.Fatal("should be available")
	}
}

func TestTmuxLifecycle(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	tm := NewTmux(t.TempDir())
	tm.LookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
	tm.run = func(cmd string, args ...string) error { return nil }

	spec := Spec{Name: "test-job", Command: "/bin/sh", Args: []string{"-c", "echo hi"}, Dir: t.TempDir()}
	proc, err := tm.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	tp := proc.(*tmuxProc)
	if !strings.HasPrefix(tp.session, "test-job-job-") {
		t.Fatalf("session = %q", tp.session)
	}
	if err := os.WriteFile(tp.exitFile, []byte("0"), 0o600); err != nil {
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
		t.Fatal("Wait did not return after exitcode written")
	}
}

func TestTmuxWaitStopsAfterKill(t *testing.T) {
	tm := NewTmux(t.TempDir())
	tm.LookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
	tm.run = func(cmd string, args ...string) error { return nil }
	proc, err := tm.Start(context.Background(), Spec{Name: "cancel", Command: "/bin/sh", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait remained blocked after Kill")
	}
}

func TestTmuxWaitNonZero(t *testing.T) {
	tm := NewTmux(t.TempDir())
	tm.LookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
	tm.run = func(cmd string, args ...string) error { return nil }
	proc, _ := tm.Start(context.Background(), Spec{Name: "j", Command: "/bin/sh", Dir: t.TempDir()})
	tp := proc.(*tmuxProc)
	if err := os.WriteFile(tp.exitFile, []byte("3"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := proc.Wait(); err == nil {
		t.Fatal("expected non-zero exit error")
	}
	_ = filepath.Dir(tp.exitFile)
}

func TestTmuxMetadataInterruptAndUnavailable(t *testing.T) {
	tm := NewTmux(t.TempDir())
	tm.LookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
	var calls []string
	tm.run = func(_ string, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	if tm.Name() != "tmux" {
		t.Fatalf("name=%q", tm.Name())
	}
	proc, err := tm.Start(context.Background(), Spec{Name: "metadata", Command: "/bin/sh", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if proc.PID() != 0 {
		t.Fatalf("PID=%d", proc.PID())
	}
	if err := proc.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if len(calls) < 2 || !strings.Contains(calls[len(calls)-1], "send-keys") {
		t.Fatalf("calls=%v", calls)
	}
	_ = proc.Kill()

	missing := NewTmux(t.TempDir())
	missing.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if missing.Available() {
		t.Fatal("missing tmux reported available")
	}
	if _, err := missing.Start(context.Background(), Spec{}); err == nil {
		t.Fatal("missing tmux started")
	}
}

func TestTmuxWaitHonorsContextCancellation(t *testing.T) {
	tm := NewTmux(t.TempDir())
	tm.LookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
	tm.run = func(string, ...string) error { return nil }
	ctx, cancel := context.WithCancel(context.Background())
	proc, err := tm.Start(ctx, Spec{Name: "context", Command: "/bin/sh", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := proc.Wait(); err != context.Canceled {
		t.Fatalf("Wait=%v", err)
	}
}

func TestSessionSanitizationAndShellQuoting(t *testing.T) {
	if sanitizeSession("job !/name") != "jobname" {
		t.Fatalf("sanitize=%q", sanitizeSession("job !/name"))
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Fatalf("shellQuote=%q", got)
	}
}
