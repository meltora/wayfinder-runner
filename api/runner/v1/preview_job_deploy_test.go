package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPreviewDeployRequestIDVectors(t *testing.T) {
	cases := []struct {
		sequence       uint64
		requestID      string
		eventID        string
		idempotencyKey string
	}{
		{1, "preview-deploy-request-83dd81540c70d6ce4ee2db94e4545a51d6b2b75455a956bb94968e8273b98e77", "preview-deploy-requested-661697229b9faa39988ccf80b827376202fa1dd8001a12ab33839c04ad1ac9d5", "preview-deploy-5acc3a9c760806dda42b1a4c83fc0099c85e126c2f61b49225c9db42833c6654"},
		{2, "preview-deploy-request-3d38ecad015152ee723a4454c5ff3c32bd9c4a6e6b15baa9c25df3de98f5a2d9", "preview-deploy-requested-ac102e93ed962ab102fcc995e0aee7d6e20ef9f63b54068e6228eadda9044b01", "preview-deploy-2c3d00b3fa1e705f88e81296b023f9778740beac4b453ed780bc60f79f698a11"},
		{6, "preview-deploy-request-3a2d43f98b82d6e2cd9f2b080bb5077a47d7fb5e4ebda3b57ee5878be16430b3", "preview-deploy-requested-995a5bd700452bb04645f90557a28dd3effe9412a550ace99b4d9ff1a44d9c76", "preview-deploy-864a868c17ec0dccea05d24d28455d3a2a684e61d3a63094b805a01840b49de1"},
	}
	for _, tc := range cases {
		requestID := PreviewDeployRequestID("job-p5f-1", "lease-p5f-1", tc.sequence)
		if requestID != tc.requestID {
			t.Fatalf("sequence %d request_id = %s", tc.sequence, requestID)
		}
		if got := PreviewDeployRequestedEventID(requestID); got != tc.eventID {
			t.Fatalf("sequence %d event_id = %s", tc.sequence, got)
		}
		if got := PreviewJobDeployIdempotencyKey(requestID); got != tc.idempotencyKey {
			t.Fatalf("sequence %d idempotency_key = %s", tc.sequence, got)
		}
		if len(requestID) != 87 || len(tc.eventID) != 89 || len(tc.idempotencyKey) != 79 {
			t.Fatalf("sequence %d id lengths = %d %d %d", tc.sequence, len(requestID), len(tc.eventID), len(tc.idempotencyKey))
		}
	}
}

func TestPreviewDeployRequestIDMatchesSpecBytes(t *testing.T) {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", "job-p5f-1", "lease-p5f-1", 12)))
	if got, want := PreviewDeployRequestID("job-p5f-1", "lease-p5f-1", 12), "preview-deploy-request-"+hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("request_id = %s, want %s", got, want)
	}
}

func TestPreviewCommitSetDigestVectors(t *testing.T) {
	two := []PreviewObservationCommit{{RepositoryID: "repo-a", CommitSHA: strings.Repeat("a", 40)}, {RepositoryID: "repo-b", CommitSHA: strings.Repeat("b", 40)}}
	if got := PreviewLifecycleCommitSetDigest(two); got != "f71b0c108391637c3339a88f666a2528d14cff81ab3015c0e1d3dc39b9febb58" {
		t.Fatalf("two-repository digest = %s", got)
	}
	if got := PreviewLifecycleCommitSetDigest(two[:1]); got != "4cd0caabb02a04e7dc4e319408f4daa8aef8bb9f5638b24222bd852789560454" {
		t.Fatalf("one-repository digest = %s", got)
	}
}

func TestPreviewCommitSetFromMapSortsAndRejects(t *testing.T) {
	commits, err := PreviewCommitSetFromMap(map[string]string{"repo-c": strings.Repeat("c", 40), "repo-a": strings.Repeat("a", 40), "repo-b": strings.Repeat("b", 40)})
	if err != nil {
		t.Fatal(err)
	}
	want := []PreviewObservationCommit{
		{RepositoryID: "repo-a", CommitSHA: strings.Repeat("a", 40)},
		{RepositoryID: "repo-b", CommitSHA: strings.Repeat("b", 40)},
		{RepositoryID: "repo-c", CommitSHA: strings.Repeat("c", 40)},
	}
	if len(commits) != len(want) {
		t.Fatalf("commits = %v", commits)
	}
	for index := range want {
		if commits[index] != want[index] {
			t.Fatalf("commits = %v", commits)
		}
	}
	seventeen := map[string]string{}
	for index := 0; index < 17; index++ {
		seventeen[fmt.Sprintf("repo-%02d", index)] = strings.Repeat("a", 40)
	}
	sixteen := map[string]string{}
	for index := 0; index < 16; index++ {
		sixteen[fmt.Sprintf("repo-%02d", index)] = strings.Repeat("a", 40)
	}
	if _, err := PreviewCommitSetFromMap(sixteen); err != nil {
		t.Fatalf("16 entries: %v", err)
	}
	for name, set := range map[string]map[string]string{
		"empty":         {},
		"nil":           nil,
		"seventeen":     seventeen,
		"empty repo id": {"": strings.Repeat("a", 40)},
		"short sha":     {"repo-a": strings.Repeat("a", 39)},
		"uppercase sha": {"repo-a": strings.Repeat("A", 40)},
		"sha256 length": {"repo-a": strings.Repeat("a", 64)},
		"non-hex sha":   {"repo-a": strings.Repeat("g", 40)},
	} {
		if _, err := PreviewCommitSetFromMap(set); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestPreviewJobDeployRefusalCode(t *testing.T) {
	for _, code := range []string{
		"not_build_job", "no_approved_blueprint", "commit_set_mismatch", "request_in_flight",
		"deploy_limit_reached", "runner_capability_missing", "emergency_stop_active",
		"lease_not_found", "lease_not_active", "lease_expired", "delivery_conflict", "decision_expired",
	} {
		reply, final := PreviewJobDeployRefusalCode(code)
		if reply != code || !final {
			t.Fatalf("%s = (%q, %v)", code, reply, final)
		}
	}
	if reply, final := PreviewJobDeployRefusalCode("timestamp_out_of_window"); reply != "" || final {
		t.Fatalf("timestamp_out_of_window = (%q, %v)", reply, final)
	}
	if reply, final := PreviewJobDeployRefusalCode("unknown_x"); reply != "rejected" || !final {
		t.Fatalf("unknown_x = (%q, %v)", reply, final)
	}
}

func TestFormatPreviewJobDeployTime(t *testing.T) {
	at := time.Date(2030, 8, 14, 13, 45, 6, 789_123_456, time.FixedZone("plus1", 3600))
	if got := FormatPreviewJobDeployTime(at); got != "2030-08-14T12:45:06.789Z" {
		t.Fatalf("formatted = %s", got)
	}
	if got := FormatPreviewJobDeployTime(time.Date(2030, 8, 14, 12, 45, 6, 0, time.UTC)); got != "2030-08-14T12:45:06.000Z" {
		t.Fatalf("formatted = %s", got)
	}
}

func TestPreviewDeployRequestedEventWireFields(t *testing.T) {
	raw, err := json.Marshal(PreviewDeployRequestedEvent{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"schema_version", "job_id", "lease_id", "fencing_token", "request_id", "commit_set", "commit_set_digest", "requested_at"}
	if len(fields) != len(want) {
		t.Fatalf("fields = %v", fields)
	}
	for _, name := range want {
		if _, ok := fields[name]; !ok {
			t.Fatalf("missing %s in %v", name, fields)
		}
	}
}
