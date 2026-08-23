package config

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"
)

const (
	MaxPreviewLifecycleControllers          = 64
	defaultPreviewLifecycleTimeoutSeconds   = 20
	maxPreviewLifecycleTimeoutSeconds       = 120
	defaultPreviewLifecycleMaxResponseBytes = 64 << 10
	maxPreviewLifecycleMaxResponseBytes     = 1 << 20
	defaultPreviewLifecycleMaxExtensionMS   = uint64(30 * 24 * 60 * 60 * 1000)
)

type PreviewLifecycleControllerConfig struct {
	BlueprintID       string   `yaml:"blueprint_id"`
	Version           string   `yaml:"version"`
	Digest            string   `yaml:"digest"`
	ControllerURL     string   `yaml:"controller_url"`
	TrustedCAFile     string   `yaml:"trusted_ca_file,omitempty"`
	AuthorizationFile string   `yaml:"authorization_file,omitempty"`
	Actions           []string `yaml:"actions,omitempty"`
	TimeoutSeconds    int      `yaml:"timeout_seconds,omitempty"`
	MaxResponseBytes  int      `yaml:"max_response_bytes,omitempty"`
	MaxExtensionMS    uint64   `yaml:"max_extension_ms,omitempty"`
}

func normalizePreviewLifecycleControllers(
	controllers map[string]PreviewLifecycleControllerConfig,
) map[string]PreviewLifecycleControllerConfig {
	if controllers == nil {
		return nil
	}
	result := make(map[string]PreviewLifecycleControllerConfig, len(controllers))
	for id, controller := range controllers {
		copyValue := controller
		copyValue.Actions = append([]string(nil), controller.Actions...)
		if len(copyValue.Actions) == 0 {
			copyValue.Actions = []string{"destroy", "extend"}
		}
		if copyValue.TimeoutSeconds == 0 {
			copyValue.TimeoutSeconds = defaultPreviewLifecycleTimeoutSeconds
		}
		if copyValue.MaxResponseBytes == 0 {
			copyValue.MaxResponseBytes = defaultPreviewLifecycleMaxResponseBytes
		}
		if copyValue.MaxExtensionMS == 0 && previewLifecycleActionPresent(copyValue.Actions, "extend") {
			copyValue.MaxExtensionMS = defaultPreviewLifecycleMaxExtensionMS
		}
		result[id] = copyValue
	}
	return result
}

func validatePreviewLifecycleControllers(controllers map[string]PreviewLifecycleControllerConfig) error {
	if len(controllers) > MaxPreviewLifecycleControllers {
		return fmt.Errorf("preview_lifecycle_controllers must contain at most %d entries", MaxPreviewLifecycleControllers)
	}
	identities := make(map[string]string, len(controllers))
	for id, controller := range controllers {
		if !validConfigID(id) {
			return fmt.Errorf("preview lifecycle controller ID %q is invalid", id)
		}
		if err := validatePreviewLifecycleController(id, controller); err != nil {
			return err
		}
		identity := controller.BlueprintID + "\x00" + controller.Version + "\x00" + controller.Digest
		if previous, exists := identities[identity]; exists {
			return fmt.Errorf("preview lifecycle controllers %s and %s have ambiguous blueprint identity", previous, id)
		}
		identities[identity] = id
	}
	return nil
}

func validatePreviewLifecycleController(id string, controller PreviewLifecycleControllerConfig) error {
	if !validConfigID(controller.BlueprintID) {
		return fmt.Errorf("preview lifecycle controller %s blueprint_id is invalid", id)
	}
	if strings.TrimSpace(controller.Version) == "" || len(controller.Version) > 128 ||
		strings.ContainsAny(controller.Version, "\x00\r\n") {
		return fmt.Errorf("preview lifecycle controller %s version is invalid", id)
	}
	if !lowerSHA256Pattern.MatchString(controller.Digest) {
		return fmt.Errorf("preview lifecycle controller %s digest is invalid", id)
	}
	if err := validatePreviewLifecycleControllerURL(controller.ControllerURL); err != nil {
		return fmt.Errorf("preview lifecycle controller %s controller_url: %w", id, err)
	}
	for field, value := range map[string]string{
		"trusted_ca_file": controller.TrustedCAFile, "authorization_file": controller.AuthorizationFile,
	} {
		if value != "" && (!filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsRune(value, '\x00')) {
			return fmt.Errorf("preview lifecycle controller %s %s must be a clean absolute path", id, field)
		}
	}
	if len(controller.Actions) < 1 || len(controller.Actions) > 2 {
		return fmt.Errorf("preview lifecycle controller %s actions are invalid", id)
	}
	previous := ""
	for _, action := range controller.Actions {
		if (action != "destroy" && action != "extend") || action <= previous {
			return fmt.Errorf("preview lifecycle controller %s actions must be supported, unique, and sorted", id)
		}
		previous = action
	}
	if controller.TimeoutSeconds < 1 || controller.TimeoutSeconds > maxPreviewLifecycleTimeoutSeconds {
		return fmt.Errorf("preview lifecycle controller %s timeout_seconds is invalid", id)
	}
	if controller.MaxResponseBytes < 1 || controller.MaxResponseBytes > maxPreviewLifecycleMaxResponseBytes {
		return fmt.Errorf("preview lifecycle controller %s max_response_bytes is invalid", id)
	}
	hasExtend := previewLifecycleActionPresent(controller.Actions, "extend")
	if (hasExtend && (controller.MaxExtensionMS < 1 || controller.MaxExtensionMS > defaultPreviewLifecycleMaxExtensionMS)) ||
		(!hasExtend && controller.MaxExtensionMS != 0) {
		return fmt.Errorf("preview lifecycle controller %s max_extension_ms is invalid", id)
	}
	return nil
}

func previewLifecycleActionPresent(actions []string, expected string) bool {
	for _, action := range actions {
		if action == expected {
			return true
		}
	}
	return false
}

func validatePreviewLifecycleControllerURL(raw string) error {
	if err := validateServiceURL(raw, "https", "", false); err != nil {
		return err
	}
	parsed, _ := url.Parse(raw)
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.RawPath != "" || parsed.Host != strings.ToLower(parsed.Host) || strings.HasSuffix(parsed.Hostname(), ".") ||
		parsed.Port() == "443" || (parsed.Path != "" && path.Clean(parsed.Path) != parsed.Path) ||
		(parsed.Path != "" && parsed.Path != "/" && strings.HasSuffix(parsed.Path, "/")) {
		return fmt.Errorf("must be a canonical fixed HTTPS URL")
	}
	for _, value := range []byte(parsed.Host) {
		if value >= 0x80 {
			return fmt.Errorf("hostname must use canonical ASCII encoding")
		}
	}
	return nil
}
