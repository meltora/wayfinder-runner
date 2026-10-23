package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func newSparseResultTest(t *testing.T) agentResultTest {
	t.Helper()
	return newAgentResultTestWith(t, func(origin string) {
		writeAgentFile(t, origin, "src/app.txt", "app\n")
		writeAgentFile(t, origin, "docs/guide.txt", "guide\n")
	})
}

func TestSparsePatternsLeftAfterDisableDoNotHideADeletion(t *testing.T) {
	t.Run("checkout", func(t *testing.T) {
		f := newSparseResultTest(t)
		agentGit(t, f.checkout, "sparse-checkout", "set", "--cone", "src")
		agentGit(t, f.checkout, "sparse-checkout", "disable")
		if _, err := os.Stat(filepath.Join(f.checkout, ".git", "info", "sparse-checkout")); err != nil {
			t.Fatalf("Git removed the patterns file, so this test proves nothing: %v", err)
		}
		if err := os.Remove(filepath.Join(f.checkout, "docs", "guide.txt")); err != nil {
			t.Fatal(err)
		}
		writeAgentFile(t, f.checkout, "src/app.txt", "changed\n")

		sha := f.publish(t)

		if slices.Contains(f.trustedPaths(t, sha), "docs/guide.txt") {
			t.Fatal("a deleted file outside the old cone was published")
		}
	})
	t.Run("worktree", func(t *testing.T) {
		f := newSparseResultTest(t)
		wt := f.addWorktree(t, ".claude/worktrees/s", "agent-s")
		agentGit(t, wt, "sparse-checkout", "set", "--cone", "src")
		agentGit(t, wt, "sparse-checkout", "disable")
		if err := os.Remove(filepath.Join(wt, "docs", "guide.txt")); err != nil {
			t.Fatal(err)
		}
		writeAgentFile(t, wt, "src/app.txt", "changed\n")

		sha := f.publish(t)

		if slices.Contains(f.trustedPaths(t, sha), "docs/guide.txt") {
			t.Fatal("a deleted file outside the old worktree cone was published")
		}
	})
}

func TestEnabledSparseCheckoutKeepsOutOfConeFiles(t *testing.T) {
	t.Run("worktree config", func(t *testing.T) {
		f := newSparseResultTest(t)
		wt := f.addWorktree(t, ".claude/worktrees/s", "agent-s")
		agentGit(t, wt, "sparse-checkout", "set", "--cone", "src")
		if _, err := os.Stat(filepath.Join(wt, "docs", "guide.txt")); err == nil {
			t.Fatal("sparse checkout kept an out-of-cone file")
		}
		writeAgentFile(t, wt, "src/app.txt", "changed\n")

		sha := f.publish(t)

		if !slices.Contains(f.trustedPaths(t, sha), "docs/guide.txt") {
			t.Fatal("an out-of-cone file was deleted from the result")
		}
	})
	t.Run("legacy common config", func(t *testing.T) {
		f := newSparseResultTest(t)
		agentGit(t, f.checkout, "config", "core.sparseCheckout", "true")
		writeAgentFile(t, f.checkout, ".git/info/sparse-checkout", "/*\n!/docs/\n")
		agentGit(t, f.checkout, "read-tree", "-mu", "HEAD")
		if _, err := os.Stat(filepath.Join(f.checkout, "docs", "guide.txt")); err == nil {
			t.Fatal("sparse checkout kept an out-of-cone file")
		}
		writeAgentFile(t, f.checkout, "src/app.txt", "changed\n")

		sha := f.publish(t)

		if !slices.Contains(f.trustedPaths(t, sha), "docs/guide.txt") {
			t.Fatal("an out-of-cone file was deleted from the result")
		}
	})
	t.Run("special patterns file refuses only when enabled", func(t *testing.T) {
		f := newSparseResultTest(t)
		makeFifo(t, filepath.Join(f.checkout, ".git", "info", "sparse-checkout"))
		writeAgentFile(t, f.checkout, "src/app.txt", "changed\n")
		f.publish(t)
		agentGit(t, f.checkout, "config", "core.sparseCheckout", "true")
		assertRefused(t, f, "sparse-checkout patterns")
	})
}

