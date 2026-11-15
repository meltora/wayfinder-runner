package validation

import (
	"fmt"
	"slices"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func ElevatedCapabilityOutcomesEvent(value v1.ElevatedCapabilityOutcomesEvent) error {
	if value.Kind != v1.EventKindElevatedCapabilityOutcomes {
		return fmt.Errorf("elevated capability outcomes kind is invalid")
	}
	if err := manifestBoundEvent(value.ManifestKind, value.LeaseID, value.JobID, value.AttemptID,
		value.ManifestID, value.ManifestDigest, value.FencingToken, value.ObservedAt); err != nil {
		return err
	}
	if value.AgentRunAuthority != nil {
		if err := AgentRunAuthority(*value.AgentRunAuthority); err != nil {
			return err
		}
	}
	if value.SchemaVersion != v1.ElevatedCapabilityOutcomesSchemaVersion {
		return fmt.Errorf("elevated capability outcomes schema_version is unsupported")
	}
	if len(value.Outcomes) < 1 || len(value.Outcomes) > v1.MaxElevatedCapabilityGrants {
		return fmt.Errorf("elevated capability outcomes are out of range")
	}
	seen := map[string]bool{}
	for index, outcome := range value.Outcomes {
		field := fmt.Sprintf("outcomes[%d]", index)
		if err := ID(field+".request_id", outcome.RequestID); err != nil {
			return err
		}
		if seen[outcome.RequestID] {
			return fmt.Errorf("%s.request_id appears more than once", field)
		}
		seen[outcome.RequestID] = true
		if !slices.Contains(v1.ElevatedCapabilityOutcomeReasons, outcome.Reason) {
			return fmt.Errorf("%s.reason is unknown", field)
		}
		if outcome.Applied != (outcome.Reason == v1.ElevatedCapabilityOutcomeApplied) {
			return fmt.Errorf("%s.applied disagrees with its reason", field)
		}
	}
	if err := SHA256Digest("content_digest_sha256", value.ContentDigestSHA256); err != nil {
		return err
	}
	if value.ContentDigestSHA256 != v1.ElevatedCapabilityOutcomesContentDigest(value) {
		return fmt.Errorf("content_digest_sha256 does not bind the elevated capability outcomes")
	}
	return nil
}
