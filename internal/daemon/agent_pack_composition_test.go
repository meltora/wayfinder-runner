package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

func compositionConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFixtureFile(t, dir, "claude-settings.json", `{"permissions":{}}`, 0o600)
	writeFixtureFile(t, dir, "claude-mcp.json", `{"mcpServers":{}}`, 0o600)
	writeFixtureFile(t, dir, "job.token", "secret-token\n", 0o400)
	return dir
}

func compositionPackFiles() map[string]string {
	return map[string]string{
		".claude-plugin/plugin.json": strings.Repeat("1", 64),
		"skills/plan/SKILL.md":       strings.Repeat("2", 64),
		"skills/review/SKILL.md":     strings.Repeat("3", 64),
		"WORKFLOW.md":                strings.Repeat("4", 64),
	}
}

func compositionInputFor(t *testing.T, primary string, peers ...string) compositionInput {
	t.Helper()
	repositories := []compositionRepository{{ID: "repo-1", Root: primary, Primary: true}}
	for index, peer := range peers {
		repositories = append(repositories, compositionRepository{ID: fmt.Sprintf("repo-%d", index+2), Root: peer})
	}
	return compositionInput{
		Adapter: "claude", Isolation: v1.AgentPackCompositionIsolationDocker,
		Pack:      &v1.AgentPackRef{Version: "1.0.0", Digest: strings.Repeat("d", 64), KeyID: "platform-key-1"},
		PackFiles: compositionPackFiles(), PluginLoaded: true,
		ConfigDir: compositionConfigDir(t), HostHome: t.TempDir(), Repositories: repositories,
	}
}

func compositionEventFor(t *testing.T, body compositionBody, phase, packView string) v1.AgentPackCompositionEvent {
	t.Helper()
	event := v1.AgentPackCompositionEvent{
		Kind: v1.EventKindAgentPackComposition, ManifestKind: v1.JobKindPlan, LeaseID: "lease-1", JobID: "job-1",
		AttemptID: "attempt-1", ManifestID: "manifest-1", ManifestDigest: strings.Repeat("a", 64),
		FencingToken: 7, ObservedAt: time.Now(),
	}
	body.apply(&event, phase, packView)
	if err := validation.AgentPackCompositionEvent(event); err != nil {
		t.Fatalf("composition event is invalid: %v\n%+v", err, event)
	}
	return event
}

func layerByName(t *testing.T, body compositionBody, name string) v1.AgentPackCompositionLayer {
	t.Helper()
	for _, layer := range body.Layers {
		if layer.Layer == name {
			return layer
		}
	}
	t.Fatalf("layer %s missing", name)
	return v1.AgentPackCompositionLayer{}
}

