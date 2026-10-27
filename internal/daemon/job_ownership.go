package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/isolation"
)

const ownershipOperationTimeout = 10 * time.Minute

type containerRemover func(ctx context.Context, runnerID, attemptID string) error

type ownershipScope struct {
	once    sync.Once
	err     error
	release func() error
}

func (scope *ownershipScope) Release() error {
	if scope == nil {
		return nil
	}
	scope.once.Do(func() { scope.err = scope.release() })
	return scope.err
}

func handOffOwnership(
	ctx context.Context,
	ownership isolation.Ownership,
	remove containerRemover,
	request isolation.OwnershipRequest,
) (*ownershipScope, error) {
	scope := &ownershipScope{release: func() error {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ownershipOperationTimeout)
		defer cancel()
		if remove != nil {
			if err := remove(cleanupCtx, request.RunnerID, request.AttemptID); err != nil {
				return fmt.Errorf("remove container before re-owning its output: %w", err)
			}
		}
		if ownership == nil {
			return nil
		}
		return ownership.Reclaim(cleanupCtx, request)
	}}
	if ownership == nil {
		return scope, nil
	}
	handOffCtx, cancel := context.WithTimeout(ctx, ownershipOperationTimeout)
	defer cancel()
	return scope, ownership.HandOff(handOffCtx, request)
}

func jobOutputReclaimFailure() jobOutcome {
	return jobOutcome{
		State: v1.JobFinishedQuarantined, FailureCode: v1.JobFailureCleanup,
		FailureMessage: "job output could not be re-owned by the runner",
	}
}

func (d *Daemon) containerRemover() containerRemover {
	if d.removeJobContainer != nil {
		return d.removeJobContainer
	}
	return isolation.RemoveJobContainer
}

func (d *Daemon) recoveryReclaimer() func(context.Context, []string) error {
	return ownershipReclaimer(d.ownership, d.runnerID)
}

func ownershipReclaimer(ownership isolation.Ownership, runnerID string) func(context.Context, []string) error {
	if ownership == nil {
		return nil
	}
	return func(ctx context.Context, roots []string) error {
		return isolation.ReclaimInBatches(ctx, ownership, runnerID, "recovery", roots)
	}
}
