package v1_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

type agentPackCompositionFixture struct {
	Version  int                          `json:"version"`
	Type     v1.MessageType               `json:"type"`
	ID       string                       `json:"id"`
	SentAt   time.Time                    `json:"sent_at"`
	RunnerID string                       `json:"runner_id"`
	Payload  v1.AgentPackCompositionEvent `json:"payload"`
}

func TestAgentPackCompositionParityFixtureStrictlyParsesValidatesAndRoundTrips(t *testing.T) {
	var fixture agentPackCompositionFixture
	decodeStrictFixture(t, "testdata/event--agent-pack-composition.json", &fixture)
	if fixture.Version != v1.ProtocolVersion || fixture.Type != v1.TypeEvent {
		t.Fatalf("fixture envelope = %+v", fixture)
	}
	if fixture.Payload.Kind != v1.EventKindAgentPackComposition {
		t.Fatalf("kind = %q", fixture.Payload.Kind)
	}
	if err := validation.AgentPackCompositionEvent(fixture.Payload); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Payload.Layers) != len(v1.AgentPackCompositionLayerOrder) {
		t.Fatalf("layers = %d", len(fixture.Payload.Layers))
	}
	for index, layer := range fixture.Payload.Layers {
		if layer.Layer != v1.AgentPackCompositionLayerOrder[index] {
			t.Fatalf("layer %d = %q", index, layer.Layer)
		}
	}

	raw, err := os.ReadFile("testdata/event--agent-pack-composition.json")
	if err != nil {
		t.Fatal(err)
	}
	var original struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(fixture.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !equalJSON(t, original.Payload, encoded) {
		t.Fatalf("round trip differs:\n%s", encoded)
	}
}

func TestAgentPackCompositionEncodesNullPackAndEmptySources(t *testing.T) {
	event := v1.AgentPackCompositionEvent{Layers: []v1.AgentPackCompositionLayer{{Layer: v1.AgentPackCompositionLayerPlatformSafety, Status: v1.AgentPackCompositionApplied, Sources: []v1.AgentPackCompositionSource{}}}, Conflicts: []v1.AgentPackCompositionConflict{}}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"agent_pack":null`, `"sources":[]`, `"conflicts":[]`} {
		if !bytes.Contains(encoded, []byte(fragment)) {
			t.Fatalf("encoded event lacks %s: %s", fragment, encoded)
		}
	}
	if bytes.Contains(encoded, []byte("agent_run_authority")) {
		t.Fatalf("absent authority was encoded: %s", encoded)
	}
}

func equalJSON(t *testing.T, left, right []byte) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(left, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(right, &b); err != nil {
		t.Fatal(err)
	}
	ae, _ := json.Marshal(a)
	be, _ := json.Marshal(b)
	return bytes.Equal(ae, be)
}
