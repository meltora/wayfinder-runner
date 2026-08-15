package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validJobFinishedEvent() v1.JobFinishedEvent {
	return v1.JobFinishedEvent{
		Kind: "job_finished", ManifestKind: v1.JobKindBuild, LeaseID: "lease-1",
		JobID: "job-1", AttemptID: "attempt-1", ManifestID: "manifest-1",
		ManifestDigest: strings.Repeat("a", 64), FencingToken: 7, ObservedAt: time.Now().UTC(),
		State: v1.JobFinishedSucceeded,
	}
}

func TestJobFinishedEventAcceptsStateConsistentEvidence(t *testing.T) {
	t.Parallel()
	valid := []v1.JobFinishedEvent{validJobFinishedEvent()}
	for _, pair := range []struct {
		state v1.JobFinishedState
		code  v1.JobFinishedFailureCode
	}{
		{v1.JobFinishedFailed, v1.JobFailureAgentExit},
		{v1.JobFinishedCancelled, v1.JobFailureCancelled},
		{v1.JobFinishedQuarantined, v1.JobFailureLeaseLost},
		{v1.JobFinishedQuarantined, v1.JobFailureCleanup},
	} {
		event := validJobFinishedEvent()
		event.State, event.FailureCode, event.FailureMessage = pair.state, pair.code, "bounded detail"
		valid = append(valid, event)
	}
	for category, retryable := range map[v1.AgentPackMaterializationFailureCategory]bool{
		v1.AgentPackFailureDownloadUnavailable: true,
		v1.AgentPackFailureStorageUnavailable:  true,
		v1.AgentPackFailureDigestMismatch:      false,
		v1.AgentPackFailureArchiveInvalid:      false,
	} {
		event := validJobFinishedEvent()
		event.State = v1.JobFinishedFailed
		event.FailureCode = v1.JobFailureAgentPackMaterialization
		event.FailureCategory = category
		event.Retryable = boolPointer(retryable)
		valid = append(valid, event)
	}
	for _, event := range valid {
		if err := JobFinishedEvent(event); err != nil {
			t.Fatalf("valid event rejected: %#v: %v", event, err)
		}
	}
}

func TestJobFinishedEventRejectsInvalidAuthorityAndFailureCombinations(t *testing.T) {
	t.Parallel()
	mutations := map[string]func(*v1.JobFinishedEvent){
		"kind":            func(event *v1.JobFinishedEvent) { event.Kind = "finished" },
		"manifest":        func(event *v1.JobFinishedEvent) { event.ManifestID = "" },
		"digest":          func(event *v1.JobFinishedEvent) { event.ManifestDigest = strings.Repeat("A", 64) },
		"fence":           func(event *v1.JobFinishedEvent) { event.FencingToken = 0 },
		"observed":        func(event *v1.JobFinishedEvent) { event.ObservedAt = time.Time{} },
		"state":           func(event *v1.JobFinishedEvent) { event.State = "waiting" },
		"success code":    func(event *v1.JobFinishedEvent) { event.FailureCode = v1.JobFailureAgentExit },
		"failure no code": func(event *v1.JobFinishedEvent) { event.State = v1.JobFinishedFailed },
		"failure cancelled code": func(event *v1.JobFinishedEvent) {
			event.State, event.FailureCode = v1.JobFinishedFailed, v1.JobFailureCancelled
		},
		"cancel mismatch": func(event *v1.JobFinishedEvent) {
			event.State, event.FailureCode = v1.JobFinishedCancelled, v1.JobFailureAgentExit
		},
		"quarantine mismatch": func(event *v1.JobFinishedEvent) {
			event.State, event.FailureCode = v1.JobFinishedQuarantined, v1.JobFailureAgentExit
		},
		"materialization category missing": func(event *v1.JobFinishedEvent) {
			event.State, event.FailureCode = v1.JobFinishedFailed, v1.JobFailureAgentPackMaterialization
		},
		"materialization retryability mismatch": func(event *v1.JobFinishedEvent) {
			event.State, event.FailureCode = v1.JobFinishedFailed, v1.JobFailureAgentPackMaterialization
			event.FailureCategory, event.Retryable = v1.AgentPackFailureDigestMismatch, boolPointer(true)
		},
		"unrelated category": func(event *v1.JobFinishedEvent) {
			event.State, event.FailureCode = v1.JobFinishedFailed, v1.JobFailureAgentExit
			event.FailureCategory, event.Retryable = v1.AgentPackFailureDownloadUnavailable, boolPointer(true)
		},
		"message": func(event *v1.JobFinishedEvent) {
			event.State, event.FailureCode, event.FailureMessage = v1.JobFinishedFailed, v1.JobFailureAgentExit, strings.Repeat("x", 513)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			event := validJobFinishedEvent()
			mutate(&event)
			if err := JobFinishedEvent(event); err == nil {
				t.Fatalf("invalid event accepted: %#v", event)
			}
		})
	}
}

func boolPointer(value bool) *bool { return &value }
