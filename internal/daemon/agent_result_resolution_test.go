package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func assertRunnerCommit(t *testing.T, f agentResultTest, sha, parent string) {
	t.Helper()
	if parents := f.trustedGit(t, "rev-list", "--parents", "-n", "1", sha); parents != sha+" "+parent {
		t.Fatalf("result commit parents = %q, want single parent %s", parents, parent)
	}
	if identity := f.trustedGit(t, "log", "-1", "--format=%an <%ae>|%cn <%ce>|%s", sha); identity != "our-runner <runner@localhost>|our-runner <runner@localhost>|agent: implement KAN-7 Add feature" {
		t.Fatalf("result commit identity = %q", identity)
	}
	if branch := f.trustedGit(t, "rev-parse", "refs/heads/"+f.branch); branch != sha {
		t.Fatalf("trusted job branch = %s, want %s", branch, sha)
	}
}

func lastAudit(f agentResultTest) (map[string]any, bool) {
	progress := f.events.byKind("progress")
	for index := len(progress) - 1; index >= 0; index-- {
		if progress[index]["milestone"] == "agent_result_resolved" {
			return progress[index], true
		}
	}
	return nil, false
}

func TestBuildPublishesCommittedAgentWorktreeResult(t *testing.T) {
	f := newAgentResultTest(t)
	worktree := f.addWorktree(t, ".claude/worktrees/feature", "agent-feature")
	agentTip := agentCommit(t, worktree, "feature.txt", "implemented in worktree\n")

	sha := f.publish(t)

	if sha == agentTip || sha == f.base {
		t.Fatalf("published %s; agent tip %s, base %s", sha, agentTip, f.base)
	}
	assertRunnerCommit(t, f, sha, f.base)
	paths := f.trustedPaths(t, sha)
	if !slices.Contains(paths, "feature.txt") || !slices.Contains(paths, "README.md") {
		t.Fatalf("result tree = %v", paths)
	}
	for _, path := range paths {
		if strings.HasPrefix(path, ".claude/") {
			t.Fatalf("nested worktree path committed: %v", paths)
		}
	}
	if head := agentGit(t, f.checkout, "rev-parse", "HEAD"); head != f.base {
		t.Fatalf("runner moved the agent checkout to %s", head)
	}
	audit, ok := lastAudit(f)
	if !ok {
		t.Fatal("agent result resolution was not audited")
	}
	task, _ := audit["current_task"].(string)
	if !strings.Contains(task, "repositories/repo-1/.claude/worktrees/feature") || !strings.Contains(task, sha) ||
		!strings.Contains(task, "repo-1") || strings.Contains(task, "agent-feature") {
		t.Fatalf("audit task = %q", task)
	}
	assertNoHostPath(t, task, f.jr.workDir)
}

func TestBuildPublishesUncommittedAgentWorktreeFiles(t *testing.T) {
	f := newAgentResultTest(t)
	worktree := f.addWorktree(t, ".claude/worktrees/draft", "agent-draft")
	writeAgentFile(t, worktree, "draft.txt", "not committed by the agent\n")

	sha := f.publish(t)

	assertRunnerCommit(t, f, sha, f.base)
	if !slices.Contains(f.trustedPaths(t, sha), "draft.txt") {
		t.Fatal("uncommitted worktree file missing from the result")
	}
	if status := agentGit(t, worktree, "status", "--porcelain"); status == "" {
		t.Fatal("the runner changed the agent worktree")
	}
}

func TestBuildPublishesDetachedAgentWorktree(t *testing.T) {
	f := newAgentResultTest(t)
	worktree := filepath.Join(f.checkout, ".claude", "worktrees", "detached")
	agentGit(t, f.checkout, "worktree", "add", "-q", "--detach", worktree)
	agentCommit(t, worktree, "detached.txt", "detached\n")

	sha := f.publish(t)

	if !slices.Contains(f.trustedPaths(t, sha), "detached.txt") {
		t.Fatal("detached worktree result missing")
	}
}

func TestBuildIgnoresIdleWorktreeAndPublishesCheckoutChanges(t *testing.T) {
	f := newAgentResultTest(t)
	f.addWorktree(t, ".claude/worktrees/idle", "agent-idle")
	writeAgentFile(t, f.checkout, "checkout.txt", "checkout change\n")

	sha := f.publish(t)

	assertRunnerCommit(t, f, sha, f.base)
	if !slices.Contains(f.trustedPaths(t, sha), "checkout.txt") {
		t.Fatal("checkout change missing")
	}
	if _, ok := lastAudit(f); ok {
		t.Fatal("an idle worktree was reported as the result")
	}
}

