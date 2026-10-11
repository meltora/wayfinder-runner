package plancommand

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func decodeHookInput(raw []byte) (string, string, error) {
	input, err := decodeExactObject(raw, "hook input")
	if err != nil {
		return "", "", err
	}
	var toolName string
	if err := decodeExactField(input, "tool_name", &toolName); err != nil || toolName != "Bash" {
		return "", "", fmt.Errorf("the planning command guard only accepts Bash tool calls")
	}
	var cwd string
	if err := decodeExactField(input, "cwd", &cwd); err != nil {
		return "", "", fmt.Errorf("hook input has no valid cwd")
	}
	toolInputRaw, err := exactField(input, "tool_input")
	if err != nil {
		return "", "", err
	}
	toolInput, err := decodeExactObject(toolInputRaw, "tool input")
	if err != nil {
		return "", "", err
	}
	var command string
	if err := decodeExactField(toolInput, "command", &command); err != nil || command == "" {
		return "", "", fmt.Errorf("hook input has no command")
	}
	return cwd, command, nil
}

func decodeExactObject(raw []byte, label string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, fmt.Errorf("%s is not a valid JSON object", label)
	}
	if decoder.More() {
		return nil, fmt.Errorf("%s has trailing data", label)
	}
	return object, nil
}

func exactField(object map[string]json.RawMessage, name string) (json.RawMessage, error) {
	for key := range object {
		if key != name && strings.EqualFold(key, name) {
			return nil, fmt.Errorf("hook input has an ambiguous %q key", name)
		}
	}
	value, ok := object[name]
	if !ok {
		return nil, fmt.Errorf("hook input has no %q key", name)
	}
	return value, nil
}

func decodeExactField(object map[string]json.RawMessage, name string, target *string) error {
	value, err := exactField(object, name)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(value, target); err != nil {
		return fmt.Errorf("hook input %q is not a string", name)
	}
	return nil
}
