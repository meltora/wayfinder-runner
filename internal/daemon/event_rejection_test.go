package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/mockcp"
)

func startRejectionDaemon(t *testing.T, cp *mockcp.Server, stateDir string) (context.CancelFunc, <-chan error) {
	t.Helper()
	d, err := daemon.New(directRunnerConfig(cp.WSURL(), stateDir), nil)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	return cancel, done
}

func stopRejectionDaemon(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("daemon did not stop")
	}
}

func sendGatewayError(t *testing.T, cp *mockcp.Server, inReplyTo, code string) {
	t.Helper()
	envelope := v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeError, ID: "srv-err-" + code + "-" + inReplyTo,
		SentAt: time.Now(), InReplyTo: inReplyTo,
		Payload: v1.ErrorPayload{Code: code, Message: "Runner event rejected"},
	}
	waitFor(t, "gateway error delivered", 10*time.Second, func() bool {
		return cp.SendCommandEnvelope(context.Background(), envelope) == nil
	})
}

func rejectionSummary(t *testing.T, path string) journal.RejectionSummary {
	t.Helper()
	summary, err := journal.ReadRejectionSummary(context.Background(), path, time.Now())
	if err != nil {
		t.Fatalf("ReadRejectionSummary: %v", err)
	}
	return summary
}

func TestGatewayRejectionIsRecordedWithoutAcknowledgingAndReplayedAfterRestart(t *testing.T) {
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := cp.Start(context.Background()); err != nil {
		t.Fatalf("cp.Start: %v", err)
	}
	defer cp.Close()
	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	journalPath := filepath.Join(stateDir, "journal.db")

	cancel, done := startRejectionDaemon(t, cp, stateDir)
	waitFor(t, "runner connected and replay finished", 10*time.Second, func() bool { return cp.Heartbeats() >= 1 })

	writer, err := journal.Open(journalPath)
	if err != nil {
		t.Fatalf("journal.Open: %v", err)
	}
	envelope := v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeEvent, ID: "evt-rejected-1",
		SentAt: time.Now().UTC().Truncate(time.Second), Payload: map[string]any{"kind": "rejection_test"},
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(context.Background(), journal.Record{
		ID: envelope.ID, Kind: journal.KindEvent, Payload: raw, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("journal.Append: %v", err)
	}
	original, _, err := writer.LoadRecord(context.Background(), envelope.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()

	for range 3 {
		sendGatewayError(t, cp, envelope.ID, "runner_event_rejected")
	}
	sendGatewayError(t, cp, "bad id with spaces", "runner_event_rejected")
	sendGatewayError(t, cp, "", "invalid_envelope")
	waitFor(t, "rejections recorded", 10*time.Second, func() bool {
		summary := rejectionSummary(t, journalPath)
		return summary.RejectedEvents == 1 && len(summary.SessionRejections) == 1
	})
	summary := rejectionSummary(t, journalPath)
	if summary.LastRejection == nil ||
		summary.LastRejection.Code != "runner_event_rejected" || summary.SessionRejections[0].Code != "invalid_envelope" {
		t.Fatalf("summary while connected = %+v", summary)
	}
	stopRejectionDaemon(t, cancel, done)

	before := len(cp.Events())
	cancel, done = startRejectionDaemon(t, cp, stateDir)
	defer stopRejectionDaemon(t, cancel, done)
	waitFor(t, "rejected event replayed", 10*time.Second, func() bool {
		for _, event := range cp.Events()[before:] {
			if event.ID == envelope.ID {
				return true
			}
		}
		return false
	})
	var replayed v1.Envelope
	for _, event := range cp.Events()[before:] {
		if event.ID == envelope.ID {
			replayed = event
		}
	}
	replayedRaw, err := json.Marshal(replayed)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(original.Payload, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(replayedRaw, &got); err != nil {
		t.Fatal(err)
	}
	wantRaw, _ := json.Marshal(want)
	gotRaw, _ := json.Marshal(got)
	if !bytes.Equal(wantRaw, gotRaw) {
		t.Fatalf("replayed envelope differs:\nwant %s\ngot  %s", wantRaw, gotRaw)
	}
	waitFor(t, "replayed event acknowledged", 10*time.Second, func() bool {
		summary := rejectionSummary(t, journalPath)
		return summary.UnacknowledgedEvents == 0 && summary.RejectedEvents == 0
	})
}

func TestOnEventRejectedValidatesEventID(t *testing.T) {
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := cp.Start(context.Background()); err != nil {
		t.Fatalf("cp.Start: %v", err)
	}
	defer cp.Close()
	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)
	d, err := daemon.New(directRunnerConfig(cp.WSURL(), stateDir), nil)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	ctx := context.Background()
	rejectErr := d.OnEventRejected(ctx, "bad id with spaces", "runner_event_rejected")
	unknownErr := d.OnEventRejected(ctx, "evt-unknown", "runner_event_rejected")
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(stopped) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("daemon did not stop")
	}
	if rejectErr == nil || !strings.Contains(rejectErr.Error(), "event_id must be") {
		t.Fatalf("invalid event ID error = %v", rejectErr)
	}
	if unknownErr != nil {
		t.Fatalf("unknown event ID error = %v", unknownErr)
	}
	summary := rejectionSummary(t, filepath.Join(stateDir, "journal.db"))
	if summary.RejectedEvents != 0 || summary.UnacknowledgedEvents != 0 {
		t.Fatalf("summary = %+v", summary)
	}
}
