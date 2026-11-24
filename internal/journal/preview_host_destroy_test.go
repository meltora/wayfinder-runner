package journal

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewHostDestroyReasonIsDurableAndKeptAcrossLaterUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	host := testPreviewHost("preview-1", "preview-deploy-"+strings.Repeat("1", 64))
	host.State = PreviewHostDeployed
	if _, _, err := j.InsertPreviewHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	if err := j.UpdatePreviewHost(ctx, PreviewHostUpdate{
		IdempotencyKey: host.IdempotencyKey, State: PreviewHostDestroying, DestroyReason: "emergency_stop",
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
	destroying, err := j.PreviewHostsInStates(ctx, PreviewHostDestroying)
	if err != nil || len(destroying) != 1 || destroying[0].DestroyReason != "emergency_stop" {
		t.Fatalf("destroying=%+v err=%v", destroying, err)
	}
	if err := j.UpdatePreviewHost(ctx, PreviewHostUpdate{IdempotencyKey: host.IdempotencyKey, State: PreviewHostDestroyed}); err != nil {
		t.Fatal(err)
	}
	stored, _, err := j.PreviewHostByIdempotencyKey(ctx, host.IdempotencyKey)
	if err != nil || stored.State != PreviewHostDestroyed || stored.DestroyReason != "emergency_stop" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestPreviewHostsInStatesFiltersAndOrders(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	states := []PreviewHostState{PreviewHostDeploying, PreviewHostDeployed, PreviewHostUncertain, PreviewHostDestroying}
	for index, state := range states {
		host := testPreviewHost("preview-"+string(rune('a'+index)), "preview-deploy-"+strings.Repeat(string(rune('1'+index)), 64))
		host.State = state
		if _, _, err := j.InsertPreviewHost(ctx, host); err != nil {
			t.Fatal(err)
		}
	}
	got, err := j.PreviewHostsInStates(ctx, PreviewHostDeployed, PreviewHostDestroying, PreviewHostDeploying)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].PreviewID != "preview-a" || got[1].PreviewID != "preview-b" || got[2].PreviewID != "preview-d" {
		t.Fatalf("got=%+v", got)
	}
	if none, err := j.PreviewHostsInStates(ctx); err != nil || len(none) != 0 {
		t.Fatalf("no states returned %v err=%v", none, err)
	}
	if _, err := j.PreviewHostsInStates(ctx, "running"); err == nil {
		t.Fatal("invalid state accepted")
	}
}

func TestPreviewHostRefusesInvalidDestroyReason(t *testing.T) {
	j := openTest(t)
	ctx := context.Background()
	host := testPreviewHost("preview-1", "preview-deploy-"+strings.Repeat("1", 64))
	if _, _, err := j.InsertPreviewHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	if err := j.UpdatePreviewHost(ctx, PreviewHostUpdate{
		IdempotencyKey: host.IdempotencyKey, State: PreviewHostDestroying, DestroyReason: "operator_whim",
	}); err == nil {
		t.Fatal("unknown destroy reason accepted")
	}
}

func TestPreviewHostSchemaAddsDestroyReasonToAnExistingTable(t *testing.T) {
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
	if _, err := j.db.Exec(`ALTER TABLE preview_hosts DROP COLUMN destroy_reason`); err != nil {
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
	stored, found, err := j.PreviewHostByIdempotencyKey(ctx, host.IdempotencyKey)
	if err != nil || !found || stored.DestroyReason != "" {
		t.Fatalf("stored=%+v found=%v err=%v", stored, found, err)
	}
	if err := j.UpdatePreviewHost(ctx, PreviewHostUpdate{
		IdempotencyKey: host.IdempotencyKey, State: PreviewHostDestroying, DestroyReason: "expired",
	}); err != nil {
		t.Fatal(err)
	}
}
