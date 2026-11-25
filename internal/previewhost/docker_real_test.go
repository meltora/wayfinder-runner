package previewhost

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/repo"
)

type localRepositories map[string]string

func (repositories localRepositories) ResolveRepository(_ context.Context, id string, _ *v1.ProviderCredential) (ResolvedRepository, error) {
	return ResolvedRepository{URL: repositories[id]}, nil
}

func gitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=preview", "-c", "user.email=preview@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func pinnedBusybox(t *testing.T) string {
	t.Helper()
	if output, err := exec.Command("docker", "pull", "--quiet", "busybox:1.36").CombinedOutput(); err != nil {
		t.Fatalf("docker pull: %v: %s", err, output)
	}
	output, err := exec.Command("docker", "image", "inspect", "--format", "{{index .RepoDigests 0}}", "busybox:1.36").CombinedOutput()
	if err != nil {
		t.Fatalf("docker image inspect: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func staticRunner(t *testing.T) string {
	t.Helper()
	output := filepath.Join(t.TempDir(), "our-runner")
	cmd := exec.Command("go", "build", "-o", output, "./cmd/our-runner")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, combined)
	}
	return output
}

func networkGateway(t *testing.T, ctx context.Context, network string) string {
	t.Helper()
	address, found := optionalNetworkGateway(t, ctx, network)
	if !found {
		t.Fatalf("network %s has no IPv4 gateway", network)
	}
	return address
}

func optionalNetworkGateway(t *testing.T, ctx context.Context, network string) (string, bool) {
	t.Helper()
	output, err := exec.CommandContext(ctx, "docker", "network", "inspect", "--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}", network).CombinedOutput()
	if err != nil {
		t.Fatalf("docker network inspect %s: %v: %s", network, err, output)
	}
	for _, field := range strings.Fields(string(output)) {
		if address, err := netip.ParseAddr(field); err == nil && address.Is4() {
			return address.String(), true
		}
	}
	return "", false
}

func ipv6Routable(address netip.Addr) bool {
	return !address.IsLinkLocalUnicast() && !address.IsMulticast() && !address.IsLoopback()
}

func routableIPv6Routes(output string) []string {
	var routable []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		destination := fields[0]
		if destination == "local" || destination == "multicast" || destination == "anycast" {
			if len(fields) < 2 {
				continue
			}
			destination = fields[1]
		}
		if prefix, err := netip.ParsePrefix(destination); err == nil && !ipv6Routable(prefix.Addr()) {
			continue
		}
		if address, err := netip.ParseAddr(destination); err == nil && !ipv6Routable(address) {
			continue
		}
		routable = append(routable, line)
	}
	return routable
}

func probeFromPreviewNetwork(ctx context.Context, cli DockerCLI, prefix, previewID, image, name string, command ...string) (string, error) {
	return cli.RunOnce(ctx, ContainerSpec{
		Name: prefix + "-probe-" + name, Image: image, Network: prefix,
		Labels: PreviewLabels("runner-real", previewID), User: "65534:65534", ReadOnly: true,
		MemoryMB: 64, CPUs: 0.25, PidsLimit: 64, Command: command,
	})
}

func TestRoutableIPv6Routes(t *testing.T) {
	output := "fe80::/64 dev eth0  metric 256 \nmulticast ff00::/8 dev eth0  metric 256 \nlocal ::1 dev lo\n" +
		"2001:db8::/64 dev eth0  metric 256 \ndefault via fe80::1 dev eth0  metric 1024 \n"
	routable := routableIPv6Routes(output)
	if len(routable) != 2 || !strings.HasPrefix(routable[0], "2001:db8::/64") || !strings.HasPrefix(routable[1], "default") {
		t.Fatalf("routable = %q", routable)
	}
	if routable := routableIPv6Routes(""); len(routable) != 0 {
		t.Fatalf("routable = %q", routable)
	}
}

