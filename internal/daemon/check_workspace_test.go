package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/example/app-runner/internal/config"
)

func (f agentResultTest) runCheck(t *testing.T, check config.CheckConfig) map[string]any {
	t.Helper()
	result, err := f.runCheckTry(t, check)
	if err != nil {
		t.Fatalf("run-check: %v", err)
	}
	return result
}

func (f agentResultTest) runCheckTry(t *testing.T, check config.CheckConfig) (map[string]any, error) {
	t.Helper()
	checkID := fmt.Sprintf("check-%d", len(f.jr.job.RequiredChecks))
	if f.jr.d.cfg.Checks == nil {
		f.jr.d.cfg.Checks = map[string]config.CheckConfig{}
	}
	f.jr.d.cfg.Checks[checkID] = check
	f.jr.job.RequiredChecks = append(f.jr.job.RequiredChecks, checkID)
	response, err := call(t, f.jr, "platform_run_check", fmt.Sprintf(`{"check_id":%q,"idempotency_key":"key-%s"}`, checkID, checkID))
	if err != nil {
		return nil, err
	}
	return response.(map[string]any), nil
}

func shellCheck(script string) config.CheckConfig {
	return config.CheckConfig{Command: "/bin/sh", Args: []string{"-c", script}}
}

func assertCheckResult(t *testing.T, result map[string]any, want string) {
	t.Helper()
	if result["result"] != want {
		t.Fatalf("check result = %v, want %s", result, want)
	}
}

func TestChecksRunAgainstThePublishedTree(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, f agentResultTest)
		script  string
		want    string
	}{
		{"an ignored file that is not a dependency is absent", func(t *testing.T, f agentResultTest) {
			writeAgentFile(t, f.checkout, ".gitignore", "secret.env\n")
			writeAgentFile(t, f.checkout, "secret.env", "TOKEN=x\n")
		}, "test -f secret.env", "failed"},
		{"a published change is present", func(t *testing.T, f agentResultTest) {
			writeAgentFile(t, f.checkout, "feature.txt", "real\n")
		}, "grep -q real feature.txt", "passed"},
		{"the dependency directory is carried", func(t *testing.T, f agentResultTest) {
			writeAgentFile(t, f.checkout, ".gitignore", "node_modules/\n")
			writeAgentFile(t, f.checkout, "node_modules/pkg/index.js", "original\n")
		}, "grep -q original node_modules/pkg/index.js", "passed"},
		{"a nested dependency directory is carried", func(t *testing.T, f agentResultTest) {
			writeAgentFile(t, f.checkout, ".gitignore", "node_modules/\n")
			writeAgentFile(t, f.checkout, "packages/a/node_modules/pkg/index.js", "nested\n")
		}, "grep -q nested packages/a/node_modules/pkg/index.js", "passed"},
		{"a build cache is absent", func(t *testing.T, f agentResultTest) {
			writeAgentFile(t, f.checkout, ".gitignore", ".next/\nnode_modules/\n")
			writeAgentFile(t, f.checkout, ".next/cache/data", "stale\n")
		}, "test ! -e .next", "passed"},
		{"the agent's Git metadata is absent", func(t *testing.T, f agentResultTest) {}, "test ! -e .git", "passed"},
		{"a published symbolic link is present", func(t *testing.T, f agentResultTest) {
			symlinkAgentPath(t, f, "README.md", "link")
		}, "test -L link", "passed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentResultTest(t)
			tc.prepare(t, f)
			assertCheckResult(t, f.runCheck(t, shellCheck(tc.script)), tc.want)
		})
	}
}

func TestCheckDependencyDirectoriesFollowTheCheckConfiguration(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, ".gitignore", "node_modules/\n.venv/\n")
	writeAgentFile(t, f.checkout, "node_modules/pkg/index.js", "js\n")
	writeAgentFile(t, f.checkout, ".venv/lib/site.py", "py\n")

	none := shellCheck("test ! -e node_modules && test ! -e .venv")
	none.DependencyDirectories = []string{}
	assertCheckResult(t, f.runCheck(t, none), "passed")

	both := shellCheck("test -f node_modules/pkg/index.js && test -f .venv/lib/site.py")
	both.DependencyDirectories = []string{"node_modules", ".venv"}
	assertCheckResult(t, f.runCheck(t, both), "passed")

	assertCheckResult(t, f.runCheck(t, shellCheck("test -e node_modules && test ! -e .venv")), "passed")
}

