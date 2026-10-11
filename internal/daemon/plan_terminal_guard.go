package daemon

import (
	"context"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func (jr *jobRunner) planExitOutcome(ctx context.Context) jobOutcome {
	queued, err := jr.d.journal.PlanPublicationQueued(ctx, jr.job.AttemptID)
	if err != nil {
		jr.logger.Error("read plan publication evidence", "err", err)
		return jobOutcome{
			State: v1.JobFinishedFailed, FailureCode: v1.JobFailureRunnerInternal,
			FailureMessage: "plan publication evidence could not be read",
		}
	}
	if !queued {
		return jobOutcome{
			State: v1.JobFinishedFailed, FailureCode: v1.JobFailurePlanNotPublished,
			FailureMessage: "planning finished without publishing a plan",
		}
	}
	return jobOutcome{State: v1.JobFinishedSucceeded}
}
