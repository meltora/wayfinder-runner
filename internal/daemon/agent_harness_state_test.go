package daemon

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func assertNoHarnessEntry(t *testing.T, paths []string) {
	t.Helper()
	for _, path := range paths {
		if strings.Contains(path, ".cc-writes") {
			t.Fatalf("harness state reached the result: %q in %v", path, paths)
		}
	}
}

func plantStagingDirectory(t *testing.T, root string, mode os.FileMode) {
	t.Helper()
	writeAgentFile(t, root, ".claude/.cc-writes/settings.json.tmp", "{\"staged\":true}\n")
	staging := filepath.Join(root, ".claude", ".cc-writes")
	if err := os.Chmod(staging, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(staging, 0o700) })
}

func TestAgentHarnessStateListNamesOnlyTheClaudeStagingDirectory(t *testing.T) {
	if got := agentHarnessStatePaths(); !slices.Equal(got, []string{".claude/.cc-writes"}) {
		t.Fatalf("harness state paths = %v", got)
	}
}

func TestPublishLeavesOutAnUntrackedHarnessStagingDirectory(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	plantStagingDirectory(t, f.checkout, 0o700)

	sha := f.publish(t)

	paths := f.trustedPaths(t, sha)
	assertNoHarnessEntry(t, paths)
	if !slices.Contains(paths, "feature.txt") {
		t.Fatalf("published paths = %v", paths)
	}
}

func TestPushedBuildBranchLeavesOutTheHarnessStagingDirectory(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	plantStagingDirectory(t, f.checkout, 0o700)

	if err := f.finalizeWithPush(t); err != nil {
		t.Fatal(err)
	}

	paths := f.originPaths(t)
	assertNoHarnessEntry(t, paths)
	if !slices.Contains(paths, "feature.txt") {
		t.Fatalf("pushed paths = %v", paths)
	}
}

func TestPublishSkipsAnUntrackedHarnessDirectoryTheRunnerCannotRead(t *testing.T) {
	requireNonRootPermissions(t)
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	plantStagingDirectory(t, f.checkout, 0o000)

	sha := f.publish(t)

	paths := f.trustedPaths(t, sha)
	assertNoHarnessEntry(t, paths)
	if !slices.Contains(paths, "feature.txt") {
		t.Fatalf("published paths = %v", paths)
	}
}

func TestIterativeCheckSnapshotSkipsAnUnreadableHarnessDirectory(t *testing.T) {
	requireNonRootPermissions(t)
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	plantStagingDirectory(t, f.checkout, 0o000)

	result, err := f.jr.resolveAgentResult(context.Background(), f.repository(), "agent check go-test")
	if err != nil {
		t.Fatalf("the iterative snapshot was refused: %v", err)
	}

	paths := f.trustedPaths(t, result.commit)
	assertNoHarnessEntry(t, paths)
	if !slices.Contains(paths, "feature.txt") {
		t.Fatalf("snapshot paths = %v", paths)
	}
	if _, ok := result.candidate.changes[".claude/.cc-writes/settings.json.tmp"]; ok {
		t.Fatalf("change set = %v", result.candidate.changes)
	}
}

func TestPublishLeavesTheResultUnchangedWhenOnlyHarnessStateWasWritten(t *testing.T) {
	f := newAgentResultTest(t)
	plantStagingDirectory(t, f.checkout, 0o700)

	sha := f.publish(t)

	if sha != f.base {
		t.Fatalf("harness state alone produced a result commit %s over %s", sha, f.base)
	}
}

func TestPublishComparesAHarnessPathTrackedAtTheStartCommit(t *testing.T) {
	f := newAgentResultTestWith(t, func(origin string) {
		writeAgentFile(t, origin, ".claude/.cc-writes/tracked.txt", "base\n")
	})
	writeAgentFile(t, f.checkout, ".claude/.cc-writes/tracked.txt", "changed by the agent\n")

	sha := f.publish(t)

	_, object := treeEntry(t, f, sha, ".claude/.cc-writes/tracked.txt")
	if content := f.trustedGit(t, "cat-file", "blob", object); content != "changed by the agent" {
		t.Fatalf("tracked harness path content = %q", content)
	}
}

