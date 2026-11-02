package daemon

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/isolation"
)

const npmCIArgs = "ci --ignore-scripts --no-audit --no-fund --no-update-notifier"

func TestInstallArgvAndNetworkFollowTheIsolationMode(t *testing.T) {
	for _, tc := range []struct {
		isolation, network, networkName string
		wantNetwork                     string
		wantOffline                     bool
	}{
		{"direct", "", "", installNetworkHost, false},
		{"direct", "none", "", installNetworkHost, false},
		{"docker", "none", "", installNetworkNone, true},
		{"docker", "restricted", "runner-egress", installNetworkRestricted, false},
	} {
		t.Run(tc.isolation+"-"+tc.network, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			jr.d.cfg.Isolation, jr.d.cfg.JobNetwork, jr.d.cfg.JobNetworkName = tc.isolation, tc.network, tc.networkName
			want := []string{"npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--no-update-notifier"}
			if tc.wantOffline {
				want = append(want, "--offline")
			}
			if got := jr.npmInstallArgv(); !slices.Equal(got, want) {
				t.Fatalf("argv = %q, want %q", got, want)
			}
			if jr.installNetwork() != tc.wantNetwork || jr.offlineInstall() != tc.wantOffline {
				t.Fatalf("network = %s offline = %v", jr.installNetwork(), jr.offlineInstall())
			}
		})
	}
}

func TestInstallProbesAndInstallsEachDirectoryInConfigurationOrder(t *testing.T) {
	f := newNPMResultTest(t, "a", "b", "c")
	npm := useFakeNPM(t, fakeNPMInstallBody)
	check := npmInstallCheck("test -f c/node_modules/fixture-dep/index.js && test -f a/node_modules/fixture-dep/index.js && test -f b/node_modules/fixture-dep/index.js && test -z \"${npm_config_cache:-}\"", "c", "a", "b")

	assertCheckResult(t, f.runCheck(t, check), "passed")

	root, err := filepath.EvalSymlinks(f.jr.checkWorkspace().root)
	if err != nil {
		t.Fatal(err)
	}
	cache := npmCacheRoot(f.jr.d.cfg.StateDir, f.jr.workDir)
	repoDir := filepath.Join(root, "repositories", "repo-1")
	calls := npm.calls(t)
	want := []npmCall{
		{dir: filepath.Join(repoDir, "c"), args: "--version", cache: cache},
		{dir: filepath.Join(repoDir, "c"), args: npmCIArgs, cache: cache},
		{dir: filepath.Join(repoDir, "a"), args: "--version", cache: cache},
		{dir: filepath.Join(repoDir, "a"), args: npmCIArgs, cache: cache},
		{dir: filepath.Join(repoDir, "b"), args: "--version", cache: cache},
		{dir: filepath.Join(repoDir, "b"), args: npmCIArgs, cache: cache},
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("npm calls = %+v\nwant %+v", calls, want)
	}
}

func TestEveryCheckRunReinstallsFromAnEmptyDirectory(t *testing.T) {
	f := newNPMResultTest(t)
	npm := useFakeNPM(t, "test ! -e node_modules || exit 42\n"+fakeNPMInstallBody)
	check := npmInstallCheck("test -f node_modules/fixture-dep/index.js")

	assertCheckResult(t, f.runCheck(t, check), "passed")
	assertCheckResult(t, f.runCheck(t, check), "passed")

	var args []string
	for _, call := range npm.calls(t) {
		args = append(args, call.args)
	}
	if !slices.Equal(args, []string{"--version", npmCIArgs, "--version", npmCIArgs}) {
		t.Fatalf("npm calls = %q", args)
	}
}

func TestACheckWithoutInstallRunsNoInstallCommand(t *testing.T) {
	f := newNPMResultTest(t)
	npm := useFakeNPM(t, fakeNPMInstallBody)

	assertCheckResult(t, f.runCheck(t, shellCheck("test ! -e node_modules && test -z \"${npm_config_cache:-}\"")), "passed")

	if calls := npm.calls(t); len(calls) != 0 {
		t.Fatalf("npm ran without install: %v", calls)
	}
	if _, err := os.Stat(npmCacheRoot(f.jr.d.cfg.StateDir, f.jr.workDir)); !os.IsNotExist(err) {
		t.Fatalf("npm cache created without install: %v", err)
	}
}

