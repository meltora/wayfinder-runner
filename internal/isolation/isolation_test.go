package isolation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/supervisor"
)

func TestDirectPassthrough(t *testing.T) {
	direct := Direct{}
	if direct.Name() != "direct" || !direct.Available() {
		t.Fatalf("name=%q available=%v", direct.Name(), direct.Available())
	}
	cmd, args, err := direct.Wrap("codex", []string{"exec"}, Policy{})
	if err != nil || cmd != "codex" || args[0] != "exec" {
		t.Fatalf("got %s %v %v", cmd, args, err)
	}
}

func TestDockerAvailabilityAndPolicyValidation(t *testing.T) {
	docker := Docker{LookPath: func(string) (string, error) { return "", errors.New("missing") }}
	if docker.Name() != "docker" || docker.Available() {
		t.Fatalf("name=%q available=%v", docker.Name(), docker.Available())
	}
	if _, _, err := docker.Wrap("true", nil, Policy{NetworkNone: true}); err == nil {
		t.Fatal("missing container identity accepted")
	}
	if _, _, err := docker.Wrap("true", nil, Policy{RunnerID: "runner-1", AttemptID: "attempt-1"}); err == nil {
		t.Fatal("unapproved default network accepted")
	}
	_, args, err := docker.Wrap("true", nil, Policy{
		RunnerID: "runner-1", AttemptID: "attempt-1", NetworkNone: true,
		AgentPackDir: "/pack", MCPSock: "/run/a/mcp.sock", MCPTokenFile: "/run/b/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"/pack:/pack:ro", "/run/a/mcp.sock:/run/a/mcp.sock:ro", "/run/b/token:/run/b/token:ro", DefaultJobImage} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args %q missing %q", joined, want)
		}
	}
}

func TestSelectDockerWhenExecutableExists(t *testing.T) {
	dir := t.TempDir()
	dockerPath := filepath.Join(dir, "docker")
	if err := os.WriteFile(dockerPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	selected, err := Select("docker", false)
	if err != nil || selected.Name() != "docker" {
		t.Fatalf("selected=%v err=%v", selected, err)
	}
}

func TestDockerWrap(t *testing.T) {
	d := Docker{LookPath: func(string) (string, error) { return "/usr/bin/docker", nil }}
	if !d.Available() {
		t.Fatal("should be available")
	}
	cmd, args, err := d.Wrap("claude", []string{"-p", "hi"}, Policy{
		MemoryMB: 512, CPUs: 1.5, PIDs: 128, NetworkNone: true, ReadOnlyFS: true,
		ReadOnlyWorkDir: true, WorkDir: "/workspace/repositories/primary", WorkspaceRoot: "/workspace", JobHome: "/job-home",
		MCPSock: "/run/job/mcp.sock", MCPTokenFile: "/run/job/token",
		RunnerID: "runner-1", AttemptID: "attempt-1", NestedSandbox: true,
		Environment: []string{"CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "docker" {
		t.Fatalf("cmd = %s", cmd)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--memory 512m", "--cpus 1.5", "--pids-limit 128", "--network none", "--read-only",
		"--cap-drop ALL", "--cap-add SYS_ADMIN", "seccomp=unconfined", "--init", "/workspace:/workspace:ro", "-w /workspace/repositories/primary", "/job-home:/home/runner:rw", "claude -p hi",
		"/run/job:/run/job:ro",
		"-e CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1",
		"--name our-runner-", "com.ourplatform.runner-id=runner-1", "com.ourplatform.attempt-id=attempt-1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "no-new-privileges") {
		t.Fatalf("nested sandbox cannot start with no-new-privileges: %s", joined)
	}
}

func TestDockerWrapRejectsUnapprovedContainerEnvironment(t *testing.T) {
	d := Docker{}
	base := Policy{NetworkNone: true, RunnerID: "runner-1", AttemptID: "attempt-1"}
	for _, entry := range []string{"SECRET=value", "OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_CONTENT=bad\nvalue"} {
		p := base
		p.Environment = []string{entry}
		if _, _, err := d.Wrap("opencode", nil, p); err == nil {
			t.Fatalf("invalid container environment %q accepted", entry)
		}
	}
}

func TestDockerWrapAllowsOnlyPrivateRunnerManagedConfigMount(t *testing.T) {
	d := Docker{}
	configDir := t.TempDir()
	requirements := filepath.Join(configDir, "codex-requirements.toml")
	if err := os.WriteFile(requirements, []byte("[features]\nhooks = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := Policy{
		NetworkNone: true, RunnerID: "runner-1", AttemptID: "attempt-1",
		MCPSock: filepath.Join(configDir, "mcp.sock"), MCPTokenFile: filepath.Join(configDir, "job.token"),
	}
	base.ManagedConfigMounts = []supervisor.ManagedConfigMount{{Source: requirements, Target: "/etc/codex/requirements.toml"}}
	_, args, err := d.Wrap("codex", nil, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), requirements+":/etc/codex/requirements.toml:ro") {
		t.Fatalf("managed mount absent: %v", args)
	}

	for _, mount := range []supervisor.ManagedConfigMount{
		{Source: "/etc/passwd", Target: "/etc/codex/requirements.toml"},
		{Source: requirements, Target: "/root/.ssh/authorized_keys"},
	} {
		policy := base
		policy.ManagedConfigMounts = []supervisor.ManagedConfigMount{mount}
		if _, _, err := d.Wrap("codex", nil, policy); err == nil {
			t.Fatalf("unsafe managed mount accepted: %+v", mount)
		}
	}
}

func TestDockerCredentialFreeTaskRetainsNoNewPrivileges(t *testing.T) {
	d := Docker{LookPath: func(string) (string, error) { return "/usr/bin/docker", nil }}
	_, args, err := d.Wrap("git", []string{"status"}, Policy{
		NetworkNone: true, ReadOnlyFS: true, WorkDir: "/workspace", WorkspaceRoot: "/workspace",
		Image: "registry/job@sha256:abc", RunnerID: "runner-1", AttemptID: "check-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "no-new-privileges") || strings.Contains(joined, "--cap-add") || strings.Contains(joined, "seccomp=unconfined") {
		t.Fatalf("credential-free task was unnecessarily elevated: %s", joined)
	}
}

func TestSelectDirectRequiresUnsafeOptIn(t *testing.T) {
	if _, err := Select("direct", false); err == nil {
		t.Fatal("direct execution should fail closed")
	}
	if _, err := Select("direct", true); err != nil {
		t.Fatalf("unsafe development opt-in rejected: %v", err)
	}
}

func TestSelectUnknown(t *testing.T) {
	if _, err := Select("gvisor", false); err == nil {
		t.Fatal("expected error")
	}
}

func TestNestedSandboxContainerIntegration(t *testing.T) {
	if os.Getenv("RUN_DOCKER_SANDBOX_PROBE") != "1" {
		t.Skip("set RUN_DOCKER_SANDBOX_PROBE=1 for the container integration gate")
	}
	image := os.Getenv("JOB_IMAGE")
	if image == "" {
		t.Fatal("JOB_IMAGE is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := ProbeNestedSandbox(ctx, t.TempDir(), image); err != nil {
		t.Fatal(err)
	}
}
