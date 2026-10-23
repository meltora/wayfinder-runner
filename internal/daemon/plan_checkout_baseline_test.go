package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func planCheckoutGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func plantGitExecutionMarkers(t *testing.T, dir string) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "executed")
	script := filepath.Join(t.TempDir(), "planted.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch \""+marker+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	planCheckoutGit(t, dir, "config", "core.fsmonitor", script)
	for _, hook := range []string{"post-checkout", "post-index-change", "reference-transaction"} {
		if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", hook), []byte("#!/bin/sh\ntouch \""+marker+"\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return marker
}

func TestPlanPublicationRunsNoGitInThePlanCheckout(t *testing.T) {
	jr, captured := testJobRunner(t)
	configurePlanPublicationRepositories(t, jr)
	marker := plantGitExecutionMarkers(t, jr.repositories[0].WorkDir)

	if _, err := call(t, jr, publishPlanTool, planPublicationArgs(t, "no-host-git", "Plan", "Behavior", "")); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(marker); err == nil {
		t.Fatal("plan publication ran Git configuration planted in the plan checkout")
	}
	if events := captured.byKind("plan_published"); len(events) != 1 {
		t.Fatalf("plan events = %d", len(events))
	}
}

func TestPlanPublicationRefusesChangedCheckouts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, dir string)
	}{
		{"a modified tracked file", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("rewritten\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"a deleted tracked file", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "README.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"a symbolic link replacing a file", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "README.md")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/etc/hostname", filepath.Join(dir, "README.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"an ignored file", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("scratch.txt\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("ignored\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"a moved HEAD", func(t *testing.T, dir string) {
			planCheckoutGit(t, dir, "commit", "--allow-empty", "-m", "moved")
		}},
		{"a detached HEAD at another commit", func(t *testing.T, dir string) {
			planCheckoutGit(t, dir, "commit", "--allow-empty", "-m", "moved")
			planCheckoutGit(t, dir, "checkout", "--detach", "HEAD")
		}},
		{"a symbolic link replacing .git", func(t *testing.T, dir string) {
			moved := filepath.Join(t.TempDir(), "git")
			if err := os.Rename(filepath.Join(dir, ".git"), moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, filepath.Join(dir, ".git")); err != nil {
				t.Fatal(err)
			}
		}},
		{"a gitdir file replacing .git", func(t *testing.T, dir string) {
			moved := filepath.Join(t.TempDir(), "git")
			if err := os.Rename(filepath.Join(dir, ".git"), moved); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+moved+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jr, captured := testJobRunner(t)
			configurePlanPublicationRepositories(t, jr)
			tc.change(t, jr.repositories[0].WorkDir)

			_, err := call(t, jr, publishPlanTool, planPublicationArgs(t, "changed-checkout", "Plan", "Behavior", ""))

			if err == nil || !strings.Contains(err.Error(), "plan repository checkout mismatch for repository-1") {
				t.Fatalf("err = %v", err)
			}
			if events := captured.byKind("plan_published"); len(events) != 0 {
				t.Fatalf("changed checkout emitted %d plan events", len(events))
			}
		})
	}
}

func TestPlanPublicationAllowsRunnerArtifactsAndPackedRefs(t *testing.T) {
	jr, captured := testJobRunner(t)
	configurePlanPublicationRepositories(t, jr)
	dir := jr.repositories[0].WorkDir
	planCheckoutGit(t, dir, "pack-refs", "--all")
	if err := os.MkdirAll(filepath.Join(dir, "shortstack-artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shortstack-artifacts", "screenshot.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := call(t, jr, publishPlanTool, planPublicationArgs(t, "artifacts", "Plan", "Behavior", "")); err != nil {
		t.Fatal(err)
	}
	if events := captured.byKind("plan_published"); len(events) != 1 {
		t.Fatalf("plan events = %d", len(events))
	}
}
