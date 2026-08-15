package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/example/app-runner/internal/repo"
	"github.com/example/app-runner/internal/scanner"
	"github.com/example/app-runner/internal/validation"
)

func (jr *jobRunner) publishRepositoryDiscovery(
	ctx context.Context,
	manager *repo.Manager,
	repository checkedOutRepository,
) error {
	commitSHA, err := manager.HeadSHA(ctx, repository.WorkDir)
	if err != nil {
		return fmt.Errorf("resolve discovery snapshot: %w", err)
	}
	inventory, err := scanner.Scan(repository.WorkDir)
	if err != nil {
		return fmt.Errorf("scan repository configuration: %w", err)
	}
	confirmedSHA, err := manager.HeadSHA(ctx, repository.WorkDir)
	if err != nil {
		return fmt.Errorf("confirm discovery snapshot: %w", err)
	}
	if confirmedSHA != commitSHA {
		return fmt.Errorf("repository commit changed during configuration discovery")
	}
	event, err := scanner.BuildDiscoveryEvent(scanner.DiscoveryInput{
		ProjectID: jr.job.ProjectID, RepositoryID: repository.ID,
		BranchRef: repository.Base, CommitSHA: commitSHA, ObservedAt: time.Now(),
	}, inventory)
	if err != nil {
		return err
	}
	if err := validation.AgentConfigDiscoveryEvent(event); err != nil {
		return fmt.Errorf("validate repository configuration discovery: %w", err)
	}
	if _, err := jr.d.sendEventWithStableID(ctx, scanner.DiscoveryEventID(event.ScanID), event); err != nil {
		return fmt.Errorf("queue repository configuration discovery: %w", err)
	}
	return nil
}
