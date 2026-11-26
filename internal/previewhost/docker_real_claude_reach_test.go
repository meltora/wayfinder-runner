package previewhost

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/isolation/claudeoffline"
	"github.com/example/app-runner/internal/provider"
	"github.com/example/app-runner/internal/repo"
)

const claudeReachProbe = `const http = require('http');
const net = require('net');
const proxy = process.env.HTTP_PROXY || process.env.http_proxy || '';
function viaProxy(label, target) {
  return new Promise((resolve) => {
    if (!proxy) { console.log(label + '=no-proxy'); return resolve(); }
    const p = new URL(proxy);
    const headers = { host: new URL(target).host };
    if (p.username) {
      headers['proxy-authorization'] = 'Basic ' + Buffer.from(decodeURIComponent(p.username) + ':' + decodeURIComponent(p.password)).toString('base64');
    }
    const req = http.request({ host: p.hostname, port: p.port, path: target, headers, timeout: 10000 }, (res) => {
      let data = '';
      res.on('data', (chunk) => { data += chunk; });
      res.on('end', () => { console.log(label + '=' + res.statusCode + ' ' + JSON.stringify(data.slice(0, 80))); resolve(); });
    });
    req.on('timeout', () => { console.log(label + '=timeout'); req.destroy(); });
    req.on('error', (error) => { console.log(label + '=error ' + error.code); resolve(); });
    req.end();
  });
}
function direct(label, host, port) {
  return new Promise((resolve) => {
    const socket = net.connect({ host, port, timeout: 5000 }, () => { console.log(label + '=connected'); socket.destroy(); resolve(); });
    socket.on('timeout', () => { console.log(label + '=timeout'); socket.destroy(); resolve(); });
    socket.on('error', (error) => { console.log(label + '=error ' + error.code); resolve(); });
  });
}
async function plainFetch(label, target) {
  try {
    const res = await fetch(target, { signal: AbortSignal.timeout(10000) });
    console.log(label + '=' + res.status + ' ' + JSON.stringify((await res.text()).slice(0, 80)));
  } catch (error) {
    console.log(label + '=error ' + ((error.cause && error.cause.code) || error.name));
  }
}
(async () => {
  console.log('proxy=' + (proxy ? 'set' : 'none'));
  await viaProxy('preview', 'http://our-preview:8080/index.html');
  await viaProxy('other', 'http://example.com/');
  await plainFetch('fetch-preview', 'http://our-preview:8080/index.html');
  await plainFetch('fetch-other', 'http://example.com/');
  await direct('direct', process.argv[2], 8080);
})();
`

const claudeReachScript = `set -u
if getent hosts our-preview >/dev/null 2>&1; then echo "==preview-resolved-before-claude"; fi
export FAKE_BASH_COMMAND="node $PROBE_DIR/reach.js {{address}}" FAKE_BASH_AWAIT_HOST=our-preview
node "$PROBE_DIR/messages-api.js" &
sleep 1
export ANTHROPIC_BASE_URL=` + claudeoffline.BaseURL + ` ANTHROPIC_API_KEY=offline-probe-key CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
timeout 120 claude "$@" > /tmp/claude.out 2>&1
echo "==claude-exit:$?"
echo "==claude-out"
cat /tmp/claude.out
`

type claudeReachRun struct {
	address    string
	exit       string
	toolResult string
	toolError  bool
	output     string
}

func (r claudeReachRun) probe(label string) string {
	for _, line := range strings.Split(r.toolResult, "\n") {
		if value, ok := strings.CutPrefix(line, label+"="); ok {
			return value
		}
	}
	return ""
}

