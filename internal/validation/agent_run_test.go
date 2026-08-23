package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validAgentRunAuthority() v1.AgentRunAuthority {
	return v1.AgentRunAuthority{SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 1}
}

func validJobControl(now time.Time) v1.JobControlOperation {
	op := v1.JobControlOperation{
		ControlID: "control-1", Action: v1.JobControlPause, Reason: "operator requested pause",
		RequestedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano),
		JobID: "job-1", AttemptID: "attempt-1", LeaseID: "lease-1", ManifestID: "manifest-1",
		ManifestDigest: strings.Repeat("a", 64), FencingToken: 7, AgentRunAuthority: validAgentRunAuthority(),
	}
	op.CommandDigest = v1.JobControlCommandDigest(op)
	return op
}

func TestJobSpecAcceptsOnlyCompleteAgentRunAuthority(t *testing.T) {
	job := validJob()
	authority := validAgentRunAuthority()
	job.AgentRunAuthority = &authority
	if err := JobSpec(job); err != nil {
		t.Fatalf("valid authority rejected: %v", err)
	}
	for name, mutate := range map[string]func(*v1.AgentRunAuthority){
		"schema":   func(value *v1.AgentRunAuthority) { value.SchemaVersion = 2 },
		"agent":    func(value *v1.AgentRunAuthority) { value.AgentRunID = "" },
		"step":     func(value *v1.AgentRunAuthority) { value.StepRunID = "bad/id" },
		"sequence": func(value *v1.AgentRunAuthority) { value.StepSequence = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := authority
			mutate(&candidate)
			if err := AgentRunAuthority(candidate); err == nil {
				t.Fatal("invalid authority accepted")
			}
		})
	}
}

func TestJobControlOperationRequiresFreshExactDigest(t *testing.T) {
	now := time.Now().UTC()
	valid := validJobControl(now)
	if err := JobControlOperation(valid, now); err != nil {
		t.Fatalf("valid control rejected: %v", err)
	}
	for name, mutate := range map[string]func(*v1.JobControlOperation){
		"action": func(value *v1.JobControlOperation) { value.Action = "retry" },
		"reason": func(value *v1.JobControlOperation) { value.Reason = " " },
		"stale":  func(value *v1.JobControlOperation) { value.ExpiresAt = now.Add(-time.Second).Format(time.RFC3339Nano) },
		"future": func(value *v1.JobControlOperation) {
			value.RequestedAt = now.Add(time.Second).Format(time.RFC3339Nano)
		},
		"long window": func(value *v1.JobControlOperation) {
			requestedAt, _ := time.Parse(time.RFC3339Nano, value.RequestedAt)
			value.ExpiresAt = requestedAt.Add(5*time.Minute + time.Nanosecond).Format(time.RFC3339Nano)
		},
		"digest": func(value *v1.JobControlOperation) { value.CommandDigest = strings.Repeat("b", 64) },
		"fence":  func(value *v1.JobControlOperation) { value.FencingToken = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if name != "digest" {
				candidate.CommandDigest = v1.JobControlCommandDigest(candidate)
			}
			if err := JobControlOperation(candidate, now); err == nil {
				t.Fatal("invalid control accepted")
			}
		})
	}
}

func validRecoveryCheckpoint() v1.TrustedRecoveryCheckpoint {
	repositories := []v1.RecoveryCheckpointRepository{
		{RepositoryID: "api", Branch: "main", CommitSHA: strings.Repeat("1", 40)},
		{RepositoryID: "web", Branch: "main", CommitSHA: strings.Repeat("2", 40), PatchDigestSHA256: strings.Repeat("a", 64)},
	}
	checkpoint := v1.TrustedRecoveryCheckpoint{
		SchemaVersion: 1, CheckpointID: "checkpoint-1", Collector: "runner", Repositories: repositories,
		RepositorySetDigest:  v1.RecoveryRepositorySetDigest(repositories),
		SafetyClassification: v1.RecoverySafetyDisplayOnly, SideEffectClassification: v1.RecoverySideEffectsUnknown,
		TestClassification: v1.RecoveryTestsUnknown,
	}
	checkpoint.DigestSHA256 = v1.TrustedRecoveryCheckpointDigest(checkpoint)
	return checkpoint
}