func TestCheckWorkspaceIsFreshForEveryCheck(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, ".gitignore", "node_modules/\n")
	writeAgentFile(t, f.checkout, "node_modules/pkg/index.js", "original\n")

	assertCheckResult(t, f.runCheck(t, shellCheck("touch generated.txt && echo tampered > node_modules/pkg/index.js && touch node_modules/extra.js && rm README.md")), "passed")

	assertCheckResult(t, f.runCheck(t, shellCheck("test ! -e generated.txt && test ! -e node_modules/extra.js && grep -q original node_modules/pkg/index.js && test -f README.md")), "passed")
	if _, err := os.Stat(filepath.Join(f.checkout, "README.md")); err != nil {
		t.Fatalf("a check changed the agent's files: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.checkout, "generated.txt")); err == nil {
		t.Fatal("a check wrote into the agent's directory")
	}
}

func TestCheckWorkspaceKeepsPublishedContentOverACarriedDirectory(t *testing.T) {
	f := newAgentResultTestWith(t, func(origin string) {
		writeAgentFile(t, origin, ".gitignore", "node_modules/\n")
		writeAgentFile(t, origin, "node_modules/pkg/index.js", "published\n")
		buildWorkspaceGit(t, origin, "add", "-f", "node_modules/pkg/index.js")
	})
	writeAgentFile(t, f.checkout, "node_modules/other.js", "ignored\n")
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	commit := f.publish(t)
	published := f.publishedContent(t, commit, "node_modules/pkg/index.js")

	result := f.runCheck(t, shellCheck("cat node_modules/pkg/index.js && test -f node_modules/other.js && false"))

	if !strings.Contains(fmt.Sprint(result["output"]), published) {
		t.Fatalf("check saw %v, want the published %q", result, published)
	}
}

func TestCheckSeesOutOfConeSparseFiles(t *testing.T) {
	f := newSparseResultTest(t)
	agentGit(t, f.checkout, "sparse-checkout", "set", "--cone", "src")
	writeAgentFile(t, f.checkout, "src/app.txt", "changed\n")

	assertCheckResult(t, f.runCheck(t, shellCheck("test -f docs/guide.txt && grep -q changed src/app.txt")), "passed")
}

func TestCheckEvidenceNamesTheMaterialisedCommit(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")

	result := f.runCheck(t, shellCheck("true"))

	if result["commit_sha"] != f.publish(t) || result["worktree_dirty"] != false {
		t.Fatalf("evidence = %v", result)
	}
}

func TestCheckReadsAnotherRepositoryThroughItsPublishedTree(t *testing.T) {
	jr, _ := runCheckFixture(t)
	secondary := jr.repositories[1].WorkDir
	writeAgentFile(t, secondary, "shared.txt", "shared\n")
	writeAgentFile(t, secondary, ".gitignore", "private.txt\n")
	writeAgentFile(t, secondary, "private.txt", "private\n")
	jr.d.cfg.Checks["cross"] = config.CheckConfig{Command: "/bin/sh", Args: []string{"-c", "test -f ../secondary/shared.txt && test ! -e ../secondary/private.txt"}}
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "cross")

	response, err := call(t, jr, "platform_run_check", `{"check_id":"cross","idempotency_key":"rc-cross"}`)

	if err != nil {
		t.Fatal(err)
	}
	assertCheckResult(t, response.(map[string]any), "passed")
}

func TestCheckWorkspaceFailuresAreTypedWithoutHostPaths(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, ".gitignore", "node_modules/\n")
	for _, name := range []string{"a", "b", "c", "d"} {
		writeAgentFile(t, f.checkout, "node_modules/"+name+".js", name)
	}
	f.jr.checkWorkspace().entryLimit = 2

	_, err := f.runCheckTry(t, shellCheck("true"))

	if err == nil || !strings.Contains(err.Error(), "dependency directories have more than 2 entries") {
		t.Fatalf("err = %v, want the dependency limit", err)
	}
	assertNoHostPath(t, err.Error(), f.jr.d.cfg.StateDir, f.checkout)

	f.jr.job.RequiredChecks = []string{"limited"}
	f.jr.d.cfg.Checks["limited"] = shellCheck("true")
	if f.jr.runRequiredChecks(context.Background(), map[string]string{"repo-1": f.base}, nil) {
		t.Fatal("a required check passed without its workspace")
	}
	events := f.events.byKind("test_results")
	last := events[len(events)-1]
	if last["result"] != "error" || last["error_classification"] != "internal" {
		t.Fatalf("terminal event = %v", last)
	}
	failures := fmt.Sprint(last["failures"])
	assertNoHostPath(t, failures, f.jr.d.cfg.StateDir, f.checkout)
}

