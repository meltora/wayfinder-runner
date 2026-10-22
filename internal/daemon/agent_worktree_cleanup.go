package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func removeAgentWorktrees(workspaceRoot, checkout string) error {
	checkoutRel, ok := relativeWithin(workspaceRoot, checkout)
	if !ok {
		return fmt.Errorf("repository checkout is outside the job workspace")
	}
	root, err := os.OpenRoot(workspaceRoot)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	adminRel := path.Join(checkoutRel, ".git", "worktrees")
	if err := requireWorkspaceDirectories(root, adminRel); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("agent worktree metadata: %w", err)
	}
	failures := removeAgentWorktreeDirectories(root, workspaceRoot, checkoutRel, adminRel)
	failures = append(failures, root.RemoveAll(adminRel))
	return errors.Join(failures...)
}

func removeAgentWorktreeDirectories(root *os.Root, workspaceRoot, checkoutRel, adminRel string) []error {
	admin, err := root.Open(adminRel)
	if err != nil {
		return []error{err}
	}
	entries, err := admin.ReadDir(-1)
	_ = admin.Close()
	if err != nil {
		return []error{err}
	}
	var failures []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rel, ok := agentWorktreeDirectory(root, workspaceRoot, path.Join(adminRel, entry.Name()))
		if !ok || rel == checkoutRel || strings.HasPrefix(checkoutRel, rel+"/") {
			continue
		}
		if requireWorkspaceDirectories(root, rel) != nil {
			continue
		}
		if _, err := inspectWorkspaceFile(root, path.Join(rel, ".git"), maxWorktreePointerBytes); err != nil {
			continue
		}
		if err := makeWorkspaceTreeRemovable(root, rel); err != nil {
			failures = append(failures, err)
		}
		failures = append(failures, root.RemoveAll(rel))
	}
	return failures
}

func agentWorktreeDirectory(root *os.Root, workspaceRoot, adminDir string) (string, bool) {
	content, err := readWorkspaceFile(root, path.Join(adminDir, "gitdir"), maxWorktreePointerBytes)
	if err != nil {
		return "", false
	}
	gitFile := strings.TrimSpace(string(content))
	if !filepath.IsAbs(gitFile) || filepath.Base(gitFile) != ".git" {
		return "", false
	}
	return workspaceRelativePath(workspaceRoot, filepath.Dir(gitFile))
}

func workspaceRelativePath(workspaceRoot, target string) (string, bool) {
	rel, ok := relativeWithin(workspaceRoot, target)
	if !ok {
		real, err := filepath.EvalSymlinks(workspaceRoot)
		if err != nil {
			return "", false
		}
		if rel, ok = relativeWithin(real, target); !ok {
			return "", false
		}
	}
	return rel, safeWorkspacePath(rel)
}

func makeWorkspaceTreeRemovable(root *os.Root, rel string) error {
	return fs.WalkDir(root.FS(), rel, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			if err := root.Chmod(name, 0o700); err != nil {
				return fmt.Errorf("make agent worktree directory removable: %w", err)
			}
		}
		return nil
	})
}
