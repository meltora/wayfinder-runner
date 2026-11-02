package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/example/app-runner/internal/config"
)

func lockfileWithPackages(packages string) string {
	return `{"name":"fixture","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{"":{"name":"fixture","version":"1.0.0"}` + packages + `}}`
}

func addGitlink(t *testing.T, origin, rel string) {
	t.Helper()
	head := buildWorkspaceGit(t, origin, "rev-parse", "HEAD")
	if err := os.MkdirAll(filepath.Join(origin, filepath.FromSlash(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	buildWorkspaceGit(t, origin, "update-index", "--add", "--cacheinfo", "160000,"+head+","+rel)
}

func seedBaseCommit(t *testing.T, origin string) {
	t.Helper()
	buildWorkspaceGit(t, origin, "add", ".")
	buildWorkspaceGit(t, origin, "commit", "-q", "-m", "seed")
}

type installRefusalCase struct {
	name        string
	seed        func(t *testing.T, origin string)
	limit       int64
	directories []string
	want        string
}

func runInstallRefusalCase(t *testing.T, tc installRefusalCase) {
	t.Helper()
	f := newAgentResultTestWith(t, func(origin string) { tc.seed(t, origin) })
	npm := useFakeNPM(t, fakeNPMInstallBody)
	if tc.limit != 0 {
		f.jr.checkWorkspace().lockfileByteLimit = tc.limit
	}
	marker := filepath.Join(t.TempDir(), "ran")

	result := f.runCheck(t, npmInstallCheck("touch '"+marker+"'", tc.directories...))

	if result["result"] != "error" || result["error_classification"] != "internal" || result["output"] != tc.want {
		t.Fatalf("result = %v\nwant output %q", result, tc.want)
	}
	if calls := npm.calls(t); len(calls) != 0 {
		t.Fatalf("npm ran after a refusal: %v", calls)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the check command ran after an install refusal")
	}
}

func TestInstallInputsAreRefusedBeforeAnythingRuns(t *testing.T) {
	prefix := "dependency install for check check-0 "
	for _, tc := range []installRefusalCase{
		{name: "missing lockfile", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package.json", fixturePackageJSON)
		}, want: prefix + "needs package-lock.json or npm-shrinkwrap.json in the repository root; publish the lockfile"},
		{name: "missing lockfile in a directory", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "web/package.json", fixturePackageJSON)
		}, directories: []string{"web"}, want: prefix + "needs package-lock.json or npm-shrinkwrap.json in web; publish the lockfile"},
		{name: "missing package.json", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package-lock.json", fixtureLockfile)
		}, want: prefix + "needs package.json in the repository root"},
		{name: "missing directory", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin)
		}, directories: []string{"absent"}, want: prefix + "needs package-lock.json or npm-shrinkwrap.json in absent; publish the lockfile"},
		{name: "directory reached through a symbolic link", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin, "real")
			if err := os.Symlink("real", filepath.Join(origin, "web")); err != nil {
				t.Fatal(err)
			}
		}, directories: []string{"web"}, want: prefix + "needs package-lock.json or npm-shrinkwrap.json in web; publish the lockfile"},
		{name: "symbolic link lockfile", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package.json", fixturePackageJSON)
			writeAgentFile(t, origin, "real-lock.json", fixtureLockfile)
			if err := os.Symlink("real-lock.json", filepath.Join(origin, "package-lock.json")); err != nil {
				t.Fatal(err)
			}
		}, want: prefix + "cannot use package-lock.json: it is not a regular file in the published tree"},
		{name: "directory package.json", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "web/package-lock.json", fixtureLockfile)
			writeAgentFile(t, origin, "web/package.json/inner", "x")
		}, directories: []string{"web"}, want: prefix + "cannot use web/package.json: it is not a regular file in the published tree"},
		{name: "gitlink lockfile", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package.json", fixturePackageJSON)
			seedBaseCommit(t, origin)
			addGitlink(t, origin, "package-lock.json")
		}, want: prefix + "cannot use package-lock.json: it is not a regular file in the published tree"},
		{name: "oversized lockfile", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin)
		}, limit: 32, want: prefix + "cannot use package-lock.json: it is larger than 32 bytes"},
		{name: "invalid JSON", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package.json", fixturePackageJSON)
			writeAgentFile(t, origin, "package-lock.json", "{not json")
		}, want: prefix + "cannot use package-lock.json: it is not an npm lockfile of version 2 or 3"},
		{name: "missing lockfileVersion", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package.json", fixturePackageJSON)
			writeAgentFile(t, origin, "package-lock.json", `{"packages":{}}`)
		}, want: prefix + "cannot use package-lock.json: it is not an npm lockfile of version 2 or 3"},
		{name: "lockfileVersion 1", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package.json", fixturePackageJSON)
			writeAgentFile(t, origin, "package-lock.json", `{"lockfileVersion":1,"dependencies":{}}`)
		}, want: prefix + "cannot use package-lock.json: it is not an npm lockfile of version 2 or 3"},
		{name: "string lockfileVersion", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package.json", fixturePackageJSON)
			writeAgentFile(t, origin, "package-lock.json", `{"lockfileVersion":"3","packages":{}}`)
		}, want: prefix + "cannot use package-lock.json: it is not an npm lockfile of version 2 or 3"},
		{name: "array lockfile", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package.json", fixturePackageJSON)
			writeAgentFile(t, origin, "package-lock.json", `[3]`)
		}, want: prefix + "cannot use package-lock.json: it is not an npm lockfile of version 2 or 3"},
		{name: "registry package without integrity", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package.json", fixturePackageJSON)
			writeAgentFile(t, origin, "package-lock.json", lockfileWithPackages(
				`,"node_modules/a-ok":{"version":"1.0.0","resolved":"https://registry.npmjs.org/a-ok/-/a-ok-1.0.0.tgz","integrity":"sha512-AAAA"}`+
					`,"node_modules/left-pad":{"version":"1.3.0","resolved":"HTTPS://registry.npmjs.org/left-pad/-/left-pad-1.3.0.tgz"}`))
		}, want: prefix + "cannot use package-lock.json: the registry package node_modules/left-pad has no integrity"},
		{name: "plain HTTP package with blank integrity", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package.json", fixturePackageJSON)
			writeAgentFile(t, origin, "package-lock.json", lockfileWithPackages(
				`,"node_modules/x":{"version":"1.0.0","resolved":"http://mirror.example/x.tgz","integrity":" "}`))
		}, want: prefix + "cannot use package-lock.json: the registry package node_modules/x has no integrity"},
		{name: "published node_modules file", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin)
			writeAgentFile(t, origin, "node_modules/pkg/index.js", "published\n")
		}, want: prefix + "cannot install into the repository root: the published tree has node_modules there; the runner installs it from the lockfile"},
		{name: "published node_modules symbolic link", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin, "web")
			if err := os.Symlink("../shared", filepath.Join(origin, "web", "node_modules")); err != nil {
				t.Fatal(err)
			}
		}, directories: []string{"web"}, want: prefix + "cannot install into web: the published tree has node_modules there; the runner installs it from the lockfile"},
		{name: "published node_modules gitlink", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin)
			seedBaseCommit(t, origin)
			addGitlink(t, origin, "node_modules")
		}, want: prefix + "cannot install into the repository root: the published tree has node_modules there; the runner installs it from the lockfile"},
		{name: "invalid shrinkwrap takes precedence over a valid package lock", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin)
			writeAgentFile(t, origin, "npm-shrinkwrap.json", "{}")
		}, want: prefix + "cannot use npm-shrinkwrap.json: it is not an npm lockfile of version 2 or 3"},
		{name: "the second of three directories is invalid", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin, "a", "c")
			writeAgentFile(t, origin, "b/package.json", fixturePackageJSON)
		}, directories: []string{"a", "b", "c"}, want: prefix + "needs package-lock.json or npm-shrinkwrap.json in b; publish the lockfile"},
	} {
		t.Run(tc.name, func(t *testing.T) { runInstallRefusalCase(t, tc) })
	}
}