func TestCheckWorkspaceRecoversFromAnUnremovableCheckOutput(t *testing.T) {
	f := newAgentResultTest(t)

	assertCheckResult(t, f.runCheck(t, shellCheck("mkdir -p locked/inner && touch locked/inner/file && chmod 000 locked")), "passed")

	assertCheckResult(t, f.runCheck(t, shellCheck("test ! -e locked")), "passed")
}

func TestRequiredChecksUseTheCheckWorkspace(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, ".gitignore", "secret.env\n")
	writeAgentFile(t, f.checkout, "secret.env", "TOKEN=x\n")
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	f.jr.job.RequiredChecks = []string{"published", "unpublished"}
	f.jr.d.cfg.Checks = map[string]config.CheckConfig{
		"published":   shellCheck("test -f feature.txt"),
		"unpublished": shellCheck("test -f secret.env"),
	}

	if err := f.finalizeWithPush(t); err != nil {
		t.Fatal(err)
	}

	results := map[string]any{}
	for _, event := range f.events.byKind("test_results") {
		results[fmt.Sprint(event["check_id"])] = event["result"]
	}
	if results["published"] != "passed" || results["unpublished"] != "failed" {
		t.Fatalf("terminal results = %v", results)
	}
}

func TestCheckWorkspaceIsRemovedWithTheTrustedResults(t *testing.T) {
	f := newAgentResultTest(t)
	assertCheckResult(t, f.runCheck(t, shellCheck("mkdir -p locked && chmod 000 locked")), "passed")
	roots := []string{trustedResultsRoot(f.jr.d.cfg.StateDir, f.jr.workDir), checkWorkspaceRoot(f.jr.d.cfg.StateDir, f.jr.workDir)}
	rel, err := f.jr.checkRepositoryRel(f.jr.repositories[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(roots[1], rel, "locked")); err != nil {
		t.Fatalf("the check did not run in the check workspace: %v", err)
	}

	if err := removeTrustedResults(f.jr.d.cfg.StateDir, f.jr.workDir); err != nil {
		t.Fatal(err)
	}

	for _, root := range roots {
		if _, err := os.Lstat(root); !os.IsNotExist(err) {
			t.Fatalf("trusted results remain at %s: %v", root, err)
		}
	}
}

func TestOrphanSweepRemovesUnremovableCheckWorkspaces(t *testing.T) {
	state := t.TempDir()
	locked := filepath.Join(checkWorkspaceRoot(state, "job-1"), "repo", "locked")
	if err := os.MkdirAll(filepath.Join(locked, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}

	if err := cleanupOrphanedJobState(context.Background(), state, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(checkWorkspaceRoot(state, "job-1")); !os.IsNotExist(err) {
		t.Fatalf("check workspace remains: %v", err)
	}
}

func TestCheckPolicyMountsOnlyTheCheckWorkspace(t *testing.T) {
	jr, _ := testJobRunner(t)
	policy := jr.commandPolicy("/state/job-results/job-1/.checks/repositories/repo", "/state/job-results/job-1/.checks", "/home", "attempt", true)
	if policy.WorkspaceRoot != "/state/job-results/job-1/.checks" || policy.WorkDir != "/state/job-results/job-1/.checks/repositories/repo" {
		t.Fatalf("policy = %+v", policy)
	}
}

func TestCheckDependencyCopyKeepsLinksAndSkipsSpecialFiles(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, ".gitignore", "node_modules/\n")
	writeAgentFile(t, f.checkout, "node_modules/pkg/bin/run.js", "run\n")
	if err := os.MkdirAll(filepath.Join(f.checkout, "node_modules", ".bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../pkg/bin/run.js", filepath.Join(f.checkout, "node_modules", ".bin", "run")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(f.checkout, "node_modules", "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}

	script := "test -L node_modules/.bin/run && grep -q run node_modules/.bin/run && test ! -e node_modules/pipe"
	assertCheckResult(t, f.runCheck(t, shellCheck(script)), "passed")
	assertCheckResult(t, f.runCheck(t, shellCheck(script)), "passed")
}

func TestCheckDependencyLimitsAreTyped(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit func(*checkWorkspace)
		want  string
	}{
		{"bytes", func(w *checkWorkspace) { w.byteLimit = 3 }, "dependency directories have more than 3 bytes"},
		{"depth", func(w *checkWorkspace) { w.depthLimit = 1 }, "dependency directories are more than 1 directories deep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentResultTest(t)
			writeAgentFile(t, f.checkout, ".gitignore", "node_modules/\n")
			writeAgentFile(t, f.checkout, "node_modules/pkg/index.js", "content\n")
			tc.limit(f.jr.checkWorkspace())

			_, err := f.runCheckTry(t, shellCheck("true"))

			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
