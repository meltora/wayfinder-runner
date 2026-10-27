package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

func TestTrustedResultRepositoryHasTheRunnerConfig(t *testing.T) {
	f := newAgentResultTest(t)
	trusted := f.repository().Trusted
	for _, setting := range trustedRepositoryConfig {
		if got := trustedGitForTest(t, trusted.gitDir, "config", "--local", "--get", setting[0]); got != setting[1] {
			t.Fatalf("%s = %q, want %q", setting[0], got, setting[1])
		}
	}
	exclude, err := os.ReadFile(filepath.Join(trusted.gitDir, "info", "exclude"))
	if err != nil || !strings.Contains(string(exclude), runnerArtifactsExcludeEntry) {
		t.Fatalf("trusted exclude = %q err=%v", exclude, err)
	}
	if got := f.trustedGit(t, "rev-parse", "--is-bare-repository"); got != "false" {
		t.Fatalf("trusted repository bare = %s", got)
	}
	if got := f.trustedGit(t, "rev-parse", f.base+"^{commit}"); got != f.base {
		t.Fatalf("trusted repository start commit = %s", got)
	}
	for _, name := range []string{"HOME=", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "GIT_ATTR_NOSYSTEM=1"} {
		found := false
		for _, entry := range trustedGitEnvironment(trusted.root) {
			if strings.HasPrefix(entry, name) {
				found = true
			}
		}
		if !found {
			t.Fatalf("trusted Git environment lacks %s", name)
		}
	}
}

func TestTrustedResultRepositoryIsOutsideTheJobWorkspace(t *testing.T) {
	f := newAgentResultTest(t)
	trusted := f.repository().Trusted
	relative, err := filepath.Rel(f.jr.workDir, trusted.root)
	if err != nil || relative == "." || !strings.HasPrefix(relative, "..") {
		t.Fatalf("trusted repository %s is inside the job workspace %s", trusted.root, f.jr.workDir)
	}
	if !strings.HasPrefix(trusted.root, filepath.Join(f.jr.d.cfg.StateDir, trustedResultsDirectory)+string(filepath.Separator)) {
		t.Fatalf("trusted repository %s is not under the state directory", trusted.root)
	}
}

func TestTrustedResultRepositoryIsRemovedByCleanupRetirementAndSweep(t *testing.T) {
	t.Run("workspace cleanup", func(t *testing.T) {
		f := newAgentResultTest(t)
		root := f.repository().Trusted.root
		f.cleanup()
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("trusted repository survived cleanup: %v", err)
		}
	})
	t.Run("retirement", func(t *testing.T) {
		f := newAgentResultTest(t)
		root := f.repository().Trusted.root
		f.jr.retireCompletedJob()
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("trusted repository survived retirement: %v", err)
		}
	})
	t.Run("orphan sweep", func(t *testing.T) {
		stateDir := t.TempDir()
		orphan := filepath.Join(stateDir, trustedResultsDirectory, "job-old", "repo-1", "repo.git")
		keep := filepath.Join(stateDir, trustedResultsDirectory, "keep")
		for _, dir := range []string{orphan, keep} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := cleanupOrphanedJobState(context.Background(), stateDir, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(stateDir, trustedResultsDirectory, "job-old")); !os.IsNotExist(err) {
			t.Fatalf("orphaned trusted repository survived the sweep: %v", err)
		}
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("unrelated directory removed: %v", err)
		}
	})
}

func TestTrustedResultRepositoryPreparationFailureRemovesTheWorkspace(t *testing.T) {
	jr, _, _ := pinnedBuildWorkspaceFixture(t)
	if err := os.WriteFile(filepath.Join(jr.d.cfg.StateDir, trustedResultsDirectory), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	t.Cleanup(cleanup)
	if err == nil || workDir != "" || !strings.Contains(err.Error(), "trusted result repository") {
		t.Fatalf("preparation with an unusable trusted location: directory=%q err=%v", workDir, err)
	}
	if _, err := os.Stat(filepath.Join(jr.workDir, "repositories", "repo-1")); !os.IsNotExist(err) {
		t.Fatalf("workspace survived trusted repository failure: %v", err)
	}
}

func TestTrustedResultRepositoryRejectsAStartCommitItDoesNotHave(t *testing.T) {
	f := newAgentResultTest(t)
	dest := filepath.Join(t.TempDir(), "trusted")
	agentGit(t, f.checkout, "clone", "-q", "--bare", "--local", f.checkout, filepath.Join(dest, "repo.git"))
	if _, err := configureTrustedResultRepository(context.Background(), dest, strings.Repeat("e", 40), time.Unix(1700000000, 0)); err == nil {
		t.Fatal("trusted repository accepted a missing start commit")
	}
	if _, err := configureTrustedResultRepository(context.Background(), dest, "main", time.Unix(1700000000, 0)); err == nil {
		t.Fatal("trusted repository accepted a mutable start revision")
	}
}

func TestPlanJobsGetNoTrustedResultRepository(t *testing.T) {
	jr, _, _ := pinnedBuildWorkspaceFixture(t)
	jr.job.Kind = v1.JobKindPlan
	_, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if jr.repositories[0].Trusted != nil {
		t.Fatal("plan job received a trusted result repository")
	}
	if _, err := os.Stat(trustedResultsRoot(jr.d.cfg.StateDir, jr.workDir)); !os.IsNotExist(err) {
		t.Fatalf("plan job created trusted result state: %v", err)
	}
}

func TestMissingTrustedRepositoryRefusesEveryResultUse(t *testing.T) {
	f := newAgentResultTest(t)
	f.jr.repositories[0].Trusted = nil
	f.jr.job.RequiredChecks = []string{"unit-check"}
	f.jr.d.cfg.Checks = map[string]config.CheckConfig{"unit-check": {Command: "true"}}
	var resultErr *agentResultError
	if _, err := f.jr.previewCommitSet(context.Background()); !errors.As(err, &resultErr) {
		t.Fatalf("preview without a trusted repository: %v", err)
	}
	if _, err := call(t, f.jr, "platform_run_check", `{"check_id":"unit-check","idempotency_key":"rc-untrusted"}`); err == nil || !strings.Contains(err.Error(), "trusted result repository") {
		t.Fatalf("run-check without a trusted repository: %v", err)
	}
	if _, err := f.jr.snapshotRepository(context.Background(), f.repository()); !errors.As(err, &resultErr) {
		t.Fatalf("pause without a trusted repository: %v", err)
	}
	if err := f.finalizeWithPush(t); !errors.As(err, &resultErr) {
		t.Fatalf("build without a trusted repository: %v", err)
	}
	if originHasBranch(t, f.origin, f.branch) {
		t.Fatal("build without a trusted repository pushed a branch")
	}
}
