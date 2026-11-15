package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/elevatedcapability"
	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/validation"
)

func elevatedTestDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func elevatedOutcomeReasons(jr *jobRunner) map[string]elevatedcapability.Reason {
	reasons := map[string]elevatedcapability.Reason{}
	for _, outcome := range jr.elevatedOutcomes {
		reasons[outcome.RequestID] = outcome.Reason
	}
	return reasons
}

const elevatedTestDeclaration = `{"mcpServers":{"docs":{"command":"/usr/local/bin/docs-mcp","args":["--stdio"]}}}`

func elevatedTestRunner(t *testing.T) (*jobRunner, string) {
	t.Helper()
	jobDir := t.TempDir()
	workDir := filepath.Join(jobDir, "repositories", "web-app")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".mcp.json"), []byte(elevatedTestDeclaration), 0o644); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(time.Hour)
	jr := &jobRunner{
		logger:       slog.New(slog.DiscardHandler),
		workDir:      jobDir,
		repositories: []checkedOutRepository{{ID: "web-app", WorkDir: workDir, Primary: true}},
		job: v1.JobSpec{ElevatedCapabilities: &v1.ElevatedCapabilityGrants{SchemaVersion: 2, Grants: []v1.ElevatedCapabilityGrant{
			{RequestID: "mcp", Kind: v1.ElevatedCapabilityMCPServer, Key: ".mcp.json", DeclarationDigest: elevatedTestDigest(elevatedTestDeclaration), ExpiresAt: expires, RepositoryID: "web-app"},
			{RequestID: "host", Kind: v1.ElevatedCapabilityNetworkAccess, Key: "registry.npmjs.org", DeclarationDigest: elevatedTestDigest("registry.npmjs.org"), ExpiresAt: expires},
			{RequestID: "old", Kind: v1.ElevatedCapabilityNetworkAccess, Key: "old.example.com", DeclarationDigest: elevatedTestDigest("old.example.com"), ExpiresAt: time.Now().UTC().Add(-time.Minute)},
		}}},
	}
	return jr, workDir
}

func TestJobRunnerResolvesOnlyGrantedElevatedCapabilities(t *testing.T) {
	jr, _ := elevatedTestRunner(t)
	grants := jr.resolveElevatedCapabilities("claude", false, false)
	if names := grants.MCPServerNames(); !reflect.DeepEqual(names, []string{"docs"}) {
		t.Fatalf("MCP servers = %v", names)
	}
	if !reflect.DeepEqual(grants.AllowedDomains, []string{"registry.npmjs.org"}) {
		t.Fatalf("allowed domains = %v", grants.AllowedDomains)
	}
	reasons := elevatedOutcomeReasons(jr)
	if reasons["mcp"] != elevatedcapability.ReasonApplied || reasons["host"] != elevatedcapability.ReasonApplied || reasons["old"] != elevatedcapability.ReasonExpired {
		t.Fatalf("outcomes = %v", reasons)
	}
}

func TestJobRunnerRefusesStdioGrantsUnderDockerIsolation(t *testing.T) {
	jr, _ := elevatedTestRunner(t)
	grants := jr.resolveElevatedCapabilities("claude", false, true)
	if len(grants.MCPServers) != 0 || !reflect.DeepEqual(grants.AllowedDomains, []string{"registry.npmjs.org"}) {
		t.Fatalf("grants = %+v", grants)
	}
	if reason := elevatedOutcomeReasons(jr)["mcp"]; reason != elevatedcapability.ReasonContainerProgramUnverifiable {
		t.Fatalf("docker stdio grant reason = %s", reason)
	}
}

func TestElevatedProtectedDirsIncludeContainerMounts(t *testing.T) {
	jr, _ := elevatedTestRunner(t)
	host := jr.elevatedProtectedDirs(false)
	if !slices.Contains(host, jr.workDir) || slices.Contains(host, isolation.ContainerHome) {
		t.Fatalf("host protected dirs = %v", host)
	}
	docker := jr.elevatedProtectedDirs(true)
	if !slices.Contains(docker, jr.workDir) || !slices.Contains(docker, isolation.ContainerHome) {
		t.Fatalf("docker protected dirs = %v", docker)
	}
}

