package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/example/app-runner/internal/config"
)

const workspaceRootPackageJSON = `{"name":"root","version":"1.0.0","workspaces":["packages/web"]}` + "\n"

func TestAncestorPackageJSONIsRefusedBeforeAnythingRuns(t *testing.T) {
	prefix := "dependency install for check check-0 "
	for _, tc := range []installRefusalCase{
		{name: "workspace root lists the install directory", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin, "packages/web")
			writeAgentFile(t, origin, "package.json", workspaceRootPackageJSON)
			writeAgentFile(t, origin, "package-lock.json", lockfileWithPackages(`,"node_modules/evil":{"version":"1.0.0","resolved":"http://127.0.0.1:1/evil-1.0.0.tgz"}`))
		}, directories: []string{"packages/web"}, want: prefix + "cannot install into packages/web: package.json makes npm treat packages/web as part of another project; install the repository root instead"},
		{name: "workspace root with a glob and a shrinkwrap in the install directory", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "packages/web/package.json", fixturePackageJSON)
			writeAgentFile(t, origin, "packages/web/npm-shrinkwrap.json", fixtureLockfile)
			writeAgentFile(t, origin, "package.json", `{"name":"root","workspaces":["packages/*"]}`)
		}, directories: []string{"packages/web"}, want: prefix + "cannot install into packages/web: package.json makes npm treat packages/web as part of another project; install the repository root instead"},
		{name: "ancestor package.json without workspaces", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin, "packages/web")
			writeAgentFile(t, origin, "package.json", fixturePackageJSON)
		}, directories: []string{"packages/web"}, want: prefix + "cannot install into packages/web: package.json makes npm treat packages/web as part of another project; install the repository root instead"},
		{name: "intermediate ancestor", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin, "a/b/c")
			writeAgentFile(t, origin, "a/b/package.json", fixturePackageJSON)
		}, directories: []string{"a/b/c"}, want: prefix + "cannot install into a/b/c: a/b/package.json makes npm treat a/b/c as part of another project; install a/b instead"},
		{name: "the shallowest ancestor is reported", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin, "a/b/c")
			writeAgentFile(t, origin, "a/b/package.json", fixturePackageJSON)
			writeAgentFile(t, origin, "a/package.json", fixturePackageJSON)
		}, directories: []string{"a/b/c"}, want: prefix + "cannot install into a/b/c: a/package.json makes npm treat a/b/c as part of another project; install a instead"},
		{name: "ancestor package.json as a directory", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin, "web/app")
			writeAgentFile(t, origin, "web/package.json/inner", "x")
		}, directories: []string{"web/app"}, want: prefix + "cannot install into web/app: web/package.json makes npm treat web/app as part of another project; install web instead"},
		{name: "ancestor package.json as a symbolic link", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin, "web/app")
			if err := os.Symlink("app/package.json", filepath.Join(origin, "web", "package.json")); err != nil {
				t.Fatal(err)
			}
		}, directories: []string{"web/app"}, want: prefix + "cannot install into web/app: web/package.json makes npm treat web/app as part of another project; install web instead"},
		{name: "ancestor package.json added by the agent", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin, "packages/web")
		}, agent: func(t *testing.T, checkout string) {
			agentCommit(t, checkout, "package.json", workspaceRootPackageJSON)
		}, directories: []string{"packages/web"}, want: prefix + "cannot install into packages/web: package.json makes npm treat packages/web as part of another project; install the repository root instead"},
		{name: "only the second of three directories has an ancestor package.json", seed: func(t *testing.T, origin string) {
			seedNPMProject(t, origin, "a", "b/web", "c")
			writeAgentFile(t, origin, "b/package.json", fixturePackageJSON)
		}, directories: []string{"a", "b/web", "c"}, want: prefix + "cannot install into b/web: b/package.json makes npm treat b/web as part of another project; install b instead"},
		{name: "packageManager that is not npm", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package.json", `{"name":"fixture","packageManager":"pnpm@9.0.0"}`)
			writeAgentFile(t, origin, "package-lock.json", fixtureLockfile)
		}, want: prefix + "cannot use package.json: its packageManager is not npm"},
		{name: "packageManager that is not a string", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "web/package.json", `{"name":"fixture","packageManager":["npm@10.9.4"]}`)
			writeAgentFile(t, origin, "web/package-lock.json", fixtureLockfile)
		}, directories: []string{"web"}, want: prefix + "cannot use web/package.json: its packageManager is not npm"},
		{name: "packageManager with only the npm prefix", seed: func(t *testing.T, origin string) {
			writeAgentFile(t, origin, "package.json", `{"name":"fixture","packageManager":"npm@"}`)
			writeAgentFile(t, origin, "package-lock.json", fixtureLockfile)
		}, want: prefix + "cannot use package.json: its packageManager is not npm"},
	} {
		t.Run(tc.name, func(t *testing.T) { runInstallRefusalCase(t, tc) })
	}
}

