package hookspool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func validEvent(eventType string) Event {
	return Event{
		Type: eventType, Timestamp: time.Now().UTC(), ManifestKind: "build",
		LeaseID: "lease-1", JobID: "job-1", AttemptID: "attempt-1",
		ManifestID: "manifest-1", ManifestDigest: strings.Repeat("a", 64),
		FencingToken: 7,
	}
}

func TestConcurrentSpoolInstancesCannotRacePastFileQuota(t *testing.T) {
	dir := t.TempDir()
	const writers = 32
	spools := make([]*Spool, writers)
	for i := range spools {
		spool, err := NewSpool(dir)
		if err != nil {
			t.Fatal(err)
		}
		spool.maxFiles = 1
		spools[i] = spool
	}
	start := make(chan struct{})
	var success atomic.Int32
	var group sync.WaitGroup
	for i := range spools {
		group.Add(1)
		go func(spool *Spool) {
			defer group.Done()
			<-start
			if spool.Append(validEvent("stop")) == nil {
				success.Add(1)
			}
		}(spools[i])
	}
	close(start)
	group.Wait()
	if success.Load() != 1 {
		t.Fatalf("successful concurrent appends=%d, want 1", success.Load())
	}
	_, files, err := spoolUsage(dir)
	if err != nil || files != 1 {
		t.Fatalf("spool files=%d err=%v", files, err)
	}
}

func TestAppendDrain(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(validEvent("session-start")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(validEvent("post-tool")); err != nil {
		t.Fatal(err)
	}
	evts, err := s.Drain(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evts) != 2 {
		t.Fatalf("events = %d, want 2", len(evts))
	}
	evts, err = s.Drain(10)
	if err != nil || len(evts) != 0 {
		t.Fatalf("spool should be empty after drain: %d %v", len(evts), err)
	}
}

func TestRedaction(t *testing.T) {
	redacted := Redact(map[string]any{
		"ANTHROPIC_API_KEY": "sk-ant-secret",
		"normal":            "value",
		"nested":            map[string]any{"authorization": "Bearer abcdefghijklmnop"},
		"message":           "request used Bearer abcdefghijklmnop",
	})
	if redacted["ANTHROPIC_API_KEY"] != "[redacted]" {
		t.Fatalf("not redacted: %v", redacted)
	}
	if redacted["normal"] != "value" {
		t.Fatalf("normal key mangled: %v", redacted)
	}
	if redacted["message"] != "[redacted]" || redacted["nested"].(map[string]any)["authorization"] != "[redacted]" {
		t.Fatalf("nested or value secret not redacted: %v", redacted)
	}
}

func TestProcessKeepsEventWhenDeliveryFails(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(validEvent("pre-tool")); err != nil {
		t.Fatal(err)
	}
	if err := s.Process(10, func(Event) error { return fmt.Errorf("offline") }); err == nil {
		t.Fatal("delivery failure was ignored")
	}
	events, err := s.Drain(10)
	if err != nil || len(events) != 1 {
		t.Fatalf("failed event was not retained: events=%v err=%v", events, err)
	}
}

func TestInvalidEventIsQuarantined(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "bad.json"), []byte(`{"type":"ok","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Process(10, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.Dir, "quarantine"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("quarantine entries=%v err=%v", entries, err)
	}
}

func TestAppendEnforcesQuota(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.maxBytes = 1
	if err := s.Append(validEvent("stop")); err == nil {
		t.Fatal("event exceeding quota was accepted")
	}
}

func TestRedactionBoundsNestedArraysAndLongValues(t *testing.T) {
	items := make([]any, 300)
	for i := range items {
		items[i] = "ordinary"
	}
	deep := any("leaf")
	for range 8 {
		deep = map[string]any{"nested": deep}
	}
	redacted := Redact(map[string]any{
		"items": items,
		"long":  strings.Repeat("x", 5000),
		"deep":  deep,
		"key":   "-----BEGIN PRIVATE KEY-----",
		"count": 7,
	})
	if len(redacted["items"].([]any)) != 256 {
		t.Fatal("array was not bounded")
	}
	if len(redacted["long"].(string)) > 4120 || !strings.Contains(redacted["long"].(string), "truncated") {
		t.Fatal("long string was not bounded")
	}
	if redacted["key"] != "[redacted]" || redacted["count"] != 7 {
		t.Fatalf("redaction=%v", redacted)
	}
}

func TestEventValidationAndDrainLimits(t *testing.T) {
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []Event{
		{},
		func() Event { event := validEvent("stop"); event.Type = "bad/type"; return event }(),
		func() Event { event := validEvent("stop"); event.JobID = "bad id"; return event }(),
		func() Event { event := validEvent("stop"); event.AttemptID = "bad/id"; return event }(),
		func() Event { event := validEvent("stop"); event.FencingToken = 0; return event }(),
	} {
		if err := spool.Append(event); err == nil {
			t.Fatalf("invalid event accepted: %+v", event)
		}
	}
	for _, limit := range []int{0, 1001} {
		if err := spool.Process(limit, func(Event) error { return nil }); err == nil {
			t.Fatalf("drain limit %d accepted", limit)
		}
	}
	spool.maxFiles = 0
	if err := spool.Append(validEvent("stop")); err == nil {
		t.Fatal("file-count quota not enforced")
	}
}

func TestProcessQuarantinesEmptyOversizedAndMultilineRecords(t *testing.T) {
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"empty.json":      {},
		"oversized.json":  []byte(strings.Repeat("x", maxEventBytes+1)),
		"multiline.jsonl": []byte(`{"type":"first"}` + "\n" + `{"type":"second"}` + "\n"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(spool.Dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := spool.Process(10, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(spool.Dir, "quarantine"))
	if err != nil || len(entries) != len(files) {
		t.Fatalf("quarantine=%v err=%v", entries, err)
	}
}

func TestSpoolUsageAndSanitizationIgnoreUnsafeEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "event.json"), []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".temporary"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	bytes, files, err := spoolUsage(dir)
	if err != nil || bytes != 4 || files != 1 {
		t.Fatalf("usage=%d/%d err=%v", bytes, files, err)
	}
	if sanitize("UPPER !") != "event" || sanitize("tool-use_1") != "tool-use_1" {
		t.Fatal("event type sanitization failed")
	}
}
