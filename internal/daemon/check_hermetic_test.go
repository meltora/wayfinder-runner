package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/example/app-runner/internal/config"
)

const plantedShimScript = "node_modules/.bin/test-runner test.sh"

func plantTestRunnerShim(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "node_modules", ".bin", "test-runner")
	writeAgentFile(t, dir, "node_modules/.bin/test-runner", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func seedFailingPublishedTest(t *testing.T, origin string, withNPM bool) {
	t.Helper()
	writeAgentFile(t, origin, "test.sh", "exit 1\n")
	writeAgentFile(t, origin, ".gitignore", "node_modules/\n")
	if withNPM {
		seedNPMProject(t, origin)
	}
}

func shimCheck(withInstall bool) config.CheckConfig {
	if withInstall {
		return npmInstallCheck(plantedShimScript)
	}
	return shellCheck(plantedShimScript)
}

func TestPlantedDependencyShimCannotPassARequiredCheck(t *testing.T) {
	for _, tc := range []struct {
		name    string
		install bool
	}{
		{"without an install", false},
		{"with an npm install", true},
	} {
		t.Run(tc.name+" terminal", func(t *testing.T) {
			f := newAgentResultTestWith(t, func(origin string) { seedFailingPublishedTest(t, origin, tc.install) })
			useFakeNPM(t, fakeNPMInstallBody)
			plantTestRunnerShim(t, f.checkout)
			commit := f.publish(t)
			f.jr.job.RequiredChecks = []string{"shim"}
			f.jr.d.cfg.Checks = map[string]config.CheckConfig{"shim": shimCheck(tc.install)}

			if f.jr.runRequiredChecks(context.Background(), map[string]string{"repo-1": commit}, nil) {
				t.Fatal("a planted shim made the required checks pass")
			}

			events := f.events.byKind("test_results")
			last := events[len(events)-1]
			if last["result"] != "failed" || last["error_classification"] != "exit_nonzero" || last["commit_sha"] != commit {
				t.Fatalf("terminal event = %v", last)
			}
		})
		t.Run(tc.name+" iterative", func(t *testing.T) {
			f := newAgentResultTestWith(t, func(origin string) { seedFailingPublishedTest(t, origin, tc.install) })
			useFakeNPM(t, fakeNPMInstallBody)
			plantTestRunnerShim(t, f.checkout)

			result := f.runCheck(t, shimCheck(tc.install))

			assertCheckResult(t, result, "failed")
			events := f.events.byKind("test_results")
			if len(events) != 1 || events[0]["result"] != "failed" || events[0]["phase"] != "iterative" {
				t.Fatalf("iterative events = %v", events)
			}
		})
	}
}

func TestPublishedDependencyShimDecidesTheResult(t *testing.T) {
	f := newAgentResultTestWith(t, func(origin string) {
		writeAgentFile(t, origin, "test.sh", "exit 1\n")
		writeAgentFile(t, origin, ".gitignore", "node_modules/\n")
		plantTestRunnerShim(t, origin)
		buildWorkspaceGit(t, origin, "add", "-f", "node_modules/.bin/test-runner")
	})

	assertCheckResult(t, f.runCheck(t, shellCheck(plantedShimScript)), "passed")

	commit := f.publish(t)
	f.jr.job.RequiredChecks = []string{"shim"}
	f.jr.d.cfg.Checks = map[string]config.CheckConfig{"shim": shellCheck(plantedShimScript)}
	if !f.jr.runRequiredChecks(context.Background(), map[string]string{"repo-1": commit}, nil) {
		t.Fatalf("the published shim did not decide the result: %v", f.events.byKind("test_results"))
	}
}
