package repo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const pinnedRemoteURL = "https://git.example.com/org/app.git"

type pinnedRemote struct {
	origin string
	calls  []string
}

func (remote *pinnedRemote) run(ctx context.Context, dir, remoteURL string, _ GitHTTPSCredential, args ...string) (string, error) {
	remote.calls = append(remote.calls, strings.Join(args, " "))
	rewritten := make([]string, len(args))
	for index, arg := range args {
		if arg == remoteURL {
			arg = remote.origin
		}
		rewritten[index] = arg
	}
	cmd := exec.CommandContext(ctx, "git", rewritten...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_ALLOW_PROTOCOL=file")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	if rewritten[0] == "clone" {
		target := rewritten[len(rewritten)-1]
		if out, err := exec.CommandContext(ctx, "git", "-C", target, "remote", "set-url", "origin", remoteURL).CombinedOutput(); err != nil {
			return string(out), err
		}
	}
	return strings.TrimSpace(string(out)), nil
}

func pinnedCredential() *GitHTTPSCredential {
	return &GitHTTPSCredential{Username: "x-access-token", Password: "token-value", NotAfter: time.Now().Add(time.Hour)}
}

func TestCreatePinnedWorkspaceUsesTheCachedCommitWithoutNetwork(t *testing.T) {
	ctx := context.Background()
	origin := seedOrigin(t)
	sha := gitOutputForTest(t, origin, "rev-parse", "HEAD")
	m := NewManager(filepath.Join(t.TempDir(), "repos"))
	m.AllowLocal = true
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatal(err)
	}
	remote := &pinnedRemote{origin: origin}
	m.RemoteGit = remote.run
	dest := filepath.Join(t.TempDir(), "workspace")
	if err := m.CreatePinnedWorkspace(ctx, origin, sha, dest, nil); err != nil {
		t.Fatal(err)
	}
	if got := gitOutputForTest(t, dest, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("HEAD = %s, want %s", got, sha)
	}
	if len(remote.calls) != 0 {
		t.Fatalf("cached commit reached the network: %v", remote.calls)
	}
}

func TestCreatePinnedWorkspaceRefusesAnonymousFetchOfAMissingCommit(t *testing.T) {
	ctx := context.Background()
	origin := seedOrigin(t)
	m := NewManager(filepath.Join(t.TempDir(), "repos"))
	m.AllowLocal = true
	if _, err := m.Register(ctx, origin); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin, "later.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, origin, "add", "later.txt")
	git(t, origin, "commit", "-m", "later")
	later := gitOutputForTest(t, origin, "rev-parse", "HEAD")
	for name, credential := range map[string]*GitHTTPSCredential{"no credential": nil, "credential for a non-https origin": pinnedCredential()} {
		dest := filepath.Join(t.TempDir(), "workspace")
		err := m.CreatePinnedWorkspace(ctx, origin, later, dest, credential)
		if err == nil || !strings.Contains(err.Error(), "without a repository credential") {
			t.Fatalf("%s: err = %v", name, err)
		}
		if _, statErr := os.Lstat(dest); !os.IsNotExist(statErr) {
			t.Fatalf("%s: workspace created after refusal", name)
		}
	}
	if _, err := m.runGitWithProtocols(ctx, m.mirrorDir(origin), false, "cat-file", "-e", later+"^{commit}"); err == nil {
		t.Fatal("refused checkout still fetched the commit anonymously")
	}
}

func TestCreatePinnedWorkspaceRefusesAMissingMirrorWithoutCredential(t *testing.T) {
	ctx := context.Background()
	m := NewManager(filepath.Join(t.TempDir(), "repos"))
	remote := &pinnedRemote{origin: seedOrigin(t)}
	m.RemoteGit = remote.run
	err := m.CreatePinnedWorkspace(ctx, pinnedRemoteURL, strings.Repeat("a", 40), filepath.Join(t.TempDir(), "workspace"), nil)
	if err == nil || !strings.Contains(err.Error(), "without a repository credential") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Lstat(m.mirrorDir(pinnedRemoteURL)); !os.IsNotExist(statErr) {
		t.Fatal("mirror cloned anonymously")
	}
	if len(remote.calls) != 0 {
		t.Fatalf("remote calls = %v", remote.calls)
	}
}

func TestCreatePinnedWorkspaceEnsuresTheMirrorWithACredential(t *testing.T) {
	ctx := context.Background()
	origin := seedOrigin(t)
	sha := gitOutputForTest(t, origin, "rev-parse", "HEAD")
	m := NewManager(filepath.Join(t.TempDir(), "repos"))
	remote := &pinnedRemote{origin: origin}
	m.RemoteGit = remote.run
	dest := filepath.Join(t.TempDir(), "workspace")
	if err := m.CreatePinnedWorkspace(ctx, pinnedRemoteURL, sha, dest, pinnedCredential()); err != nil {
		t.Fatal(err)
	}
	if got := gitOutputForTest(t, dest, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("HEAD = %s, want %s", got, sha)
	}
	if len(remote.calls) != 1 || !strings.HasPrefix(remote.calls[0], "clone --mirror") {
		t.Fatalf("remote calls = %v", remote.calls)
	}
}
