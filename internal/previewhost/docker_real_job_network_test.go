package previewhost

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/repo"
)

func TestRealDockerJobNetworkReachesOnlyIngress(t *testing.T) {
	if os.Getenv("RUN_PREVIEW_HOST_DOCKER") != "1" {
		t.Skip("set RUN_PREVIEW_HOST_DOCKER=1 to run against a real Docker daemon")
	}
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	manager := repo.NewManager(t.TempDir())
	manager.AllowLocal = true
	if _, err := manager.Register(ctx, source); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	cli := DockerCLI{TempDir: filepath.Join(state, "tmp")}
	controller, err := New(Config{
		RunnerID: "runner-real", StateDir: state, Lease: time.Hour,
		HealthInterval: 200 * time.Millisecond, TeardownTimeout: time.Minute,
		JobUID: os.Getuid(), JobGID: os.Getgid(), GatewayExecutable: runner,
	}, Dependencies{
		Engine: cli, Workspaces: manager,
		Repositories: localRepositories{"site": source},
	})
	if err != nil {
		t.Fatal(err)
	}
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
	operation := testOperation()
	operation.PreviewID = "preview-real-job"
	operation.CommitSet = []v1.PreviewObservationCommit{{RepositoryID: "site", CommitSHA: sha}}
	t.Cleanup(func() {
		_ = controller.teardownDetached(context.Background(), operation.PreviewID)
	})
	if outcome := controller.Deploy(ctx, DeployRequest{Operation: operation, Content: content}).Outcome; outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("deploy outcome = %+v", outcome)
	}

	attemptID := "attempt-real-job"
	base := "our-preview-jobtest-base"
	job := "our-preview-jobtest-container"
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", job).Run()
		_ = exec.Command("docker", "network", "rm", base).Run()
		_ = controller.DetachJob(context.Background(), attemptID)
	})
	if err := cli.CreateNetwork(ctx, NetworkSpec{Name: base, Internal: true, Options: isolatedNetworkOptions()}); err != nil {
		t.Fatal(err)
	}
	if err := cli.RunDetached(ctx, ContainerSpec{
		Name: job, Image: image, Network: base, User: "65534:65534", ReadOnly: true,
		MemoryMB: 64, CPUs: 0.25, PidsLimit: 64, Command: []string{"sleep", "600"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := controller.AttachJob(ctx, operation.PreviewID, attemptID, job); err != nil {
		t.Fatal(err)
	}

	if output, err := cli.Exec(ctx, job, []string{"wget", "-q", "-T", "5", "-O", "-", "http://our-preview:8080/index.html"}); err != nil || output != "preview ok\n" {
		t.Fatalf("job container could not reach the preview ingress: %q %v", output, err)
	}
	request := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"
	output, _ := cli.Exec(ctx, job, []string{"sh", "-c", `printf "$0" | nc -w 5 our-preview 3128`, request})
	if strings.Contains(output, "HTTP/") {
		t.Fatalf("job container reached the egress proxy: %q", output)
	}
	routes, err := cli.Exec(ctx, job, []string{"ip", "route", "show"})
	if err != nil {
		t.Fatalf("route probe failed: %q %v", routes, err)
	}
	for _, line := range strings.Split(routes, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "default") {
			t.Fatalf("job container has a default route: %q", routes)
		}
	}
	hostListener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hostListener.Close() }()
	go func() {
		for {
			conn, err := hostListener.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("HTTP/1.0 200 OK\r\n\r\nhost listener\n"))
			_ = conn.Close()
		}
	}()
	_, hostPort, _ := net.SplitHostPort(hostListener.Addr().String())
	network := JobNetworkName("runner-real", attemptID)
	targets := map[string]string{
		"public":         "http://1.1.1.1/",
		"metadata":       "http://169.254.169.254/latest/meta-data/",
		"bridge gateway": "http://" + net.JoinHostPort(networkGateway(t, ctx, "bridge"), hostPort) + "/",
		"preview app":    "http://app:8080/index.html",
	}
	if own, found := optionalNetworkGateway(t, ctx, network); found {
		targets["job network gateway"] = "http://" + net.JoinHostPort(own, hostPort) + "/"
	}
	for name, target := range targets {
		output, err := cli.Exec(ctx, job, []string{"wget", "-q", "-T", "5", "-O", "-", target})
		if err == nil || strings.Contains(output, "host listener") || strings.Contains(output, "preview ok") {
			t.Fatalf("%s %s reachable from the job network: %q", name, target, output)
		}
	}

	if err := controller.DetachJob(ctx, attemptID); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.CommandContext(ctx, "docker", "network", "inspect", network).CombinedOutput(); err == nil {
		t.Fatalf("job network survived DetachJob: %s", output)
	}
	if output, err := cli.Exec(ctx, job, []string{"wget", "-q", "-T", "5", "-O", "-", "http://our-preview:8080/index.html"}); err == nil {
		t.Fatalf("preview reachable after DetachJob: %q", output)
	}
}
