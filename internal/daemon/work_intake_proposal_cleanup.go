package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const workIntakeProposalExecutionRootName = "work-intake-proposals"

var workIntakeProposalExecutionNamePattern = regexp.MustCompile(`^execution-[A-Za-z0-9]+$`)

// cleanupStaleWorkIntakeProposalExecutions removes private provider scratch
// directories left by a hard crash. Every filesystem operation is rooted at
// the exact configured state directory, and opened-directory identity checks
// prevent a symlink or rename race from widening the deletion scope.
func cleanupStaleWorkIntakeProposalExecutions(
	stateDir string,
	preserve map[string]struct{},
) error {
	if !safeWorkIntakeProposalStateDir(stateDir) {
		return fmt.Errorf("work intake proposal cleanup requires a clean dedicated absolute state directory")
	}
	for name := range preserve {
		if !workIntakeProposalExecutionNamePattern.MatchString(name) {
			return fmt.Errorf("work intake proposal cleanup preserve name is invalid")
		}
	}
	stateRoot, err := os.OpenRoot(stateDir)
	if err != nil {
		return fmt.Errorf("open work intake proposal state root: %w", err)
	}
	defer stateRoot.Close()
	rootInfo, err := stateRoot.Lstat(workIntakeProposalExecutionRootName)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect work intake proposal execution root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("work intake proposal execution root must be a real directory")
	}
	executionRoot, err := stateRoot.OpenRoot(workIntakeProposalExecutionRootName)
	if err != nil {
		return fmt.Errorf("open work intake proposal execution root: %w", err)
	}
	defer executionRoot.Close()
	openedRootInfo, err := executionRoot.Stat(".")
	if err != nil || !os.SameFile(rootInfo, openedRootInfo) {
		return fmt.Errorf("work intake proposal execution root changed while opening")
	}
	rootDirectory, err := executionRoot.Open(".")
	if err != nil {
		return fmt.Errorf("read work intake proposal execution root: %w", err)
	}
	entries, readErr := rootDirectory.ReadDir(-1)
	closeErr := rootDirectory.Close()
	if readErr != nil {
		return fmt.Errorf("read work intake proposal execution root: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close work intake proposal execution root: %w", closeErr)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !workIntakeProposalExecutionNamePattern.MatchString(name) {
			continue
		}
		if _, keep := preserve[name]; keep {
			continue
		}
		if err := removeStaleWorkIntakeProposalExecution(executionRoot, name); err != nil {
			return err
		}
	}
	return nil
}

func safeWorkIntakeProposalStateDir(stateDir string) bool {
	if stateDir == "" || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return false
	}
	volumeRoot := filepath.VolumeName(stateDir) + string(filepath.Separator)
	return stateDir != volumeRoot && filepath.Dir(stateDir) != volumeRoot
}

func removeStaleWorkIntakeProposalExecution(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect stale work intake proposal execution %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("stale work intake proposal execution %s is not a real directory", name)
	}
	childRoot, err := root.OpenRoot(name)
	if err != nil {
		return fmt.Errorf("open stale work intake proposal execution %s: %w", name, err)
	}
	openedInfo, err := childRoot.Stat(".")
	if err != nil || !os.SameFile(info, openedInfo) {
		_ = childRoot.Close()
		return fmt.Errorf("stale work intake proposal execution %s changed while opening", name)
	}
	childDirectory, err := childRoot.Open(".")
	if err != nil {
		_ = childRoot.Close()
		return fmt.Errorf("read stale work intake proposal execution %s: %w", name, err)
	}
	entries, readErr := childDirectory.ReadDir(-1)
	closeDirectoryErr := childDirectory.Close()
	if readErr != nil || closeDirectoryErr != nil {
		_ = childRoot.Close()
		if readErr != nil {
			return fmt.Errorf("read stale work intake proposal execution %s: %w", name, readErr)
		}
		return fmt.Errorf("close stale work intake proposal execution %s: %w", name, closeDirectoryErr)
	}
	for _, entry := range entries {
		if err := childRoot.RemoveAll(entry.Name()); err != nil {
			_ = childRoot.Close()
			return fmt.Errorf("remove stale work intake proposal execution content %s: %w", name, err)
		}
	}
	if err := childRoot.Close(); err != nil {
		return fmt.Errorf("close stale work intake proposal execution %s: %w", name, err)
	}
	currentInfo, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, currentInfo) {
		return fmt.Errorf("stale work intake proposal execution %s changed before removal", name)
	}
	if err := root.Remove(name); err != nil {
		return fmt.Errorf("remove stale work intake proposal execution %s: %w", name, err)
	}
	return nil
}
