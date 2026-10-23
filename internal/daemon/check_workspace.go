package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sync"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/safeio"
)

const (
	checkWorkspaceSuffix       = ".checks"
	checkIndexName             = "check.index"
	maxCheckDependencyEntries  = 1_000_000
	maxCheckDependencyBytes    = 4 << 30
	maxCheckDependencyDepth    = 256
	checkWorkspaceUnavailable  = "the check workspace could not be prepared"
	defaultDependencyDirectory = "node_modules"
)

type checkWorkspaceError struct {
	message string
}

func (e *checkWorkspaceError) Error() string {
	return e.message
}

type checkWorkspace struct {
	mu         sync.Mutex
	root       string
	entryLimit int
	byteLimit  int64
	depthLimit int
	copied     map[string]copiedFile
}

type checkLocation struct {
	root string
	dirs map[string]string
}

type checkDependency struct {
	source string
	target string
}

func checkWorkspaceRoot(stateDir, workDir string) string {
	return trustedResultsRoot(stateDir, workDir) + checkWorkspaceSuffix
}

func (jr *jobRunner) checkWorkspace() *checkWorkspace {
	jr.checkSpaceOnce.Do(func() {
		jr.checkSpace = &checkWorkspace{
			root:       checkWorkspaceRoot(jr.d.cfg.StateDir, jr.workDir),
			entryLimit: maxCheckDependencyEntries,
			byteLimit:  maxCheckDependencyBytes,
			depthLimit: maxCheckDependencyDepth,
		}
	})
	return jr.checkSpace
}

func checkDependencyNames(check config.CheckConfig) []string {
	if check.DependencyDirectories == nil {
		return []string{defaultDependencyDirectory}
	}
	return check.DependencyDirectories
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

func (jr *jobRunner) runInCheckWorkspace(ctx context.Context, check config.CheckConfig, results map[string]agentResult, run func(checkLocation) checkOutcome) (checkOutcome, error) {
	workspace := jr.checkWorkspace()
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	location, err := jr.prepareCheckWorkspace(ctx, workspace, check, results)
	if err != nil {
		var limit *checkWorkspaceError
		if errors.As(err, &limit) || ctx.Err() != nil {
			return checkOutcome{}, err
		}
		if wipeErr := workspace.wipe(); wipeErr != nil {
			return checkOutcome{}, errors.Join(err, wipeErr)
		}
		if location, err = jr.prepareCheckWorkspace(ctx, workspace, check, results); err != nil {
			return checkOutcome{}, err
		}
	}
	return run(location), nil
}

func (jr *jobRunner) requiredCheckOutcome(ctx context.Context, checkID string, repository checkedOutRepository, check config.CheckConfig, results map[string]agentResult) checkOutcome {
	resolved, err := jr.checkResults(ctx, results, "required check")
	if err == nil {
		var outcome checkOutcome
		outcome, err = jr.runInCheckWorkspace(ctx, check, resolved, func(location checkLocation) checkOutcome {
			return jr.executeConfiguredCheck(ctx, checkID, location.root, location.dirs[repository.ID], check)
		})
		if err == nil {
			return outcome
		}
	}
	jr.logger.Warn("check workspace preparation failed", "check", checkID, "err", err)
	return checkOutcome{Result: "error", Class: "internal", Output: checkWorkspaceMessage(err)}
}

func (w *checkWorkspace) wipe() error {
	w.copied = nil
	if _, err := os.Lstat(w.root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := safeio.MakeTreeRemovable(w.root); err != nil {
		return err
	}
	return os.RemoveAll(w.root)
}

func checkWorkspaceMessage(err error) string {
	var limit *checkWorkspaceError
	if errors.As(err, &limit) {
		return limit.message
	}
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

func (jr *jobRunner) prepareCheckWorkspace(ctx context.Context, workspace *checkWorkspace, check config.CheckConfig, results map[string]agentResult) (checkLocation, error) {
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
	dependencies := []checkDependency{}
	names := checkDependencyNames(check)
	for _, repository := range jr.repositories {
		rel, err := jr.checkRepositoryRel(repository)
		if err != nil {
			return checkLocation{}, err
		}
		result, ok := results[repository.ID]
		if !ok {
			return checkLocation{}, fmt.Errorf("repository %s has no resolved result", repository.ID)
		}
		repositoryRels[repository.ID] = rel
		location.dirs[repository.ID] = filepath.Join(workspace.root, filepath.FromSlash(rel))
		for _, ignored := range result.candidate.ignoredDirs {
			if slices.Contains(names, path.Base(ignored)) {
				dependencies = append(dependencies, checkDependency{
					source: path.Join(result.candidate.relPath, ignored),
					target: path.Join(rel, ignored),
				})
			}
		}
	}
	kept := map[string]bool{}
	for _, dependency := range dependencies {
		kept[dependency.target] = true
	}
	ancestors := map[string]bool{}
	for _, rel := range repositoryRels {
		markAncestors(ancestors, rel, true)
	}
	for target := range kept {
		markAncestors(ancestors, target, false)
	}
	if err := pruneCheckTree(root, ".", kept, ancestors); err != nil {
		return checkLocation{}, err
	}
	if err := workspace.syncDependencies(ctx, root, jr.workDir, dependencies); err != nil {
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

func markAncestors(ancestors map[string]bool, rel string, inclusive bool) {
	current := rel
	if !inclusive {
		current = path.Dir(rel)
	}
	for current != "." && current != "/" && current != "" {
		ancestors[current] = true
		current = path.Dir(current)
	}
}

func pruneCheckTree(root *os.Root, rel string, kept, ancestors map[string]bool) error {
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
		realDirectory := info.IsDir() && info.Mode()&fs.ModeSymlink == 0
		switch {
		case kept[child] && realDirectory:
		case ancestors[child] && realDirectory:
			if err := pruneCheckTree(root, child, kept, ancestors); err != nil {
				return err
			}
		default:
			if err := root.RemoveAll(child); err != nil {
				return err
			}
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

func checkDependencyLimit(format string, args ...any) error {
	return &checkWorkspaceError{message: "dependency directories " + fmt.Sprintf(format, args...)}
}
