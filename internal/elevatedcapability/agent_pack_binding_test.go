package elevatedcapability

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var testPackDigest = strings.Repeat("d", 64)

func packGrant(id string, kind v1.ElevatedCapabilityKind, key, digest string) v1.ElevatedCapabilityGrant {
	bound := grant(id, kind, key, digest)
	bound.RepositoryID = ""
	bound.AgentPackDigest = testPackDigest
	return bound
}

func TestResolveBindsAgentPackGrantsToTheJobPack(t *testing.T) {
	jobDir := t.TempDir()
	checkout := filepath.Join(jobDir, "repositories", "web-app")
	pack := filepath.Join(jobDir, "pack")
	writeRepositoryFile(t, pack, ".mcp.json", testMCP)
	writeRepositoryFile(t, pack, "hooks/format.json", testHook)
	writeRepositoryFile(t, pack, ".claude/hooks/format.json", testHook)
	writeRepositoryFile(t, checkout, ".mcp.json", `{"mcpServers":{}}`)
	mcp := packGrant("ecr_pack_mcp", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP))
	hook := packGrant("ecr_pack_hook", v1.ElevatedCapabilityHook, ".claude/hooks/format.json", digestOf(testHook))
	pluginHookPath := packGrant("ecr_pack_plugin_path", v1.ElevatedCapabilityHook, "hooks/format.json", digestOf(testHook))
	otherPack := packGrant("ecr_other_pack", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP))
	otherPack.AgentPackDigest = strings.Repeat("e", 64)
	both := packGrant("ecr_both", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP))
	both.RepositoryID = testRepositoryID
	malformed := packGrant("ecr_malformed", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP))
	malformed.AgentPackDigest = "sha256:" + testPackDigest
	resolution := Resolve(Input{
		Grants: &v1.ElevatedCapabilityGrants{SchemaVersion: 2, Grants: []v1.ElevatedCapabilityGrant{
			mcp, hook, pluginHookPath, otherPack, both, malformed,
		}},
		Repositories:    repositoriesFor(checkout),
		AgentPackDigest: testPackDigest, AgentPackDir: pack,
		ProtectedDirs: []string{jobDir}, Adapter: "claude", Now: testNow,
	})
	want := map[string]Reason{
		"ecr_pack_mcp": ReasonApplied, "ecr_pack_hook": ReasonApplied, "ecr_pack_plugin_path": ReasonHookScriptOnly,
		"ecr_other_pack": ReasonAgentPackNotInJob, "ecr_both": ReasonDeclarationInvalid, "ecr_malformed": ReasonDeclarationInvalid,
	}
	if got := reasonsByID(resolution); !reflect.DeepEqual(got, want) {
		t.Fatalf("reasons = %v", got)
	}
	if got := resolution.Grants.MCPServerNames(); !reflect.DeepEqual(got, []string{"docs"}) {
		t.Fatalf("servers = %v", got)
	}
	if len(resolution.Grants.Hooks["PostToolUse"]) != 1 {
		t.Fatalf("hooks = %#v", resolution.Grants.Hooks)
	}
}

func TestResolveRefusesAgentPackGrantsWithoutAnInstalledPack(t *testing.T) {
	checkout := t.TempDir()
	writeRepositoryFile(t, checkout, ".mcp.json", testMCP)
	grants := &v1.ElevatedCapabilityGrants{SchemaVersion: 2, Grants: []v1.ElevatedCapabilityGrant{
		packGrant("ecr_pack_mcp", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP)),
	}}
	for name, input := range map[string]Input{
		"no pack":        {Grants: grants, Repositories: repositoriesFor(checkout), Adapter: "claude", Now: testNow},
		"digest only":    {Grants: grants, Repositories: repositoriesFor(checkout), AgentPackDigest: testPackDigest, Adapter: "claude", Now: testNow},
		"directory only": {Grants: grants, Repositories: repositoriesFor(checkout), AgentPackDir: checkout, Adapter: "claude", Now: testNow},
		"another digest": {Grants: grants, Repositories: repositoriesFor(checkout), AgentPackDigest: strings.Repeat("f", 64), AgentPackDir: checkout, Adapter: "claude", Now: testNow},
	} {
		resolution := Resolve(input)
		if got := reasonsByID(resolution)["ecr_pack_mcp"]; got != ReasonAgentPackNotInJob || !resolution.Grants.Empty() {
			t.Fatalf("%s: reason = %s grants = %+v", name, got, resolution.Grants)
		}
	}
}

func TestResolveKeepsRefusingUnboundDeclarationGrants(t *testing.T) {
	pack := t.TempDir()
	writeRepositoryFile(t, pack, ".mcp.json", testMCP)
	unbound := grant("ecr_unbound", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP))
	unbound.RepositoryID = ""
	resolution := Resolve(Input{
		Grants:          &v1.ElevatedCapabilityGrants{SchemaVersion: 2, Grants: []v1.ElevatedCapabilityGrant{unbound}},
		AgentPackDigest: testPackDigest, AgentPackDir: pack, Adapter: "claude", Now: testNow,
	})
	if got := reasonsByID(resolution)["ecr_unbound"]; got != ReasonRepositoryNotBound {
		t.Fatalf("reason = %s", got)
	}
}
