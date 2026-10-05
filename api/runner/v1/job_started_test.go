package v1_test

import (
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

type jobStartedFixture struct {
	Version  int                `json:"version"`
	Type     v1.MessageType     `json:"type"`
	ID       string             `json:"id"`
	SentAt   time.Time          `json:"sent_at"`
	RunnerID string             `json:"runner_id"`
	Payload  v1.JobStartedEvent `json:"payload"`
}

func TestCanonicalJobStartedFixtureStrictlyParsesAndValidates(t *testing.T) {
	var fixture jobStartedFixture
	decodeStrictFixture(t, "testdata/job_started_v1_event.json", &fixture)
	if fixture.Version != v1.ProtocolVersion || fixture.Type != v1.TypeEvent {
		t.Fatalf("fixture envelope = %+v", fixture)
	}
	if err := validation.JobStartedEvent(fixture.Payload); err != nil {
		t.Fatal(err)
	}
	if fixture.Payload.AgentRunAuthority.AgentRunID != "agent-run-1" ||
		fixture.Payload.AgentRunAuthority.StepRunID != "step-run-1" {
		t.Fatalf("fixture AgentRun authority = %+v", fixture.Payload.AgentRunAuthority)
	}
}
