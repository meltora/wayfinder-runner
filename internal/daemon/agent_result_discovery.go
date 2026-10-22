package daemon

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	maxWorktreePointerBytes = 4096
	maxAgentResultRefs      = 128
)

var safeWorkspacePathPattern = regexp.MustCompile(`^[A-Za-z0-9._@+-]+(/[A-Za-z0-9._@+-]+)*$`)

type agentDiscovery struct {
	workspace     *os.Root
	workspaceRoot string
	repository    checkedOutRepository
	others        []checkedOutRepository
	checkoutRel   string
}

func discoverAgentCandidates(workspace *os.Root, workspaceRoot string, repository checkedOutRepository, all []checkedOutRepository) ([]agentCandidate, error) {
	checkoutRel, ok := relativeWithin(workspaceRoot, repository.WorkDir)
	if !ok || !safeWorkspacePath(checkoutRel) {
		return nil, agentResultRefusal(repository.ID, "the repository checkout is outside the job workspace")
	}
	if err := requireWorkspaceDirectories(workspace, checkoutRel); err != nil {
		return nil, agentResultRefusal(repository.ID, "the repository checkout %s is not a real directory", checkoutRel)
	}
	discovery := agentDiscovery{
		workspace: workspace, workspaceRoot: workspaceRoot, repository: repository, checkoutRel: checkoutRel,
	}
	for _, other := range all {
		if other.ID != repository.ID {
			discovery.others = append(discovery.others, other)
		}
	}
	checkout := agentCandidate{source: agentResultCheckout, relPath: checkoutRel, adminRel: path.Join(checkoutRel, ".git")}
	names, err := discovery.adminEntries()
	if err != nil {
		return nil, err
	}
	candidates := []agentCandidate{checkout}
	var worktrees []agentCandidate
	for _, name := range names {
		worktree, present, err := discovery.worktree(name)
		if err != nil {
			return nil, err
		}
		if present {
			worktrees = append(worktrees, worktree)
		}
	}
	sort.Slice(worktrees, func(i, j int) bool { return worktrees[i].relPath < worktrees[j].relPath })
	return append(candidates, worktrees...), nil
}