func TestTrustedRecoveryCheckpointRequiresCanonicalExactRepositorySet(t *testing.T) {
	valid := validRecoveryCheckpoint()
	if err := TrustedRecoveryCheckpoint(valid); err != nil {
		t.Fatalf("valid checkpoint rejected: %v", err)
	}
	safe := valid
	safe.SafetyClassification = v1.RecoverySafetySafeToResume
	safe.SideEffectClassification = v1.RecoverySideEffectsNone
	safe.TestClassification = v1.RecoveryTestsPassed
	safe.DigestSHA256 = v1.TrustedRecoveryCheckpointDigest(safe)
	if err := TrustedRecoveryCheckpoint(safe); err != nil {
		t.Fatalf("host-classified safe checkpoint rejected: %v", err)
	}
	for name, mutate := range map[string]func(*v1.TrustedRecoveryCheckpoint){
		"schema":             func(value *v1.TrustedRecoveryCheckpoint) { value.SchemaVersion = 2 },
		"collector":          func(value *v1.TrustedRecoveryCheckpoint) { value.Collector = "agent" },
		"checkpoint":         func(value *v1.TrustedRecoveryCheckpoint) { value.CheckpointID = "bad/id" },
		"empty repositories": func(value *v1.TrustedRecoveryCheckpoint) { value.Repositories = nil },
		"safety":             func(value *v1.TrustedRecoveryCheckpoint) { value.SafetyClassification = "model_claim" },
		"side effects":       func(value *v1.TrustedRecoveryCheckpoint) { value.SideEffectClassification = "unresolved" },
		"tests":              func(value *v1.TrustedRecoveryCheckpoint) { value.TestClassification = "skipped" },
		"unsafe safe resume": func(value *v1.TrustedRecoveryCheckpoint) {
			value.SafetyClassification = v1.RecoverySafetySafeToResume
			value.SideEffectClassification = v1.RecoverySideEffectsUnknown
		},
		"untested safe resume": func(value *v1.TrustedRecoveryCheckpoint) {
			value.SafetyClassification = v1.RecoverySafetySafeToResume
			value.SideEffectClassification = v1.RecoverySideEffectsNone
			value.TestClassification = v1.RecoveryTestsUnknown
		},
		"order": func(value *v1.TrustedRecoveryCheckpoint) {
			value.Repositories[0], value.Repositories[1] = value.Repositories[1], value.Repositories[0]
		},
		"branch":            func(value *v1.TrustedRecoveryCheckpoint) { value.Repositories[0].Branch = "bad branch" },
		"commit":            func(value *v1.TrustedRecoveryCheckpoint) { value.Repositories[0].CommitSHA = "abc" },
		"patch":             func(value *v1.TrustedRecoveryCheckpoint) { value.Repositories[1].PatchDigestSHA256 = "abc" },
		"repository digest": func(value *v1.TrustedRecoveryCheckpoint) { value.RepositorySetDigest = strings.Repeat("b", 64) },
		"checkpoint digest": func(value *v1.TrustedRecoveryCheckpoint) { value.DigestSHA256 = strings.Repeat("b", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Repositories = append([]v1.RecoveryCheckpointRepository(nil), valid.Repositories...)
			mutate(&candidate)
			if err := TrustedRecoveryCheckpoint(candidate); err == nil {
				t.Fatal("invalid checkpoint accepted")
			}
		})
	}
}

func validControlResult() v1.JobControlResultEvent {
	return v1.JobControlResultEvent{
		Kind: "job_control_result", ManifestKind: v1.JobKindBuild, LeaseID: "lease-1", JobID: "job-1",
		AttemptID: "attempt-1", ManifestID: "manifest-1", ManifestDigest: strings.Repeat("a", 64),
		FencingToken: 7, ObservedAt: time.Now().UTC(), AgentRunAuthority: validAgentRunAuthority(),
		ControlID: "control-1", CommandDigest: strings.Repeat("b", 64), Action: v1.JobControlPause, Outcome: v1.JobControlApplied,
	}
}