func TestInstallInputsAcceptExemptAndShrinkwrapLockfiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed func(t *testing.T, origin string)
	}{
		{"file, git and link entries need no integrity", func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package.json", fixturePackageJSON)
			writeAgentFile(t, origin, "package-lock.json", lockfileWithPackages(
				`,"node_modules/local":{"resolved":"file:vendor/local-1.0.0.tgz"}`+
					`,"node_modules/fromgit":{"resolved":"git+ssh://git@example.test/x.git#abc"}`+
					`,"node_modules/linked":{"resolved":"https://example.test/linked","link":true}`+
					`,"node_modules/bundled":{"version":"1.0.0","inBundle":true}`))
		}},
		{"version 2 lockfile", func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package.json", fixturePackageJSON)
			writeAgentFile(t, origin, "package-lock.json", `{"lockfileVersion":2,"packages":{}}`)
		}},
		{"a valid shrinkwrap wins over an invalid package lock", func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package.json", fixturePackageJSON)
			writeAgentFile(t, origin, "package-lock.json", "{broken")
			writeAgentFile(t, origin, "npm-shrinkwrap.json", fixtureLockfile)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentResultTestWith(t, func(origin string) { tc.seed(t, origin) })
			npm := useFakeNPM(t, fakeNPMInstallBody)

			assertCheckResult(t, f.runCheck(t, npmInstallCheck("test -f node_modules/fixture-dep/index.js")), "passed")
			if got := len(npm.ciCalls(t)); got != 1 {
				t.Fatalf("npm ci ran %d times", got)
			}
		})
	}
}

func TestInstallInputDefaultsMatchTheContract(t *testing.T) {
	jr, _ := testJobRunner(t)
	workspace := jr.checkWorkspace()
	if workspace.lockfileByteLimit != 67108864 || workspace.npmCacheByteLimit != 4<<30 ||
		workspace.entryLimit != 1_000_000 || workspace.byteLimit != 4<<30 || workspace.depthLimit != 256 {
		t.Fatalf("workspace limits = %+v", workspace)
	}
	install := config.CheckInstallConfig{Ecosystem: config.CheckInstallEcosystemNPM}
	if install.InstallTimeoutSeconds() != 600 {
		t.Fatalf("install timeout = %d", install.InstallTimeoutSeconds())
	}
}
