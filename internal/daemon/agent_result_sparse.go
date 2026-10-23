package daemon

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const maxSparsePatternBytes = 1 << 20

func (t *trustedResultRepository) restoreSparseDeletions(ctx context.Context, workspace *os.Root, cache *candidateCache, sparseRel, start, repositoryID, label string) error {
	patterns, err := readOptionalWorkspaceFile(workspace, sparseRel, maxSparsePatternBytes)
	if err != nil {
		return agentResultRefusal(repositoryID, "%s has sparse-checkout patterns that are not a regular file within %d bytes", label, maxSparsePatternBytes)
	}
	if patterns == nil {
		return nil
	}
	listing, err := t.gitAt(ctx, t.indexedGit(cache), "diff-index", "--cached", "--name-only", "-z", "--no-renames", "--ignore-submodules=none", "--diff-filter=D", start)
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
	indexed, err := t.gitAt(ctx, t.indexedGit(cache), "ls-files", "-z", "--cached")
	if err != nil {
		return err
	}
	if replaced, found := replacedOutOfCone(strings.Split(string(indexed), "\x00"), outside); found {
		return agentResultRefusal(repositoryID, "%s replaces %s, which is outside the sparse-checkout cone, with a different kind of entry; add it to the cone with git sparse-checkout add, or leave that path unchanged", label, displayEntry(replaced))
	}
	options := t.indexedGit(cache, "GIT_LITERAL_PATHSPECS=1")
	options.stdin = strings.NewReader(strings.Join(outside, "\x00") + "\x00")
	_, err = t.gitAt(ctx, options, "reset", "-q", start, "--pathspec-from-file=-", "--pathspec-file-nul")
	return err
}

func replacedOutOfCone(indexed, restored []string) (string, bool) {
	present := make(map[string]bool, len(indexed))
	sorted := make([]string, 0, len(indexed))
	for _, entry := range indexed {
		if entry != "" {
			present[entry] = true
			sorted = append(sorted, entry)
		}
	}
	sort.Strings(sorted)
	for _, entry := range restored {
		below := sort.SearchStrings(sorted, entry+"/")
		if below < len(sorted) && strings.HasPrefix(sorted[below], entry+"/") {
			return entry, true
		}
		for parent := path.Dir(entry); parent != "."; parent = path.Dir(parent) {
			if present[parent] {
				return entry, true
			}
		}
	}
	return "", false
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
