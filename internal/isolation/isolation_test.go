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
	if _, _, err := docker.Wrap("true", nil, Policy{RunnerID: "runner-1", AttemptID: "attempt-1", User: testJobUser}); err == nil {
		t.Fatal("unapproved default network accepted")
	}
	_, args, err := docker.Wrap("true", nil, Policy{
		RunnerID: "runner-1", AttemptID: "attempt-1", User: testJobUser, NetworkNone: true,
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
		RunnerID: "runner-1", AttemptID: "attempt-1", User: testJobUser, NestedSandbox: true,
		DisableLogs: true,
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
		"--log-driver none",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "no-new-privileges") {
		t.Fatalf("nested sandbox cannot start with no-new-privileges: %s", joined)
	}
	if users := userArguments(args); len(users) != 1 || users[0] != testJobUser {
		t.Fatalf("--user values = %v", users)
	}
}

func TestDockerWrapForwardsStdinAndAddsNoPromptArgument(t *testing.T) {
	stateDir := "/var/lib/our-runner"
	providerArgs := []string{"-p", "--input-format", "text", "--output-format", "stream-json"}
	cmd, args, err := (Docker{}).Wrap("claude", providerArgs, Policy{
		NetworkNone: true, WorkDir: stateDir + "/jobs/job-1/repositories/primary", WorkspaceRoot: stateDir + "/jobs/job-1",
		JobHome: stateDir + "/job-homes/home-1", MCPSock: stateDir + "/sockets/job-1/mcp.sock", MCPTokenFile: stateDir + "/sockets/job-1/job.token",
		RunnerID: "runner-1", AttemptID: "attempt-1", User: testJobUser, NestedSandbox: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "docker" || len(args) < 3 || args[0] != "run" || args[1] != "--rm" || args[2] != "-i" {
		t.Fatalf("docker argv = %s %v, want run --rm -i", cmd, args)
	}
	tail := args[len(args)-len(providerArgs)-1:]
	if tail[0] != "claude" || strings.Join(tail[1:], "\x00") != strings.Join(providerArgs, "\x00") {
		t.Fatalf("provider argv changed by the wrapper: %v", tail)
	}
	for _, arg := range args {
		if strings.Contains(arg, "supervisor") {
			t.Fatalf("docker argv references the supervisor runtime: %q", arg)
		}
	}
}

const testJobUser = "10001:10001"

func userArguments(args []string) []string {
	var users []string
	for index, argument := range args {
		if argument == "--user" && index+1 < len(args) {
			users = append(users, args[index+1])
		}
	}
	return users
}

func TestDockerWrapUsesTheResolvedUserAndNeverTheRunnerProcessIDs(t *testing.T) {
	d := Docker{EffectiveUID: func() int { return 1000 }}
	for _, user := range []string{"10001:10001", "1000:1000", "501:20"} {
		_, args, err := d.Wrap("true", nil, Policy{NetworkNone: true, RunnerID: "runner-1", AttemptID: "attempt-1", User: user})
		if err != nil {
			t.Fatal(err)
		}
		if users := userArguments(args); len(users) != 1 || users[0] != user {
			t.Fatalf("user %s produced --user %v", user, users)
		}
	}
	for _, user := range []string{"", "10001", "runner", "0:10001", "10001:0", "0:0", "-1:5", "10001:10001 ", "10001:10001:1", "99999999999:1"} {
		if _, _, err := d.Wrap("true", nil, Policy{NetworkNone: true, RunnerID: "runner-1", AttemptID: "attempt-1", User: user}); err == nil {
			t.Fatalf("container user %q accepted", user)
		}
	}
}

func TestDockerWrapRefusesHostRoot(t *testing.T) {
	d := Docker{EffectiveUID: func() int { return 0 }}
	_, _, err := d.Wrap("true", nil, Policy{NetworkNone: true, RunnerID: "runner-1", AttemptID: "attempt-1", User: testJobUser})
	if !errors.Is(err, ErrHostRoot) {
		t.Fatalf("err = %v", err)
	}
}

func TestDockerWrapRejectsUnapprovedContainerEnvironment(t *testing.T) {
	d := Docker{}
	base := Policy{NetworkNone: true, RunnerID: "runner-1", AttemptID: "attempt-1", User: testJobUser}
	for _, entry := range []string{"SECRET=value", "OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_CONTENT=bad\nvalue"} {
		p := base
		p.Environment = []string{entry}
		if _, _, err := d.Wrap("opencode", nil, p); err == nil {
			t.Fatalf("invalid container environment %q accepted", entry)
		}
	}
}

func TestDockerWrapForwardsTheNodeEnvironmentProxySwitch(t *testing.T) {
	p := Policy{NetworkNone: true, RunnerID: "runner-1", AttemptID: "attempt-1", User: testJobUser, Environment: []string{"NODE_USE_ENV_PROXY=1"}}
	_, args, err := Docker{}.Wrap("claude", nil, p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "-e NODE_USE_ENV_PROXY=1 ") {
		t.Fatalf("docker args = %q", args)
	}
}

func TestDockerWrapForwardsProviderAPIKeysByNameOnly(t *testing.T) {
	d := Docker{}
	p := Policy{NetworkNone: true, RunnerID: "runner-1", AttemptID: "attempt-1", User: testJobUser, ForwardedSecretNames: []string{"ANTHROPIC_API_KEY"}}
	_, args, err := d.Wrap("claude", nil, p)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-e ANTHROPIC_API_KEY ") || strings.Contains(joined, "ANTHROPIC_API_KEY=") {
		t.Fatalf("docker args must forward the key by name only: %s", joined)
	}
	for _, name := range []string{"GH_TOKEN", "ANTHROPIC_API_KEY=value", "PATH"} {
		p.ForwardedSecretNames = []string{name}
		if _, _, err := d.Wrap("claude", nil, p); err == nil {
			t.Fatalf("forwarded secret name %q accepted", name)
		}
	}
}

func TestDockerWrapAttachesARestrictedJobToTheNamedNetwork(t *testing.T) {
	_, args, err := (Docker{}).Wrap("claude", nil, Policy{
		NetworkNone: false, NetworkName: "our-runner-jobs-dev", RunnerID: "runner-1", AttemptID: "attempt-1", User: testJobUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--network our-runner-jobs-dev") || strings.Contains(joined, "--network none") {
		t.Fatalf("restricted job network args = %s", joined)
	}
}

func TestDockerWrapPinsConsultationNetworkIdentityAndDisablesLogs(t *testing.T) {
	networkID := strings.Repeat("a", 64)
	_, args, err := (Docker{}).Wrap("codex", []string{"exec"}, Policy{
		NetworkName: networkID, RunnerID: "runner-1", AttemptID: "attempt-1", User: testJobUser,
		ReadOnlyFS: true, DisableLogs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--network "+networkID) ||
		!strings.Contains(joined, "--log-driver none") {
		t.Fatalf("consultation Docker boundary is not identity/log pinned: %s", joined)
	}
	if strings.Contains(joined, "provider-egress") {
		t.Fatalf("reusable network name reached Docker start: %s", joined)
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
		NetworkNone: true, RunnerID: "runner-1", AttemptID: "attempt-1", User: testJobUser,
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
		Image: "registry/job@sha256:abc", RunnerID: "runner-1", AttemptID: "check-1", User: testJobUser,
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
	mapping, err := ResolveUserMapping(ctx, image, "doctor")
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	if err := ProbeNestedSandbox(ctx, state, image, mapping); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(filepath.Join(state, "doctor")); err != nil || len(entries) != 0 {
		t.Fatalf("probe left %v (%v)", entries, err)
	}
}
