package daemon

import (
	"fmt"
	"strings"
)

const (
	maxManualTestPlanItems        = 128
	maxManualTestPlanItemBytes    = 4096
	maxManualTestPlanCleanupBytes = 8192
)

type manualTestPlanPayload struct {
	steps           []string
	prerequisites   []string
	expectedResults []string
	cleanup         string
}

func parseManualTestPlan(raw map[string]any) (manualTestPlanPayload, error) {
	allowed := map[string]struct{}{
		"steps": {}, "prerequisites": {}, "expected_results": {}, "cleanup": {}, "idempotency_key": {},
	}
	for field := range raw {
		if _, ok := allowed[field]; !ok {
			return manualTestPlanPayload{}, fmt.Errorf("manual test plan field %q is not allowed", field)
		}
	}
	steps, err := parseManualInstructions(raw, "steps", true)
	if err != nil {
		return manualTestPlanPayload{}, err
	}
	prerequisites, err := parseManualInstructions(raw, "prerequisites", false)
	if err != nil {
		return manualTestPlanPayload{}, err
	}
	expectedResults, err := parseManualInstructions(raw, "expected_results", true)
	if err != nil {
		return manualTestPlanPayload{}, err
	}
	cleanup := str(raw, "cleanup")
	if _, exists := raw["cleanup"]; exists {
		if _, ok := raw["cleanup"].(string); !ok {
			return manualTestPlanPayload{}, fmt.Errorf("cleanup must be a string")
		}
	}
	if err := validateManualInstruction("cleanup", cleanup, maxManualTestPlanCleanupBytes, true); err != nil {
		return manualTestPlanPayload{}, err
	}
	return manualTestPlanPayload{
		steps: steps, prerequisites: prerequisites, expectedResults: expectedResults, cleanup: cleanup,
	}, nil
}

func parseManualInstructions(raw map[string]any, field string, required bool) ([]string, error) {
	value, exists := raw[field]
	if !exists {
		if required {
			return nil, fmt.Errorf("%s is required", field)
		}
		return []string{}, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", field)
	}
	if (required && len(items) == 0) || len(items) > maxManualTestPlanItems {
		return nil, fmt.Errorf("%s must contain between 1 and %d items", field, maxManualTestPlanItems)
	}
	result := make([]string, 0, len(items))
	for index, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be a string", field, index)
		}
		if err := validateManualInstruction(fmt.Sprintf("%s[%d]", field, index), text, maxManualTestPlanItemBytes, false); err != nil {
			return nil, err
		}
		result = append(result, text)
	}
	return result, nil
}

func validateManualInstruction(field, value string, maxBytes int, allowEmpty bool) error {
	if (!allowEmpty && strings.TrimSpace(value) == "") || len(value) > maxBytes {
		return fmt.Errorf("%s is blank or exceeds its size limit", field)
	}
	for _, r := range value {
		if (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || r == 0x7f {
			return fmt.Errorf("%s contains a control character", field)
		}
	}
	return nil
}
