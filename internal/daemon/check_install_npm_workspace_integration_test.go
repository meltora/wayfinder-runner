package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func packFixtureDependency(t *testing.T, origin, cache, version string) {
	t.Helper()
	dir := t.TempDir()
	writeAgentFile(t, dir, "package.json", `{"name":"dep","version":"`+version+`","bin":{"dep":"index.js"}}`+"\n")
	writeAgentFile(t, dir, "index.js", "#!/usr/bin/env node\nmodule.exports = '"+version+"';\n")
	if err := os.MkdirAll(filepath.Join(origin, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	runNPMForFixture(t, dir, cache, "pack", "--pack-destination", filepath.Join(origin, "vendor"))
}

func seedNestedWorkspaceProject(t *testing.T, origin string) {
	t.Helper()
	cache := t.TempDir()
	packFixtureDependency(t, origin, cache, "1.0.0")
	packFixtureDependency(t, origin, cache, "2.0.0")
	writeAgentFile(t, origin, "package.json", `{"name":"root","version":"1.0.0","private":true,"workspaces":["packages/a"],"dependencies":{"dep":"file:vendor/dep-1.0.0.tgz"}}`+"\n")
	writeAgentFile(t, origin, "packages/a/package.json", `{"name":"a","version":"1.0.0","dependencies":{"dep":"file:../../vendor/dep-2.0.0.tgz"}}`+"\n")
	runNPMForFixture(t, origin, cache, "install", "--package-lock-only", "--ignore-scripts", "--offline")
	writeAgentFile(t, origin, ".gitignore", "node_modules/\n")
	raw, err := os.ReadFile(filepath.Join(origin, "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"packages/a/node_modules/dep"`) {
		t.Fatalf("the fixture lockfile has no nested dependency: %s", raw)
	}
}

func TestRealNPMInstallsAWorkspaceRootWithNestedDependencies(t *testing.T) {
	requireRealNPM(t)
	t.Setenv("HOME", t.TempDir())
	f := newAgentResultTestWith(t, func(origin string) { seedNestedWorkspaceProject(t, origin) })

	result := f.runCheck(t, npmInstallCheck(
		"grep -q 2.0.0 packages/a/node_modules/dep/index.js && grep -q 1.0.0 node_modules/dep/index.js && test -L node_modules/a"))

	assertCheckResult(t, result, "passed")
}

func TestRealNPMRefusesAWorkspaceFolderAsTheInstallDirectory(t *testing.T) {
	requireRealNPM(t)
	t.Setenv("HOME", t.TempDir())
	f := newAgentResultTestWith(t, func(origin string) {
		seedNestedWorkspaceProject(t, origin)
		writeAgentFile(t, origin, "packages/a/package-lock.json", fixtureLockfile)
	})

	result := f.runCheck(t, npmInstallCheck("true", "packages/a"))

	want := "dependency install for check check-0 cannot install into packages/a: package.json makes npm treat packages/a as part of another project; install the repository root instead"
	if result["result"] != "error" || result["output"] != want {
		t.Fatalf("result = %v", result)
	}
}