func TestInstallEnvironmentAddsOnlyTheNPMCache(t *testing.T) {
	base := []string{"PATH=/usr/bin", "LANG=C"}
	got := installEnvironment(base, "/state/job-results/job-1.npm-cache")
	if !slices.Equal(got, []string{"PATH=/usr/bin", "LANG=C", "npm_config_cache=/state/job-results/job-1.npm-cache"}) {
		t.Fatalf("environment = %q", got)
	}
	if !slices.Equal(base, []string{"PATH=/usr/bin", "LANG=C"}) {
		t.Fatalf("base environment changed: %q", base)
	}

	f := newNPMResultTest(t)
	envFile := filepath.Join(t.TempDir(), "env")
	useFakeNPM(t, "env > '"+envFile+"'\n"+fakeNPMInstallBody)
	assertCheckResult(t, f.runCheck(t, npmInstallCheck("true")), "passed")
	raw, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	wantEnv := map[string]bool{}
	for _, entry := range installEnvironment(allowedCheckEnvironment(), npmCacheRoot(f.jr.d.cfg.StateDir, f.jr.workDir)) {
		wantEnv[entry] = true
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		name, _, _ := strings.Cut(line, "=")
		if name == "PWD" || name == "OLDPWD" || name == "SHLVL" || name == "_" {
			continue
		}
		if !wantEnv[line] {
			t.Fatalf("npm saw unexpected environment entry %q", line)
		}
	}
}

type dockerArgvLog struct{ path string }

