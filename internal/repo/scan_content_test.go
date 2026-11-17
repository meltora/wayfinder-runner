package repo

import (
	"context"
	"reflect"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestScanAgentConfigCapturesRawContentOnlyWhenAsked(t *testing.T) {
	dir, sha := newScanFixture(t)
	m := NewManager(t.TempDir())
	m.AllowLocal = true
	got, inventory, err := m.ScanAgentConfigWithOptions(context.Background(), dir, "refs/heads/main", nil, AgentConfigScanOptions{ContentMaxItemBytes: v1.AgentConfigContentMaxItemBytes})
	if err != nil || got != sha {
		t.Fatalf("scan = %q %v", got, err)
	}
	if !reflect.DeepEqual(inventory.Contents, map[string]string{"AGENTS.md": "raw $Format:%H$\n"}) {
		t.Fatalf("contents=%q", inventory.Contents)
	}
	_, plain, err := m.ScanAgentConfig(context.Background(), dir, "refs/heads/main")
	if err != nil || plain.Contents != nil || !reflect.DeepEqual(plain.Items, inventory.Items) {
		t.Fatalf("plain scan = %#v %v", plain, err)
	}
}