func TestPackageJSONCandidatesAreTheProperAncestors(t *testing.T) {
	for directory, want := range map[string][]string{
		".":       nil,
		"web":     {"package.json"},
		"a/b/c":   {"package.json", "a/package.json", "a/b/package.json"},
		"@s/pkg":  {"package.json", "@s/package.json"},
		"web/app": {"package.json", "web/package.json"},
	} {
		if got := ancestorCandidates(directory, npmPackageManifestFile, false); !slices.Equal(got, want) {
			t.Fatalf("%s: candidates = %v, want %v", directory, got, want)
		}
	}
}

func TestPackageManagerMustNameNPM(t *testing.T) {
	for content, want := range map[string]bool{
		`{}`:                              true,
		`{"packageManager":"npm@10.9.4"}`: true,
		`{"packageManager":"npm@12.0.2+sha512.abc"}`: true,
		`{"packageManager":"npm@"}`:                  false,
		`{"packageManager":"pnpm@9.0.0"}`:            false,
		`{"packageManager":"yarn@4.0.0"}`:            false,
		`{"packageManager":"NPM@10.9.4"}`:            false,
		`{"packageManager":null}`:                    false,
		`{"packageManager":""}`:                      false,
	} {
		manifest, ok := parseNPMManifest([]byte(content))
		if !ok || manifest.npmPackageManager != want {
			t.Fatalf("%s: npm = %v ok = %v, want %v", content, manifest.npmPackageManager, ok, want)
		}
	}
}

func TestLockfileInstallRootsAreTheLocalWorkspaceAndNestedFolders(t *testing.T) {
	packages := map[string]npmLockedPackage{}
	for _, key := range []string{"", "node_modules/a", "node_modules/a/node_modules/b", "packages/web", "packages/web/node_modules/c",
		"tools/node_modules/d", "../outside", "../outside/node_modules/e", "/abs/node_modules/f", "x/./y", "x//y", ".GIT/node_modules/g", "packages/web/"} {
		packages[key] = npmLockedPackage{}
	}
	if got, want := lockfileInstallRoots(packages), []string{"packages/web", "tools"}; !slices.Equal(got, want) {
		t.Fatalf("roots = %v, want %v", got, want)
	}
}

func TestTheInstallGuardIsBounded(t *testing.T) {
	prefix := "dependency install for check check-0 could not compare the check workspace: it "
	for _, tc := range []struct {
		name  string
		limit func(*installGuard)
		want  string
	}{
		{"entries", func(g *installGuard) { g.entryLimit = 2 }, prefix + "has more than 2 entries"},
		{"depth", func(g *installGuard) { g.depthLimit = 2 }, prefix + "is more than 2 directories deep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "a", "b", "c"), 0o700); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			guard := &installGuard{ctx: context.Background(), root: root, checkID: "check-0", entryLimit: 100, depthLimit: 100}
			tc.limit(guard)
			if _, err := guard.snapshot(); err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	t.Run("defaults", func(t *testing.T) {
		jr, _ := testJobRunner(t)
		guard := jr.checkWorkspace().newInstallGuard(context.Background(), nil, "check-0")
		if guard.entryLimit != 4_000_000 || guard.depthLimit != 1024 {
			t.Fatalf("guard limits = %d %d", guard.entryLimit, guard.depthLimit)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "f"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		guard := &installGuard{ctx: ctx, root: root, checkID: "check-0", entryLimit: 100, depthLimit: 100}
		if _, err := guard.snapshot(); err == nil {
			t.Fatal("a cancelled snapshot succeeded")
		}
	})
}

func TestTheInstallGuardReportsUnreadableEntries(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "file", "content")
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	guard := &installGuard{ctx: context.Background(), root: root, checkID: "check-0", entryLimit: 100, depthLimit: 100}
	info, err := root.Lstat("file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.describe("file", symlinkInfo{info}, true); err == nil {
		t.Fatal("a regular file was read as a symlink")
	}
	if err := os.Chmod(filepath.Join(dir, "file"), 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(filepath.Join(dir, "file"), 0o600) }()
	if _, err := guard.snapshot(); err == nil {
		t.Fatal("an unreadable file was hashed")
	}
}

type symlinkInfo struct{ os.FileInfo }

func (symlinkInfo) Mode() os.FileMode { return os.ModeSymlink | 0o777 }

func TestVerificationFailuresMapToTheirResults(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	prefix := "dependency install for check check-0 "
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		err    error
		result string
		class  string
		output string
	}{
		{"refusal", context.Background(), &installRefusal{message: "refused here"}, installResultRefused, "internal", "refused here"},
		{"cancelled", cancelled, os.ErrClosed, installResultCancelled, "cancelled", prefix + "was cancelled"},
		{"other", context.Background(), os.ErrClosed, installResultRefused, "internal", prefix + "could not be verified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			install := &checkInstall{ctx: tc.ctx, request: checkRequest{checkID: "check-0"}}
			if got := install.verificationFailed(tc.err); got != tc.result {
				t.Fatalf("result = %q, want %q", got, tc.result)
			}
			if out := install.refusalOut; out == nil || out.Class != tc.class || out.Output != tc.output {
				t.Fatalf("outcome = %+v", out)
			}
		})
	}
}

