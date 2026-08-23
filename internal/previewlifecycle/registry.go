package previewlifecycle

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/validation"
)

type Dependencies struct {
	Now func() time.Time
}

type Registry struct {
	controllers  map[string]controller
	capabilities []v1.PreviewLifecycleProfileCapability
	dependencies Dependencies
}

func NewRegistry(values map[string]config.PreviewLifecycleControllerConfig, dependencies Dependencies) (*Registry, error) {
	controllers := make(map[string]controller, len(values))
	for id, value := range values {
		compiled, err := compileController(value)
		if err != nil {
			return nil, fmt.Errorf("initialize preview lifecycle controller %s: %w", id, err)
		}
		key := controllerKey(compiled.capability.BlueprintID, compiled.capability.Version, compiled.capability.Digest)
		if _, exists := controllers[key]; exists {
			return nil, fmt.Errorf("preview lifecycle controller capability identity is ambiguous")
		}
		controllers[key] = compiled
	}
	capabilities := make([]v1.PreviewLifecycleProfileCapability, 0, len(controllers))
	for _, compiled := range controllers {
		capabilities = append(capabilities, cloneCapability(compiled.capability))
	}
	sort.Slice(capabilities, func(left, right int) bool {
		return controllerKey(capabilities[left].BlueprintID, capabilities[left].Version, capabilities[left].Digest) <
			controllerKey(capabilities[right].BlueprintID, capabilities[right].Version, capabilities[right].Digest)
	})
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	return &Registry{controllers: controllers, capabilities: capabilities, dependencies: dependencies}, nil
}

func (registry *Registry) ReadyProfiles() []v1.PreviewLifecycleProfileCapability {
	result := make([]v1.PreviewLifecycleProfileCapability, 0, len(registry.capabilities))
	for _, capability := range registry.capabilities {
		compiled := registry.controllers[controllerKey(capability.BlueprintID, capability.Version, capability.Digest)]
		if _, err := compiled.withCurrentSecrets(); err == nil {
			result = append(result, cloneCapability(capability))
		}
	}
	return result
}

func (registry *Registry) controllerFor(operation v1.PreviewLifecycleOperation) (controller, bool) {
	key := controllerKey(operation.Blueprint.BlueprintID, operation.Blueprint.Version, operation.Blueprint.Digest)
	compiled, ok := registry.controllers[key]
	if !ok || !hasAction(compiled.capability.Actions, operation.Action) {
		return controller{}, false
	}
	if operation.Action == v1.PreviewLifecycleExtend {
		if operation.DesiredExpiresAt == nil || compiled.capability.MaxExtensionMS == nil {
			return controller{}, false
		}
		extensionMS := operation.DesiredExpiresAt.Sub(operation.CurrentEffectiveExpiresAt) / time.Millisecond
		if *compiled.capability.MaxExtensionMS > uint64(math.MaxInt64) {
			return controller{}, false
		}
		// #nosec G115 -- the explicit MaxInt64 guard makes this conversion lossless.
		maxExtensionMS := int64(*compiled.capability.MaxExtensionMS)
		if extensionMS <= 0 || int64(extensionMS) > maxExtensionMS {
			return controller{}, false
		}
	}
	current, err := compiled.withCurrentSecrets()
	if err != nil {
		return controller{}, false
	}
	return current, true
}

func (registry *Registry) Apply(ctx context.Context, operation v1.PreviewLifecycleOperation) v1.PreviewLifecycleResultOutcome {
	if err := validation.PreviewLifecycleOperation(operation); err != nil {
		return permanent(v1.PreviewLifecycleProfileUnavailable)
	}
	compiled, ok := registry.controllerFor(operation)
	if !ok {
		return permanent(v1.PreviewLifecycleProfileUnavailable)
	}
	return registry.apply(ctx, compiled, operation)
}

func permanent(code v1.PreviewLifecycleErrorCode) v1.PreviewLifecycleResultOutcome {
	return v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecyclePermanent, Code: code}
}

func retryable(code v1.PreviewLifecycleErrorCode, retryAfterMS *uint64) v1.PreviewLifecycleResultOutcome {
	return v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecycleRetryable, Code: code, RetryAfterMS: retryAfterMS}
}

func uncertain(code v1.PreviewLifecycleErrorCode) v1.PreviewLifecycleResultOutcome {
	return v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecycleUncertain, Code: code}
}
