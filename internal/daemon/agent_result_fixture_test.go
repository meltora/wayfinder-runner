package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/repo"
)

type agentResultTest struct {
	jr       *jobRunner
	events   *capturedEvents
	origin   string
	checkout string
	base     string
	branch   string
	cleanup  func()
}

func newAgentResultTest(t *testing.T) agentResultTest {
	t.Helper()
	return newAgentResultTestWith(t, nil)
}

func newAgentResultTestWith(t *testing.T, seed func(origin string)) agentResultTest {
	t.Helper()
	origin := t.TempDir()
	buildWorkspaceGit(t, origin, "init", "-b", "main")
	buildWorkspaceGit(t, origin, "config", "user.email", "runner@example.test")
	buildWorkspaceGit(t, origin, "config", "user.name", "Runner Test")
	writeAgentFile(t, origin, "README.md", "approved base\n")
	if seed != nil {
		seed(origin)
	}
	buildWorkspaceGit(t, origin, "add", ".")
	buildWorkspaceGit(t, origin, "commit", "-m", "approved base")
	base := buildWorkspaceGit(t, origin, "rev-parse", "HEAD")
	writeAgentFile(t, origin, "advanced-only.txt", "outside approved snapshot\n")
	buildWorkspaceGit(t, origin, "add", ".")
	buildWorkspaceGit(t, origin, "commit", "-m", "branch advanced after authorization")
	jr, events := testJobRunner(t)
	jr.d.cfg.AllowUnsafeDirect = true
	jr.d.cfg.Isolation = "direct"
	jr.d.cfg.Repositories = map[string]config.RepositoryConfig{"repo-1": {URL: origin}}
	jr.job.Ticket = v1.TicketContext{Key: "KAN-7", Summary: "Add feature"}
	jr.job.Repositories = []v1.JobRepository{{
		RepositoryID: "repo-1", BaseBranch: "refs/heads/main", BaseSHA: base, Primary: true,
	}}
	checkout, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return agentResultTest{
		jr: jr, events: events, origin: origin, checkout: checkout, base: base,
		branch: jr.repositories[0].Branch, cleanup: cleanup,
	}
}

func (jr *jobRunner) commitAgentChanges(ctx context.Context, repository checkedOutRepository) (string, error) {
	result, err := jr.publishAgentResult(ctx, repository)
	if err != nil {
		return "", err
	}
	return result.commit, nil
}

func (f agentResultTest) repository() checkedOutRepository {
	return f.jr.repositories[0]
}

func (f agentResultTest) addWorktree(t *testing.T, relative, branch string) string {
	t.Helper()
	path := filepath.Join(f.checkout, filepath.FromSlash(relative))
	agentGit(t, f.checkout, "worktree", "add", "-q", "-b", branch, path)
	return path
}

func (f agentResultTest) publish(t *testing.T) string {
	t.Helper()
	sha, err := f.jr.commitAgentChanges(context.Background(), f.repository())
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func (f agentResultTest) finalizeWithPush(t *testing.T) error {
	t.Helper()
	f.jr.job.PushBranch = true
	rm := repo.NewManager(filepath.Join(f.jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	_, _, err := f.jr.finalizeBuild(context.Background(), rm, false)
	return err
}

func (f agentResultTest) trustedGit(t *testing.T, args ...string) string {
	t.Helper()
	return trustedGitForTest(t, f.repository().Trusted.gitDir, args...)
}

func (f agentResultTest) trustedPaths(t *testing.T, commit string) []string {
	t.Helper()
	return strings.Split(f.trustedGit(t, "ls-tree", "-r", "--name-only", commit), "\n")
}

func (f agentResultTest) originPaths(t *testing.T) []string {
	t.Helper()
	return strings.Split(trustedGitForTest(t, filepath.Join(f.origin, ".git"), "ls-tree", "-r", "--name-only", "refs/heads/"+f.branch), "\n")
}

func trustedGitForTest(t *testing.T, gitDir string, args ...string) string {
	t.Helper()
	output, err := trustedGitTry(gitDir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return output
}

func trustedGitTry(gitDir string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"--git-dir=" + gitDir, "-c", "core.commitGraph=false"}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1")
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func agentGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "user.name=Agent", "-c", "user.email=agent@example.test"}, args...)...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func agentCommit(t *testing.T, dir, name, content string) string {
	t.Helper()
	writeAgentFile(t, dir, name, content)
	agentGit(t, dir, "add", "-f", "--", name)
	agentGit(t, dir, "commit", "-q", "-m", "agent change "+name)
	return agentGit(t, dir, "rev-parse", "HEAD")
}

func writeAgentFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertNoHostPath(t *testing.T, message string, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if path != "" && strings.Contains(message, path) {
			t.Fatalf("message discloses host path %q: %s", path, message)
		}
	}
	if strings.Contains(message, string(filepath.Separator)+"tmp"+string(filepath.Separator)) {
		t.Fatalf("message discloses an absolute path: %s", message)
	}
}

func workspaceRepositoryDir(t *testing.T, jr *jobRunner, repositoryID string) string {
	t.Helper()
	dir := filepath.Join(jr.workDir, "repositories", repositoryID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func attachTrustedRepositories(t *testing.T, jr *jobRunner) {
	t.Helper()
	for index := range jr.repositories {
		repository := &jr.repositories[index]
		head := agentGit(t, repository.WorkDir, "rev-parse", "HEAD")
		dest := filepath.Join(trustedResultsRoot(jr.d.cfg.StateDir, jr.workDir), repository.ID)
		agentGit(t, repository.WorkDir, "clone", "-q", "--bare", "--local", repository.WorkDir, filepath.Join(dest, "repo.git"))
		trusted, err := configureTrustedResultRepository(context.Background(), dest, head, time.Unix(1700000000, 0))
		if err != nil {
			t.Fatal(err)
		}
		repository.Trusted = trusted
		repository.StartSHA = head
		if repository.BaseSHA == "" {
			repository.BaseSHA = head
		}
	}
}

func makeFifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if writer, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = writer.Close()
		}
	})
}

func withinDeadline(t *testing.T, run func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		run()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("operation blocked on an agent-planted special file")
	}
}

func plantMarkerProgram(t *testing.T, dir, name string) (string, string) {
	t.Helper()
	marker := filepath.Join(dir, name+".marker")
	script := filepath.Join(dir, name+".sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return script, marker
}

func assertMarkerAbsent(t *testing.T, markers ...string) {
	t.Helper()
	for _, marker := range markers {
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("agent program ran: %s exists", marker)
		}
	}
}
