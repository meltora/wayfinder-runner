package v1_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

type jobFinishedFixture struct {
	Version  int                 `json:"version"`
	Type     v1.MessageType      `json:"type"`
	ID       string              `json:"id"`
	SentAt   time.Time           `json:"sent_at"`
	RunnerID string              `json:"runner_id"`
	Payload  v1.JobFinishedEvent `json:"payload"`
}

func TestCanonicalJobFinishedProviderUsageFixtureStrictlyParsesAndValidates(t *testing.T) {
	var fixture jobFinishedFixture
	decodeStrictFixture(t, "testdata/job_finished_provider_usage_v1_event.json", &fixture)
	if err := validation.JobFinishedEvent(fixture.Payload); err != nil {
		t.Fatal(err)
	}
	usage := fixture.Payload.ProviderUsage
	if usage == nil || usage.Provider != "claude" || usage.Model != "claude-opus-5-5" ||
		usage.InputTokens != 1200 || usage.OutputTokens != 340 ||
		usage.CacheReadInputTokens == nil || *usage.CacheReadInputTokens != 9000 ||
		usage.CacheCreationInputTokens == nil || *usage.CacheCreationInputTokens != 150 {
		t.Fatalf("fixture provider usage = %+v", usage)
	}
}

func TestJobFinishedOmitsProviderUsageWhenNothingWasReported(t *testing.T) {
	event := v1.JobFinishedEvent{
		Kind: "job_finished", ManifestKind: v1.JobKindBuild, LeaseID: "lease-1",
		JobID: "job-1", AttemptID: "attempt-1", ManifestID: "manifest-1",
		ManifestDigest: strings.Repeat("a", 64), FencingToken: 9,
		ObservedAt: time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC),
		State:      v1.JobFinishedSucceeded,
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "provider_usage") {
		t.Fatalf("absent usage serialized: %s", raw)
	}
	event.ProviderUsage = &v1.ProviderUsage{Provider: "codex", InputTokens: 1, OutputTokens: 0}
	raw, err = json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"provider_usage":{"provider":"codex","input_tokens":1,"output_tokens":0}`) {
		t.Fatalf("usage wire shape = %s", raw)
	}
}
