package daemon

import (
	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

const maxPreviewHostingExtensionMS = uint64(config.MaxPreviewHostingExtensionSeconds) * 1000

var previewHostingActions = map[v1.PreviewLifecycleAction]bool{
	"deploy":                   true,
	v1.PreviewLifecycleExtend:  true,
	v1.PreviewLifecycleDestroy: true,
	"observe":                  true,
}

func validPreviewHostingCapability(capability v1.PreviewHostingCapability) bool {
	if capability.ProtocolVersion != v1.PreviewHostingProtocolVersion {
		return false
	}
	if capability.MaxConcurrent <= 0 || capability.MaxConcurrent > config.MaxPreviewHostingConcurrent {
		return false
	}
	return validPreviewHostingActions(capability.Actions) && validPreviewHostingExtension(capability.MaxExtensionMS)
}

func validPreviewHostingActions(actions []v1.PreviewLifecycleAction) bool {
	if len(actions) == 0 {
		return false
	}
	seen := make(map[v1.PreviewLifecycleAction]bool, len(actions))
	for _, action := range actions {
		if !previewHostingActions[action] || seen[action] {
			return false
		}
		seen[action] = true
	}
	return true
}

func validPreviewHostingExtension(extension *uint64) bool {
	return extension == nil || (*extension > 0 && *extension <= maxPreviewHostingExtensionMS)
}

func (d *Daemon) previewHostingCapability() *v1.PreviewHostingCapability {
	if d.previewHostController == nil {
		return nil
	}
	hosting := d.previewHostController.Capability()
	if !validPreviewHostingCapability(hosting) {
		d.logger.Warn("omit preview hosting capability: invalid capability", "protocol_version", hosting.ProtocolVersion, "max_concurrent", hosting.MaxConcurrent, "actions", hosting.Actions)
		return nil
	}
	return &hosting
}
