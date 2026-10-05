package repo

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func seedOrigin(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	git(t, src, "init", "-b", "main")
	git(t, src, "config", "user.email", "t@t")
	git(t, src, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, src, "add", ".")
	git(t, src, "commit", "-m", "init")
	return src
}

func TestRegisterAndDefaultBranch(t *testing.T) {
	origin := seedOrigin(t)
	m := NewManager(t.TempDir())
	m.AllowLocal = true
	ctx := context.Background()

	dir, err := m.Register(ctx, origin)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("mirror missing: %v", err)
	}
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatalf("re-Register (fetch): %v", err)
	}
	branch, err := m.DefaultBranch(ctx, origin)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if branch != "main" {
		t.Fatalf("DefaultBranch = %q", branch)
	}
}

func TestWorktreeLifecycle(t *testing.T) {
	origin := seedOrigin(t)
	root := t.TempDir()
	m := NewManager(root)
	m.AllowLocal = true
	ctx := context.Background()
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatalf("Register: %v", err)
	}
	dest := filepath.Join(root, "wt", "job-1")
	if err := m.CreateWorktree(ctx, origin, "feature/job-1", "main", dest); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Fatalf("worktree content missing: %v", err)
	}
	sha, err := m.HeadSHA(ctx, dest)
	if err != nil || len(sha) != 40 {
		t.Fatalf("HeadSHA = %q, %v", sha, err)
	}
	if err := m.RemoveWorktree(ctx, origin, dest); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("worktree not removed")
	}
}

func TestMirrorNamesDoNotCollideByBasename(t *testing.T) {
	t.Parallel()
	m := NewManager(t.TempDir())
	first := m.mirrorDir("https://git.example.com/team-a/api.git")
	second := m.mirrorDir("https://git.example.com/team-b/api.git")
	if first == second {
		t.Fatalf("different repositories share mirror path %q", first)
	}
}

func TestRegisterRejectsUnsafeTransport(t *testing.T) {
	t.Parallel()
	m := NewManager(t.TempDir())
	for _, raw := range []string{"ext::sh -c touch /tmp/pwned", "file:///etc", "/tmp/local.git", "https://user:password@git.example.com/repo.git"} {
		if _, err := m.Register(context.Background(), raw); err == nil {
			t.Fatalf("unsafe repository URL %q accepted", raw)
		}
	}
}

func TestSelfContainedWorkspaceDoesNotReferenceRunnerMirror(t *testing.T) {
	ctx := context.Background()
	origin := seedOrigin(t)
	m := NewManager(filepath.Join(t.TempDir(), "repos"))
	m.AllowLocal = true
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "workspace")
	if err := m.CreateWorkspace(ctx, origin, "job/test/attempt", "main", dest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dest, ".git"))
	if err != nil || !info.IsDir() {
		t.Fatalf("workspace .git must be self-contained: %v", err)
	}
	alternates := filepath.Join(dest, ".git", "objects", "info", "alternates")
	if _, err := os.Stat(alternates); !os.IsNotExist(err) {
		t.Fatalf("workspace unexpectedly references shared objects: %v", err)
	}
}

