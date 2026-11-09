package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/isolation"
)

func TestIterativeSnapshotSkipsHarnessStateDuringARootlessHandOff(t *testing.T) {
	if os.Getenv("RUN_DOCKER_SANDBOX_PROBE") != "1" {
		t.Skip("set RUN_DOCKER_SANDBOX_PROBE=1 for the container integration gate")
	}
	image := os.Getenv("JOB_IMAGE")
	if image == "" {
		t.Fatal("JOB_IMAGE is required")
	}
	const runnerID = "rdbc-harness-test"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	mapping, err := isolation.ResolveUserMapping(ctx, image, runnerID)
	if err != nil {
		t.Fatal(err)
	}
	if !mapping.RequiresOwnershipHandOff() {
		t.Fatalf("mapping %+v is not rootless; set DOCKER_HOST to the rootless daemon", mapping)
	}
	stateDir, err := os.MkdirTemp("", "rdbc-state-")
	if err != nil {
		t.Fatal(err)
	}
	if stateDir, err = filepath.EvalSymlinks(stateDir); err != nil {
		t.Fatal(err)
	}
	ownership := isolation.DockerOwnership{Mapping: mapping, Image: image, StateDir: stateDir}
	workspace := filepath.Join(stateDir, "jobs", "job-1")
	checkout := filepath.Join(workspace, "repositories", "repo-1")
	request := isolation.OwnershipRequest{RunnerID: runnerID, AttemptID: "rdbc-harness", Purpose: "job", Workspace: []string{workspace}}
	t.Cleanup(func() {
		_ = isolation.RemoveJobContainer(context.Background(), runnerID, "rdbc-write")
		_ = ownership.Reclaim(context.Background(), isolation.OwnershipRequest{
			RunnerID: runnerID, AttemptID: "cleanup", Purpose: "cleanup", Workspace: existingRoots([]string{workspace}),
		})
		_ = removeRunnerTree(stateDir)
	})
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	buildWorkspaceGit(t, checkout, "init", "-b", "main")
	buildWorkspaceGit(t, checkout, "config", "user.email", "runner@example.test")
	buildWorkspaceGit(t, checkout, "config", "user.name", "Runner Test")
	writeFixtureFile(t, checkout, "README.md", "base\n", 0o644)
	buildWorkspaceGit(t, checkout, "add", "-A")
	buildWorkspaceGit(t, checkout, "commit", "-m", "fixture")

	jr, _ := testJobRunner(t)
	jr.workDir = workspace
	jr.job.Kind = v1.JobKindBuild
	jr.job.Ticket = v1.TicketContext{Key: "KAN-1", Summary: "Harness state"}
	jr.repositories = []checkedOutRepository{{ID: "repo-1", Base: "main", WorkDir: checkout, Primary: true}}
	attachTrustedRepositories(t, jr)

	if err := ownership.HandOff(ctx, request); err != nil {
		t.Fatal(err)
	}
	policy := isolation.Policy{
		User: mapping.User(), NetworkNone: true, ReadOnlyFS: true,
		WorkDir: checkout, WorkspaceRoot: workspace, Image: image, RunnerID: runnerID, AttemptID: "rdbc-write",
	}
	runPlanCheckoutContainer(t, policy, "mkdir -p .claude && mkdir -m 0700 .claude/.cc-writes && echo staged > .claude/.cc-writes/settings.json.tmp && chmod 0600 .claude/.cc-writes/settings.json.tmp && echo feature > feature.txt")
	if owner := fileOwner(t, filepath.Join(checkout, ".claude", ".cc-writes")); owner == os.Getuid() {
		t.Fatalf("the staging directory is owned by the runner user %d", owner)
	}

	result, err := jr.resolveAgentResult(ctx, jr.repositories[0], "agent check go-test")
	if err != nil {
		t.Fatalf("iterative snapshot with only harness state present: %v", err)
	}
	paths := strings.Split(trustedGitForTest(t, jr.repositories[0].Trusted.gitDir, "ls-tree", "-r", "--name-only", result.commit), "\n")
	t.Logf("iterative snapshot paths: %v", paths)
	assertNoHarnessEntry(t, paths)
	if !slices.Contains(paths, "feature.txt") {
		t.Fatalf("snapshot paths = %v", paths)
	}

	runPlanCheckoutContainer(t, policy, "mkdir -m 0700 other && echo private > other/inner.txt")
	_, err = jr.resolveAgentResult(ctx, jr.repositories[0], "agent check go-test")
	t.Logf("iterative snapshot with other/ present: %v", err)
	if err == nil || !strings.Contains(err.Error(), "has an entry the runner cannot read at other") {
		t.Fatalf("snapshot with a private directory = %v", err)
	}
	assertNoHostPath(t, err.Error(), stateDir, workspace)

	if err := ownership.Reclaim(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := removeRunnerTree(workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace survived removal: %v", err)
	}
	command := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=com.ourplatform.runner-id="+runnerID)
	command.Env = isolation.DockerEnvironment()
	output, err := command.Output()
	if err != nil || strings.TrimSpace(string(output)) != "" {
		t.Fatalf("runner containers left: %q err=%v", output, err)
	}
}
