package daemon

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/example/app-runner/internal/validation"
)

const (
	agentResultCheckout = "checkout"
	agentResultWorktree = "worktree"
	maxNamedCandidates  = 8
)

type agentResultError struct{ message string }

func (e *agentResultError) Error() string { return e.message }

func agentResultRefusal(repositoryID, format string, args ...any) error {
	return &agentResultError{message: "agent result refused in repository " + repositoryID + ": " + fmt.Sprintf(format, args...)}
}

func agentResultAmbiguity(repositoryID, format string, args ...any) error {
	return &agentResultError{message: "agent result is ambiguous in repository " + repositoryID + ": " + fmt.Sprintf(format, args...)}
}

type agentCandidate struct {
	source      string
	relPath     string
	adminRel    string
	tree        string
	changes     map[string]string
	ignoredDirs []string
}

type agentResult struct {
	candidate agentCandidate
	commit    string
	dir       string
}

func startCommit(repository checkedOutRepository) string {
	if repository.StartSHA != "" {
		return repository.StartSHA
	}
	return repository.BaseSHA
}

func (jr *jobRunner) resolveAgentResult(ctx context.Context, repository checkedOutRepository, use string) (agentResult, error) {
	trusted := repository.Trusted
	if trusted == nil {
		return agentResult{}, agentResultRefusal(repository.ID, "the trusted result repository is not available")
	}
	start := startCommit(repository)
	if err := validation.GitCommitSHA("start_sha", start); err != nil {
		return agentResult{}, agentResultRefusal(repository.ID, "the start commit is invalid")
	}
	trusted.mu.Lock()
	defer trusted.mu.Unlock()
	bounded, cancel := trusted.resolutionContext(ctx)
	defer cancel()
	result, err := jr.resolveWithinBound(bounded, trusted, repository, start)
	if err != nil {
		return agentResult{}, err
	}
	if result.candidate.source == agentResultWorktree {
		jr.auditAgentResult(ctx, repository, result, use)
	}
	return result, nil
}

func (jr *jobRunner) resolveWithinBound(ctx context.Context, trusted *trustedResultRepository, repository checkedOutRepository, start string) (agentResult, error) {
	workspace, err := os.OpenRoot(jr.workDir)
	if err != nil {
		return agentResult{}, err
	}
	defer func() { _ = workspace.Close() }()
	candidates, err := discoverAgentCandidates(workspace, jr.workDir, repository, jr.repositories)
	if err != nil {
		return agentResult{}, err
	}
	commonGitRel := path.Join(candidates[0].relPath, ".git")
	excludeRel := path.Join(commonGitRel, "info", "exclude")
	for index := range candidates {
		candidate := &candidates[index]
		label := describeCandidate(*candidate)
		outcome, err := trusted.snapshot(ctx, workspace, agentSnapshot{
			candidate: *candidate, skip: nestedCandidates(candidates, candidate.relPath),
			excludeRel: excludeRel, commonGitRel: commonGitRel, start: start, repositoryID: repository.ID,
			workspaceRoot: jr.workDir,
		})
		if err != nil {
			return agentResult{}, trusted.stepRefusal(ctx, repository.ID, label, err)
		}
		changes, err := trusted.changeSet(ctx, start, outcome.tree)
		if err != nil {
			return agentResult{}, trusted.stepRefusal(ctx, repository.ID, label, err)
		}
		candidate.tree, candidate.changes, candidate.ignoredDirs = outcome.tree, changes, outcome.ignoredDirs
	}
	winner, err := selectAgentResult(repository.ID, candidates)
	if err != nil {
		return agentResult{}, err
	}
	if err := trusted.refuseRawLFSContent(ctx, repository.ID, describeCandidate(winner), winner); err != nil {
		return agentResult{}, trusted.stepRefusal(ctx, repository.ID, describeCandidate(winner), err)
	}
	result := agentResult{candidate: winner, commit: start, dir: filepath.Join(jr.workDir, filepath.FromSlash(winner.relPath))}
	if len(winner.changes) > 0 {
		message := fmt.Sprintf("agent: implement %s %s", jr.job.Ticket.Key, jr.job.Ticket.Summary)
		if result.commit, err = trusted.commitTree(ctx, winner.tree, start, message); err != nil {
			return agentResult{}, trusted.stepRefusal(ctx, repository.ID, describeCandidate(winner), err)
		}
	}
	return result, nil
}

