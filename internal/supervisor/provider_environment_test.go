package supervisor

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestDirectSupervisorAppliesProviderEnvironment(t *testing.T) {
	var output bytes.Buffer
	process, err := NewDirectProcess().Start(context.Background(), Spec{
		Name: "direct", Command: "/bin/sh", Args: []string{"-c", `printf '%s|%s' "$CLAUDE_CONFIG_DIR" "$LANG"`},
		Env:                 []string{"PATH=/usr/bin:/bin", "LANG=C"},
		ProviderEnvironment: []string{"CLAUDE_CONFIG_DIR=/accounts/xogito", "LANG=en_US.UTF-8"},
		Stdout:              &output, Stderr: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil || output.String() != "/accounts/xogito|en_US.UTF-8" {
		t.Fatalf("Wait=%v output=%q", err, output.String())
	}
}

func TestHerdrLaunchScriptExportsProviderEnvironment(t *testing.T) {
	fake := startFakeHerdr(t)
	proc, err := NewHerdr(fake.ln.Addr().String()).Start(context.Background(), Spec{
		Name: "job-env", Command: "/bin/echo", Dir: t.TempDir(), RuntimeDir: t.TempDir(),
		Env:                 []string{"HOME=/x", "CLAUDE_CONFIG_DIR=/unapproved"},
		ProviderEnvironment: []string{"CLAUDE_CONFIG_DIR=/accounts/xogito"},
	})
	if err != nil {
		t.Fatal(err)
	}
	launch, err := os.ReadFile(proc.(*herdrProc).launchFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(launch)
	if !strings.Contains(text, "export 'CLAUDE_CONFIG_DIR=/accounts/xogito'") || strings.Contains(text, "/unapproved") {
		t.Fatalf("launch script = %q", text)
	}
}

func TestTmuxCommandExportsProviderEnvironment(t *testing.T) {
	tm := NewTmux(t.TempDir())
	tm.LookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
	var command string
	tm.run = func(_ string, args ...string) error {
		command = args[len(args)-1]
		return nil
	}
	if _, err := tm.Start(context.Background(), Spec{
		Name: "job-env", Command: "/bin/sh", Dir: t.TempDir(),
		Env:                 []string{"CLAUDE_CONFIG_DIR=/unapproved"},
		ProviderEnvironment: []string{"CLAUDE_CONFIG_DIR=/accounts/xogito"},
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "export 'CLAUDE_CONFIG_DIR=/accounts/xogito';") || strings.Contains(command, "/unapproved") {
		t.Fatalf("tmux command = %q", command)
	}
}
