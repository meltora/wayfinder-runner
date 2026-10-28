package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/isolation"
)

func TestPlanCheckoutCarryForwardOnARootlessDaemon(t *testing.T) {
	if os.Getenv("RUN_DOCKER_SANDBOX_PROBE") != "1" {
		t.Skip("set RUN_DOCKER_SANDBOX_PROBE=1 for the container integration gate")
	}
	image := os.Getenv("JOB_IMAGE")
	if image == "" {
		t.Fatal("JOB_IMAGE is required")
	}
	const runnerID = "rpcr-plan-checkout-test"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	mapping, err := isolation.ResolveUserMapping(ctx, image, runnerID)
	if err != nil {
		t.Fatal(err)
	}
	if !mapping.RequiresOwnershipHandOff() {
		t.Skip("the configured Docker daemon is not rootless")
	}
	stateDir, err := os.MkdirTemp("", "rpcr-state-")
	if err != nil {
		t.Fatal(err)
	}
	if stateDir, err = filepath.EvalSymlinks(stateDir); err != nil {
		t.Fatal(err)
	}
	ownership := isolation.DockerOwnership{Mapping: mapping, Image: image, StateDir: stateDir}
	workspace := filepath.Join(stateDir, "jobs", "job-1")
	checkout := filepath.Join(workspace, "repositories", "repo-1")
	request := isolation.OwnershipRequest{RunnerID: runnerID, AttemptID: "rpcr-plan", Purpose: "job", Workspace: []string{workspace}}
	t.Cleanup(func() {
		_ = isolation.RemoveJobContainer(context.Background(), runnerID, "rpcr-read")
		_ = isolation.RemoveJobContainer(context.Background(), runnerID, "rpcr-write")
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
	writeFixtureFile(t, checkout, "README.md", "observed\n", 0o644)
	writeFixtureFile(t, checkout, "private.txt", "private\n", 0o600)
	writeFixtureFile(t, checkout, "src/main.go", "package main\n", 0o644)
	buildWorkspaceGit(t, checkout, "add", "-A")
	buildWorkspaceGit(t, checkout, "commit", "-m", "fixture")
	head := buildWorkspaceGit(t, checkout, "rev-parse", "HEAD")
	buildWorkspaceGit(t, checkout, "checkout", "--detach", "-q", head)
	prepared, err := planCheckoutBaseline(checkout)
	if err != nil {
		t.Fatal(err)
	}

	jr, captured := testJobRunner(t)
	jr.workDir = workspace
	jr.job.Kind = v1.JobKindPlan
	jr.job.Repositories = []v1.JobRepository{{
		RepositoryID: "repo-1", RepositoryHeadObservationID: "head-observation-1", BaseBranch: "main", BaseSHA: head, Primary: true,
	}}
	jr.verifiedPlanRepositories = append([]v1.JobRepository(nil), jr.job.Repositories...)
	jr.repositories = []checkedOutRepository{{
		ID: "repo-1", Base: "main", WorkDir: checkout, Primary: true, PlanBaseline: newPlanCheckoutEvidence(prepared),
	}}

	if err := ownership.HandOff(ctx, request); err != nil {
		t.Fatal(err)
	}
	if owner := fileOwner(t, filepath.Join(checkout, "README.md")); owner != mapping.HostUID {
		t.Fatalf("README.md owner = %d, want %d", owner, mapping.HostUID)
	}
	if content, err := os.ReadFile(filepath.Join(checkout, ".git", "HEAD")); err != nil || strings.TrimSpace(string(content)) != head {
		t.Fatalf("host read of .git/HEAD = %q, %v", content, err)
	}
	handedOff, err := planCheckoutBaseline(checkout)
	if err != nil {
		t.Fatalf("host walk after hand-off: %v", err)
	}
	if handedOff.Strict == prepared.Strict {
		t.Fatal("the hand-off left the strict digest unchanged; F-3 was not reproduced")
	}
	if handedOff.Identity != prepared.Identity {
		t.Fatal("the hand-off changed the checkout identity")
	}
	if cause := planCheckoutMismatchCause(jr.repositories[0], head); cause != "baseline_mismatch" {
		t.Fatalf("prepare-time verification cause = %q", cause)
	}
	if err := jr.carryPlanCheckoutForward(); err != nil {
		t.Fatal(err)
	}
	if _, err := jr.verifyPlanRepositoryAuthority(ctx); err != nil {
		t.Fatalf("verification after carry-forward: %v", err)
	}

	policy := isolation.Policy{
		User: mapping.User(), NetworkNone: true, ReadOnlyFS: true, ReadOnlyWorkDir: true,
		WorkDir: checkout, WorkspaceRoot: workspace, Image: image, RunnerID: runnerID, AttemptID: "rpcr-read",
	}
	if output := runPlanCheckoutContainer(t, policy, "git rev-parse HEAD"); strings.TrimSpace(output) != head {
		t.Fatalf("job-user rev-parse = %q", output)
	}
	if _, err := jr.verifyPlanRepositoryAuthority(ctx); err != nil {
		t.Fatalf("verification after a read-only job read: %v", err)
	}

	policy.ReadOnlyWorkDir, policy.AttemptID = false, "rpcr-write"
	runPlanCheckoutContainer(t, policy, "echo appended >> README.md")
	if _, err := jr.verifyPlanRepositoryAuthority(ctx); err == nil || err.Error() != "plan repository checkout mismatch for repo-1" {
		t.Fatalf("verification after a job write = %v", err)
	}
	if events := captured.byKind("plan_published"); len(events) != 0 {
		t.Fatalf("plan events = %d", len(events))
	}

	if err := ownership.Reclaim(ctx, request); err != nil {
		t.Fatal(err)
	}
	if owner := fileOwner(t, filepath.Join(checkout, "README.md")); owner != os.Getuid() {
		t.Fatalf("README.md owner after reclaim = %d", owner)
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

func runPlanCheckoutContainer(t *testing.T, policy isolation.Policy, script string) string {
	t.Helper()
	command, args, err := isolation.Docker{}.Wrap("sh", []string{"-c", script}, policy)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := exec.CommandContext(ctx, command, args...)
	run.Env = isolation.DockerEnvironment()
	output, runErr := run.CombinedOutput()
	if err := isolation.RemoveJobContainer(context.Background(), policy.RunnerID, policy.AttemptID); err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("container failed: %v\n%s", runErr, output)
	}
	return string(output)
}

func fileOwner(t *testing.T, path string) int {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return int(info.Sys().(*syscall.Stat_t).Uid)
}
