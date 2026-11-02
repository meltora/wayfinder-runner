package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/example/app-runner/internal/safeio"
)

const (
	checkWorkspaceSuffix      = ".checks"
	checkIndexName            = "check.index"
	maxCheckDependencyEntries = 1_000_000
	maxCheckDependencyBytes   = 4 << 30
	maxCheckDependencyDepth   = 256
	checkWorkspaceUnavailable = "the check workspace could not be prepared"
)

type checkWorkspace struct {
	mu                sync.Mutex
	root              string
	npmCache          string
	entryLimit        int
	byteLimit         int64
	depthLimit        int
	lockfileByteLimit int64
	npmCacheByteLimit int64
}

type checkLocation struct {
	root string
	dirs map[string]string
}

func checkWorkspaceRoot(stateDir, workDir string) string {
	return trustedResultsRoot(stateDir, workDir) + checkWorkspaceSuffix
}

func (jr *jobRunner) checkWorkspace() *checkWorkspace {
	jr.checkSpaceOnce.Do(func() {
		jr.checkSpace = &checkWorkspace{
			root:              checkWorkspaceRoot(jr.d.cfg.StateDir, jr.workDir),
			npmCache:          npmCacheRoot(jr.d.cfg.StateDir, jr.workDir),
			entryLimit:        maxCheckDependencyEntries,
			byteLimit:         maxCheckDependencyBytes,
			depthLimit:        maxCheckDependencyDepth,
			lockfileByteLimit: maxInstallInputBytes,
			npmCacheByteLimit: maxNPMCacheBytes,
		}
	})
	return jr.checkSpace
}

func (jr *jobRunner) checkResults(ctx context.Context, results map[string]agentResult, use string) (map[string]agentResult, error) {
	resolved := make(map[string]agentResult, len(jr.repositories))
	for _, repository := range jr.repositories {
		if result, ok := results[repository.ID]; ok {
			resolved[repository.ID] = result
			continue
		}
		result, err := jr.resolveAgentResult(ctx, repository, use)
		if err != nil {
			return nil, err
		}
		resolved[repository.ID] = result
	}
	return resolved, nil
}

func (jr *jobRunner) runInCheckWorkspace(ctx context.Context, results map[string]agentResult, run func(*checkWorkspace, checkLocation) checkOutcome) (checkOutcome, error) {
	workspace := jr.checkWorkspace()
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	location, err := jr.prepareCheckWorkspace(ctx, workspace, results)
	if err != nil {
		if ctx.Err() != nil {
			return checkOutcome{}, err
		}
		if wipeErr := workspace.wipe(); wipeErr != nil {
			return checkOutcome{}, errors.Join(err, wipeErr)
		}
		if location, err = jr.prepareCheckWorkspace(ctx, workspace, results); err != nil {
			return checkOutcome{}, err
		}
	}
	return run(workspace, location), nil
}

func (jr *jobRunner) requiredCheckOutcome(ctx context.Context, request checkRequest, results map[string]agentResult) checkReport {
	resolved, err := jr.checkResults(ctx, results, "required check")
	if err == nil {
		var report checkReport
		report, err = jr.runCheckRequest(ctx, request, resolved)
		if err == nil {
			return report
		}
	}
	jr.logger.Warn("check workspace preparation failed", "check", request.checkID, "err", err)
	return checkReport{outcome: checkOutcome{Result: "error", Class: "internal", Output: checkWorkspaceMessage(err)}}
}

func (w *checkWorkspace) wipe() error {
	if _, err := os.Lstat(w.root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := safeio.MakeTreeRemovable(w.root); err != nil {
		return err
	}
	return os.RemoveAll(w.root)
}

func checkWorkspaceMessage(err error) string {
	var refusal *agentResultError
	if errors.As(err, &refusal) {
		return refusal.Error()
	}
	return checkWorkspaceUnavailable
}

func (jr *jobRunner) checkRepositoryRel(repository checkedOutRepository) (string, error) {
	rel, err := filepath.Rel(jr.workDir, repository.WorkDir)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("repository %s is outside the job workspace", repository.ID)
	}
	return filepath.ToSlash(rel), nil
}

func (jr *jobRunner) prepareCheckWorkspace(ctx context.Context, workspace *checkWorkspace, results map[string]agentResult) (checkLocation, error) {
	if err := os.MkdirAll(workspace.root, 0o700); err != nil {
		return checkLocation{}, err
	}
	root, err := os.OpenRoot(workspace.root)
	if err != nil {
		return checkLocation{}, err
	}
	defer func() { _ = root.Close() }()
	location := checkLocation{root: workspace.root, dirs: map[string]string{}}
	repositoryRels := map[string]string{}
	for _, repository := range jr.repositories {
		rel, err := jr.checkRepositoryRel(repository)
		if err != nil {
			return checkLocation{}, err
		}
		if _, ok := results[repository.ID]; !ok {
			return checkLocation{}, fmt.Errorf("repository %s has no resolved result", repository.ID)
		}
		repositoryRels[repository.ID] = rel
		location.dirs[repository.ID] = filepath.Join(workspace.root, filepath.FromSlash(rel))
	}
	ancestors := map[string]bool{}
	for _, rel := range repositoryRels {
		markAncestors(ancestors, rel)
	}
	if err := pruneCheckTree(root, ".", ancestors); err != nil {
		return checkLocation{}, err
	}
	for _, repository := range jr.repositories {
		rel := repositoryRels[repository.ID]
		if err := root.MkdirAll(rel, 0o755); err != nil {
			return checkLocation{}, err
		}
		if err := materialiseCheckTree(ctx, repository, results[repository.ID].commit, location.dirs[repository.ID]); err != nil {
			return checkLocation{}, err
		}
	}
	return location, nil
}

func markAncestors(ancestors map[string]bool, rel string) {
	for current := rel; current != "." && current != "/" && current != ""; current = path.Dir(current) {
		ancestors[current] = true
	}
}

func pruneCheckTree(root *os.Root, rel string, ancestors map[string]bool) error {
	handle, err := root.Open(rel)
	if err != nil {
		return err
	}
	names, err := handle.Readdirnames(-1)
	closeErr := handle.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return err
	}
	for _, name := range names {
		child := joinRel(rel, name)
		info, err := root.Lstat(child)
		if err != nil {
			return err
		}
		if ancestors[child] && info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
			if err := pruneCheckTree(root, child, ancestors); err != nil {
				return err
			}
			continue
		}
		if err := root.RemoveAll(child); err != nil {
			return err
		}
	}
	return nil
}

func materialiseCheckTree(ctx context.Context, repository checkedOutRepository, commit, dir string) error {
	trusted := repository.Trusted
	if trusted == nil {
		return agentResultRefusal(repository.ID, "the trusted result repository is not available")
	}
	index := filepath.Join(trusted.root, checkIndexName)
	if err := os.Remove(index); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	options := trustedGitOptions{gitDir: trusted.gitDir, env: []string{"GIT_INDEX_FILE=" + index}}
	if _, err := trusted.gitAt(ctx, options, "read-tree", commit); err != nil {
		return err
	}
	options.workTree = dir
	_, err := trusted.gitAt(ctx, options, "checkout-index", "--all", "--force")
	return err
}
