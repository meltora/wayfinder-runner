package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/example/app-runner/internal/safeio"
)

func recoverStartupJobState(stateDir string, cleanupJobs bool, reclaim func(context.Context, []string) error) error {
	reclaimErr := reclaimStaleWorkIntakeProposalExecutions(stateDir, reclaim)
	if err := cleanupStaleWorkIntakeProposalExecutions(stateDir, nil); err != nil {
		return errors.Join(fmt.Errorf("cleanup stale work intake proposal executions: %w", err), reclaimErr)
	}
	if !cleanupJobs {
		return nil
	}
	if err := cleanupOrphanedJobState(context.Background(), stateDir, reclaim); err != nil {
		return fmt.Errorf("cleanup orphaned job state: %w", err)
	}
	return nil
}

func reclaimStaleWorkIntakeProposalExecutions(stateDir string, reclaim func(context.Context, []string) error) error {
	if reclaim == nil {
		return nil
	}
	parent := filepath.Join(stateDir, workIntakeProposalExecutionRootName)
	entries, err := os.ReadDir(parent)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var roots []string
	for _, entry := range entries {
		if entry.IsDir() && workIntakeProposalExecutionNamePattern.MatchString(entry.Name()) {
			roots = append(roots, filepath.Join(parent, entry.Name()))
		}
	}
	if len(roots) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), ownershipOperationTimeout)
	defer cancel()
	if err := reclaim(ctx, roots); err != nil {
		return fmt.Errorf("re-own stale work intake proposal executions: %w", err)
	}
	var errs []error
	for _, root := range roots {
		errs = append(errs, safeio.MakeTreeRemovable(root))
	}
	return errors.Join(errs...)
}
