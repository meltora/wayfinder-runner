package journal

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func testTrustedCommits() []v1.PreviewObservationCommit {
	return []v1.PreviewObservationCommit{
		{RepositoryID: "web", CommitSHA: strings.Repeat("2", 40)},
		{RepositoryID: "api", CommitSHA: strings.Repeat("1", 40)},
	}
}

func TestTrustedCommitSetRoundTripsSortedAndReplacesPerManifest(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	digest := strings.Repeat("c", 64)
	if _, found, err := j.TrustedCommitSet(ctx, "manifest-1", digest); err != nil || found {
		t.Fatalf("empty ledger found=%v err=%v", found, err)
	}
	if err := j.RecordTrustedCommitSet(ctx, "manifest-1", digest, testTrustedCommits(), time.Now()); err != nil {
		t.Fatal(err)
	}
	commits, found, err := j.TrustedCommitSet(ctx, "manifest-1", digest)
	if err != nil || !found || len(commits) != 2 || commits[0].RepositoryID != "api" || commits[1].RepositoryID != "web" {
		t.Fatalf("commits=%v found=%v err=%v", commits, found, err)
	}
	if _, found, _ := j.TrustedCommitSet(ctx, "manifest-1", strings.Repeat("d", 64)); found {
		t.Fatal("ledger matched a different manifest digest")
	}
	replacement := []v1.PreviewObservationCommit{{RepositoryID: "api", CommitSHA: strings.Repeat("3", 40)}}
	if err := j.RecordTrustedCommitSet(ctx, "manifest-1", digest, replacement, time.Now()); err != nil {
		t.Fatal(err)
	}
	commits, _, _ = j.TrustedCommitSet(ctx, "manifest-1", digest)
	if len(commits) != 1 || commits[0].CommitSHA != strings.Repeat("3", 40) {
		t.Fatalf("replacement commits=%v", commits)
	}
}

