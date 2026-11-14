package validation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func AgentGuidance(value v1.AgentGuidance) error {
	if value.SchemaVersion != v1.AgentGuidanceSchemaVersion {
		return fmt.Errorf("agent_guidance schema_version is unsupported")
	}
	if len(value.Layers) < 1 || len(value.Layers) > v1.MaxAgentGuidanceLayers {
		return fmt.Errorf("agent_guidance layers are out of range")
	}
	if len(value.Layers) == 2 && (value.Layers[0].Layer != v1.AgentGuidanceLayerOrganization || value.Layers[1].Layer != v1.AgentGuidanceLayerProject) {
		return fmt.Errorf("agent_guidance layers must be organization then project")
	}
	for _, layer := range value.Layers {
		if err := agentGuidanceLayer(layer); err != nil {
			return err
		}
	}
	return nil
}

func agentGuidanceLayer(layer v1.AgentGuidanceLayer) error {
	switch layer.Layer {
	case v1.AgentGuidanceLayerOrganization, v1.AgentGuidanceLayerProject:
	default:
		return fmt.Errorf("agent_guidance layer is unsupported")
	}
	if layer.Revision < 1 {
		return fmt.Errorf("agent_guidance revision is invalid")
	}
	if len(layer.Content) > v1.MaxAgentGuidanceContentBytes || !utf8.ValidString(layer.Content) ||
		strings.ContainsRune(layer.Content, 0) || strings.TrimSpace(layer.Content) == "" {
		return fmt.Errorf("agent_guidance content is invalid")
	}
	sum := sha256.Sum256([]byte(layer.Content))
	if layer.DigestSHA256 != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("agent_guidance digest does not match content")
	}
	return nil
}
