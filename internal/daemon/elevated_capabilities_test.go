package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/elevatedcapability"
)

func elevatedTestDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func TestJobRunnerResolvesOnlyGrantedElevatedCapabilities(t *testing.T) {
	workDir := t.TempDir()
	declaration := `{"mcpServers":{"docs":{"command":"node","args":["docs.js"]}}}`
	if err := os.WriteFile(filepath.Join(workDir, ".mcp.json"), []byte(declaration), 0o644); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(time.Hour)
	jr := &jobRunner{
		logger: slog.New(slog.DiscardHandler),
		job: v1.JobSpec{ElevatedCapabilities: &v1.ElevatedCapabilityGrants{SchemaVersion: 1, Grants: []v1.ElevatedCapabilityGrant{
			{RequestID: "mcp", Kind: v1.ElevatedCapabilityMCPServer, Key: ".mcp.json", DeclarationDigest: elevatedTestDigest(declaration), ExpiresAt: expires},
			{RequestID: "host", Kind: v1.ElevatedCapabilityNetworkAccess, Key: "registry.npmjs.org", DeclarationDigest: elevatedTestDigest("registry.npmjs.org"), ExpiresAt: expires},
			{RequestID: "old", Kind: v1.ElevatedCapabilityNetworkAccess, Key: "old.example.com", DeclarationDigest: elevatedTestDigest("old.example.com"), ExpiresAt: time.Now().UTC().Add(-time.Minute)},
		}}},
	}
	grants := jr.resolveElevatedCapabilities("claude", workDir, false)
	if names := grants.MCPServerNames(); !reflect.DeepEqual(names, []string{"docs"}) {
		t.Fatalf("MCP servers = %v", names)
	}
	if !reflect.DeepEqual(grants.AllowedDomains, []string{"registry.npmjs.org"}) {
		t.Fatalf("allowed domains = %v", grants.AllowedDomains)
	}
	reasons := map[string]elevatedcapability.Reason{}
	for _, outcome := range jr.elevatedOutcomes {
		reasons[outcome.RequestID] = outcome.Reason
	}
	if reasons["mcp"] != elevatedcapability.ReasonApplied || reasons["host"] != elevatedcapability.ReasonApplied || reasons["old"] != elevatedcapability.ReasonExpired {
		t.Fatalf("outcomes = %v", reasons)
	}
}

func TestJobRunnerWithoutElevatedCapabilitiesResolvesNothing(t *testing.T) {
	jr := &jobRunner{logger: slog.New(slog.DiscardHandler)}
	if grants := jr.resolveElevatedCapabilities("claude", t.TempDir(), false); !grants.Empty() {
		t.Fatalf("grants = %#v", grants)
	}
	if len(jr.elevatedOutcomes) != 0 {
		t.Fatalf("outcomes = %v", jr.elevatedOutcomes)
	}
}
