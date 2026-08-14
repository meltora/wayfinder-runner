package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLeaseRenewalEventJSONIsStrictManifestBoundAuthority(t *testing.T) {
	t.Parallel()
	event := LeaseRenewalEvent{
		Kind: "lease_renewal", ManifestKind: JobKindBuild, LeaseID: "lease-1",
		JobID: "job-1", AttemptID: "attempt-1", ManifestID: "manifest-1",
		ManifestDigest: strings.Repeat("a", 64), FencingToken: 7,
		ObservedAt: time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC),
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
		"manifest_digest", "fencing_token", "observed_at",
	} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("strict field %s missing from %s", field, raw)
		}
	}
	if len(fields) != 9 {
		t.Fatalf("renewal contains non-contract fields: %s", raw)
	}
}
