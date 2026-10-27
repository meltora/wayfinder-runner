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

	"github.com/example/app-runner/internal/isolation"
)

func TestRecoveryReclaimsJobOwnedLeftoversOnARootlessDaemon(t *testing.T) {
	if os.Getenv("RUN_DOCKER_SANDBOX_PROBE") != "1" {
		t.Skip("set RUN_DOCKER_SANDBOX_PROBE=1 for the container integration gate")
	}
	image := os.Getenv("JOB_IMAGE")
	if image == "" {
		t.Fatal("JOB_IMAGE is required")
	}
	const runnerID = "rdju-recovery-test"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	mapping, err := isolation.ResolveUserMapping(ctx, image, runnerID)
	if err != nil {
		t.Fatal(err)
	}
	if !mapping.RequiresOwnershipHandOff() {
		t.Skip("the configured Docker daemon is not rootless")
	}
	stateDir, err := os.MkdirTemp("", "rdju-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	stateDir, err = filepath.EvalSymlinks(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	ownership := isolation.DockerOwnership{Mapping: mapping, Image: image, StateDir: stateDir}
	workspace := []string{
		filepath.Join(stateDir, "jobs", "job-1"),
		filepath.Join(stateDir, trustedResultsDirectory, "job-1.checks"),
	}
	private := []string{
		filepath.Join(stateDir, "job-homes", "home-1"),
		filepath.Join(stateDir, "sockets", "job-1"),
		filepath.Join(stateDir, "check-homes", "home-1"),
	}
	roots := append(append([]string(nil), workspace...), private...)
	t.Cleanup(func() {
		_ = ownership.Reclaim(context.Background(), isolation.OwnershipRequest{
			RunnerID: runnerID, AttemptID: "cleanup", Purpose: "cleanup", Private: existingRoots(roots),
		})
		_ = removeRunnerTree(stateDir)
	})
	for _, root := range roots {
		if err := os.MkdirAll(filepath.Join(root, "nested"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "nested", "file"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ownership.HandOff(ctx, isolation.OwnershipRequest{
		RunnerID: runnerID, AttemptID: "crash", Purpose: "job", Workspace: workspace, Private: private,
	}); err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		info, err := os.Lstat(root)
		if err != nil {
			t.Fatal(err)
		}
		if owner := info.Sys().(*syscall.Stat_t).Uid; int(owner) != mapping.HostUID {
			t.Fatalf("%s owner = %d, want %d", root, owner, mapping.HostUID)
		}
	}

	err = cleanupOrphanedJobState(ctx, stateDir, nil)
	if err == nil || !strings.Contains(err.Error(), "the runner could not re-own job output") || !strings.Contains(err.Error(), roots[0]) {
		t.Fatalf("cleanup without reclaim = %v", err)
	}
	if err := cleanupOrphanedJobState(ctx, stateDir, ownershipReclaimer(ownership, runnerID)); err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		if _, err := os.Lstat(root); !os.IsNotExist(err) {
			t.Fatalf("%s survived recovery: %v", root, err)
		}
	}
	command := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=com.ourplatform.runner-id="+runnerID)
	command.Env = isolation.DockerEnvironment()
	output, err := command.Output()
	if err != nil || strings.TrimSpace(string(output)) != "" {
		t.Fatalf("runner containers left: %q err=%v", output, err)
	}
}

func existingRoots(roots []string) []string {
	var existing []string
	for _, root := range roots {
		if _, err := os.Lstat(root); err == nil {
			existing = append(existing, root)
		}
	}
	return existing
}
