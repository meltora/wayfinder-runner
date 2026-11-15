package elevatedcapability

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var testNow = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

const testMCP = `{"mcpServers":{"docs":{"command":"/usr/local/bin/docs-mcp","args":["--stdio"]}}}`

const testRemoteMCP = `{"mcpServers":{"tickets":{"type":"http","url":"https://mcp.example.com/v1"}}}`

const testHook = `{"hooks":{"PostToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"/usr/local/bin/format-changed --quiet","timeout":30}]}]}}`

func digestOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func writeRepositoryFile(t *testing.T, root, name, content string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const testRepositoryID = "web-app"

func grant(id string, kind v1.ElevatedCapabilityKind, key, digest string) v1.ElevatedCapabilityGrant {
	return v1.ElevatedCapabilityGrant{RequestID: id, Kind: kind, Key: key, DeclarationDigest: digest, ExpiresAt: testNow.Add(time.Hour), RepositoryID: testRepositoryID}
}

func repositoriesFor(workDir string) map[string]string {
	return map[string]string{testRepositoryID: workDir}
}

func resolveFor(workDir string, grants ...v1.ElevatedCapabilityGrant) Resolution {
	return Resolve(Input{
		Grants:       &v1.ElevatedCapabilityGrants{SchemaVersion: 2, Grants: grants},
		Repositories: repositoriesFor(workDir), Adapter: "claude", Now: testNow,
	})
}

func TestResolveBindsDeclarationGrantsToTheirRepository(t *testing.T) {
	jobDir := t.TempDir()
	webApp := filepath.Join(jobDir, "repositories", "web-app")
	docs := filepath.Join(jobDir, "repositories", "docs")
	writeRepositoryFile(t, webApp, ".mcp.json", testMCP)
	writeRepositoryFile(t, docs, ".mcp.json", testMCP)
	writeRepositoryFile(t, docs, ".claude/hooks/format.json", testHook)
	bound := grant("ecr_web", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP))
	otherRepository := grant("ecr_docs_hook", v1.ElevatedCapabilityHook, ".claude/hooks/format.json", digestOf(testHook))
	otherRepository.RepositoryID = "docs"
	missing := grant("ecr_web_hook", v1.ElevatedCapabilityHook, ".claude/hooks/format.json", digestOf(testHook))
	unbound := grant("ecr_unbound", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP))
	unbound.RepositoryID = ""
	foreign := grant("ecr_foreign", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP))
	foreign.RepositoryID = "billing"
	network := grant("ecr_net", v1.ElevatedCapabilityNetworkAccess, "registry.npmjs.org", digestOf("host"))
	network.RepositoryID = ""
	resolution := Resolve(Input{
		Grants:       &v1.ElevatedCapabilityGrants{SchemaVersion: 2, Grants: []v1.ElevatedCapabilityGrant{bound, otherRepository, missing, unbound, foreign, network}},
		Repositories: map[string]string{"web-app": webApp, "docs": docs}, Adapter: "claude", Now: testNow,
	})
	want := map[string]Reason{
		"ecr_web": ReasonApplied, "ecr_docs_hook": ReasonApplied, "ecr_web_hook": ReasonDeclarationUnreadable,
		"ecr_unbound": ReasonRepositoryNotBound, "ecr_foreign": ReasonRepositoryNotInJob, "ecr_net": ReasonApplied,
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

func TestResolveRefusesStdioProgramsUnderContainerIsolation(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFile(t, root, ".mcp.json", testMCP)
	writeRepositoryFile(t, root, "remote/.mcp.json", testRemoteMCP)
	writeRepositoryFile(t, root, ".claude/hooks/format.json", testHook)
	resolution := Resolve(Input{
		Grants: &v1.ElevatedCapabilityGrants{SchemaVersion: 2, Grants: []v1.ElevatedCapabilityGrant{
			grant("ecr_stdio", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP)),
			grant("ecr_remote", v1.ElevatedCapabilityMCPServer, "remote/.mcp.json", digestOf(testRemoteMCP)),
			grant("ecr_hook", v1.ElevatedCapabilityHook, ".claude/hooks/format.json", digestOf(testHook)),
			grant("ecr_net", v1.ElevatedCapabilityNetworkAccess, "mcp.example.com", digestOf("host")),
		}},
		Repositories: repositoriesFor(root), Adapter: "claude", ContainerIsolation: true, Now: testNow,
	})
	want := map[string]Reason{
		"ecr_stdio": ReasonContainerProgramUnverifiable, "ecr_remote": ReasonApplied,
		"ecr_hook": ReasonContainerProgramUnverifiable, "ecr_net": ReasonApplied,
	}
	if got := reasonsByID(resolution); !reflect.DeepEqual(got, want) {
		t.Fatalf("reasons = %v", got)
	}
	if got := resolution.Grants.MCPServerNames(); !reflect.DeepEqual(got, []string{"tickets"}) || len(resolution.Grants.Hooks) != 0 {
		t.Fatalf("grants = %+v", resolution.Grants)
	}
}

