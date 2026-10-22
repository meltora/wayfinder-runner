package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func assertRefused(t *testing.T, f agentResultTest, want string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := f.jr.commitAgentChanges(context.Background(), f.repository())
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("result resolution blocked")
	}
	var resultErr *agentResultError
	if !errors.As(err, &resultErr) || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want refusal containing %q", err, want)
	}
	assertNoHostPath(t, err.Error(), f.jr.workDir, f.checkout)
}

func TestDiscoveryRefusesWorktreeOutsideTheWorkspace(t *testing.T) {
	f := newAgentResultTest(t)
	outside := filepath.Join(t.TempDir(), "outside")
	agentGit(t, f.checkout, "worktree", "add", "-q", "-b", "agent-outside", outside)
	agentCommit(t, outside, "outside.txt", "outside\n")

	assertRefused(t, f, "outside the job workspace")
}

func TestDiscoveryRefusesWorktreeReachedThroughASymlink(t *testing.T) {
	f := newAgentResultTest(t)
	worktree := filepath.Join(f.jr.workDir, "agent-wt")
	agentGit(t, f.checkout, "worktree", "add", "-q", "-b", "agent-link", worktree)
	agentCommit(t, worktree, "linked.txt", "linked\n")
	real := filepath.Join(f.jr.workDir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(worktree, filepath.Join(real, "agent-wt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, "agent-wt"), worktree); err != nil {
		t.Fatal(err)
	}

	assertRefused(t, f, "symbolic link")
}

func TestDiscoveryRefusesForgedOrMissingGitFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, f agentResultTest, worktree string)
		want  string
	}{
		{"forged", func(t *testing.T, f agentResultTest, worktree string) {
			other := f.addWorktree(t, ".claude/worktrees/other", "agent-other")
			writeAgentFile(t, worktree, ".git", "gitdir: "+filepath.Join(f.checkout, ".git", "worktrees", filepath.Base(other))+"\n")
		}, "does not belong to the repository checkout"},
		{"missing", func(t *testing.T, f agentResultTest, worktree string) {
			if err := os.Remove(filepath.Join(worktree, ".git")); err != nil {
				t.Fatal(err)
			}
		}, "does not belong to the repository checkout"},
		{"directory", func(t *testing.T, f agentResultTest, worktree string) {
			if err := os.Remove(filepath.Join(worktree, ".git")); err != nil {
				t.Fatal(err)
			}
			agentGit(t, worktree, "init", "-q")
		}, "does not belong to the repository checkout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentResultTest(t)
			worktree := f.addWorktree(t, ".claude/worktrees/forged", "agent-forged")
			agentCommit(t, worktree, "forged.txt", "forged\n")
			tc.plant(t, f, worktree)
			assertRefused(t, f, tc.want)
		})
	}
}

func TestDiscoveryRefusesWorktreeInsideAnotherJobCheckout(t *testing.T) {
	f := newAgentResultTest(t)
	other := workspaceRepositoryDir(t, f.jr, "repo-2")
	agentGit(t, other, "init", "-q", "-b", "main")
	f.jr.repositories = append(f.jr.repositories, checkedOutRepository{ID: "repo-2", WorkDir: other})
	worktree := filepath.Join(other, "nested")
	agentGit(t, f.checkout, "worktree", "add", "-q", "-b", "agent-cross", worktree)
	agentCommit(t, worktree, "cross.txt", "cross\n")

	assertRefused(t, f, "inside the checkout of repository repo-2")
}

func TestDiscoveryRefusesUnsupportedWorktreePath(t *testing.T) {
	f := newAgentResultTest(t)
	worktree := filepath.Join(f.checkout, ".claude", "worktrees", "bad name")
	agentGit(t, f.checkout, "worktree", "add", "-q", "-b", "agent-bad", worktree)

	assertRefused(t, f, "unsupported path")
}