func TestJobRunnerAppliesGrantsOnlyToTheirRepositoryCheckout(t *testing.T) {
	jr, _ := elevatedTestRunner(t)
	docsDir := filepath.Join(jr.workDir, "repositories", "docs")
	if err := os.MkdirAll(docsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	jr.repositories = append(jr.repositories, checkedOutRepository{ID: "docs", WorkDir: docsDir})
	jr.job.ElevatedCapabilities.Grants[0].RepositoryID = "docs"
	grants := jr.resolveElevatedCapabilities("claude", false, false)
	if len(grants.MCPServers) != 0 {
		t.Fatalf("grant for docs applied from another checkout: %+v", grants.MCPServers)
	}
	if reason := elevatedOutcomeReasons(jr)["mcp"]; reason != elevatedcapability.ReasonDeclarationUnreadable {
		t.Fatalf("reason = %s", reason)
	}
}

func TestJobRunnerPublishesElevatedCapabilityOutcomesOnce(t *testing.T) {
	jr, events := testJobRunner(t)
	grantRunner, _ := elevatedTestRunner(t)
	jr.workDir, jr.repositories = grantRunner.workDir, grantRunner.repositories
	jr.job.ElevatedCapabilities = grantRunner.job.ElevatedCapabilities
	jr.resolveElevatedCapabilities("claude", false, true)
	jr.publishElevatedCapabilityOutcomes(context.Background())
	jr.publishElevatedCapabilityOutcomes(context.Background())
	published := events.byKind(v1.EventKindElevatedCapabilityOutcomes)
	if len(published) != 1 {
		t.Fatalf("published = %v", published)
	}
	raw, _ := json.Marshal(published[0])
	var event v1.ElevatedCapabilityOutcomesEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	if err := validation.ElevatedCapabilityOutcomesEvent(event); err != nil {
		t.Fatalf("published invalid event: %v", err)
	}
	got := map[string]v1.ElevatedCapabilityOutcome{}
	for _, outcome := range event.Outcomes {
		got[outcome.RequestID] = outcome
	}
	if len(got) != 3 || !got["host"].Applied || got["mcp"].Reason != "container_program_unverifiable" || got["old"].Reason != "expired" {
		t.Fatalf("outcomes = %+v", event.Outcomes)
	}
	if event.AgentRunAuthority != nil {
		t.Fatalf("authority = %+v", event.AgentRunAuthority)
	}
}

func TestJobRunnerSkipsOutcomesWithoutGrants(t *testing.T) {
	jr, events := testJobRunner(t)
	jr.resolveElevatedCapabilities("claude", false, false)
	jr.publishElevatedCapabilityOutcomes(context.Background())
	if published := events.byKind(v1.EventKindElevatedCapabilityOutcomes); len(published) != 0 {
		t.Fatalf("published = %v", published)
	}
}

func TestElevatedCapabilityReasonsMatchWireContract(t *testing.T) {
	wire := make([]string, 0, len(elevatedcapability.Reasons))
	for _, reason := range elevatedcapability.Reasons {
		wire = append(wire, string(reason))
	}
	if !reflect.DeepEqual(wire, v1.ElevatedCapabilityOutcomeReasons) {
		t.Fatalf("resolver reasons %v differ from wire reasons %v", wire, v1.ElevatedCapabilityOutcomeReasons)
	}
}

func TestJobRunnerWithoutElevatedCapabilitiesResolvesNothing(t *testing.T) {
	jr := &jobRunner{logger: slog.New(slog.DiscardHandler)}
	if grants := jr.resolveElevatedCapabilities("claude", false, false); !grants.Empty() {
		t.Fatalf("grants = %#v", grants)
	}
	if len(jr.elevatedOutcomes) != 0 {
		t.Fatalf("outcomes = %v", jr.elevatedOutcomes)
	}
}