func reasonsByID(resolution Resolution) map[string]Reason {
	reasons := map[string]Reason{}
	for _, outcome := range resolution.Outcomes {
		reasons[outcome.RequestID] = outcome.Reason
	}
	return reasons
}

func TestResolveEnablesOnlyApprovedMCPServers(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFile(t, root, ".mcp.json", testMCP)
	writeRepositoryFile(t, root, "tools/.mcp.json", `{"mcpServers":{"other":{"command":"evil"}}}`)
	resolution := resolveFor(root, grant("ecr_mcp", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP)))
	if got := resolution.Grants.MCPServerNames(); !reflect.DeepEqual(got, []string{"docs"}) {
		t.Fatalf("approved servers = %v", got)
	}
	want := map[string]any{"type": "stdio", "command": "/usr/local/bin/docs-mcp", "args": []string{"--stdio"}}
	if !reflect.DeepEqual(resolution.Grants.MCPServers["docs"], want) {
		t.Fatalf("server entry = %#v", resolution.Grants.MCPServers["docs"])
	}
	if reasonsByID(resolution)["ecr_mcp"] != ReasonApplied {
		t.Fatalf("outcomes = %+v", resolution.Outcomes)
	}
}

func TestResolveRefusesChangedOrUnsafeDeclarations(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFile(t, root, ".mcp.json", testMCP+" ")
	writeRepositoryFile(t, root, "reserved/.mcp.json", `{"mcpServers":{"platform":{"command":"x"}}}`)
	writeRepositoryFile(t, root, "extra/.mcp.json", `{"mcpServers":{"a":{"command":"x","cwd":"/"}}}`)
	writeRepositoryFile(t, root, "target.json", testMCP)
	if err := os.Symlink(filepath.Join(root, "target.json"), filepath.Join(root, "link.json")); err != nil {
		t.Fatal(err)
	}
	reserved := `{"mcpServers":{"platform":{"command":"x"}}}`
	extra := `{"mcpServers":{"a":{"command":"x","cwd":"/"}}}`
	resolution := resolveFor(root,
		grant("ecr_changed", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP)),
		grant("ecr_reserved", v1.ElevatedCapabilityMCPServer, "reserved/.mcp.json", digestOf(reserved)),
		grant("ecr_extra", v1.ElevatedCapabilityMCPServer, "extra/.mcp.json", digestOf(extra)),
		grant("ecr_link", v1.ElevatedCapabilityMCPServer, "link.json", digestOf(testMCP)),
		grant("ecr_missing", v1.ElevatedCapabilityMCPServer, "missing/.mcp.json", digestOf(testMCP)),
		grant("ecr_escape", v1.ElevatedCapabilityMCPServer, "../.mcp.json", digestOf(testMCP)),
	)
	want := map[string]Reason{
		"ecr_changed": ReasonDigestMismatch, "ecr_reserved": ReasonDeclarationInvalid, "ecr_extra": ReasonDeclarationInvalid,
		"ecr_link": ReasonDeclarationUnreadable, "ecr_missing": ReasonDeclarationUnreadable, "ecr_escape": ReasonDeclarationInvalid,
	}
	if got := reasonsByID(resolution); !reflect.DeepEqual(got, want) {
		t.Fatalf("reasons = %v", got)
	}
	if !resolution.Grants.Empty() {
		t.Fatalf("refused declarations granted something: %+v", resolution.Grants)
	}
}

func TestResolveSkipsGrantsOutsideTheirScope(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFile(t, root, ".mcp.json", testMCP)
	expired := grant("ecr_expired", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP))
	expired.ExpiresAt = testNow
	if got := reasonsByID(resolveFor(root, expired))["ecr_expired"]; got != ReasonExpired {
		t.Fatalf("expired grant reason = %s", got)
	}
	current := grant("ecr_mcp", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP))
	grants := &v1.ElevatedCapabilityGrants{SchemaVersion: 2, Grants: []v1.ElevatedCapabilityGrant{current}}
	codex := Resolve(Input{Grants: grants, Repositories: repositoriesFor(root), Adapter: "codex", Now: testNow})
	plan := Resolve(Input{Grants: grants, Repositories: repositoriesFor(root), Adapter: "claude", ReadOnly: true, Now: testNow})
	if reasonsByID(codex)["ecr_mcp"] != ReasonAdapterUnsupported || !codex.Grants.Empty() {
		t.Fatalf("codex resolution = %+v", codex)
	}
	if reasonsByID(plan)["ecr_mcp"] != ReasonReadOnlyJob || !plan.Grants.Empty() {
		t.Fatalf("plan resolution = %+v", plan)
	}
	if none := Resolve(Input{Repositories: repositoriesFor(root), Adapter: "claude", Now: testNow}); !none.Grants.Empty() || len(none.Outcomes) != 0 {
		t.Fatalf("absent grants resolved to %+v", none)
	}
}

