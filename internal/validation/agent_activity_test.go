package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validAgentActivityPolicy() v1.AgentActivityPolicy {
	return v1.AgentActivityPolicy{SchemaVersion: 1, MaxEvents: 200, MaxEventsPerMinute: 30, MaxMessageBytes: 280}
}

func TestAgentActivityPolicyBounds(t *testing.T) {
	if err := AgentActivityPolicy(validAgentActivityPolicy()); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	for name, mutate := range map[string]func(*v1.AgentActivityPolicy){
		"schema":           func(value *v1.AgentActivityPolicy) { value.SchemaVersion = 2 },
		"events zero":      func(value *v1.AgentActivityPolicy) { value.MaxEvents = 0 },
		"events high":      func(value *v1.AgentActivityPolicy) { value.MaxEvents = 501 },
		"rate zero":        func(value *v1.AgentActivityPolicy) { value.MaxEventsPerMinute = 0 },
		"rate high":        func(value *v1.AgentActivityPolicy) { value.MaxEventsPerMinute = 61 },
		"message low":      func(value *v1.AgentActivityPolicy) { value.MaxMessageBytes = 63 },
		"message too long": func(value *v1.AgentActivityPolicy) { value.MaxMessageBytes = 481 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := validAgentActivityPolicy()
			mutate(&candidate)
			if err := AgentActivityPolicy(candidate); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

func TestJobRejectsInvalidAgentActivityPolicy(t *testing.T) {
	job := validJob()
	policy := validAgentActivityPolicy()
	job.AgentActivity = &policy
	if err := JobSpec(job); err != nil {
		t.Fatalf("valid job rejected: %v", err)
	}
	policy.MaxEvents = 0
	if err := JobSpec(job); err == nil {
		t.Fatal("job with invalid agent activity accepted")
	}
}

func validAgentActivity() v1.AgentActivityEvent {
	value := v1.AgentActivityEvent{
		Kind: v1.EventKindAgentActivity, ManifestKind: v1.JobKindBuild, LeaseID: "lease-1", JobID: "job-1",
		AttemptID: "attempt-1", ManifestID: "manifest-1", ManifestDigest: strings.Repeat("a", 64),
		FencingToken: 7, ObservedAt: time.Now().UTC(), AgentRunAuthority: validAgentRunAuthority(),
		Sequence: 1, ActivityType: v1.AgentActivityAssistantMessage, Message: "Reading the parser.",
		Redaction: v1.SanitizedLogRedaction{SchemaVersion: 1, PolicyVersion: v1.AgentActivityRedactionPolicyVersion, Collector: "runner", Categories: []string{}},
	}
	value.ContentDigestSHA256 = v1.AgentActivityContentDigest(value)
	return value
}

func TestAgentActivityEventValidation(t *testing.T) {
	if err := AgentActivityEvent(validAgentActivity(), 280); err != nil {
		t.Fatalf("valid activity rejected: %v", err)
	}
	for name, mutate := range map[string]func(*v1.AgentActivityEvent){
		"kind":         func(value *v1.AgentActivityEvent) { value.Kind = "sanitized_log" },
		"sequence":     func(value *v1.AgentActivityEvent) { value.Sequence = 0 },
		"sequence max": func(value *v1.AgentActivityEvent) { value.Sequence = 501 },
		"type":         func(value *v1.AgentActivityEvent) { value.ActivityType = "reasoning" },
		"blank":        func(value *v1.AgentActivityEvent) { value.Message = "  " },
		"policy cap":   func(value *v1.AgentActivityEvent) { value.Message = strings.Repeat("a", 281) },
		"utf8":         func(value *v1.AgentActivityEvent) { value.Message = "bad\xff" },
		"schema":       func(value *v1.AgentActivityEvent) { value.Redaction.SchemaVersion = 2 },
		"collector":    func(value *v1.AgentActivityEvent) { value.Redaction.Collector = "agent" },
		"count":        func(value *v1.AgentActivityEvent) { value.Redaction.RedactionCount = 1 },
		"order": func(value *v1.AgentActivityEvent) {
			value.Redaction.RedactionCount = 2
			value.Redaction.Categories = []string{"personal_data", "credential"}
		},
		"category": func(value *v1.AgentActivityEvent) {
			value.Redaction.RedactionCount = 1
			value.Redaction.Categories = []string{"raw"}
		},
		"authority": func(value *v1.AgentActivityEvent) { value.AgentRunAuthority = v1.AgentRunAuthority{} },
		"fence":     func(value *v1.AgentActivityEvent) { value.FencingToken = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := validAgentActivity()
			mutate(&candidate)
			candidate.ContentDigestSHA256 = v1.AgentActivityContentDigest(candidate)
			if err := AgentActivityEvent(candidate, 280); err == nil {
				t.Fatal("invalid activity accepted")
			}
		})
	}
	tampered := validAgentActivity()
	tampered.Message = "Something else."
	if err := AgentActivityEvent(tampered, 280); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	long := validAgentActivity()
	long.Message = strings.Repeat("a", 481)
	long.ContentDigestSHA256 = v1.AgentActivityContentDigest(long)
	if err := AgentActivityEvent(long, 1000); err == nil {
		t.Fatal("protocol hard cap not enforced")
	}
}
