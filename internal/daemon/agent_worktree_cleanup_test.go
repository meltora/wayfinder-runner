package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceCleanupAcceptsAnAlreadyRemovedWorkspace(t *testing.T) {
	f := newAgentResultTest(t)
	if err := os.RemoveAll(f.jr.workDir); err != nil {
		t.Fatal(err)
	}
	if err := removeAgentWorktrees(f.jr.workDir, f.checkout); err != nil {
		t.Fatalf("cleanup of a removed workspace = %v", err)
	}
}

func TestWorkspaceCleanupRefusesARegularFileInPlaceOfTheWorkspace(t *testing.T) {
	f := newAgentResultTest(t)
	if err := os.RemoveAll(f.jr.workDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.jr.workDir, []byte("not a workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeAgentWorktrees(f.jr.workDir, f.checkout); err == nil {
		t.Fatal("cleanup accepted a regular file as the workspace root")
	}
}

func TestWorkspaceCleanupRefusesASymlinkedWorkspace(t *testing.T) {
	f := newAgentResultTest(t)
	sibling := f.addWorktree(t, "../linked-root-sibling", "agent-linked-root")
	moved := f.jr.workDir + ".moved"
	if err := os.Rename(f.jr.workDir, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	if err := os.Symlink(moved, f.jr.workDir); err != nil {
		t.Fatal(err)
	}
	if err := removeAgentWorktrees(f.jr.workDir, f.checkout); err == nil {
		t.Fatal("cleanup accepted a symlinked workspace root")
	}
	if _, err := os.Stat(filepath.Join(moved, strings.TrimPrefix(sibling, f.jr.workDir), "README.md")); err != nil {
		t.Fatalf("cleanup removed a worktree through a symlinked root: %v", err)
	}
}

func TestWorkspaceCleanupRefusesSymlinkedGitDirectory(t *testing.T) {
	f := newAgentResultTest(t)
	outside := t.TempDir()
	writeAgentFile(t, outside, "worktrees/keep/precious", "x")
	gitDir := filepath.Join(f.checkout, ".git")
	if err := os.Rename(gitDir, gitDir+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, gitDir); err != nil {
		t.Fatal(err)
	}

	if err := removeAgentWorktrees(f.jr.workDir, f.checkout); err == nil {
		t.Fatal("cleanup accepted a symlinked Git directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "worktrees", "keep", "precious")); err != nil {
		t.Fatalf("cleanup deleted a directory outside the workspace: %v", err)
	}
}

func TestWorkspaceCleanupRefusesSymlinkedCheckout(t *testing.T) {
	f := newAgentResultTest(t)
	outside := t.TempDir()
	writeAgentFile(t, outside, ".git/worktrees/keep/precious", "x")
	if err := os.Rename(f.checkout, f.checkout+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, f.checkout); err != nil {
		t.Fatal(err)
	}

	if err := removeAgentWorktrees(f.jr.workDir, f.checkout); err == nil {
		t.Fatal("cleanup accepted a symlinked repository checkout")
	}
	if _, err := os.Stat(filepath.Join(outside, ".git", "worktrees", "keep", "precious")); err != nil {
		t.Fatalf("cleanup deleted a directory outside the workspace: %v", err)
	}
}

func TestWorkspaceCleanupSkipsWorktreeReachedThroughSymlink(t *testing.T) {
	f := newAgentResultTest(t)
	sibling := f.addWorktree(t, "../linked-sibling", "agent-linked")
	outside := t.TempDir()
	writeAgentFile(t, outside, "precious", "x")
	writeAgentFile(t, outside, ".git", "gitdir: elsewhere\n")
	if err := os.RemoveAll(sibling); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, sibling); err != nil {
		t.Fatal(err)
	}

	_ = removeAgentWorktrees(f.jr.workDir, f.checkout)
	if _, err := os.Stat(filepath.Join(outside, "precious")); err != nil {
		t.Fatalf("cleanup followed a symlinked worktree directory: %v", err)
	}
}

func TestWorkspaceCleanupIgnoresFifoWorktreePointer(t *testing.T) {
	f := newAgentResultTest(t)
	sibling := f.addWorktree(t, "../fifo-sibling", "agent-fifo-pointer")
	pointer := filepath.Join(f.checkout, ".git", "worktrees", "fifo-sibling", "gitdir")
	if err := os.Remove(pointer); err != nil {
		t.Fatal(err)
	}
	makeFifo(t, pointer)

	withinDeadline(t, func() { _ = removeAgentWorktrees(f.jr.workDir, f.checkout) })
	if _, err := os.Lstat(filepath.Join(f.checkout, ".git", "worktrees")); !os.IsNotExist(err) {
		t.Fatalf("worktree metadata survived cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sibling, "README.md")); err != nil {
		t.Fatalf("cleanup removed a worktree it could not identify: %v", err)
	}
}
