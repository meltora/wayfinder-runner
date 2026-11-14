package validation

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func guidanceLayer(layer v1.AgentGuidanceLayerName, content string) v1.AgentGuidanceLayer {
	sum := sha256.Sum256([]byte(content))
	return v1.AgentGuidanceLayer{Layer: layer, Revision: 2, DigestSHA256: hex.EncodeToString(sum[:]), Content: content}
}

func validAgentGuidance() v1.AgentGuidance {
	return v1.AgentGuidance{SchemaVersion: 1, Layers: []v1.AgentGuidanceLayer{
		guidanceLayer(v1.AgentGuidanceLayerOrganization, "Prefer small commits."),
		guidanceLayer(v1.AgentGuidanceLayerProject, "Run make lint before finishing."),
	}}
}

func TestAgentGuidanceValidation(t *testing.T) {
	if err := AgentGuidance(validAgentGuidance()); err != nil {
		t.Fatalf("valid guidance rejected: %v", err)
	}
	single := v1.AgentGuidance{SchemaVersion: 1, Layers: []v1.AgentGuidanceLayer{guidanceLayer(v1.AgentGuidanceLayerProject, "Only project.")}}
	if err := AgentGuidance(single); err != nil {
		t.Fatalf("single project layer rejected: %v", err)
	}
	for name, mutate := range map[string]func(*v1.AgentGuidance){
		"schema":    func(value *v1.AgentGuidance) { value.SchemaVersion = 2 },
		"empty":     func(value *v1.AgentGuidance) { value.Layers = nil },
		"too many":  func(value *v1.AgentGuidance) { value.Layers = append(value.Layers, value.Layers[1]) },
		"order":     func(value *v1.AgentGuidance) { value.Layers[0], value.Layers[1] = value.Layers[1], value.Layers[0] },
		"duplicate": func(value *v1.AgentGuidance) { value.Layers[1] = value.Layers[0] },
		"layer":     func(value *v1.AgentGuidance) { value.Layers = value.Layers[:1]; value.Layers[0].Layer = "team" },
		"revision":  func(value *v1.AgentGuidance) { value.Layers[0].Revision = 0 },
		"digest":    func(value *v1.AgentGuidance) { value.Layers[0].Content = "changed" },
		"blank": func(value *v1.AgentGuidance) {
			value.Layers[0] = guidanceLayer(v1.AgentGuidanceLayerOrganization, "  \n")
		},
		"nul": func(value *v1.AgentGuidance) {
			value.Layers[0] = guidanceLayer(v1.AgentGuidanceLayerOrganization, "a\x00b")
		},
		"too long": func(value *v1.AgentGuidance) {
			value.Layers[0] = guidanceLayer(v1.AgentGuidanceLayerOrganization, strings.Repeat("x", v1.MaxAgentGuidanceContentBytes+1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := validAgentGuidance()
			candidate.Layers = append([]v1.AgentGuidanceLayer(nil), candidate.Layers...)
			mutate(&candidate)
			if err := AgentGuidance(candidate); err == nil {
				t.Fatal("invalid guidance accepted")
			}
		})
	}
}

func TestJobRejectsInvalidAgentGuidance(t *testing.T) {
	job := validJob()
	guidance := validAgentGuidance()
	job.AgentGuidance = &guidance
	if err := JobSpec(job); err != nil {
		t.Fatalf("valid job rejected: %v", err)
	}
	guidance.Layers[0].Revision = 0
	if err := JobSpec(job); err == nil {
		t.Fatal("job with invalid agent guidance accepted")
	}
}
