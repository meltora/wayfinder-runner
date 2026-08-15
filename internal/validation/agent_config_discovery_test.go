package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validAgentConfigDiscoveryEvent() v1.AgentConfigDiscoveryEvent {
	return v1.AgentConfigDiscoveryEvent{
		Kind: v1.EventKindAgentConfigDiscovery, ScanID: "scan-1",
		ProjectID: "project-1", RepositoryID: "repository-1", BranchRef: "main",
		CommitSHA: strings.Repeat("a", 40), CatalogVersion: v1.AgentConfigCatalogVersion,
		ParserVersion: "runner-scanner-v1", ObservedAt: time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC),
		Items: []v1.AgentConfigDiscoveryItem{{
			Path: "AGENTS.md", SourceType: v1.AgentConfigRepositoryInstructions,
			DigestSHA256: strings.Repeat("b", 64), SizeBytes: 10,
			Classification: v1.AgentConfigClassReady, Risk: v1.AgentConfigRiskLow,
			Compatibility: v1.AgentConfigCompatible, Decision: v1.AgentConfigDecisionNotRequired,
			RequestedPermissions: []string{}, Conflicts: []string{},
		}},
	}
}

func TestAgentConfigDiscoveryEventAcceptsCanonicalMetadata(t *testing.T) {
	if err := AgentConfigDiscoveryEvent(validAgentConfigDiscoveryEvent()); err != nil {
		t.Fatal(err)
	}
}

func TestAgentConfigDiscoveryEventRejectsInvalidAuthorityAndSnapshotFields(t *testing.T) {
	mutations := map[string]func(*v1.AgentConfigDiscoveryEvent){
		"kind":       func(event *v1.AgentConfigDiscoveryEvent) { event.Kind = "config_inventory" },
		"scan":       func(event *v1.AgentConfigDiscoveryEvent) { event.ScanID = "" },
		"project":    func(event *v1.AgentConfigDiscoveryEvent) { event.ProjectID = "../project" },
		"repository": func(event *v1.AgentConfigDiscoveryEvent) { event.RepositoryID = "" },
		"branch":     func(event *v1.AgentConfigDiscoveryEvent) { event.BranchRef = "-unsafe" },
		"commit":     func(event *v1.AgentConfigDiscoveryEvent) { event.CommitSHA = "ABC" },
		"catalog":    func(event *v1.AgentConfigDiscoveryEvent) { event.CatalogVersion = "catalog-v2" },
		"parser":     func(event *v1.AgentConfigDiscoveryEvent) { event.ParserVersion = "bad parser" },
		"observed":   func(event *v1.AgentConfigDiscoveryEvent) { event.ObservedAt = time.Time{} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			event := validAgentConfigDiscoveryEvent()
			mutate(&event)
			if err := AgentConfigDiscoveryEvent(event); err == nil {
				t.Fatal("invalid event was accepted")
			}
		})
	}
}

func TestAgentConfigDiscoveryEventRejectsUnsafeOrNonCanonicalItems(t *testing.T) {
	mutations := map[string]func(*v1.AgentConfigDiscoveryEvent){
		"path traversal": func(event *v1.AgentConfigDiscoveryEvent) { event.Items[0].Path = "../AGENTS.md" },
		"missing digest": func(event *v1.AgentConfigDiscoveryEvent) { event.Items[0].DigestSHA256 = "" },
		"rejected digest": func(event *v1.AgentConfigDiscoveryEvent) {
			event.Items[0].SourceType = v1.AgentConfigCredential
			event.Items[0].Classification = v1.AgentConfigClassRejected
			event.Items[0].Decision = v1.AgentConfigDecisionRejected
		},
		"self approval": func(event *v1.AgentConfigDiscoveryEvent) { event.Items[0].Decision = v1.AgentConfigDecisionApproved },
		"unsafe bypass": func(event *v1.AgentConfigDiscoveryEvent) {
			event.Items[0].SourceType = v1.AgentConfigMCPDeclaration
		},
		"unsorted permissions": func(event *v1.AgentConfigDiscoveryEvent) {
			event.Items[0].Decision = v1.AgentConfigDecisionPending
			event.Items[0].RequestedPermissions = []string{"process.execute", "network.connect"}
		},
		"duplicate path": func(event *v1.AgentConfigDiscoveryEvent) {
			event.Items = append(event.Items, event.Items[0])
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			event := validAgentConfigDiscoveryEvent()
			mutate(&event)
			if err := AgentConfigDiscoveryEvent(event); err == nil {
				t.Fatal("invalid inventory was accepted")
			}
		})
	}
}
