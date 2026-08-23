package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAgentRunWireContractUsesNestedVersionedAuthority(t *testing.T) {
	job := JobSpec{
		JobID: "job-1", AttemptID: "attempt-1",
		AgentRunAuthority: &AgentRunAuthority{
			SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 2,
		},
	}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"agent_run_authority":{"schema_version":1,"agent_run_id":"agent-run-1","step_run_id":"step-run-1","step_sequence":2}`) {
		t.Fatalf("agent authority is not nested and exact: %s", raw)
	}

	legacy, err := json.Marshal(JobSpec{JobID: "job-1", AttemptID: "attempt-1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), "agent_run_authority") {
		t.Fatalf("legacy job grew a partial authority: %s", legacy)
	}
}

func TestControlJobCommandWireContract(t *testing.T) {
	now := time.Date(2026, 8, 23, 15, 0, 0, 0, time.UTC)
	command := CommandPayload{
		Command:   CmdControlJob,
		ControlID: "control-1", Action: JobControlPause, Reason: "human requested pause",
		RequestedAt: now.Format(time.RFC3339Nano), JobID: "job-1", AttemptID: "attempt-1", LeaseID: "lease-1",
		ManifestID: "manifest-1", ManifestDigest: strings.Repeat("a", 64), FencingToken: 7,
		AgentRunAuthority: &AgentRunAuthority{
			SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 2,
		},
	}
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["command"] != CmdControlJob || decoded["control_id"] != "control-1" || decoded["agent_run_authority"] == nil {
		t.Fatalf("control_job command is not encoded exactly: %s", raw)
	}
	operation := command.JobControlOperation()
	if operation.ControlID != command.ControlID || operation.AgentRunAuthority != *command.AgentRunAuthority {
		t.Fatalf("operation=%+v", operation)
	}
}

func TestCapabilityManifestAdvertisesAgentRunTransport(t *testing.T) {
	raw, err := json.Marshal(CapabilityManifest{AgentRunTransportVersion: AgentRunTransportVersion})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"agent_run_transport_version":1`) {
		t.Fatalf("agent-run transport capability missing: %s", raw)
	}
}

func TestAgentRunDigestsMatchControlPlaneVectors(t *testing.T) {
	requestedAt := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	for action, want := range map[JobControlAction]string{
		JobControlPause:  "08cd4b428e3b0de4340a5f9ccb7d93ac56771d9c67b174261078a1f1698dd510",
		JobControlCancel: "edded5395c3eb1cc95dba327122c9eae489c889b4d9a192715867e468fc5d48e",
	} {
		operation := JobControlOperation{
			ControlID: "control-" + string(action), Action: action, Reason: "Operator requested control.",
			RequestedAt: requestedAt.Format(time.RFC3339Nano), ExpiresAt: requestedAt.Add(5 * time.Minute).Format(time.RFC3339Nano),
			LeaseID: "lease-1", JobID: "job-1", AttemptID: "attempt-1", ManifestID: "manifest-1",
			ManifestDigest: strings.Repeat("a", 64), FencingToken: 7,
			AgentRunAuthority: AgentRunAuthority{SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 1},
		}
		if got := JobControlCommandDigest(operation); got != want {
			t.Fatalf("%s command digest=%s, want %s", action, got, want)
		}
	}

	repositories := []RecoveryCheckpointRepository{
		{RepositoryID: "api", Branch: "feature/abc-123", CommitSHA: strings.Repeat("1", 40)},
		{RepositoryID: "web", Branch: "feature/abc-123", CommitSHA: strings.Repeat("2", 40), PatchDigestSHA256: strings.Repeat("d", 64)},
	}
	checkpoint := TrustedRecoveryCheckpoint{
		SchemaVersion: 1, CheckpointID: "checkpoint-1", Collector: "runner", Repositories: repositories,
		RepositorySetDigest:  RecoveryRepositorySetDigest(repositories),
		SafetyClassification: RecoverySafetyDisplayOnly, SideEffectClassification: RecoverySideEffectsUnknown,
		TestClassification: RecoveryTestsUnknown,
	}
	if checkpoint.RepositorySetDigest != "2d9b212c822cdeadeec6c89847a9b7fd93a738499beaeb0f1d2da993f0ea1e74" {
		t.Fatalf("repository set digest=%s", checkpoint.RepositorySetDigest)
	}
	if got := TrustedRecoveryCheckpointDigest(checkpoint); got != "9d9d730954d2fb129ef1d9eaf1f64e1d884cd39f419e97a656404d4521eac13e" {
		t.Fatalf("checkpoint digest=%s", got)
	}

	logEvent := SanitizedLogEvent{
		Source: SanitizedLogAgentStatus, Summary: "Implemented the bounded protocol schema.",
		Detail: "Focused schema validation passed after redaction.", Truncated: false,
		Redaction: SanitizedLogRedaction{SchemaVersion: 1, PolicyVersion: "runner-redaction-1", Collector: "runner", RedactionCount: 2, Categories: []string{"credential", "prompt"}},
	}
	if got := SanitizedLogContentDigest(logEvent); got != "4fb9535ff5919f585e09bef8fa7f02d3d65758355d5d40d32561bba86cc84f99" {
		t.Fatalf("sanitized log digest=%s", got)
	}
}

func TestControlDigestPreservesExactRFC3339WireText(t *testing.T) {
	operation := JobControlOperation{
		ControlID: "control-1", Action: JobControlPause, Reason: "pause",
		RequestedAt: "2026-08-23T14:00:00.000+02:00", ExpiresAt: "2026-08-23T14:01:00.000+02:00",
		LeaseID: "lease-1", JobID: "job-1", AttemptID: "attempt-1", ManifestID: "manifest-1",
		ManifestDigest: strings.Repeat("a", 64), FencingToken: 7,
		AgentRunAuthority: AgentRunAuthority{SchemaVersion: 1, AgentRunID: "agent-run-1", StepRunID: "step-run-1", StepSequence: 1},
	}
	want, err := CommandContentDigest(map[string]any{
		"command": CmdControlJob, "control_id": operation.ControlID, "action": operation.Action,
		"reason": operation.Reason, "requested_at": operation.RequestedAt, "expires_at": operation.ExpiresAt,
		"lease_id": operation.LeaseID, "job_id": operation.JobID, "attempt_id": operation.AttemptID,
		"manifest_id": operation.ManifestID, "manifest_digest": operation.ManifestDigest,
		"fencing_token": operation.FencingToken, "agent_run_authority": operation.AgentRunAuthority,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := JobControlCommandDigest(operation); got != want {
		t.Fatalf("wire-preserving digest=%s want %s", got, want)
	}
}