func TestPublishRefusesATrackedHarnessPathTheRunnerCannotRead(t *testing.T) {
	requireNonRootPermissions(t)
	f := newAgentResultTestWith(t, func(origin string) {
		writeAgentFile(t, origin, ".claude/.cc-writes/tracked.txt", "base\n")
	})
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	staging := filepath.Join(f.checkout, ".claude", ".cc-writes")
	if err := os.Chmod(staging, 0o000); err != nil {
		t.Fatal(err)
	}
	restoreMode(t, staging, 0o700)

	assertRefused(t, f, unreadableEntryRefusal+".claude/.cc-writes")
}

func TestPublishSkipsAnUntrackedHarnessSymlinkWithoutFollowingIt(t *testing.T) {
	f := newAgentResultTest(t)
	outside := t.TempDir()
	writeAgentFile(t, outside, "secret.txt", "HOST-SECRET\n")
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")
	if err := os.MkdirAll(filepath.Join(f.checkout, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.checkout, ".claude", ".cc-writes")); err != nil {
		t.Fatal(err)
	}

	sha := f.publish(t)

	paths := f.trustedPaths(t, sha)
	assertNoHarnessEntry(t, paths)
	if _, err := trustedGitTry(f.repository().Trusted.gitDir, "grep", "-q", "HOST-SECRET", sha); err == nil {
		t.Fatal("the symlink target was published")
	}
}

func TestPublishKeepsPathsOutsideTheHarnessList(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, ".claude/settings.local.json", "{}\n")
	writeAgentFile(t, f.checkout, "src/.cc-writes/kept.txt", "kept\n")
	writeAgentFile(t, f.checkout, ".claude/.cc-writes-other/kept.txt", "kept\n")
	plantStagingDirectory(t, f.checkout, 0o700)

	sha := f.publish(t)

	paths := f.trustedPaths(t, sha)
	for _, want := range []string{".claude/settings.local.json", "src/.cc-writes/kept.txt", ".claude/.cc-writes-other/kept.txt"} {
		if !slices.Contains(paths, want) {
			t.Fatalf("%s was skipped: %v", want, paths)
		}
	}
	if slices.Contains(paths, ".claude/.cc-writes/settings.json.tmp") {
		t.Fatalf("listed path was published: %v", paths)
	}
}

func TestPublishLeavesHarnessStateOutOfEveryCandidateRoot(t *testing.T) {
	f := newAgentResultTest(t)
	wt := f.addWorktree(t, ".claude/worktrees/x", "agent-x")
	plantStagingDirectory(t, f.checkout, 0o700)
	plantStagingDirectory(t, wt, 0o700)
	writeAgentFile(t, wt, "feature.txt", "worktree work\n")

	sha := f.publish(t)

	paths := f.trustedPaths(t, sha)
	assertNoHarnessEntry(t, paths)
	if !slices.Contains(paths, "feature.txt") {
		t.Fatalf("published paths = %v", paths)
	}
}

func TestSnapshotSkipsCombineNestedCandidatesAndHarnessState(t *testing.T) {
	harness := map[string]bool{".claude/.cc-writes": true}
	for _, tc := range []struct {
		name       string
		candidates []agentCandidate
		parent     string
		want       []string
	}{
		{"no candidate roots", nil, "repo", []string{".claude/.cc-writes"}},
		{"one candidate root", []agentCandidate{{relPath: "repo"}}, "repo", []string{".claude/.cc-writes"}},
		{"two candidate roots", []agentCandidate{{relPath: "repo"}, {relPath: "repo/.claude/worktrees/x"}}, "repo",
			[]string{".claude/.cc-writes", ".claude/worktrees/x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := snapshotSkips(tc.candidates, tc.parent, harness)
			keys := slices.Sorted(maps.Keys(got))
			if !slices.Equal(keys, tc.want) {
				t.Fatalf("skips = %v, want %v", keys, tc.want)
			}
		})
	}
	if got := snapshotSkips([]agentCandidate{{relPath: "repo"}}, "repo", nil); len(got) != 0 {
		t.Fatalf("skips without harness state = %v", got)
	}
}
