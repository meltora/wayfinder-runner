package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestJobFinishedEventJSONIsTheStrictManifestBoundContract(t *testing.T) {
	t.Parallel()
	event := JobFinishedEvent{
		Kind: "job_finished", ManifestKind: JobKindBuild, LeaseID: "lease-1",
		JobID: "job-1", AttemptID: "attempt-1", ManifestID: "manifest-1",
		ManifestDigest: strings.Repeat("a", 64), FencingToken: 9,
		ObservedAt: time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC),
		State:      JobFinishedSucceeded, ProviderSessionID: "session-1",
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"kind", "manifest_kind", "lease_id", "job_id", "attempt_id", "manifest_id",
		"manifest_digest", "fencing_token", "observed_at", "state", "provider_session_id",
	} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("strict field %s missing from %s", field, raw)
		}
	}
	for _, forbidden := range []string{"workflow_revision", "branch", "head_sha", "commit_set", "pushed", "error"} {
		if _, ok := fields[forbidden]; ok {
			t.Fatalf("non-contract field %s present in %s", forbidden, raw)
		}
	}
}

func TestJobFinishedAgentPackFailureCarriesControlledRetryEvidence(t *testing.T) {
	t.Parallel()
	retryable := true
	event := JobFinishedEvent{
		Kind: "job_finished", ManifestKind: JobKindBuild, LeaseID: "lease-1",
		JobID: "job-1", AttemptID: "attempt-1", ManifestID: "manifest-1",
		ManifestDigest: strings.Repeat("a", 64), FencingToken: 9,
		ObservedAt: time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC),
		State:      JobFinishedFailed, FailureCode: JobFailureAgentPackMaterialization,
		FailureCategory: AgentPackFailureDownloadUnavailable, Retryable: &retryable,
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["failure_category"] != "download_unavailable" || fields["retryable"] != true {
		t.Fatalf("failure evidence = %v", fields)
	}
	for _, forbidden := range []string{"url", "path", "error", "archive", "key"} {
		if _, ok := fields[forbidden]; ok {
			t.Fatalf("unsafe field %q present in %s", forbidden, raw)
		}
	}
}
