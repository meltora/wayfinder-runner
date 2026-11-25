package v1

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

const (
	previewJobDeployVectorsPath      = "testdata/preview_job_deploy_v1_vectors.json"
	previewJobDeployVectorsUpdateEnv = "PREVIEW_JOB_DEPLOY_VECTORS_UPDATE"
)

type PreviewJobDeployVectorFile struct {
	SchemaVersion int                               `json:"schema_version"`
	Generator     string                            `json:"generator"`
	Conventions   map[string]string                 `json:"conventions"`
	RequestIDs    []PreviewJobDeployRequestIDVector `json:"request_ids"`
	CommitSets    []PreviewJobDeployCommitSetVector `json:"commit_sets"`
	Events        []PreviewJobDeployEventVector     `json:"events"`
	RefusalCodes  []PreviewJobDeployRefusalVector   `json:"refusal_codes"`
}

type PreviewJobDeployRequestIDVector struct {
	JobID          string `json:"job_id"`
	LeaseID        string `json:"lease_id"`
	Sequence       uint64 `json:"sequence"`
	RequestID      string `json:"request_id"`
	EventID        string `json:"event_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type PreviewJobDeployCommitSetVector struct {
	Name            string                     `json:"name"`
	InputMap        map[string]string          `json:"input_map"`
	CommitSet       []PreviewObservationCommit `json:"commit_set"`
	CommitSetDigest string                     `json:"commit_set_digest"`
}

type PreviewJobDeployEventVector struct {
	Name   string                      `json:"name"`
	Rule   string                      `json:"rule,omitempty"`
	Event  PreviewDeployRequestedEvent `json:"event"`
	Expect string                      `json:"expect"`
}

type PreviewJobDeployRefusalVector struct {
	Code  string `json:"code"`
	Reply string `json:"reply"`
	Final bool   `json:"final"`
}

func previewJobDeployVectorCommitSet(set map[string]string) []PreviewObservationCommit {
	commits, err := PreviewCommitSetFromMap(set)
	if err != nil {
		panic(err)
	}
	return commits
}

func previewJobDeployValidVectorEvent() PreviewDeployRequestedEvent {
	commits := previewJobDeployVectorCommitSet(map[string]string{"repo-b": strings.Repeat("b", 40), "repo-a": strings.Repeat("a", 40)})
	return PreviewDeployRequestedEvent{
		SchemaVersion:   PreviewDeployRequestedSchemaVersion,
		JobID:           "job-p5f-1",
		LeaseID:         "lease-p5f-1",
		FencingToken:    7,
		RequestID:       PreviewDeployRequestID("job-p5f-1", "lease-p5f-1", 1),
		CommitSet:       commits,
		CommitSetDigest: PreviewLifecycleCommitSetDigest(commits),
		RequestedAt:     "2030-08-14T12:45:06.789Z",
	}
}

func previewJobDeployInvalidVectorEvent(name, rule string, mutate func(*PreviewDeployRequestedEvent)) PreviewJobDeployEventVector {
	event := previewJobDeployValidVectorEvent()
	mutate(&event)
	return PreviewJobDeployEventVector{Name: name, Rule: rule, Event: event, Expect: "invalid"}
}

func BuildPreviewJobDeployVectorFile() PreviewJobDeployVectorFile {
	file := PreviewJobDeployVectorFile{
		SchemaVersion: 1,
		Generator:     previewJobDeployVectorsUpdateEnv + "=1 go test ./api/runner/v1 -run TestPreviewJobDeployV1VectorsAreCurrent",
		Conventions: map[string]string{
			"hash":              "sha256 lowercase hex",
			"separator":         "\\u0000",
			"time":              "RFC3339 ms UTC",
			"envelope":          "events show only the preview_deploy_requested fields; the runner sends them inside the standard job event envelope (kind, manifest_kind, manifest_id, manifest_digest, attempt_id, observed_at), whose job_id, lease_id and fencing_token are the same values",
			"event_id":          "preview-deploy-requested- + hex(sha256(request_id))",
			"commit_set_digest": "the P5d commit set digest: hex(sha256(compact JSON array of {\"commit_sha\",\"repository_id\"} objects, keys in that order, ascending repository_id))",
		},
	}
	for _, sequence := range []uint64{1, 2, 6} {
		requestID := PreviewDeployRequestID("job-p5f-1", "lease-p5f-1", sequence)
		file.RequestIDs = append(file.RequestIDs, PreviewJobDeployRequestIDVector{
			JobID: "job-p5f-1", LeaseID: "lease-p5f-1", Sequence: sequence, RequestID: requestID,
			EventID: PreviewDeployRequestedEventID(requestID), IdempotencyKey: PreviewJobDeployIdempotencyKey(requestID),
		})
	}
	for _, set := range []struct {
		name  string
		input map[string]string
	}{
		{"two_repositories", map[string]string{"repo-b": strings.Repeat("b", 40), "repo-a": strings.Repeat("a", 40)}},
		{"one_repository", map[string]string{"repo-a": strings.Repeat("a", 40)}},
	} {
		commits := previewJobDeployVectorCommitSet(set.input)
		file.CommitSets = append(file.CommitSets, PreviewJobDeployCommitSetVector{
			Name: set.name, InputMap: set.input, CommitSet: commits, CommitSetDigest: PreviewLifecycleCommitSetDigest(commits),
		})
	}
	file.Events = []PreviewJobDeployEventVector{
		{Name: "requested_two_repositories", Event: previewJobDeployValidVectorEvent(), Expect: "ok"},
		{Name: "requested_one_repository_sequence_6", Event: func() PreviewDeployRequestedEvent {
			event := previewJobDeployValidVectorEvent()
			event.RequestID = PreviewDeployRequestID("job-p5f-1", "lease-p5f-1", 6)
			event.CommitSet = event.CommitSet[:1]
			event.CommitSetDigest = PreviewLifecycleCommitSetDigest(event.CommitSet)
			event.RequestedAt = "2030-08-14T12:45:06.000Z"
			return event
		}(), Expect: "ok"},
		previewJobDeployInvalidVectorEvent("schema_version_2", "schema_version must be 1", func(event *PreviewDeployRequestedEvent) {
			event.SchemaVersion = 2
		}),
		previewJobDeployInvalidVectorEvent("request_id_bad_prefix", "request_id must match preview-deploy-request-<64 hex>", func(event *PreviewDeployRequestedEvent) {
			event.RequestID = "preview-deploy-" + strings.Repeat("a", 64)
		}),
		previewJobDeployInvalidVectorEvent("fencing_token_zero", "fencing_token must be 1..2^53-1", func(event *PreviewDeployRequestedEvent) {
			event.FencingToken = 0
		}),
		previewJobDeployInvalidVectorEvent("commit_set_unsorted", "commit_set must be ascending by repository_id", func(event *PreviewDeployRequestedEvent) {
			event.CommitSet = []PreviewObservationCommit{event.CommitSet[1], event.CommitSet[0]}
			event.CommitSetDigest = PreviewLifecycleCommitSetDigest(event.CommitSet)
		}),
		previewJobDeployInvalidVectorEvent("commit_set_empty", "commit_set must have 1-16 entries", func(event *PreviewDeployRequestedEvent) {
			event.CommitSet = []PreviewObservationCommit{}
			event.CommitSetDigest = PreviewLifecycleCommitSetDigest(event.CommitSet)
		}),
		previewJobDeployInvalidVectorEvent("commit_sha_64_hex", "commit_sha must be 40 lowercase hex characters", func(event *PreviewDeployRequestedEvent) {
			event.CommitSet = []PreviewObservationCommit{{RepositoryID: "repo-a", CommitSHA: strings.Repeat("a", 64)}}
			event.CommitSetDigest = PreviewLifecycleCommitSetDigest(event.CommitSet)
		}),
		previewJobDeployInvalidVectorEvent("commit_set_digest_mismatch", "commit_set_digest must equal the digest of commit_set", func(event *PreviewDeployRequestedEvent) {
			event.CommitSetDigest = PreviewLifecycleCommitSetDigest(event.CommitSet[:1])
		}),
		previewJobDeployInvalidVectorEvent("requested_at_nanoseconds", "requested_at must carry exactly 3 fractional digits", func(event *PreviewDeployRequestedEvent) {
			event.RequestedAt = "2030-08-14T12:45:06.789123456Z"
		}),
		previewJobDeployInvalidVectorEvent("requested_at_offset", "requested_at must be UTC with a Z suffix", func(event *PreviewDeployRequestedEvent) {
			event.RequestedAt = "2030-08-14T13:45:06.789+01:00"
		}),
	}
	for _, code := range append(append([]string{}, previewJobDeployPassThroughRefusalCodes...), PreviewJobDeployRefusalTimestampOutOfWindow, "unknown_x") {
		reply, final := PreviewJobDeployRefusalCode(code)
		file.RefusalCodes = append(file.RefusalCodes, PreviewJobDeployRefusalVector{Code: code, Reply: reply, Final: final})
	}
	return file
}

func encodePreviewJobDeployVectorFile(t *testing.T, file PreviewJobDeployVectorFile) []byte {
	t.Helper()
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(file); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestPreviewJobDeployV1VectorsAreCurrent(t *testing.T) {
	want := encodePreviewJobDeployVectorFile(t, BuildPreviewJobDeployVectorFile())
	if os.Getenv(previewJobDeployVectorsUpdateEnv) == "1" {
		if err := os.WriteFile(previewJobDeployVectorsPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(previewJobDeployVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; regenerate with %s=1", previewJobDeployVectorsPath, previewJobDeployVectorsUpdateEnv)
	}
}

func TestPreviewJobDeployV1VectorsRecomputeFromTheWire(t *testing.T) {
	raw, err := os.ReadFile(previewJobDeployVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var file PreviewJobDeployVectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	pinned := map[uint64]string{
		1: "preview-deploy-request-83dd81540c70d6ce4ee2db94e4545a51d6b2b75455a956bb94968e8273b98e77",
		2: "preview-deploy-request-3d38ecad015152ee723a4454c5ff3c32bd9c4a6e6b15baa9c25df3de98f5a2d9",
		6: "preview-deploy-request-3a2d43f98b82d6e2cd9f2b080bb5077a47d7fb5e4ebda3b57ee5878be16430b3",
	}
	for _, vector := range file.RequestIDs {
		requestID := PreviewDeployRequestID(vector.JobID, vector.LeaseID, vector.Sequence)
		if requestID != vector.RequestID || requestID != pinned[vector.Sequence] ||
			PreviewDeployRequestedEventID(requestID) != vector.EventID || PreviewJobDeployIdempotencyKey(requestID) != vector.IdempotencyKey {
			t.Fatalf("request id vector %d does not recompute", vector.Sequence)
		}
		delete(pinned, vector.Sequence)
	}
	if len(pinned) != 0 {
		t.Fatalf("pinned request ids missing: %v", pinned)
	}
	digests := map[string]string{
		"two_repositories": "f71b0c108391637c3339a88f666a2528d14cff81ab3015c0e1d3dc39b9febb58",
		"one_repository":   "4cd0caabb02a04e7dc4e319408f4daa8aef8bb9f5638b24222bd852789560454",
	}
	for _, vector := range file.CommitSets {
		commits, err := PreviewCommitSetFromMap(vector.InputMap)
		if err != nil || !reflect.DeepEqual(commits, vector.CommitSet) ||
			PreviewLifecycleCommitSetDigest(commits) != vector.CommitSetDigest || vector.CommitSetDigest != digests[vector.Name] {
			t.Fatalf("commit set vector %s does not recompute", vector.Name)
		}
		delete(digests, vector.Name)
	}
	if len(digests) != 0 {
		t.Fatalf("pinned commit sets missing: %v", digests)
	}
	if len(file.RefusalCodes) != 14 {
		t.Fatalf("refusal codes = %d", len(file.RefusalCodes))
	}
	for _, vector := range file.RefusalCodes {
		if reply, final := PreviewJobDeployRefusalCode(vector.Code); reply != vector.Reply || final != vector.Final {
			t.Fatalf("refusal code %s does not recompute", vector.Code)
		}
	}
}
