package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewTmuxDefaultsRunTheTmuxExecutableFromPath(t *testing.T) {
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + record + "'\n[ \"$1\" != fail ] || { echo boom; exit 3; }\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	dir := t.TempDir()
	tm := NewTmux(dir)
	if tm.Dir != dir || tm.LookPath == nil || tm.run == nil {
		t.Fatalf("NewTmux defaults = %+v", tm)
	}
	if !tm.Available() {
		t.Fatal("tmux on PATH was not found by the default lookup")
	}
	if err := tm.run("tmux", "has-session", "-t", "x"); err != nil {
		t.Fatal(err)
	}
	err := tm.run("tmux", "fail", "now")
	if err == nil || !strings.Contains(err.Error(), "tmux fail now") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("failing tmux error = %v", err)
	}
	recorded, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if string(recorded) != "has-session -t x\nfail now\n" {
		t.Fatalf("tmux received %q", recorded)
	}
	tm.LookPath = nil
	if !tm.Available() {
		t.Fatal("a nil LookPath did not fall back to exec.LookPath")
	}
}
