package daemon

import (
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type jobOutcome struct {
	State           v1.JobFinishedState
	FailureCode     v1.JobFinishedFailureCode
	FailureCategory v1.AgentPackMaterializationFailureCategory
	Retryable       *bool
	FailureMessage  string
}

func (jr *jobRunner) jobFinishedEvent(outcome jobOutcome) v1.JobFinishedEvent {
	var authority *v1.AgentRunAuthority
	if jr.job.AgentRunAuthority != nil {
		copyValue := *jr.job.AgentRunAuthority
		authority = &copyValue
	}
	return v1.JobFinishedEvent{
		Kind:              "job_finished",
		ManifestKind:      jr.job.Kind,
		LeaseID:           jr.job.LeaseID,
		JobID:             jr.job.JobID,
		AttemptID:         jr.job.AttemptID,
		ManifestID:        jr.job.ManifestID,
		ManifestDigest:    jr.job.ManifestDigest,
		FencingToken:      jr.job.FencingToken,
		ObservedAt:        time.Now().UTC(),
		State:             outcome.State,
		ProviderSessionID: jr.currentSessionID(),
		FailureCode:       outcome.FailureCode,
		FailureCategory:   outcome.FailureCategory,
		Retryable:         outcome.Retryable,
		FailureMessage:    truncateUTF8Bytes(outcome.FailureMessage, 512),
		AgentRunAuthority: authority,
		ProviderUsage:     jr.currentProviderUsage(),
	}
}
