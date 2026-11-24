package previewhost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

func TestRealDockerDeployAndDestroy(t *testing.T) {
	if os.Getenv("RUN_PREVIEW_HOST_DOCKER") != "1" {
		t.Skip("set RUN_PREVIEW_HOST_DOCKER=1 to run against a real Docker daemon")
	}
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
	controller, err := New(Config{
		RunnerID: "runner-real", StateDir: state, Lease: time.Hour,
		HealthInterval: 200 * time.Millisecond, TeardownTimeout: time.Minute,
		JobUID: os.Getuid(), JobGID: os.Getgid(),
	}, Dependencies{
		Engine: DockerCLI{TempDir: filepath.Join(state, "tmp")}, Workspaces: manager,
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
}
