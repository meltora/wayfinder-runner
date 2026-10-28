package daemon

import (
	"errors"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const planCheckoutHandOffFailure = "plan checkout changed during ownership hand-off"

var errPlanCheckoutHandOffChanged = errors.New(planCheckoutHandOffFailure)

func (jr *jobRunner) carryPlanCheckoutForward() error {
	if jr.job.Kind != v1.JobKindPlan {
		return nil
	}
	for _, repository := range jr.repositories {
		digests, err := planCheckoutBaseline(repository.WorkDir)
		cause := ""
		switch {
		case err != nil:
			cause = "baseline_unreadable"
		case repository.PlanBaseline.current().Identity == "":
			cause = "baseline_missing"
		case !repository.PlanBaseline.carryForward(digests):
			cause = "identity_mismatch"
		}
		if cause != "" {
			jr.logger.Error(planCheckoutHandOffFailure, "repository", repository.ID, "cause", cause)
			return errPlanCheckoutHandOffChanged
		}
	}
	return nil
}
