package validation

import (
	"fmt"
	"strings"

	v1 "github.com/example/app-runner/api/runner/v1"
)

// ProviderCapacityExhaustedEvent validates the exact strict wire contract
// before consequential recovery evidence is durably queued.
func ProviderCapacityExhaustedEvent(event v1.ProviderCapacityExhaustedEvent) error {
	if event.Kind != "provider_capacity_exhausted" {
		return fmt.Errorf("provider capacity event kind is invalid")
	}
	if event.ManifestKind != v1.JobKindPlan && event.ManifestKind != v1.JobKindBuild {
		return fmt.Errorf("provider capacity manifest kind is invalid")
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
	for field, value := range map[string]string{
		"adapter": event.Adapter, "provider": event.Provider,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", field)
		}
		if err := boundedText(field, value, 128, true); err != nil {
			return err
		}
	}
	if err := boundedText("model", event.Model, 256, false); err != nil {
		return err
	}
	if event.Reason != "quota_exhausted" {
		return fmt.Errorf("provider capacity reason is invalid")
	}
	if event.ResumePolicy != v1.CapacityResumeManual && event.ResumePolicy != v1.CapacityResumeAutomatic {
		return fmt.Errorf("provider capacity resume policy is invalid")
	}
	if event.RetryAt != nil && event.RetryAt.IsZero() {
		return fmt.Errorf("retry_at is invalid")
	}
	if event.ProviderSessionID != "" {
		if err := ID("provider_session_id", event.ProviderSessionID); err != nil {
			return err
		}
	}
	if (event.BranchRef == "") != (event.CommitSHA == "") {
		return fmt.Errorf("branch_ref and commit_sha must be supplied together")
	}
	if event.BranchRef != "" {
		if err := GitRef("branch_ref", event.BranchRef); err != nil {
			return err
		}
		if err := GitCommitSHA("commit_sha", event.CommitSHA); err != nil {
			return err
		}
	}
	if event.CheckpointRef != "" {
		if event.BranchRef == "" {
			return fmt.Errorf("checkpoint_ref requires Git lineage")
		}
		if err := boundedText("checkpoint_ref", event.CheckpointRef, 512, true); err != nil {
			return err
		}
	}
	return boundedText("message", event.Message, 512, false)
}