func conflictCodes(body compositionBody) []string {
	codes := make([]string, 0, len(body.Conflicts))
	for _, conflict := range body.Conflicts {
		codes = append(codes, conflict.Code+":"+conflict.RepositoryID+":"+conflict.Path+":"+conflict.Winner)
	}
	return codes
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestCompositionReportWithoutConflictsListsLayersInPrecedenceOrder(t *testing.T) {
	primary := t.TempDir()
	writeFixtureFile(t, primary, "CLAUDE.md", "unique-body-marker", 0o644)
	writeFixtureFile(t, primary, ".claude/skills/local/SKILL.md", "# local", 0o644)
	writeFixtureFile(t, primary, "services/api/CLAUDE.md", "nested", 0o644)
	input := compositionInputFor(t, primary)
	body, err := buildAgentPackComposition(input)
	if err != nil {
		t.Fatal(err)
	}
	event := compositionEventFor(t, body, v1.AgentPackCompositionPhaseBeforeStart, v1.AgentPackCompositionPackViewVerified)
	for index, layer := range event.Layers {
		if layer.Layer != v1.AgentPackCompositionLayerOrder[index] {
			t.Fatalf("layer %d = %s", index, layer.Layer)
		}
	}
	if len(body.Conflicts) != 0 || body.Truncated {
		t.Fatalf("conflicts = %v truncated = %v", conflictCodes(body), body.Truncated)
	}
	if event.AgentPack == nil || !event.AgentPack.PluginLoaded || event.AgentPack.KeyID != "platform-key-1" || event.AgentPack.Version != "1.0.0" {
		t.Fatalf("agent pack = %+v", event.AgentPack)
	}
	safety := layerByName(t, body, v1.AgentPackCompositionLayerPlatformSafety)
	wantSafety := map[string]string{
		"claude-settings.json": fileDigest(t, filepath.Join(input.ConfigDir, "claude-settings.json")),
		"claude-mcp.json":      fileDigest(t, filepath.Join(input.ConfigDir, "claude-mcp.json")),
	}
	if safety.Status != v1.AgentPackCompositionApplied || len(safety.Sources) != len(wantSafety) {
		t.Fatalf("platform safety = %+v", safety)
	}
	for _, source := range safety.Sources {
		if wantSafety[source.Path] != source.DigestSHA256 || source.RepositoryID != "" {
			t.Fatalf("platform safety source = %+v", source)
		}
	}
	pack := layerByName(t, body, v1.AgentPackCompositionLayerAgentPack)
	if pack.Status != v1.AgentPackCompositionApplied || len(pack.Sources) != 4 {
		t.Fatalf("agent pack layer = %+v", pack)
	}
	types := map[string]v1.AgentConfigSourceType{}
	for _, source := range pack.Sources {
		types[source.Path] = source.SourceType
	}
	if types[".claude-plugin/plugin.json"] != v1.AgentConfigProviderConfiguration || types["skills/plan/SKILL.md"] != v1.AgentConfigSkill ||
		types["WORKFLOW.md"] != v1.AgentConfigDevelopmentInstruction {
		t.Fatalf("pack source types = %v", types)
	}
	repository := layerByName(t, body, v1.AgentPackCompositionLayerRepository)
	if repository.Status != v1.AgentPackCompositionApplied || len(repository.Sources) != 2 ||
		repository.Sources[0].Path != ".claude/skills/local/SKILL.md" || repository.Sources[1].Path != "CLAUDE.md" ||
		repository.Sources[1].RepositoryID != "repo-1" || repository.Sources[1].DigestSHA256 != fileDigest(t, filepath.Join(primary, "CLAUDE.md")) {
		t.Fatalf("repository layer = %+v", repository)
	}
	nested := layerByName(t, body, v1.AgentPackCompositionLayerNested)
	if nested.Status != v1.AgentPackCompositionApplied || len(nested.Sources) != 1 || nested.Sources[0].Path != "services/api/CLAUDE.md" {
		t.Fatalf("nested layer = %+v", nested)
	}
	for _, name := range []string{v1.AgentPackCompositionLayerExecutionManifest, v1.AgentPackCompositionLayerTicketContext} {
		if layer := layerByName(t, body, name); layer.Status != v1.AgentPackCompositionApplied || len(layer.Sources) != 0 {
			t.Fatalf("%s = %+v", name, layer)
		}
	}
	if host := layerByName(t, body, v1.AgentPackCompositionLayerHostUser); host.Status != v1.AgentPackCompositionExcluded || len(host.Sources) != 0 {
		t.Fatalf("host layer under docker = %+v", host)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{input.ConfigDir, primary, input.HostHome, "secret-token", "job.token", "unique-body-marker"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("report leaks %q: %s", forbidden, raw)
		}
	}
}

func TestCompositionConflictRules(t *testing.T) {
	cases := map[string]struct {
		prepare func(t *testing.T, input *compositionInput)
		want    []string
	}{
		"repository settings": {
			prepare: func(t *testing.T, input *compositionInput) {
				writeFixtureFile(t, input.Repositories[0].Root, ".claude/settings.json", "{}", 0o644)
				writeFixtureFile(t, input.Repositories[0].Root, ".claude/settings.local.json", "{}", 0o644)
			},
			want: []string{
				"repository_settings_excluded:repo-1:.claude/settings.json:platform_safety",
				"repository_settings_excluded:repo-1:.claude/settings.local.json:platform_safety",
			},
		},
		"repository mcp": {
			prepare: func(t *testing.T, input *compositionInput) {
				writeFixtureFile(t, input.Repositories[0].Root, ".mcp.json", "{}", 0o644)
			},
			want: []string{"repository_mcp_excluded:repo-1:.mcp.json:platform_safety"},
		},
		"skill name collision": {
			prepare: func(t *testing.T, input *compositionInput) {
				writeFixtureFile(t, input.Repositories[0].Root, ".claude/skills/plan/SKILL.md", "# plan", 0o644)
				writeFixtureFile(t, input.Repositories[0].Root, ".claude/skills/plan/notes.md", "notes", 0o644)
				writeFixtureFile(t, input.Repositories[0].Root, ".claude/skills/other/SKILL.md", "# other", 0o644)
			},
			want: []string{"skill_name_collision:repo-1:.claude/skills/plan:unresolved"},
		},
		"peer repository instructions": {
			prepare: func(t *testing.T, input *compositionInput) {
				peer := t.TempDir()
				writeFixtureFile(t, peer, "CLAUDE.md", "peer", 0o644)
				writeFixtureFile(t, peer, "AGENTS.md", "peer", 0o644)
				input.Repositories = append(input.Repositories, compositionRepository{ID: "repo-2", Root: peer})
			},
			want: []string{
				"peer_repository_instructions:repo-2:AGENTS.md:unresolved",
				"peer_repository_instructions:repo-2:CLAUDE.md:unresolved",
			},
		},
		"repository manifest": {
			prepare: func(t *testing.T, input *compositionInput) {
				writeFixtureFile(t, input.Repositories[0].Root, ".yourplatform/manifest.yaml", "pack: other", 0o644)
			},
			want: []string{"repository_manifest_not_applied:repo-1:.yourplatform/manifest.yaml:agent_pack"},
		},
		"host user configuration": {
			prepare: func(t *testing.T, input *compositionInput) {
				input.Isolation = v1.AgentPackCompositionIsolationDirect
				writeFixtureFile(t, input.HostHome, ".claude/settings.json", "{}", 0o600)
				writeFixtureFile(t, input.HostHome, ".claude/CLAUDE.md", "operator memory", 0o600)
				if err := os.MkdirAll(filepath.Join(input.HostHome, ".claude", "plugins"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{
				"host_user_configuration_loaded::host_home/.claude/CLAUDE.md:unresolved",
				"host_user_configuration_loaded::host_home/.claude/plugins:unresolved",
				"host_user_configuration_loaded::host_home/.claude/settings.json:unresolved",
			},
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			input := compositionInputFor(t, t.TempDir())
			test.prepare(t, &input)
			body, err := buildAgentPackComposition(input)
			if err != nil {
				t.Fatal(err)
			}
			compositionEventFor(t, body, v1.AgentPackCompositionPhaseBeforeStart, v1.AgentPackCompositionPackViewVerified)
			if got := conflictCodes(body); strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Fatalf("conflicts:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(test.want, "\n"))
			}
		})
	}
}

func TestCompositionReportsPeerInstructionsWithoutClaimingThemLoaded(t *testing.T) {
	peer := t.TempDir()
	writeFixtureFile(t, peer, "CLAUDE.md", "peer", 0o644)
	body, err := buildAgentPackComposition(compositionInputFor(t, t.TempDir(), peer))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{v1.AgentPackCompositionLayerRepository, v1.AgentPackCompositionLayerNested} {
		layer := layerByName(t, body, name)
		if layer.Status != v1.AgentPackCompositionAbsent || len(layer.Sources) != 0 {
			t.Fatalf("%s claims peer instructions: %+v", name, layer)
		}
	}
}

func TestCompositionHostConfigurationUnderDirectIsolation(t *testing.T) {
	input := compositionInputFor(t, t.TempDir())
	input.Isolation = v1.AgentPackCompositionIsolationDirect
	writeFixtureFile(t, input.HostHome, ".claude/settings.json", "{}", 0o600)
	body, err := buildAgentPackComposition(input)
	if err != nil {
		t.Fatal(err)
	}
	host := layerByName(t, body, v1.AgentPackCompositionLayerHostUser)
	if host.Status != v1.AgentPackCompositionAppliedUnisolated || len(host.Sources) != 1 ||
		host.Sources[0].Path != "host_home/.claude/settings.json" || host.Sources[0].DigestSHA256 != "" {
		t.Fatalf("host layer = %+v", host)
	}

	clean := compositionInputFor(t, t.TempDir())
	clean.Isolation = v1.AgentPackCompositionIsolationDirect
	body, err = buildAgentPackComposition(clean)
	if err != nil {
		t.Fatal(err)
	}
	if host := layerByName(t, body, v1.AgentPackCompositionLayerHostUser); host.Status != v1.AgentPackCompositionAbsent || len(body.Conflicts) != 0 {
		t.Fatalf("clean host layer = %+v conflicts = %v", host, conflictCodes(body))
	}

	codex := compositionInputFor(t, t.TempDir())
	codex.Adapter = "codex"
	codex.Isolation = v1.AgentPackCompositionIsolationDirect
	writeFixtureFile(t, codex.HostHome, ".claude/settings.json", "{}", 0o600)
	writeFixtureFile(t, codex.Repositories[0].Root, ".mcp.json", "{}", 0o644)
	writeFixtureFile(t, codex.Repositories[0].Root, "AGENTS.md", "codex instructions", 0o644)
	body, err = buildAgentPackComposition(codex)
	if err != nil {
		t.Fatal(err)
	}
	compositionEventFor(t, body, v1.AgentPackCompositionPhaseBeforeStart, v1.AgentPackCompositionPackViewVerified)
	if host := layerByName(t, body, v1.AgentPackCompositionLayerHostUser); host.Status != v1.AgentPackCompositionNotComposed {
		t.Fatalf("codex host layer = %+v", host)
	}
	if repository := layerByName(t, body, v1.AgentPackCompositionLayerRepository); len(repository.Sources) != 1 || repository.Sources[0].Path != "AGENTS.md" {
		t.Fatalf("codex repository layer = %+v", repository)
	}
	if len(body.Conflicts) != 0 {
		t.Fatalf("claude conflict rules applied to codex: %v", conflictCodes(body))
	}
}

func TestCompositionWithoutPackAndAfterExitPackView(t *testing.T) {
	input := compositionInputFor(t, t.TempDir())
	input.Pack = nil
	input.PackFiles = nil
	input.PluginLoaded = false
	body, err := buildAgentPackComposition(input)
	if err != nil {
		t.Fatal(err)
	}
	event := compositionEventFor(t, body, v1.AgentPackCompositionPhaseBeforeStart, v1.AgentPackCompositionPackViewAbsent)
	if event.AgentPack != nil || event.Layers[1].Status != v1.AgentPackCompositionAbsent {
		t.Fatalf("event without pack = %+v", event)
	}

	withPack, err := buildAgentPackComposition(compositionInputFor(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	after := compositionEventFor(t, withPack, v1.AgentPackCompositionPhaseAfterExit, v1.AgentPackCompositionPackViewModified)
	if after.PackView != v1.AgentPackCompositionPackViewModified || after.Phase != v1.AgentPackCompositionPhaseAfterExit {
		t.Fatalf("after exit = %+v", after)
	}
}

func TestCompositionReportIsBoundedAndMarkedTruncated(t *testing.T) {
	primary := t.TempDir()
	for index := 0; index < 70; index++ {
		writeFixtureFile(t, primary, fmt.Sprintf("module-%02d/CLAUDE.md", index), "nested", 0o644)
		writeFixtureFile(t, primary, fmt.Sprintf("module-%02d/.mcp.json", index), "{}", 0o644)
	}
	body, err := buildAgentPackComposition(compositionInputFor(t, primary))
	if err != nil {
		t.Fatal(err)
	}
	compositionEventFor(t, body, v1.AgentPackCompositionPhaseBeforeStart, v1.AgentPackCompositionPackViewVerified)
	nested := layerByName(t, body, v1.AgentPackCompositionLayerNested)
	if !body.Truncated || len(nested.Sources) != v1.MaxAgentPackCompositionSourcesPerLayer || len(body.Conflicts) != v1.MaxAgentPackCompositionConflicts {
		t.Fatalf("truncated=%v nested=%d conflicts=%d", body.Truncated, len(nested.Sources), len(body.Conflicts))
	}
}

func TestCompositionEventIDIsStableAndBounded(t *testing.T) {
	first := agentPackCompositionEventID("attempt-1", "lease-1", 7, v1.AgentPackCompositionPhaseBeforeStart)
	if first != agentPackCompositionEventID("attempt-1", "lease-1", 7, v1.AgentPackCompositionPhaseBeforeStart) {
		t.Fatal("event id is not stable")
	}
	if first == agentPackCompositionEventID("attempt-1", "lease-1", 7, v1.AgentPackCompositionPhaseAfterExit) ||
		first == agentPackCompositionEventID("attempt-1", "lease-1", 8, v1.AgentPackCompositionPhaseBeforeStart) {
		t.Fatal("event id ignores phase or fence")
	}
	long := agentPackCompositionEventID(strings.Repeat("a", 128), strings.Repeat("l", 128), 7, v1.AgentPackCompositionPhaseAfterExit)
	if err := validation.ID("event_id", long); err != nil {
		t.Fatal(err)
	}
}
