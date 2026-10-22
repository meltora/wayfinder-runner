package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

const maxSparsePatternBytes = 1 << 20

func (t *trustedResultRepository) restoreSparseDeletions(ctx context.Context, workspace *os.Root, sparseRel, start, repositoryID, label string) error {
	patterns, err := readOptionalWorkspaceFile(workspace, sparseRel, maxSparsePatternBytes)
	if err != nil {
		return agentResultRefusal(repositoryID, "%s has sparse-checkout patterns that are not a regular file within %d bytes", label, maxSparsePatternBytes)
	}
	if patterns == nil {
		return nil
	}
	listing, err := t.git(ctx, "diff-index", "--cached", "--name-only", "-z", "--no-renames", "--ignore-submodules=none", "--diff-filter=D", start)
	if err != nil {
		return err
	}
	var deleted []string
	for _, entry := range strings.Split(string(listing), "\x00") {
		if entry != "" {
			deleted = append(deleted, entry)
		}
	}
	if len(deleted) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(t.sparseGitDir, "info"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(t.sparseGitDir, "info", "exclude"), patterns, 0o600); err != nil {
		return err
	}
	outside, err := t.outsideSparseCone(ctx, deleted)
	if err != nil {
		return err
	}
	if len(outside) == 0 {
		return nil
	}
	_, err = t.gitAt(ctx, trustedGitOptions{
		gitDir: t.gitDir, workTree: t.filesDir,
		stdin: strings.NewReader(strings.Join(outside, "\x00") + "\x00"),
		env:   []string{"GIT_LITERAL_PATHSPECS=1"},
	}, "reset", "-q", start, "--pathspec-from-file=-", "--pathspec-file-nul")
	return err
}

func (t *trustedResultRepository) outsideSparseCone(ctx context.Context, paths []string) ([]string, error) {
	oracle, err := t.startIgnoreOracle(ctx, t.sparseGitDir, t.sparseTree, true)
	if err != nil {
		return nil, err
	}
	var outside []string
	for _, entry := range paths {
		inside, err := oracle.ignored(entry)
		if err != nil {
			_ = oracle.close()
			return nil, err
		}
		if !inside {
			outside = append(outside, entry)
		}
	}
	return outside, oracle.close()
}