func (d agentDiscovery) adminEntries() ([]string, error) {
	gitRel := path.Join(d.checkoutRel, ".git")
	info, err := d.workspace.Lstat(gitRel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, agentResultRefusal(d.repository.ID, "the checkout Git metadata .git is not a real directory")
	}
	adminRoot := path.Join(gitRel, "worktrees")
	info, err = d.workspace.Lstat(adminRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, agentResultRefusal(d.repository.ID, "the checkout Git metadata .git/worktrees is not a real directory")
	}
	dir, err := d.workspace.Open(adminRoot)
	if err != nil {
		return nil, agentResultRefusal(d.repository.ID, "the checkout Git metadata .git/worktrees is unreadable")
	}
	entries, err := dir.ReadDir(maxAgentResultRefs + 1)
	_ = dir.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, agentResultRefusal(d.repository.ID, "the checkout Git metadata .git/worktrees is unreadable")
	}
	if len(entries) > maxAgentResultRefs {
		return nil, agentResultRefusal(d.repository.ID, "the checkout has too many linked worktrees (more than %d)", maxAgentResultRefs)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

func (d agentDiscovery) worktree(name string) (agentCandidate, bool, error) {
	id := d.repository.ID
	if strings.Contains(name, "/") || !safeWorkspacePath(name) {
		return agentCandidate{}, false, agentResultRefusal(id, "worktree metadata has an entry with an unsupported name")
	}
	adminRel := path.Join(d.checkoutRel, ".git", "worktrees", name)
	label := ".git/worktrees/" + name
	info, err := d.workspace.Lstat(adminRel)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return agentCandidate{}, false, agentResultRefusal(id, "worktree metadata %s is not a real directory", label)
	}
	content, err := readWorkspaceFile(d.workspace, path.Join(adminRel, "gitdir"), maxWorktreePointerBytes)
	if err != nil {
		return agentCandidate{}, false, agentResultRefusal(id, "worktree metadata %s/gitdir is not a readable regular file within %d bytes", label, maxWorktreePointerBytes)
	}
	gitFile, ok := singleLine(string(content))
	if !ok || path.Base(filepath.ToSlash(gitFile)) != ".git" {
		return agentCandidate{}, false, agentResultRefusal(id, "worktree metadata %s/gitdir is malformed", label)
	}
	if !filepath.IsAbs(gitFile) {
		gitFile = filepath.Join(d.workspaceRoot, filepath.FromSlash(adminRel), gitFile)
	}
	worktreeRel, inside := workspaceRelativeLocation(d.workspaceRoot, filepath.Dir(filepath.Clean(gitFile)))
	if !inside {
		return agentCandidate{}, false, agentResultRefusal(id, "a linked worktree is outside the job workspace")
	}
	if !safeWorkspacePath(worktreeRel) {
		return agentCandidate{}, false, agentResultRefusal(id, "a linked worktree has an unsupported path")
	}
	if err := requireWorkspaceDirectories(d.workspace, worktreeRel); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return agentCandidate{}, false, nil
		}
		if errors.Is(err, errWorkspaceSymlink) {
			return agentCandidate{}, false, agentResultRefusal(id, "linked worktree %s is reached through a symbolic link", worktreeRel)
		}
		return agentCandidate{}, false, agentResultRefusal(id, "linked worktree %s is not a real directory", worktreeRel)
	}
	if within(d.checkoutRel, worktreeRel) {
		return agentCandidate{}, false, agentResultRefusal(id, "linked worktree %s contains the repository checkout", worktreeRel)
	}
	for _, other := range d.others {
		otherRel, ok := relativeWithin(d.workspaceRoot, other.WorkDir)
		if ok && within(worktreeRel, otherRel) {
			return agentCandidate{}, false, agentResultRefusal(id, "linked worktree %s is inside the checkout of repository %s", worktreeRel, other.ID)
		}
		if ok && within(otherRel, worktreeRel) {
			return agentCandidate{}, false, agentResultRefusal(id, "linked worktree %s contains the checkout of repository %s", worktreeRel, other.ID)
		}
	}
	if !d.gitFileNamesAdmin(worktreeRel, adminRel) {
		return agentCandidate{}, false, agentResultRefusal(id, "linked worktree %s does not belong to the repository checkout", worktreeRel)
	}
	return agentCandidate{source: agentResultWorktree, relPath: worktreeRel, adminRel: adminRel}, true, nil
}

func (d agentDiscovery) gitFileNamesAdmin(worktreeRel, adminRel string) bool {
	content, err := readWorkspaceFile(d.workspace, path.Join(worktreeRel, ".git"), maxWorktreePointerBytes)
	if err != nil {
		return false
	}
	line, ok := singleLine(string(content))
	target, found := strings.CutPrefix(line, "gitdir: ")
	if !ok || !found || target == "" {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(d.workspaceRoot, filepath.FromSlash(worktreeRel), target)
	}
	rel, inside := workspaceRelativeLocation(d.workspaceRoot, filepath.Clean(target))
	return inside && rel == adminRel
}

func singleLine(content string) (string, bool) {
	line := strings.TrimSuffix(content, "\n")
	line = strings.TrimSuffix(line, "\r")
	if line == "" || strings.ContainsAny(line, "\n\r\x00") {
		return "", false
	}
	return line, true
}

func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/")
}

func workspaceRelativeLocation(workspaceRoot, target string) (string, bool) {
	if rel, ok := relativeWithin(workspaceRoot, target); ok {
		return rel, true
	}
	real, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return "", false
	}
	return relativeWithin(real, target)
}

func relativeWithin(root, target string) (string, bool) {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func safeWorkspacePath(value string) bool {
	if len(value) > 512 || !safeWorkspacePathPattern.MatchString(value) {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "." || component == ".." {
			return false
		}
	}
	return true
}