func useLoggingDocker(t *testing.T) dockerArgvLog {
	t.Helper()
	dir := t.TempDir()
	log := dockerArgvLog{path: filepath.Join(dir, "argv.log")}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log.path + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func (l dockerArgvLog) runs(t *testing.T) [][]string {
	t.Helper()
	raw, err := os.ReadFile(l.path)
	if err != nil {
		t.Fatal(err)
	}
	var runs [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		runs = append(runs, strings.Fields(line))
	}
	return runs
}

func flagValue(args []string, flag string) string {
	index := slices.Index(args, flag)
	if index < 0 || index+1 >= len(args) {
		return ""
	}
	return args[index+1]
}

func flagValues(args []string, flag string) []string {
	var values []string
	for index, arg := range args {
		if arg == flag && index+1 < len(args) {
			values = append(values, args[index+1])
		}
	}
	return values
}

func TestDockerInstallRunsInItsOwnContainerAndOnlyItMountsTheCache(t *testing.T) {
	for _, tc := range []struct {
		network, networkName, wantNetwork string
	}{
		{"none", "", "none"},
		{"restricted", "runner-egress", "runner-egress"},
	} {
		t.Run(tc.network, func(t *testing.T) {
			h := newCheckOwnershipHarness(t)
			docker := useLoggingDocker(t)
			jr := h.jr
			jr.d.cfg.JobNetwork, jr.d.cfg.JobNetworkName = tc.network, tc.networkName
			jr.d.cfg.JobMemoryMB, jr.d.cfg.JobCPUs, jr.d.cfg.JobPIDs = 2048, 2, 256
			cache := npmCacheRoot(jr.d.cfg.StateDir, jr.workDir)
			if err := prepareNPMCache(cache, maxNPMCacheBytes); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(h.root, "primary")
			request := checkRequest{checkID: "lint", check: npmInstallCheck("npm test")}
			if _, _, err := jr.runInstallCommand(context.Background(), request, dir, jr.npmInstallArgv()[1:], cache, 1024); err != nil {
				t.Fatal(err)
			}
			if _, _, err := jr.runWorkspaceCommand(context.Background(), "check-lint", h.root, dir, "npm", []string{"test"}, true, 1024); err != nil {
				t.Fatal(err)
			}

			runs := docker.runs(t)
			if len(runs) != 2 {
				t.Fatalf("docker runs = %q", runs)
			}
			install, check := runs[0], runs[1]
			wantInstallName := isolation.ContainerName("runner-1", jr.job.AttemptID+"-task-"+shortDigest("install-lint"))
			if flagValue(install, "--name") != wantInstallName || flagValue(install, "--name") == flagValue(check, "--name") {
				t.Fatalf("container names install=%s check=%s", flagValue(install, "--name"), flagValue(check, "--name"))
			}
			for _, args := range [][]string{install, check} {
				for _, flag := range []string{"--memory", "--cpus", "--pids-limit", "--cap-drop", "--user", "--network"} {
					if flagValue(args, flag) == "" || flagValue(args, flag) != flagValue(install, flag) {
						t.Fatalf("%s differs or is missing: install %q check %q", flag, install, check)
					}
				}
				if flagValue(args, "--network") != tc.wantNetwork || slices.Contains(args, "host") ||
					!slices.Contains(args, "--read-only") || !slices.Contains(flagValues(args, "--security-opt"), "no-new-privileges") {
					t.Fatalf("isolation flags = %q", args)
				}
				if !slices.Contains(flagValues(args, "-v"), h.root+":"+h.root+":rw") {
					t.Fatalf("check root mount missing: %q", args)
				}
			}
			cacheMount := cache + ":" + cache + ":rw"
			installMounts, checkMounts := flagValues(install, "-v"), flagValues(check, "-v")
			if !slices.Contains(installMounts, cacheMount) || !slices.Contains(flagValues(install, "-e"), "npm_config_cache="+cache) {
				t.Fatalf("install mounts = %q", installMounts)
			}
			if len(installMounts) != 3 {
				t.Fatalf("install mounts more than the check root, home and cache: %q", installMounts)
			}
			if strings.Contains(strings.Join(check, " "), "npm-cache") || strings.Contains(strings.Join(check, " "), "npm_config_cache") {
				t.Fatalf("check container sees the npm cache: %q", check)
			}
			if len(checkMounts) != 2 {
				t.Fatalf("check mounts = %q", checkMounts)
			}

			if got := h.log.String(); got != "handoff:install remove reclaim:install handoff:check remove reclaim:check" {
				t.Fatalf("ownership calls = %q", got)
			}
			handOff := h.ownership.handOffs[0]
			if !slices.Equal(handOff.Workspace, []string{h.root}) || len(handOff.Private) != 2 || handOff.Private[1] != cache ||
				!strings.HasPrefix(handOff.Private[0], filepath.Join(jr.d.cfg.StateDir, "check-homes", "home-")) {
				t.Fatalf("install hand-off = %+v", handOff)
			}
			if reclaim := h.ownership.reclaims[0]; !slices.Equal(reclaim.Private, handOff.Private) || !slices.Equal(reclaim.Workspace, handOff.Workspace) {
				t.Fatalf("install reclaim = %+v", reclaim)
			}
			if checkHandOff := h.ownership.handOffs[1]; len(checkHandOff.Private) != 1 {
				t.Fatalf("check hand-off = %+v", checkHandOff)
			}
		})
	}
}

func TestOnlyTheInstallTaskSetsTheNPMCacheMount(t *testing.T) {
	fset := token.NewFileSet()
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var cacheFields, cacheTasks []string
	for _, name := range matches {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			function, ok := decl.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch value := node.(type) {
				case *ast.SelectorExpr:
					if value.Sel.Name == "NPMCacheDir" {
						cacheFields = append(cacheFields, function.Name.Name)
					}
				case *ast.KeyValueExpr:
					if key, ok := value.Key.(*ast.Ident); ok && key.Name == "NPMCacheDir" {
						cacheFields = append(cacheFields, function.Name.Name)
					}
				case *ast.CompositeLit:
					if kind, ok := value.Type.(*ast.Ident); ok && kind.Name == "workspaceTask" {
						for _, element := range value.Elts {
							if field, ok := element.(*ast.KeyValueExpr); ok {
								if key, ok := field.Key.(*ast.Ident); ok && key.Name == "npmCache" {
									cacheTasks = append(cacheTasks, function.Name.Name)
								}
							}
						}
					}
				}
				return true
			})
		}
	}
	if !slices.Equal(cacheFields, []string{"runWorkspaceTask"}) || !slices.Equal(cacheTasks, []string{"runInstallCommand"}) {
		t.Fatalf("npm cache policy set in %v; workspace tasks with a cache in %v", cacheFields, cacheTasks)
	}
}

