package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnershipAcceptsThePerJobNPMCache(t *testing.T) {
	layout := newOwnershipLayout(t)
	cache := filepath.Join(layout.state, "job-results", "job-123.npm-cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	var calls []recordedHelper
	ownership := recordingOwnership(layout, &calls)
	request := OwnershipRequest{
		RunnerID: "runner-1", AttemptID: "attempt-1", Purpose: "install",
		Workspace: []string{layout.checks}, Private: []string{layout.checkHome, cache},
	}
	if err := ownership.HandOff(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := ownership.Reclaim(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !strings.HasSuffix(strings.Join(calls[1].args, " "), layout.checks+" "+layout.checkHome+" "+cache) {
		t.Fatalf("ownership calls = %q", calls)
	}
}

func TestOwnershipRefusesOtherJobResultSiblings(t *testing.T) {
	layout := newOwnershipLayout(t)
	for _, name := range []string{"job-123.npm-cache-x", "job-123.cache", "job-123.npm-cache.checks.x", "other.npm-cache"} {
		root := filepath.Join(layout.state, "job-results", name)
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		var calls []recordedHelper
		ownership := recordingOwnership(layout, &calls)
		err := ownership.HandOff(context.Background(), OwnershipRequest{
			RunnerID: "runner-1", AttemptID: "attempt-1", Purpose: "install", Private: []string{root},
		})
		if err == nil || !strings.Contains(err.Error(), "per-job") || len(calls) != 0 {
			t.Fatalf("%s: err = %v, calls = %d", name, err, len(calls))
		}
	}
}
