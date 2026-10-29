package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/supervisor"
)

func promptsDirectory(stateDir string) string {
	return filepath.Join(stateDir, "supervisor", "prompts")
}

func assertPrivatePromptFile(t *testing.T, stateDir, path, want string) {
	t.Helper()
	if filepath.Dir(path) != promptsDirectory(stateDir) {
		t.Fatalf("prompt file %q is outside %q", path, promptsDirectory(stateDir))
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("prompt file missing while the provider starts: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("prompt file mode = %v", info.Mode())
	}
	dirInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("prompts directory mode = %v err=%v", dirInfo.Mode(), err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != want {
		t.Fatalf("prompt file holds %d bytes err=%v, want %d", len(raw), err, len(want))
	}
}

func TestPromptFileIsPrivateWhileTheProviderRunsAndRemovedAfterwards(t *testing.T) {
	run := newPromptDeliveryRun(t, v1.JobKindBuild)
	run.jr.job.Ticket.Description = sentinelText("DESC", 256<<10)
	checked := false
	run.sup.onStart = func(spec supervisor.Spec) {
		assertPrivatePromptFile(t, run.jr.d.cfg.StateDir, spec.StdinFile, run.jr.buildPrompt())
		for _, root := range []string{run.jr.workDir, run.jr.configDir} {
			if strings.HasPrefix(spec.StdinFile, root+string(filepath.Separator)) {
				t.Fatalf("prompt file %q is inside %q", spec.StdinFile, root)
			}
		}
		checked = true
	}
	run.execute(t)
	if !checked {
		t.Fatalf("provider never started: %v", run.finished(t))
	}
	if _, err := os.Lstat(run.sup.started(t).StdinFile); !os.IsNotExist(err) {
		t.Fatalf("prompt file survived the job: %v", err)
	}
	assertNoPromptFiles(t, run.jr.d.cfg.StateDir)
}

func TestDockerPromptFileIsNeverMountedOrHandedOff(t *testing.T) {
	run := newOwnershipRun(t, testRootlessMapping)
	var promptFile string
	run.sup.duringRun = func() {
		promptFile = run.spec.StdinFile
		assertPrivatePromptFile(t, run.jr.d.cfg.StateDir, promptFile, run.jr.buildPrompt())
	}
	run.execute(t)
	if promptFile == "" {
		t.Fatalf("provider never started: %s", run.log)
	}
	if run.spec.Command != "docker" || !slices.Contains(run.spec.Args, "-i") || run.spec.Stdin != nil {
		t.Fatalf("docker spec = %s %v stdin %v", run.spec.Command, run.spec.Args, run.spec.Stdin)
	}
	prompts := promptsDirectory(run.jr.d.cfg.StateDir)
	for _, flag := range []string{"-v", "--volume", "--mount"} {
		for _, mount := range argumentsAfter(run.spec.Args, flag) {
			if strings.Contains(mount, prompts) || strings.Contains(mount, filepath.Join("supervisor", "prompts")) {
				t.Fatalf("docker mounts the prompts directory: %q", mount)
			}
		}
	}
	for _, arg := range run.spec.Args {
		if strings.Contains(arg, promptFile) || strings.Contains(arg, prompts) {
			t.Fatalf("docker argv references the prompt file: %q", arg)
		}
	}
	for _, request := range append(append([]isolation.OwnershipRequest(nil), run.ownership.handOffs...), run.ownership.reclaims...) {
		for _, root := range append(append([]string(nil), request.Workspace...), request.Private...) {
			if strings.HasPrefix(prompts, root) || strings.HasPrefix(root, prompts) {
				t.Fatalf("ownership request %+v covers the prompts directory", request)
			}
		}
	}
	assertNoPromptFiles(t, run.jr.d.cfg.StateDir)
}

func TestPromptFileIsRemovedOnEveryExitPath(t *testing.T) {
	for name, configure := range map[string]func(*ownershipRun){
		"provider failure": func(run *ownershipRun) { run.sup.waitErr = errors.New("exit status 3") },
		"start failure":    func(run *ownershipRun) { run.sup.startErr = errors.New("docker run failed") },
		"cancelled before start": func(run *ownershipRun) {
			run.ownership.onHandOff = func(isolation.OwnershipRequest) { run.jr.cancelled = true }
		},
		"cancelled while running": func(run *ownershipRun) {
			run.sup.waitErr = errors.New("killed")
			run.sup.duringRun = func() { run.jr.cancelled = true }
		},
		"start evidence failure": func(run *ownershipRun) {
			authority := v1.AgentRunAuthority{SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 1}
			run.jr.job.AgentRunAuthority = &authority
			run.sup.duringRun = func() { run.jr.cancelled = true }
		},
		"hand-off failure": func(run *ownershipRun) { run.ownership.handOffErr = errors.New("chown failed") },
		"lease lost": func(run *ownershipRun) {
			run.sup.waitErr = errors.New("killed")
			run.sup.duringRun = func() { run.jr.leaseLost, run.jr.cancelled = true, true }
		},
	} {
		t.Run(name, func(t *testing.T) {
			run := newOwnershipRun(t, testRootlessMapping)
			configure(run)
			existedAtStart := false
			duringRun := run.sup.duringRun
			run.sup.duringRun = func() {
				_, err := os.Lstat(run.spec.StdinFile)
				existedAtStart = err == nil
				if duringRun != nil {
					duringRun()
				}
			}
			run.execute(t)
			if len(run.finished(t)) == 0 {
				t.Fatal("job did not finish")
			}
			reachedStart := strings.Contains(run.log.String(), "start")
			if reachedStart && run.sup.startErr == nil && !existedAtStart {
				t.Fatalf("prompt file %q did not exist when the provider started", run.spec.StdinFile)
			}
			if reachedStart && run.spec.StdinFile == "" {
				t.Fatal("provider started without a prompt file")
			}
			if run.spec.StdinFile != "" {
				if _, err := os.Lstat(run.spec.StdinFile); !os.IsNotExist(err) {
					t.Fatalf("prompt file survived: %v", err)
				}
			}
			assertNoPromptFiles(t, run.jr.d.cfg.StateDir)
		})
	}
}

func TestConcurrentAttemptsGetDistinctPromptFiles(t *testing.T) {
	stateDir := t.TempDir()
	first, err := writeAgentPromptFile(stateDir, "attempt-1", "first prompt")
	if err != nil {
		t.Fatal(err)
	}
	second, err := writeAgentPromptFile(stateDir, "attempt-2", "second prompt")
	if err != nil {
		t.Fatal(err)
	}
	again, err := writeAgentPromptFile(stateDir, "attempt-1", "retry prompt")
	if err != nil {
		t.Fatal(err)
	}
	if first.path == second.path || first.path == again.path {
		t.Fatalf("prompt files collide: %q %q %q", first.path, second.path, again.path)
	}
	if !strings.Contains(filepath.Base(first.path), "attempt-1") || !strings.Contains(filepath.Base(second.path), "attempt-2") {
		t.Fatalf("prompt files are not scoped to their attempt: %q %q", first.path, second.path)
	}
	assertPrivatePromptFile(t, stateDir, second.path, "second prompt")
	if err := first.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := first.Remove(); err != nil {
		t.Fatalf("second removal failed: %v", err)
	}
	assertPrivatePromptFile(t, stateDir, second.path, "second prompt")
	assertPrivatePromptFile(t, stateDir, again.path, "retry prompt")
	if _, err := os.Lstat(first.path); !os.IsNotExist(err) {
		t.Fatalf("removed prompt file remains: %v", err)
	}
}

func TestPromptFileRefusesAnUnsafePromptsDirectory(t *testing.T) {
	stateDir := t.TempDir()
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "supervisor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, promptsDirectory(stateDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := writeAgentPromptFile(stateDir, "attempt-1", "prompt"); err == nil {
		t.Fatal("prompt file written through a symlinked prompts directory")
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("symlink target received %v err=%v", entries, err)
	}

	loose := t.TempDir()
	if err := os.MkdirAll(promptsDirectory(loose), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(promptsDirectory(loose), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := writeAgentPromptFile(loose, "attempt-1", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	assertPrivatePromptFile(t, loose, file.path, "prompt")
}

func TestStartupSweepsLeftoverPromptFiles(t *testing.T) {
	for _, cleanupJobs := range []bool{false, true} {
		stateDir := t.TempDir()
		file, err := writeAgentPromptFile(stateDir, "attempt-1", "prompt")
		if err != nil {
			t.Fatal(err)
		}
		if err := recoverStartupJobState(stateDir, cleanupJobs, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(file.path); !os.IsNotExist(err) {
			t.Fatalf("cleanupJobs=%v left %q: %v", cleanupJobs, file.path, err)
		}
		assertNoPromptFiles(t, stateDir)
	}
	if err := recoverStartupJobState(t.TempDir(), false, nil); err != nil {
		t.Fatalf("sweep without a prompts directory: %v", err)
	}
}
