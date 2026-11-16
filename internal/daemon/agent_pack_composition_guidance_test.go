package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func guidanceLayerFor(name v1.AgentGuidanceLayerName, revision int, content string) v1.AgentGuidanceLayer {
	sum := sha256.Sum256([]byte(content))
	return v1.AgentGuidanceLayer{Layer: name, Revision: revision, DigestSHA256: hex.EncodeToString(sum[:]), Content: content}
}

func TestCompositionReportListsDeliveredGuidanceLayersBetweenThePackAndRepositoryInstructions(t *testing.T) {
	organization := guidanceLayerFor(v1.AgentGuidanceLayerOrganization, 3, "Prefer small pull requests.")
	project := guidanceLayerFor(v1.AgentGuidanceLayerProject, 1, "Run the focused tests.")
	input := compositionInputFor(t, t.TempDir())
	input.Guidance = &v1.AgentGuidance{SchemaVersion: v1.AgentGuidanceSchemaVersion, Layers: []v1.AgentGuidanceLayer{organization, project}}
	body, err := buildAgentPackComposition(input)
	if err != nil {
		t.Fatal(err)
	}
	event := compositionEventFor(t, body, v1.AgentPackCompositionPhaseBeforeStart, v1.AgentPackCompositionPackViewVerified)
	if event.SchemaVersion != 2 {
		t.Fatalf("schema version = %d", event.SchemaVersion)
	}
	names := make([]string, 0, len(event.Layers))
	for _, layer := range event.Layers {
		names = append(names, layer.Layer)
	}
	want := []string{
		"platform_safety", "agent_pack", "organization_guidance", "project_guidance", "repository_instructions",
		"nested_instructions", "execution_manifest", "ticket_context", "host_user_configuration",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("layers = %v", names)
	}
	for _, check := range []struct {
		layer string
		path  string
		guide v1.AgentGuidanceLayer
	}{
		{v1.AgentPackCompositionLayerOrganizationGuidance, "agent_guidance/organization/revision-3", organization},
		{v1.AgentPackCompositionLayerProjectGuidance, "agent_guidance/project/revision-1", project},
	} {
		layer := layerByName(t, body, check.layer)
		wantSources := []v1.AgentPackCompositionSource{{
			Path: check.path, SourceType: v1.AgentConfigDevelopmentInstruction, DigestSHA256: check.guide.DigestSHA256,
		}}
		if layer.Status != v1.AgentPackCompositionApplied || !reflect.DeepEqual(layer.Sources, wantSources) {
			t.Fatalf("%s = %+v", check.layer, layer)
		}
	}
}

func TestCompositionReportMarksGuidanceLayersAbsentWhenTheJobCarriesNone(t *testing.T) {
	for _, guidance := range []*v1.AgentGuidance{
		nil,
		{SchemaVersion: v1.AgentGuidanceSchemaVersion, Layers: []v1.AgentGuidanceLayer{guidanceLayerFor(v1.AgentGuidanceLayerProject, 2, "Project only.")}},
	} {
		input := compositionInputFor(t, t.TempDir())
		input.Guidance = guidance
		body, err := buildAgentPackComposition(input)
		if err != nil {
			t.Fatal(err)
		}
		compositionEventFor(t, body, v1.AgentPackCompositionPhaseBeforeStart, v1.AgentPackCompositionPackViewVerified)
		organization := layerByName(t, body, v1.AgentPackCompositionLayerOrganizationGuidance)
		if organization.Status != v1.AgentPackCompositionAbsent || len(organization.Sources) != 0 {
			t.Fatalf("organization guidance = %+v", organization)
		}
		project := layerByName(t, body, v1.AgentPackCompositionLayerProjectGuidance)
		if guidance == nil && (project.Status != v1.AgentPackCompositionAbsent || len(project.Sources) != 0) {
			t.Fatalf("project guidance without a guidance block = %+v", project)
		}
		if guidance != nil && (project.Status != v1.AgentPackCompositionApplied || project.Sources[0].Path != "agent_guidance/project/revision-2") {
			t.Fatalf("project guidance = %+v", project)
		}
	}
}
