package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		{"the agent's dependency directory is absent", func(t *testing.T, f agentResultTest) {
			writeAgentFile(t, f.checkout, ".gitignore", "node_modules/\n")
			writeAgentFile(t, f.checkout, "node_modules/pkg/index.js", "original\n")
		}, "test ! -e node_modules", "passed"},
		{"the agent's nested dependency directory is absent", func(t *testing.T, f agentResultTest) {
			writeAgentFile(t, f.checkout, ".gitignore", "node_modules/\n")
			writeAgentFile(t, f.checkout, "packages/a/node_modules/pkg/index.js", "nested\n")
			writeAgentFile(t, f.checkout, "packages/a/index.js", "published\n")
		}, "test -f packages/a/index.js && test ! -e packages/a/node_modules", "passed"},
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

var agentOnlyDirectories = []string{"node_modules", "vendor", ".venv", "target", ".gradle", "bin", "dist", "packages/a/node_modules"}

func plantIgnoredDirectories(t *testing.T, checkout string, directories []string) {
	t.Helper()
	ignore := ""
	for _, directory := range directories {
		ignore += "/" + directory + "/\n"
		writeAgentFile(t, checkout, directory+"/planted.txt", "agent content\n")
	}
	writeAgentFile(t, checkout, ".gitignore", ignore)
	writeAgentFile(t, checkout, "packages/a/index.js", "published\n")
}

func absentScript(prefix string, directories []string) string {
	script := "true"
	for _, directory := range directories {
		script += " && test ! -e " + prefix + directory
	}
	return script
}

func TestNoAgentDirectoryReachesTheCheckWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name        string
		directories []string
	}{
		{"zero ignored directories", nil},
		{"one ignored directory", []string{"node_modules"}},
		{"many ignored directories", agentOnlyDirectories},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentResultTest(t)
			plantIgnoredDirectories(t, f.checkout, tc.directories)
			script := absentScript("", agentOnlyDirectories) + " && test -f packages/a/index.js"

			assertCheckResult(t, f.runCheck(t, shellCheck(script)), "passed")
			empty := shellCheck(script)
			empty.DependencyDirectories = []string{}
			assertCheckResult(t, f.runCheck(t, empty), "passed")
		})
	}
}

func TestNoAgentDirectoryReachesAnyRepositoryOfTheCheckWorkspace(t *testing.T) {
	jr, _ := runCheckFixture(t)
	for _, repository := range jr.repositories {
		plantIgnoredDirectories(t, repository.WorkDir, agentOnlyDirectories)
	}
	script := absentScript("", agentOnlyDirectories) + " && " + absentScript("../secondary/", agentOnlyDirectories) +
		" && test -f packages/a/index.js && test -f ../secondary/packages/a/index.js"
	jr.d.cfg.Checks["absent"] = shellCheck(script)
	jr.job.RequiredChecks = append(jr.job.RequiredChecks, "absent")

	response, err := call(t, jr, "platform_run_check", `{"check_id":"absent","idempotency_key":"rc-absent"}`)

	if err != nil {
		t.Fatal(err)
	}
	assertCheckResult(t, response.(map[string]any), "passed")
}

func TestCheckWorkspaceIsFreshForEveryCheck(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, ".gitignore", "node_modules/\n")
	writeAgentFile(t, f.checkout, "node_modules/pkg/index.js", "original\n")

	assertCheckResult(t, f.runCheck(t, shellCheck("test ! -e node_modules && touch generated.txt && mkdir -p node_modules/pkg && touch node_modules/extra.js && rm README.md")), "passed")

	assertCheckResult(t, f.runCheck(t, shellCheck("test ! -e generated.txt && test ! -e node_modules && test -f README.md")), "passed")
	if got, err := os.ReadFile(filepath.Join(f.checkout, "node_modules", "pkg", "index.js")); err != nil || string(got) != "original\n" {
		t.Fatalf("a check changed the agent's dependency directory: %q, %v", got, err)
	}
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
	writeAgentFile(t, f.checkout, "node_modules/pkg/extra.js", "ignored\n")
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	commit := f.publish(t)
	published := f.publishedContent(t, commit, "node_modules/pkg/index.js")

	if strings.TrimSpace(published) != "published" {
		t.Fatalf("published content = %q", published)
	}

	result := f.runCheck(t, shellCheck("test \"$(cat node_modules/pkg/index.js)\" = published && "+
		"test \"$(find node_modules | sort | tr '\\n' ' ')\" = 'node_modules node_modules/pkg node_modules/pkg/index.js '"))

	assertCheckResult(t, result, "passed")
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
	f := newNPMResultTest(t)
	useFakeNPM(t, "mkdir -p node_modules && for name in a b c d; do echo $name > node_modules/$name.js; done")
	f.jr.checkWorkspace().entryLimit = 2

	result := f.runCheck(t, npmInstallCheck("true"))

	if result["result"] != "error" || result["error_classification"] != "internal" || result["output"] != "installed dependencies have more than 2 entries" {
		t.Fatalf("result = %v, want the installed dependency limit", result)
	}
	assertNoHostPath(t, fmt.Sprint(result["output"]), f.jr.d.cfg.StateDir, f.checkout)

	f.jr.job.RequiredChecks = []string{"limited"}
	f.jr.d.cfg.Checks["limited"] = npmInstallCheck("true")
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

func TestInstalledDependenciesKeepLinksAndRefuseSpecialFiles(t *testing.T) {
	f := newNPMResultTest(t)
	npm := useFakeNPM(t, "mkdir -p node_modules/pkg/bin node_modules/.bin && echo run > node_modules/pkg/bin/run.js && ln -s ../pkg/bin/run.js node_modules/.bin/run")

	script := "test -L node_modules/.bin/run && grep -q run node_modules/.bin/run"
	assertCheckResult(t, f.runCheck(t, npmInstallCheck(script)), "passed")
	assertCheckResult(t, f.runCheck(t, npmInstallCheck(script)), "passed")
	if got := len(npm.ciCalls(t)); got != 2 {
		t.Fatalf("npm ci ran %d times, want once per check run", got)
	}

	useFakeNPM(t, "mkdir -p node_modules && mkfifo node_modules/pipe")
	result := f.runCheck(t, npmInstallCheck("true"))
	if result["result"] != "error" || result["output"] != "dependency install for check check-2 produced a special file: node_modules/pipe" {
		t.Fatalf("result = %v", result)
	}
}

func TestInstalledDependencyLimitsAreTyped(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit func(*checkWorkspace)
		want  string
	}{
		{"bytes", func(w *checkWorkspace) { w.byteLimit = 3 }, "installed dependencies have more than 3 bytes"},
		{"depth", func(w *checkWorkspace) { w.depthLimit = 1 }, "installed dependencies are more than 1 directories deep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNPMResultTest(t)
			useFakeNPM(t, "mkdir -p node_modules/pkg && echo content > node_modules/pkg/index.js")
			tc.limit(f.jr.checkWorkspace())

			result := f.runCheck(t, npmInstallCheck("true"))

			if result["result"] != "error" || result["error_classification"] != "internal" || result["output"] != tc.want {
				t.Fatalf("result = %v, want %q", result, tc.want)
			}
		})
	}
}
