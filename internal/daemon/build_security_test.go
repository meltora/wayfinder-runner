package daemon

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/repo"
)

func TestCappedBufferBoundsUntrustedCheckOutput(t *testing.T) {
	buffer := cappedBuffer{limit: 32}
	payload := bytes.Repeat([]byte("x"), 1024)
	n, err := buffer.Write(payload)
	if err != nil || n != len(payload) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if len(buffer.data) != 32 || !buffer.truncated {
		t.Fatalf("buffer size=%d truncated=%v", len(buffer.data), buffer.truncated)
	}
}

func TestWorkspaceCommandUsesBoundedCredentialFreeEnvironment(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.d.cfg.Isolation = "direct"
	t.Setenv("CHECK_PRIVATE_SECRET", "must-not-leak")
	out, _, err := jr.runWorkspaceCommand(context.Background(), "environment", t.TempDir(), "/bin/sh",
		[]string{"-c", `printf '%s' "${CHECK_PRIVATE_SECRET-unset}"; printf '0123456789'`}, false, 8)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "must-not-leak") || !strings.Contains(out, "truncated") {
		t.Fatalf("output=%q", out)
	}
	if _, _, err := jr.runWorkspaceCommand(context.Background(), "failure", t.TempDir(), "/bin/sh", []string{"-c", "exit 7"}, false, 1024); err == nil {
		t.Fatal("failing command reported success")
	}
}

func TestRequiredCheckRegistrationAndRepositorySelection(t *testing.T) {
	jr, cap := testJobRunner(t)
	jr.repositories = []checkedOutRepository{
		{ID: "primary", WorkDir: t.TempDir(), Primary: true},
		{ID: "secondary", WorkDir: t.TempDir()},
	}
	if got, ok := jr.checkRepository(""); !ok || got.ID != "primary" {
		t.Fatalf("default repository=%+v ok=%v", got, ok)
	}
	if got, ok := jr.checkRepository("secondary"); !ok || got.ID != "secondary" {
		t.Fatalf("secondary repository=%+v ok=%v", got, ok)
	}
	if _, ok := jr.checkRepository("missing"); ok {
		t.Fatal("missing repository selected")
	}
	jr.job.RequiredChecks = []string{"unregistered", "wrong-repository"}
	jr.d.cfg.Checks = map[string]config.CheckConfig{
		"wrong-repository": {Command: "true", RepositoryID: "not-in-job"},
	}
	if jr.runRequiredChecks(context.Background(), map[string]string{"primary": "abc"}) {
		t.Fatal("invalid required checks passed")
	}
	if len(cap.byKind("test_results")) != 2 {
		t.Fatalf("check events=%v", cap.events)
	}
}

func TestCommitCleanRepositoryAndPushRequiresLease(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.test")
	git("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("clean\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "initial")
	jr, _ := testJobRunner(t)
	jr.d.cfg.Isolation = "direct"
	repository := checkedOutRepository{ID: "repo-1", WorkDir: dir, Branch: "feature/test", URL: "https://example.test/repo.git", Primary: true}
	sha, err := jr.commitAgentChanges(context.Background(), repository)
	if err != nil || len(sha) != 40 {
		t.Fatalf("sha=%q err=%v", sha, err)
	}
	jr.d.leases.Release(jr.job.AttemptID)
	if err := jr.pushBranch(context.Background(), repository, repo.NewManager(t.TempDir()), sha); err == nil {
		t.Fatal("push without lease accepted")
	}
}
