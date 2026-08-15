package hookevent

import "fmt"

var supportedTypes = map[string]struct{}{
	"session-start":      {},
	"session-end":        {},
	"pre-tool":           {},
	"post-tool":          {},
	"tool-error":         {},
	"stop":               {},
	"subagent-stop":      {},
	"notification":       {},
	"user-prompt-submit": {},
}

// ValidateType constrains non-authoritative provider telemetry to the stable
// platform vocabulary. Provider-specific names are normalized before this
// boundary and arbitrary payloads are never accepted.
func ValidateType(name string) error {
	if _, ok := supportedTypes[name]; !ok {
		return fmt.Errorf("unsupported hook event type %q", name)
	}
	return nil
}
