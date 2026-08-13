package v1

import (
	"encoding/json"
	"testing"
)

func TestProviderCapacityExhaustedEventJSONLineageContract(t *testing.T) {
	t.Parallel()
	event := ProviderCapacityExhaustedEvent{
		Kind:          "provider_capacity_exhausted",
		JobID:         "job-1",
		AttemptID:     "attempt-1",
		FencingToken:  7,
		Adapter:       "claude",
		Provider:      "anthropic",
		Reason:        "quota_exhausted",
		ResumePolicy:  CapacityResumeAutomatic,
		BranchRef:     "job/job-1/attempt-1",
		CommitSHA:     "0123456789abcdef0123456789abcdef01234567",
		CheckpointRef: "attempt-1",
		Message:       "capacity exhausted",
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"branch_ref": event.BranchRef, "commit_sha": event.CommitSHA, "checkpoint_ref": event.CheckpointRef,
	} {
		if payload[key] != want {
			t.Fatalf("%s = %#v, want %q; payload=%s", key, payload[key], want, raw)
		}
	}
}