func TestRealDockerClaudeSandboxReachesPreviewIngress(t *testing.T) {
	image := os.Getenv("JOB_IMAGE")
	if os.Getenv("RUN_PREVIEW_HOST_DOCKER") != "1" || image == "" {
		t.Skip("set RUN_PREVIEW_HOST_DOCKER=1 and JOB_IMAGE to a local job image to run against a rootless Docker daemon")
	}
	const runnerID = "runner-real-claude"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	mapping, err := isolation.ResolveUserMapping(ctx, image, runnerID)
	if err != nil {
		t.Fatal(err)
	}
	if !mapping.RequiresOwnershipHandOff() {
		t.Fatalf("mapping %+v is not rootless; set DOCKER_HOST to the rootless daemon, where the nested sandbox can start", mapping)
	}
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controller, previewID := deployClaudeReachPreview(t, ctx, runnerID, filepath.Join(state, "preview"))
	cli := DockerCLI{TempDir: filepath.Join(state, "preview", "tmp")}
	base := "our-preview-claude-reach-base"
	_ = exec.Command("docker", "network", "rm", base).Run()
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", base).Run() })
	if err := cli.CreateNetwork(ctx, NetworkSpec{Name: base, Internal: true, Options: isolatedNetworkOptions()}); err != nil {
		t.Fatal(err)
	}

	t.Run("allowlisted", func(t *testing.T) {
		run := runClaudeReachJob(t, ctx, claudeReachJob{
			controller: controller, previewID: previewID, runnerID: runnerID, attemptID: "claudereach",
			image: image, mapping: mapping, state: state, network: base, previewReach: true,
		})
		if ip := net.ParseIP(run.address); ip == nil || !ip.IsPrivate() {
			t.Fatalf("our-preview resolved to %q, want a private address", run.address)
		}
		if got := run.probe("preview"); got != `200 "preview ok\n"` {
			t.Fatalf("sandboxed request to our-preview = %q:\n%s", got, run.output)
		}
		if got := run.probe("other"); !strings.HasPrefix(got, "403 ") {
			t.Fatalf("sandboxed request to a host off the allowlist = %q:\n%s", got, run.output)
		}
		if got := run.probe("fetch-preview"); got != `200 "preview ok\n"` {
			t.Fatalf("sandboxed node fetch of our-preview = %q:\n%s", got, run.output)
		}
		if got := run.probe("fetch-other"); !strings.HasPrefix(got, "403 ") && !strings.HasPrefix(got, "error ") {
			t.Fatalf("sandboxed node fetch of a host off the allowlist = %q:\n%s", got, run.output)
		}
		if got := run.probe("direct"); !strings.HasPrefix(got, "error ") && got != "timeout" {
			t.Fatalf("sandboxed direct connection to the preview address = %q:\n%s", got, run.output)
		}
	})

	t.Run("not allowlisted", func(t *testing.T) {
		run := runClaudeReachJob(t, ctx, claudeReachJob{
			controller: controller, previewID: previewID, runnerID: runnerID, attemptID: "claudenoreach",
			image: image, mapping: mapping, state: state, network: base, previewReach: false,
		})
		if got := run.probe("preview"); !strings.HasPrefix(got, "403 ") {
			t.Fatalf("sandboxed request to our-preview without preview reach = %q:\n%s", got, run.output)
		}
		if got := run.probe("fetch-preview"); !strings.HasPrefix(got, "403 ") && !strings.HasPrefix(got, "error ") {
			t.Fatalf("sandboxed node fetch of our-preview without preview reach = %q:\n%s", got, run.output)
		}
		if got := run.probe("direct"); !strings.HasPrefix(got, "error ") && got != "timeout" {
			t.Fatalf("sandboxed direct connection to the preview address = %q:\n%s", got, run.output)
		}
	})
}

