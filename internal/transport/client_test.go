package transport

import (
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

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
		got := jitterBackoff(base)
		if got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("jitterBackoff(%s) = %s", base, got)
		}
	}
}
