package validation

import (
	"fmt"

	v1 "github.com/example/app-runner/api/runner/v1"
)

var agentPackCompositionStatuses = map[string]bool{
	v1.AgentPackCompositionApplied:           true,
	v1.AgentPackCompositionExcluded:          true,
	v1.AgentPackCompositionAbsent:            true,
	v1.AgentPackCompositionNotComposed:       true,
	v1.AgentPackCompositionAppliedUnisolated: true,
}

var agentPackCompositionAdapters = map[string]bool{"claude": true, "codex": true, "opencode": true}

func AgentPackCompositionEvent(value v1.AgentPackCompositionEvent) error {
	if value.Kind != v1.EventKindAgentPackComposition {
		return fmt.Errorf("agent pack composition kind is invalid")
	}
	if value.ManifestKind != v1.JobKindPlan && value.ManifestKind != v1.JobKindBuild {
		return fmt.Errorf("manifest kind is invalid")
	}
	for field, id := range map[string]string{"lease_id": value.LeaseID, "job_id": value.JobID, "attempt_id": value.AttemptID, "manifest_id": value.ManifestID} {
		if err := ID(field, id); err != nil {
			return err
		}
	}
	if err := SHA256Digest("manifest_digest", value.ManifestDigest); err != nil {
		return err
	}
	if value.FencingToken == 0 || value.FencingToken > maxJSONSafeInteger || value.ObservedAt.IsZero() {
		return fmt.Errorf("manifest event fence or observation time is invalid")
	}
	if value.AgentRunAuthority != nil {
		if err := AgentRunAuthority(*value.AgentRunAuthority); err != nil {
			return err
		}
	}
	if value.SchemaVersion != v1.AgentPackCompositionSchemaVersion {
		return fmt.Errorf("agent pack composition schema_version is invalid")
	}
	if value.Phase != v1.AgentPackCompositionPhaseBeforeStart && value.Phase != v1.AgentPackCompositionPhaseAfterExit {
		return fmt.Errorf("agent pack composition phase is invalid")
	}
	if !agentPackCompositionAdapters[value.Adapter] {
		return fmt.Errorf("agent pack composition adapter is invalid")
	}
	if value.Isolation != v1.AgentPackCompositionIsolationDocker && value.Isolation != v1.AgentPackCompositionIsolationDirect {
		return fmt.Errorf("agent pack composition isolation is invalid")
	}
	if err := agentPackCompositionPack(value); err != nil {
		return err
	}
	if len(value.Layers) != len(v1.AgentPackCompositionLayerOrder) {
		return fmt.Errorf("agent pack composition must list every layer once")
	}
	for index, layer := range value.Layers {
		if err := agentPackCompositionLayer(index, layer); err != nil {
			return err
		}
	}
	if (value.AgentPack == nil) != (value.Layers[1].Status == v1.AgentPackCompositionAbsent) {
		return fmt.Errorf("agent pack layer must be absent exactly when no pack is bound")
	}
	if value.Conflicts == nil || len(value.Conflicts) > v1.MaxAgentPackCompositionConflicts {
		return fmt.Errorf("agent pack composition conflicts are invalid")
	}
	for index, conflict := range value.Conflicts {
		if err := agentPackCompositionConflict(index, conflict); err != nil {
			return err
		}
	}
	return nil
}

func agentPackCompositionPack(value v1.AgentPackCompositionEvent) error {
	switch value.PackView {
	case v1.AgentPackCompositionPackViewVerified, v1.AgentPackCompositionPackViewModified, v1.AgentPackCompositionPackViewAbsent:
	default:
		return fmt.Errorf("agent pack composition pack_view is invalid")
	}
	if (value.AgentPack == nil) != (value.PackView == v1.AgentPackCompositionPackViewAbsent) {
		return fmt.Errorf("pack_view must be absent exactly when no pack is bound")
	}
	if value.Phase == v1.AgentPackCompositionPhaseBeforeStart && value.PackView == v1.AgentPackCompositionPackViewModified {
		return fmt.Errorf("a modified pack view cannot be reported before start")
	}
	if value.AgentPack == nil {
		return nil
	}
	if err := boundedText("agent_pack.version", value.AgentPack.Version, 128, true); err != nil {
		return err
	}
	if err := SHA256Digest("agent_pack.digest", value.AgentPack.Digest); err != nil {
		return err
	}
	return ID("agent_pack.key_id", value.AgentPack.KeyID)
}

func agentPackCompositionLayer(index int, layer v1.AgentPackCompositionLayer) error {
	prefix := fmt.Sprintf("layers[%d]", index)
	if layer.Layer != v1.AgentPackCompositionLayerOrder[index] {
		return fmt.Errorf("%s is out of precedence order", prefix)
	}
	if !agentPackCompositionStatuses[layer.Status] {
		return fmt.Errorf("%s.status is invalid", prefix)
	}
	if layer.Sources == nil || len(layer.Sources) > v1.MaxAgentPackCompositionSourcesPerLayer {
		return fmt.Errorf("%s.sources are invalid", prefix)
	}
	for sourceIndex, source := range layer.Sources {
		field := fmt.Sprintf("%s.sources[%d]", prefix, sourceIndex)
		if !validAgentConfigSourceTypes[source.SourceType] {
			return fmt.Errorf("%s.source_type is invalid", field)
		}
		if err := optionalCompositionLocation(field, source.RepositoryID, source.Path); err != nil {
			return err
		}
		if source.DigestSHA256 != "" {
			if err := SHA256Digest(field+".digest_sha256", source.DigestSHA256); err != nil {
				return err
			}
		}
	}
	return nil
}

func agentPackCompositionConflict(index int, conflict v1.AgentPackCompositionConflict) error {
	field := fmt.Sprintf("conflicts[%d]", index)
	winner, known := v1.AgentPackCompositionConflictWinners[conflict.Code]
	if !known {
		return fmt.Errorf("%s.code is invalid", field)
	}
	if conflict.Winner != winner {
		return fmt.Errorf("%s.winner does not match the conflict rule", field)
	}
	validLayer := false
	for _, layer := range v1.AgentPackCompositionLayerOrder {
		validLayer = validLayer || layer == conflict.Layer
	}
	if !validLayer {
		return fmt.Errorf("%s.layer is invalid", field)
	}
	return optionalCompositionLocation(field, conflict.RepositoryID, conflict.Path)
}

func optionalCompositionLocation(field, repositoryID, path string) error {
	if repositoryID != "" {
		if err := ID(field+".repository_id", repositoryID); err != nil {
			return err
		}
	}
	if path != "" {
		return repositoryPath(field+".path", path)
	}
	return nil
}
