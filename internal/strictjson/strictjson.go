package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func Object(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("expected a JSON object")
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("expected a JSON object")
	}
	out := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid JSON object")
		}
		name, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("invalid JSON object")
		}
		if _, exists := out[name]; exists {
			return nil, fmt.Errorf("duplicate field %q", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("invalid JSON value for %q", name)
		}
		out[name] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, fmt.Errorf("invalid JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("trailing data after JSON object")
	}
	return out, nil
}

func Array(raw json.RawMessage) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, fmt.Errorf("expected a JSON array")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil || items == nil {
		return nil, fmt.Errorf("expected a JSON array")
	}
	return items, nil
}

func ExactFields(fields map[string]json.RawMessage, allowed, required []string) error {
	allow := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allow[name] = true
	}
	for name := range fields {
		if !allow[name] {
			return fmt.Errorf("unexpected field %q", name)
		}
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("missing field %q", name)
		}
	}
	return nil
}

func String(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", fmt.Errorf("expected a JSON string")
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return "", fmt.Errorf("expected a JSON string")
	}
	return value, nil
}

func Number(raw json.RawMessage) (json.Number, error) {
	trimmed := bytes.TrimSpace(raw)
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return "", fmt.Errorf("expected a JSON number")
	}
	number, ok := token.(json.Number)
	if !ok {
		return "", fmt.Errorf("expected a JSON number")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("expected a JSON number")
	}
	return number, nil
}
