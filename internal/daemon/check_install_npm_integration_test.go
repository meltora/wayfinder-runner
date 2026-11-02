package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
)

const (
	runnerToolBin   = "#!/usr/bin/env node\nconst fs = require('fs');\nprocess.exit(fs.readFileSync(process.argv[2], 'utf8').includes('ok') ? 0 : 1);\n"
	ghostTestSource = "require('ghost');\n"
)

func requireRealNPM(t *testing.T) {
	t.Helper()
	_, npmErr := exec.LookPath("npm")
	_, nodeErr := exec.LookPath("node")
	if npmErr == nil && nodeErr == nil {
		return
	}
	if os.Getenv("RUNNER_REQUIRE_NPM_TESTS") != "" {
		t.Fatalf("RUNNER_REQUIRE_NPM_TESTS is set but npm or node is missing: %v %v", npmErr, nodeErr)
	}
	t.Skip("npm is not on PATH and RUNNER_REQUIRE_NPM_TESTS is unset")
}

func runNPMForFixture(t *testing.T, dir, cache string, args ...string) {
	t.Helper()
	command := exec.Command("npm", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "npm_config_cache="+cache, "npm_config_update_notifier=false", "npm_config_fund=false", "npm_config_audit=false")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("npm %v: %v\n%s", args, err, output)
	}
}

func seedRunnerToolProject(t *testing.T, origin, spec string) {
	t.Helper()
	cache := t.TempDir()
	tool := t.TempDir()
	writeAgentFile(t, tool, "package.json", `{"name":"runner-tool","version":"1.0.0","bin":{"runner-tool":"bin.js"}}`+"\n")
	writeAgentFile(t, tool, "bin.js", runnerToolBin)
	if err := os.Chmod(filepath.Join(tool, "bin.js"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(origin, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	runNPMForFixture(t, tool, cache, "pack", "--pack-destination", filepath.Join(origin, "vendor"))
	writeAgentFile(t, origin, "package.json", `{"name":"fixture","version":"1.0.0","private":true,`+
		`"scripts":{"test":"runner-tool spec.txt"},"dependencies":{"runner-tool":"file:vendor/runner-tool-1.0.0.tgz"}}`+"\n")
	runNPMForFixture(t, origin, cache, "install", "--package-lock-only", "--ignore-scripts", "--offline")
	assertPublishedToolHasIntegrity(t, origin)
	writeAgentFile(t, origin, "spec.txt", spec+"\n")
	writeAgentFile(t, origin, "ghost.test.js", ghostTestSource)
	writeAgentFile(t, origin, ".gitignore", "node_modules/\n")
}

func assertPublishedToolHasIntegrity(t *testing.T, origin string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(origin, "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lockfile struct {
		LockfileVersion int `json:"lockfileVersion"`
		Packages        map[string]struct {
			Resolved  string `json:"resolved"`
			Integrity string `json:"integrity"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &lockfile); err != nil {
		t.Fatal(err)
	}
	tool := lockfile.Packages["node_modules/runner-tool"]
	if lockfile.LockfileVersion != 3 || tool.Resolved != "file:vendor/runner-tool-1.0.0.tgz" || !strings.HasPrefix(tool.Integrity, "sha512-") {
		t.Fatalf("fixture lockfile = %s", raw)
	}
}

func plantPatchedRunnerTool(t *testing.T, checkout string) {
	t.Helper()
	writeAgentFile(t, checkout, "node_modules/runner-tool/package.json", `{"name":"runner-tool","version":"1.0.0","bin":{"runner-tool":"bin.js"}}`+"\n")
	writeAgentFile(t, checkout, "node_modules/runner-tool/bin.js", "#!/usr/bin/env node\nprocess.exit(0);\n")
	if err := os.Chmod(filepath.Join(checkout, "node_modules", "runner-tool", "bin.js"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(checkout, "node_modules", ".bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../runner-tool/bin.js", filepath.Join(checkout, "node_modules", ".bin", "runner-tool")); err != nil {
		t.Fatal(err)
	}
}

func plantGhostPackage(t *testing.T, checkout string) {
	t.Helper()
	writeAgentFile(t, checkout, "node_modules/ghost/package.json", `{"name":"ghost","version":"1.0.0","main":"index.js"}`+"\n")
	writeAgentFile(t, checkout, "node_modules/ghost/index.js", "module.exports = 1;\n")
}

type npmScenario struct {
	name  string
	spec  string
	agent func(t *testing.T, checkout string)
	check config.CheckConfig
	want  string
	cause string
}

func npmTestCheck() config.CheckConfig {
	return config.CheckConfig{Command: "npm", Args: []string{"test"}, TimeoutSeconds: 300,
		Install: &config.CheckInstallConfig{Ecosystem: config.CheckInstallEcosystemNPM}}
}

func npmScenarios() []npmScenario {
	ghost := npmTestCheck()
	ghost.Command, ghost.Args = "node", []string{"ghost.test.js"}
	return []npmScenario{
		{"a published passing spec passes", "ok", nil, npmTestCheck(), "passed", ""},
		{"a patched ignored tool cannot pass a bad spec", "bad", plantPatchedRunnerTool, npmTestCheck(), "failed", "runner-tool spec.txt"},
		{"an ignored package outside the lockfile is absent", "ok", plantGhostPackage, ghost, "failed", "Cannot find module 'ghost'"},
	}
}

func TestRealNPMInstallsOnlyThePublishedLockfile(t *testing.T) {
	requireRealNPM(t)
	for _, tc := range npmScenarios() {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			f := newAgentResultTestWith(t, func(origin string) { seedRunnerToolProject(t, origin, tc.spec) })
			if tc.agent != nil {
				tc.agent(t, f.checkout)
			}

			result := f.runCheck(t, tc.check)

			output, _ := result["output"].(string)
			if result["result"] != tc.want || !strings.Contains(output, tc.cause) {
				t.Fatalf("result = %v, want %s caused by %q", result, tc.want, tc.cause)
			}
		})
	}
}
