package validation

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func validAgentPackComposition() v1.AgentPackCompositionEvent {
	layers := make([]v1.AgentPackCompositionLayer, 0, len(v1.AgentPackCompositionLayerOrder))
	for _, name := range v1.AgentPackCompositionLayerOrder {
		layers = append(layers, v1.AgentPackCompositionLayer{Layer: name, Status: v1.AgentPackCompositionApplied, Sources: []v1.AgentPackCompositionSource{}})
	}
	layers[0].Sources = []v1.AgentPackCompositionSource{{Path: "claude-settings.json", SourceType: v1.AgentConfigProviderConfiguration, DigestSHA256: strings.Repeat("a", 64)}}
	layers[2].Sources = []v1.AgentPackCompositionSource{{RepositoryID: "repo-1", Path: "CLAUDE.md", SourceType: v1.AgentConfigRepositoryInstructions, DigestSHA256: strings.Repeat("b", 64)}}
	return v1.AgentPackCompositionEvent{
		Kind: v1.EventKindAgentPackComposition, ManifestKind: v1.JobKindPlan,
		LeaseID: "lease-1", JobID: "job-1", AttemptID: "attempt-1", ManifestID: "manifest-1",
		ManifestDigest: strings.Repeat("c", 64), FencingToken: 7, ObservedAt: time.Unix(1, 0),
		SchemaVersion: 1, Phase: v1.AgentPackCompositionPhaseBeforeStart, Adapter: "claude",
		Isolation: v1.AgentPackCompositionIsolationDocker,
		AgentPack: &v1.AgentPackCompositionPack{Version: "1.0.0", Digest: strings.Repeat("d", 64), KeyID: "key-1", PluginLoaded: true},
		PackView:  v1.AgentPackCompositionPackViewVerified, Layers: layers,
		Conflicts: []v1.AgentPackCompositionConflict{{Code: v1.AgentPackConflictRepositoryMCPExcluded, Layer: v1.AgentPackCompositionLayerRepository, RepositoryID: "repo-1", Path: ".mcp.json", Winner: v1.AgentPackWinnerPlatformSafety}},
	}
}

func TestAgentPackCompositionEventAcceptsBoundedReport(t *testing.T) {
	if err := AgentPackCompositionEvent(validAgentPackComposition()); err != nil {
		t.Fatal(err)
	}
	withoutPack := validAgentPackComposition()
	withoutPack.AgentPack = nil
	withoutPack.PackView = v1.AgentPackCompositionPackViewAbsent
	withoutPack.Layers[1].Status = v1.AgentPackCompositionAbsent
	if err := AgentPackCompositionEvent(withoutPack); err != nil {
		t.Fatal(err)
	}
}