func TestTrustedCommitSetRefusesUnverifiableInput(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	digest := strings.Repeat("c", 64)
	cases := map[string]struct {
		manifestID, digest string
		commits            []v1.PreviewObservationCommit
	}{
		"empty":         {"manifest-1", digest, nil},
		"manifest":      {"", digest, testTrustedCommits()},
		"digest":        {"manifest-1", "c", testTrustedCommits()},
		"short sha":     {"manifest-1", digest, []v1.PreviewObservationCommit{{RepositoryID: "api", CommitSHA: "abc"}}},
		"duplicate":     {"manifest-1", digest, []v1.PreviewObservationCommit{{RepositoryID: "api", CommitSHA: strings.Repeat("1", 40)}, {RepositoryID: "api", CommitSHA: strings.Repeat("2", 40)}}},
		"repository id": {"manifest-1", digest, []v1.PreviewObservationCommit{{RepositoryID: "", CommitSHA: strings.Repeat("1", 40)}}},
	}
	for name, test := range cases {
		if err := j.RecordTrustedCommitSet(ctx, test.manifestID, test.digest, test.commits, time.Now()); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func testPreviewHost(previewID, key string) PreviewHost {
	return PreviewHost{
		PreviewID: previewID, PreviewKey: "abcdefghijklmnopqrstuvwxyz", IdempotencyKey: key,
		OperationDigest: strings.Repeat("a", 64), OrganizationID: "organization-1", ProjectID: "project-1",
		BlueprintID: "node-web", BlueprintVersion: "1", BlueprintDigest: strings.Repeat("b", 64),
		MemoryMB: 1024, ExpiresAt: time.Date(2030, 8, 14, 18, 0, 0, 123_000_000, time.UTC),
		State: PreviewHostDeploying,
	}
}

func TestPreviewHostRegistryLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	host := testPreviewHost("preview-1", "preview-deploy-"+strings.Repeat("1", 64))
	stored, inserted, err := j.InsertPreviewHost(ctx, host)
	if err != nil || !inserted || stored.State != PreviewHostDeploying || !stored.ExpiresAt.Equal(host.ExpiresAt) {
		t.Fatalf("stored=%+v inserted=%v err=%v", stored, inserted, err)
	}
	again, inserted, err := j.InsertPreviewHost(ctx, host)
	if err != nil || inserted || again.IdempotencyKey != host.IdempotencyKey {
		t.Fatalf("duplicate insert again=%+v inserted=%v err=%v", again, inserted, err)
	}
	competing := testPreviewHost("preview-1", "preview-deploy-"+strings.Repeat("2", 64))
	if _, _, err := j.InsertPreviewHost(ctx, competing); !errors.Is(err, ErrPreviewHostConflict) {
		t.Fatalf("competing live deploy err=%v", err)
	}
	live, err := j.LivePreviewHosts(ctx)
	if err != nil || len(live) != 1 {
		t.Fatalf("live=%v err=%v", live, err)
	}
	if err := j.UpdatePreviewHost(ctx, PreviewHostUpdate{
		IdempotencyKey: host.IdempotencyKey, State: PreviewHostDeployed, Result: []byte(`{"kind":"preview_deploy_result"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	byKey, found, err := j.PreviewHostByIdempotencyKey(ctx, host.IdempotencyKey)
	if err != nil || !found || byKey.State != PreviewHostDeployed || string(byKey.Result) != `{"kind":"preview_deploy_result"}` || byKey.MemoryMB != 1024 {
		t.Fatalf("byKey=%+v found=%v err=%v", byKey, found, err)
	}
	extended := host.ExpiresAt.Add(24 * time.Hour)
	if err := j.UpdatePreviewHost(ctx, PreviewHostUpdate{IdempotencyKey: host.IdempotencyKey, State: PreviewHostDeployed, ExpiresAt: &extended}); err != nil {
		t.Fatal(err)
	}
	byID, found, err := j.PreviewHostByPreviewID(ctx, "preview-1")
	if err != nil || !found || !byID.ExpiresAt.Equal(extended) || string(byID.Result) != `{"kind":"preview_deploy_result"}` {
		t.Fatalf("byID=%+v found=%v err=%v", byID, found, err)
	}
	if err := j.UpdatePreviewHost(ctx, PreviewHostUpdate{IdempotencyKey: host.IdempotencyKey, State: PreviewHostDestroyed}); err != nil {
		t.Fatal(err)
	}
	if live, _ := j.LivePreviewHosts(ctx); len(live) != 0 {
		t.Fatalf("destroyed host still live: %v", live)
	}
	if _, inserted, err := j.InsertPreviewHost(ctx, competing); err != nil || !inserted {
		t.Fatalf("redeploy after destroy inserted=%v err=%v", inserted, err)
	}
	latest, _, _ := j.PreviewHostByPreviewID(ctx, "preview-1")
	if latest.IdempotencyKey != competing.IdempotencyKey {
		t.Fatalf("latest=%s", latest.IdempotencyKey)
	}
	if _, found, _ := j.PreviewHostByPreviewID(ctx, "preview-unknown"); found {
		t.Fatal("unknown preview found")
	}
}

func TestPreviewHostRegistryRefusesInvalidRows(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	mutations := map[string]func(*PreviewHost){
		"state":           func(h *PreviewHost) { h.State = "running" },
		"preview id":      func(h *PreviewHost) { h.PreviewID = "" },
		"preview key":     func(h *PreviewHost) { h.PreviewKey = "ABC" },
		"operation":       func(h *PreviewHost) { h.OperationDigest = "a" },
		"memory":          func(h *PreviewHost) { h.MemoryMB = 0 },
		"expires":         func(h *PreviewHost) { h.ExpiresAt = time.Time{} },
		"idempotency key": func(h *PreviewHost) { h.IdempotencyKey = "" },
	}
	for name, mutate := range mutations {
		host := testPreviewHost("preview-1", "preview-deploy-"+strings.Repeat("1", 64))
		mutate(&host)
		if _, _, err := j.InsertPreviewHost(ctx, host); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if err := j.UpdatePreviewHost(ctx, PreviewHostUpdate{IdempotencyKey: "preview-deploy-" + strings.Repeat("9", 64), State: PreviewHostFailed}); !errors.Is(err, ErrPreviewHostNotFound) {
		t.Fatalf("update of missing host err=%v", err)
	}
	host := testPreviewHost("preview-1", "preview-deploy-"+strings.Repeat("1", 64))
	if _, _, err := j.InsertPreviewHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	if err := j.UpdatePreviewHost(ctx, PreviewHostUpdate{IdempotencyKey: host.IdempotencyKey, State: "running"}); err == nil {
		t.Fatal("invalid update state accepted")
	}
}