func TestInstallTimeoutStopsBeforeTheCheckRuns(t *testing.T) {
	f := newNPMResultTest(t)
	useFakeNPM(t, "sleep 10")
	marker := filepath.Join(t.TempDir(), "ran")
	check := npmInstallCheck("touch '" + marker + "'")
	check.Install.TimeoutSeconds = 1
	started := time.Now()

	result := f.runCheck(t, check)

	if result["result"] != "error" || result["error_classification"] != "timeout" ||
		result["output"] != "dependency install for check check-0 took longer than 1s" {
		t.Fatalf("result = %v", result)
	}
	if elapsed := time.Since(started); elapsed > 8*time.Second {
		t.Fatalf("install was not stopped at its timeout: %v", elapsed)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("check ran after an install timeout")
	}
}

func TestInstallFailureReportsOutputWithoutHostPaths(t *testing.T) {
	f := newNPMResultTest(t)
	useFakeNPM(t, "echo \"broke in $(pwd -P) and $npm_config_cache\"; exit 3")
	marker := filepath.Join(t.TempDir(), "ran")

	result := f.runCheck(t, npmInstallCheck("touch '"+marker+"'"))

	output, _ := result["output"].(string)
	if result["result"] != "error" || result["error_classification"] != "internal" || result["truncated"] != false ||
		output != "dependency install for check check-0 failed (npm ci exited 3): broke in <check workspace>/repositories/repo-1 and <npm cache>\n" {
		t.Fatalf("result = %v", result)
	}
	if strings.Contains(output, f.jr.d.cfg.StateDir) {
		t.Fatalf("output leaks a host path: %q", output)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("check ran after an install failure")
	}
}

func TestInstallFailureOutputIsRedacted(t *testing.T) {
	f := newNPMResultTest(t)
	token := "ghp_" + strings.Repeat("a", 30)
	useFakeNPM(t, "echo \"broke in $(pwd -P) with "+token+"\"; exit 3")

	result := f.runCheck(t, npmInstallCheck("true"))

	output, _ := result["output"].(string)
	if output != "dependency install for check check-0 failed (npm ci exited 3): "+redactCheckOutput("x "+token) ||
		strings.Contains(output, token) || strings.Contains(output, f.jr.d.cfg.StateDir) {
		t.Fatalf("output = %q", output)
	}
}

func TestInstallFailureOutputIsTruncatedToTheMessageLimit(t *testing.T) {
	f := newNPMResultTest(t)
	useFakeNPM(t, "i=0; while [ $i -lt 300 ]; do printf 'éééééééééééééééééééééééééééééé\\n'; i=$((i+1)); done; exit 3")

	result := f.runCheck(t, npmInstallCheck("true"))

	output, _ := result["output"].(string)
	if result["truncated"] != true || len(output) > maxInstallMessageBytes || len(output) < maxInstallMessageBytes-4 ||
		!strings.HasPrefix(output, "dependency install for check check-0 failed (npm ci exited 3): ") || !utf8Valid(output) {
		t.Fatalf("result truncated=%v len=%d", result["truncated"], len(output))
	}
}

func utf8Valid(value string) bool {
	return strings.ToValidUTF8(value, "�") == value
}

