package validation

import (
	"fmt"
	"sort"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func AgentRunAuthority(value v1.AgentRunAuthority) error {
	if value.SchemaVersion != v1.AgentRunTransportVersion {
		return fmt.Errorf("agent_run_authority.schema_version must be %d", v1.AgentRunTransportVersion)
	}
	if err := ID("agent_run_id", value.AgentRunID); err != nil {
		return err
	}
	if err := ID("step_run_id", value.StepRunID); err != nil {
		return err
	}
	if value.StepSequence < 1 || value.StepSequence > maxJSONSafeInteger {
		return fmt.Errorf("step_sequence is outside the protocol JSON-safe range")
	}
	return nil
}

func JobControlOperation(value v1.JobControlOperation, now time.Time) error {
	if err := ID("control_id", value.ControlID); err != nil {
		return err
	}
	if value.Action != v1.JobControlPause && value.Action != v1.JobControlCancel {
		return fmt.Errorf("action must be pause or cancel")
	}
	if err := boundedText("reason", value.Reason, 16<<10, true); err != nil || strings.TrimSpace(value.Reason) == "" {
		return fmt.Errorf("reason is required and must be at most %d bytes", 16<<10)
	}
	requestedAt, requestedErr := time.Parse(time.RFC3339Nano, value.RequestedAt)
	expiresAt, expiresErr := time.Parse(time.RFC3339Nano, value.ExpiresAt)
	if requestedErr != nil || expiresErr != nil || !expiresAt.After(requestedAt) ||
		expiresAt.Sub(requestedAt) > 5*time.Minute {
		return fmt.Errorf("control authority window is invalid")
	}
	if !expiresAt.After(now) || requestedAt.After(now) {
		return fmt.Errorf("control authority is not current")
	}
	for field, id := range map[string]string{
		"job_id": value.JobID, "attempt_id": value.AttemptID, "lease_id": value.LeaseID,
		"manifest_id": value.ManifestID,
	} {
		if err := ID(field, id); err != nil {
			return err
		}
	}
	if err := SHA256Digest("manifest_digest", value.ManifestDigest); err != nil {
		return err
	}
	if value.FencingToken == 0 || value.FencingToken > maxJSONSafeInteger {
		return fmt.Errorf("fencing_token is outside the protocol JSON-safe range")
	}
	if err := AgentRunAuthority(value.AgentRunAuthority); err != nil {
		return err
	}
	if err := SHA256Digest("command_digest", value.CommandDigest); err != nil {
		return err
	}
	if value.CommandDigest != v1.JobControlCommandDigest(value) {
		return fmt.Errorf("command_digest does not bind the exact control command")
	}
	return nil
}

func TrustedRecoveryCheckpoint(value v1.TrustedRecoveryCheckpoint) error {
	if value.SchemaVersion != 1 || value.Collector != "runner" {
		return fmt.Errorf("trusted recovery checkpoint collector authority is invalid")
	}
	if err := ID("checkpoint_id", value.CheckpointID); err != nil {
		return err
	}
	if len(value.Repositories) < 1 || len(value.Repositories) > 16 {
		return fmt.Errorf("trusted recovery checkpoint repository count is invalid")
	}
	if value.SafetyClassification != v1.RecoverySafetyDisplayOnly && value.SafetyClassification != v1.RecoverySafetySafeToResume {
		return fmt.Errorf("trusted recovery safety classification is invalid")
	}
	if value.SideEffectClassification != v1.RecoverySideEffectsNone &&
		value.SideEffectClassification != v1.RecoverySideEffectsResolved &&
		value.SideEffectClassification != v1.RecoverySideEffectsUnknown {
		return fmt.Errorf("trusted recovery side-effect classification is invalid")
	}
	if value.TestClassification != v1.RecoveryTestsPassed && value.TestClassification != v1.RecoveryTestsFailed &&
		value.TestClassification != v1.RecoveryTestsUnknown {
		return fmt.Errorf("trusted recovery test classification is invalid")
	}
	if value.SafetyClassification == v1.RecoverySafetySafeToResume &&
		value.SideEffectClassification != v1.RecoverySideEffectsNone && value.SideEffectClassification != v1.RecoverySideEffectsResolved {
		return fmt.Errorf("safe-to-resume recovery requires resolved or absent side effects")
	}
	if value.SafetyClassification == v1.RecoverySafetySafeToResume && value.TestClassification != v1.RecoveryTestsPassed {
		return fmt.Errorf("safe-to-resume recovery requires passed tests")
	}
	for index, repository := range value.Repositories {
		if err := ID("repository_id", repository.RepositoryID); err != nil {
			return err
		}
		if index > 0 && value.Repositories[index-1].RepositoryID >= repository.RepositoryID {
			return fmt.Errorf("trusted recovery repositories must have unique sorted IDs")
		}
		if err := GitRef("branch", repository.Branch); err != nil {
			return err
		}
		if err := GitCommitSHA("commit_sha", repository.CommitSHA); err != nil {
			return err
		}
		if repository.PatchDigestSHA256 != "" {
			if err := SHA256Digest("patch_digest_sha256", repository.PatchDigestSHA256); err != nil {
				return err
			}
		}
	}
	if err := SHA256Digest("repository_set_digest", value.RepositorySetDigest); err != nil {
		return err
	}
	if value.RepositorySetDigest != v1.RecoveryRepositorySetDigest(value.Repositories) {
		return fmt.Errorf("repository_set_digest does not bind the canonical repository set")
	}
	if err := SHA256Digest("digest_sha256", value.DigestSHA256); err != nil {
		return err
	}
	if value.DigestSHA256 != v1.TrustedRecoveryCheckpointDigest(value) {
		return fmt.Errorf("digest_sha256 does not bind the exact recovery checkpoint")
	}
	return nil
}

func JobControlResultEvent(value v1.JobControlResultEvent) error {
	if value.Kind != "job_control_result" {
		return fmt.Errorf("job control result kind is invalid")
	}
	if err := manifestBoundAgentEvent(value.ManifestKind, value.LeaseID, value.JobID, value.AttemptID,
		value.ManifestID, value.ManifestDigest, value.FencingToken, value.ObservedAt, value.AgentRunAuthority); err != nil {
		return err
	}
	if err := ID("control_id", value.ControlID); err != nil {
		return err
	}
	if err := SHA256Digest("command_digest", value.CommandDigest); err != nil {
		return err
	}
	if value.Action != v1.JobControlPause && value.Action != v1.JobControlCancel {
		return fmt.Errorf("job control result action is invalid")
	}
	knownReasons := map[v1.JobControlReasonCode]bool{
		v1.JobControlReasonAlreadyPaused: true, v1.JobControlReasonAlreadyCancelled: true,
		v1.JobControlReasonStaleAuthority: true, v1.JobControlReasonTerminalAttempt: true,
		v1.JobControlReasonCheckpointUnavailable: true, v1.JobControlReasonPolicyDenied: true,
		v1.JobControlReasonRunnerInternalError: true,
	}
	if value.ReasonCode != "" && !knownReasons[value.ReasonCode] {
		return fmt.Errorf("job control result reason is invalid")
	}
	valid := value.Outcome == v1.JobControlApplied && value.ReasonCode == "" ||
		value.Outcome == v1.JobControlAlreadyApplied &&
			(value.Action == v1.JobControlPause && value.ReasonCode == v1.JobControlReasonAlreadyPaused ||
				value.Action == v1.JobControlCancel && value.ReasonCode == v1.JobControlReasonAlreadyCancelled) ||
		value.Outcome == v1.JobControlRejected && value.ReasonCode != "" &&
			value.ReasonCode != v1.JobControlReasonAlreadyPaused && value.ReasonCode != v1.JobControlReasonAlreadyCancelled &&
			(value.ReasonCode != v1.JobControlReasonCheckpointUnavailable || value.Action == v1.JobControlPause)
	if !valid {
		return fmt.Errorf("job control result reason does not match action and outcome")
	}
	return nil
}

func SanitizedLogEvent(value v1.SanitizedLogEvent) error {
	if value.Kind != "sanitized_log" {
		return fmt.Errorf("sanitized log kind is invalid")
	}
	if err := manifestBoundAgentEvent(value.ManifestKind, value.LeaseID, value.JobID, value.AttemptID,
		value.ManifestID, value.ManifestDigest, value.FencingToken, value.ObservedAt, value.AgentRunAuthority); err != nil {
		return err
	}
	if value.Source != v1.SanitizedLogAgentStatus && value.Source != v1.SanitizedLogRunnerStatus &&
		value.Source != v1.SanitizedLogToolSummary && value.Source != v1.SanitizedLogCheckSummary {
		return fmt.Errorf("sanitized log source is invalid")
	}
	if err := boundedText("summary", value.Summary, 1024, true); err != nil || strings.TrimSpace(value.Summary) == "" {
		return fmt.Errorf("sanitized log summary is invalid")
	}
	if value.Detail != "" {
		if err := boundedText("detail", value.Detail, 16<<10, true); err != nil || strings.TrimSpace(value.Detail) == "" {
			return fmt.Errorf("sanitized log detail is invalid")
		}
	}
	policyErr := boundedText("policy_version", value.Redaction.PolicyVersion, 128, true)
	if value.Redaction.SchemaVersion != 1 || value.Redaction.Collector != "runner" || policyErr != nil ||
		strings.TrimSpace(value.Redaction.PolicyVersion) == "" ||
		value.Redaction.RedactionCount < 0 || value.Redaction.RedactionCount > 1_000_000 ||
		(value.Redaction.RedactionCount == 0) != (len(value.Redaction.Categories) == 0) {
		return fmt.Errorf("sanitized log redaction metadata is invalid")
	}
	if !sort.StringsAreSorted(value.Redaction.Categories) {
		return fmt.Errorf("sanitized log redaction categories must be sorted")
	}
	allowed := map[string]bool{
		"command_argument": true, "credential": true, "environment": true, "filesystem_path": true,
		"personal_data": true, "prompt": true, "tool_output": true,
	}
	for index, category := range value.Redaction.Categories {
		if !allowed[category] || index > 0 && value.Redaction.Categories[index-1] == category {
			return fmt.Errorf("sanitized log redaction category is invalid")
		}
	}
	if err := SHA256Digest("content_digest_sha256", value.ContentDigestSHA256); err != nil {
		return err
	}
	if value.ContentDigestSHA256 != v1.SanitizedLogContentDigest(value) {
		return fmt.Errorf("content_digest_sha256 does not bind the sanitized log")
	}
	return nil
}

func manifestBoundAgentEvent(kind v1.JobKind, leaseID, jobID, attemptID, manifestID, manifestDigest string,
	fencingToken uint64, observedAt time.Time, authority v1.AgentRunAuthority) error {
	if kind != v1.JobKindPlan && kind != v1.JobKindBuild {
		return fmt.Errorf("manifest kind is invalid")
	}
	for field, value := range map[string]string{"lease_id": leaseID, "job_id": jobID, "attempt_id": attemptID, "manifest_id": manifestID} {
		if err := ID(field, value); err != nil {
			return err
		}
	}
	if err := SHA256Digest("manifest_digest", manifestDigest); err != nil {
		return err
	}
	if fencingToken == 0 || fencingToken > maxJSONSafeInteger || observedAt.IsZero() {
		return fmt.Errorf("manifest event fence or observation time is invalid")
	}
	return AgentRunAuthority(authority)
}
