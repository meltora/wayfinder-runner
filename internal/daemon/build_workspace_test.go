package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

func buildWorkspaceGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func pinnedBuildWorkspaceFixture(t *testing.T) (*jobRunner, string, string) {
	t.Helper()
	origin := t.TempDir()
	buildWorkspaceGit(t, origin, "init", "-b", "main")
	buildWorkspaceGit(t, origin, "config", "user.email", "runner@example.test")
	buildWorkspaceGit(t, origin, "config", "user.name", "Runner Test")
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("approved base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	buildWorkspaceGit(t, origin, "add", ".")
	buildWorkspaceGit(t, origin, "commit", "-m", "approved base")
	authorizedSHA := buildWorkspaceGit(t, origin, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(origin, "advanced-only.txt"), []byte("outside approved snapshot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	buildWorkspaceGit(t, origin, "add", ".")
	buildWorkspaceGit(t, origin, "commit", "-m", "branch advanced after authorization")
	jr, _ := testJobRunner(t)
	jr.d.cfg.AllowUnsafeDirect = true
	jr.d.cfg.Isolation = "direct"
	jr.d.cfg.Repositories = map[string]config.RepositoryConfig{"repo-1": {URL: origin}}
	jr.job.Repositories = []v1.JobRepository{{
		RepositoryID: "repo-1", BaseBranch: "refs/heads/main", BaseSHA: authorizedSHA, Primary: true,
	}}
	return jr, origin, authorizedSHA
}

func TestBuildWorkspacePinsAuthorizedCommitAfterBranchMoves(t *testing.T) {
	jr, origin, authorizedSHA := pinnedBuildWorkspaceFixture(t)
	originSHA := buildWorkspaceGit(t, origin, "rev-parse", "HEAD")
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if got := buildWorkspaceGit(t, workDir, "rev-parse", "HEAD"); got != authorizedSHA {
		t.Fatalf("build checkout = %s, want authorized %s", got, authorizedSHA)
	}
	if got := buildWorkspaceGit(t, workDir, "branch", "--show-current"); got != "job/j1/a1" {
		t.Fatalf("build branch = %q", got)
	}
	if got := jr.repositories[0].Base; got != "refs/heads/main" {
		t.Fatalf("discovery branch metadata = %q", got)
	}
	if _, err := os.Stat(filepath.Join(workDir, "advanced-only.txt")); !os.IsNotExist(err) {
		t.Fatalf("unauthorized branch content present: %v", err)
	}
	if got := buildWorkspaceGit(t, origin, "rev-parse", "HEAD"); got != originSHA {
		t.Fatalf("origin changed: %s", got)
	}
	cleanup()
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Fatalf("workspace survived cleanup: %v", err)
	}
}

func TestBuildWorkspaceRejectsUnavailableAuthorizedCommit(t *testing.T) {
	for _, sha := range []string{strings.Repeat("f", 40), "", "main"} {
		t.Run("sha-"+sha, func(t *testing.T) {
			jr, _, _ := pinnedBuildWorkspaceFixture(t)
			jr.job.Repositories[0].BaseSHA = sha
			workDir, cleanup, err := jr.prepareWorkspace(context.Background())
			t.Cleanup(cleanup)
			if err == nil || workDir != "" {
				t.Fatalf("invalid or unavailable authorized SHA accepted: directory=%q err=%v", workDir, err)
			}
			if _, err := os.Stat(filepath.Join(jr.workDir, "repositories", "repo-1")); !os.IsNotExist(err) {
				t.Fatalf("failed workspace survived: %v", err)
			}
		})
	}
}

func TestBuildPatchUsesExactAuthorizedBaseAfterBranchRefDisappears(t *testing.T) {
	jr, _, authorizedSHA := pinnedBuildWorkspaceFixture(t)
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if err := os.WriteFile(filepath.Join(workDir, "implemented.txt"), []byte("approved change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	headSHA, err := jr.commitAgentChanges(context.Background(), jr.repositories[0])
	if err != nil {
		t.Fatal(err)
	}
	if parent := trustedGitForTest(t, jr.repositories[0].Trusted.gitDir, "rev-parse", headSHA+"^"); parent != authorizedSHA {
		t.Fatalf("implementation parent = %s, want %s", parent, authorizedSHA)
	}
	buildWorkspaceGit(t, workDir, "update-ref", "-d", "refs/heads/main")
	if err := os.WriteFile(filepath.Join(workDir, "after-checks.txt"), []byte("not in captured result\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := jr.commitAgentChanges(context.Background(), jr.repositories[0]); err != nil {
		t.Fatal(err)
	}
	if err := jr.collectPatch(context.Background(), jr.repositories[0], headSHA); err != nil {
		t.Fatal(err)
	}
	patch, err := os.ReadFile(filepath.Join(jr.workDir, "changes-repo-1-"+headSHA[:8]+".patch"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "+approved change") || strings.Contains(string(patch), "advanced-only.txt") || strings.Contains(string(patch), "after-checks.txt") {
		t.Fatalf("patch contains incorrect change set: %s", patch)
	}
}

func TestBuildWorkspaceCleansUpIfJobBranchIsInvalid(t *testing.T) {
	jr, _, _ := pinnedBuildWorkspaceFixture(t)
	jr.job.JobID = "invalid..branch"
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	t.Cleanup(cleanup)
	if err == nil || workDir != "" {
		t.Fatalf("invalid job branch accepted: directory=%q err=%v", workDir, err)
	}
	if _, err := os.Stat(filepath.Join(jr.workDir, "repositories", "repo-1")); !os.IsNotExist(err) {
		t.Fatalf("detached workspace survived branch failure: %v", err)
	}
}

func TestBuildPatchRejectsMutableOrMissingCommitEndpoints(t *testing.T) {
	for _, test := range []struct{ base, head string }{
		{"", strings.Repeat("a", 40)},
		{"main", strings.Repeat("a", 40)},
		{strings.Repeat("a", 40), "HEAD"},
	} {
		jr, _ := testJobRunner(t)
		if err := jr.collectPatch(context.Background(), checkedOutRepository{Base: "main", BaseSHA: test.base}, test.head); err == nil {
			t.Fatalf("non-immutable patch endpoints accepted: %+v", test)
		}
	}
}

func TestBuildWorkspaceRemovesPreparedPrimaryWhenSecondaryIsRejected(t *testing.T) {
	for _, test := range []struct{ name, repositoryID, baseSHA string }{
		{"unavailable authorized commit", "repo-2", strings.Repeat("f", 40)},
		{"unregistered repository", "not-registered", strings.Repeat("f", 40)},
		{"duplicate repository", "repo-1", strings.Repeat("f", 40)},
		{"invalid repository identity", "invalid/id", strings.Repeat("f", 40)},
	} {
		t.Run(test.name, func(t *testing.T) {
			jr, origin, _ := pinnedBuildWorkspaceFixture(t)
			jr.d.cfg.Repositories = map[string]config.RepositoryConfig{
				"repo-1": {URL: origin}, "repo-2": {URL: origin},
			}
			jr.job.Repositories = append(jr.job.Repositories, v1.JobRepository{
				RepositoryID: test.repositoryID, BaseBranch: "main", BaseSHA: test.baseSHA,
			})
			workDir, cleanup, err := jr.prepareWorkspace(context.Background())
			t.Cleanup(cleanup)
			if err == nil || workDir != "" {
				t.Fatalf("rejected secondary allowed partial Build: directory=%q err=%v", workDir, err)
			}
			if len(jr.repositories) != 1 || jr.repositories[0].ID != "repo-1" {
				t.Fatalf("test did not reach rejection after preparing primary: %+v", jr.repositories)
			}
			for _, repositoryID := range []string{"repo-1", "repo-2"} {
				if _, err := os.Stat(filepath.Join(jr.workDir, "repositories", repositoryID)); !os.IsNotExist(err) {
					t.Fatalf("partial workspace %s survived rejection: %v", repositoryID, err)
				}
			}
		})
	}
}

func TestBuildWorkspaceDefaultBranchMetadataDoesNotReplaceAuthorizedCommit(t *testing.T) {
	jr, _, authorizedSHA := pinnedBuildWorkspaceFixture(t)
	jr.job.Repositories = []v1.JobRepository{{RepositoryID: "repo-1", BaseSHA: authorizedSHA, Primary: true}}
	workDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if got := buildWorkspaceGit(t, workDir, "rev-parse", "HEAD"); got != authorizedSHA {
		t.Fatalf("default branch replaced authorized snapshot: got %s, want %s", got, authorizedSHA)
	}
	if jr.repositories[0].Base != "main" || jr.repositories[0].BaseSHA != authorizedSHA {
		t.Fatalf("default branch and immutable base lost: %+v", jr.repositories[0])
	}
}
