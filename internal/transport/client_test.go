package transport

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestSendEventRequiresTheExactPrebuiltEnvelope(t *testing.T) {
	t.Parallel()
	client := &Client{runnerID: "runner-1"}
	valid := v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeEvent, ID: "event-1",
		SentAt: time.Now(), RunnerID: "runner-1", Payload: map[string]any{"kind": "progress"},
	}
	if err := client.SendEvent(context.Background(), valid); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("valid prebuilt event = %v", err)
	}
	invalidCases := make([]v1.Envelope, 5)
	for index := range invalidCases {
		invalidCases[index] = valid
	}
	invalidCases[0].Type = v1.TypeHeartbeat
	invalidCases[1].ID = ""
	invalidCases[2].SentAt = time.Time{}
	invalidCases[3].RunnerID = "runner-2"
	invalidCases[4].InReplyTo = "command-1"
	for _, invalid := range invalidCases {
		if err := client.SendEvent(context.Background(), invalid); err == nil || strings.Contains(err.Error(), "not connected") {
			t.Fatalf("invalid event envelope reached transport: %#v: %v", invalid, err)
		}
	}
}

func TestDecodePayloadRejectsUnknownFields(t *testing.T) {
	envelope := v1.Envelope{Payload: map[string]any{"accepted": true, "unknown": true}}
	if _, err := decodePayload[v1.HelloAckPayload](envelope); err == nil {
		t.Fatal("unknown payload field was accepted")
	}
}

func TestJitterBackoffStaysWithinBounds(t *testing.T) {
	t.Parallel()
	base := 10 * time.Second
	for range 100 {
		got := JitterBackoff(base)
		if got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("JitterBackoff(%s) = %s", base, got)
		}
	}
}
