package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/repo"
)

func requireWorktreeFeatureCheck(f agentResultTest) {
	f.jr.job.RequiredChecks = []string{"feature-file"}
	f.jr.d.cfg.Checks = map[string]config.CheckConfig{
		"feature-file": {Command: "test", Args: []string{"-f", "feature.txt"}},
	}
}

func TestRequiredCheckRunsInTheWorktreeCandidate(t *testing.T) {
	f := newAgentResultTest(t)
	worktree := f.addWorktree(t, ".claude/worktrees/feature", "agent-feature")
	writeAgentFile(t, worktree, "feature.txt", "real")
	requireWorktreeFeatureCheck(f)
	rm := repo.NewManager(filepath.Join(f.jr.d.cfg.StateDir, "repos"))
	rm.AllowLocal = true
	state, extra, err := f.jr.finalizeBuild(context.Background(), rm, false)
	if err != nil || state != "succeeded" {
		t.Fatalf("state=%s extra=%v err=%v", state, extra, err)
	}
	events := f.events.byKind("test_results")
	if len(events) != 1 || events[0]["phase"] != "terminal" || events[0]["result"] != "passed" || events[0]["commit_sha"] != extra["head_sha"] {
		t.Fatalf("terminal check events = %v, head %v", events, extra["head_sha"])
	}
}

func TestRunCheckRunsInTheWorktreeCandidateAndReportsItsResultCommit(t *testing.T) {
	f := newAgentResultTest(t)
	worktree := filepath.Join(f.jr.workDir, "feature")
	agentGit(t, f.checkout, "worktree", "add", "-q", "-b", "agent-feature", worktree)
	writeAgentFile(t, worktree, "feature.txt", "real")
	requireWorktreeFeatureCheck(f)
	response, err := call(t, f.jr, "platform_run_check", `{"check_id":"feature-file","idempotency_key":"rc-worktree"}`)
	if err != nil {
		t.Fatal(err)
	}
	result := response.(map[string]any)
	set, err := f.jr.previewCommitSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result["result"] != "passed" || result["worktree_dirty"] != false || result["commit_sha"] != set["repo-1"] || set["repo-1"] == f.base {
		t.Fatalf("run-check result = %v, preview %v", result, set)
	}
	if paths := f.trustedPaths(t, set["repo-1"]); !strings.Contains(strings.Join(paths, "\n"), "feature.txt") {
		t.Fatalf("result commit paths = %v", paths)
	}
}

func TestRunCheckAndPauseRefuseAnAmbiguousResult(t *testing.T) {
	f := newAgentResultTest(t)
	worktree := f.addWorktree(t, ".claude/worktrees/feature", "agent-feature")
	writeAgentFile(t, worktree, "feature.txt", "worktree")
	writeAgentFile(t, f.checkout, "feature.txt", "checkout")
	requireWorktreeFeatureCheck(f)
	if _, err := call(t, f.jr, "platform_run_check", `{"check_id":"feature-file","idempotency_key":"rc-ambiguous"}`); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous run-check: %v", err)
	}
	if events := f.events.byKind("test_results"); len(events) != 0 {
		t.Fatalf("ambiguous run-check emitted evidence: %v", events)
	}
	var resultErr *agentResultError
	if _, err := f.jr.snapshotRepository(context.Background(), f.repository()); !errors.As(err, &resultErr) || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous pause: %v", err)
	}
}

func TestRefusedResultLeavesTheTrustedJobBranchUnset(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, "Shortstack-Artifacts/coverage.json", "{}")
	if _, err := f.jr.commitAgentChanges(context.Background(), f.repository()); err == nil || !strings.Contains(strings.ToLower(err.Error()), "shortstack-artifacts") {
		t.Fatalf("case-folded artifact accepted: %v", err)
	}
	if _, err := trustedGitTry(f.repository().Trusted.gitDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+f.branch); err == nil {
		t.Fatal("a refused result moved the trusted job branch")
	}
}

func TestPauseRecordsTheWorktreeResultCommit(t *testing.T) {
	for _, location := range []string{"repositories/repo-1/.claude/worktrees/feature", "feature"} {
		t.Run(location, func(t *testing.T) {
			f := newAgentResultTest(t)
			worktree := filepath.Join(f.jr.workDir, filepath.FromSlash(location))
			agentGit(t, f.checkout, "worktree", "add", "-q", "-b", "agent-feature", worktree)
			agentCommit(t, worktree, "feature.txt", "committed")
			writeAgentFile(t, worktree, "draft.txt", "uncommitted")
			snapshot, err := f.jr.snapshotRepository(context.Background(), f.repository())
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.CommitSHA == f.base || snapshot.PatchDigestSHA256 != "" || snapshot.Branch != f.branch {
				t.Fatalf("snapshot = %+v", snapshot)
			}
			paths := strings.Join(f.trustedPaths(t, snapshot.CommitSHA), "\n")
			if !strings.Contains(paths, "feature.txt") || !strings.Contains(paths, "draft.txt") || strings.Contains(paths, ".claude") {
				t.Fatalf("checkpoint commit paths = %s", paths)
			}
			if sha := f.publish(t); sha != snapshot.CommitSHA {
				t.Fatalf("build result %s differs from the checkpoint %s for the same files", sha, snapshot.CommitSHA)
			}
		})
	}
}
