package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func unsafeRedirectStdinFiles(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	target := writeStdinFixture(t, "secret prompt body")
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(dir, "shared")
	if err := os.WriteFile(shared, []byte("secret prompt body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o644); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"symlink":     link,
		"directory":   dir,
		"fifo":        fifo,
		"missing":     filepath.Join(dir, "missing"),
		"shared 0644": shared,
		"relative":    "prompt",
	}
}

func TestTmuxAndHerdrRefuseUnsafeStdinFilesBeforeAnyExternalCall(t *testing.T) {
	tm := NewTmux(t.TempDir())
	tm.LookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
	tmuxCalls := 0
	tm.run = func(string, ...string) error {
		tmuxCalls++
		return nil
	}
	fake := startFakeHerdr(t)
	herdr := NewHerdr(fake.ln.Addr().String())
	for name, path := range unsafeRedirectStdinFiles(t) {
		for _, sup := range []Supervisor{tm, herdr} {
			t.Run(sup.Name()+"/"+name, func(t *testing.T) {
				process, err := sup.Start(context.Background(), Spec{
					Name: "unsafe", Command: "/bin/sh", Args: []string{"-c", "cat"},
					Dir: t.TempDir(), RuntimeDir: t.TempDir(), StdinFile: path,
				})
				if err == nil {
					_ = process.Kill()
					t.Fatalf("%s accepted an unsafe stdin file (%s)", sup.Name(), name)
				}
				if strings.Contains(err.Error(), "secret prompt body") {
					t.Fatalf("refusal discloses file content: %v", err)
				}
			})
		}
	}
	if tmuxCalls != 0 || len(fake.methodsCalled()) != 0 {
		t.Fatalf("unsafe stdin files reached tmux (%d calls) or Herdr (%v)", tmuxCalls, fake.methodsCalled())
	}
}

func TestTmuxAndHerdrAcceptAPrivateRegularStdinFile(t *testing.T) {
	path := writeStdinFixture(t, "prompt")
	tm := NewTmux(t.TempDir())
	tm.LookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
	tm.run = func(string, ...string) error { return nil }
	fake := startFakeHerdr(t)
	for _, sup := range []Supervisor{tm, NewHerdr(fake.ln.Addr().String())} {
		process, err := sup.Start(context.Background(), Spec{
			Name: "private", Command: "/bin/sh", Dir: t.TempDir(), RuntimeDir: t.TempDir(), StdinFile: path,
		})
		if err != nil {
			t.Fatalf("%s refused a private regular stdin file: %v", sup.Name(), err)
		}
		_ = process.Kill()
	}
}