func TestCreateDetachedWorkspacePinsExactCommitAcrossBranchMove(t *testing.T) {
	ctx := context.Background()
	origin := seedOrigin(t)
	expected := gitOutputForTest(t, origin, "rev-parse", "HEAD")
	m := NewManager(filepath.Join(t.TempDir(), "repos"))
	m.AllowLocal = true
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(origin, "moved.txt"), []byte("new head\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, origin, "add", "moved.txt")
	git(t, origin, "commit", "-m", "move branch")

	dest := filepath.Join(t.TempDir(), "detached-workspace")
	if err := m.CreateDetachedWorkspace(ctx, origin, expected, dest); err != nil {
		t.Fatal(err)
	}
	if got := gitOutputForTest(t, dest, "rev-parse", "HEAD"); got != expected {
		t.Fatalf("workspace HEAD = %s, want observed %s", got, expected)
	}
	if branch := gitOutputForTest(t, dest, "branch", "--show-current"); branch != "" {
		t.Fatalf("planning workspace is attached to mutable branch %q", branch)
	}
}

func TestCreateDetachedWorkspaceRejectsWrongOrMissingCommit(t *testing.T) {
	ctx := context.Background()
	origin := seedOrigin(t)
	m := NewManager(filepath.Join(t.TempDir(), "repos"))
	m.AllowLocal = true
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{"", "abc", strings.Repeat("f", 40)} {
		if err := m.CreateDetachedWorkspace(ctx, origin, sha, filepath.Join(t.TempDir(), "workspace")); err == nil {
			t.Fatalf("invalid or unavailable commit %q accepted", sha)
		}
	}
}

func TestCreateDetachedWorkspaceFetchesObservedCommitMissingFromMirror(t *testing.T) {
	ctx := context.Background()
	origin := seedOrigin(t)
	m := NewManager(filepath.Join(t.TempDir(), "repos"))
	m.AllowLocal = true
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin, "observed-after-register.txt"), []byte("observed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, origin, "add", "observed-after-register.txt")
	git(t, origin, "commit", "-m", "observed after mirror registration")
	expected := gitOutputForTest(t, origin, "rev-parse", "HEAD")
	dest := filepath.Join(t.TempDir(), "detached-workspace")
	if err := m.CreateDetachedWorkspace(ctx, origin, expected, dest); err != nil {
		t.Fatal(err)
	}
	if got := gitOutputForTest(t, dest, "rev-parse", "HEAD"); got != expected {
		t.Fatalf("workspace HEAD = %s, want fetched %s", got, expected)
	}
}

func TestCreateDetachedWorkspaceRejectsExistingDestination(t *testing.T) {
	ctx := context.Background()
	origin := seedOrigin(t)
	m := NewManager(filepath.Join(t.TempDir(), "repos"))
	m.AllowLocal = true
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.CreateDetachedWorkspace(ctx, origin, gitOutputForTest(t, origin, "rev-parse", "HEAD"), dest); err == nil {
		t.Fatal("existing workspace destination was accepted")
	}
}

func TestCreateDetachedWorkspaceRejectsUnsafeOrigin(t *testing.T) {
	m := NewManager(t.TempDir())
	if err := m.CreateDetachedWorkspace(
		context.Background(),
		"file:///etc/passwd",
		strings.Repeat("1", 40),
		filepath.Join(t.TempDir(), "workspace"),
	); err == nil {
		t.Fatal("unsafe planning repository origin was accepted")
	}
}

func TestCreateDetachedWorkspaceFailsClosedOnUnwritableDestination(t *testing.T) {
	ctx := context.Background()
	origin := seedOrigin(t)
	m := NewManager(filepath.Join(t.TempDir(), "repos"))
	m.AllowLocal = true
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatal(err)
	}
	sha := gitOutputForTest(t, origin, "rev-parse", "HEAD")
	for _, nested := range []bool{false, true} {
		parent := filepath.Join(t.TempDir(), "blocked")
		if err := os.Mkdir(parent, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
		dest := filepath.Join(parent, "workspace")
		if nested {
			dest = filepath.Join(parent, "nested", "workspace")
		}
		if err := m.CreateDetachedWorkspace(ctx, origin, sha, dest); err == nil {
			t.Fatalf("unwritable destination nested=%v was accepted", nested)
		}
	}
}

func gitOutputForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCreateWorkspaceResolvesPreviouslyPushedRemoteJobBranch(t *testing.T) {
	ctx := context.Background()
	origin := seedOrigin(t)
	git(t, origin, "checkout", "-b", "job/previous/attempt-1")
	if err := os.WriteFile(filepath.Join(origin, "feature.txt"), []byte("previous attempt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, origin, "add", "feature.txt")
	git(t, origin, "commit", "-m", "previous attempt")
	git(t, origin, "checkout", "main")

	m := NewManager(filepath.Join(t.TempDir(), "repos"))
	m.AllowLocal = true
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "workspace")
	if err := m.CreateWorkspace(
		ctx,
		origin,
		"job/current/attempt-2",
		"job/previous/attempt-1",
		dest,
	); err != nil {
		t.Fatalf("CreateWorkspace from remote job branch: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dest, "feature.txt"))
	if err != nil || string(content) != "previous attempt\n" {
		t.Fatalf("workspace did not use previous attempt: %q, %v", content, err)
	}
}

func TestWorkspaceDiffAndRemoval(t *testing.T) {
	ctx := context.Background()
	origin := seedOrigin(t)
	m := NewManager(filepath.Join(t.TempDir(), "repos"))
	m.AllowLocal = true
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "workspace")
	if err := m.CreateWorkspace(ctx, origin, "feature/change", "main", dest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "README.md"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dest, "add", ".")
	git(t, dest, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "change")
	diff, err := m.Diff(ctx, dest, "main")
	if err != nil || !bytes.Contains([]byte(diff), []byte("changed")) {
		t.Fatalf("Diff=%q err=%v", diff, err)
	}
	if _, err := m.Diff(ctx, dest, "../unsafe"); err == nil {
		t.Fatal("unsafe diff ref accepted")
	}
	if err := m.RemoveWorkspace("relative"); err == nil {
		t.Fatal("relative workspace removal accepted")
	}
	if err := m.RemoveWorkspace(dest); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryOperationValidation(t *testing.T) {
	m := NewManager(t.TempDir())
	ctx := context.Background()
	dest := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.CreateWorkspace(ctx, "https://example.test/repo.git", "feature", "main", dest); err == nil {
		t.Fatal("existing workspace destination accepted")
	}
	if err := m.CreateWorkspace(ctx, "https://example.test/repo.git", "../bad", "main", filepath.Join(t.TempDir(), "new")); err == nil {
		t.Fatal("unsafe branch accepted")
	}
	if err := m.PushMirrorBranch(ctx, "file:///tmp/repo", "main"); err == nil {
		t.Fatal("unsafe push URL accepted")
	}
	if err := m.PushMirrorBranch(ctx, "https://example.test/repo.git", "../bad"); err == nil {
		t.Fatal("unsafe push branch accepted")
	}
	if err := m.ImportBundle(ctx, "https://example.test/repo.git", "missing", "main", "bad"); err == nil {
		t.Fatal("invalid expected SHA accepted")
	}
	if err := m.ImportBundle(ctx, "https://example.test/repo.git", "missing", "main", strings.Repeat("a", 40)); err == nil {
		t.Fatal("missing bundle accepted")
	}
}

func TestGitOutputAndEnvironmentAreBounded(t *testing.T) {
	var output limitedOutput
	payload := bytes.Repeat([]byte("x"), (1<<20)+100)
	n, err := output.Write(payload)
	if err != nil || n != len(payload) || !strings.Contains(output.String(), "truncated") {
		t.Fatalf("Write=%d err=%v output=%d", n, err, len(output.String()))
	}
	t.Setenv("REPOSITORY_SECRET", "hidden")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	env := strings.Join(gitEnvironment(), "\n")
	if strings.Contains(env, "REPOSITORY_SECRET") || !strings.Contains(env, "SSH_AUTH_SOCK=") {
		t.Fatalf("git environment=%q", env)
	}
}
