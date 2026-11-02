package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/config"
)

func blockedPath(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(file, "child")
}

func TestAnUnpreparableNPMCacheStopsTheInstall(t *testing.T) {
	t.Run("before the version probe", func(t *testing.T) {
		f := newNPMResultTest(t)
		npm := useFakeNPM(t, fakeNPMInstallBody)
		f.jr.checkWorkspace().npmCache = blockedPath(t)

		result := f.runCheck(t, npmInstallCheck("true"))

		if result["result"] != "error" || result["output"] != "dependency install for check check-0 could not prepare the npm cache" {
			t.Fatalf("result = %v", result)
		}
		if calls := npm.calls(t); len(calls) != 0 {
			t.Fatalf("npm ran: %v", calls)
		}
	})
	t.Run("before a directory install", func(t *testing.T) {
		f := newNPMResultTest(t)
		cache := npmCacheRoot(f.jr.d.cfg.StateDir, f.jr.workDir)
		dir := t.TempDir()
		parent := filepath.Dir(cache)
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
		script := "#!/bin/sh\nchmod 755 '" + cache + "' && chmod 500 '" + parent + "'\necho " + fakeNPMVersion + "\n"
		if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

		result := f.runCheck(t, npmInstallCheck("true"))

		if result["output"] != "dependency install for check check-0 could not prepare the npm cache" {
			t.Fatalf("result = %v", result)
		}
	})
}

func TestPrepareNPMCacheReportsFailures(t *testing.T) {
	if err := prepareNPMCache(blockedPath(t), 10); err == nil {
		t.Fatal("a cache below a file was prepared")
	}
	cache := filepath.Join(t.TempDir(), "cache")
	seedUnreadableCache(t, cache)
	if err := prepareNPMCache(cache, 1<<20); err != nil {
		t.Fatalf("an unmeasurable cache was not wiped: %v", err)
	}
	if entries, err := os.ReadDir(cache); err != nil || len(entries) != 0 {
		t.Fatalf("cache after wipe = %v, %v", entries, err)
	}
}

func TestANonPrivateNPMCacheIsReplaced(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "entry"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cache, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := prepareNPMCache(cache, 1<<20); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(cache)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("cache = %v, %v", info, err)
	}
	if entries, err := os.ReadDir(cache); err != nil || len(entries) != 0 {
		t.Fatalf("a shared cache was kept: %v, %v", entries, err)
	}
}

