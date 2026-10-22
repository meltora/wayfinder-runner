package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
)

const maxGitExcludeBytes = 1 << 20

func ensureGitExcludeEntries(workspace, checkoutRel string, entries ...string) error {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	gitDir := path.Join(checkoutRel, ".git")
	if err := requireWorkspaceDirectories(root, gitDir); err != nil {
		return err
	}
	infoDir := path.Join(gitDir, "info")
	if err := root.Mkdir(infoDir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err := requireWorkspaceDirectories(root, infoDir); err != nil {
		return err
	}
	excludePath := path.Join(infoDir, "exclude")
	existing, err := inspectWorkspaceFile(root, excludePath, maxGitExcludeBytes)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var content []byte
	if existing != nil {
		if content, err = readWorkspaceFile(root, excludePath, maxGitExcludeBytes); err != nil {
			return err
		}
	}
	addition := missingExcludeEntries(string(content), entries)
	if addition == "" {
		return nil
	}
	if int64(len(content)+len(addition)) > maxGitExcludeBytes {
		return fmt.Errorf("git exclude file would exceed %d bytes", maxGitExcludeBytes)
	}
	return appendWorkspaceFile(root, excludePath, existing, addition)
}

func missingExcludeEntries(content string, entries []string) string {
	present := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		present[strings.TrimSpace(line)] = true
	}
	addition := ""
	for _, entry := range entries {
		if !present[entry] {
			present[entry] = true
			addition += entry + "\n"
		}
	}
	if addition != "" && content != "" && !strings.HasSuffix(content, "\n") {
		addition = "\n" + addition
	}
	return addition
}