func TestRealDockerDeployAndDestroy(t *testing.T) {
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
			Install: []string{"test -f index.html"},
			Start:   "exec httpd -f -p 8080 -h /workspace/site", Port: 8080,
			HealthChecks: []v1.PreviewBlueprintHealthCheckV1{{ID: "root", Path: "/index.html", ExpectedStatus: 200}},
			Env:          map[string]v1.PreviewBlueprintEnvEntryV1{"PUBLIC_URL": generated("preview_url")},
		},
		Limits: v1.PreviewBlueprintLimitsV1{MemoryMB: 128, CPUs: 0.5, MaxLifetimeMinutes: 30},
		Egress: v1.PreviewBlueprintEgressV1{Mode: v1.PreviewBlueprintEgressNone},
	}
	operation := testOperation()
	operation.PreviewID = "preview-real"
	operation.CommitSet = []v1.PreviewObservationCommit{{RepositoryID: "site", CommitSHA: sha}}
	t.Cleanup(func() {
		_ = controller.teardownDetached(context.Background(), operation.PreviewID)
	})

	outcome := controller.Deploy(ctx, DeployRequest{Operation: operation, Content: content}).Outcome
	if outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("deploy outcome = %+v", outcome)
	}
	if len(outcome.Checks) != 1 || outcome.Checks[0].HTTPStatus != 200 {
		t.Fatalf("checks = %+v", outcome.Checks)
	}
	prefix := ResourcePrefix("runner-real", operation.PreviewID)
	port, err := cli.HostPort(ctx, prefix+"-gateway", gatewayIngressPort)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + "/index.html")
	if err != nil {
		t.Fatalf("app is not reachable through the gateway: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || string(body) != "preview ok\n" {
		t.Fatalf("gateway ingress = %d %q", response.StatusCode, body)
	}
	if _, err := cli.HostPort(ctx, prefix+"-app", 8080); err == nil {
		t.Fatal("the app container publishes a host port")
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
	if output, err := probeFromPreviewNetwork(ctx, cli, prefix, operation.PreviewID, image, "control",
		"wget", "-q", "-T", "5", "-O", "-", "http://app:8080/index.html"); err != nil || output != "preview ok\n" {
		t.Fatalf("positive control: the probe could not reach the app: %q %v", output, err)
	}
	targets := map[string]string{
		"public":         "http://1.1.1.1/",
		"metadata":       "http://169.254.169.254/latest/meta-data/",
		"edge gateway":   "http://" + net.JoinHostPort(networkGateway(t, ctx, prefix+"-edge"), hostPort) + "/",
		"bridge gateway": "http://" + net.JoinHostPort(networkGateway(t, ctx, "bridge"), hostPort) + "/",
	}
	if own, found := optionalNetworkGateway(t, ctx, prefix); found {
		targets["preview gateway"] = "http://" + net.JoinHostPort(own, hostPort) + "/"
	}
	for name, target := range targets {
		output, err := probeFromPreviewNetwork(ctx, cli, prefix, operation.PreviewID, image, strings.ReplaceAll(name, " ", "-"),
			"wget", "-q", "-T", "5", "-O", "-", target)
		if err == nil || strings.Contains(output, "host listener") {
			t.Fatalf("%s %s reachable from the preview network: %q", name, target, output)
		}
	}
	if output, err := probeFromPreviewNetwork(ctx, cli, prefix, operation.PreviewID, image, "dns-public", "nslookup", "example.com"); err == nil {
		t.Fatalf("example.com resolved from the preview network: %q", output)
	}
	routes, err := probeFromPreviewNetwork(ctx, cli, prefix, operation.PreviewID, image, "ipv6-route", "ip", "-6", "route", "show")
	if err != nil && !strings.Contains(routes, "not supported") {
		t.Fatalf("ipv6 route probe failed: %q %v", routes, err)
	}
	if routable := routableIPv6Routes(routes); len(routable) > 0 {
		t.Fatalf("the preview network carries routable IPv6 routes: %q", routable)
	}
	destroy := v1.PreviewLifecycleOperation{PreviewID: operation.PreviewID}
	destroy.Action = v1.PreviewLifecycleDestroy
	result := controller.ApplyLifecycle(ctx, LifecycleRequest{Operation: destroy}).Outcome
	if result.Kind != v1.PreviewLifecycleSucceeded || result.ResourceState == nil ||
		result.ResourceState.Disposition != v1.PreviewLifecycleDestroyed {
		t.Fatalf("destroy outcome = %+v", result)
	}
	left, err := DockerCLI{}.List(ctx, PreviewLabels("runner-real", operation.PreviewID))
	if err != nil || !left.Empty() {
		t.Fatalf("resources left after destroy: %+v %v", left, err)
	}
	if _, err := os.Lstat(controller.previewDir(operation.PreviewID)); !os.IsNotExist(err) {
		t.Fatalf("preview state dir survived destroy: %v", err)
	}

	allow := operation
	allow.PreviewID = "preview-real-allow"
	t.Cleanup(func() {
		_ = controller.teardownDetached(context.Background(), allow.PreviewID)
	})
	content.Egress = v1.PreviewBlueprintEgressV1{Mode: v1.PreviewBlueprintEgressAllow, Hosts: []string{"example.com"}}
	if outcome := controller.Deploy(ctx, DeployRequest{Operation: allow, Content: content}).Outcome; outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("allow deploy outcome = %+v", outcome)
	}
	allowPrefix := ResourcePrefix("runner-real", allow.PreviewID)
	connect := func(name, host string) string {
		request := "CONNECT " + host + ":443 HTTP/1.1\r\nHost: " + host + ":443\r\n\r\n"
		output, _ := probeFromPreviewNetwork(ctx, cli, allowPrefix, allow.PreviewID, image, name,
			"sh", "-c", `(printf "$0"; sleep 5) | nc -w 10 our-gateway 3128 | head -n 1`, request)
		return output
	}
	if status := connect("allowed", "example.com"); !strings.Contains(status, " 200") {
		t.Fatalf("CONNECT to an allowlisted host = %q", status)
	}
	if status := connect("refused", "example.org"); !strings.Contains(status, " 403") {
		t.Fatalf("CONNECT to a host that is not allowlisted = %q", status)
	}
	output, err := probeFromPreviewNetwork(ctx, cli, allowPrefix, allow.PreviewID, image, "direct",
		"wget", "-q", "-T", "5", "-O", "-", "http://example.com/")
	if err == nil {
		t.Fatalf("an allowlisted host was reachable without the proxy: %q", output)
	}
}
