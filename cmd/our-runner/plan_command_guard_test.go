package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/plancommand"
)

func planGuardHookInput(t *testing.T, cwd, command string) io.Reader {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse", "cwd": cwd, "tool_name": "Bash",
		"tool_input": map[string]any{"command": command},
	})
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(raw)
}

type guardFailingReader struct{}

func (guardFailingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestPlanCommandGuardExitCodesAndSilence(t *testing.T) {
	workspace := t.TempDir()
	pack := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots := []string{"--root", workspace, "--root", pack}
	cases := []struct {
		name   string
		args   []string
		stdin  io.Reader
		status int
	}{
		{"accepted", roots, planGuardHookInput(t, workspace, "cat README.md"), 0},
		{"denied command", roots, planGuardHookInput(t, workspace, "cat README.md > out"), 2},
		{"denied cwd", roots, planGuardHookInput(t, t.TempDir(), "ls"), 2},
		{"malformed input", roots, strings.NewReader("{"), 2},
		{"empty input", roots, strings.NewReader(""), 2},
		{"oversized input", roots, strings.NewReader(strings.Repeat(" ", plancommand.MaxHookInputBytes+1)), 2},
		{"unreadable input", roots, guardFailingReader{}, 2},
		{"no roots", nil, planGuardHookInput(t, workspace, "ls"), 2},
		{"unknown flag", append([]string{"--allow"}, roots...), planGuardHookInput(t, workspace, "ls"), 2},
		{"missing root value", []string{"--root"}, planGuardHookInput(t, workspace, "ls"), 2},
		{"relative root", []string{"--root", "relative"}, planGuardHookInput(t, workspace, "ls"), 2},
		{"unreadable root", []string{"--root", filepath.Join(workspace, "missing")}, planGuardHookInput(t, workspace, "ls"), 2},
		{"positional", append(roots, "extra"), planGuardHookInput(t, workspace, "ls"), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			status := runPlanCommandGuard(tc.args, tc.stdin, &stdout, &stderr)
			if status != tc.status {
				t.Fatalf("status=%d want=%d stderr=%q", status, tc.status, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("guard wrote to stdout: %q", stdout.String())
			}
			if strings.Contains(stderr.String(), "permissionDecision") {
				t.Fatalf("guard emitted a permission decision: %q", stderr.String())
			}
			if tc.status == 0 && stderr.Len() != 0 {
				t.Fatalf("accepted command wrote stderr: %q", stderr.String())
			}
			if tc.status == 2 && strings.TrimSpace(stderr.String()) == "" {
				t.Fatal("denial has no reason on stderr")
			}
		})
	}
}

func TestRunCLIDispatchesPlanCommandGuard(t *testing.T) {
	if status := runCLI([]string{"plan-command-guard"}); status != 2 {
		t.Fatalf("status=%d", status)
	}
}

type guardBlockingReader struct{ release chan struct{} }

func (r guardBlockingReader) Read([]byte) (int, error) {
	<-r.release
	return 0, io.EOF
}

func TestPlanCommandGuardDeniesWhenUndecidedBeforeDeadline(t *testing.T) {
	workspace := t.TempDir()
	stdin := guardBlockingReader{release: make(chan struct{})}
	defer close(stdin.release)
	var stdout, stderr bytes.Buffer
	status := runPlanCommandGuardWithin([]string{"--root", workspace}, stdin, &stdout, &stderr, 20*time.Millisecond)
	if status != 2 {
		t.Fatalf("status=%d stderr=%q", status, stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "planning command guard denied this command: guard did not decide within") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestPlanCommandGuardDecidesWithinDeadline(t *testing.T) {
	workspace := t.TempDir()
	var stdout, stderr bytes.Buffer
	status := runPlanCommandGuardWithin([]string{"--root", workspace}, planGuardHookInput(t, workspace, "ls"), &stdout, &stderr, time.Minute)
	if status != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}

func TestPlanCommandGuardDeadlineIsWellInsideTheHookTimeout(t *testing.T) {
	if planCommandGuardDeadline <= 0 || planCommandGuardDeadline > 5*time.Second {
		t.Fatalf("deadline=%v", planCommandGuardDeadline)
	}
}
