package validation

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validPreviewDeployRequestedEvent() v1.PreviewDeployRequestedEvent {
	commits := []v1.PreviewObservationCommit{
		{RepositoryID: "repo-a", CommitSHA: strings.Repeat("a", 40)},
		{RepositoryID: "repo-b", CommitSHA: strings.Repeat("b", 40)},
	}
	return v1.PreviewDeployRequestedEvent{
		SchemaVersion:   v1.PreviewDeployRequestedSchemaVersion,
		JobID:           "job-p5f-1",
		LeaseID:         "lease-p5f-1",
		FencingToken:    7,
		RequestID:       v1.PreviewDeployRequestID("job-p5f-1", "lease-p5f-1", 1),
		CommitSet:       commits,
		CommitSetDigest: v1.PreviewLifecycleCommitSetDigest(commits),
		RequestedAt:     "2030-08-14T12:45:06.789Z",
	}
}

func TestValidatePreviewDeployRequestedEvent(t *testing.T) {
	if err := PreviewDeployRequestedEvent(validPreviewDeployRequestedEvent()); err != nil {
		t.Fatalf("valid event: %v", err)
	}
	cases := map[string]func(*v1.PreviewDeployRequestedEvent){
		"schema 2":       func(e *v1.PreviewDeployRequestedEvent) { e.SchemaVersion = 2 },
		"empty job_id":   func(e *v1.PreviewDeployRequestedEvent) { e.JobID = "" },
		"bad lease_id":   func(e *v1.PreviewDeployRequestedEvent) { e.LeaseID = "lease/1" },
		"zero fencing":   func(e *v1.PreviewDeployRequestedEvent) { e.FencingToken = 0 },
		"unsafe fencing": func(e *v1.PreviewDeployRequestedEvent) { e.FencingToken = 1 << 53 },
		"bad request_id prefix": func(e *v1.PreviewDeployRequestedEvent) {
			e.RequestID = "preview-deploy-" + strings.Repeat("a", 64)
		},
		"uppercase request_id": func(e *v1.PreviewDeployRequestedEvent) {
			e.RequestID = "preview-deploy-request-" + strings.Repeat("A", 64)
		},
		"unsorted commit set": func(e *v1.PreviewDeployRequestedEvent) {
			e.CommitSet = []v1.PreviewObservationCommit{e.CommitSet[1], e.CommitSet[0]}
			e.CommitSetDigest = v1.PreviewLifecycleCommitSetDigest(e.CommitSet)
		},
		"duplicate repository": func(e *v1.PreviewDeployRequestedEvent) {
			e.CommitSet = []v1.PreviewObservationCommit{e.CommitSet[0], e.CommitSet[0]}
			e.CommitSetDigest = v1.PreviewLifecycleCommitSetDigest(e.CommitSet)
		},
		"empty commit set": func(e *v1.PreviewDeployRequestedEvent) {
			e.CommitSet = nil
			e.CommitSetDigest = v1.PreviewLifecycleCommitSetDigest(e.CommitSet)
		},
		"64 hex commit sha": func(e *v1.PreviewDeployRequestedEvent) {
			e.CommitSet = []v1.PreviewObservationCommit{{RepositoryID: "repo-a", CommitSHA: strings.Repeat("a", 64)}}
			e.CommitSetDigest = v1.PreviewLifecycleCommitSetDigest(e.CommitSet)
		},
		"digest mismatch": func(e *v1.PreviewDeployRequestedEvent) {
			e.CommitSetDigest = v1.PreviewLifecycleCommitSetDigest(e.CommitSet[:1])
		},
		"malformed digest":         func(e *v1.PreviewDeployRequestedEvent) { e.CommitSetDigest = "abc" },
		"requested_at nanoseconds": func(e *v1.PreviewDeployRequestedEvent) { e.RequestedAt = "2030-08-14T12:45:06.789123456Z" },
		"requested_at offset":      func(e *v1.PreviewDeployRequestedEvent) { e.RequestedAt = "2030-08-14T13:45:06.789+01:00" },
		"requested_at no fraction": func(e *v1.PreviewDeployRequestedEvent) { e.RequestedAt = "2030-08-14T12:45:06Z" },
		"requested_at empty":       func(e *v1.PreviewDeployRequestedEvent) { e.RequestedAt = "" },
	}
	for name, mutate := range cases {
		event := validPreviewDeployRequestedEvent()
		mutate(&event)
		if err := PreviewDeployRequestedEvent(event); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestPreviewDeployRequestedEventSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("../../api/runner/v1/testdata/preview_job_deploy_v1_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Events []struct {
			Name   string                         `json:"name"`
			Event  v1.PreviewDeployRequestedEvent `json:"event"`
			Expect string                         `json:"expect"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, vector := range file.Events {
		err := PreviewDeployRequestedEvent(vector.Event)
		switch vector.Expect {
		case "ok":
			if err != nil {
				t.Fatalf("%s: %v", vector.Name, err)
			}
		case "invalid":
			if err == nil {
				t.Fatalf("%s: expected error", vector.Name)
			}
		default:
			t.Fatalf("%s: expect=%q", vector.Name, vector.Expect)
		}
		seen[vector.Expect]++
	}
	if seen["ok"] == 0 || seen["invalid"] == 0 {
		t.Fatalf("event vectors = %v", seen)
	}
}
