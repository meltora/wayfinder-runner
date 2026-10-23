package daemon

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/example/app-runner/internal/validation"
)

const maxPackedRefsBytes = 8 << 20

type submoduleProblem int

const (
	submoduleReadable submoduleProblem = iota
	submoduleUnreadable
	submoduleOutside
	submoduleNotGitDir
	submoduleShared
)

func (t *trustedResultRepository) submodulePaths(ctx context.Context, commit string) (map[string]bool, error) {
	declared := map[string]bool{}
	if _, err := t.git(ctx, "rev-parse", "--verify", "--quiet", commit+":.gitmodules"); err != nil {
		return declared, ctx.Err()
	}
	output, err := t.git(ctx, "config", "-z", "--no-includes", "--blob", commit+":.gitmodules", "--get-regexp", `^submodule\..*\.path$`)
	if err != nil {
		return declared, ctx.Err()
	}
	for _, record := range strings.Split(string(output), "\x00") {
		if _, value, ok := strings.Cut(record, "\n"); ok && value != "" {
			declared[value] = true
		}
	}
	return declared, nil
}

func (t *trustedResultRepository) recordSubmodulePointers(ctx context.Context, workspace *os.Root, cache *candidateCache, input agentSnapshot, label string) error {
	paths := make([]string, 0, len(t.declaredSubmodules))
	for gitlink := range t.declaredSubmodules {
		paths = append(paths, gitlink)
	}
	sort.Strings(paths)
	for _, gitlink := range paths {
		if skippedBy(input.skip, gitlink) {
			continue
		}
		head, problem := readSubmoduleHead(workspace, input.workspaceRoot, path.Join(input.candidate.relPath, gitlink))
		if problem != submoduleReadable {
			return submoduleRefusal(input.repositoryID, label, gitlink, problem)
		}
		if head == "" || head == t.gitlinkCommits[gitlink] {
			continue
		}
		if _, err := t.gitAt(ctx, t.indexedGit(cache), "update-index", "--cacheinfo", gitlinkMode+","+head+","+gitlink); err != nil {
			return err
		}
	}
	return nil
}

func skippedBy(skip map[string]bool, rel string) bool {
	for skipped := range skip {
		if within(rel, skipped) {
			return true
		}
	}
	return false
}

func submoduleRefusal(repositoryID, label, gitlink string, problem submoduleProblem) error {
	name := displayEntry(gitlink)
	switch problem {
	case submoduleOutside:
		return agentResultRefusal(repositoryID, "%s cannot be published: submodule %s has Git metadata outside the job workspace", label, name)
	case submoduleNotGitDir:
		return agentResultRefusal(repositoryID, "%s cannot be published: submodule %s has a .git that is neither a directory nor a gitdir file", label, name)
	case submoduleShared:
		return agentResultRefusal(repositoryID, "%s cannot be published: submodule %s uses a shared Git directory with a branch checked out; detach its HEAD at the commit to publish", label, name)
	default:
		return agentResultRefusal(repositoryID, "%s cannot be published: submodule %s has a HEAD the runner cannot read; check out a commit in the submodule or remove its .git", label, name)
	}
}

func readSubmoduleHead(workspace *os.Root, workspaceRoot, subRel string) (string, submoduleProblem) {
	if err := requireWorkspaceDirectories(workspace, subRel); err != nil {
		return "", submoduleReadable
	}
	gitRel := path.Join(subRel, ".git")
	info, err := workspace.Lstat(gitRel)
	if errors.Is(err, fs.ErrNotExist) {
		return "", submoduleReadable
	}
	if err != nil {
		return "", submoduleUnreadable
	}
	gitDirRel := gitRel
	switch {
	case info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()):
		return "", submoduleNotGitDir
	case info.Mode().IsRegular():
		resolved, problem := submoduleGitFile(workspace, workspaceRoot, subRel)
		if problem != submoduleReadable {
			return "", problem
		}
		gitDirRel = resolved
	}
	if err := requireWorkspaceDirectories(workspace, gitDirRel); err != nil {
		return "", submoduleUnreadable
	}
	return submoduleHeadCommit(workspace, gitDirRel)
}

func submoduleGitFile(workspace *os.Root, workspaceRoot, subRel string) (string, submoduleProblem) {
	content, err := readWorkspaceFile(workspace, path.Join(subRel, ".git"), maxWorktreePointerBytes)
	if err != nil {
		return "", submoduleUnreadable
	}
	line, ok := singleLine(string(content))
	target, found := strings.CutPrefix(line, "gitdir: ")
	if !ok || !found || target == "" {
		return "", submoduleUnreadable
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(workspaceRoot, filepath.FromSlash(subRel), target)
	}
	rel, inside := workspaceRelativeLocation(workspaceRoot, filepath.Clean(target))
	if !inside {
		return "", submoduleOutside
	}
	if !safeWorkspacePath(rel) {
		return "", submoduleUnreadable
	}
	return rel, submoduleReadable
}

func submoduleHeadCommit(workspace *os.Root, gitDirRel string) (string, submoduleProblem) {
	content, err := readWorkspaceFile(workspace, path.Join(gitDirRel, "HEAD"), maxWorktreePointerBytes)
	if err != nil {
		return "", submoduleUnreadable
	}
	line, ok := singleLine(string(content))
	if !ok {
		return "", submoduleUnreadable
	}
	if validation.GitCommitSHA("submodule_head", line) == nil {
		return line, submoduleReadable
	}
	ref, found := strings.CutPrefix(line, "ref: ")
	if !found || !strings.HasPrefix(ref, "refs/") || !safeWorkspacePath(ref) {
		return "", submoduleUnreadable
	}
	if _, err := workspace.Lstat(path.Join(gitDirRel, "commondir")); !errors.Is(err, fs.ErrNotExist) {
		return "", submoduleShared
	}
	loose, err := readWorkspaceFile(workspace, path.Join(gitDirRel, ref), maxWorktreePointerBytes)
	if err == nil {
		commit, ok := singleLine(string(loose))
		if ok && validation.GitCommitSHA("submodule_ref", commit) == nil {
			return commit, submoduleReadable
		}
		return "", submoduleUnreadable
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", submoduleUnreadable
	}
	return packedRefCommit(workspace, gitDirRel, ref)
}

func packedRefCommit(workspace *os.Root, gitDirRel, ref string) (string, submoduleProblem) {
	packed, err := readWorkspaceFile(workspace, path.Join(gitDirRel, "packed-refs"), maxPackedRefsBytes)
	if err != nil {
		return "", submoduleUnreadable
	}
	for _, line := range strings.Split(string(packed), "\n") {
		commit, name, ok := strings.Cut(strings.TrimSuffix(line, "\r"), " ")
		if ok && name == ref && validation.GitCommitSHA("submodule_ref", commit) == nil {
			return commit, submoduleReadable
		}
	}
	return "", submoduleUnreadable
}
