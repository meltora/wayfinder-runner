package validation

import (
	"fmt"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var failedJobCodes = map[v1.JobFinishedFailureCode]struct{}{
	v1.JobFailureAgentStart:           {},
	v1.JobFailureAgentExit:            {},
	v1.JobFailureWorkspacePreparation: {},
	v1.JobFailureBuildFinalization:    {},
	v1.JobFailureRequiredChecks:       {},
	v1.JobFailureRunnerInternal:       {},
}

// JobFinishedEvent validates terminal evidence before it is durably queued.
func JobFinishedEvent(event v1.JobFinishedEvent) error {
	if err := validateJobFinishedAuthority(event); err != nil {
		return err
	}
	if event.ProviderSessionID != "" {
		if err := ID("provider_session_id", event.ProviderSessionID); err != nil {
			return err
		}
	}
	if err := boundedText("failure_message", event.FailureMessage, 512, false); err != nil {
		return err
	}
	if !jobFinishedEvidenceMatchesState(event) {
		return fmt.Errorf("failure evidence does not match terminal state")
	}
	return nil
}

func validateJobFinishedAuthority(event v1.JobFinishedEvent) error {
	if event.Kind != "job_finished" {
		return fmt.Errorf("job finished event kind is invalid")
	}
	if event.ManifestKind != v1.JobKindPlan && event.ManifestKind != v1.JobKindBuild {
		return fmt.Errorf("job finished manifest kind is invalid")
	}
	for field, value := range map[string]string{
		"lease_id": event.LeaseID, "job_id": event.JobID,
		"attempt_id": event.AttemptID, "manifest_id": event.ManifestID,
	} {
		if err := ID(field, value); err != nil {
			return err
		}
	}
	if err := SHA256Digest("manifest_digest", event.ManifestDigest); err != nil {
		return err
	}
	if event.FencingToken == 0 || event.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("fencing_token is outside the protocol JSON-safe range")
	}
	if event.ObservedAt.IsZero() {
		return fmt.Errorf("observed_at is required")
	}
	return nil
}

func jobFinishedEvidenceMatchesState(event v1.JobFinishedEvent) bool {
	switch event.State {
	case v1.JobFinishedSucceeded:
		return event.FailureCode == "" && event.FailureMessage == ""
	case v1.JobFinishedFailed:
		_, ok := failedJobCodes[event.FailureCode]
		return ok
	case v1.JobFinishedCancelled:
		return event.FailureCode == v1.JobFailureCancelled
	case v1.JobFinishedQuarantined:
		return event.FailureCode == v1.JobFailureLeaseLost || event.FailureCode == v1.JobFailureCleanup
	default:
		return false
	}
}
