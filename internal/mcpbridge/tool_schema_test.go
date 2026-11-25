package mcpbridge

import (
	"encoding/json"
	"fmt"
	"testing"
)

var knownSchemaTypes = map[string]bool{
	"object": true, "array": true, "string": true, "integer": true,
	"number": true, "boolean": true, "null": true,
}

func TestEveryServedToolSchemaIsValidJSONSchema(t *testing.T) {
	names := make([]string, 0, len(toolDefinitions))
	for _, definition := range toolDefinitions {
		names = append(names, definition.Name)
	}
	byNames, err := ToolDefinitionsForNames(names)
	if err != nil {
		t.Fatal(err)
	}
	for source, definitions := range map[string][]ToolDef{"ToolDefinitions": ToolDefinitions(), "ToolDefinitionsForNames": byNames} {
		if len(definitions) != len(toolDefinitions) {
			t.Fatalf("%s returned %d tools, want %d", source, len(definitions), len(toolDefinitions))
		}
		for _, definition := range definitions {
			encoded, err := json.Marshal(definition)
			if err != nil {
				t.Fatalf("%s %s: %v", source, definition.Name, err)
			}
			var decoded struct {
				InputSchema any `json:"inputSchema"`
			}
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("%s %s: %v", source, definition.Name, err)
			}
			schema, ok := decoded.InputSchema.(map[string]any)
			if !ok || schema["type"] != "object" {
				t.Fatalf("%s %s: input schema is not an object schema: %s", source, definition.Name, encoded)
			}
			if err := validateSchema(schema, "inputSchema"); err != nil {
				t.Fatalf("%s %s: %v\n%s", source, definition.Name, err, encoded)
			}
		}
	}
}

func TestDeployPreviewSchemaServesAnEmptyRequiredList(t *testing.T) {
	definitions, err := ToolDefinitionsForNames([]string{DeployPreviewTool})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(definitions[0].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"additionalProperties":false,"properties":{},"required":[],"type":"object"}`
	if string(encoded) != want {
		t.Fatalf("schema = %s, want %s", encoded, want)
	}
}

func TestCloneToolSchemaNeverProducesANullStringList(t *testing.T) {
	for _, input := range [][]string{nil, {}, {"a"}} {
		encoded, err := json.Marshal(cloneToolSchema(map[string]any{"required": input}))
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, ok := decoded["required"].([]any); !ok {
			t.Fatalf("cloned %#v = %s", input, encoded)
		}
	}
	original := []string{"a"}
	cloned := cloneToolSchema(original).([]string)
	cloned[0] = "b"
	if original[0] != "a" {
		t.Fatal("clone shares its backing array with the definition")
	}
}

func validateSchema(value any, path string) error {
	schema, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s is %T, want a schema object", path, value)
	}
	for key, nested := range schema {
		if nested == nil {
			return fmt.Errorf("%s.%s is null", path, key)
		}
	}
	if raw, present := schema["type"]; present {
		name, ok := raw.(string)
		if !ok || !knownSchemaTypes[name] {
			return fmt.Errorf("%s.type = %v", path, raw)
		}
	}
	properties := map[string]any{}
	if raw, present := schema["properties"]; present {
		properties, ok = raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.properties is %T", path, raw)
		}
		for name, nested := range properties {
			if err := validateSchema(nested, path+".properties."+name); err != nil {
				return err
			}
		}
	}
	if raw, present := schema["required"]; present {
		list, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("%s.required is %T, want an array", path, raw)
		}
		seen := map[string]bool{}
		for _, item := range list {
			name, ok := item.(string)
			if !ok || seen[name] {
				return fmt.Errorf("%s.required has an invalid or duplicate entry %v", path, item)
			}
			seen[name] = true
			if _, declared := properties[name]; !declared {
				return fmt.Errorf("%s.required names undeclared property %q", path, name)
			}
		}
	}
	if raw, present := schema["additionalProperties"]; present {
		if _, isBool := raw.(bool); !isBool {
			if err := validateSchema(raw, path+".additionalProperties"); err != nil {
				return err
			}
		}
	}
	if raw, present := schema["items"]; present {
		if err := validateSchema(raw, path+".items"); err != nil {
			return err
		}
	}
	if raw, present := schema["enum"]; present {
		list, ok := raw.([]any)
		if !ok || len(list) == 0 {
			return fmt.Errorf("%s.enum = %v, want a non-empty array", path, raw)
		}
	}
	for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
		if raw, present := schema[keyword]; present {
			list, ok := raw.([]any)
			if !ok || len(list) == 0 {
				return fmt.Errorf("%s.%s = %v, want a non-empty array", path, keyword, raw)
			}
			for index, nested := range list {
				if err := validateSchema(nested, fmt.Sprintf("%s.%s[%d]", path, keyword, index)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