func TestAnUnusableNPMVersionStopsTheInstall(t *testing.T) {
	f := newNPMResultTest(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte("#!/bin/sh\nprintf '\\001'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result := f.runCheck(t, npmInstallCheck("true"))

	if result["output"] != "dependency install for check check-0 failed: npm reported an unusable version" {
		t.Fatalf("result = %v", result)
	}
}

func installFixtureRequest(t *testing.T, f agentResultTest) (checkRequest, *checkWorkspace, checkLocation, string) {
	t.Helper()
	commit := f.publish(t)
	workspace := f.jr.checkWorkspace()
	location := checkLocation{root: workspace.root, dirs: map[string]string{"repo-1": filepath.Join(workspace.root, "repositories", "repo-1")}}
	request := checkRequest{checkID: "lint", check: npmInstallCheck("true"), repository: f.repository()}
	return request, workspace, location, commit
}

func TestInstallFailsClosedWhenItsInputsAreUnavailable(t *testing.T) {
	f := newNPMResultTest(t)
	useFakeNPM(t, fakeNPMInstallBody)
	request, workspace, location, commit := installFixtureRequest(t, f)

	for _, tc := range []struct {
		name      string
		ctx       func() context.Context
		mutate    func(*checkRequest, *checkLocation)
		want      string
		wantClass string
	}{
		{"no trusted repository", context.Background, func(r *checkRequest, _ *checkLocation) { r.repository.Trusted = nil },
			"dependency install for check lint could not read the published tree", "internal"},
		{"cancelled before reading", func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, func(*checkRequest, *checkLocation) {}, "dependency install for check lint was cancelled", "cancelled"},
		{"unreadable commit", context.Background, func(r *checkRequest, _ *checkLocation) {},
			"dependency install for check lint could not read the published tree", "internal"},
		{"missing check root", context.Background, func(_ *checkRequest, l *checkLocation) { l.root = filepath.Join(t.TempDir(), "absent") },
			"dependency install for check lint could not be verified", "internal"},
		{"repository outside the workspace", context.Background, func(r *checkRequest, _ *checkLocation) { r.repository.WorkDir = t.TempDir() },
			"dependency install for check lint could not be verified", "internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			localRequest, localLocation := request, location
			localLocation.dirs = map[string]string{"repo-1": location.dirs["repo-1"]}
			tc.mutate(&localRequest, &localLocation)
			useCommit := commit
			if tc.name == "unreadable commit" {
				useCommit = strings.Repeat("0", 40)
			}
			if err := os.MkdirAll(location.dirs["repo-1"], 0o755); err != nil {
				t.Fatal(err)
			}

			_, outcome := f.jr.installCheckDependencies(tc.ctx(), localRequest, workspace, localLocation, useCommit)

			if outcome == nil || outcome.Result != "error" || outcome.Output != tc.want || outcome.Class != tc.wantClass {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}
}

func TestClassifyInstallContext(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for ctx, want := range map[context.Context]string{cancelled: "cancelled", expired: "timeout", context.Background(): "internal"} {
		if got := classifyInstallContext(ctx); got != want {
			t.Fatalf("classification = %s, want %s", got, want)
		}
	}
}

func TestInstallRefusalIsAnError(t *testing.T) {
	var err error = &installRefusal{message: "refused"}
	var refusal *installRefusal
	if err.Error() != "refused" || !errors.As(err, &refusal) {
		t.Fatalf("refusal = %v", err)
	}
}

func TestInstalledNodeModulesThatIsNotADirectory(t *testing.T) {
	for _, tc := range []struct {
		name, body, script string
		want               string
	}{
		{"a link inside the repository", "mkdir -p mods/pkg && ln -s mods node_modules", "test -L node_modules", "passed"},
		{"a regular file", "printf x > node_modules", "test -f node_modules", "passed"},
		{"a link outside the workspace", "ln -s /tmp node_modules", "true", "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNPMResultTest(t)
			useFakeNPM(t, tc.body)

			result := f.runCheck(t, npmInstallCheck(tc.script))

			if result["result"] != tc.want {
				t.Fatalf("result = %v", result)
			}
		})
	}
}

func TestInstalledFileDigestDetectsAChangedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	scan := &installedTreeScan{ctx: context.Background(), root: root}
	if _, err := scan.fileDigest("file", 2); err == nil || !strings.Contains(err.Error(), "changed while it was scanned") {
		t.Fatalf("err = %v", err)
	}
	if _, err := scan.fileDigest("absent", 1); err == nil {
		t.Fatal("a missing file was digested")
	}
}

func TestCheckWorkspaceFailuresBecomeTypedCheckErrors(t *testing.T) {
	t.Run("an unpreparable workspace", func(t *testing.T) {
		f := newAgentResultTest(t)
		f.jr.checkWorkspace().root = blockedPath(t)
		commit := f.publish(t)
		f.jr.job.RequiredChecks = []string{"lint"}
		f.jr.d.cfg.Checks = map[string]config.CheckConfig{"lint": shellCheck("true")}

		f.jr.runRequiredChecks(context.Background(), map[string]string{"repo-1": commit}, nil)

		events := f.events.byKind("test_results")
		if len(events) != 1 || events[0]["result"] != "error" ||
			!equalStrings(stringSlice(events[0]["failures"]), []string{checkWorkspaceUnavailable}) {
			t.Fatalf("events = %v", events)
		}
		if artifacts := uploadedArtifacts(t, f.jr); len(artifacts) != 0 {
			t.Fatalf("an unprepared check published provenance: %+v", artifacts)
		}
		if _, err := f.runCheckTry(t, shellCheck("true")); err == nil || err.Error() != checkWorkspaceUnavailable {
			t.Fatalf("iterative err = %v", err)
		}
	})
	t.Run("an agent result refusal", func(t *testing.T) {
		f := newAgentResultTest(t)
		f.jr.repositories[0].Trusted = nil
		f.jr.job.RequiredChecks = []string{"lint"}
		f.jr.d.cfg.Checks = map[string]config.CheckConfig{"lint": shellCheck("true")}

		f.jr.runRequiredChecks(context.Background(), map[string]string{"repo-1": f.base}, nil)

		events := f.events.byKind("test_results")
		want := "agent result refused in repository repo-1: the trusted result repository is not available"
		if len(events) != 1 || !equalStrings(stringSlice(events[0]["failures"]), []string{want}) {
			t.Fatalf("events = %v", events)
		}
	})
	t.Run("a cancelled preparation is not retried", func(t *testing.T) {
		f := newAgentResultTest(t)
		f.jr.checkWorkspace().root = blockedPath(t)
		results, err := f.jr.checkResults(context.Background(), nil, "test")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := f.jr.runCheckRequest(ctx, checkRequest{checkID: "lint", check: shellCheck("true"), repository: f.repository()}, results); err == nil {
			t.Fatal("a cancelled preparation succeeded")
		}
	})
}

func TestCheckWorkspaceWipeRemovesLockedTrees(t *testing.T) {
	workspace := &checkWorkspace{root: filepath.Join(t.TempDir(), "job-1.checks")}
	if err := workspace.wipe(); err != nil {
		t.Fatalf("wipe of an absent root: %v", err)
	}
	seedUnreadableCache(t, workspace.root)
	if err := workspace.wipe(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(workspace.root); !os.IsNotExist(err) {
		t.Fatalf("root survived: %v", err)
	}
}

func TestCheckRepositoryRelRefusesARepositoryOutsideTheWorkspace(t *testing.T) {
	jr, _ := testJobRunner(t)
	if _, err := jr.checkRepositoryRel(checkedOutRepository{ID: "x", WorkDir: t.TempDir()}); err == nil {
		t.Fatal("an outside repository was accepted")
	}
}