func TestAWorkspaceRootInstallsWhenItIsTheInstallDirectory(t *testing.T) {
	f := newAgentResultTestWith(t, func(origin string) {
		writeAgentFile(t, origin, "package.json", workspaceRootPackageJSON)
		writeAgentFile(t, origin, "package-lock.json", lockfileWithPackages(
			`,"node_modules/web":{"resolved":"packages/web","link":true}`+
				`,"packages/web":{"name":"web","version":"1.0.0"}`+
				`,"packages/web/node_modules/nested":{"version":"1.0.0","resolved":`+testHTTPSTgz+`,"integrity":`+testIntegrity+`}`))
		writeAgentFile(t, origin, "packages/web/package.json", `{"name":"web","version":"1.0.0","packageManager":"npm@10.9.4"}`)
	})
	npm := useFakeNPM(t, fakeNPMInstallBody+" && mkdir -p packages/web/node_modules/nested packages/web/node_modules/.bin && echo n > packages/web/node_modules/nested/index.js && ln -s ../nested/index.js packages/web/node_modules/.bin/nested && ln -s ../packages/web node_modules/web")

	result := f.runCheck(t, npmInstallCheck("test -f node_modules/fixture-dep/index.js && test -f packages/web/node_modules/nested/index.js"))
	assertCheckResult(t, result, "passed")
	if got := len(npm.ciCalls(t)); got != 1 {
		t.Fatalf("npm ci ran %d times", got)
	}
}

func TestInstallChangesOutsideTheInstalledTreesAreRefused(t *testing.T) {
	prefix := "dependency install for check check-0 "
	for _, tc := range []struct {
		name        string
		seed        func(t *testing.T, origin string)
		body        string
		directories []string
		want        string
	}{
		{name: "a node_modules above the install directory", body: fakeNPMInstallBody + " && mkdir -p ../node_modules/evil && echo x > ../node_modules/evil/bin.js",
			directories: []string{"web"}, want: prefix + "changed node_modules outside web/node_modules"},
		{name: "a published file in the install directory", body: fakeNPMInstallBody + " && echo changed >> package.json",
			want: prefix + "changed package.json outside node_modules"},
		{name: "a published file with the same size", body: fakeNPMInstallBody + " && printf '%s' \"$(tr 'f' 'g' < package.json)\" > p && cat p > package.json && rm p",
			want: prefix + "changed package.json outside node_modules"},
		{name: "a removed published file", body: fakeNPMInstallBody + " && rm package-lock.json",
			want: prefix + "changed package-lock.json outside node_modules"},
		{name: "a new file beside the install directory", body: fakeNPMInstallBody + " && echo x > ../planted.txt",
			directories: []string{"web"}, want: prefix + "changed planted.txt outside web/node_modules"},
		{name: "a node_modules in a folder the lockfile does not name", body: fakeNPMInstallBody + " && mkdir -p src/node_modules/evil && echo x > src/node_modules/evil/index.js",
			want: prefix + "changed src/node_modules outside node_modules"},
		{name: "a published file made executable", body: fakeNPMInstallBody + " && chmod u+x package.json",
			want: prefix + "changed package.json outside node_modules"},
		{name: "a published file replaced by a symbolic link", body: fakeNPMInstallBody + " && rm package.json && ln -s package-lock.json package.json",
			want: prefix + "changed package.json outside node_modules"},
		{name: "an earlier directory's node_modules", seed: func(t *testing.T, origin string) { seedNPMProject(t, origin, "a", "b") },
			body:        "if [ \"$(basename \"$PWD\")\" = b ]; then echo evil > ../a/node_modules/fixture-dep/index.js; fi; " + fakeNPMInstallBody,
			directories: []string{"a", "b"}, want: prefix + "changed a/node_modules/fixture-dep/index.js outside b/node_modules"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed := tc.seed
			if seed == nil {
				seed = func(t *testing.T, origin string) { seedNPMProject(t, origin, tc.directories...) }
			}
			f := newAgentResultTestWith(t, func(origin string) {
				seed(t, origin)
				writeAgentFile(t, origin, "src/index.js", "module.exports = 1\n")
			})
			useFakeNPM(t, tc.body)
			marker := filepath.Join(t.TempDir(), "ran")

			result := f.runCheck(t, npmInstallCheck("touch '"+marker+"'", tc.directories...))

			if result["result"] != "error" || result["error_classification"] != "internal" || result["output"] != tc.want {
				t.Fatalf("result = %v\nwant output %q", result, tc.want)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("the check command ran after an install changed the workspace")
			}
		})
	}
}

