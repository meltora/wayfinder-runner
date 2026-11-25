package isolation_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/isolation/claudeoffline"
	"github.com/example/app-runner/internal/provider"
	"github.com/example/app-runner/internal/toolsummary"
)

const loggingBubblewrap = `#!/bin/sh
{ echo "argv-begin"; for argument in "$@"; do printf '%s\n' "$argument"; done; echo "argv-end"; } >> /tmp/bwrap-argv.log
exec ` + isolation.UnprivilegedBubblewrapDir + `/bwrap "$@"
`

const offlineClaudeBashScript = `set -u
mkdir -p /home/runner/.claude /home/runner/work
echo TOPSECRET > /home/runner/.claude/canary.txt
export FAKE_BASH_COMMAND='echo sandbox-bash-ok; grep NSpid /proc/self/status; cat /home/runner/.claude/canary.txt 2>/dev/null; touch /home/runner/outside 2>/dev/null; echo "scrub=${CLAUDE_CODE_SUBPROCESS_ENV_SCRUB:-unset}"'
node /probe/messages-api.js &
sleep 1
export PATH=/probe/bin:$PATH ANTHROPIC_BASE_URL=` + claudeoffline.BaseURL + ` ANTHROPIC_API_KEY=offline-probe-key CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
cd /home/runner/work
timeout 120 claude "$@" > /tmp/claude.out 2>&1
echo "==claude-exit:$?"
if [ -e /home/runner/outside ]; then echo "==outside-write:allowed"; else echo "==outside-write:refused"; fi
echo "==claude-out"
cat /tmp/claude.out
echo "==bwrap-argv"
cat /tmp/bwrap-argv.log 2>/dev/null
echo "==end"
`

type offlineClaudeBashRun struct {
	exit         string
	outsideWrite string
	toolResult   string
	toolIsError  bool
	sandboxArgv  []string
	outputLines  []string
}

func writeProbeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func offlineClaudeBuildSpec(t *testing.T, prompt string) ([]string, []string, []byte) {
	t.Helper()
	configDir := t.TempDir()
	spec, err := provider.NewClaude().BuildSpec(provider.StartParams{
		Job:             v1.JobSpec{JobID: "rjic-offline", AttemptID: "rjic-offline-1", Kind: v1.JobKindBuild, FencingToken: 1},
		WorkDir:         "/home/runner/work",
		ConfigDir:       configDir,
		MCPSock:         "/run/job/mcp.sock",
		MCPTokenFile:    "/run/job/job.token",
		MCPBinary:       "/usr/local/bin/our-runner",
		Prompt:          prompt,
		HardenedRuntime: true,
		DockerIsolation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Stdin == nil {
		t.Fatal("Claude job spec has no stdin prompt")
	}
	stdin, err := io.ReadAll(spec.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(configDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := os.Chmod(filepath.Join(configDir, entry.Name()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mounts := []string{"-v", configDir + ":" + configDir + ":ro"}
	for _, entry := range provider.ContainerEnvironment(spec.Env) {
		mounts = append(mounts, "-e", entry)
	}
	return append([]string(nil), spec.Args...), mounts, stdin
}

func runOfflineClaudeBash(t *testing.T, image string, extra ...string) offlineClaudeBashRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	mapping, err := isolation.ResolveUserMapping(ctx, image, providerCLIIntegrationRunner)
	if err != nil {
		t.Fatal(err)
	}
	probeDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(probeDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProbeFile(t, filepath.Join(probeDir, "messages-api.js"), claudeoffline.MessagesAPIScript(), 0o644)
	writeProbeFile(t, filepath.Join(probeDir, "bin", "bwrap"), loggingBubblewrap, 0o755)
	if err := os.Chmod(probeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	args, specFlags, stdin := offlineClaudeBuildSpec(t, "run the sandbox probe")
	uid, gid, _ := strings.Cut(mapping.User(), ":")
	dockerArgs := []string{"run", "--rm", "-i", "--init", "--user", mapping.User(),
		"--cap-drop", "ALL", "--cap-add", "SYS_ADMIN", "--cap-add", "SYS_CHROOT",
		"--cap-add", "SETUID", "--cap-add", "SETGID", "--security-opt", "seccomp=unconfined",
		"--label", "com.ourplatform.runner-id=" + providerCLIIntegrationRunner,
		"--network", "none", "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=256m",
		"--tmpfs", "/home/runner:rw,nosuid,nodev,size=64m,uid=" + uid + ",gid=" + gid,
		"-e", "HOME=/home/runner",
		"-e", "PATH=" + isolation.UnprivilegedBubblewrapDir + ":" + isolation.JobImagePath,
		"-v", probeDir + ":/probe:ro",
	}
	dockerArgs = append(dockerArgs, specFlags...)
	dockerArgs = append(dockerArgs, extra...)
	dockerArgs = append(dockerArgs, image, "sh", "-c", offlineClaudeBashScript, "offline-claude-bash")
	dockerArgs = append(dockerArgs, args...)
	command := exec.CommandContext(ctx, "docker", dockerArgs...)
	command.Stdin = bytes.NewReader(stdin)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("offline claude bash probe failed: %v: %s", err, output)
	}
	assertNoProbeContainersRemain(t)
	return parseOfflineClaudeBashRun(t, string(output))
}

func parseOfflineClaudeBashRun(t *testing.T, output string) offlineClaudeBashRun {
	t.Helper()
	var run offlineClaudeBashRun
	var argv []string
	section := ""
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "==claude-exit:"):
			run.exit = strings.TrimPrefix(line, "==claude-exit:")
		case strings.HasPrefix(line, "==outside-write:"):
			run.outsideWrite = strings.TrimPrefix(line, "==outside-write:")
		case line == "==claude-out" || line == "==bwrap-argv" || line == "==end":
			section = line
		case section == "==claude-out":
			run.outputLines = append(run.outputLines, line)
			if result, isError, ok := claudeoffline.ToolResult(line); ok {
				run.toolResult, run.toolIsError = result, isError
			}
		case section == "==bwrap-argv":
			switch line {
			case "argv-begin":
				argv = nil
			case "argv-end":
				if len(argv) > 1 {
					run.sandboxArgv = argv
				}
			default:
				argv = append(argv, line)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if run.exit == "" {
		t.Fatalf("offline claude bash probe printed no exit status: %s", output)
	}
	return run
}

func procArgument(argv []string) string {
	for index := 0; index+2 < len(argv); index++ {
		if argv[index] == "--bind" && argv[index+1] == "/proc" && argv[index+2] == "/proc" {
			return "bind"
		}
	}
	for index := 0; index+1 < len(argv); index++ {
		if argv[index] == "--proc" {
			return "fresh"
		}
	}
	return "none"
}

func TestClaudeBashRunsInTheWeakerNestedSandboxWithoutTheEnvironmentScrub(t *testing.T) {
	image := requireDockerSandboxGate(t, "JOB_IMAGE")
	run := runOfflineClaudeBash(t, image)
	if run.exit != "0" || run.toolIsError {
		t.Fatalf("claude exit %s, tool error %v: %q", run.exit, run.toolIsError, run.toolResult)
	}
	lines := strings.Split(strings.TrimSpace(run.toolResult), "\n")
	if len(lines) != 3 || lines[0] != "sandbox-bash-ok" || lines[2] != "scrub=unset" {
		t.Fatalf("sandboxed bash output = %q", run.toolResult)
	}
	if fields := strings.Fields(lines[1]); len(fields) < 4 || fields[0] != "NSpid:" {
		t.Fatalf("sandboxed bash did not run in a nested pid namespace: %q", lines[1])
	}
	if strings.Contains(run.toolResult, "TOPSECRET") || run.outsideWrite != "refused" {
		t.Fatalf("sandbox did not refuse the denied read or the outside write: %q, outside write %s", run.toolResult, run.outsideWrite)
	}
	if len(run.sandboxArgv) == 0 {
		t.Fatal("claude did not start bubblewrap through PATH")
	}
	if got := procArgument(run.sandboxArgv); got != "bind" {
		t.Fatalf("bubblewrap /proc handling = %s, want the weaker /proc bind: %q", got, run.sandboxArgv)
	}
}

func TestClaudeBashCannotStartUnderTheEnvironmentScrubInDocker(t *testing.T) {
	image := requireDockerSandboxGate(t, "JOB_IMAGE")
	run := runOfflineClaudeBash(t, image, "-e", "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1")
	if !run.toolIsError || !strings.Contains(run.toolResult, "Can't mount proc") {
		t.Fatalf("scrubbed sandbox result = %q (error %v)", run.toolResult, run.toolIsError)
	}
	if got := procArgument(run.sandboxArgv); got != "fresh" {
		t.Fatalf("bubblewrap /proc handling under the scrub = %s: %q", got, run.sandboxArgv)
	}
}

func claudeToolSummaries(t *testing.T, lines []string) []string {
	t.Helper()
	tracker := toolsummary.NewTracker()
	var summaries []string
	for _, line := range lines {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if summary, ok := toolsummary.Summarize(event, tracker); ok {
			summaries = append(summaries, summary.Text)
		}
	}
	return summaries
}

func assertToolSummarySequence(t *testing.T, got []string, want ...string) {
	t.Helper()
	next := 0
	for _, summary := range got {
		if next < len(want) && summary == want[next] {
			next++
		}
		if strings.Contains(summary, "toolu_") || strings.Contains(summary, "sandbox") || strings.Contains(summary, "/") {
			t.Fatalf("tool summary carries provider content: %q", summary)
		}
	}
	if next != len(want) {
		t.Fatalf("tool summaries = %q, want the sequence %q", got, want)
	}
	t.Logf("tool summaries: %q", got)
}

func TestClaudeBashToolSummaryNamesASucceededCallInDocker(t *testing.T) {
	image := requireDockerSandboxGate(t, "JOB_IMAGE")
	run := runOfflineClaudeBash(t, image)
	if run.exit != "0" || run.toolIsError {
		t.Fatalf("claude exit %s, tool error %v: %q", run.exit, run.toolIsError, run.toolResult)
	}
	assertToolSummarySequence(t, claudeToolSummaries(t, run.outputLines), "Tool call: Bash", "Tool result: Bash succeeded")
}

func TestClaudeBashToolSummaryNamesAFailedCallUnderTheEnvironmentScrubInDocker(t *testing.T) {
	image := requireDockerSandboxGate(t, "JOB_IMAGE")
	run := runOfflineClaudeBash(t, image, "-e", "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1")
	if !run.toolIsError {
		t.Fatalf("scrubbed sandbox result = %q (error %v)", run.toolResult, run.toolIsError)
	}
	assertToolSummarySequence(t, claudeToolSummaries(t, run.outputLines), "Tool call: Bash", "Tool result: Bash failed")
}