func TestMissingNPMIsReportedWithoutRunningTheCheck(t *testing.T) {
	f := newNPMResultTest(t)
	bin := t.TempDir()
	for _, tool := range []string{"git", "sh"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	marker := filepath.Join(t.TempDir(), "ran")

	result := f.runCheck(t, npmInstallCheck("touch '"+marker+"'"))

	if result["result"] != "error" || result["error_classification"] != "internal" ||
		result["output"] != "dependency install for check check-0 failed: npm is not available in the check environment" {
		t.Fatalf("result = %v", result)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("check ran without npm")
	}
}

func TestLeaseLossStopsTheInstallAndEmitsNoEvidence(t *testing.T) {
	f := newNPMResultTest(t)
	useFakeNPM(t, "sleep 30")
	marker := filepath.Join(t.TempDir(), "ran")
	go func() {
		time.Sleep(500 * time.Millisecond)
		f.jr.d.leases.Release(f.jr.job.AttemptID)
	}()
	started := time.Now()

	if _, err := f.runCheckTry(t, npmInstallCheck("touch '"+marker+"'")); err == nil {
		t.Fatal("an install that lost its lease returned evidence")
	}

	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("install was not stopped after lease loss: %v", elapsed)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("check ran after lease loss")
	}
	if got := len(f.events.byKind("test_results")); got != 0 {
		t.Fatalf("evidence emitted after lease loss: %d", got)
	}
}

func TestJobCancellationDuringATerminalInstallIsClassifiedCancelled(t *testing.T) {
	f := newNPMResultTest(t)
	useFakeNPM(t, "sleep 30")
	commit := f.publish(t)
	f.jr.job.RequiredChecks = []string{"lint"}
	f.jr.d.cfg.Checks = map[string]config.CheckConfig{"lint": npmInstallCheck("true")}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	if f.jr.runRequiredChecks(ctx, map[string]string{"repo-1": commit}, nil) {
		t.Fatal("cancelled install passed")
	}

	events := f.events.byKind("test_results")
	last := events[len(events)-1]
	if last["result"] != "error" || last["error_classification"] != "cancelled" ||
		!slices.Equal(stringSlice(last["failures"]), []string{"dependency install for check lint was cancelled"}) {
		t.Fatalf("event = %v", last)
	}
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, _ := item.(string)
			out = append(out, text)
		}
		return out
	default:
		return nil
	}
}

func TestTheNPMCacheIsPerJobOutsideTheWorkspaceWithMode0700(t *testing.T) {
	f := newNPMResultTest(t)
	useFakeNPM(t, fakeNPMInstallBody)

	assertCheckResult(t, f.runCheck(t, npmInstallCheck("true")), "passed")

	cache := npmCacheRoot(f.jr.d.cfg.StateDir, f.jr.workDir)
	want := filepath.Join(f.jr.d.cfg.StateDir, "job-results", filepath.Base(f.jr.workDir)+".npm-cache")
	if cache != want {
		t.Fatalf("cache = %s, want %s", cache, want)
	}
	if rel, err := filepath.Rel(f.jr.workDir, cache); err != nil || !strings.HasPrefix(rel, "..") {
		t.Fatalf("cache %s is inside the workspace %s", cache, f.jr.workDir)
	}
	info, err := os.Stat(cache)
	if err != nil || info.Mode().Perm() != 0o700 || !info.IsDir() {
		t.Fatalf("cache = %v, %v", info, err)
	}
}