func TestJobControlResultRequiresConsistentControlledOutcome(t *testing.T) {
	valid := validControlResult()
	values := []v1.JobControlResultEvent{valid}
	already := valid
	already.Outcome, already.ReasonCode = v1.JobControlAlreadyApplied, v1.JobControlReasonAlreadyPaused
	values = append(values, already)
	rejected := valid
	rejected.Outcome, rejected.ReasonCode = v1.JobControlRejected, v1.JobControlReasonStaleAuthority
	values = append(values, rejected)
	for _, value := range values {
		if err := JobControlResultEvent(value); err != nil {
			t.Fatalf("valid result rejected: %v", err)
		}
	}
	for name, mutate := range map[string]func(*v1.JobControlResultEvent){
		"kind":      func(value *v1.JobControlResultEvent) { value.Kind = "control" },
		"authority": func(value *v1.JobControlResultEvent) { value.AgentRunAuthority.StepRunID = "" },
		"control":   func(value *v1.JobControlResultEvent) { value.ControlID = "" },
		"digest":    func(value *v1.JobControlResultEvent) { value.CommandDigest = "bad" },
		"action":    func(value *v1.JobControlResultEvent) { value.Action = "retry" },
		"unknown reason": func(value *v1.JobControlResultEvent) {
			value.Outcome, value.ReasonCode = v1.JobControlRejected, "unknown"
		},
		"applied reason": func(value *v1.JobControlResultEvent) { value.ReasonCode = v1.JobControlReasonPolicyDenied },
		"cancel checkpoint": func(value *v1.JobControlResultEvent) {
			value.Action, value.Outcome, value.ReasonCode = v1.JobControlCancel, v1.JobControlRejected, v1.JobControlReasonCheckpointUnavailable
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := JobControlResultEvent(candidate); err == nil {
				t.Fatal("invalid result accepted")
			}
		})
	}
}

func validSanitizedLog() v1.SanitizedLogEvent {
	value := v1.SanitizedLogEvent{
		Kind: "sanitized_log", ManifestKind: v1.JobKindBuild, LeaseID: "lease-1", JobID: "job-1",
		AttemptID: "attempt-1", ManifestID: "manifest-1", ManifestDigest: strings.Repeat("a", 64),
		FencingToken: 7, ObservedAt: time.Now().UTC(), AgentRunAuthority: validAgentRunAuthority(),
		Source: v1.SanitizedLogAgentStatus, Summary: "Agent status: completed", Detail: "Status: success",
		Redaction: v1.SanitizedLogRedaction{SchemaVersion: 1, PolicyVersion: "runner-redaction-1", Collector: "runner", RedactionCount: 1, Categories: []string{"tool_output"}},
	}
	value.ContentDigestSHA256 = v1.SanitizedLogContentDigest(value)
	return value
}

func TestSanitizedLogRequiresExactRedactionMetadataAndDigest(t *testing.T) {
	valid := validSanitizedLog()
	if err := SanitizedLogEvent(valid); err != nil {
		t.Fatalf("valid sanitized log rejected: %v", err)
	}
	for name, mutate := range map[string]func(*v1.SanitizedLogEvent){
		"kind":     func(value *v1.SanitizedLogEvent) { value.Kind = "log" },
		"source":   func(value *v1.SanitizedLogEvent) { value.Source = "stdout" },
		"summary":  func(value *v1.SanitizedLogEvent) { value.Summary = " " },
		"detail":   func(value *v1.SanitizedLogEvent) { value.Detail = " " },
		"schema":   func(value *v1.SanitizedLogEvent) { value.Redaction.SchemaVersion = 2 },
		"policy":   func(value *v1.SanitizedLogEvent) { value.Redaction.PolicyVersion = "bad\x00policy" },
		"count":    func(value *v1.SanitizedLogEvent) { value.Redaction.RedactionCount = 0 },
		"order":    func(value *v1.SanitizedLogEvent) { value.Redaction.Categories = []string{"tool_output", "credential"} },
		"category": func(value *v1.SanitizedLogEvent) { value.Redaction.Categories = []string{"raw_output"} },
		"digest":   func(value *v1.SanitizedLogEvent) { value.ContentDigestSHA256 = strings.Repeat("c", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Redaction.Categories = append([]string(nil), valid.Redaction.Categories...)
			mutate(&candidate)
			if err := SanitizedLogEvent(candidate); err == nil {
				t.Fatal("invalid sanitized log accepted")
			}
		})
	}
}