func TestResolveRemoteMCPServerNeedsApprovedHost(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFile(t, root, ".mcp.json", testRemoteMCP)
	mcp := grant("ecr_mcp", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testRemoteMCP))
	if got := reasonsByID(resolveFor(root, mcp))["ecr_mcp"]; got != ReasonRemoteHostNotApproved {
		t.Fatalf("remote server without host grant = %s", got)
	}
	network := grant("ecr_net", v1.ElevatedCapabilityNetworkAccess, "*.example.com", digestOf("host"))
	resolution := resolveFor(root, mcp, network)
	if got := reasonsByID(resolution); got["ecr_mcp"] != ReasonApplied || got["ecr_net"] != ReasonApplied {
		t.Fatalf("reasons = %v", got)
	}
	if !reflect.DeepEqual(resolution.Grants.AllowedDomains, []string{"*.example.com"}) {
		t.Fatalf("domains = %v", resolution.Grants.AllowedDomains)
	}
}

func TestResolveRefusesDuplicateServerNames(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFile(t, root, ".mcp.json", testMCP)
	writeRepositoryFile(t, root, "nested/.mcp.json", testMCP)
	resolution := resolveFor(root,
		grant("ecr_a", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP)),
		grant("ecr_b", v1.ElevatedCapabilityMCPServer, "nested/.mcp.json", digestOf(testMCP)),
	)
	if got := reasonsByID(resolution); got["ecr_a"] != ReasonApplied || got["ecr_b"] != ReasonServerNameConflict {
		t.Fatalf("reasons = %v", got)
	}
}

func TestResolveEnablesApprovedHookDeclarations(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFile(t, root, ".claude/hooks/format.json", testHook)
	writeRepositoryFile(t, root, ".claude/hooks/format.sh", "#!/bin/sh\n")
	badEvent := `{"hooks":{"Everything":[{"hooks":[{"type":"command","command":"x"}]}]}}`
	writeRepositoryFile(t, root, ".claude/hooks/bad.json", badEvent)
	resolution := resolveFor(root,
		grant("ecr_hook", v1.ElevatedCapabilityHook, ".claude/hooks/format.json", digestOf(testHook)),
		grant("ecr_script", v1.ElevatedCapabilityHook, ".claude/hooks/format.sh", digestOf("#!/bin/sh\n")),
		grant("ecr_bad", v1.ElevatedCapabilityHook, ".claude/hooks/bad.json", digestOf(badEvent)),
	)
	want := map[string]Reason{"ecr_hook": ReasonApplied, "ecr_script": ReasonHookScriptOnly, "ecr_bad": ReasonDeclarationInvalid}
	if got := reasonsByID(resolution); !reflect.DeepEqual(got, want) {
		t.Fatalf("reasons = %v", got)
	}
	groups := resolution.Grants.Hooks["PostToolUse"]
	if len(resolution.Grants.Hooks) != 1 || len(groups) != 1 || groups[0]["matcher"] != "Edit" {
		t.Fatalf("hooks = %#v", resolution.Grants.Hooks)
	}
	commands := groups[0]["hooks"].([]map[string]any)
	if commands[0]["command"] != "/usr/local/bin/format-changed --quiet" || commands[0]["timeout"] != 30 {
		t.Fatalf("hook commands = %#v", commands)
	}
}

func TestNormalizeHost(t *testing.T) {
	for _, valid := range []string{"registry.npmjs.org", "*.example.com", "a-b.example.io"} {
		if got, ok := NormalizeHost(valid); !ok || got != valid {
			t.Fatalf("%q rejected", valid)
		}
	}
	for _, invalid := range []string{"", "localhost", "Example.com", "https://example.com", "example.com:443", "10.0.0.1", "*", "*.com.", "a..b.com", "-a.com", "a_b.com", "example.com/path", "*.co.uk", "*.github.io", "*.amazonaws.com", "egress:api.github.com:443"} {
		if _, ok := NormalizeHost(invalid); ok {
			t.Fatalf("%q accepted", invalid)
		}
	}
}

func TestHostAllowed(t *testing.T) {
	domains := map[string]bool{"api.example.com": true, "*.internal.example.org": true}
	for rawURL, want := range map[string]bool{
		"https://api.example.com/x":            true,
		"https://API.example.com:8443/x":       true,
		"https://a.internal.example.org":       true,
		"https://internal.example.org":         false,
		"https://other.example.com":            false,
		"https://api.example.com.attacker.net": false,
	} {
		if got := HostAllowed(rawURL, domains); got != want {
			t.Fatalf("HostAllowed(%q) = %v", rawURL, got)
		}
	}
}