func TestAgentPackCompositionEventRejectsMalformedReports(t *testing.T) {
	cases := map[string]func(*v1.AgentPackCompositionEvent){
		"kind":            func(e *v1.AgentPackCompositionEvent) { e.Kind = "progress" },
		"schema version":  func(e *v1.AgentPackCompositionEvent) { e.SchemaVersion = 2 },
		"phase":           func(e *v1.AgentPackCompositionEvent) { e.Phase = "during" },
		"adapter":         func(e *v1.AgentPackCompositionEvent) { e.Adapter = "cursor" },
		"isolation":       func(e *v1.AgentPackCompositionEvent) { e.Isolation = "vm" },
		"pack view":       func(e *v1.AgentPackCompositionEvent) { e.PackView = "unknown" },
		"missing fence":   func(e *v1.AgentPackCompositionEvent) { e.FencingToken = 0 },
		"bad lease":       func(e *v1.AgentPackCompositionEvent) { e.LeaseID = "../lease" },
		"bad authority":   func(e *v1.AgentPackCompositionEvent) { e.AgentRunAuthority = &v1.AgentRunAuthority{} },
		"pack version":    func(e *v1.AgentPackCompositionEvent) { e.AgentPack.Version = "" },
		"pack digest":     func(e *v1.AgentPackCompositionEvent) { e.AgentPack.Digest = strings.Repeat("F", 64) },
		"pack key":        func(e *v1.AgentPackCompositionEvent) { e.AgentPack.KeyID = "../key" },
		"null pack view":  func(e *v1.AgentPackCompositionEvent) { e.AgentPack = nil },
		"absent view":     func(e *v1.AgentPackCompositionEvent) { e.PackView = v1.AgentPackCompositionPackViewAbsent },
		"modified before": func(e *v1.AgentPackCompositionEvent) { e.PackView = v1.AgentPackCompositionPackViewModified },
		"missing layer":   func(e *v1.AgentPackCompositionEvent) { e.Layers = e.Layers[:6] },
		"layer order":     func(e *v1.AgentPackCompositionEvent) { e.Layers[0], e.Layers[1] = e.Layers[1], e.Layers[0] },
		"layer status":    func(e *v1.AgentPackCompositionEvent) { e.Layers[3].Status = "pending" },
		"nil sources":     func(e *v1.AgentPackCompositionEvent) { e.Layers[3].Sources = nil },
		"nil conflicts":   func(e *v1.AgentPackCompositionEvent) { e.Conflicts = nil },
		"too many sources": func(e *v1.AgentPackCompositionEvent) {
			e.Layers[3].Sources = make([]v1.AgentPackCompositionSource, 65)
			for i := range e.Layers[3].Sources {
				e.Layers[3].Sources[i] = v1.AgentPackCompositionSource{Path: "a/CLAUDE.md", SourceType: v1.AgentConfigRepositoryInstructions}
			}
		},
		"traversal path":  func(e *v1.AgentPackCompositionEvent) { e.Layers[2].Sources[0].Path = "../CLAUDE.md" },
		"absolute path":   func(e *v1.AgentPackCompositionEvent) { e.Layers[2].Sources[0].Path = "/Users/operator/CLAUDE.md" },
		"nul path":        func(e *v1.AgentPackCompositionEvent) { e.Layers[2].Sources[0].Path = "a\x00b" },
		"long path":       func(e *v1.AgentPackCompositionEvent) { e.Layers[2].Sources[0].Path = strings.Repeat("x", 1025) },
		"source type":     func(e *v1.AgentPackCompositionEvent) { e.Layers[2].Sources[0].SourceType = "secret" },
		"source digest":   func(e *v1.AgentPackCompositionEvent) { e.Layers[2].Sources[0].DigestSHA256 = "abc" },
		"source repo":     func(e *v1.AgentPackCompositionEvent) { e.Layers[2].Sources[0].RepositoryID = "../repo" },
		"conflict code":   func(e *v1.AgentPackCompositionEvent) { e.Conflicts[0].Code = "unknown" },
		"conflict layer":  func(e *v1.AgentPackCompositionEvent) { e.Conflicts[0].Layer = "other" },
		"conflict winner": func(e *v1.AgentPackCompositionEvent) { e.Conflicts[0].Winner = v1.AgentPackWinnerUnresolved },
		"conflict path":   func(e *v1.AgentPackCompositionEvent) { e.Conflicts[0].Path = "../x" },
		"too many conflicts": func(e *v1.AgentPackCompositionEvent) {
			e.Conflicts = make([]v1.AgentPackCompositionConflict, 65)
			for i := range e.Conflicts {
				e.Conflicts[i] = v1.AgentPackCompositionConflict{Code: v1.AgentPackConflictRepositoryMCPExcluded, Layer: v1.AgentPackCompositionLayerRepository, Winner: v1.AgentPackWinnerPlatformSafety}
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			event := validAgentPackComposition()
			mutate(&event)
			if err := AgentPackCompositionEvent(event); err == nil {
				t.Fatal("malformed report accepted")
			}
		})
	}
}
