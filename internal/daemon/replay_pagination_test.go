package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/journal"
)

func appendReplayEvent(t *testing.T, jr *jobRunner, id string) {
	t.Helper()
	envelope := v1.Envelope{
		Version: v1.ProtocolVersion,
		Type:    v1.TypeEvent,
		ID:      id,
		SentAt:  time.Now().UTC(),
		Payload: map[string]any{"kind": "progress"},
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := jr.d.journal.Append(context.Background(), journal.Record{
		ID: envelope.ID, Kind: journal.KindEvent, Payload: raw, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReplayDoesNotSkipRowsAcknowledgedDuringPagination(t *testing.T) {
	jr, _ := testJobRunner(t)
	ctx := context.Background()
	for index := 0; index < 101; index++ {
		appendReplayEvent(t, jr, fmt.Sprintf("replay-%03d", index))
	}

	delivered := make([]string, 0, 101)
	err := jr.d.replayUnacked(ctx, func(envelope v1.Envelope) error {
		delivered = append(delivered, envelope.ID)
		if len(delivered) == 1 {
			appendReplayEvent(t, jr, "replay-appended-during-pass")
		}
		return jr.d.journal.AckKind(ctx, envelope.ID, journal.KindEvent)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 101 {
		t.Fatalf("delivered %d events, want 101; last=%v", len(delivered), delivered[len(delivered)-1:])
	}
	for index, id := range delivered {
		want := fmt.Sprintf("replay-%03d", index)
		if id != want {
			t.Fatalf("delivery %d = %q, want %q", index, id, want)
		}
	}
	remaining, err := jr.d.journal.Unacked(ctx, journal.KindEvent, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].ID != "replay-appended-during-pass" {
		t.Fatalf("next-pass events = %+v", remaining)
	}
}

func TestReplaySendFailureLeavesFailedAndLaterEventsForReconnect(t *testing.T) {
	jr, _ := testJobRunner(t)
	ctx := context.Background()
	for index := 0; index < 3; index++ {
		appendReplayEvent(t, jr, fmt.Sprintf("failure-%d", index))
	}
	wantErr := errors.New("connection lost")
	firstPass := make([]string, 0, 2)
	err := jr.d.replayUnacked(ctx, func(envelope v1.Envelope) error {
		firstPass = append(firstPass, envelope.ID)
		if envelope.ID == "failure-1" {
			return wantErr
		}
		return jr.d.journal.AckKind(ctx, envelope.ID, journal.KindEvent)
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("first replay error = %v, want %v", err, wantErr)
	}
	if fmt.Sprint(firstPass) != "[failure-0 failure-1]" {
		t.Fatalf("first pass = %v", firstPass)
	}

	secondPass := make([]string, 0, 2)
	if err := jr.d.replayUnacked(ctx, func(envelope v1.Envelope) error {
		secondPass = append(secondPass, envelope.ID)
		return jr.d.journal.AckKind(ctx, envelope.ID, journal.KindEvent)
	}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(secondPass) != "[failure-1 failure-2]" {
		t.Fatalf("second pass = %v", secondPass)
	}
}

func TestReplaySendsJournaledEnvelopeByteForByte(t *testing.T) {
	jr, _ := testJobRunner(t)
	ctx := context.Background()
	type replayPayload struct {
		Kind              string               `json:"kind"`
		FencingToken      uint64               `json:"fencing_token"`
		AgentRunAuthority v1.AgentRunAuthority `json:"agent_run_authority"`
		Note              string               `json:"note"`
	}
	sentAt := time.Date(2026, 11, 25, 10, 11, 12, 123456789, time.FixedZone("test", 3600))
	first := v1.Envelope{
		Version:  v1.ProtocolVersion,
		Type:     v1.TypeEvent,
		ID:       "replay-byte-identity",
		SentAt:   sentAt,
		RunnerID: "runner-1",
		Payload: replayPayload{
			Kind:         "progress",
			FencingToken: 9007199254740993,
			AgentRunAuthority: v1.AgentRunAuthority{
				SchemaVersion: 1, AgentRunID: "run-1", StepRunID: "step-1", StepSequence: 18446744073709551615,
			},
			Note: "<a&b>",
		},
	}
	firstWire, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := jr.d.journal.Append(ctx, journal.Record{
		ID: first.ID, Kind: journal.KindEvent, Payload: firstWire, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	var replayed []v1.Envelope
	if err := jr.d.replayUnacked(ctx, func(envelope v1.Envelope) error {
		replayed = append(replayed, envelope)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 1 {
		t.Fatalf("replayed %d envelopes", len(replayed))
	}
	replayWire, err := json.Marshal(replayed[0])
	if err != nil {
		t.Fatal(err)
	}
	type wirePayload struct {
		ID      string          `json:"id"`
		SentAt  json.RawMessage `json:"sent_at"`
		Payload json.RawMessage `json:"payload"`
	}
	var want, got wirePayload
	if err := json.Unmarshal(firstWire, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(replayWire, &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Payload) != string(want.Payload) {
		t.Fatalf("replayed payload bytes differ:\n got %s\nwant %s", got.Payload, want.Payload)
	}
	if got.ID != want.ID || string(got.SentAt) != string(want.SentAt) {
		t.Fatalf("replayed id/sent_at = %s/%s, want %s/%s", got.ID, got.SentAt, want.ID, want.SentAt)
	}
	if string(replayWire) != string(firstWire) {
		t.Fatalf("replayed envelope bytes differ:\n got %s\nwant %s", replayWire, firstWire)
	}
}

func TestReplayAdvancesPastCorruptDurableRows(t *testing.T) {
	jr, _ := testJobRunner(t)
	ctx := context.Background()
	if err := jr.d.journal.Append(ctx, journal.Record{
		ID: "corrupt", Kind: journal.KindEvent, Payload: []byte("not-json"), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	appendReplayEvent(t, jr, "valid-after-corrupt")

	delivered := make([]string, 0, 1)
	if err := jr.d.replayUnacked(ctx, func(envelope v1.Envelope) error {
		delivered = append(delivered, envelope.ID)
		return jr.d.journal.AckKind(ctx, envelope.ID, journal.KindEvent)
	}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(delivered) != "[valid-after-corrupt]" {
		t.Fatalf("delivered = %v", delivered)
	}
	remaining, err := jr.d.journal.Unacked(ctx, journal.KindEvent, 10)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining = %+v, %v", remaining, err)
	}
}
