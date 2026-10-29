package supervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeStdinFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prompt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func largeStdinPrompt() string {
	return "FR08-START" + strings.Repeat("p'\"$`\\<>&\n", 64<<10) + "FR08-END"
}

func TestDirectProcessStartsWithAPromptOverTheSingleArgumentLimit(t *testing.T) {
	prompt := largeStdinPrompt()
	path := writeStdinFixture(t, prompt)
	var output bytes.Buffer
	process, err := NewDirectProcess().Start(context.Background(), Spec{
		Name: "stdin", Command: "/bin/sh", Args: []string{"-c", "cat"},
		Env: []string{"PATH=/usr/bin:/bin"}, StdinFile: path, Stdout: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	if output.Len() != len(prompt) || sha256.Sum256(output.Bytes()) != sha256.Sum256([]byte(prompt)) {
		t.Fatalf("child read %d bytes, want %d identical bytes", output.Len(), len(prompt))
	}
}

func TestDirectProcessRefusesUnsafeStdinFiles(t *testing.T) {
	dir := t.TempDir()
	target := writeStdinFixture(t, "prompt")
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for name, spec := range map[string]Spec{
		"symlink":   {StdinFile: link},
		"directory": {StdinFile: dir},
		"missing":   {StdinFile: filepath.Join(dir, "missing")},
		"relative":  {StdinFile: "prompt"},
		"both":      {StdinFile: target, Stdin: strings.NewReader("prompt")},
	} {
		t.Run(name, func(t *testing.T) {
			spec.Name, spec.Command, spec.Args = "unsafe", "/bin/sh", []string{"-c", "cat"}
			spec.Env = []string{"PATH=/usr/bin:/bin"}
			if process, err := NewDirectProcess().Start(context.Background(), spec); err == nil {
				_ = process.Wait()
				t.Fatal("unsafe stdin file accepted")
			}
		})
	}
}

func TestTmuxRedirectsTheStdinFileIntoTheProviderCommand(t *testing.T) {
	prompt := largeStdinPrompt()
	path := writeStdinFixture(t, prompt)
	tm := NewTmux(t.TempDir())
	tm.LookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
	var command string
	tm.run = func(_ string, args ...string) error {
		command = args[len(args)-1]
		return nil
	}
	proc, err := tm.Start(context.Background(), Spec{
		Name: "stdin", Command: "/bin/sh", Args: []string{"-c", "cat"}, Dir: t.TempDir(), StdinFile: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Kill() }()
	tp := proc.(*tmuxProc)
	logFile := filepath.Join(tp.dir, "output.log")
	want := "'/bin/sh' '-c' 'cat' < " + shellQuote(path) + " > " + shellQuote(logFile) + " 2>&1; echo $? > "
	if !strings.Contains(command, want) {
		t.Fatalf("tmux command %q lacks %q", command, want)
	}
	if strings.Contains(command, "FR08-START") || len(command) > 16<<10 {
		t.Fatalf("tmux command carries prompt bytes (%d bytes)", len(command))
	}
	if err := exec.Command("/bin/sh", "-c", command).Run(); err != nil {
		t.Fatal(err)
	}
	received, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(received) != prompt {
		t.Fatalf("tmux child read %d bytes, want %d", len(received), len(prompt))
	}
}

func TestHerdrLaunchScriptRedirectsTheStdinFile(t *testing.T) {
	prompt := largeStdinPrompt()
	path := writeStdinFixture(t, prompt)
	fake := startFakeHerdr(t)
	sup := NewHerdr(fake.ln.Addr().String())
	var output bytes.Buffer
	proc, err := sup.Start(context.Background(), Spec{
		Name: "stdin", Command: "/bin/sh", Args: []string{"-c", "cat"}, Dir: t.TempDir(),
		RuntimeDir: t.TempDir(), StdinFile: path, Stdout: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	hp := proc.(*herdrProc)
	script, err := os.ReadFile(hp.launchFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "exec '/bin/sh' '-c' 'cat' < " + shellQuote(path) + "\n) > "
	if !strings.Contains(string(script), want) {
		t.Fatalf("launch script %q lacks %q", script, want)
	}
	if strings.Contains(string(script), "FR08-START") {
		t.Fatal("launch script carries prompt bytes")
	}
	if err := exec.Command(hp.launchFile).Run(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Herdr wait did not finish")
	}
	if output.String() != prompt {
		t.Fatalf("Herdr child read %d bytes, want %d", output.Len(), len(prompt))
	}
}

func TestEverySupervisorRefusesStdinAndStdinFileTogether(t *testing.T) {
	path := writeStdinFixture(t, "prompt")
	tm := NewTmux(t.TempDir())
	tm.LookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
	tmuxCalls := 0
	tm.run = func(string, ...string) error {
		tmuxCalls++
		return nil
	}
	fake := startFakeHerdr(t)
	for _, sup := range []Supervisor{NewDirectProcess(), tm, NewHerdr(fake.ln.Addr().String())} {
		t.Run(sup.Name(), func(t *testing.T) {
			spec := Spec{
				Name: "both", Command: "/bin/sh", Args: []string{"-c", "cat"}, Dir: t.TempDir(),
				RuntimeDir: t.TempDir(), StdinFile: path, Stdin: strings.NewReader("prompt"),
			}
			if process, err := sup.Start(context.Background(), spec); err == nil {
				_ = process.Kill()
				t.Fatal("Stdin and StdinFile accepted together")
			}
		})
	}
	if tmuxCalls != 0 || len(fake.methodsCalled()) != 0 {
		t.Fatalf("refused specs reached tmux (%d calls) or Herdr (%v)", tmuxCalls, fake.methodsCalled())
	}
	for name, sup := range map[string]Supervisor{"tmux": tm, "herdr": NewHerdr(fake.ln.Addr().String())} {
		if process, err := sup.Start(context.Background(), Spec{
			Name: "relative", Command: "/bin/sh", Dir: t.TempDir(), RuntimeDir: t.TempDir(), StdinFile: "prompt",
		}); err == nil {
			_ = process.Kill()
			t.Fatalf("%s accepted a relative stdin file", name)
		}
	}
}