func TestSparseCheckoutRefusesOutOfConeTypeChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, dir string)
		want   string
	}{
		{
			name: "a file replaced by a directory",
			change: func(t *testing.T, dir string) {
				writeAgentFile(t, dir, "docs/guide.txt/inner.txt", "agent\n")
			},
			want: "replaces docs/guide.txt, which is outside the sparse-checkout cone",
		},
		{
			name: "a directory replaced by a file",
			change: func(t *testing.T, dir string) {
				if err := os.RemoveAll(filepath.Join(dir, "docs")); err != nil {
					t.Fatal(err)
				}
				writeAgentFile(t, dir, "docs", "agent\n")
			},
			want: "replaces docs/guide.txt, which is outside the sparse-checkout cone",
		},
	} {
		t.Run(tc.name+" in the checkout", func(t *testing.T) {
			f := newSparseResultTest(t)
			agentGit(t, f.checkout, "sparse-checkout", "set", "--cone", "src")
			tc.change(t, f.checkout)
			assertRefused(t, f, tc.want)
		})
		t.Run(tc.name+" in a worktree", func(t *testing.T) {
			f := newSparseResultTest(t)
			wt := f.addWorktree(t, ".claude/worktrees/s", "agent-s")
			agentGit(t, wt, "sparse-checkout", "set", "--cone", "src")
			tc.change(t, wt)
			assertRefused(t, f, tc.want)
		})
	}
	t.Run("a plain out-of-cone deletion is still restored next to an in-cone directory", func(t *testing.T) {
		f := newSparseResultTest(t)
		agentGit(t, f.checkout, "sparse-checkout", "set", "--cone", "src")
		writeAgentFile(t, f.checkout, "src/docs/guide.txt/inner.txt", "agent\n")

		sha := f.publish(t)

		paths := f.trustedPaths(t, sha)
		if !slices.Contains(paths, "docs/guide.txt") || !slices.Contains(paths, "src/docs/guide.txt/inner.txt") {
			t.Fatalf("paths = %v", paths)
		}
	})
}

func TestSparseCheckoutEnabledReadsConfigAsData(t *testing.T) {
	for _, tc := range []struct {
		name     string
		common   string
		worktree string
		want     bool
	}{
		{"absent", "", "", false},
		{"common true", "[core]\n\tsparseCheckout = true\n", "", true},
		{"key without value", "[core]\n\tsparsecheckout\n", "", true},
		{"same line as section", "[Core] SparseCheckout = yes\n", "", true},
		{"last value wins", "[core]\n\tsparseCheckout = true\n[core]\n\tsparseCheckout = off\n", "", false},
		{"comments and quotes", "# [core] sparseCheckout = true\n[core]\n\tsparseCheckout = \"1\" ; note\n", "", true},
		{"subsection is another section", "[core \"x\"]\n\tsparseCheckout = true\n", "", false},
		{"other key", "[core]\n\tsparseCheckoutCone = true\n", "", false},
		{"worktree config ignored without the extension", "", "[core]\n\tsparseCheckout = true\n", false},
		{"worktree config overrides", "[extensions]\n\tworktreeConfig = true\n[core]\n\tsparseCheckout = true\n", "[core]\n\tsparseCheckout = false\n", false},
		{"worktree config enables", "[extensions]\n\tworktreeConfig = true\n", "[core]\n\tsparseCheckout = true\n", true},
		{"unparsable value", "[core]\n\tsparseCheckout = maybe\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.common != "" {
				writeAgentFile(t, dir, "repo/.git/config", tc.common)
			}
			if tc.worktree != "" {
				writeAgentFile(t, dir, "repo/.git/worktrees/w/config.worktree", tc.worktree)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			if got := sparseCheckoutEnabled(root, "repo/.git", "repo/.git/worktrees/w"); got != tc.want {
				t.Fatalf("sparseCheckoutEnabled = %v, want %v", got, tc.want)
			}
		})
	}
	t.Run("unreadable config keeps the patterns", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "repo", ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		makeFifo(t, filepath.Join(dir, "repo", ".git", "config"))
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		withinDeadline(t, func() {
			if !sparseCheckoutEnabled(root, "repo/.git", "repo/.git") {
				t.Error("an unreadable config disabled the sparse patterns")
			}
		})
	})
}