func TestBuildUnchangedResultIsTheStartCommit(t *testing.T) {
	f := newAgentResultTest(t)
	if main := agentGit(t, f.checkout, "rev-parse", "refs/heads/main"); main == f.base {
		t.Fatal("fixture main did not advance past the base")
	}
	sha := f.publish(t)
	if sha != f.base {
		t.Fatalf("unchanged result = %s, want start commit %s", sha, f.base)
	}
	if branch := f.trustedGit(t, "rev-parse", "refs/heads/"+f.branch); branch != f.base {
		t.Fatalf("trusted job branch = %s", branch)
	}
}

func TestBuildDoesNotPublishWorkThatExistsOnlyOnABranch(t *testing.T) {
	f := newAgentResultTest(t)
	agentGit(t, f.checkout, "switch", "-q", "-c", "agent-only")
	agentCommit(t, f.checkout, "branch.txt", "committed on a side branch\n")
	agentGit(t, f.checkout, "switch", "-q", f.branch)

	if sha := f.publish(t); sha != f.base {
		t.Fatalf("branch-only work was published as %s", sha)
	}
}

func TestBuildPublishesCheckoutFilesWhenCheckoutIsOnAnotherBranch(t *testing.T) {
	f := newAgentResultTest(t)
	agentGit(t, f.checkout, "switch", "-q", "-c", "agent-elsewhere")
	agentCommit(t, f.checkout, "elsewhere.txt", "committed on another branch\n")

	sha := f.publish(t)

	assertRunnerCommit(t, f, sha, f.base)
	if !slices.Contains(f.trustedPaths(t, sha), "elsewhere.txt") {
		t.Fatal("checkout files on another branch were lost")
	}
}

func assertAmbiguous(t *testing.T, f agentResultTest, err error, names ...string) {
	t.Helper()
	var resultErr *agentResultError
	if !errors.As(err, &resultErr) || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous result accepted: %v", err)
	}
	for _, name := range names {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("message %q does not name %s", err.Error(), name)
		}
	}
	assertNoHostPath(t, err.Error(), f.jr.workDir, f.checkout)
	outcome := buildFinalizationFailure(fmt.Errorf("commit repo-1: %w", err))
	if outcome.FailureCode != v1.JobFailureBuildFinalization || outcome.FailureMessage != resultErr.Error() {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestBuildRefusesDivergentCheckoutAndWorktreeChanges(t *testing.T) {
	f := newAgentResultTest(t)
	worktree := f.addWorktree(t, ".claude/worktrees/side", "agent-side")
	agentCommit(t, worktree, "worktree.txt", "worktree change\n")
	writeAgentFile(t, f.checkout, "checkout.txt", "checkout change\n")

	_, err := f.jr.commitAgentChanges(context.Background(), f.repository())

	assertAmbiguous(t, f, err, "the checkout repositories/repo-1", "worktree repositories/repo-1/.claude/worktrees/side")
	if output, gitErr := trustedGitTry(f.repository().Trusted.gitDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+f.branch); gitErr == nil {
		t.Fatalf("job branch was set after an ambiguous result: %s", output)
	}
}

func TestBuildRefusesTwoDivergentWorktrees(t *testing.T) {
	f := newAgentResultTest(t)
	first := f.addWorktree(t, ".claude/worktrees/one", "agent-one")
	agentCommit(t, first, "one.txt", "one\n")
	second := filepath.Join(f.jr.workDir, "agent-two")
	agentGit(t, f.checkout, "worktree", "add", "-q", "-b", "agent-two", second)
	agentCommit(t, second, "two.txt", "two\n")

	_, err := f.jr.commitAgentChanges(context.Background(), f.repository())

	assertAmbiguous(t, f, err, "repositories/repo-1/.claude/worktrees/one", "agent-two")
}

func TestBuildPublishesCheckoutWhenWorktreeWorkWasMergedBack(t *testing.T) {
	f := newAgentResultTest(t)
	worktree := f.addWorktree(t, ".claude/worktrees/feature", "agent-feature")
	agentCommit(t, worktree, "feature.txt", "feature\n")
	agentGit(t, f.checkout, "merge", "-q", "--ff-only", "agent-feature")

	sha := f.publish(t)

	if !slices.Contains(f.trustedPaths(t, sha), "feature.txt") {
		t.Fatal("merged work missing")
	}
	if _, ok := lastAudit(f); ok {
		t.Fatal("the checkout should win when both hold the same changes")
	}
}

func TestBuildPublishesWorktreeThatContainsTheCheckoutChanges(t *testing.T) {
	f := newAgentResultTest(t)
	worktree := f.addWorktree(t, ".claude/worktrees/ahead", "agent-ahead")
	writeAgentFile(t, f.checkout, "shared.txt", "shared\n")
	writeAgentFile(t, worktree, "shared.txt", "shared\n")
	writeAgentFile(t, worktree, "extra.txt", "only in the worktree\n")

	sha := f.publish(t)

	paths := f.trustedPaths(t, sha)
	if !slices.Contains(paths, "shared.txt") || !slices.Contains(paths, "extra.txt") {
		t.Fatalf("result tree = %v", paths)
	}
	if _, ok := lastAudit(f); !ok {
		t.Fatal("worktree result was not audited")
	}
}

func TestBuildResultCommitIsDeterministic(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "same.txt", "same\n")

	first := f.publish(t)
	second := f.publish(t)
	commitSet, err := f.jr.previewCommitSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first != second || commitSet["repo-1"] != first {
		t.Fatalf("result commits differ: %s %s preview %s", first, second, commitSet["repo-1"])
	}
}

