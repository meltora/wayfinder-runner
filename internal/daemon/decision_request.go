package daemon

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultDecisionTimeout  = 4 * time.Hour
	maxDecisionTimeout      = 4 * time.Hour
	maxDecisionOptions      = 32
	maxDecisionOptionBytes  = 4 << 10
	maxDecisionContextBytes = 16 << 10
)

func decisionRequestParameters(raw map[string]any) ([]string, time.Duration, error) {
	question := str(raw, "question")
	if strings.TrimSpace(question) == "" {
		return nil, 0, fmt.Errorf("question is required")
	}
	for _, field := range []string{"recommendation", "consequence_of_delay", "blocked_operation"} {
		value, exists := raw[field]
		if !exists {
			continue
		}
		text, ok := value.(string)
		if !ok || len(text) > maxDecisionContextBytes || !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') {
			return nil, 0, fmt.Errorf("%s must be valid UTF-8 and at most %d bytes", field, maxDecisionContextBytes)
		}
	}

	options, err := decisionOptions(raw["options"])
	if err != nil {
		return nil, 0, err
	}
	timeout := defaultDecisionTimeout
	if value, exists := raw["timeout_seconds"]; exists {
		seconds, ok := value.(float64)
		if !ok || seconds <= 0 || seconds != math.Trunc(seconds) || seconds > maxDecisionTimeout.Seconds() {
			return nil, 0, fmt.Errorf("timeout_seconds must be an integer from 1 to %d", int(maxDecisionTimeout.Seconds()))
		}
		timeout = time.Duration(seconds) * time.Second
	}
	return options, timeout, nil
}

func decisionOptions(value any) ([]string, error) {
	if value == nil {
		return []string{}, nil
	}
	rawOptions, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("options must be an array of strings")
	}
	if len(rawOptions) > maxDecisionOptions {
		return nil, fmt.Errorf("options cannot contain more than %d entries", maxDecisionOptions)
	}
	options := make([]string, 0, len(rawOptions))
	seen := make(map[string]struct{}, len(rawOptions))
	for _, rawOption := range rawOptions {
		option, ok := rawOption.(string)
		if !ok || strings.TrimSpace(option) == "" || len(option) > maxDecisionOptionBytes ||
			!utf8.ValidString(option) || strings.ContainsRune(option, '\x00') {
			return nil, fmt.Errorf("each decision option must be non-empty valid UTF-8 and at most %d bytes", maxDecisionOptionBytes)
		}
		if _, duplicate := seen[option]; duplicate {
			return nil, fmt.Errorf("decision options must be unique")
		}
		seen[option] = struct{}{}
		options = append(options, option)
	}
	return options, nil
}