func TestNestedInstalledTreesAreScannedAndDigested(t *testing.T) {
	prefix := "dependency install for check check-0 "
	lockfile := lockfileWithPackages(`,"packages/web":{"name":"web"},"packages/web/node_modules/nested":{"version":"1.0.0","resolved":` + testHTTPSTgz + `,"integrity":` + testIntegrity + `}`)
	seed := func(t *testing.T, origin string) {
		writeAgentFile(t, origin, "package.json", fixturePackageJSON)
		writeAgentFile(t, origin, "package-lock.json", lockfile)
		writeAgentFile(t, origin, "packages/web/package.json", `{"name":"web"}`)
	}
	t.Run("a symbolic link that leaves the check workspace", func(t *testing.T) {
		f := newAgentResultTestWith(t, func(origin string) { seed(t, origin) })
		useFakeNPM(t, fakeNPMInstallBody+" && mkdir -p packages/web/node_modules && ln -s /etc packages/web/node_modules/nested")
		result := f.runCheck(t, npmInstallCheck("true"))
		want := prefix + "produced a symbolic link that leaves the check workspace: packages/web/node_modules/nested"
		if result["result"] != "error" || result["output"] != want {
			t.Fatalf("result = %v\nwant output %q", result, want)
		}
	})
	t.Run("the nested tree changes the digest", func(t *testing.T) {
		digests := map[string]string{}
		for _, body := range []string{"one", "two"} {
			f := newAgentResultTestWith(t, func(origin string) { seed(t, origin) })
			useFakeNPM(t, fakeNPMInstallBody+" && mkdir -p packages/web/node_modules/nested && echo "+body+" > packages/web/node_modules/nested/index.js")
			f.publish(t)
			f.jr.job.RequiredChecks = []string{"lint"}
			f.jr.d.cfg.Checks = map[string]config.CheckConfig{"lint": npmInstallCheck("true")}
			results, err := f.jr.checkResults(context.Background(), nil, "test")
			if err != nil {
				t.Fatal(err)
			}
			report, err := f.jr.runCheckRequest(context.Background(), checkRequest{checkID: "lint", check: f.jr.d.cfg.Checks["lint"], repository: f.repository()}, results)
			if err != nil || report.install == nil || report.install.Directories[0].TreeDigest == nil {
				t.Fatalf("report = %+v, %v", report, err)
			}
			if got := *report.install.Directories[0].Entries; got != 4 {
				t.Fatalf("entries = %d, want the top and the nested tree", got)
			}
			digests[body] = *report.install.Directories[0].TreeDigest
		}
		if digests["one"] == digests["two"] {
			t.Fatal("the nested tree did not change the digest")
		}
	})
}

func TestTheVersionProbeRunsInEveryInstallDirectory(t *testing.T) {
	t.Run("one version", func(t *testing.T) {
		f := newNPMResultTest(t, "a", "b")
		npm := useFakeNPM(t, fakeNPMInstallBody)
		assertCheckResult(t, f.runCheck(t, npmInstallCheck("true", "a", "b")), "passed")
		var dirs []string
		for _, call := range npm.calls(t) {
			if call.args == "--version" {
				dirs = append(dirs, filepath.Base(call.dir))
			}
		}
		if !slices.Equal(dirs, []string{"a", "b"}) {
			t.Fatalf("version probes ran in %v", dirs)
		}
	})
	t.Run("different versions", func(t *testing.T) {
		f := newNPMResultTest(t, "a", "b")
		dir := t.TempDir()
		script := "#!/bin/sh\nif [ \"$1\" = --version ]; then if [ \"$(basename \"$PWD\")\" = b ]; then echo 11.0.0; else echo 12.0.2; fi; exit 0; fi\n" + fakeNPMInstallBody + "\n"
		if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		result := f.runCheck(t, npmInstallCheck("true", "a", "b"))
		want := "dependency install for check check-0 needs one npm version; the check environment has npm 12.0.2 in a and npm 11.0.0 in b"
		if result["result"] != "error" || result["output"] != want {
			t.Fatalf("result = %v\nwant output %q", result, want)
		}
	})
}
