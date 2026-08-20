package previewobserver

import (
	"fmt"
	"sort"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

type Registry struct {
	profiles     map[string]profile
	capabilities []v1.PreviewObservationProfileCapability
	dependencies Dependencies
}

func NewRegistry(
	values map[string]config.PreviewObservationProfileConfig,
	dependencies Dependencies,
) (*Registry, error) {
	profiles := make(map[string]profile, len(values))
	for id, value := range values {
		compiled, err := compileProfile(value)
		if err != nil {
			return nil, fmt.Errorf("initialize preview observation profile %s: %w", id, err)
		}
		key := capabilityKey(compiled.capability.BlueprintID, compiled.capability.Version, compiled.capability.Digest)
		if _, exists := profiles[key]; exists {
			return nil, fmt.Errorf("preview observation profile capability identity is ambiguous")
		}
		profiles[key] = compiled
	}
	capabilities := make([]v1.PreviewObservationProfileCapability, 0, len(profiles))
	for _, compiled := range profiles {
		capabilities = append(capabilities, cloneCapability(compiled.capability))
	}
	sort.Slice(capabilities, func(left, right int) bool {
		return capabilityKey(capabilities[left].BlueprintID, capabilities[left].Version, capabilities[left].Digest) <
			capabilityKey(capabilities[right].BlueprintID, capabilities[right].Version, capabilities[right].Digest)
	})
	return &Registry{
		profiles: profiles, capabilities: capabilities,
		dependencies: normalizeDependencies(dependencies),
	}, nil
}

func (registry *Registry) ReadyProfiles() []v1.PreviewObservationProfileCapability {
	result := make([]v1.PreviewObservationProfileCapability, 0, len(registry.capabilities))
	for _, capability := range registry.capabilities {
		compiled := registry.profiles[capabilityKey(capability.BlueprintID, capability.Version, capability.Digest)]
		if _, err := compiled.withCurrentSecrets(); err == nil {
			result = append(result, cloneCapability(capability))
		}
	}
	return result
}

func (registry *Registry) profileFor(operation v1.PreviewObservationOperation) (profile, bool) {
	key := capabilityKey(operation.Blueprint.BlueprintID, operation.Blueprint.Version, operation.Blueprint.Digest)
	compiled, ok := registry.profiles[key]
	if !ok || len(operation.RequiredHealthCheckIDs) != len(compiled.capability.HealthCheckIDs) {
		return profile{}, false
	}
	for index, checkID := range operation.RequiredHealthCheckIDs {
		if checkID != compiled.capability.HealthCheckIDs[index] {
			return profile{}, false
		}
	}
	current, err := compiled.withCurrentSecrets()
	if err != nil {
		return profile{}, false
	}
	return current, true
}