func seedUnreadableCache(t *testing.T, cache string) {
	t.Helper()
	locked := filepath.Join(cache, "_cacache", "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "entry"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
}

func TestTheNPMCacheIsRemovedByCleanupAndTheOrphanSweep(t *testing.T) {
	t.Run("cleanup", func(t *testing.T) {
		state, work := t.TempDir(), filepath.Join(t.TempDir(), "job-abc")
		cache := npmCacheRoot(state, work)
		seedUnreadableCache(t, cache)
		if err := removeTrustedResults(state, work); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(cache); !os.IsNotExist(err) {
			t.Fatalf("cache survived cleanup: %v", err)
		}
	})
	t.Run("orphan sweep", func(t *testing.T) {
		state := t.TempDir()
		cache := filepath.Join(state, "job-results", "job-abc.npm-cache")
		seedUnreadableCache(t, cache)
		var reclaimed []string
		if err := cleanupOrphanedJobState(context.Background(), state, func(_ context.Context, paths []string) error {
			reclaimed = append(reclaimed, paths...)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(cache); !os.IsNotExist(err) {
			t.Fatalf("cache survived the orphan sweep: %v", err)
		}
		if !slices.Contains(reclaimed, cache) {
			t.Fatalf("cache was not re-owned before removal: %v", reclaimed)
		}
	})
}

func TestTheNPMCacheIsWipedWhenItExceedsItsCap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int64
		kept  bool
	}{
		{"above the cap", 10, false},
		{"within the cap", 1 << 20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNPMResultTest(t)
			useFakeNPM(t, fakeNPMInstallBody)
			f.jr.checkWorkspace().npmCacheByteLimit = tc.limit
			cache := npmCacheRoot(f.jr.d.cfg.StateDir, f.jr.workDir)
			old := filepath.Join(cache, "old-entry")
			if err := os.MkdirAll(cache, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(old, []byte(strings.Repeat("x", 100)), 0o600); err != nil {
				t.Fatal(err)
			}

			assertCheckResult(t, f.runCheck(t, npmInstallCheck("true")), "passed")

			if _, err := os.Stat(old); (err == nil) != tc.kept {
				t.Fatalf("old cache entry kept=%v, want %v", err == nil, tc.kept)
			}
		})
	}
}

func eventKeys(event map[string]any) []string {
	return slices.Sorted(maps.Keys(event))
}

func TestInstallOutcomesKeepTheTestResultsEventShape(t *testing.T) {
	type outcome struct {
		name, script, body string
		install            bool
		timeout            int
		result, class      string
	}
	outcomes := []outcome{
		{"passed", "true", fakeNPMInstallBody, false, 0, "passed", ""},
		{"failed", "exit 1", fakeNPMInstallBody, false, 0, "failed", "exit_nonzero"},
		{"install refused", "true", "", true, 0, "error", "internal"},
		{"install timeout", "true", "sleep 10", true, 1, "error", "timeout"},
		{"install failed", "true", "exit 3", true, 0, "error", "internal"},
	}
	terminal := map[string][]string{}
	iterative := map[string][]string{}
	for _, tc := range outcomes {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentResultTestWith(t, func(origin string) {
				if tc.name != "install refused" {
					seedNPMProject(t, origin)
				}
			})
			useFakeNPM(t, tc.body)
			check := shellCheck(tc.script)
			if tc.install {
				check = npmInstallCheck(tc.script)
				check.Install.TimeoutSeconds = tc.timeout
			}
			result := f.runCheck(t, check)
			if result["result"] != tc.result || (tc.class != "" && result["error_classification"] != tc.class) {
				t.Fatalf("iterative result = %v", result)
			}
			commit := f.publish(t)
			f.jr.job.RequiredChecks = []string{"shape"}
			f.jr.d.cfg.Checks = map[string]config.CheckConfig{"shape": check}
			f.jr.runRequiredChecks(context.Background(), map[string]string{"repo-1": commit}, nil)
			events := f.events.byKind("test_results")
			if len(events) != 2 {
				t.Fatalf("events = %v", events)
			}
			if events[1]["result"] != tc.result || events[1]["commit_sha"] != commit {
				t.Fatalf("terminal event = %v", events[1])
			}
			iterative[tc.name], terminal[tc.name] = eventKeys(events[0]), eventKeys(events[1])
		})
	}
	for _, name := range []string{"install refused", "install timeout", "install failed"} {
		if !slices.Equal(iterative[name], iterative["failed"]) || !slices.Equal(terminal[name], terminal["failed"]) {
			t.Fatalf("%s keys iterative %v terminal %v; failed has %v and %v", name, iterative[name], terminal[name], iterative["failed"], terminal["failed"])
		}
	}
	if slices.Contains(iterative["passed"], "failures") || slices.Contains(terminal["passed"], "failures") {
		t.Fatalf("passed events carry failures: %v %v", iterative["passed"], terminal["passed"])
	}
}
