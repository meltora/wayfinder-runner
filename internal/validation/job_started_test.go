package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validJobStartedEvent() v1.JobStartedEvent {
	return v1.JobStartedEvent{
		Kind: "job_started", ManifestKind: v1.JobKindBuild,
		LeaseID: "lease-1", JobID: "job-1", AttemptID: "attempt-1",
		ManifestID: "manifest-1", ManifestDigest: strings.Repeat("a", 64),
		FencingToken: 7, ObservedAt: time.Now().UTC(),
		AgentRunAuthority: validAgentRunAuthority(),
	}
}

func TestJobStartedEventRequiresExactManifestAndAgentRunAuthority(t *testing.T) {
	if err := JobStartedEvent(validJobStartedEvent()); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	tests := map[string]func(*v1.JobStartedEvent){
		"kind":          func(event *v1.JobStartedEvent) { event.Kind = "progress" },
		"manifest kind": func(event *v1.JobStartedEvent) { event.ManifestKind = "other" },
		"lease":         func(event *v1.JobStartedEvent) { event.LeaseID = "" },
		"job":           func(event *v1.JobStartedEvent) { event.JobID = "" },
		"attempt":       func(event *v1.JobStartedEvent) { event.AttemptID = "" },
		"manifest":      func(event *v1.JobStartedEvent) { event.ManifestID = "" },
		"digest":        func(event *v1.JobStartedEvent) { event.ManifestDigest = "bad" },
		"fence":         func(event *v1.JobStartedEvent) { event.FencingToken = 0 },
		"observed_at":   func(event *v1.JobStartedEvent) { event.ObservedAt = time.Time{} },
		"agent":         func(event *v1.JobStartedEvent) { event.AgentRunAuthority.AgentRunID = "" },
		"step":          func(event *v1.JobStartedEvent) { event.AgentRunAuthority.StepRunID = "../bad" },
		"sequence":      func(event *v1.JobStartedEvent) { event.AgentRunAuthority.StepSequence = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			event := validJobStartedEvent()
			mutate(&event)
			if err := JobStartedEvent(event); err == nil {
				t.Fatal("invalid job_started event accepted")
			}
		})
	}
}
