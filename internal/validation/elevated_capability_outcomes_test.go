package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validElevatedCapabilityOutcomes() v1.ElevatedCapabilityOutcomesEvent {
	authority := validAgentRunAuthority()
	value := v1.ElevatedCapabilityOutcomesEvent{
		Kind: v1.EventKindElevatedCapabilityOutcomes, ManifestKind: v1.JobKindBuild, LeaseID: "lease-1", JobID: "job-1",
		AttemptID: "attempt-1", ManifestID: "manifest-1", ManifestDigest: strings.Repeat("a", 64),
		FencingToken: 7, ObservedAt: time.Now().UTC(), AgentRunAuthority: &authority,
		SchemaVersion: v1.ElevatedCapabilityOutcomesSchemaVersion,
		Outcomes: []v1.ElevatedCapabilityOutcome{
			{RequestID: "ecr_mcp", Applied: true, Reason: "applied"},
			{RequestID: "ecr_hook", Applied: false, Reason: "container_program_unverifiable"},
		},
	}
	value.ContentDigestSHA256 = v1.ElevatedCapabilityOutcomesContentDigest(value)
	return value
}

func TestElevatedCapabilityOutcomesDigestVector(t *testing.T) {
	if got := validElevatedCapabilityOutcomes().ContentDigestSHA256; got != "5d304257b6e0700d33f56afa9cdb1b0133a4ff32e2fb166e7249be088d93efa4" {
		t.Fatalf("digest = %s", got)
	}
}

func TestElevatedCapabilityOutcomesEvent(t *testing.T) {
	if err := ElevatedCapabilityOutcomesEvent(validElevatedCapabilityOutcomes()); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	withoutAuthority := validElevatedCapabilityOutcomes()
	withoutAuthority.AgentRunAuthority = nil
	if err := ElevatedCapabilityOutcomesEvent(withoutAuthority); err != nil {
		t.Fatalf("event without agent run authority rejected: %v", err)
	}
	for name, mutate := range map[string]func(*v1.ElevatedCapabilityOutcomesEvent){
		"kind":            func(value *v1.ElevatedCapabilityOutcomesEvent) { value.Kind = "run_network_policy" },
		"schema":          func(value *v1.ElevatedCapabilityOutcomesEvent) { value.SchemaVersion = 2 },
		"empty":           func(value *v1.ElevatedCapabilityOutcomesEvent) { value.Outcomes = nil },
		"duplicate":       func(value *v1.ElevatedCapabilityOutcomesEvent) { value.Outcomes[1].RequestID = "ecr_mcp" },
		"request id":      func(value *v1.ElevatedCapabilityOutcomesEvent) { value.Outcomes[0].RequestID = "../x" },
		"unknown reason":  func(value *v1.ElevatedCapabilityOutcomesEvent) { value.Outcomes[1].Reason = "because" },
		"applied refusal": func(value *v1.ElevatedCapabilityOutcomesEvent) { value.Outcomes[1].Applied = true },
		"refused applied": func(value *v1.ElevatedCapabilityOutcomesEvent) { value.Outcomes[0].Applied = false },
		"fence":           func(value *v1.ElevatedCapabilityOutcomesEvent) { value.FencingToken = 0 },
		"authority":       func(value *v1.ElevatedCapabilityOutcomesEvent) { value.AgentRunAuthority.SchemaVersion = 9 },
		"too many": func(value *v1.ElevatedCapabilityOutcomesEvent) {
			outcomes := make([]v1.ElevatedCapabilityOutcome, v1.MaxElevatedCapabilityGrants+1)
			for index := range outcomes {
				outcomes[index] = v1.ElevatedCapabilityOutcome{RequestID: "ecr_" + strings.Repeat("x", index+1), Reason: "expired"}
			}
			value.Outcomes = outcomes
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := validElevatedCapabilityOutcomes()
			authority := *candidate.AgentRunAuthority
			candidate.AgentRunAuthority = &authority
			candidate.Outcomes = append([]v1.ElevatedCapabilityOutcome(nil), candidate.Outcomes...)
			mutate(&candidate)
			candidate.ContentDigestSHA256 = v1.ElevatedCapabilityOutcomesContentDigest(candidate)
			if err := ElevatedCapabilityOutcomesEvent(candidate); err == nil {
				t.Fatal("invalid event accepted")
			}
		})
	}
	tampered := validElevatedCapabilityOutcomes()
	tampered.Outcomes[1].Reason = "expired"
	if err := ElevatedCapabilityOutcomesEvent(tampered); err == nil {
		t.Fatal("event whose digest does not bind its outcomes accepted")
	}
}
