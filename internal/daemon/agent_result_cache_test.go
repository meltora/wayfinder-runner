package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func singleCandidateCache(t *testing.T, f agentResultTest) *candidateCache {
	t.Helper()
	caches := f.repository().Trusted.caches
	if len(caches) != 1 {
		t.Fatalf("candidate caches = %d, want 1", len(caches))
	}
	for _, cache := range caches {
		return cache
	}
	return nil
}

func cachedKey(t *testing.T, cache *candidateCache, name string) statKey {
	t.Helper()
	info, err := os.Lstat(filepath.Join(cache.files, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	key, ok := fileStatKey(info)
	if !ok {
		t.Fatal("no stat key")
	}
	return key
}

func (f agentResultTest) publishedContent(t *testing.T, commit, name string) string {
	t.Helper()
	return f.trustedGit(t, "show", commit+":"+name)
}

func TestSnapshotReusesAnUnchangedFile(t *testing.T) {
	f := newAgentResultTest(t)
	f.repository().Trusted.racyWindow = 0
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	first := f.publish(t)
	before := cachedKey(t, singleCandidateCache(t, f), "feature.txt")

	second := f.publish(t)

	if second != first {
		t.Fatalf("second snapshot = %s, want %s", second, first)
	}
	if after := cachedKey(t, singleCandidateCache(t, f), "feature.txt"); after != before {
		t.Fatal("an unchanged file was copied again")
	}
}

func TestSnapshotCopiesAgainAFileInsideTheRacyWindow(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	f.publish(t)
	before := cachedKey(t, singleCandidateCache(t, f), "feature.txt")

	f.publish(t)

	if after := cachedKey(t, singleCandidateCache(t, f), "feature.txt"); after == before {
		t.Fatal("a file written inside the racy window was reused")
	}
}

func TestSnapshotDetectsASameSizeEditWithTheModificationTimeRestored(t *testing.T) {
	f := newAgentResultTest(t)
	f.repository().Trusted.racyWindow = 0
	path := filepath.Join(f.checkout, "feature.txt")
	writeAgentFile(t, f.checkout, "feature.txt", "aaaa\n")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	f.publish(t)

	writeAgentFile(t, f.checkout, "feature.txt", "bbbb\n")
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	commit := f.publish(t)

	if got := f.publishedContent(t, commit, "feature.txt"); got != "bbbb" {
		t.Fatalf("published %q, want the same-size edit", got)
	}
}

func TestSnapshotDetectsAReplacementWithEqualSizeAndTimes(t *testing.T) {
	f := newAgentResultTest(t)
	f.repository().Trusted.racyWindow = 0
	path := filepath.Join(f.checkout, "feature.txt")
	writeAgentFile(t, f.checkout, "feature.txt", "aaaa\n")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	f.publish(t)

	writeAgentFile(t, f.checkout, "replacement.txt", "cccc\n")
	if err := os.Chtimes(filepath.Join(f.checkout, "replacement.txt"), past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.checkout, "replacement.txt"), path); err != nil {
		t.Fatal(err)
	}
	commit := f.publish(t)

	if got := f.publishedContent(t, commit, "feature.txt"); got != "cccc" {
		t.Fatalf("published %q, want the replacement", got)
	}
}

func TestSnapshotReflectsChangesBetweenSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, f agentResultTest)
		change  func(t *testing.T, f agentResultTest)
		check   func(t *testing.T, f agentResultTest, commit string)
	}{
		{
			name:    "a deleted file is gone",
			prepare: func(t *testing.T, f agentResultTest) { writeAgentFile(t, f.checkout, "gone.txt", "x\n") },
			change: func(t *testing.T, f agentResultTest) {
				removeAgentPath(t, f, "gone.txt")
				writeAgentFile(t, f.checkout, "kept.txt", "y\n")
			},
			check: func(t *testing.T, f agentResultTest, commit string) {
				if slices.Contains(f.trustedPaths(t, commit), "gone.txt") {
					t.Fatal("a deleted file was published")
				}
			},
		},
		{
			name:    "a file replaced by a directory",
			prepare: func(t *testing.T, f agentResultTest) { writeAgentFile(t, f.checkout, "entry", "file\n") },
			change: func(t *testing.T, f agentResultTest) {
				removeAgentPath(t, f, "entry")
				writeAgentFile(t, f.checkout, "entry/inner.txt", "inner\n")
			},
			check: func(t *testing.T, f agentResultTest, commit string) {
				if got := f.publishedContent(t, commit, "entry/inner.txt"); got != "inner" {
					t.Fatalf("published %q", got)
				}
			},
		},
		{
			name:    "a directory replaced by a file",
			prepare: func(t *testing.T, f agentResultTest) { writeAgentFile(t, f.checkout, "entry/inner.txt", "inner\n") },
			change: func(t *testing.T, f agentResultTest) {
				removeAgentPath(t, f, "entry")
				writeAgentFile(t, f.checkout, "entry", "file\n")
			},
			check: func(t *testing.T, f agentResultTest, commit string) {
				if got := f.publishedContent(t, commit, "entry"); got != "file" {
					t.Fatalf("published %q", got)
				}
			},
		},
		{
			name: "a retargeted symbolic link",
			prepare: func(t *testing.T, f agentResultTest) {
				symlinkAgentPath(t, f, "first", "link")
			},
			change: func(t *testing.T, f agentResultTest) {
				removeAgentPath(t, f, "link")
				symlinkAgentPath(t, f, "second", "link")
			},
			check: func(t *testing.T, f agentResultTest, commit string) {
				if got := f.publishedContent(t, commit, "link"); got != "second" {
					t.Fatalf("published link target %q", got)
				}
			},
		},
		{
			name:    "a new ignore rule hides a copied file",
			prepare: func(t *testing.T, f agentResultTest) { writeAgentFile(t, f.checkout, "build/out.txt", "out\n") },
			change: func(t *testing.T, f agentResultTest) {
				writeAgentFile(t, f.checkout, ".gitignore", "build/\n")
			},
			check: func(t *testing.T, f agentResultTest, commit string) {
				if slices.Contains(f.trustedPaths(t, commit), "build/out.txt") {
					t.Fatal("a newly ignored file was published")
				}
			},
		},
		{
			name: "a removed nested ignore file no longer hides files",
			prepare: func(t *testing.T, f agentResultTest) {
				writeAgentFile(t, f.checkout, "pkg/.gitignore", "hidden.txt\n")
				writeAgentFile(t, f.checkout, "pkg/hidden.txt", "now visible\n")
			},
			change: func(t *testing.T, f agentResultTest) {
				removeAgentPath(t, f, "pkg/.gitignore")
			},
			check: func(t *testing.T, f agentResultTest, commit string) {
				if got := f.publishedContent(t, commit, "pkg/hidden.txt"); got != "now visible" {
					t.Fatalf("published %q", got)
				}
			},
		},
		{
			name:    "a file made executable",
			prepare: func(t *testing.T, f agentResultTest) { writeAgentFile(t, f.checkout, "run.sh", "#!/bin/sh\n") },
			change: func(t *testing.T, f agentResultTest) {
				if err := os.Chmod(filepath.Join(f.checkout, "run.sh"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, f agentResultTest, commit string) {
				if mode := f.trustedGit(t, "ls-tree", commit, "run.sh"); !strings.HasPrefix(mode, "100755 ") {
					t.Fatalf("ls-tree = %q, want an executable file", mode)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentResultTest(t)
			f.repository().Trusted.racyWindow = 0
			tc.prepare(t, f)
			ageAgentTree(t, f.checkout)
			f.publish(t)
			tc.change(t, f)
			tc.check(t, f, f.publish(t))
		})
	}
}

func TestSnapshotKeepsSeparateCachesPerCandidate(t *testing.T) {
	f := newAgentResultTest(t)
	f.repository().Trusted.racyWindow = 0
	worktree := f.addWorktree(t, ".claude/worktrees/feature", "feature-wt")
	writeAgentFile(t, worktree, "feature.txt", "from the worktree\n")

	commit := f.publish(t)

	if caches := len(f.repository().Trusted.caches); caches != 2 {
		t.Fatalf("candidate caches = %d, want 2", caches)
	}
	if got := f.publishedContent(t, commit, "feature.txt"); got != "from the worktree" {
		t.Fatalf("published %q", got)
	}
}

func TestSnapshotEvictsTheLeastRecentlyUsedCandidateCache(t *testing.T) {
	f := newAgentResultTest(t)
	trusted := f.repository().Trusted
	trusted.racyWindow = 0
	trusted.cacheLimit = 1
	worktree := f.addWorktree(t, ".claude/worktrees/feature", "feature-wt")
	writeAgentFile(t, worktree, "feature.txt", "from the worktree\n")

	first := f.publish(t)
	second := f.publish(t)

	if first != second {
		t.Fatalf("snapshots differ after eviction: %s and %s", first, second)
	}
	if caches := len(trusted.caches); caches != 1 {
		t.Fatalf("candidate caches = %d, want 1", caches)
	}
	entries, err := os.ReadDir(filepath.Join(trusted.root, "candidates"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 2 {
		t.Fatalf("evicted candidate files remain: %d entries", len(entries))
	}
}

func TestSnapshotDropsTheCacheAfterAFailure(t *testing.T) {
	f := newAgentResultTest(t)
	trusted := f.repository().Trusted
	trusted.racyWindow = 0
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	expected := f.publish(t)
	cache := singleCandidateCache(t, f)
	makeFifo(t, filepath.Join(f.checkout, "pipe"))

	_, err := f.jr.resolveAgentResult(context.Background(), f.repository(), "build result")

	var resultErr *agentResultError
	if !errors.As(err, &resultErr) {
		t.Fatalf("err = %v, want the special file refusal", err)
	}
	if len(trusted.caches) != 0 {
		t.Fatal("a failed snapshot kept its cache")
	}
	if _, statErr := os.Lstat(cache.files); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the failed candidate's files remain: %v", statErr)
	}
	removeAgentPath(t, f, "pipe")
	if got := f.publish(t); got != expected {
		t.Fatalf("snapshot after recovery = %s, want %s", got, expected)
	}
}

func removeAgentPath(t *testing.T, f agentResultTest, name string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(f.checkout, filepath.FromSlash(name))); err != nil {
		t.Fatal(err)
	}
}

func symlinkAgentPath(t *testing.T, f agentResultTest, target, name string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(f.checkout, filepath.FromSlash(name))); err != nil {
		t.Fatal(err)
	}
}

func ageAgentTree(t *testing.T, root string) {
	t.Helper()
	past := time.Now().Add(-time.Hour)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		return os.Chtimes(path, past, past)
	})
	if err != nil {
		t.Fatal(err)
	}
}
