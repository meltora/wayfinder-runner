package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/example/app-runner/internal/isolation"
)

func useDockerChecks(t *testing.T, f agentResultTest) {
	t.Helper()
	image := os.Getenv("JOB_IMAGE")
	if image == "" {
		t.Fatal("JOB_IMAGE is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const runnerID = "rjic-check-install-test"
	mapping, err := isolation.ResolveUserMapping(ctx, image, runnerID)
	if err != nil {
		t.Fatal(err)
	}
	jr := f.jr
	jr.d.runnerID = runnerID
	jr.d.cfg.Isolation, jr.d.cfg.JobNetwork, jr.d.cfg.JobImage = "docker", "none", image
	jr.d.userMapping = mapping
	if mapping.RequiresOwnershipHandOff() {
		jr.d.ownership = isolation.DockerOwnership{Mapping: mapping, Image: image, StateDir: jr.d.cfg.StateDir}
	}
}

func TestDockerNPMInstallIsOfflineAndUsesOnlyThePublishedLockfile(t *testing.T) {
	if os.Getenv("RUN_DOCKER_SANDBOX_PROBE") != "1" {
		t.Skip("set RUN_DOCKER_SANDBOX_PROBE=1 for the container integration gate")
	}
	requireRealNPM(t)
	for _, tc := range npmScenarios()[:2] {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			jr, events := testJobRunner(t)
			jr.workDir = filepath.Join(jr.d.cfg.StateDir, "jobs", "job-rjic")
			if err := os.MkdirAll(jr.workDir, 0o700); err != nil {
				t.Fatal(err)
			}
			f := newAgentResultTestOn(t, jr, events, func(origin string) { seedRunnerToolProject(t, origin, tc.spec) })
			if tc.agent != nil {
				tc.agent(t, f.checkout)
			}
			useDockerChecks(t, f)
			if !f.jr.offlineInstall() || f.jr.installNetwork() != installNetworkNone {
				t.Fatalf("docker install is not offline: %q", f.jr.npmInstallArgv())
			}

			result := f.runCheck(t, tc.check)

			output, _ := result["output"].(string)
			if result["result"] != tc.want || !strings.Contains(output, tc.cause) {
				t.Fatalf("result = %v, want %s caused by %q", result, tc.want, tc.cause)
			}
			cache := npmCacheRoot(f.jr.d.cfg.StateDir, f.jr.workDir)
			if info, err := os.Stat(cache); err != nil || info.Mode().Perm() != 0o700 || fileOwner(t, cache) != os.Getuid() {
				t.Fatalf("npm cache after the docker install = %v, %v", info, err)
			}
		})
	}
}