func TestDiscoveryRefusesWorktreeContainingTheCheckout(t *testing.T) {
	f := newAgentResultTest(t)
	admin := filepath.Join(f.checkout, ".git", "worktrees", "evil")
	parent := filepath.Dir(f.checkout)
	writeAgentFile(t, admin, "gitdir", filepath.Join(parent, ".git")+"\n")
	writeAgentFile(t, parent, ".git", "gitdir: "+admin+"\n")

	assertRefused(t, f, "contains the repository checkout")
}

func TestDiscoveryRefusesSymlinkedGitMetadata(t *testing.T) {
	for _, target := range []string{".git", ".git/worktrees"} {
		t.Run(target, func(t *testing.T) {
			f := newAgentResultTest(t)
			f.addWorktree(t, ".claude/worktrees/x", "agent-x")
			path := filepath.Join(f.checkout, filepath.FromSlash(target))
			moved := filepath.Join(t.TempDir(), "moved")
			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, path); err != nil {
				t.Fatal(err)
			}
			assertRefused(t, f, "Git metadata")
		})
	}
}

func TestDiscoveryRefusesSpecialOrOversizedAdminFiles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, admin string)
	}{
		{"fifo", func(t *testing.T, admin string) {
			gitdir := filepath.Join(admin, "gitdir")
			if err := os.Remove(gitdir); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(gitdir, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"oversized", func(t *testing.T, admin string) {
			writeAgentFile(t, admin, "gitdir", strings.Repeat("a", maxWorktreePointerBytes+1))
		}},
		{"malformed", func(t *testing.T, admin string) {
			writeAgentFile(t, admin, "gitdir", "/somewhere/not-a-gitfile\n")
		}},
		{"admin-file", func(t *testing.T, admin string) {
			if err := os.RemoveAll(admin); err != nil {
				t.Fatal(err)
			}
			writeAgentFile(t, filepath.Dir(admin), filepath.Base(admin), "not a directory\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentResultTest(t)
			f.addWorktree(t, ".claude/worktrees/special", "agent-special")
			tc.plant(t, filepath.Join(f.checkout, ".git", "worktrees", "special"))
			assertRefused(t, f, "worktree metadata")
		})
	}
}

func TestDiscoveryRefusesTooManyAdminEntries(t *testing.T) {
	f := newAgentResultTest(t)
	for index := 0; index <= maxAgentResultRefs; index++ {
		if err := os.MkdirAll(filepath.Join(f.checkout, ".git", "worktrees", fmt.Sprintf("w%d", index)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	assertRefused(t, f, "too many linked worktrees")
}

func TestDiscoveryAcceptsRelativeGitdirAndIgnoresMissingWorktrees(t *testing.T) {
	f := newAgentResultTest(t)
	worktree := f.addWorktree(t, ".claude/worktrees/relative", "agent-relative")
	writeAgentFile(t, worktree, "relative.txt", "relative\n")
	admin := filepath.Join(f.checkout, ".git", "worktrees", "relative")
	writeAgentFile(t, admin, "gitdir", "../../../.claude/worktrees/relative/.git\n")
	writeAgentFile(t, worktree, ".git", "gitdir: ../../../.git/worktrees/relative\n")
	gone := f.addWorktree(t, ".claude/worktrees/gone", "agent-gone")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	sha := f.publish(t)

	if !slices.Contains(f.trustedPaths(t, sha), "relative.txt") {
		t.Fatal("worktree with relative paths was not published")
	}
}

func TestDiscoveryWithoutGitDirectoryPublishesTheCheckoutFiles(t *testing.T) {
	f := newAgentResultTest(t)
	if err := os.RemoveAll(filepath.Join(f.checkout, ".git")); err != nil {
		t.Fatal(err)
	}
	writeAgentFile(t, f.checkout, "after.txt", "no git directory\n")

	sha := f.publish(t)

	if !slices.Contains(f.trustedPaths(t, sha), "after.txt") {
		t.Fatal("checkout files were not published")
	}
}
