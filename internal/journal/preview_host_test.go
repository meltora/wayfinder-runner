package journal

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
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

func trustedCommitSetDigest(commits []v1.PreviewObservationCommit) string {
	sorted := append([]v1.PreviewObservationCommit(nil), commits...)
	slices.SortFunc(sorted, func(left, right v1.PreviewObservationCommit) int {
		return strings.Compare(left.RepositoryID, right.RepositoryID)
	})
	return v1.PreviewLifecycleCommitSetDigest(sorted)
}

func trustedCommitSetRecorded(t *testing.T, j *Journal, manifestDigest string, commits []v1.PreviewObservationCommit) bool {
	t.Helper()
	recorded, err := j.TrustedCommitSetRecorded(context.Background(), "manifest-1", manifestDigest, trustedCommitSetDigest(commits), commits)
	if err != nil {
		t.Fatal(err)
	}
	return recorded
}

func TestTrustedCommitSetLedgerAppendsSetsPerManifest(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	digest := strings.Repeat("c", 64)
	first := testTrustedCommits()
	if trustedCommitSetRecorded(t, j, digest, first) {
		t.Fatal("empty ledger trusted a set")
	}
	if err := j.RecordTrustedCommitSet(ctx, "manifest-1", digest, first, time.Now()); err != nil {
		t.Fatal(err)
	}
	second := []v1.PreviewObservationCommit{
		{RepositoryID: "api", CommitSHA: strings.Repeat("3", 40)},
		{RepositoryID: "web", CommitSHA: strings.Repeat("4", 40)},
	}
	if err := j.RecordTrustedCommitSet(ctx, "manifest-1", digest, second, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordTrustedCommitSet(ctx, "manifest-1", digest, first, time.Now()); err != nil {
		t.Fatalf("re-recording a set: %v", err)
	}
	if !trustedCommitSetRecorded(t, j, digest, first) || !trustedCommitSetRecorded(t, j, digest, second) {
		t.Fatal("ledger lost a recorded set")
	}
	mixed := []v1.PreviewObservationCommit{first[1], second[1]}
	if trustedCommitSetRecorded(t, j, digest, mixed) {
		t.Fatal("ledger trusted a set mixing two recorded sets")
	}
	if trustedCommitSetRecorded(t, j, digest, first[:1]) {
		t.Fatal("ledger trusted a subset of a recorded set")
	}
	if trustedCommitSetRecorded(t, j, strings.Repeat("d", 64), first) {
		t.Fatal("ledger matched a different manifest digest")
	}
	recorded, err := j.TrustedCommitSetRecorded(ctx, "manifest-1", digest, trustedCommitSetDigest(second), first)
	if err != nil || recorded {
		t.Fatalf("set trusted under another set's digest recorded=%v err=%v", recorded, err)
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
		ExecutionManifestID: "manifest-1", ExecutionManifestDigest: strings.Repeat("c", 64),
		CommitSetDigest: strings.Repeat("e", 64), OriginalExpiresAt: time.Date(2030, 8, 14, 18, 0, 0, 123_000_000, time.UTC),
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
	if err != nil || !found || byKey.State != PreviewHostDeployed || string(byKey.Result) != `{"kind":"preview_deploy_result"}` || byKey.MemoryMB != 1024 ||
		byKey.ExecutionManifestID != host.ExecutionManifestID || byKey.ExecutionManifestDigest != host.ExecutionManifestDigest ||
		byKey.CommitSetDigest != host.CommitSetDigest || !byKey.OriginalExpiresAt.Equal(host.OriginalExpiresAt) {
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
		"manifest id":     func(h *PreviewHost) { h.ExecutionManifestID = "" },
		"manifest digest": func(h *PreviewHost) { h.ExecutionManifestDigest = "c" },
		"commit set":      func(h *PreviewHost) { h.CommitSetDigest = "" },
		"original expiry": func(h *PreviewHost) { h.OriginalExpiresAt = time.Time{} },
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

func testHostedLifecycleResult(key, operationDigest string) PreviewHostedLifecycleResult {
	return PreviewHostedLifecycleResult{
		IdempotencyKey: key, OperationDigest: operationDigest,
		EventID: "preview-lifecycle-result-" + strings.Repeat("f", 64), Result: []byte(`{"kind":"preview_lifecycle_result"}`),
	}
}

func TestPreviewHostedLifecycleResultIsDurableAndAppliesHostUpdateOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	host := testPreviewHost("preview-1", "preview-deploy-"+strings.Repeat("1", 64))
	if _, _, err := j.InsertPreviewHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	key := "preview-lifecycle-" + strings.Repeat("d", 64)
	if _, found, err := j.PreviewHostedLifecycleResultByKey(ctx, key); err != nil || found {
		t.Fatalf("empty found=%v err=%v", found, err)
	}
	extended := host.ExpiresAt.Add(time.Hour)
	record := testHostedLifecycleResult(key, strings.Repeat("a", 64))
	stored, inserted, err := j.RecordPreviewHostedLifecycleResult(ctx, record, &PreviewHostUpdate{
		IdempotencyKey: host.IdempotencyKey, State: PreviewHostDeployed, ExpiresAt: &extended,
	})
	if err != nil || !inserted || stored.OperationDigest != record.OperationDigest {
		t.Fatalf("stored=%+v inserted=%v err=%v", stored, inserted, err)
	}
	later := extended.Add(time.Hour)
	other := testHostedLifecycleResult(key, strings.Repeat("b", 64))
	again, inserted, err := j.RecordPreviewHostedLifecycleResult(ctx, other, &PreviewHostUpdate{
		IdempotencyKey: host.IdempotencyKey, State: PreviewHostDeployed, ExpiresAt: &later,
	})
	if err != nil || inserted || again.OperationDigest != record.OperationDigest {
		t.Fatalf("second record again=%+v inserted=%v err=%v", again, inserted, err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	loaded, found, err := j.PreviewHostedLifecycleResultByKey(ctx, key)
	if err != nil || !found || loaded.EventID != record.EventID || string(loaded.Result) != string(record.Result) ||
		loaded.OperationDigest != record.OperationDigest {
		t.Fatalf("loaded=%+v found=%v err=%v", loaded, found, err)
	}
	current, _, _ := j.PreviewHostByIdempotencyKey(ctx, host.IdempotencyKey)
	if !current.ExpiresAt.Equal(extended) {
		t.Fatalf("expires_at=%s want %s", current.ExpiresAt, extended)
	}
}

func TestPreviewHostedLifecycleResultRefusesInvalidRecords(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	mutations := map[string]func(*PreviewHostedLifecycleResult){
		"key":      func(r *PreviewHostedLifecycleResult) { r.IdempotencyKey = "" },
		"digest":   func(r *PreviewHostedLifecycleResult) { r.OperationDigest = "a" },
		"event id": func(r *PreviewHostedLifecycleResult) { r.EventID = "" },
		"result":   func(r *PreviewHostedLifecycleResult) { r.Result = nil },
	}
	for name, mutate := range mutations {
		record := testHostedLifecycleResult("preview-lifecycle-"+strings.Repeat("d", 64), strings.Repeat("a", 64))
		mutate(&record)
		if _, _, err := j.RecordPreviewHostedLifecycleResult(ctx, record, nil); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	record := testHostedLifecycleResult("preview-lifecycle-"+strings.Repeat("d", 64), strings.Repeat("a", 64))
	missing := PreviewHostUpdate{IdempotencyKey: "preview-deploy-" + strings.Repeat("9", 64), State: PreviewHostDestroyed}
	if _, _, err := j.RecordPreviewHostedLifecycleResult(ctx, record, &missing); !errors.Is(err, ErrPreviewHostNotFound) {
		t.Fatalf("missing host err=%v", err)
	}
	if _, found, _ := j.PreviewHostedLifecycleResultByKey(ctx, record.IdempotencyKey); found {
		t.Fatal("record kept after its host update failed")
	}
}