func TestBuildResolutionUsesContinuationStartCommit(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "continued.txt", "existing pull request head\n")
	start := f.publish(t)
	repository := f.repository()
	repository.StartSHA = start
	worktree := f.addWorktree(t, ".claude/worktrees/continue", "agent-continue")
	writeAgentFile(t, worktree, "continued.txt", "existing pull request head\n")
	writeAgentFile(t, worktree, "fix.txt", "requested change\n")
	writeAgentFile(t, f.checkout, "continued.txt", "existing pull request head\n")

	sha, err := f.jr.commitAgentChanges(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if parent := f.trustedGit(t, "rev-parse", sha+"^"); parent != start {
		t.Fatalf("continuation result parent = %s, want %s", parent, start)
	}
	if diff := f.trustedGit(t, "diff", "--name-only", start, sha); diff != "fix.txt" {
		t.Fatalf("continuation diff = %q", diff)
	}
}

func TestBuildPushesTheRunnerCommitToTheJobBranch(t *testing.T) {
	f := newAgentResultTest(t)
	worktree := f.addWorktree(t, ".claude/worktrees/push", "agent-push")
	agentCommit(t, worktree, "one.txt", "one\n")
	agentCommit(t, worktree, "two.txt", "two\n")

	if err := f.finalizeWithPush(t); err != nil {
		t.Fatal(err)
	}
	originGit := filepath.Join(f.origin, ".git")
	pushed := trustedGitForTest(t, originGit, "rev-parse", "refs/heads/"+f.branch)
	if count := trustedGitForTest(t, originGit, "rev-list", "--count", f.base+".."+pushed); count != "1" {
		t.Fatalf("origin received %s commits, want the single runner commit", count)
	}
	paths := f.originPaths(t)
	if !slices.Contains(paths, "one.txt") || !slices.Contains(paths, "two.txt") {
		t.Fatalf("origin tree = %v", paths)
	}
	if author := trustedGitForTest(t, originGit, "log", "-1", "--format=%an", pushed); author != "our-runner" {
		t.Fatalf("origin commit author = %q", author)
	}
}

func TestDescribeResultCandidatesIsBounded(t *testing.T) {
	var candidates []agentCandidate
	for index := 0; index < 10; index++ {
		candidates = append(candidates, agentCandidate{source: agentResultWorktree, relPath: fmt.Sprintf("wt-%d", index)})
	}
	description := describeCandidates(candidates)
	if !strings.Contains(description, "and 2 more") || strings.Contains(description, "wt-9") {
		t.Fatalf("description = %q", description)
	}
	if describeCandidates([]agentCandidate{{source: agentResultCheckout, relPath: "repositories/repo-1"}}) != "the checkout repositories/repo-1" {
		t.Fatal("checkout description changed")
	}
}

func TestResultRequiresATrustedRepository(t *testing.T) {
	f := newAgentResultTest(t)
	repository := f.repository()
	repository.Trusted = nil
	_, err := f.jr.commitAgentChanges(context.Background(), repository)
	var resultErr *agentResultError
	if !errors.As(err, &resultErr) || !strings.Contains(err.Error(), "trusted result repository") {
		t.Fatalf("missing trusted repository accepted: %v", err)
	}
	if _, err := os.Stat(f.repository().Trusted.gitDir); err != nil {
		t.Fatal(err)
	}
}