func deployClaudeReachPreview(t *testing.T, ctx context.Context, runnerID, state string) (*Docker, string) {
	t.Helper()
	runner := staticRunner(t)
	image := pinnedBusybox(t)
	source := t.TempDir()
	gitCommand(t, source, "init", "--quiet")
	if err := os.MkdirAll(filepath.Join(source, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "site", "index.html"), []byte("preview ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, source, "add", ".")
	gitCommand(t, source, "commit", "--quiet", "-m", "site")
	sha := gitCommand(t, source, "rev-parse", "HEAD")
	manager := repo.NewManager(t.TempDir())
	manager.AllowLocal = true
	if _, err := manager.Register(ctx, source); err != nil {
		t.Fatal(err)
	}
	controller, err := New(Config{
		RunnerID: runnerID, StateDir: state, Lease: time.Hour,
		HealthInterval: 200 * time.Millisecond, TeardownTimeout: time.Minute,
		JobUID: os.Getuid(), JobGID: os.Getgid(), GatewayExecutable: runner,
	}, Dependencies{
		Engine: DockerCLI{TempDir: filepath.Join(state, "tmp")}, Workspaces: manager,
		Repositories: localRepositories{"site": source},
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := testOperation()
	operation.PreviewID = "preview-real-claude"
	operation.CommitSet = []v1.PreviewObservationCommit{{RepositoryID: "site", CommitSHA: sha}}
	t.Cleanup(func() { _ = controller.teardownDetached(context.Background(), operation.PreviewID) })
	content := v1.PreviewBlueprintContentV1{
		SchemaVersion: 1,
		App: v1.PreviewBlueprintAppV1{
			Image: image, WorkingDirectory: "site",
			Start: "exec httpd -f -p 8080 -h /workspace/site", Port: 8080,
			HealthChecks: []v1.PreviewBlueprintHealthCheckV1{{ID: "root", Path: "/index.html", ExpectedStatus: 200}},
		},
		Limits: v1.PreviewBlueprintLimitsV1{MemoryMB: 128, CPUs: 0.5, MaxLifetimeMinutes: 30},
		Egress: v1.PreviewBlueprintEgressV1{Mode: v1.PreviewBlueprintEgressAllow, Hosts: []string{"example.com"}},
	}
	if outcome := controller.Deploy(ctx, DeployRequest{Operation: operation, Content: content}).Outcome; outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("deploy outcome = %+v", outcome)
	}
	return controller, operation.PreviewID
}

type claudeReachJob struct {
	controller   *Docker
	previewID    string
	runnerID     string
	attemptID    string
	image        string
	mapping      isolation.UserMapping
	state        string
	network      string
	previewReach bool
}

func runClaudeReachJob(t *testing.T, ctx context.Context, job claudeReachJob) claudeReachRun {
	t.Helper()
	workspace := filepath.Join(job.state, "jobs", "job-"+job.attemptID)
	home := filepath.Join(job.state, "job-homes", "home-"+job.attemptID)
	sockets := filepath.Join(job.state, "sockets", "job-"+job.attemptID)
	for _, dir := range []string{workspace, home, sockets} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	probeDir := filepath.Join(workspace, ".probe")
	if err := os.Mkdir(probeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"messages-api.js": claudeoffline.MessagesAPIScript(), "reach.js": claudeReachProbe} {
		if err := os.WriteFile(filepath.Join(probeDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mcpSock := filepath.Join(sockets, "mcp.sock")
	tokenFile := filepath.Join(sockets, "job.token")
	if err := os.WriteFile(tokenFile, []byte("offline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := provider.NewClaude()
	spec, err := adapter.BuildSpec(provider.StartParams{
		Job:     v1.JobSpec{JobID: "job-" + job.attemptID, AttemptID: job.attemptID, Kind: v1.JobKindBuild, FencingToken: 1},
		WorkDir: workspace, ConfigDir: sockets, MCPSock: mcpSock, MCPTokenFile: tokenFile,
		MCPBinary: "/usr/local/bin/our-runner", Prompt: "run the preview reach probe",
		HardenedRuntime: true, DockerIsolation: true, PreviewReach: job.previewReach,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Stdin == nil {
		t.Fatal("Claude job spec has no stdin prompt")
	}
	var stdin bytes.Buffer
	if _, err := stdin.ReadFrom(spec.Stdin); err != nil {
		t.Fatal(err)
	}
	ownership := isolation.DockerOwnership{Mapping: job.mapping, Image: job.image, StateDir: job.state}
	request := isolation.OwnershipRequest{
		RunnerID: job.runnerID, AttemptID: job.attemptID, Purpose: "job",
		Workspace: []string{workspace}, Private: []string{home, sockets},
	}
	if err := ownership.HandOff(ctx, request); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ownership.Reclaim(context.Background(), request) })

	policy := isolation.Policy{
		User: job.mapping.User(), MemoryMB: 1024, CPUs: 1, PIDs: 512,
		NetworkName: job.network, ReadOnlyFS: true,
		WorkDir: workspace, WorkspaceRoot: workspace, JobHome: home,
		MCPSock: mcpSock, MCPTokenFile: tokenFile,
		Image: job.image, PullNever: true, RunnerID: job.runnerID, AttemptID: job.attemptID,
		NestedSandbox: true, UnprivilegedBwrap: provider.UsesUnprivilegedBubblewrap(adapter),
		Environment:         provider.ContainerEnvironment(spec.Env),
		ManagedConfigMounts: spec.ManagedConfigMounts,
	}
	command, args, err := isolation.Docker{}.Wrap("sh", append([]string{"-c", "PROBE_DIR=" + probeDir + "\n" + claudeReachScript, "claude-reach"}, spec.Args...), policy)
	if err != nil {
		t.Fatal(err)
	}
	container := isolation.ContainerName(job.runnerID, job.attemptID)
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", container).Run()
		_ = job.controller.DetachJob(context.Background(), job.attemptID)
	})
	wrapped := exec.CommandContext(ctx, command, args...)
	wrapped.Stdin = &stdin
	output := &lockedOutput{}
	wrapped.Stdout, wrapped.Stderr = output, output
	if err := wrapped.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = wrapped.Process.Kill()
			_ = wrapped.Wait()
		}
	})
	if err := waitForOutput(ctx, output, "==awaiting-host:our-preview"); err != nil {
		t.Fatalf("Claude did not ask for the Bash call before the preview was attached: %v: %s", err, output.String())
	}
	if err := job.controller.AttachJob(ctx, job.previewID, job.attemptID, container); err != nil {
		t.Fatal(err)
	}
	err = wrapped.Wait()
	waited = true
	if err != nil {
		t.Fatalf("claude job container: %v: %s", err, output.String())
	}
	if err := job.controller.DetachJob(ctx, job.attemptID); err != nil {
		t.Fatal(err)
	}
	run := parseClaudeReachRun(output.String())
	t.Logf("our-preview: %s, claude exit %s, tool result (error %v):\n%s", run.address, run.exit, run.toolError, run.toolResult)
	if strings.Contains(run.output, "==preview-resolved-before-claude") {
		t.Fatalf("our-preview resolved before Claude started, so the run did not attach mid-session:\n%s", run.output)
	}
	if run.exit != "0" || run.toolError || run.probe("proxy") != "set" {
		t.Fatalf("claude exit %q, tool error %v, tool result %q:\n%s", run.exit, run.toolError, run.toolResult, run.output)
	}
	return run
}

type lockedOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (o *lockedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.Write(p)
}

func (o *lockedOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.String()
}

func waitForOutput(ctx context.Context, output *lockedOutput, marker string) error {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), marker) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("no %q within 90s", marker)
}

func parseClaudeReachRun(output string) claudeReachRun {
	run := claudeReachRun{output: output}
	inClaude := false
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "==awaited-address:"):
			run.address = strings.TrimPrefix(line, "==awaited-address:")
		case strings.HasPrefix(line, "==claude-exit:"):
			run.exit = strings.TrimPrefix(line, "==claude-exit:")
		case line == "==claude-out":
			inClaude = true
		case inClaude:
			if result, isError, ok := claudeoffline.ToolResult(line); ok {
				run.toolResult, run.toolError = result, isError
			}
		}
	}
	return run
}
