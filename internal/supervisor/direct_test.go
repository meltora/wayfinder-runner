package supervisor

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestDirectSupervisorLifecycleAndMetadata(t *testing.T) {
	supervisor := NewDirectProcess()
	if supervisor.Name() != "direct" || !supervisor.Available() {
		t.Fatalf("name=%q available=%v", supervisor.Name(), supervisor.Available())
	}
	var output bytes.Buffer
	process, err := supervisor.Start(context.Background(), Spec{
		Name: "direct", Command: "/bin/sh", Args: []string{"-c", "printf direct"},
		Env: []string{"PATH=/usr/bin:/bin"}, Stdout: &output, Stderr: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if process.PID() <= 0 {
		t.Fatalf("PID=%d", process.PID())
	}
	if err := process.Wait(); err != nil || output.String() != "direct" {
		t.Fatalf("Wait=%v output=%q", err, output.String())
	}
}

func TestDirectSupervisorRejectsMissingBinary(t *testing.T) {
	supervisor := NewDirectProcess()
	supervisor.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	if _, err := supervisor.Start(context.Background(), Spec{Command: "missing"}); err == nil {
		t.Fatal("missing binary started")
	}
}

func TestDirectProcessInterruptAndKill(t *testing.T) {
	for _, stop := range []func(Process) error{
		func(process Process) error { return process.Interrupt() },
		func(process Process) error { return process.Kill() },
	} {
		supervisor := NewDirectProcess()
		process, err := supervisor.Start(context.Background(), Spec{
			Name: "stoppable", Command: "/bin/sh", Args: []string{"-c", "sleep 30"},
			Env: []string{"PATH=/usr/bin:/bin"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := stop(process); err != nil {
			t.Fatal(err)
		}
		if err := process.Wait(); err == nil {
			t.Fatal("stopped process reported success")
		}
	}
}

func TestForwardSessionEnvironmentPolicy(t *testing.T) {
	for _, entry := range []string{"PLATFORM_MCP_SOCKET=/x", "RUNNER_MODE=job", "CODEX_PROFILE=runner", "PATH=/bin"} {
		if !forwardSessionEnvironment(entry) {
			t.Fatalf("required environment %q rejected", entry)
		}
	}
	for _, entry := range []string{"", "SECRET=value", "MALFORMED"} {
		if forwardSessionEnvironment(entry) {
			t.Fatalf("unsafe environment %q forwarded", entry)
		}
	}
}