func nestedCandidates(candidates []agentCandidate, parent string) map[string]bool {
	skip := map[string]bool{}
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate.relPath, parent+"/") {
			skip[strings.TrimPrefix(candidate.relPath, parent+"/")] = true
		}
	}
	return skip
}

func (t *trustedResultRepository) changeSet(ctx context.Context, start, tree string) (map[string]string, error) {
	raw, err := t.git(ctx, "diff-tree", "-r", "-z", "--raw", "--no-renames", "--no-abbrev", "--ignore-submodules=none", start, tree)
	if err != nil {
		return nil, err
	}
	changes := map[string]string{}
	fields := strings.Split(string(raw), "\x00")
	for index := 0; index+1 < len(fields); index += 2 {
		header := fields[index]
		if header == "" {
			break
		}
		parts := strings.Fields(strings.TrimPrefix(header, ":"))
		if !strings.HasPrefix(header, ":") || len(parts) < 5 {
			return nil, fmt.Errorf("unexpected trusted diff output")
		}
		changes[fields[index+1]] = parts[1] + " " + parts[3]
	}
	return changes, nil
}

func (t *trustedResultRepository) commitTree(ctx context.Context, tree, parent, message string) (string, error) {
	identity := []string{
		"GIT_AUTHOR_NAME=our-runner", "GIT_AUTHOR_EMAIL=runner@localhost", "GIT_AUTHOR_DATE=" + t.date,
		"GIT_COMMITTER_NAME=our-runner", "GIT_COMMITTER_EMAIL=runner@localhost", "GIT_COMMITTER_DATE=" + t.date,
	}
	output, err := t.gitAt(ctx, trustedGitOptions{gitDir: t.gitDir, workTree: t.filesDir, env: identity},
		"commit-tree", tree, "-p", parent, "-m", message)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(output))
	if err := validation.GitCommitSHA("result_commit", commit); err != nil {
		return "", err
	}
	return commit, nil
}

func selectAgentResult(repositoryID string, candidates []agentCandidate) (agentCandidate, error) {
	var changed []agentCandidate
	for _, candidate := range candidates {
		if len(candidate.changes) > 0 {
			changed = append(changed, candidate)
		}
	}
	if len(changed) == 0 {
		return candidates[0], nil
	}
	for _, candidate := range changed {
		if containsAll(candidate, changed) {
			return candidate, nil
		}
	}
	var maximal []agentCandidate
	for _, candidate := range changed {
		dominated := false
		for _, other := range changed {
			if other.relPath != candidate.relPath && contains(other, candidate) && !contains(candidate, other) {
				dominated = true
				break
			}
		}
		if !dominated {
			maximal = append(maximal, candidate)
		}
	}
	return agentCandidate{}, agentResultAmbiguity(repositoryID,
		"%s hold different changes; keep the work in one checkout or worktree", describeCandidates(maximal))
}

func containsAll(candidate agentCandidate, others []agentCandidate) bool {
	for _, other := range others {
		if !contains(candidate, other) {
			return false
		}
	}
	return true
}

func contains(candidate, other agentCandidate) bool {
	for path, change := range other.changes {
		if candidate.changes[path] != change {
			return false
		}
	}
	return true
}

func describeCandidates(candidates []agentCandidate) string {
	names := make([]string, 0, len(candidates))
	for index, candidate := range candidates {
		if index == maxNamedCandidates {
			names = append(names, fmt.Sprintf("and %d more", len(candidates)-maxNamedCandidates))
			break
		}
		names = append(names, describeCandidate(candidate))
	}
	return strings.Join(names, ", ")
}

func describeCandidate(candidate agentCandidate) string {
	if candidate.source == agentResultCheckout {
		return "the checkout " + candidate.relPath
	}
	return "worktree " + candidate.relPath
}

func (jr *jobRunner) auditAgentResult(ctx context.Context, repository checkedOutRepository, result agentResult, use string) {
	task := fmt.Sprintf("%s for repository %s uses agent worktree %s at commit %s", use, repository.ID, result.candidate.relPath, result.commit)
	jr.logger.Info("agent result resolved", "repository", repository.ID, "source", result.candidate.source,
		"path", result.candidate.relPath, "commit", result.commit, "use", use)
	jr.publishProgress(ctx, "agent_result_resolved", task, "")
}
